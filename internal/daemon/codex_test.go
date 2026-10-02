package daemon

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/opspresso/romty/internal/codexstate"
	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/protocol"
)

const codexSessionA = "01234567-89ab-7000-8000-123456789abc"
const codexSessionB = "fedcba98-7654-7000-8000-123456789abc"

type fakeCodexReader struct {
	states map[string]codexstate.State
	err    error
	onRead func()
}

func (r *fakeCodexReader) Read(context.Context, string, []string) (map[string]codexstate.State, error) {
	if r.onRead != nil {
		r.onRead()
	}
	return r.states, r.err
}
func (*fakeCodexReader) Close() {}

func codexSession(id string) *session {
	value := newSessionForTest(nil)
	value.codexHome = "/codex-home"
	value.guest.title = id[:29] + "... | project"
	return value
}

func TestCodexHookUsesTerminalIdentityInsteadOfSharedDaemonTabID(t *testing.T) {
	s := &Server{sessions: map[string]*session{"a": codexSession(codexSessionA), "b": codexSession(codexSessionB)}, agentStatuses: make(map[string]agentRuntime)}
	event := &protocol.AgentEvent{Agent: model.AgentCodex, SessionID: codexSessionB, HookEvent: "UserPromptSubmit"}
	if r := s.recordAgentEvent("a", event); r.Error != "" {
		t.Fatal(r.Error)
	}
	if _, ok := s.agentStatuses["a"]; ok {
		t.Fatal("inherited tab ID received another session's hook")
	}
	if got := s.agentStatuses["b"].Phase; got != model.AgentPhaseThinking {
		t.Fatalf("correct tab phase = %s", got)
	}
	event.HookEvent = "PreToolUse"
	if r := s.recordAgentEvent("", event); r.Error != "" {
		t.Fatal(r.Error)
	}
	if s.agentStatuses["b"].Phase != model.AgentPhaseWorking {
		t.Fatal("server started outside romty could not route its hook")
	}
	s.sessions["a"].guest.title = s.sessions["b"].guest.title
	event.HookEvent = "Stop"
	s.recordAgentEvent("b", event)
	if s.agentStatuses["b"].Phase != model.AgentPhaseWorking {
		t.Fatal("ambiguous identity was guessed")
	}
}

func TestNativeCodexReadUpdatesPlainCodexAndInvalidatesStaleState(t *testing.T) {
	previousGroup, previousList := foregroundProcessGroup, runProcessList
	foregroundProcessGroup = func(*os.File) (int, error) { return 101, nil }
	process := "codex"
	runProcessList = func(context.Context) ([]byte, error) { return []byte("101 " + process + "\n"), nil }
	t.Cleanup(func() { foregroundProcessGroup, runProcessList = previousGroup, previousList })
	value := codexSession(codexSessionA)
	reader := &fakeCodexReader{states: map[string]codexstate.State{codexSessionA[:29]: {
		AgentStatus: model.AgentStatus{Agent: model.AgentCodex, Phase: model.AgentPhaseWorking, Source: "runtime", Active: true}, SessionID: codexSessionA,
	}}}
	s := &Server{sessions: map[string]*session{"tab": value}, agentStatuses: make(map[string]agentRuntime), codex: reader}
	if got := s.agentStatusesSnapshot()["tab"]; got.Phase != model.AgentPhaseWorking || got.SessionID != codexSessionA {
		t.Fatalf("native status = %+v", got)
	}
	reader.err = errors.New("server unavailable")
	if got := s.agentStatusesSnapshot()["tab"].Phase; got != model.AgentPhaseUnknown {
		t.Fatalf("lost server left %s latched", got)
	}
	reader.err = nil
	reader.onRead = func() { value.broadcast([]byte("\x1b]0;" + codexSessionB + "\x07")) }
	if got := s.agentStatusesSnapshot()["tab"].Phase; got != model.AgentPhaseUnknown {
		t.Fatalf("late read updated a different thread: %s", got)
	}
	reader.onRead = nil
	value.broadcast([]byte("\x1b]0;" + codexSessionA[:29] + "... | project\x07"))
	if s.agentStatusesSnapshot()["tab"].Phase != model.AgentPhaseWorking {
		t.Fatal("native status was not re-established")
	}
	process = "zsh"
	value.broadcast([]byte("\x1b]0;project shell\x07"))
	s.agentStatusesSnapshot()
	if _, ok := s.agentStatuses["tab"]; ok {
		t.Fatal("exited Codex retained a resume identity")
	}
}

func TestSessionCodexHomeUsesTheClientEnvironment(t *testing.T) {
	if got := sessionCodexHome([]string{"HOME=/client", "CODEX_HOME="}, "/workspace"); got != "/client/.codex" {
		t.Fatal(got)
	}
	if got := sessionCodexHome([]string{"HOME=/client", "CODEX_HOME=custom"}, "/workspace"); got != "/workspace/custom" {
		t.Fatal(got)
	}
	if strings.Contains(sessionCodexHome([]string{"CODEX_HOME=/selected"}, "/workspace"), "workspace") {
		t.Fatal("absolute Codex home changed")
	}
}

func TestCodexResumeDoesNotNameAThreadThatLeftTheTerminal(t *testing.T) {
	value := codexSession(codexSessionB)
	s := &Server{sessions: map[string]*session{"tab": value}, agentStatuses: map[string]agentRuntime{
		"tab": {AgentStatus: model.AgentStatus{Agent: model.AgentCodex, Source: "runtime"}, SessionID: codexSessionA},
	}}
	s.value.Tabs = []model.Tab{{ID: "tab", WorkspaceID: "workspace"}}
	if saves := s.resumeSnapshotsLocked(); len(saves) != 1 || saves[0].meta.AgentSessionID != "" || saves[0].meta.Agent != model.AgentCodex {
		t.Fatalf("resume retained a different thread: %+v", saves)
	}
	value.guest.title = "shell"
	if saves := s.resumeSnapshotsLocked(); saves[0].meta.Agent != "" {
		t.Fatal("resume retained an exited agent")
	}
}
