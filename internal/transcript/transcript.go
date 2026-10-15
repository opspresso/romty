// Package transcript reads what an agent already writes to its own transcript:
// its token counters, and whether the user interrupted its last turn.
//
// Nothing here derives a number from what an agent drew. The counters are the
// agent's own record of what it sent, and a transcript that does not carry them
// yields nothing rather than a guess. romty reports the totals and never the
// share of a context window: a transcript records no window size, so a
// percentage could only come from a table of model limits that romty would have
// to keep correct as models change.
//
// Claude Code runs no hook when the user interrupts a turn or rejects a tool,
// and Kiro CLI runs no Stop hook for a cancelled turn. The record each agent
// writes for that is the only signal the turn has ended.
package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Reading is what one session's transcript reports.
type Reading struct {
	// ContextTokens is what the newest request carried into the model: the
	// prompt plus everything the cache held for it. That sum is the session's
	// context occupancy, which is what a long-running agent is judged by.
	ContextTokens int
	// CostUSD is what the session has cost so far, as the agent totalled it.
	CostUSD float64
	// InterruptedAt is when the user interrupted the session's last turn. It
	// is zero unless that interrupt is the newest conversation record: a later
	// prompt or response means the session went on.
	InterruptedAt time.Time
}

// Empty reports a reading that found neither counters nor an interrupt.
func (r Reading) Empty() bool {
	return r.ContextTokens == 0 && r.CostUSD == 0 && r.InterruptedAt.IsZero()
}

// interruptMarker starts the user record Claude Code writes when the user
// interrupts a turn or rejects a tool call.
var interruptMarker = []byte("[Request interrupted by user")

// maxTranscriptTail bounds the read. The newest counters are at the end of the
// file, a transcript grows to hundreds of megabytes, and none of the rest of it
// answers the question. It is a variable so a test can reach the boundary
// without writing a megabyte to get there.
var maxTranscriptTail int64 = 1 << 20

// maxRecordBytes skips a record too large to be one that carries counters. A
// single JSONL line can hold a whole tool result or an inline image, and
// reading one into memory to look for a token count is how a corrupt or hostile
// transcript would become an allocation the daemon cannot refuse.
//
// It is well under maxTranscriptTail on purpose. A ceiling above the tail could
// never be reached, and skipping a record only helps if what follows it is
// still inside the window.
const maxRecordBytes = 64 << 10

// maxCachedTranscripts bounds what a Reader remembers. A session that ends
// leaves its reading behind, and one entry per session it ever saw would grow
// for as long as the daemon runs.
const maxCachedTranscripts = 256

// Reader reads transcripts and keeps the last reading of each, refreshing one
// only when its file has changed. Status is taken every couple of seconds and a
// transcript is a megabyte to read, so re-reading an unchanged one would cost
// more than everything else that goes into a snapshot.
type Reader struct {
	mu      sync.Mutex
	entries map[string]entry
}

type entry struct {
	modified time.Time
	size     int64
	value    Reading
	found    bool
}

func NewReader() *Reader {
	return &Reader{entries: make(map[string]entry)}
}

// ReadClaude returns what one Claude Code session's transcript reports. path
// is the transcript_path the session's hook reported. It is read only when it
// names <configDir>/projects/<folder>/<sessionID>.jsonl and is a regular
// file: the path arrives over the socket, so it is untrusted input.
//
// romty takes the path from the hook rather than building it, because Claude
// Code's folder naming is its own and changes between versions.
func (r *Reader) ReadClaude(configDir, path, sessionID string) (Reading, bool) {
	if !claudeTranscriptPath(configDir, path, sessionID) {
		return Reading{}, false
	}
	return r.read(path, applyRecord)
}

// ReadKiro returns whether one Kiro CLI session's last turn was cancelled.
// The session identifier names the record: Kiro CLI V3 keeps
// <home>/sessions/<workspace>/<sess_…>/messages.jsonl, and V2 keeps
// <home>/sessions/cli/<UUID>.jsonl.
func (r *Reader) ReadKiro(home, sessionID string) (Reading, bool) {
	if home == "" || !SafeSessionID(sessionID) {
		return Reading{}, false
	}
	sessions := filepath.Join(home, "sessions")
	if strings.HasPrefix(sessionID, "sess_") {
		matches, _ := filepath.Glob(filepath.Join(sessions, "*", sessionID, "messages.jsonl"))
		if len(matches) == 0 {
			return Reading{}, false
		}
		return r.read(matches[0], applyKiroV3Record)
	}
	return r.read(filepath.Join(sessions, "cli", sessionID+".jsonl"), applyKiroV2Record)
}

// read returns one record file's reading, re-reading it only when it changed.
// apply folds each record in file order; it receives the file's modification
// time for records that carry no time of their own.
func (r *Reader) read(path string, apply func(*Reading, []byte, time.Time)) (Reading, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return Reading{}, false
	}
	r.mu.Lock()
	cached, ok := r.entries[path]
	r.mu.Unlock()
	if ok && cached.modified.Equal(info.ModTime()) && cached.size == info.Size() {
		return cached.value, cached.found
	}
	value, found := readTranscript(path, apply)
	r.mu.Lock()
	if len(r.entries) >= maxCachedTranscripts {
		// Every entry is a cache of something re-readable, so dropping them all
		// costs one re-read rather than a wrong answer.
		clear(r.entries)
	}
	r.entries[path] = entry{modified: info.ModTime(), size: info.Size(), value: value, found: found}
	r.mu.Unlock()
	return value, found
}

// claudeTranscriptPath reports whether path is a session transcript directly
// inside one project folder of configDir.
func claudeTranscriptPath(configDir, path, sessionID string) bool {
	if configDir == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || !SafeSessionID(sessionID) {
		return false
	}
	folder := filepath.Dir(path)
	return filepath.Base(path) == sessionID+".jsonl" &&
		filepath.Dir(folder) == filepath.Join(configDir, "projects") &&
		filepath.Base(folder) != ".."
}

// SafeSessionID reports whether an agent's session identifier can be joined
// into a path. It arrives over the socket from a hook, so it is untrusted
// input, and anything but an identifier character could walk out of the
// transcript directory and name a file romty was never asked to read.
func SafeSessionID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, letter := range value {
		switch {
		case letter >= 'a' && letter <= 'z',
			letter >= 'A' && letter <= 'Z',
			letter >= '0' && letter <= '9',
			letter == '-', letter == '_':
		default:
			return false
		}
	}
	return true
}

// transcriptRecord is the part of a transcript line romty reads. Everything
// else in it — prompts, tool inputs, assistant messages — is deliberately not
// described here, so it is never decoded and never held.
type transcriptRecord struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   *struct {
		Usage *struct {
			InputTokens              int `json:"input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	TotalCostUSD *float64 `json:"totalCostUSD"`
}

// interruptRecord is decoded only from a line that contains the marker, so
// the text it holds is the marker Claude Code wrote, not a user's prompt.
type interruptRecord struct {
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func readTranscript(path string, apply func(*Reading, []byte, time.Time)) (Reading, bool) {
	file, err := os.Open(path)
	if err != nil {
		return Reading{}, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return Reading{}, false
	}
	offset := max(info.Size()-maxTranscriptTail, 0)
	if offset > 0 {
		// Back up a byte so the line discarded below is always a fragment.
		// A boundary that lands exactly on a record start would otherwise
		// throw a whole record away, and if that were the one carrying the
		// newest counters romty would report the previous request's, or none.
		offset--
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return Reading{}, false
	}
	reader := bufio.NewReaderSize(file, 64<<10)
	if offset > 0 {
		// The read began mid-file, so the first line is the tail of whatever
		// record straddles the seek and is not valid JSON on its own.
		if _, err := readRecord(reader); err != nil {
			return Reading{}, false
		}
	}

	var result Reading
	for {
		line, err := readRecord(reader)
		apply(&result, line, info.ModTime())
		if err != nil {
			return result, !result.Empty()
		}
	}
}

// applyRecord folds one Claude Code record into the reading, in file order.
func applyRecord(result *Reading, line []byte, _ time.Time) {
	if len(line) == 0 {
		return
	}
	var record transcriptRecord
	if err := json.Unmarshal(line, &record); err != nil {
		return
	}
	if record.Message != nil && record.Message.Usage != nil {
		counters := record.Message.Usage
		if tokens := counters.InputTokens + counters.CacheCreationInputTokens + counters.CacheReadInputTokens; tokens > 0 {
			result.ContextTokens = tokens
		}
	}
	if record.TotalCostUSD != nil && *record.TotalCostUSD > 0 {
		result.CostUSD = *record.TotalCostUSD
	}
	// Only conversation records order the turn. Summaries, attachments and
	// system notes can follow an interrupt without resuming the session.
	switch record.Type {
	case "assistant":
		result.InterruptedAt = time.Time{}
	case "user":
		result.InterruptedAt = time.Time{}
		if bytes.Contains(line, interruptMarker) && isInterrupt(line) {
			// An unreadable time leaves the interrupt unknown rather than
			// dated to an instant that could precede the hook it must follow.
			result.InterruptedAt, _ = time.Parse(time.RFC3339Nano, record.Timestamp)
		}
	}
}

// kiroCancelMarkers start the text Kiro CLI V2 records when the user cancels a
// response or a tool call.
var kiroCancelMarkers = []string{"Response was interrupted by the user", "Tool use was cancelled by the user"}

// kiroMarkerDepth bounds the search for a marker inside a record's data. A
// tool result nests its text five levels deep; the bound leaves room for that
// and stops a hostile record from forcing an unbounded walk.
const kiroMarkerDepth = 8

// applyKiroV3Record folds one Kiro CLI V3 record. Every turn ends with
// turn_end; a cancelled one carries stopReason "cancelled" and runs no Stop
// hook. A later prompt or response means the session went on.
func applyKiroV3Record(result *Reading, line []byte, _ time.Time) {
	var record struct {
		Timestamp string `json:"timestamp"`
		Payload   *struct {
			Type       string `json:"type"`
			StopReason string `json:"stopReason"`
		} `json:"payload"`
	}
	if len(line) == 0 || json.Unmarshal(line, &record) != nil || record.Payload == nil {
		return
	}
	switch record.Payload.Type {
	case "user", "turn_start", "assistant":
		result.InterruptedAt = time.Time{}
	case "turn_end":
		result.InterruptedAt = time.Time{}
		if record.Payload.StopReason == "cancelled" {
			result.InterruptedAt, _ = time.Parse(time.RFC3339Nano, record.Timestamp)
		}
	}
}

// applyKiroV2Record folds one Kiro CLI V2 record. V2 records carry no time, so
// a cancellation is dated by the file's modification time: the newest record
// is the last thing written to the file.
func applyKiroV2Record(result *Reading, line []byte, modified time.Time) {
	var record struct {
		Kind string          `json:"kind"`
		Data json.RawMessage `json:"data"`
	}
	if len(line) == 0 || json.Unmarshal(line, &record) != nil || record.Kind == "" {
		return
	}
	result.InterruptedAt = time.Time{}
	if record.Kind == "Prompt" {
		return
	}
	var data any
	if json.Unmarshal(record.Data, &data) == nil && hasKiroCancelMarker(data, kiroMarkerDepth) {
		result.InterruptedAt = modified
	}
}

// hasKiroCancelMarker reports whether a string within depth levels of value
// starts with a cancellation marker. The response and the tool-result records
// nest their text differently.
func hasKiroCancelMarker(value any, depth int) bool {
	switch typed := value.(type) {
	case string:
		for _, marker := range kiroCancelMarkers {
			if strings.HasPrefix(typed, marker) {
				return true
			}
		}
	case map[string]any:
		for _, child := range typed {
			if depth > 0 && hasKiroCancelMarker(child, depth-1) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if depth > 0 && hasKiroCancelMarker(child, depth-1) {
				return true
			}
		}
	}
	return false
}

// isInterrupt reports whether a user record's content starts with the marker,
// as a plain string or as a text part.
func isInterrupt(line []byte) bool {
	var record interruptRecord
	if err := json.Unmarshal(line, &record); err != nil {
		return false
	}
	var text string
	if json.Unmarshal(record.Message.Content, &text) == nil {
		return strings.HasPrefix(text, string(interruptMarker))
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(record.Message.Content, &parts) != nil {
		return false
	}
	for _, part := range parts {
		if part.Type == "text" && strings.HasPrefix(part.Text, string(interruptMarker)) {
			return true
		}
	}
	return false
}

// readRecord returns the next line, or nothing for a line too long to be one
// that carries counters or an interrupt. The error is returned alongside
// whatever was read, so the last line of a file with no trailing newline is
// not dropped.
func readRecord(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	var oversized bool
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(line)+len(chunk) > maxRecordBytes {
			oversized = true
			line = nil
		}
		if !oversized {
			line = append(line, chunk...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if oversized {
			return nil, err
		}
		return line, err
	}
}
