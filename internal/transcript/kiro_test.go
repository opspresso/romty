package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeKiro(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadKiroV3DatesACancelledTurnByItsTurnEnd(t *testing.T) {
	home := t.TempDir()
	const session = "sess_7883db40-0d55-4ed6-8be3-f62e0ac5d223"
	path := filepath.Join(home, "sessions", "6b22f016f3e45775", session, "messages.jsonl")
	user := `{"timestamp":"2026-10-15T10:48:03.582Z","payload":{"type":"user","content":"Write an essay"}}`
	cancelled := `{"timestamp":"2026-10-15T10:48:09.628Z","payload":{"type":"turn_end","stopReason":"cancelled"}}`
	completed := `{"timestamp":"2026-10-15T10:48:23.861Z","payload":{"type":"turn_end","stopReason":"end_turn"}}`
	reader := NewReader()

	writeKiro(t, path, user, cancelled)
	got, ok := reader.ReadKiro(home, session)
	want, _ := time.Parse(time.RFC3339Nano, "2026-10-15T10:48:09.628Z")
	if !ok || !got.InterruptedAt.Equal(want) {
		t.Fatalf("ReadKiro() = (%+v, %v), want interrupted at %v", got, ok, want)
	}
	// A completed turn or a new prompt after the cancel means the session went on.
	for _, lines := range [][]string{{user, cancelled, user}, {user, completed}} {
		writeKiro(t, path, lines...)
		if got, _ := reader.ReadKiro(home, session); !got.InterruptedAt.IsZero() {
			t.Fatalf("ReadKiro(%v) = %+v, want no interrupt", lines, got)
		}
	}
}

func TestReadKiroV2DatesACancellationByTheFileTime(t *testing.T) {
	home := t.TempDir()
	const session = "55d89a2c-0911-4a4d-aa3f-831a0cb0c432"
	path := filepath.Join(home, "sessions", "cli", session+".jsonl")
	prompt := `{"version":"v1","kind":"Prompt","data":{"message_id":"p","content":[{"kind":"text","data":"Write an essay"}]}}`
	response := `{"version":"v1","kind":"AssistantMessage","data":{"message_id":"a","content":[{"kind":"text","data":"Response was interrupted by the user"}]}}`
	tool := `{"version":"v1","kind":"ToolResults","data":{"results":[{"content":[{"kind":"text","data":"Tool use was cancelled by the user"}]}]}}`
	answered := `{"version":"v1","kind":"AssistantMessage","data":{"message_id":"b","content":[{"kind":"text","data":"ok"}]}}`
	modified := time.Date(2026, 10, 15, 11, 49, 54, 0, time.UTC)
	reader := NewReader()

	for _, last := range []string{response, tool} {
		writeKiro(t, path, prompt, last)
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
		if got, ok := reader.ReadKiro(home, session); !ok || !got.InterruptedAt.Equal(modified) {
			t.Fatalf("ReadKiro() = (%+v, %v), want interrupted at the file time", got, ok)
		}
		modified = modified.Add(time.Second)
	}
	for _, lines := range [][]string{{prompt, response, prompt}, {prompt, answered}} {
		writeKiro(t, path, lines...)
		if got, _ := reader.ReadKiro(home, session); !got.InterruptedAt.IsZero() {
			t.Fatalf("ReadKiro(%v) = %+v, want no interrupt", lines, got)
		}
	}
}

func TestReadKiroRefusesUnsafeOrMissingSessions(t *testing.T) {
	home := t.TempDir()
	reader := NewReader()
	for _, session := range []string{"", "../../etc/passwd", "sess_missing", "0000"} {
		if got, ok := reader.ReadKiro(home, session); ok {
			t.Fatalf("ReadKiro(%q) = %+v, want nothing", session, got)
		}
	}
	if _, ok := reader.ReadKiro("", "sess_1"); ok {
		t.Fatal("ReadKiro() read without a Kiro home")
	}
}
