package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/model"
)

func attentionSnapshot(phases ...model.AgentPhase) model.Snapshot {
	workspace := model.Workspace{ID: "workspace-1", RootID: projectsRoot.ID, Name: "alpha", Path: "/projects/alpha"}
	tabs := make([]model.Tab, len(phases))
	for index, phase := range phases {
		tabs[index] = model.Tab{ID: "tab-" + string(rune('1'+index)), WorkspaceID: workspace.ID,
			Name: string(rune('1' + index)), Running: true, Agent: model.AgentCodex, AgentPhase: phase}
	}
	return projectsSnapshot([]model.WorkspaceView{{Workspace: workspace, Tabs: tabs}})
}

// notifications runs command once and returns the OSC 9 sequences it wrote.
// A Tick command fires only once, so a command is never run twice.
func notifications(command tea.Cmd) []string {
	return notificationsIn(commandMessages(command))
}

func notificationsIn(messages []tea.Msg) []string {
	var result []string
	for _, message := range messages {
		if raw, ok := message.(tea.RawMsg); ok {
			if text, ok := raw.Msg.(string); ok && strings.HasPrefix(text, "\x1b]9;") {
				result = append(result, text)
			}
		}
	}
	return result
}

// A notification is the alert that reaches a user who connects over SSH, so
// it is on by default and names the agent, its need, and where it runs.
func TestAgentWaitingSendsADesktopNotification(t *testing.T) {
	value := newDashboard(&fakeBackend{}, attentionSnapshot(model.AgentPhaseWorking))
	value.agentSoundReady = true

	command := send(&value, agentSnapshotMsg{value: map[string]model.AgentStatus{
		"tab-1": {Agent: model.AgentCodex, Phase: model.AgentPhaseWaitingApproval},
	}})
	messages := commandMessages(command)
	got := notificationsIn(messages)
	want := ansi.Notify("romty Codex: Needs your approval · alpha / 1")
	if len(got) != 1 || got[0] != want {
		t.Fatalf("notifications = %q, want %q", got, want)
	}
	for _, message := range messages {
		if _, ok := message.(soundPlayedMsg); ok {
			t.Fatal("a sound played with the sounds off")
		}
	}

	// A phase that stays put does not notify again.
	if got := notifications(send(&value, agentSnapshotMsg{value: map[string]model.AgentStatus{
		"tab-1": {Agent: model.AgentCodex, Phase: model.AgentPhaseWaitingApproval},
	}})); len(got) != 0 {
		t.Fatalf("stable phase notified again: %q", got)
	}
}

func TestMutedNotificationsAndTheFirstSnapshotStaySilent(t *testing.T) {
	waiting := map[string]model.AgentStatus{"tab-1": {Agent: model.AgentCodex, Phase: model.AgentPhaseWaitingInput}}

	first := newDashboard(&fakeBackend{}, attentionSnapshot(model.AgentPhaseWorking))
	if got := notifications(send(&first, agentSnapshotMsg{value: waiting})); len(got) != 0 {
		t.Fatalf("first agent snapshot notified: %q", got)
	}

	muted := newDashboardWithConfig(&fakeBackend{}, attentionSnapshot(model.AgentPhaseWorking), "",
		Config{MuteNotifications: true})
	muted.agentSoundReady = true
	if got := notifications(send(&muted, agentSnapshotMsg{value: waiting})); len(got) != 0 {
		t.Fatalf("muted notifications notified: %q", got)
	}
}

func TestSeveralAlertsShareOneNotification(t *testing.T) {
	value := newDashboard(&fakeBackend{}, attentionSnapshot(model.AgentPhaseWorking, model.AgentPhaseWorking))
	value.agentSoundReady = true
	got := notifications(send(&value, agentSnapshotMsg{value: map[string]model.AgentStatus{
		"tab-1": {Agent: model.AgentCodex, Phase: model.AgentPhaseWaitingInput},
		"tab-2": {Agent: model.AgentCodex, Phase: model.AgentPhaseWaitingApproval},
	}}))
	if len(got) != 1 || !strings.Contains(got[0], "Needs your answer · alpha / 1 (+1 more)") {
		t.Fatalf("notifications = %q, want one naming the first and counting the rest", got)
	}
}

// Agents that need the user are counted where they stay visible: the tree's
// title, the window title, and the rail when the narrow layout hides the tree.
func TestAttentionIsCountedInTheTreeTitleWindowTitleAndRail(t *testing.T) {
	value := newDashboard(&fakeBackend{}, attentionSnapshot(
		model.AgentPhaseWaitingInput, model.AgentPhaseWaitingInput, model.AgentPhaseWaitingApproval,
		model.AgentPhaseError, model.AgentPhaseWorking))
	value.hostname = "build-01"

	if got := value.attention().summary(); got != "▲2 ■1 ★1" {
		t.Fatalf("attention summary = %q", got)
	}
	header := ansi.Strip(value.renderNavigation(30, 10)[0])
	if !strings.HasSuffix(strings.TrimRight(header, " "), "▲2 ■1 ★1") || !strings.HasPrefix(header, " romty") {
		t.Fatalf("tree title = %q, want the badge at its right end", header)
	}
	if got := value.windowTitle(); got != "romty · build-01 · ▲2 ■1 ★1" {
		t.Fatalf("window title = %q", got)
	}

	// A pane too narrow for both keeps the badge and gives up the title.
	if header := ansi.Strip(value.renderNavigation(14, 10)[0]); !strings.Contains(header, "▲2 ■1 ★1") {
		t.Fatalf("narrow tree title = %q, want the badge kept", header)
	}

	value.width = 60
	value.focus = terminalPane
	value.terminal = newEmbeddedTerminal("tab-5", newMemoryStream(""), 30, 10)
	t.Cleanup(func() { value.closeTerminal() })
	if got := value.railNote(); !strings.HasPrefix(got, "▲2 ■1 ★1 waiting") {
		t.Fatalf("rail note with the tree hidden = %q", got)
	}
	value.width = 120
	if got := value.railNote(); strings.Contains(got, "waiting") {
		t.Fatalf("rail note with the tree on screen = %q, want no count", got)
	}
}
