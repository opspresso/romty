package daemon

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/protocol"
	"github.com/opspresso/romty/internal/usage"
)

func TestAgentHookEventsMapToPhases(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		event protocol.AgentEvent
		want  model.AgentPhase
	}{
		{name: "session ready", event: protocol.AgentEvent{HookEvent: "SessionStart"}, want: model.AgentPhaseIdle},
		{name: "prompt", event: protocol.AgentEvent{HookEvent: "UserPromptSubmit"}, want: model.AgentPhaseThinking},
		{name: "plan prompt", event: protocol.AgentEvent{HookEvent: "UserPromptSubmit", PermissionMode: "plan"}, want: model.AgentPhasePlanning},
		{name: "tool", event: protocol.AgentEvent{HookEvent: "PreToolUse", ToolName: "Bash"}, want: model.AgentPhaseWorking},
		{name: "question tool", event: protocol.AgentEvent{HookEvent: "PreToolUse", ToolName: "AskUserQuestion"}, want: model.AgentPhaseWaitingInput},
		{name: "codex question tool", event: protocol.AgentEvent{HookEvent: "PreToolUse", ToolName: "request_user_input"}, want: model.AgentPhaseWaitingInput},
		{name: "permission", event: protocol.AgentEvent{HookEvent: "PermissionRequest"}, want: model.AgentPhaseWaitingApproval},
		{name: "notification permission", event: protocol.AgentEvent{HookEvent: "Notification", NotificationType: "permission_prompt"}, want: model.AgentPhaseWaitingApproval},
		{name: "notification input", event: protocol.AgentEvent{HookEvent: "Notification", NotificationType: "idle_prompt"}, want: model.AgentPhaseWaitingInput},
		{name: "compact", event: protocol.AgentEvent{HookEvent: "PreCompact"}, want: model.AgentPhaseCompacting},
		{name: "stop", event: protocol.AgentEvent{HookEvent: "Stop"}, want: model.AgentPhaseIdle},
		{name: "background", event: protocol.AgentEvent{HookEvent: "Stop", Background: true}, want: model.AgentPhaseBackground},
		{name: "failure", event: protocol.AgentEvent{HookEvent: "StopFailure"}, want: model.AgentPhaseError},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, terminal, recognized := phaseForAgentEvent(testCase.event)
			if !recognized || terminal || got != testCase.want {
				t.Fatalf("phaseForAgentEvent() = (%q, %v, %v), want (%q, false, true)", got, terminal, recognized, testCase.want)
			}
		})
	}
}

func TestAgentStatusIgnoresAnOldSessionEnd(t *testing.T) {
	server := &Server{
		sessions:      map[string]*session{"tab-1": nil},
		agentStatuses: make(map[string]agentRuntime),
	}
	server.recordAgentEvent("tab-1", &protocol.AgentEvent{
		Agent: model.AgentClaude, SessionID: "old", HookEvent: "SessionStart",
	})
	server.recordAgentEvent("tab-1", &protocol.AgentEvent{
		Agent: model.AgentClaude, SessionID: "new", HookEvent: "SessionStart",
	})
	server.recordAgentEvent("tab-1", &protocol.AgentEvent{
		Agent: model.AgentClaude, SessionID: "old", HookEvent: "SessionEnd",
	})
	server.recordAgentEvent("tab-1", &protocol.AgentEvent{
		Agent: model.AgentCodex, SessionID: "other", HookEvent: "SessionEnd",
	})

	got := server.agentStatuses["tab-1"]
	if got.SessionID != "new" || got.Phase != model.AgentPhaseIdle {
		t.Fatalf("status after stale SessionEnd = %#v, want the new session", got)
	}
	server.recordAgentEvent("tab-1", &protocol.AgentEvent{
		Agent: model.AgentClaude, SessionID: "new", HookEvent: "SessionEnd",
	})
	if _, exists := server.agentStatuses["tab-1"]; exists {
		t.Fatal("matching SessionEnd did not clear the status")
	}
}

func TestAgentStatusRequiresARunningTab(t *testing.T) {
	server := &Server{sessions: make(map[string]*session), agentStatuses: make(map[string]agentRuntime)}
	response := server.recordAgentEvent("missing", &protocol.AgentEvent{
		Agent: model.AgentCodex, HookEvent: "SessionStart",
	})
	if response.Error == "" {
		t.Fatal("agent event for a missing tab succeeded")
	}
}

// A hook is the agent's own account of itself, so it wins over anything read
// back off the screen. Where no hook has spoken, the screen is all romty has.
func TestAgentStatusInfersAPhaseOnlyWhereNoHookHasSpoken(t *testing.T) {
	hooked, unhooked := new(os.File), new(os.File)
	titled := new(os.File)
	previousGroup := foregroundProcessGroup
	previousList := runProcessList
	foregroundProcessGroup = func(terminal *os.File) (int, error) {
		switch terminal {
		case hooked:
			return 101, nil
		case unhooked:
			return 102, nil
		default:
			return 103, nil
		}
	}
	runProcessList = func(context.Context) ([]byte, error) {
		return []byte("101 claude\n102 claude\n103 codex\n"), nil
	}
	t.Cleanup(func() {
		foregroundProcessGroup = previousGroup
		runProcessList = previousList
	})

	hookedSession := newSessionForTest(hooked)
	// The same approval prompt is on both screens.
	hookedSession.broadcast([]byte("Bash(git push)\r\n  Do you want to proceed?\r\n"))
	unhookedSession := newSessionForTest(unhooked)
	unhookedSession.broadcast([]byte("Bash(git push)\r\n  Do you want to proceed?\r\n"))
	titledSession := newSessionForTest(titled)
	titledSession.guest.observe([]byte("\x1b]2;codex — Action required\x07"))

	server := &Server{
		sessions: map[string]*session{
			"tab-1": hookedSession,
			"tab-2": unhookedSession,
			"tab-3": titledSession,
		},
		agentStatuses: map[string]agentRuntime{
			"tab-1": {AgentStatus: model.AgentStatus{Agent: model.AgentClaude, Phase: model.AgentPhaseIdle}},
		},
	}
	want := map[string]model.AgentStatus{
		// The hook says idle even though the screen still shows the prompt it
		// answered.
		"tab-1": {Agent: model.AgentClaude, Phase: model.AgentPhaseIdle},
		"tab-2": {Agent: model.AgentClaude, Phase: model.AgentPhaseWaitingApproval, Estimated: true},
		"tab-3": {Agent: model.AgentCodex, Phase: model.AgentPhaseWaitingApproval, Estimated: true},
	}
	if got := server.agentStatusesSnapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("agentStatusesSnapshot() = %#v, want %#v", got, want)
	}
}

// The counters need the session identifier, which only a hook reports. Without
// one romty cannot tell which of a directory's transcripts belongs to which tab.
func TestAgentStatusReportsTheLedgerOnlyForAHookedSession(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	workspace := "/projects/alpha"
	directory := filepath.Join(configDir, "projects", strings.NewReplacer("/", "-", ".", "-").Replace(workspace))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	transcript := `{"type":"assistant","message":{"usage":{"input_tokens":2,` +
		`"cache_creation_input_tokens":1288,"cache_read_input_tokens":342813}}}` + "\n" +
		`{"type":"summary","totalCostUSD":1.25}` + "\n"
	if err := os.WriteFile(filepath.Join(directory, "session-1.jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	hooked, unhooked := new(os.File), new(os.File)
	previousGroup := foregroundProcessGroup
	previousList := runProcessList
	foregroundProcessGroup = func(terminal *os.File) (int, error) {
		if terminal == hooked {
			return 101, nil
		}
		return 102, nil
	}
	runProcessList = func(context.Context) ([]byte, error) { return []byte("101 claude\n102 claude\n"), nil }
	t.Cleanup(func() {
		foregroundProcessGroup = previousGroup
		runProcessList = previousList
	})

	server := &Server{
		sessions: map[string]*session{
			"tab-1": newSessionForTest(hooked),
			"tab-2": newSessionForTest(unhooked),
		},
		agentStatuses: map[string]agentRuntime{
			"tab-1": {
				AgentStatus: model.AgentStatus{Agent: model.AgentClaude, Phase: model.AgentPhaseWorking},
				SessionID:   "session-1",
			},
		},
		usage: usage.NewReader(),
	}
	server.value.Roots = []model.Root{{ID: "root-1", Path: workspace}}
	server.value.Workspaces = []model.Workspace{{ID: "workspace-1", RootID: "root-1", Path: workspace}}
	server.value.Tabs = []model.Tab{
		{ID: "tab-1", WorkspaceID: "workspace-1"},
		{ID: "tab-2", WorkspaceID: "workspace-1"},
	}

	want := map[string]model.AgentStatus{
		"tab-1": {
			Agent: model.AgentClaude, Phase: model.AgentPhaseWorking,
			ContextTokens: 2 + 1288 + 342813, CostUSD: 1.25,
		},
		// No hook, so no session to name a transcript with.
		"tab-2": {Agent: model.AgentClaude, Phase: model.AgentPhaseUnknown},
	}
	if got := server.agentStatusesSnapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("agentStatusesSnapshot() = %#v, want %#v", got, want)
	}

	snapshot := server.snapshot()
	if len(snapshot.Roots) != 1 || len(snapshot.Roots[0].Tabs) != 2 {
		t.Fatalf("snapshot tabs = %#v, want the two root tabs", snapshot.Roots)
	}
	got := snapshot.Roots[0].Tabs[0]
	if got.AgentContextTokens != 2+1288+342813 || got.AgentCostUSD != 1.25 {
		t.Fatalf("snapshot ledger = (%d, %v), want (%d, 1.25)",
			got.AgentContextTokens, got.AgentCostUSD, 2+1288+342813)
	}
}

// A tab whose agent has drawn nothing recognisable keeps the unknown phase
// rather than being guessed into a state it is not in.
func TestAgentStatusLeavesAnUnreadableScreenUnknown(t *testing.T) {
	terminal := new(os.File)
	previousGroup := foregroundProcessGroup
	previousList := runProcessList
	foregroundProcessGroup = func(*os.File) (int, error) { return 101, nil }
	runProcessList = func(context.Context) ([]byte, error) { return []byte("101 claude\n"), nil }
	t.Cleanup(func() {
		foregroundProcessGroup = previousGroup
		runProcessList = previousList
	})

	value := newSessionForTest(terminal)
	value.history.append([]byte("go: downloading github.com/example/module v1.2.3\r\n"))
	server := &Server{
		sessions:      map[string]*session{"tab-1": value},
		agentStatuses: map[string]agentRuntime{},
	}
	want := map[string]model.AgentStatus{"tab-1": {Agent: model.AgentClaude, Phase: model.AgentPhaseUnknown}}
	if got := server.agentStatusesSnapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("agentStatusesSnapshot() = %#v, want %#v", got, want)
	}
}

func TestAgentPhaseRecognizesActivityAfterPartialRedraws(t *testing.T) {
	previousGroup := foregroundProcessGroup
	previousList := runProcessList
	foregroundProcessGroup = func(*os.File) (int, error) { return 101, nil }
	runProcessList = func(context.Context) ([]byte, error) { return []byte("101 codex\n"), nil }
	t.Cleanup(func() {
		foregroundProcessGroup = previousGroup
		runProcessList = previousList
	})

	value := newSessionForTest(new(os.File))
	server := &Server{
		sessions:      map[string]*session{"tab-1": value},
		agentStatuses: make(map[string]agentRuntime),
	}
	value.broadcast([]byte("Working (1s • esc to interrupt)\r\n"))
	// A TUI redraws the spinner and elapsed time without repeating the hint.
	value.broadcast([]byte(strings.Repeat("\x1b[8;4H•\x1b[8;16H2s", phaseHintBytes)))
	if _, found := inferAgentPhase(value.history.tail(phaseHintBytes), ""); found {
		t.Fatal("partial redraw still contains a phase hint")
	}
	assertStatus := func(want model.AgentStatus) {
		t.Helper()
		if got := server.agentStatusesSnapshot()["tab-1"]; got != want {
			t.Fatalf("status = %#v, want %#v", got, want)
		}
	}
	assertStatus(model.AgentStatus{Agent: model.AgentCodex, Phase: model.AgentPhaseWorking, Estimated: true})
	value.mu.Lock()
	value.lastOutputAt = time.Now().Add(-agentActivityTimeout)
	value.mu.Unlock()
	assertStatus(model.AgentStatus{Agent: model.AgentCodex, Phase: model.AgentPhaseIdle, Estimated: true})
	if response := server.recordAgentEvent("tab-1", &protocol.AgentEvent{Agent: model.AgentCodex, HookEvent: "Stop"}); response.Error != "" {
		t.Fatalf("Stop hook error = %v", response.Error)
	}
	assertStatus(model.AgentStatus{Agent: model.AgentCodex, Phase: model.AgentPhaseIdle})
}

func TestAgentStatusUsesForegroundProcessAsPresenceAuthority(t *testing.T) {
	claudePTY := new(os.File)
	previousGroup := foregroundProcessGroup
	previousList := runProcessList
	foregroundProcessGroup = func(terminal *os.File) (int, error) { return 101, nil }
	runProcessList = func(context.Context) ([]byte, error) { return []byte("101 claude\n"), nil }
	t.Cleanup(func() {
		foregroundProcessGroup = previousGroup
		runProcessList = previousList
	})

	server := &Server{
		sessions: map[string]*session{
			"tab-1": newSessionForTest(claudePTY),
			"tab-2": newSessionForTest(new(os.File)),
		},
		agentStatuses: map[string]agentRuntime{
			"tab-1": {AgentStatus: model.AgentStatus{Agent: model.AgentClaude, Phase: model.AgentPhaseWaitingInput}},
			"tab-2": {AgentStatus: model.AgentStatus{Agent: model.AgentCodex, Phase: model.AgentPhaseIdle}},
		},
	}
	want := map[string]model.AgentStatus{
		"tab-1": {Agent: model.AgentClaude, Phase: model.AgentPhaseWaitingInput},
		// The foreground Claude process wins over the stale Codex hook identity.
		"tab-2": {Agent: model.AgentClaude, Phase: model.AgentPhaseUnknown},
	}
	if got := server.agentStatusesSnapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("agentStatusesSnapshot() = %#v, want %#v", got, want)
	}
}
