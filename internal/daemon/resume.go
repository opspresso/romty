// Saving what a stopping daemon cannot keep alive. `romty stop` kills every
// shell — that is what it is for — but the scrollback those shells printed
// and the agent conversations running in them are work the user has not
// finished. A stopping daemon writes both to disk, and the next daemon hands
// them back: the first tab opened in a workspace replays the saved output
// and, when an agent was running, finds its resume command already typed at
// the prompt, waiting for Enter.
//
// Snapshots are keyed by workspace, not by tab: the next daemon discards
// every recorded tab before it listens and issues fresh IDs, so a tab ID
// names nothing across a restart while a workspace directory still does.
//
// Closing a tab saves the same snapshot into a second store, the closed
// history. Those are not consumed by the next tab: the user picks one by ID
// when opening a workspace that has no running tab.

package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/opspresso/romty/internal/jsonfile"
	"github.com/opspresso/romty/internal/model"
)

// defaultRetention is how long an unconsumed snapshot is kept unless
// config.json sets output_retention_days. A workspace the user has not
// reopened in this long is not one they are resuming into, and replaying
// weeks-old output as if it just happened would mislead more than it helps.
// Saved output can hold whatever a terminal printed, secrets included, so it
// is not kept longer than it is useful.
const defaultRetention = 7 * 24 * time.Hour

// resumeMarker separates the restored output from the fresh shell that
// follows it, so old output is not mistaken for something that just ran.
const resumeMarker = "\r\n\x1b[0m\x1b[2m── restored from the previous romty session ──\x1b[0m\r\n"

// closedMarker does the same for a reopened closed tab, which comes from
// this daemon rather than a previous one.
const closedMarker = "\r\n\x1b[0m\x1b[2m── reopened from a closed tab ──\x1b[0m\r\n"

// closedHistoryLimit is how many closed sessions each workspace keeps for
// reopening. The newest are the ones a user comes back to.
const closedHistoryLimit = 3

var errClosedSessionNotFound = errors.New("closed session not found")

type resumeStore struct {
	directory string
	// retention is how long a snapshot is kept; nil keeps defaultRetention.
	retention func() time.Duration
	// mu makes take consume atomically: two tabs created together in one
	// workspace must restore two different snapshots, not the same one twice.
	mu sync.Mutex
}

type resumeSnapshot struct {
	Format         int         `json:"format"`
	WorkspaceID    string      `json:"workspace_id"`
	TabName        string      `json:"tab_name"`
	Agent          model.Agent `json:"agent,omitempty"`
	AgentSessionID string      `json:"agent_session_id,omitempty"`
	SavedAt        time.Time   `json:"saved_at"`
}

func (s *resumeStore) keep() time.Duration {
	if s.retention == nil {
		return defaultRetention
	}
	return s.retention()
}

// promote moves every snapshot in from into this store, unless this store
// already holds one for the same tab, which is newer. A crash leaves the
// checkpoints of the daemon that crashed; the next daemon offers them the way
// it offers a stop's snapshots. from is emptied either way.
func (s *resumeStore) promote(from *resumeStore) {
	entries, err := os.ReadDir(from.directory)
	if err != nil {
		return
	}
	if err := os.MkdirAll(s.directory, 0o700); err != nil {
		return
	}
	for _, entry := range entries {
		id, found := strings.CutSuffix(entry.Name(), ".json")
		if !found || !storedID.MatchString(id) {
			continue
		}
		if _, err := os.Stat(s.metaPath(id)); err == nil {
			continue
		}
		// The recording first, so the metadata that makes the snapshot
		// visible never names a missing recording.
		_ = os.Rename(from.recordingPath(id), s.recordingPath(id))
		_ = os.Rename(from.metaPath(id), s.metaPath(id))
	}
	_ = os.RemoveAll(from.directory)
}

func (s *resumeStore) metaPath(id string) string {
	return filepath.Join(s.directory, id+".json")
}

func (s *resumeStore) recordingPath(id string) string {
	return filepath.Join(s.directory, id+".recording")
}

// save writes one tab's snapshot under its tab ID. The ID only names the
// files; matching at restore goes by the workspace inside the metadata.
func (s *resumeStore) save(id string, meta resumeSnapshot, recording []byte) error {
	// The recording is written first and atomically, so the metadata that
	// makes a snapshot visible to take never names a partial recording.
	if err := jsonfile.WriteBytes(s.recordingPath(id), recording); err != nil {
		return fmt.Errorf("save resume recording: %w", err)
	}
	if err := jsonfile.Write(s.metaPath(id), meta); err != nil {
		return fmt.Errorf("save resume snapshot: %w", err)
	}
	return nil
}

// take returns the workspace's best snapshot and removes it, so restored
// output is offered once rather than on every later restart. Newest stop
// first — it is the one the user is coming back to — and the lowest tab name
// within it, so a workspace saved with several tabs restores them in the
// order they were named. Only snapshots list accepts are candidates: one past
// the retention or one that cannot be read is skipped, so a daemon that runs
// for weeks does not replay a stale stop and one corrupt file does not keep a
// workspace from opening.
func (s *resumeStore) take(workspaceID string) (resumeSnapshot, []byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := s.listLocked(time.Now())[workspaceID]
	if len(values) == 0 {
		return resumeSnapshot{}, nil, false
	}
	best := values[0]
	for _, value := range values[1:] {
		if !value.meta.SavedAt.Equal(best.meta.SavedAt) {
			break
		}
		if value.meta.TabName < best.meta.TabName {
			best = value
		}
	}
	recording, err := os.ReadFile(s.recordingPath(best.id))
	// Consumed either way: a snapshot that failed once would fail again, and
	// leaving it behind would repeat the failure on every tab.
	s.removeLocked(best.id)
	if err != nil {
		recording = nil
	}
	return best.meta, recording, true
}

func (s *resumeStore) removeLocked(id string) {
	_ = os.Remove(s.metaPath(id))
	_ = os.Remove(s.recordingPath(id))
}

// storedID is the shape of the tab IDs snapshots are saved under. An ID a
// client sends names a file, so nothing else may reach the filesystem.
var storedID = regexp.MustCompile(`^[0-9a-f]{1,64}$`)

type storedSnapshot struct {
	id   string
	meta resumeSnapshot
}

// list returns the readable snapshots inside the retention, grouped by
// workspace and newest first. A server built without a store has none.
func (s *resumeStore) list(now time.Time) map[string][]storedSnapshot {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked(now)
}

func (s *resumeStore) listLocked(now time.Time) map[string][]storedSnapshot {
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return nil
	}
	result := make(map[string][]storedSnapshot)
	keep := s.keep()
	for _, entry := range entries {
		id, found := strings.CutSuffix(entry.Name(), ".json")
		if !found || !storedID.MatchString(id) {
			continue
		}
		meta, err := jsonfile.Read[resumeSnapshot](s.metaPath(id))
		if err != nil || meta.WorkspaceID == "" || now.Sub(meta.SavedAt) > keep {
			continue
		}
		result[meta.WorkspaceID] = append(result[meta.WorkspaceID], storedSnapshot{id: id, meta: meta})
	}
	for _, values := range result {
		sort.Slice(values, func(i, j int) bool {
			if !values[i].meta.SavedAt.Equal(values[j].meta.SavedAt) {
				return values[i].meta.SavedAt.After(values[j].meta.SavedAt)
			}
			return values[i].id < values[j].id
		})
	}
	return result
}

// trim keeps the newest keep snapshots of a workspace and removes the rest.
func (s *resumeStore) trim(workspaceID string, keep int, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := s.listLocked(now)[workspaceID]
	for index := keep; index < len(values); index++ {
		s.removeLocked(values[index].id)
	}
}

// read returns the snapshot saved under id when it belongs to workspaceID.
// It does not consume it: the caller removes it once the snapshot is used.
func (s *resumeStore) read(workspaceID, id string) (resumeSnapshot, []byte, error) {
	if !storedID.MatchString(id) {
		return resumeSnapshot{}, nil, errClosedSessionNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, err := jsonfile.Read[resumeSnapshot](s.metaPath(id))
	if err != nil || meta.WorkspaceID != workspaceID {
		return resumeSnapshot{}, nil, errClosedSessionNotFound
	}
	recording, err := os.ReadFile(s.recordingPath(id))
	if err != nil {
		recording = nil
	}
	return meta, recording, nil
}

func (s *resumeStore) remove(id string) {
	if !storedID.MatchString(id) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLocked(id)
}

// discardStale removes snapshots past the retention, going by file age so an
// orphaned recording whose metadata is gone still ages out.
func (s *resumeStore) discardStale(now time.Time) {
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return
	}
	keep := s.keep()
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) <= keep {
			continue
		}
		_ = os.Remove(filepath.Join(s.directory, entry.Name()))
	}
}

// agentSessionID is the shape of the session identifiers agents report
// through their hooks: UUIDs from Claude Code and Codex, `ses_` IDs from
// OpenCode, and `sess_` IDs from Kiro CLI. The resume command is typed into a shell, so anything outside this
// shape is not an identifier to interpolate — it falls back to the agent's own
// "most recent session" form instead.
var agentSessionID = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// resumeCommand is the command line typed — without a newline — into a
// restored tab's fresh shell, so continuing the agent conversation is one
// Enter away and abandoning it is one Ctrl+C.
func resumeCommand(agent model.Agent, sessionID string) string {
	known := agentSessionID.MatchString(sessionID)
	switch agent {
	case model.AgentClaude:
		if known {
			return "claude --resume " + sessionID
		}
		return "claude --continue"
	case model.AgentCodex:
		if known {
			return "codex resume " + sessionID
		}
		return "codex resume"
	case model.AgentOpenCode:
		if known {
			return "opencode --session " + sessionID
		}
		return "opencode --continue"
	case model.AgentKiro:
		if known {
			return "kiro-cli --resume-id " + sessionID
		}
		return "kiro-cli --resume"
	}
	return ""
}
