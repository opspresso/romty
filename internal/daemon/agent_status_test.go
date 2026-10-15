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
	"github.com/opspresso/romty/internal/testutil"
	"github.com/opspresso/romty/internal/transcript"
)

// stubForeground reports groupOf as each terminal's foreground process group
// and processes ("pid ppid pgid tpgid command" lines) as the process list.
func stubForeground(t *testing.T, groupOf func(*os.File) int, processes string) {
	t.Helper()
	testutil.Set(t, &foregroundProcessGroup, func(terminal *os.File) (int, error) { return groupOf(terminal), nil })
	testutil.Set(t, &runProcessList, func(context.Context) ([]byte, error) { return []byte(processes), nil })
}

// group puts every terminal in the same foreground process group.
func group(id int) func(*os.File) int {
	return func(*os.File) int { return id }
}

// writeClaudeTranscript writes a session transcript under a fresh Claude
// config directory and returns its path, the one a hook would report.
func writeClaudeTranscript(t *testing.T, workspace, sessionID, records string) string {
	t.Helper()
	configDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	directory := filepath.Join(configDir, "projects", strings.NewReplacer("/", "-", ".", "-", "_", "-", " ", "-").Replace(workspace))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	path := filepath.Join(directory, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(records), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

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
		{name: "idle notification", event: protocol.AgentEvent{HookEvent: "Notification", NotificationType: "idle_prompt"}, want: model.AgentPhaseCompleted},
		{name: "notification input", event: protocol.AgentEvent{HookEvent: "Notification", NotificationType: "agent_needs_input"}, want: model.AgentPhaseWaitingInput},
		{name: "compact", event: protocol.AgentEvent{HookEvent: "PreCompact"}, want: model.AgentPhaseCompacting},
		{name: "stop", event: protocol.AgentEvent{HookEvent: "Stop"}, want: model.AgentPhaseStopped},
		{name: "interrupt", event: protocol.AgentEvent{HookEvent: "Interrupt"}, want: model.AgentPhaseInterrupted},
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

func TestAgentLifecycleRejectsOtherTurnsAndSubagents(t *testing.T) {
	for _, agent := range []model.Agent{model.AgentCodex, model.AgentClaude} {
		t.Run(string(agent), func(t *testing.T) {
			value := newSessionForTest(nil)
			value.guest.title = "01234567-89ab-7000-8000-12345... | project"
			server := &Server{sessions: map[string]*session{"tab": value}, agentStatuses: make(map[string]agentRuntime)}
			report := func(event protocol.AgentEvent, want model.AgentPhase) {
				t.Helper()
				event.Agent, event.SessionID = agent, "01234567-89ab-7000-8000-123456789abc"
				if response := server.recordAgentEvent("tab", &event); response.Error != "" {
					t.Fatal(response.Error)
				}
				if got := server.agentStatuses["tab"].Phase; got != want {
					t.Fatalf("%+v: phase = %s, want %s", event, got, want)
				}
			}
			report(protocol.AgentEvent{HookEvent: "UserPromptSubmit", TurnID: "one"}, model.AgentPhaseThinking)
			report(protocol.AgentEvent{HookEvent: "PreToolUse", TurnID: "one"}, model.AgentPhaseWorking)
			report(protocol.AgentEvent{HookEvent: "Stop", TurnID: "one", AgentID: "child"}, model.AgentPhaseWorking)
			report(protocol.AgentEvent{HookEvent: "Stop", TurnID: "one"}, model.AgentPhaseStopped)
			report(protocol.AgentEvent{HookEvent: "PreToolUse", TurnID: "one", AgentID: "child"}, model.AgentPhaseStopped)
			report(protocol.AgentEvent{HookEvent: "SessionStart", AgentID: "child"}, model.AgentPhaseStopped)
			report(protocol.AgentEvent{HookEvent: "UserPromptSubmit", TurnID: "two"}, model.AgentPhaseThinking)
			report(protocol.AgentEvent{HookEvent: "Stop", TurnID: "one"}, model.AgentPhaseThinking)
			report(protocol.AgentEvent{HookEvent: "PermissionRequest", TurnID: "two"}, model.AgentPhaseWaitingApproval)
			report(protocol.AgentEvent{HookEvent: "PostToolUse", TurnID: "two"}, model.AgentPhaseThinking)
			report(protocol.AgentEvent{HookEvent: "Interrupt", TurnID: "two"}, model.AgentPhaseInterrupted)
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
		Agent: model.AgentClaude, HookEvent: "SessionStart",
	})
	if response.Error == "" {
		t.Fatal("agent event for a missing tab succeeded")
	}
}

func TestNodeStatusReportsPresenceWithoutAgentPhases(t *testing.T) {
	testutil.Set(t, &foregroundProcessGroup, func(*os.File) (int, error) { return 101, nil })
	command := "node server.js"
	testutil.Set(t, &runProcessList, func(context.Context) ([]byte, error) { return []byte("101 1 101 101 " + command + "\n"), nil })

	value := newSessionForTest(nil)
	value.broadcast([]byte("Server ready. Press Enter to continue.\r\n"))
	server := &Server{
		sessions: map[string]*session{"tab": value},
		agentStatuses: map[string]agentRuntime{
			"tab": {AgentStatus: model.AgentStatus{Agent: model.AgentClaude, Phase: model.AgentPhaseWaitingApproval}},
		},
	}
	want := map[string]model.AgentStatus{"tab": {Agent: "node", Phase: model.AgentPhaseUnknown}}
	if got := server.agentStatusesSnapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("node status = %#v, want %#v", got, want)
	}
	command = "zsh"
	if got := server.agentStatusesSnapshot(); len(got) != 0 {
		t.Fatalf("shell status = %#v, want Node presence cleared", got)
	}
}

func TestQuestionRemainsPendingWhileOtherToolsFinish(t *testing.T) {
	s := &Server{sessions: map[string]*session{"tab": nil}, agentStatuses: make(map[string]agentRuntime)}
	report := func(hook, tool, id string) {
		t.Helper()
		if r := s.recordAgentEvent("tab", &protocol.AgentEvent{Agent: model.AgentClaude, SessionID: "session", TurnID: "prompt", HookEvent: hook, ToolName: tool, ToolUseID: id}); r.Error != "" {
			t.Fatal(r.Error)
		}
	}
	report("PreToolUse", "AskUserQuestion", "question")
	report("PreToolUse", "Bash", "build")
	report("PostToolUse", "Bash", "build")
	if got := s.agentStatuses["tab"].Phase; got != model.AgentPhaseWaitingInput {
		t.Fatalf("unrelated tool cleared the question: %s", got)
	}
	report("PostToolUse", "AskUserQuestion", "question")
	if got := s.agentStatuses["tab"].Phase; got != model.AgentPhaseThinking {
		t.Fatalf("answered question did not resume: %s", got)
	}
	report("Stop", "", "")
	if got := s.agentStatuses["tab"].Phase; got != model.AgentPhaseStopped {
		t.Fatalf("Stop must be provisional: %s", got)
	}
	report("PostToolUse", "Bash", "build")
	if got := s.agentStatuses["tab"].Phase; got != model.AgentPhaseStopped {
		t.Fatalf("late tool result restarted the response: %s", got)
	}
	report("PreToolUse", "Bash", "continuation")
	if got := s.agentStatuses["tab"].Phase; got != model.AgentPhaseWorking {
		t.Fatalf("blocked Stop did not continue: %s", got)
	}
	report("Elicitation", "", "")
	report("Elicitation", "", "")
	report("PostToolUse", "Bash", "parallel")
	report("ElicitationResult", "", "")
	if got := s.agentStatuses["tab"].Phase; got != model.AgentPhaseWaitingInput {
		t.Fatalf("another elicitation is still pending: %s", got)
	}
	report("ElicitationResult", "", "")
	if got := s.agentStatuses["tab"].Phase; got != model.AgentPhaseWorking {
		t.Fatalf("all elicitations resolved while a tool still runs: %s", got)
	}
	report("PostToolUse", "Bash", "continuation")
	if got := s.agentStatuses["tab"].Phase; got != model.AgentPhaseThinking {
		t.Fatalf("every tool and elicitation resolved: %s", got)
	}
}

// A tool that finishes cannot see a sibling that still runs. The turn stays
// working until the last running tool ends, and plan mode stays planning.
func TestAFinishedParallelToolKeepsTheTurnWorking(t *testing.T) {
	s := &Server{sessions: map[string]*session{"tab": nil}, agentStatuses: make(map[string]agentRuntime)}
	report := func(hook, id, mode string, want model.AgentPhase) {
		t.Helper()
		if r := s.recordAgentEvent("tab", &protocol.AgentEvent{Agent: model.AgentClaude, SessionID: "session", TurnID: "prompt", HookEvent: hook, ToolName: "Bash", ToolUseID: id, PermissionMode: mode}); r.Error != "" {
			t.Fatal(r.Error)
		}
		if got := s.agentStatuses["tab"].Phase; got != want {
			t.Fatalf("%s %s: phase = %s, want %s", hook, id, got, want)
		}
	}
	report("UserPromptSubmit", "", "", model.AgentPhaseThinking)
	report("PreToolUse", "slow", "", model.AgentPhaseWorking)
	report("PreToolUse", "fast", "", model.AgentPhaseWorking)
	report("PostToolUse", "fast", "", model.AgentPhaseWorking)
	report("PermissionRequest", "gated", "", model.AgentPhaseWaitingApproval)
	report("PermissionDenied", "gated", "", model.AgentPhaseWorking)
	report("PostToolUseFailure", "slow", "", model.AgentPhaseThinking)
	report("PreToolUse", "plan-a", "plan", model.AgentPhasePlanning)
	report("PreToolUse", "plan-b", "plan", model.AgentPhasePlanning)
	report("PostToolUse", "plan-a", "plan", model.AgentPhasePlanning)
	report("Stop", "", "", model.AgentPhaseStopped)
	if tools := s.agentStatuses["tab"].Tools; len(tools) != 0 {
		t.Fatalf("an ended turn kept running tools: %v", tools)
	}
}

// Claude Code runs no Stop hook for an interrupt. Its idle notification then
// ends the open turn as interrupted, and confirms only a turn that stopped.
func TestIdleNotificationSettlesTheTurnItFollows(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		before string
		want   model.AgentPhase
	}{
		{name: "after stop", before: "Stop", want: model.AgentPhaseCompleted},
		{name: "without stop", before: "PreToolUse", want: model.AgentPhaseInterrupted},
		{name: "after interrupt", before: "Interrupt", want: model.AgentPhaseInterrupted},
		{name: "after failure", before: "StopFailure", want: model.AgentPhaseError},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			s := &Server{sessions: map[string]*session{"tab": nil}, agentStatuses: make(map[string]agentRuntime)}
			for _, hook := range []string{"UserPromptSubmit", testCase.before} {
				s.recordAgentEvent("tab", &protocol.AgentEvent{Agent: model.AgentClaude, SessionID: "session", HookEvent: hook, ToolUseID: "tool"})
			}
			s.recordAgentEvent("tab", &protocol.AgentEvent{Agent: model.AgentClaude, SessionID: "session", HookEvent: "Notification", NotificationType: "idle_prompt"})
			if got := s.agentStatuses["tab"].Phase; got != testCase.want {
				t.Fatalf("phase = %s, want %s", got, testCase.want)
			}
		})
	}
}

func TestTranscriptInterruptEndsOnlyANewerOpenTurn(t *testing.T) {
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	running := agentRuntime{
		AgentStatus: model.AgentStatus{Agent: model.AgentClaude, Phase: model.AgentPhaseWorking},
		Tools:       map[string]model.AgentPhase{"tool": model.AgentPhaseWorking},
		UpdatedAt:   at,
	}
	if got, changed := applyTranscriptInterrupt(running, at.Add(time.Second)); !changed || got.Phase != model.AgentPhaseInterrupted || len(got.Tools) != 0 {
		t.Fatalf("newer interrupt = (%+v, %v), want interrupted without tools", got, changed)
	}
	if _, changed := applyTranscriptInterrupt(running, at.Add(-time.Second)); changed {
		t.Fatal("an interrupt older than the newest hook ended the turn")
	}
	waiting := running
	waiting.Phase = model.AgentPhaseWaitingApproval
	if got, changed := applyTranscriptInterrupt(waiting, at.Add(time.Second)); !changed || got.Phase != model.AgentPhaseInterrupted {
		t.Fatalf("rejected approval = (%+v, %v), want interrupted", got, changed)
	}
	stopped := running
	stopped.Phase = model.AgentPhaseStopped
	if _, changed := applyTranscriptInterrupt(stopped, at.Add(time.Second)); changed {
		t.Fatal("an interrupt replaced a turn that already stopped")
	}
}

// Terminal output cannot establish Codex/Claude lifecycle state.
func TestAgentStatusRequiresLifecycleReportsForCodexAndClaude(t *testing.T) {
	hooked, unhooked := new(os.File), new(os.File)
	titled := new(os.File)
	stubForeground(t, func(terminal *os.File) int {
		switch terminal {
		case hooked:
			return 101
		case unhooked:
			return 102
		default:
			return 103
		}
	}, "101 1 101 101 claude\n102 1 102 102 claude\n103 1 103 103 codex\n")

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
		"tab-2": {Agent: model.AgentClaude, Phase: model.AgentPhaseUnknown},
		"tab-3": {Agent: model.AgentCodex, Phase: model.AgentPhaseUnknown},
	}
	if got := server.agentStatusesSnapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("agentStatusesSnapshot() = %#v, want %#v", got, want)
	}
}

// The counters need the session identifier and transcript path, which only a
// hook reports. Without them romty cannot tell which transcript is the tab's.
func TestAgentStatusReportsTheLedgerOnlyForAHookedSession(t *testing.T) {
	workspace := "/projects/alpha"
	path := writeClaudeTranscript(t, workspace, "session-1", `{"type":"assistant","message":{"usage":{"input_tokens":2,`+
		`"cache_creation_input_tokens":1288,"cache_read_input_tokens":342813}}}`+"\n"+
		`{"type":"summary","totalCostUSD":1.25}`+"\n")

	hooked, unhooked := new(os.File), new(os.File)
	stubForeground(t, func(terminal *os.File) int {
		if terminal == hooked {
			return 101
		}
		return 102
	}, "101 1 101 101 claude\n102 1 102 102 claude\n")

	server := &Server{
		sessions: map[string]*session{
			"tab-1": newSessionForTest(hooked),
			"tab-2": newSessionForTest(unhooked),
		},
		agentStatuses: map[string]agentRuntime{
			"tab-1": {
				AgentStatus:    model.AgentStatus{Agent: model.AgentClaude, Phase: model.AgentPhaseWorking, SessionID: "session-1"},
				TranscriptPath: path,
			},
		},
		transcripts: transcript.NewReader(),
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
			ContextTokens: 2 + 1288 + 342813, CostUSD: 1.25, SessionID: "session-1",
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
	stubForeground(t, group(101), "101 1 101 101 claude\n")
	value := newSessionForTest(new(os.File))
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

func TestAgentStatusDoesNotGuessCodexLifecycleFromRedraws(t *testing.T) {
	stubForeground(t, group(101), "101 1 101 101 codex\n")

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
	assertStatus(model.AgentStatus{Agent: model.AgentCodex, Phase: model.AgentPhaseUnknown})
	value.mu.Lock()
	value.lastOutputAt = time.Now().Add(-agentActivityTimeout)
	value.mu.Unlock()
	assertStatus(model.AgentStatus{Agent: model.AgentCodex, Phase: model.AgentPhaseUnknown})
	if response := server.recordAgentEvent("tab-1", &protocol.AgentEvent{Agent: model.AgentCodex, HookEvent: "Stop"}); response.Error != "" {
		t.Fatalf("Stop hook error = %v", response.Error)
	}
	assertStatus(model.AgentStatus{Agent: model.AgentCodex, Phase: model.AgentPhaseUnknown})
}

func TestAgentStatusUsesForegroundProcessAsPresenceAuthority(t *testing.T) {
	claudePTY := new(os.File)
	stubForeground(t, group(101), "101 1 101 101 claude\n")

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

// An interrupt runs no Claude Code hook. The snapshot reads it from the
// session's transcript and keeps the interrupted phase for later snapshots.
func TestAgentStatusEndsATurnTheTranscriptRecordsAsInterrupted(t *testing.T) {
	workspace := "/projects/alpha"
	path := writeClaudeTranscript(t, workspace, "session-1",
		`{"type":"user","timestamp":"2026-10-15T09:00:05.000Z","message":{"content":[{"type":"text","text":"[Request interrupted by user]"}]}}`+"\n")
	stubForeground(t, group(101), "101 1 101 101 claude\n")

	server := &Server{
		sessions: map[string]*session{"tab-1": newSessionForTest(new(os.File))},
		agentStatuses: map[string]agentRuntime{"tab-1": {
			AgentStatus:    model.AgentStatus{Agent: model.AgentClaude, Phase: model.AgentPhaseWorking, SessionID: "session-1"},
			Tools:          map[string]model.AgentPhase{"build": model.AgentPhaseWorking},
			UpdatedAt:      time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC),
			TranscriptPath: path,
		}},
		transcripts: transcript.NewReader(),
	}
	server.value.Workspaces = []model.Workspace{{ID: "workspace-1", Path: workspace}}
	server.value.Tabs = []model.Tab{{ID: "tab-1", WorkspaceID: "workspace-1"}}

	if got := server.agentStatusesSnapshot()["tab-1"].Phase; got != model.AgentPhaseInterrupted {
		t.Fatalf("snapshot phase = %s, want interrupted", got)
	}
	if runtime := server.agentStatuses["tab-1"]; runtime.Phase != model.AgentPhaseInterrupted || len(runtime.Tools) != 0 {
		t.Fatalf("recorded runtime = %+v, want interrupted without tools", runtime)
	}
}

// OpenCode 2 runs plugins in a shared background service whose environment can
// carry another tab's ID. Only a hook below the tab's foreground job counts.
func TestOpenCodeEventsCountOnlyFromTheTabsForegroundJob(t *testing.T) {
	terminal := new(os.File)
	testutil.Set(t, &foregroundProcessGroup, func(*os.File) (int, error) { return 4242, nil })
	server := &Server{sessions: map[string]*session{"tab": newSessionForTest(terminal)}, agentStatuses: make(map[string]agentRuntime)}
	report := func(groups ...int) {
		t.Helper()
		event := protocol.AgentEvent{Agent: model.AgentOpenCode, SessionID: "ses-1", HookEvent: "UserPromptSubmit", ProcessGroups: groups}
		if response := server.recordAgentEvent("tab", &event); response.Error != "" {
			t.Fatal(response.Error)
		}
	}
	report(7, 99, 1)
	if _, ok := server.agentStatuses["tab"]; ok {
		t.Fatal("a hook outside the tab's foreground job reported for it")
	}
	report()
	if _, ok := server.agentStatuses["tab"]; ok {
		t.Fatal("a hook without ancestry reported for the tab")
	}
	report(7, 4242, 1)
	if got := server.agentStatuses["tab"].Phase; got != model.AgentPhaseThinking {
		t.Fatalf("foreground hook phase = %s, want thinking", got)
	}
}

// Kiro runs hooks from its agent server below the tab's foreground job. Stop
// runs only for a completed turn; a cancelled one runs no hook.
func TestKiroEventsCountFromTheForegroundJobAndStopCompletes(t *testing.T) {
	terminal := new(os.File)
	testutil.Set(t, &foregroundProcessGroup, func(*os.File) (int, error) { return 4242, nil })
	server := &Server{sessions: map[string]*session{"tab": newSessionForTest(terminal)}, agentStatuses: make(map[string]agentRuntime)}
	report := func(name string, groups ...int) {
		t.Helper()
		event := protocol.AgentEvent{Agent: model.AgentKiro, SessionID: "sess_1", HookEvent: name, ProcessGroups: groups}
		if response := server.recordAgentEvent("tab", &event); response.Error != "" {
			t.Fatal(response.Error)
		}
	}
	report("UserPromptSubmit", 7, 1)
	if _, ok := server.agentStatuses["tab"]; ok {
		t.Fatal("a Kiro hook outside the tab's foreground job reported for it")
	}
	report("UserPromptSubmit", 8304, 4242, 1)
	report("PreToolUse", 8304, 4242, 1)
	if got := server.agentStatuses["tab"].Phase; got != model.AgentPhaseWorking {
		t.Fatalf("tool phase = %s, want working", got)
	}
	report("Stop", 8304, 4242, 1)
	if got := server.agentStatuses["tab"].Phase; got != model.AgentPhaseCompleted {
		t.Fatalf("Stop phase = %s, want completed", got)
	}
}

// Claude Code names a project's transcript folder by its own rules, which
// change between versions. The daemon reads the path the hook reports, so a
// workspace whose name holds an underscore or a space still gets its counters
// and its interrupt.
func TestAgentStatusReadsTheTranscriptPathTheHookReports(t *testing.T) {
	workspace := "/projects/my_proj name"
	path := writeClaudeTranscript(t, workspace, "session-1",
		`{"type":"assistant","message":{"usage":{"input_tokens":3,"cache_creation_input_tokens":4,"cache_read_input_tokens":5}}}`+"\n")
	stubForeground(t, group(101), "101 1 101 101 claude\n")
	server := &Server{
		sessions:      map[string]*session{"tab-1": newSessionForTest(new(os.File))},
		agentStatuses: make(map[string]agentRuntime),
		transcripts:   transcript.NewReader(),
	}
	server.value.Workspaces = []model.Workspace{{ID: "workspace-1", Path: workspace}}
	server.value.Tabs = []model.Tab{{ID: "tab-1", WorkspaceID: "workspace-1"}}

	event := protocol.AgentEvent{
		Agent: model.AgentClaude, SessionID: "session-1", HookEvent: "UserPromptSubmit", TranscriptPath: path,
	}
	if response := server.recordAgentEvent("tab-1", &event); response.Error != "" {
		t.Fatalf("recordAgentEvent() error = %s", response.Error)
	}
	if got := server.agentStatusesSnapshot()["tab-1"]; got.ContextTokens != 12 {
		t.Fatalf("status = %+v, want the 12 tokens of the reported transcript", got)
	}
}
