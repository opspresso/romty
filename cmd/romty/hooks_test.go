package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/opspresso/romty/internal/model"
)

func TestDecodeHookEventKeepsOnlyStatusMetadata(t *testing.T) {
	secret := "do-not-forward-this-secret"
	payload := `{
		"session_id":"session-1",
		"prompt_id":"prompt-1",
		"hook_event_name":"PreToolUse",
		"tool_name":"Bash",
		"permission_mode":"plan",
		"prompt":"` + secret + `",
		"tool_input":{"command":"` + secret + `"},
		"tool_response":"` + secret + `",
		"transcript_path":"/private/transcript.jsonl"
	}`
	event, err := decodeHookEvent("claude", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("decodeHookEvent() error = %v", err)
	}
	if event.Agent != model.AgentClaude || event.SessionID != "session-1" ||
		event.HookEvent != "PreToolUse" ||
		event.ToolName != "Bash" || event.PermissionMode != "plan" {
		t.Fatalf("decodeHookEvent() = %#v", event)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if bytes.Contains(encoded, []byte(secret)) || bytes.Contains(encoded, []byte("transcript")) {
		t.Fatalf("sanitized event retained hook content: %s", encoded)
	}
}

func TestDecodeHookEventDetectsBackgroundWork(t *testing.T) {
	event, err := decodeHookEvent("claude", strings.NewReader(`{
		"hook_event_name":"Stop",
		"background_tasks":[{"status":"running","command":"private"}]
	}`))
	if err != nil {
		t.Fatalf("decodeHookEvent() error = %v", err)
	}
	if !event.Background {
		t.Fatal("running background task was not detected")
	}
}

func TestHookCommandIsSilentOutsideARomtyTab(t *testing.T) {
	t.Setenv("ROMTY_TAB_ID", "")
	t.Setenv("ROMTY_HOME", strings.Repeat("too-deep/", 200))
	var output bytes.Buffer
	if err := runCommandWithInput([]string{"hook", "codex"}, &output, strings.NewReader("not json")); err != nil {
		t.Fatalf("hook command error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("hook command output = %q, want none", output.String())
	}
}

func TestDecodeHookEventRejectsOversizedMetadata(t *testing.T) {
	payload := `{"hook_event_name":"` + strings.Repeat("x", 513) + `"}`
	if _, err := decodeHookEvent("codex", strings.NewReader(payload)); err == nil {
		t.Fatal("decodeHookEvent() accepted oversized metadata")
	}
	if _, err := decodeHookEvent("codex", io.LimitReader(strings.NewReader("{}"), maxHookInputBytes)); err == nil {
		t.Fatal("decodeHookEvent() accepted a missing hook event")
	}
	large := `{"hook_event_name":"Stop","prompt":"` + strings.Repeat("x", maxHookInputBytes) + `"}`
	if _, err := decodeHookEvent("codex", strings.NewReader(large)); err == nil {
		t.Fatal("decodeHookEvent() accepted an oversized payload")
	}
}
