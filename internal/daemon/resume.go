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

package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/opspresso/romty/internal/jsonfile"
	"github.com/opspresso/romty/internal/model"
)

// resumeRetention is how long an unconsumed snapshot is kept. A workspace the
// user has not reopened in this long is not one they are resuming into, and
// replaying weeks-old output as if it just happened would mislead more than
// it helps.
const resumeRetention = 7 * 24 * time.Hour

// resumeMarker separates the restored output from the fresh shell that
// follows it, so old output is not mistaken for something that just ran.
const resumeMarker = "\r\n\x1b[0m\x1b[2m── restored from the previous romty session ──\x1b[0m\r\n"

type resumeStore struct {
	directory string
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

func (s *resumeStore) metaPath(id string) string {
	return filepath.Join(s.directory, id+".json")
}

func (s *resumeStore) recordingPath(id string) string {
	return filepath.Join(s.directory, id+".recording")
}

// save writes one tab's snapshot under its tab ID. The ID only names the
// files; matching at restore goes by the workspace inside the metadata.
func (s *resumeStore) save(id string, meta resumeSnapshot, recording []byte) error {
	if err := os.MkdirAll(s.directory, 0o700); err != nil {
		return fmt.Errorf("create resume directory: %w", err)
	}
	if err := os.WriteFile(s.recordingPath(id), recording, 0o600); err != nil {
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
// order they were named. An entry that cannot be read is skipped, not fatal:
// one corrupt file must not keep a workspace from opening.
func (s *resumeStore) take(workspaceID string) (resumeSnapshot, []byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return resumeSnapshot{}, nil, false
	}
	var bestID string
	var best resumeSnapshot
	for _, entry := range entries {
		id, found := strings.CutSuffix(entry.Name(), ".json")
		if !found {
			continue
		}
		meta, err := jsonfile.Read[resumeSnapshot](s.metaPath(id))
		if err != nil || meta.WorkspaceID != workspaceID {
			continue
		}
		if bestID == "" ||
			meta.SavedAt.After(best.SavedAt) ||
			(meta.SavedAt.Equal(best.SavedAt) && meta.TabName < best.TabName) {
			bestID, best = id, meta
		}
	}
	if bestID == "" {
		return resumeSnapshot{}, nil, false
	}
	recording, err := os.ReadFile(s.recordingPath(bestID))
	// Consumed either way: a snapshot that failed once would fail again, and
	// leaving it behind would repeat the failure on every tab.
	_ = os.Remove(s.metaPath(bestID))
	_ = os.Remove(s.recordingPath(bestID))
	if err != nil {
		recording = nil
	}
	return best, recording, true
}

// discardStale removes snapshots past the retention, going by file age so an
// orphaned recording whose metadata is gone still ages out.
func (s *resumeStore) discardStale(now time.Time) {
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) <= resumeRetention {
			continue
		}
		_ = os.Remove(filepath.Join(s.directory, entry.Name()))
	}
}

// agentSessionID is the shape of the session identifiers Claude Code and
// Codex report through their hooks. The resume command is typed into a shell,
// so anything outside this shape is not an identifier to interpolate — it
// falls back to the agent's own "most recent session" form instead.
var agentSessionID = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

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
	}
	return ""
}
