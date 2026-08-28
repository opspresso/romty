// Package usage reads the token counters an agent already writes to disk.
//
// Nothing here derives a number from what an agent drew. The counters are the
// agent's own record of what it sent, and a transcript that does not carry them
// yields nothing rather than a guess. romty reports the totals and never the
// share of a context window: a transcript records no window size, so a
// percentage could only come from a table of model limits that romty would have
// to keep correct as models change.
package usage

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Usage is what an agent has spent on one session.
type Usage struct {
	// ContextTokens is what the newest request carried into the model: the
	// prompt plus everything the cache held for it. That sum is the session's
	// context occupancy, which is what a long-running agent is judged by.
	ContextTokens int
	// CostUSD is what the session has cost so far, as the agent totalled it.
	CostUSD float64
}

// Empty reports a reading that found no counters at all.
func (u Usage) Empty() bool {
	return u.ContextTokens == 0 && u.CostUSD == 0
}

// maxTranscriptTail bounds the read. The newest counters are at the end of the
// file, a transcript grows to hundreds of megabytes, and none of the rest of it
// answers the question.
const maxTranscriptTail = 1 << 20

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
	value    Usage
	found    bool
}

func NewReader() *Reader {
	return &Reader{entries: make(map[string]entry)}
}

// ReadClaude returns the newest counters recorded for one Claude Code session.
// configDir is the agent's configuration directory, workspace the directory the
// session runs in, and sessionID the identifier its hooks reported.
func (r *Reader) ReadClaude(configDir, workspace, sessionID string) (Usage, bool) {
	if configDir == "" || workspace == "" || !SafeSessionID(sessionID) {
		return Usage{}, false
	}
	path := filepath.Join(configDir, "projects", transcriptDirectory(workspace), sessionID+".jsonl")
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return Usage{}, false
	}

	r.mu.Lock()
	cached, ok := r.entries[path]
	r.mu.Unlock()
	if ok && cached.modified.Equal(info.ModTime()) && cached.size == info.Size() {
		return cached.value, cached.found
	}

	value, found := readTranscript(path)
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

// transcriptDirectory is how Claude Code names a working directory's transcript
// folder: every separator and dot becomes a dash.
func transcriptDirectory(workspace string) string {
	return strings.NewReplacer("/", "-", ".", "-").Replace(workspace)
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
	Message *struct {
		Usage *struct {
			InputTokens              int `json:"input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	TotalCostUSD *float64 `json:"totalCostUSD"`
}

func readTranscript(path string) (Usage, bool) {
	file, err := os.Open(path)
	if err != nil {
		return Usage{}, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return Usage{}, false
	}
	offset := max(info.Size()-maxTranscriptTail, 0)
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return Usage{}, false
	}
	reader := bufio.NewReaderSize(file, 64<<10)
	if offset > 0 {
		// The read began mid-file, so the first line is the tail of whatever
		// record straddles the seek and is not valid JSON on its own.
		if _, err := readRecord(reader); err != nil {
			return Usage{}, false
		}
	}

	var result Usage
	var found bool
	for {
		line, err := readRecord(reader)
		if value, ok := decodeRecord(line); ok {
			if value.ContextTokens > 0 {
				result.ContextTokens = value.ContextTokens
			}
			if value.CostUSD > 0 {
				result.CostUSD = value.CostUSD
			}
			found = found || !value.Empty()
		}
		if err != nil {
			return result, found
		}
	}
}

// decodeRecord reads the counters one record carries, if it carries any.
func decodeRecord(line []byte) (Usage, bool) {
	if len(line) == 0 {
		return Usage{}, false
	}
	var record transcriptRecord
	if err := json.Unmarshal(line, &record); err != nil {
		return Usage{}, false
	}
	var value Usage
	if record.Message != nil && record.Message.Usage != nil {
		counters := record.Message.Usage
		value.ContextTokens = counters.InputTokens +
			counters.CacheCreationInputTokens + counters.CacheReadInputTokens
	}
	if record.TotalCostUSD != nil {
		value.CostUSD = *record.TotalCostUSD
	}
	return value, !value.Empty()
}

// readRecord returns the next line, or nothing for a line too long to be one
// that carries counters. The error is returned alongside whatever was read, so
// the last line of a file with no trailing newline is not dropped.
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
