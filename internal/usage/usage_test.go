package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTranscript(t *testing.T, configDir, workspace, sessionID string, lines ...string) {
	t.Helper()
	directory := filepath.Join(configDir, "projects", transcriptDirectory(workspace))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	path := filepath.Join(directory, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func assistantLine(input, cacheCreation, cacheRead int) string {
	line, _ := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{"usage": map[string]any{
			"input_tokens":                input,
			"cache_creation_input_tokens": cacheCreation,
			"cache_read_input_tokens":     cacheRead,
		}},
	})
	return string(line)
}

func TestReadClaudeTakesTheNewestCounters(t *testing.T) {
	configDir := t.TempDir()
	workspace := "/Users/example/work/romty"
	writeTranscript(t, configDir, workspace, "session-1",
		`{"type":"user","message":{"content":"hello"}}`,
		assistantLine(10, 20, 30),
		`{"type":"user","message":{"content":"more"}}`,
		assistantLine(2, 1288, 342813),
		`{"type":"summary","totalCostUSD":1.25}`,
	)

	value, ok := NewReader().ReadClaude(configDir, workspace, "session-1")
	if !ok {
		t.Fatal("ReadClaude() found no counters")
	}
	if value.ContextTokens != 2+1288+342813 {
		t.Fatalf("context tokens = %d, want the newest request's total", value.ContextTokens)
	}
	if value.CostUSD != 1.25 {
		t.Fatalf("cost = %v, want 1.25", value.CostUSD)
	}
}

// A transcript runs to hundreds of megabytes, so only its end is read. The
// record straddling that seek is a fragment and must not be parsed.
func TestReadClaudeReadsOnlyTheEndOfALongTranscript(t *testing.T) {
	configDir := t.TempDir()
	workspace := "/Users/example/work/romty"
	filler := make([]string, 0, 400)
	for range 400 {
		filler = append(filler, `{"type":"user","message":{"content":"`+strings.Repeat("x", 4096)+`"}}`)
	}
	lines := append([]string{assistantLine(999, 999, 999)}, filler...)
	lines = append(lines, assistantLine(1, 2, 3))
	writeTranscript(t, configDir, workspace, "session-1", lines...)

	value, ok := NewReader().ReadClaude(configDir, workspace, "session-1")
	if !ok || value.ContextTokens != 6 {
		t.Fatalf("ReadClaude() = (%+v, %v), want the last record's 6 tokens", value, ok)
	}
}

// One record can hold a whole tool result. Reading it into memory to look for a
// token count is an allocation the daemon cannot refuse, so it is skipped.
func TestReadClaudeSkipsAnOversizedRecord(t *testing.T) {
	configDir := t.TempDir()
	workspace := "/Users/example/work/romty"
	writeTranscript(t, configDir, workspace, "session-1",
		assistantLine(1, 2, 3),
		`{"type":"user","message":{"content":"`+strings.Repeat("y", maxRecordBytes)+`"}}`,
		assistantLine(4, 5, 6),
	)

	value, ok := NewReader().ReadClaude(configDir, workspace, "session-1")
	if !ok || value.ContextTokens != 15 {
		t.Fatalf("ReadClaude() = (%+v, %v), want the record after the oversized one", value, ok)
	}
}

func TestReadClaudeReportsNothingWithoutCounters(t *testing.T) {
	configDir := t.TempDir()
	workspace := "/Users/example/work/romty"
	writeTranscript(t, configDir, workspace, "session-1", `{"type":"user","message":{"content":"hello"}}`)

	if value, ok := NewReader().ReadClaude(configDir, workspace, "session-1"); ok {
		t.Fatalf("ReadClaude() = (%+v, true), want no reading", value)
	}
	if value, ok := NewReader().ReadClaude(configDir, workspace, "missing"); ok {
		t.Fatalf("ReadClaude() on a missing transcript = (%+v, true), want no reading", value)
	}
}

// The identifier arrives from a hook over the socket. It names a file, so it is
// the one field that could walk out of the transcript directory.
func TestSafeSessionIDRejectsAnythingThatIsNotAnIdentifier(t *testing.T) {
	for _, value := range []string{
		"", "..", "../../etc/passwd", "a/b", "a\\b", "a.b", "a b", "a\x00b",
		strings.Repeat("a", 129),
	} {
		if SafeSessionID(value) {
			t.Errorf("SafeSessionID(%q) = true, want it refused", value)
		}
	}
	for _, value := range []string{"3816c4d9-e45d-489c-8aac-7925f09fe038", "abc_123", "A-Z-0-9"} {
		if !SafeSessionID(value) {
			t.Errorf("SafeSessionID(%q) = false, want it accepted", value)
		}
	}
}

func TestReadClaudeRefusesAnUnsafeSessionID(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "secret.jsonl"),
		[]byte(assistantLine(1, 2, 3)+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if value, ok := NewReader().ReadClaude(configDir, "/w", "../secret"); ok {
		t.Fatalf("ReadClaude() escaped the transcript directory: %+v", value)
	}
}

// A snapshot is taken every couple of seconds. Re-reading a transcript that has
// not changed would cost more than everything else that goes into one.
func TestReaderRereadsOnlyAChangedTranscript(t *testing.T) {
	configDir := t.TempDir()
	workspace := "/Users/example/work/romty"
	writeTranscript(t, configDir, workspace, "session-1", assistantLine(1, 2, 3))
	reader := NewReader()

	if value, ok := reader.ReadClaude(configDir, workspace, "session-1"); !ok || value.ContextTokens != 6 {
		t.Fatalf("first read = (%+v, %v), want 6 tokens", value, ok)
	}
	// Rewritten behind the reader's back with the same size and timestamp: the
	// cached reading is what it must still report.
	path := filepath.Join(configDir, "projects", transcriptDirectory(workspace), "session-1.jsonl")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(assistantLine(4, 5, 6)+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
	if value, _ := reader.ReadClaude(configDir, workspace, "session-1"); value.ContextTokens != 6 {
		t.Fatalf("unchanged read = %+v, want the cached 6 tokens", value)
	}

	// A newer timestamp is what makes it read again.
	if err := os.Chtimes(path, info.ModTime().Add(time.Second), info.ModTime().Add(time.Second)); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
	if value, _ := reader.ReadClaude(configDir, workspace, "session-1"); value.ContextTokens != 15 {
		t.Fatalf("changed read = %+v, want the new 15 tokens", value)
	}
}

func TestReaderForgetsEverythingWhenItFillsUp(t *testing.T) {
	configDir := t.TempDir()
	reader := NewReader()
	for index := range maxCachedTranscripts + 1 {
		workspace := fmt.Sprintf("/w/%d", index)
		writeTranscript(t, configDir, workspace, "session-1", assistantLine(1, 2, 3))
		if _, ok := reader.ReadClaude(configDir, workspace, "session-1"); !ok {
			t.Fatalf("read %d found no counters", index)
		}
	}
	if len(reader.entries) > maxCachedTranscripts {
		t.Fatalf("cached entries = %d, want at most %d", len(reader.entries), maxCachedTranscripts)
	}
}

func TestTranscriptDirectoryMatchesClaudesNaming(t *testing.T) {
	if got := transcriptDirectory("/Users/bruce/.vibemon"); got != "-Users-bruce--vibemon" {
		t.Fatalf("transcriptDirectory() = %q, want dots and separators dashed", got)
	}
}

// The tail is read from a fixed distance back, which can land exactly on a
// record boundary. The first line read is discarded as a fragment, so a seek
// that lands on a boundary would throw away a whole record — and if that record
// carried the newest counters, romty would report the previous request's.
func TestReadClaudeKeepsARecordThatStartsAtTheTailBoundary(t *testing.T) {
	configDir := t.TempDir()
	workspace := "/Users/example/work/romty"
	// The newest counters, then a record that carries none.
	newest := assistantLine(1, 2, 3)
	trailing := `{"type":"user","message":{"content":"thanks"}}`
	older := assistantLine(100, 200, 300)

	previous := maxTranscriptTail
	// Exactly the two final records, so the boundary sits on newest's first byte.
	maxTranscriptTail = int64(len(newest) + len(trailing) + 2)
	t.Cleanup(func() { maxTranscriptTail = previous })

	writeTranscript(t, configDir, workspace, "session-1", older, newest, trailing)
	value, ok := NewReader().ReadClaude(configDir, workspace, "session-1")
	if !ok || value.ContextTokens != 6 {
		t.Fatalf("ReadClaude() = (%+v, %v), want the 6 tokens on the boundary record", value, ok)
	}
}
