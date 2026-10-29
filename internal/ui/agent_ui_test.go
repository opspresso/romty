package ui

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/opspresso/romty/internal/agenthooks"
	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/sound"
	"github.com/opspresso/romty/internal/testutil"
)

func TestDashboardConfirmsAgentHookInstallation(t *testing.T) {
	statuses := []agenthooks.Status{
		{Provider: agenthooks.ProviderClaude, State: agenthooks.StateMissing, Path: "/home/me/.claude/settings.json"},
		{Provider: agenthooks.ProviderCodex, State: agenthooks.StateOutdated, Path: "/home/me/.codex/hooks.json"},
	}
	value := newDashboard(&fakeBackend{}, projectsSnapshot(nil))
	value.offerAgentHooks(statuses)
	if value.modal != hookInstallModal {
		t.Fatalf("hook check modal = %v, want confirmation", value.modal)
	}
	plain := ansi.Strip(strings.Join(value.renderModal(100, 30), "\n"))
	for _, want := range []string{"Claude Code: install", "Codex: update", "Existing settings and other hooks are preserved."} {
		if !strings.Contains(plain, want) {
			t.Fatalf("hook confirmation does not contain %q:\n%s", want, plain)
		}
	}

	var installed []agenthooks.Provider
	testutil.Set(t, &installHookProviders, func(providers []agenthooks.Provider) ([]agenthooks.Result, error) {
		installed = append(installed, providers...)
		return []agenthooks.Result{
			{Provider: agenthooks.ProviderClaude, Action: agenthooks.ActionInstalled},
			{Provider: agenthooks.ProviderCodex, Action: agenthooks.ActionUpdated},
		}, nil
	})

	command := send(&value, key(tea.KeyEnter, ""))
	if command == nil || !value.hookInstallPending || len(installed) != 0 {
		t.Fatalf("Enter = (command %v, pending %v, installed %v), want deferred install", command, value.hookInstallPending, installed)
	}
	if status := ansi.Strip(value.renderStatus(100, 30)[1]); !strings.Contains(status, "INSTALLING") {
		t.Fatalf("pending status = %q", status)
	}

	extra := send(&value, commandMessage[hooksInstalledMsg](t, command))
	if extra != nil || value.modal != noModal || value.hookInstallPending {
		t.Fatalf("install result = (extra %v, modal %v, pending %v)", extra, value.modal, value.hookInstallPending)
	}
	if !slices.Equal(installed, []agenthooks.Provider{agenthooks.ProviderClaude, agenthooks.ProviderCodex}) {
		t.Fatalf("installed providers = %v", installed)
	}
	if !value.noticeMessage || !strings.Contains(value.errorMessage, "Claude Code and Codex hooks installed") {
		t.Fatalf("install notice = (%v, %q)", value.noticeMessage, value.errorMessage)
	}
}

func TestDashboardCanSkipAgentHookInstallation(t *testing.T) {
	value := newDashboard(&fakeBackend{}, projectsSnapshot(nil))
	value.offerAgentHooks([]agenthooks.Status{{
		Provider: agenthooks.ProviderClaude,
		State:    agenthooks.StateMissing,
	}})

	command := send(&value, key(tea.KeyEscape, ""))
	if command != nil || value.modal != noModal || value.hookStatuses != nil {
		t.Fatalf("Esc = (command %v, modal %v, statuses %v), want no changes", command, value.modal, value.hookStatuses)
	}
}

func TestStatusRailExplainsSkippedAgentHooks(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root"}, Tabs: []model.Tab{{ID: "tab", Running: true}},
	}}})
	value.offerAgentHooks([]agenthooks.Status{
		{Provider: agenthooks.ProviderClaude, State: agenthooks.StateMissing},
		{Provider: agenthooks.ProviderCodex, State: agenthooks.StateCurrent},
	})
	send(&value, key(tea.KeyEscape, ""))
	tab, ok := value.openTab()
	if !ok {
		t.Fatal("no open tab")
	}
	note := func(agent model.Agent, phase model.AgentPhase) string {
		value.updateAgents(map[string]model.AgentStatus{tab.ID: {Agent: agent, Phase: phase}})
		return value.agentStatusNote()
	}
	if got := note(model.AgentClaude, model.AgentPhaseUnknown); got != "Status unavailable · run romty hooks" {
		t.Fatalf("skipped Claude note = %q", got)
	}
	if got := note(model.AgentCodex, model.AgentPhaseUnknown); got != "Status unavailable" {
		t.Fatalf("current Codex note = %q", got)
	}
	if got := note(model.AgentClaude, model.AgentPhaseWorking); got != "Working" {
		t.Fatalf("reporting Claude note = %q", got)
	}
}

func TestDashboardCanQuitDuringAgentHookInstallation(t *testing.T) {
	for _, pending := range []bool{false, true} {
		value := newDashboard(&fakeBackend{}, projectsSnapshot(nil))
		value.offerAgentHooks([]agenthooks.Status{{
			Provider: agenthooks.ProviderClaude,
			State:    agenthooks.StateMissing,
		}})
		value.hookInstallPending = pending

		command := send(&value, key(tea.KeyF4, ""))
		if !value.result.Quit || command == nil {
			t.Fatalf("F4 with pending %v = (quit %v, command %v), want quit", pending, value.result.Quit, command)
		}
	}
}

func TestDashboardDoesNotPromptForCurrentAgentHooks(t *testing.T) {
	value := newDashboard(&fakeBackend{}, projectsSnapshot(nil))
	value.offerAgentHooks([]agenthooks.Status{
		{Provider: agenthooks.ProviderClaude, State: agenthooks.StateCurrent},
		{Provider: agenthooks.ProviderCodex, State: agenthooks.StateUnavailable},
	})
	if value.modal != noModal || value.errorMessage != "" {
		t.Fatalf("current hooks = (modal %v, error %q), want no prompt", value.modal, value.errorMessage)
	}
}

func TestDashboardReportsInvalidAgentHookSettings(t *testing.T) {
	value := newDashboard(&fakeBackend{}, projectsSnapshot(nil))
	value.offerAgentHooks([]agenthooks.Status{{
		Provider: agenthooks.ProviderClaude,
		State:    agenthooks.StateInvalid,
		Err:      errors.New("hooks must be an object"),
	}})
	if value.modal != noModal || !strings.Contains(value.errorMessage, "hooks must be an object") || value.noticeMessage {
		t.Fatalf("invalid hooks = (modal %v, error %q, notice %v)", value.modal, value.errorMessage, value.noticeMessage)
	}
}

func TestDashboardConfiguresAndTestsSoundAlerts(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	send(&value, key(tea.KeyF3, ""))

	// Turning a sound on plays it once, so the user hears it without a
	// separate test control.
	saveDone := send(&value, key('d', "d"))
	saveWaiting := send(&value, key('b', "b"))
	if !value.config.SoundOnDone || !value.config.SoundOnWaiting || saveDone == nil || saveWaiting == nil {
		t.Fatalf("sound toggles = (done %v, waiting %v, saves %v/%v)",
			value.config.SoundOnDone, value.config.SoundOnWaiting, saveDone, saveWaiting)
	}
	if played := commandMessage[soundPlayedMsg](t, saveDone); played.kind != sound.Done {
		t.Fatalf("enabling the done sound played %q", played.kind)
	}
	if played := commandMessage[soundPlayedMsg](t, saveWaiting); played.kind != sound.Waiting {
		t.Fatalf("enabling the waiting sound played %q", played.kind)
	}
	// Turning it off plays nothing.
	if _, ok := playedSound(send(&value, key('d', "d"))); ok {
		t.Fatal("disabling the done sound played it")
	}
}

func TestDashboardSoundsOnceForAgentTransitions(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/alpha"}
	tab := model.Tab{
		ID: "tab-1", WorkspaceID: workspace.ID, Name: "1", Running: true,
		Agent: model.AgentCodex, AgentPhase: model.AgentPhaseWorking,
	}
	value := newDashboardWithConfig(&fakeBackend{}, model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "root", Path: "/"},
		Directories: []model.WorkspaceView{{Workspace: workspace, Tabs: []model.Tab{tab}}},
	}}}, "", Config{SoundOnDone: true, SoundOnWaiting: true})
	value.agentSoundReady = true

	idle := map[string]model.AgentStatus{"tab-1": {Agent: model.AgentCodex, Phase: model.AgentPhaseCompleted}}
	command := send(&value, agentSnapshotMsg{value: idle})
	if kind, ok := playedSound(command); !ok || kind != sound.Done {
		t.Fatalf("done transition sound = (%q, %v)", kind, ok)
	}
	command = send(&value, agentSnapshotMsg{value: idle})
	if kind, ok := playedSound(command); ok {
		t.Fatalf("stable idle transition played %q again", kind)
	}

	waiting := map[string]model.AgentStatus{"tab-1": {Agent: model.AgentCodex, Phase: model.AgentPhaseWaitingApproval}}
	command = send(&value, agentSnapshotMsg{value: waiting})
	if kind, ok := playedSound(command); !ok || kind != sound.Waiting {
		t.Fatalf("waiting transition sound = (%q, %v)", kind, ok)
	}
}

// Every snapshot carries the daemon's current agent statuses, so a refresh can
// land between two agent polls already holding the new phase. The transition
// still belongs to the poll and must still sound.
func TestDashboardSoundsForTransitionsASnapshotDeliveredFirst(t *testing.T) {
	for _, probe := range []struct {
		phase model.AgentPhase
		want  sound.Kind
	}{
		{phase: model.AgentPhaseCompleted, want: sound.Done},
		{phase: model.AgentPhaseWaitingInput, want: sound.Waiting},
	} {
		t.Run(string(probe.phase), func(t *testing.T) {
			tree := func(phase model.AgentPhase, revision uint64) model.Snapshot {
				return model.Snapshot{Revision: revision, Roots: []model.RootView{{
					Root: model.Root{ID: "root"},
					Tabs: []model.Tab{{ID: "tab", Running: true, Agent: model.AgentClaude, AgentPhase: phase}},
				}}}
			}
			value := newDashboardWithConfig(&fakeBackend{}, tree(model.AgentPhaseWorking, 1), "",
				Config{SoundOnDone: true, SoundOnWaiting: true})
			value.agentSoundReady = true

			send(&value, snapshotMsg{value: tree(probe.phase, 2)})
			command := send(&value, agentSnapshotMsg{value: map[string]model.AgentStatus{
				"tab": {Agent: model.AgentClaude, Phase: probe.phase},
			}})
			if kind, ok := playedSound(command); !ok || kind != probe.want {
				t.Fatalf("sound = (%q, %v), want %q", kind, ok, probe.want)
			}
		})
	}
}

func TestDashboardDoesNotSoundOnTheFirstAgentSnapshot(t *testing.T) {
	value := newDashboardWithConfig(&fakeBackend{}, model.Snapshot{}, "", Config{SoundOnWaiting: true})
	waiting := map[string]model.AgentStatus{"tab-1": {Agent: model.AgentCodex, Phase: model.AgentPhaseWaitingInput}}
	command := send(&value, agentSnapshotMsg{value: waiting})
	if !value.agentSoundReady {
		t.Fatal("first successful agent snapshot did not arm later sounds")
	}
	if kind, ok := playedSound(command); ok {
		t.Fatalf("first agent snapshot played %q", kind)
	}
}

func TestDashboardSeparatesProvisionalStopInterruptionAndCompletion(t *testing.T) {
	for _, phase := range []model.AgentPhase{model.AgentPhaseStopped, model.AgentPhaseInterrupted, model.AgentPhaseIdle, model.AgentPhaseUnknown, model.AgentPhaseCompleted} {
		t.Run(string(phase), func(t *testing.T) {
			value := newDashboardWithConfig(&fakeBackend{}, model.Snapshot{Roots: []model.RootView{{
				Root: model.Root{ID: "root"}, Tabs: []model.Tab{{ID: "tab", Running: true, Agent: model.AgentCodex, AgentPhase: model.AgentPhaseWorking}},
			}}}, "", Config{SoundOnDone: true})
			value.agentSoundReady = true
			command := send(&value, agentSnapshotMsg{value: map[string]model.AgentStatus{"tab": {Agent: model.AgentCodex, Phase: phase}}})
			if value.hasAnimatedAgent() {
				t.Fatal("terminal/non-authoritative state still animates")
			}
			_, played := playedSound(command)
			if played != (phase == model.AgentPhaseCompleted) {
				t.Fatalf("%s: completion sound = %v", phase, played)
			}
			want := map[model.AgentPhase]string{model.AgentPhaseStopped: "○", model.AgentPhaseInterrupted: "□", model.AgentPhaseIdle: "○", model.AgentPhaseUnknown: "●", model.AgentPhaseCompleted: "✓"}[phase]
			if got := ansi.Strip(openTabMarkers(value.styles, value.styles.navigationSelected, value.state.Roots[0].Tabs, 1)); got != want {
				t.Fatalf("marker = %s, want %s", got, want)
			}
		})
	}
}

func TestCompletionSoundTracksSessionAndTurnIdentity(t *testing.T) {
	value := newDashboardWithConfig(&fakeBackend{}, model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root"}, Tabs: []model.Tab{{ID: "tab", Running: true, Agent: model.AgentCodex, AgentPhase: model.AgentPhaseCompleted, AgentSessionID: "session", AgentTurnID: "old"}},
	}}}, "", Config{SoundOnDone: true})
	for _, testCase := range []struct {
		session, turn string
		want          bool
	}{
		{"session", "old", false}, {"session", "new", true}, {"another-session", "new", false},
	} {
		_, got := value.soundForAgentTransitions(map[string]model.AgentStatus{"tab": {Agent: model.AgentCodex, Phase: model.AgentPhaseCompleted, SessionID: testCase.session, TurnID: testCase.turn}})
		if got != testCase.want {
			t.Fatalf("session=%s turn=%s sound=%v", testCase.session, testCase.turn, got)
		}
	}
}

func TestDashboardDoesNotSoundForEstimatedIdle(t *testing.T) {
	value := newDashboardWithConfig(&fakeBackend{}, model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root-1"},
		Tabs: []model.Tab{{ID: "tab-1", Running: true, Agent: model.AgentCodex, AgentPhase: model.AgentPhaseWorking}},
	}}}, "", Config{SoundOnDone: true, SoundOnWaiting: true})
	value.agentSoundReady = true

	var statuses map[string]model.AgentStatus
	if err := json.Unmarshal([]byte(`{"tab-1":{"agent":"codex","phase":"idle","estimated":true}}`), &statuses); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	command := send(&value, agentSnapshotMsg{value: statuses})
	if kind, ok := playedSound(command); ok {
		t.Fatalf("estimated idle played %q, want no completion sound", kind)
	}
	if value.state.Roots[0].Tabs[0].AgentPhase != model.AgentPhaseIdle || value.hasAnimatedAgent() {
		t.Fatal("estimated idle did not stop the marker animation")
	}

	statuses["tab-1"] = model.AgentStatus{Agent: model.AgentCodex, Phase: model.AgentPhaseWorking, Estimated: true}
	send(&value, agentSnapshotMsg{value: statuses})
	statuses["tab-1"] = model.AgentStatus{Agent: model.AgentCodex, Phase: model.AgentPhaseCompleted}
	command = send(&value, agentSnapshotMsg{value: statuses})
	if kind, ok := playedSound(command); !ok || kind != sound.Done {
		t.Fatalf("hooked completion sound = (%q, %v), want done", kind, ok)
	}

	statuses["tab-1"] = model.AgentStatus{Agent: model.AgentCodex, Phase: model.AgentPhaseWaitingApproval, Estimated: true}
	_, command = value.Update(agentSnapshotMsg{value: statuses})
	if kind, ok := playedSound(command); !ok || kind != sound.Waiting {
		t.Fatalf("estimated approval sound = (%q, %v), want waiting", kind, ok)
	}
}

// A sound and a tab marker say an agent stopped to ask something; without a way
// to reach it the user still has to walk the tree to find which one.
func TestDashboardJumpsToTheNextWaitingAgent(t *testing.T) {
	root := projectsRoot
	alpha := model.Workspace{ID: "workspace-1", RootID: root.ID, Name: "alpha", Path: "/projects/alpha"}
	bravo := model.Workspace{ID: "workspace-2", RootID: root.ID, Name: "bravo", Path: "/projects/bravo"}
	charlie := model.Workspace{ID: "workspace-3", RootID: root.ID, Name: "charlie", Path: "/projects/charlie"}
	// alpha's agent is busy; bravo's wants input and charlie's wants approval.
	alphaTab := model.Tab{ID: "tab-1", WorkspaceID: alpha.ID, Name: "1", Running: true,
		Agent: model.AgentClaude, AgentPhase: model.AgentPhaseWorking}
	bravoTab := model.Tab{ID: "tab-2", WorkspaceID: bravo.ID, Name: "1", Running: true,
		Agent: model.AgentClaude, AgentPhase: model.AgentPhaseWaitingInput}
	charlieTab := model.Tab{ID: "tab-3", WorkspaceID: charlie.ID, Name: "1", Running: true,
		Agent: model.AgentCodex, AgentPhase: model.AgentPhaseWaitingApproval}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: root,
		Directories: []model.WorkspaceView{
			{Workspace: alpha, Tabs: []model.Tab{alphaTab}},
			{Workspace: bravo, Tabs: []model.Tab{bravoTab}},
			{Workspace: charlie, Tabs: []model.Tab{charlieTab}},
		},
	}}}

	backend := &fakeBackend{snapshot: snapshot, workspace: bravo}
	value := newDashboard(backend, snapshot)
	value.selectedWorkspaceID = alpha.ID
	value.selectedPath = alpha.Path
	value.terminal = newEmbeddedTerminal(alphaTab.ID, newMemoryStream(""), 40, 10)
	value.focus = terminalPane
	t.Cleanup(func() { value.closeTerminal() })

	// The open terminal is not waiting, so the walk starts at the first stop
	// and skips the busy agent entirely.
	value = pressJumpToWaitingAgent(t, value)
	if backend.openedTab != bravoTab.ID || value.terminal.id != bravoTab.ID {
		t.Fatalf("Ctrl+Shift+A = (opened %q, terminal %q), want bravo's waiting terminal",
			backend.openedTab, value.terminal.id)
	}
	if item, ok := value.navigationItem(); !ok || item.workspace.Path != bravo.Path {
		t.Fatalf("cursor = %+v, want it on bravo", item)
	}

	// Waiting for approval is a stop as much as waiting for input.
	backend.workspace = charlie
	value = pressJumpToWaitingAgent(t, value)
	if value.terminal.id != charlieTab.ID {
		t.Fatalf("second jump = terminal %q, want charlie's", value.terminal.id)
	}

	// The last stop wraps rather than stopping at the end of the tree.
	backend.workspace = bravo
	value = pressJumpToWaitingAgent(t, value)
	if value.terminal.id != bravoTab.ID {
		t.Fatalf("third jump = terminal %q, want it wrapped to bravo", value.terminal.id)
	}
}

// Reopening the terminal that is already attached would tear it down to attach
// a new one in its place, so the only waiting agent gets the keyboard instead.
func TestDashboardJumpToTheOnlyWaitingAgentMovesTheFocus(t *testing.T) {
	root := projectsRoot
	alpha := model.Workspace{ID: "workspace-1", RootID: root.ID, Name: "alpha", Path: "/projects/alpha"}
	alphaTab := model.Tab{ID: "tab-1", WorkspaceID: alpha.ID, Name: "1", Running: true,
		Agent: model.AgentClaude, AgentPhase: model.AgentPhaseWaitingInput}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        root,
		Directories: []model.WorkspaceView{{Workspace: alpha, Tabs: []model.Tab{alphaTab}}},
	}}}

	backend := &fakeBackend{snapshot: snapshot, workspace: alpha}
	value := newDashboard(backend, snapshot)
	value.selectedWorkspaceID = alpha.ID
	value.selectedPath = alpha.Path
	value.terminal = newEmbeddedTerminal(alphaTab.ID, newMemoryStream(""), 40, 10)
	value.focus = leftPane
	t.Cleanup(func() { value.closeTerminal() })

	command := send(&value, jumpToWaitingAgentKey())
	if command != nil || backend.openedTab != "" {
		t.Fatalf("jump to the open terminal = (command %v, opened %q), want no reattach",
			command, backend.openedTab)
	}
	if value.focus != terminalPane {
		t.Fatalf("focus = %v, want the terminal", value.focus)
	}
}

func TestDashboardJumpSaysWhenNoAgentIsWaiting(t *testing.T) {
	root := projectsRoot
	alpha := model.Workspace{ID: "workspace-1", RootID: root.ID, Name: "alpha", Path: "/projects/alpha"}
	alphaTab := model.Tab{ID: "tab-1", WorkspaceID: alpha.ID, Name: "1", Running: true,
		Agent: model.AgentClaude, AgentPhase: model.AgentPhaseWorking}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        root,
		Directories: []model.WorkspaceView{{Workspace: alpha, Tabs: []model.Tab{alphaTab}}},
	}}}

	value := newDashboard(&fakeBackend{snapshot: snapshot}, snapshot)
	command := send(&value, jumpToWaitingAgentKey())
	if command != nil {
		t.Fatalf("jump with nothing waiting produced %v, want no command", command)
	}
	if value.errorMessage != "no agent is waiting" || !value.noticeMessage {
		t.Fatalf("status = (%q, notice %v), want a note that nothing is waiting",
			value.errorMessage, value.noticeMessage)
	}
}

func TestOpenTabMarkersUseAgentColorsAndPhaseShapes(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	tabs := []model.Tab{
		{Running: true, Agent: model.AgentClaude, AgentPhase: model.AgentPhaseIdle},
		{Running: true, Agent: model.AgentCodex, AgentPhase: model.AgentPhaseWaitingInput},
		{Running: true, Agent: model.AgentClaude, AgentPhase: model.AgentPhaseWaitingApproval},
		{Running: true, Agent: model.AgentCodex, AgentPhase: model.AgentPhaseError},
		{Running: true, Agent: model.AgentClaude, AgentPhase: model.AgentPhaseWorking},
		{Running: true, Agent: model.AgentOpenCode, AgentPhase: model.AgentPhaseIdle},
		{Running: true, Agent: model.AgentNode, AgentPhase: model.AgentPhaseUnknown},
		{Running: true},
	}

	base := value.styles.navigationSelected
	markers := openTabMarkers(value.styles, base, tabs, 6)
	claude := base.Foreground(value.styles.agentClaude.GetForeground()).Render("○")
	// An agent that needs the user is drawn in reverse, still in its color.
	codex := base.Foreground(value.styles.agentCodex.GetForeground()).Reverse(true).Bold(true).Render("▲")
	approval := base.Foreground(value.styles.agentClaude.GetForeground()).Reverse(true).Bold(true).Render("■")
	failure := base.Foreground(value.styles.agentCodex.GetForeground()).Reverse(true).Bold(true).Render("★")
	working := base.Foreground(value.styles.agentClaude.GetForeground()).Render("◑")
	opencode := base.Foreground(value.styles.agentOpenCode.GetForeground()).Render("○")
	node := base.Foreground(value.styles.agentNode.GetForeground()).Render("●")
	plain := base.Foreground(value.styles.agentPlain.GetForeground()).Render("●")
	if markers != claude+codex+approval+failure+working+opencode+node+plain {
		t.Fatalf("open tab markers = %q, want agent colors and phase shapes", markers)
	}
}

func TestNodeMarkersStayGreenOnEverySurface(t *testing.T) {
	for _, dark := range []bool{false, true} {
		styles := newUIStyles(dark)
		green := lipgloss.Color("#3F7D20")
		if dark {
			green = lipgloss.Color("#8CCF6B")
		}
		for _, base := range []lipgloss.Style{
			styles.navigationItem, styles.navigationCurrent, styles.navigationSelected,
			styles.interactiveHover, styles.tab, styles.tabSelected,
		} {
			tab := model.Tab{Running: true, Agent: model.AgentNode, AgentPhase: model.AgentPhaseUnknown}
			if got, want := openTabMarker(styles, base, tab, 1), base.Foreground(green).Render("●"); got != want {
				t.Fatalf("dark=%v: Node marker = %q, want %q", dark, got, want)
			}
		}
	}
}

func TestNodeSnapshotUpdatesMarkersWithoutAgentActivity(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root"}, Tabs: []model.Tab{{ID: "tab", Running: true}},
	}}})
	value.config.SoundOnDone, value.config.SoundOnWaiting = true, true
	value.updateAgents(map[string]model.AgentStatus{"tab": {Agent: model.AgentNode, Phase: model.AgentPhaseUnknown}})
	if value.hasAnimatedAgent() || len(value.waitingAgentStops()) != 0 {
		t.Fatal("Node service activated agent animation or waiting navigation")
	}
	if got := value.agentStatusNote(); got != "Node.js running" {
		t.Fatalf("Node status rail = %q, want Node.js running", got)
	}
	tabs := value.state.Roots[0].Tabs
	line := renderTabBarWithHover(value.styles, tabs, 0, -1, -1, 1, 80)[0]
	marker := value.styles.tabSelected.Foreground(value.styles.agentNode.GetForeground()).Render("●")
	if !strings.Contains(line, marker) {
		t.Fatalf("Node tab rail lacks green marker: %q", line)
	}
	if kind, ok := value.soundForAgentTransitions(nil); ok {
		t.Fatalf("Node exit played %q", kind)
	}
	value.updateAgents(nil)
	if got := openTabMarkers(value.styles, value.styles.navigationSelected, tabs, 1); got != value.styles.navigationSelected.Foreground(value.styles.agentPlain.GetForeground()).Render("●") {
		t.Fatalf("Node exit did not restore neutral marker: %q", got)
	}
	if got := value.agentStatusNote(); got != "" {
		t.Fatalf("Node exit left status rail %q", got)
	}
}

func TestTabBarShowsEachRunningTabStatusMarker(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	tabs := []model.Tab{
		{Name: "one", Running: true, Agent: model.AgentClaude, AgentPhase: model.AgentPhaseWaitingApproval},
		{Name: "two", Running: true, Agent: model.AgentCodex, AgentPhase: model.AgentPhaseWorking},
		{Name: "three"},
	}

	line := renderTabBarWithHover(value.styles, tabs, 0, -1, -1, 2, 80)[0]
	if plain := ansi.Strip(line); !strings.HasPrefix(plain, "  ■ one  ×    ◑ two  ×    three  ×    +  ") {
		t.Fatalf("tab bar = %q, want the status marker before each running tab name", plain)
	}
	approval := value.styles.tabSelected.Foreground(value.styles.agentClaude.GetForeground()).Reverse(true).Bold(true).Render("■")
	working := value.styles.tab.Foreground(value.styles.agentCodex.GetForeground()).Render("◑")
	if !strings.Contains(line, approval) || !strings.Contains(line, working) {
		t.Fatalf("tab bar = %q, want markers in the agent color over the tab's own style", line)
	}
}

func TestOpenTabMarkersCycleThroughEveryAnimationFrame(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	base := value.styles.navigationSelected
	style := base.Foreground(value.styles.agentClaude.GetForeground())
	tabs := []model.Tab{{
		Running: true, Agent: model.AgentClaude, AgentPhase: model.AgentPhaseWorking,
	}}

	for index, frame := range agentAnimationFrames {
		marker := openTabMarkers(value.styles, base, tabs, index)
		if marker != style.Render(frame) || lipgloss.Width(marker) != 1 {
			t.Fatalf("animation frame %d = %q with width %d, want %q with width 1", index, marker, lipgloss.Width(marker), frame)
		}
	}
}

func TestAgentAnimationRunsOnlyWhileWorkIsActive(t *testing.T) {
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root-1"},
		Tabs: []model.Tab{{
			ID: "tab-1", Running: true, Agent: model.AgentClaude, AgentPhase: model.AgentPhaseWorking,
		}},
	}}}
	value := newDashboard(&fakeBackend{}, snapshot)
	if !value.agentAnimationActive || !value.agentAnimationPending {
		t.Fatal("working agent did not schedule the initial animation tick")
	}

	command := send(&value, agentAnimationMsg{})
	if value.agentAnimationFrame != 1 || !value.agentAnimationPending || command == nil {
		t.Fatalf("animation tick = (frame %d, pending %v, command %v), want next frame and tick", value.agentAnimationFrame, value.agentAnimationPending, command)
	}

	value.updateAgents(map[string]model.AgentStatus{
		"tab-1": {Agent: model.AgentClaude, Phase: model.AgentPhaseIdle},
	})
	command = send(&value, agentAnimationMsg{})
	if value.agentAnimationFrame != 1 || value.agentAnimationActive || value.agentAnimationPending || command != nil {
		t.Fatalf("idle animation = (frame %d, pending %v, command %v), want stopped frame", value.agentAnimationFrame, value.agentAnimationPending, command)
	}

	command = send(&value, agentSnapshotMsg{value: map[string]model.AgentStatus{
		"tab-1": {Agent: model.AgentClaude, Phase: model.AgentPhaseThinking},
	}})
	if !value.agentAnimationActive || !value.agentAnimationPending || command == nil {
		t.Fatal("new working phase did not restart the animation")
	}
}

func TestAgentSnapshotUpdatesStateAndSchedulesAnotherRefresh(t *testing.T) {
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: projectsRoot,
		Tabs: []model.Tab{{ID: "tab-1", Running: true}},
	}}}
	value := newDashboard(&fakeBackend{}, snapshot)

	command := send(&value, agentSnapshotMsg{value: map[string]model.AgentStatus{
		"tab-1": {Agent: model.AgentClaude, Phase: model.AgentPhaseWaitingApproval},
	}})
	if got := value.state.Roots[0].Tabs[0].Agent; got != model.AgentClaude {
		t.Fatalf("agent = %q, want %q", got, model.AgentClaude)
	}
	if got := value.state.Roots[0].Tabs[0].AgentPhase; got != model.AgentPhaseWaitingApproval {
		t.Fatalf("agent phase = %q, want %q", got, model.AgentPhaseWaitingApproval)
	}
	if command == nil {
		t.Fatal("agent snapshot did not schedule another refresh")
	}
}

// The counters are the agent's own; romty reports them and estimates nothing,
// so a tab with none shows none.
func TestDashboardShowsTheOpenAgentLedgerInTheRail(t *testing.T) {
	tab := model.Tab{ID: "tab-1", WorkspaceID: "workspace-1", Name: "1", Running: true,
		Agent: model.AgentClaude, AgentPhase: model.AgentPhaseWorking,
		AgentContextTokens: 344103, AgentCostUSD: 1.25}
	snapshot, alpha := alphaWorkspaceSnapshot([]model.Tab{tab})

	value := newDashboard(&fakeBackend{snapshot: snapshot}, snapshot)
	value.width = 120
	value.height = 30
	value.selectedWorkspaceID = alpha.ID
	value.selectedPath = alpha.Path
	value.focus = terminalPane

	rail := ansi.Strip(value.renderStatus(value.width, value.dimensions().bodyHeight)[0])
	if !strings.Contains(rail, "344k ctx") || !strings.Contains(rail, "$1.25") {
		t.Fatalf("rail = %q, want the agent's counters", rail)
	}

	// Nothing read means nothing shown, not a zero.
	value.state.Roots[0].Directories[0].Tabs[0].AgentContextTokens = 0
	value.state.Roots[0].Directories[0].Tabs[0].AgentCostUSD = 0
	rail = ansi.Strip(value.renderStatus(value.width, value.dimensions().bodyHeight)[0])
	if strings.Contains(rail, "ctx") || strings.Contains(rail, "$") {
		t.Fatalf("rail = %q, want no reading shown", rail)
	}
}

func TestInstalledHooksNoticeListsProvidersAsASentence(t *testing.T) {
	results := func(providers ...agenthooks.Provider) []agenthooks.Result {
		value := make([]agenthooks.Result, len(providers))
		for index, provider := range providers {
			value[index] = agenthooks.Result{Provider: provider}
		}
		return value
	}
	for _, probe := range []struct {
		results []agenthooks.Result
		want    string
	}{
		{results(agenthooks.ProviderKiro), "Kiro hooks installed"},
		{results(agenthooks.ProviderClaude, agenthooks.ProviderCodex), "Claude Code and Codex hooks installed"},
		{results(agenthooks.ProviderClaude, agenthooks.ProviderCodex, agenthooks.ProviderKiro), "Claude Code, Codex and Kiro hooks installed"},
	} {
		if got := installedHooksNotice(probe.results); !strings.HasPrefix(got, probe.want+";") {
			t.Fatalf("installedHooksNotice() = %q, want it to start with %q", got, probe.want)
		}
	}
}
