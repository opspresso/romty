package ui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/agenthooks"
	"github.com/opspresso/romty/internal/gitrepo"
	"github.com/opspresso/romty/internal/model"
)

// Each Ctrl+Shift chord has a letter in the tree, because a terminal without
// the Kitty keyboard protocol cannot send the chord at all.
func TestTreeLettersRunTheChordActions(t *testing.T) {
	tabs := openTabs(1)
	tabs[0].Agent, tabs[0].AgentPhase = model.AgentCodex, model.AgentPhaseWaitingInput
	snapshot, workspace := alphaWorkspaceSnapshot(tabs)
	backend := &fakeBackend{snapshot: snapshot, workspace: workspace}

	value := newDashboard(backend, snapshot)
	value.focus = leftPane
	value.gitStates = map[string]gitrepo.State{workspace.Path: {Branch: "main"}}
	send(&value, key(tea.KeyDown, ""))

	if m, _ := value.Update(key('g', "g")); m.(dashboard).modal != gitActionsModal {
		t.Fatalf("g opened modal %v, want Git actions", m.(dashboard).modal)
	}
	if _, command := value.Update(key('t', "t")); command == nil {
		t.Fatal("t did not create a tab")
	}
	if m, _ := value.Update(key('f', "f")); !m.(dashboard).fileView.active {
		t.Fatal("f did not open the file view")
	}

	value.setNavigation(0)
	send(&value, key('a', "a"))
	if value.cursorPath != workspace.Path {
		t.Fatalf("a left the cursor on %q, want the waiting agent's workspace", value.cursorPath)
	}
}

// The rail names the way to a waiting agent only while one is waiting, and
// in the form that works in every terminal.
func TestRailOffersTheWaitingAgentOnlyWhileOneWaits(t *testing.T) {
	tabs := openTabs(1)
	snapshot, _ := alphaWorkspaceSnapshot(tabs)
	value := newDashboard(&fakeBackend{}, snapshot)
	value.width, value.height = 120, 20
	if strings.Contains(ansi.Strip(value.render()), "waiting agent") {
		t.Fatal("rail offers a waiting agent with none waiting")
	}

	value.state.Roots[0].Directories[0].Tabs[0].Agent = model.AgentClaude
	value.state.Roots[0].Directories[0].Tabs[0].AgentPhase = model.AgentPhaseWaitingApproval
	if !strings.Contains(ansi.Strip(value.render()), " a  waiting agent") {
		t.Fatalf("tree rail lacks the waiting agent:\n%s", ansi.Strip(value.render()))
	}
	value.focus = terminalPane
	value.terminal = newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)
	t.Cleanup(func() { value.closeTerminal() })
	if !strings.Contains(ansi.Strip(value.render()), "Ctrl+/ a  waiting agent") {
		t.Fatalf("terminal rail lacks the universal form:\n%s", ansi.Strip(value.render()))
	}
}

// A shortcut row that runs out of room drops whole shortcuts, never half of
// one, and keeps the ones listed first.
func TestShortcutsDropWholeEntries(t *testing.T) {
	styles := newUIStyles(true)
	shortcuts := []shortcut{{key: "Enter", description: "open"}, {key: "Space", description: "fold"}, {key: "Tab", description: "terminal"}}
	got := ansi.Strip(renderShortcuts(styles, 26, shortcuts...))
	if got != " Enter  open   Space  fold" {
		t.Fatalf("fitted shortcuts = %q", got)
	}
	if got := ansi.Strip(renderShortcuts(styles, 5, shortcuts...)); got != " Ent…" {
		t.Fatalf("first shortcut alone = %q, want it truncated rather than dropped", got)
	}
}

func TestEmptyDashboardShowsOnlyHowToStart(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 110, 16
	screen := ansi.Strip(value.render())
	for _, want := range []string{"Add a directory to start", "F2  pick a directory", "F2  add a root directory"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("empty screen lacks %q:\n%s", want, screen)
		}
	}
	for _, unwanted := range []string{"Select a root or workspace", "Space  fold", "Tab  terminal"} {
		if strings.Contains(screen, unwanted) {
			t.Fatalf("empty screen offers %q, which does nothing yet:\n%s", unwanted, screen)
		}
	}
}

// Coming back to a workspace lands on the tab last opened there.
func TestReturningToAWorkspaceSelectsItsLastTab(t *testing.T) {
	alpha := model.Workspace{ID: "workspace-1", RootID: projectsRoot.ID, Name: "alpha", Path: "/projects/alpha"}
	bravo := model.Workspace{ID: "workspace-2", RootID: projectsRoot.ID, Name: "bravo", Path: "/projects/bravo"}
	alphaTabs := openTabs(3)
	bravoTab := model.Tab{ID: "tab-b", WorkspaceID: bravo.ID, Name: "1", Running: true}
	value := newDashboard(&fakeBackend{}, projectsSnapshot([]model.WorkspaceView{
		{Workspace: alpha, Tabs: alphaTabs}, {Workspace: bravo, Tabs: []model.Tab{bravoTab}},
	}))
	value.lastTabs = map[string]string{alpha.ID: "tab-3"}
	value.terminal = newEmbeddedTerminal(bravoTab.ID, newMemoryStream(""), 40, 10)
	t.Cleanup(func() { value.closeTerminal() })
	value.focus = leftPane

	send(&value, key(tea.KeyDown, ""))
	if value.cursorPath != alpha.Path || value.tabIndex != 2 {
		t.Fatalf("cursor = (%q, tab %d), want alpha's third tab", value.cursorPath, value.tabIndex)
	}
}

// While the cursor is on another workspace, the tab row shows that
// workspace's tabs over the open terminal, and says so.
func TestTabRowNamesTheWorkspaceItPreviews(t *testing.T) {
	alpha := model.Workspace{ID: "workspace-1", RootID: projectsRoot.ID, Name: "alpha", Path: "/projects/alpha"}
	bravo := model.Workspace{ID: "workspace-2", RootID: projectsRoot.ID, Name: "bravo", Path: "/projects/bravo"}
	value := newDashboard(&fakeBackend{}, projectsSnapshot([]model.WorkspaceView{
		{Workspace: alpha, Tabs: openTabs(1)},
		{Workspace: bravo, Tabs: []model.Tab{{ID: "tab-b", WorkspaceID: bravo.ID, Name: "1", Running: true}}},
	}))
	value.width, value.height = 110, 20
	value.selectedWorkspaceID, value.selectedPath = alpha.ID, alpha.Path
	value.terminal = newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)
	t.Cleanup(func() { value.closeTerminal() })
	value.focus = leftPane

	value.setNavigation(1)
	if strings.Contains(ansi.Strip(value.render()), "tabs of") {
		t.Fatal("the open workspace's own tabs are labelled as a preview")
	}
	value.setNavigation(2)
	if !strings.Contains(ansi.Strip(value.render()), "tabs of bravo · Enter opens") {
		t.Fatalf("preview is not labelled:\n%s", ansi.Strip(value.render()))
	}
}

// A crowded tab row shortens the longest names so every tab and the + slot
// stay on screen, and a click lands on the tab drawn under it.
func TestCrowdedTabRowKeepsEveryTabAndTheNewTabSlot(t *testing.T) {
	styles := newUIStyles(true)
	tabs := []model.Tab{
		{ID: "a", Name: "claude-refactoring", Running: true},
		{ID: "b", Name: "server", Running: true},
		{ID: "c", Name: "tests-watch-mode", Running: true},
	}
	row := ansi.Strip(renderTabBarWithHover(styles, tabs, 0, -1, -1, 0, 50)[0])
	if !strings.HasSuffix(strings.TrimRight(row, " "), "+") || !strings.Contains(row, "server") {
		t.Fatalf("crowded row = %q, want every tab and the + slot", row)
	}
	if tabs[0].Name != "claude-refactoring" {
		t.Fatal("fitting the row renamed the tab itself")
	}
	// Columns, not bytes: × before it is two bytes wide.
	at := len([]rune(row[:strings.Index(row, "server")]))
	if hit, ok := tabHitAtX(tabs, 0, at, 50); !ok || hit.index != 1 {
		t.Fatalf("click on server = (%+v, %v), want tab 1", hit, ok)
	}
	plus := strings.LastIndex(row, "+")
	if hit, ok := tabHitAtX(tabs, 0, len([]rune(row[:plus])), 50); !ok || hit.index != len(tabs) {
		t.Fatalf("click on + = (%+v, %v), want the new-tab slot", hit, ok)
	}
}

// In the tree the rail spells out the markers on the cursor's row.
func TestTreeRailNamesWhatRunsInEachTab(t *testing.T) {
	tabs := []model.Tab{
		{ID: "t1", WorkspaceID: "workspace-1", Name: "1", Running: true, Agent: model.AgentClaude, AgentPhase: model.AgentPhaseWaitingInput},
		{ID: "t2", WorkspaceID: "workspace-1", Name: "server", Running: true, Agent: model.AgentNode},
		{ID: "t3", WorkspaceID: "workspace-1", Name: "zsh", Running: true},
	}
	snapshot, _ := alphaWorkspaceSnapshot(tabs)
	value := newDashboard(&fakeBackend{}, snapshot)
	value.focus = leftPane
	value.setNavigation(1)
	if got, want := value.railNote(), "1: Claude Code · Needs your answer  server: Node.js  zsh: shell"; got != want {
		t.Fatalf("tree rail note = %q, want %q", got, want)
	}
}

// `/` narrows Help to the shortcuts that match, keeping their section
// headings; Esc clears the filter before it closes Help.
func TestHelpSearchNarrowsTheReference(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 110, 30
	send(&value, key(tea.KeyF1, ""))
	send(&value, key('/', "/"))
	typeText(&value, "WAIT")
	if !strings.Contains(ansi.Strip(value.render()), " FIND  WAIT") {
		t.Fatalf("the rail does not show the query:\n%s", ansi.Strip(value.render()))
	}
	send(&value, key(tea.KeyEnter, ""))

	body := ansi.Strip(strings.Join(value.helpEntries(), "\n"))
	if !strings.Contains(body, "SWITCH") || !strings.Contains(body, "Jump to a waiting agent") {
		t.Fatalf("filtered Help lacks the match or its section:\n%s", body)
	}
	if strings.Contains(body, "Stop daemon") || strings.Contains(body, "GLOBAL") {
		t.Fatalf("filtered Help kept entries that do not match:\n%s", body)
	}

	send(&value, key(tea.KeyEscape, ""))
	if value.modal != helpModal || value.helpQuery != "" {
		t.Fatalf("first Esc = (modal %v, query %q), want the filter cleared and Help open", value.modal, value.helpQuery)
	}
	send(&value, key(tea.KeyEscape, ""))
	if value.modal != noModal {
		t.Fatal("second Esc did not close Help")
	}

	send(&value, key(tea.KeyF1, ""))
	send(&value, key('/', "/"))
	typeText(&value, "zzz")
	if body := ansi.Strip(strings.Join(value.helpEntries(), "\n")); !strings.Contains(body, "No shortcut matches zzz") {
		t.Fatalf("an empty result does not say so:\n%s", body)
	}
}

// The other tabs give way before the active one, and a one-column name keeps
// its first character so the tabs stay distinguishable.
func TestCrowdedTabRowKeepsTheActiveNameLongest(t *testing.T) {
	tabs := []model.Tab{
		{ID: "a", Name: "claude-refactor", Running: true},
		{ID: "b", Name: "server", Running: true},
		{ID: "c", Name: "tests-watch", Running: true},
	}
	fitted := fitTabNames(tabs, 1, 44)
	if fitted[1].Name != "server" {
		t.Fatalf("active name = %q, want it whole while others can shrink", fitted[1].Name)
	}
	if got := shortenName("server", 1); got != "s" {
		t.Fatalf("one-column name = %q, want its first character", got)
	}
}

// A name stored decomposed, as macOS can store Korean, is confirmed by typing
// it composed, and a pasted name confirms as well.
func TestDeleteConfirmationAcceptsTheNameInAnyNormalForm(t *testing.T) {
	decomposed := "\u1112\u1161\u11ab" // 한 in NFD
	workspace := model.Workspace{ID: "w", RootID: projectsRoot.ID, Name: decomposed, Path: "/projects/" + decomposed}
	value := newDashboard(&fakeBackend{}, projectsSnapshot([]model.WorkspaceView{{Workspace: workspace}}))
	value.setNavigation(1)
	updated, _ := value.confirmRemoveSelection(cursorItem(t, value))
	value = updated.(dashboard)

	send(&value, key('한', "한"))
	if !value.removeConfirmed() {
		t.Fatal("the composed name does not confirm a decomposed one")
	}

	value.removeConfirm = ""
	send(&value, tea.PasteMsg{Content: decomposed})
	if command := send(&value, key(tea.KeyEnter, "")); command == nil {
		t.Fatal("a pasted name did not confirm the deletion")
	}
}

// A wide first character survives the one-column cut, so Korean tab names
// do not turn blank on a crowded row.
func TestOneColumnTabNameKeepsAWideFirstCharacter(t *testing.T) {
	if got := shortenName("한국어탭", 1); got != "한" {
		t.Fatalf("one-column name = %q, want 한", got)
	}
	tabs := []model.Tab{{ID: "a", Name: "한국어탭", Running: true}, {ID: "b", Name: "서버", Running: true}}
	for _, tab := range fitTabNames(tabs, 0, 26) {
		if tab.Name == "" {
			t.Fatalf("fitted tabs = %+v, want no blank name", fitTabNames(tabs, 0, 26))
		}
	}
}

// f closes the file view it opened, for terminals that cannot send the chord.
func TestFileViewClosesWithF(t *testing.T) {
	snapshot, workspace := alphaWorkspaceSnapshot(nil)
	value := newDashboard(&fakeBackend{}, snapshot)
	value.gitStates = map[string]gitrepo.State{workspace.Path: {Branch: "main"}}
	value.focus = leftPane
	value.setNavigation(1)
	send(&value, key('f', "f"))
	if !value.fileView.active {
		t.Fatal("f did not open the file view")
	}
	send(&value, key('f', "f"))
	if value.fileView.active {
		t.Fatal("f did not close the file view")
	}
}

// A Git read that turns one-row workspaces into two-row repositories keeps a
// cursor that was on screen there.
func TestGitStateKeepsTheCursorOnScreen(t *testing.T) {
	directories := make([]model.WorkspaceView, 0, 12)
	for index := range 12 {
		name := string(rune('a'+index)) + "-workspace"
		directories = append(directories, model.WorkspaceView{Workspace: model.Workspace{
			ID: name, RootID: projectsRoot.ID, Name: name, Path: "/projects/" + name}})
	}
	value := newDashboard(&fakeBackend{}, projectsSnapshot(directories))
	value.width, value.height = 120, 18
	value.focus = leftPane
	for range 10 {
		value.moveNavigation(1)
	}
	if _, shown := value.cursorOnScreen(); !shown {
		t.Fatal("the cursor starts off screen; the test needs it shown")
	}
	send(&value, gitStatusMsg{value: repositoryStates(directories)})
	if _, shown := value.cursorOnScreen(); !shown {
		t.Fatalf("the cursor left the screen after the Git read (offset %d)", value.navOffset)
	}
}

// Scrollback says where the view is in words, at either end and between.
func TestScrollbackPositionReadsAsWords(t *testing.T) {
	value := scrolledDashboard(t, 200)
	total := value.terminal.scrollbackLen()
	for _, probe := range []struct {
		offset int
		want   string
	}{
		{0, fmt.Sprintf("newest · %d lines above", total)},
		{10, fmt.Sprintf("10 of %d lines up", total)},
		{total, fmt.Sprintf("oldest · %d lines", total)},
	} {
		value.scrollOffset = probe.offset
		if got := value.scrollbackPosition(); got != probe.want {
			t.Fatalf("position at %d = %q, want %q", probe.offset, got, probe.want)
		}
	}
}

// A failed Git command says what to do next, and the result leads to a
// terminal or the changed files in one key.
func TestGitResultOffersTheNextStep(t *testing.T) {
	snapshot, workspace := alphaWorkspaceSnapshot(nil)
	value := newDashboard(&fakeBackend{}, snapshot)
	value.width, value.height = 110, 24
	value.gitStates = map[string]gitrepo.State{workspace.Path: {Branch: "main"}}
	value.focus = leftPane
	value.setNavigation(1)
	send(&value, key('g', "g"))
	value.gitRun.action, value.gitRun.complete = gitPullAction, true
	value.gitRun.err = "fatal: Not possible to fast-forward, aborting."
	screen := ansi.Strip(value.render())
	if !strings.Contains(screen, "Next: branches diverged; press t, then merge or rebase") || !strings.Contains(screen, "t  terminal") {
		t.Fatalf("failed pull lacks its next step:\n%s", screen)
	}

	opened := value
	if _, command := opened.Update(key('t', "t")); command == nil {
		t.Fatal("t did not open a terminal")
	}
	files, _ := value.Update(key('f', "f"))
	if !files.(dashboard).fileView.active || files.(dashboard).modal != noModal {
		t.Fatal("f did not open the changed files")
	}
	if gitFailureHint("Everything up-to-date") != "" {
		t.Fatal("a success was given a failure hint")
	}
}

// On a first run the hook offer waits for the first root, so it does not
// stand in front of the explanation of how to add one.
func TestHookOfferWaitsForTheFirstRoot(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.offerAgentHooks([]agenthooks.Status{{Provider: agenthooks.ProviderClaude, State: agenthooks.StateMissing}})
	if value.modal != noModal || !value.hooksDeferred {
		t.Fatalf("first-run hook offer = (modal %v, deferred %v), want it held", value.modal, value.hooksDeferred)
	}
	send(&value, snapshotMsg{value: projectsSnapshot(nil)})
	if value.modal != hookInstallModal || value.hooksDeferred {
		t.Fatalf("after the first root = (modal %v, deferred %v), want the offer", value.modal, value.hooksDeferred)
	}
}

// The rename field states the length rule and the current count before a
// refusal has to.
func TestRenameFieldStatesTheLengthRule(t *testing.T) {
	value := newDashboard(&fakeBackend{}, projectsSnapshot(nil))
	value.width, value.height = 110, 14
	updated, _ := value.startRenameTab(model.Tab{ID: "tab-1", Name: "server"})
	if screen := ansi.Strip(updated.(dashboard).render()); !strings.Contains(screen, "1 to 32 characters · 6 now") {
		t.Fatalf("rename field lacks the rule:\n%s", screen)
	}
}

// A tab still called by its number names the program in it; a name the user
// chose is left alone, and the rename field starts from the stored name.
func TestDefaultTabNamesShowTheirProgram(t *testing.T) {
	styles := newUIStyles(true)
	tabs := []model.Tab{
		{ID: "a", Name: "1", Running: true, Agent: model.AgentClaude},
		{ID: "b", Name: "server", Running: true, Agent: model.AgentNode},
		{ID: "c", Name: "3", Running: true},
	}
	row := ansi.Strip(renderTabBarWithHover(styles, tabs, 0, -1, -1, 0, 100)[0])
	for _, want := range []string{"1 claude", "server", "3  ×"} {
		if !strings.Contains(row, want) {
			t.Fatalf("tab row = %q, want %q", row, want)
		}
	}
	if strings.Contains(row, "server node") {
		t.Fatalf("a chosen name was changed: %q", row)
	}
	at := len([]rune(row[:strings.Index(row, "server")]))
	if hit, ok := tabHitAtX(tabs, 0, at, 100); !ok || hit.index != 1 {
		t.Fatalf("click on server = (%+v, %v), want tab 1", hit, ok)
	}
	value := newDashboard(&fakeBackend{}, projectsSnapshot(nil))
	updated, _ := value.startRenameTab(tabs[0])
	if updated.(dashboard).input.text != "1" {
		t.Fatalf("rename starts from %q, want the stored name", updated.(dashboard).input.text)
	}
}

// Shortening a numbered tab to one column keeps it there: the program name
// is added once, before fitting, so eight agent tabs fit 90 columns. Adding it
// again after fitting widened the same row to 141.
func TestFittedNumberedTabsDoNotGrowBack(t *testing.T) {
	tabs := make([]model.Tab, 8)
	for index := range tabs {
		tabs[index] = model.Tab{ID: fmt.Sprint(index), Name: fmt.Sprint(index + 1), Running: true, Agent: model.AgentClaude}
	}
	row := ansi.Strip(renderTabBarWithHover(newUIStyles(true), tabs, 0, -1, -1, 0, 90)[0])
	if width := len([]rune(strings.TrimRight(row, " "))); width > 90 || !strings.HasSuffix(strings.TrimRight(row, " "), "+") {
		t.Fatalf("eight agent tabs = %q (%d columns), want them and + within 90", row, width)
	}
}

// Server-side refusals and slow local commands get hints that fit them.
func TestGitHintsMatchTheirCause(t *testing.T) {
	if hint := gitFailureHint(" ! [remote rejected] main -> main (protected branch hook declined)"); !strings.Contains(hint, "protection") {
		t.Fatalf("remote rejection hint = %q", hint)
	}
	if hint := gitFailureHint("switch timed out after 2m0s"); strings.Contains(hint, "remote") || strings.Contains(hint, "network") {
		t.Fatalf("local timeout hint = %q", hint)
	}
}

// A held hook offer is shown when a modal open at the first root closes.
func TestHeldHookOfferWaitsForTheModalToClose(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.offerAgentHooks([]agenthooks.Status{{Provider: agenthooks.ProviderClaude, State: agenthooks.StateMissing}})
	send(&value, key(tea.KeyF1, ""))
	send(&value, snapshotMsg{value: projectsSnapshot(nil)})
	if value.modal != helpModal {
		t.Fatalf("the offer replaced the open Help (modal %v)", value.modal)
	}
	send(&value, key(tea.KeyEscape, ""))
	if value.modal != hookInstallModal {
		t.Fatalf("after Help closed modal = %v, want the hook offer", value.modal)
	}
}

// Config scrolls when a short screen cannot hold every setting, and a click
// runs the setting drawn under it.
func TestConfigScrollsOnAShortScreen(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 100, 10
	send(&value, key(tea.KeyF3, ""))
	for range len(configRows()) {
		send(&value, key(tea.KeyDown, ""))
	}
	screen := ansi.Strip(value.render())
	if !strings.Contains(screen, "╰") || !strings.Contains(screen, "Sound on waiting") || !strings.Contains(screen, "Config ") {
		t.Fatalf("short Config lost its edge or its last row:\n%s", screen)
	}
	start, end := value.configWindow(value.dimensions().bodyHeight)
	if start == 0 || end != len(configRows()) {
		t.Fatalf("window = %d-%d, want it scrolled to the end", start, end)
	}
	index, ok := value.configIndexAtContentRow(end-start-1, value.dimensions().bodyHeight)
	if !ok || index != end-1 {
		t.Fatalf("last drawn row maps to %d (%v), want %d", index, ok, end-1)
	}
}

func TestKeepAwakeIsASetting(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	send(&value, key(tea.KeyF3, ""))
	if command := send(&value, key('w', "w")); !value.config.AllowSleep || command == nil {
		t.Fatal("w did not turn keeping awake off and save it")
	}
}

// The close confirmation names the agent and what it is doing.
func TestCloseTabSaysWhatStops(t *testing.T) {
	for _, probe := range []struct {
		tab  model.Tab
		want string
	}{
		{model.Tab{Agent: model.AgentClaude, AgentPhase: model.AgentPhaseWorking}, "Claude Code is working in it (Working) and will stop."},
		{model.Tab{Agent: model.AgentCodex, AgentPhase: model.AgentPhaseWaitingInput}, "Codex runs in it (Needs your answer) and will stop."},
		{model.Tab{Agent: model.AgentNode}, "Node.js runs in it and will stop."},
		{model.Tab{}, "Its shell and everything running in it will end."},
	} {
		if got := closeTabConsequence(probe.tab); got != probe.want {
			t.Fatalf("consequence = %q, want %q", got, probe.want)
		}
	}
}

// a opens the agent the last notification named before walking the others.
func TestJumpOpensTheNotifiedAgentFirst(t *testing.T) {
	value := newDashboard(&fakeBackend{}, attentionSnapshot(model.AgentPhaseWorking, model.AgentPhaseWorking))
	value.agentSoundReady = true
	value.focus = leftPane
	// Both start waiting, then only the second is reported again later.
	send(&value, agentSnapshotMsg{value: map[string]model.AgentStatus{
		"tab-1": {Agent: model.AgentCodex, Phase: model.AgentPhaseWaitingInput},
		"tab-2": {Agent: model.AgentCodex, Phase: model.AgentPhaseWorking},
	}})
	send(&value, agentSnapshotMsg{value: map[string]model.AgentStatus{
		"tab-1": {Agent: model.AgentCodex, Phase: model.AgentPhaseWaitingInput},
		"tab-2": {Agent: model.AgentCodex, Phase: model.AgentPhaseWaitingApproval},
	}})
	if value.lastAlertTab != "tab-2" {
		t.Fatalf("last alerted = %q, want tab-2", value.lastAlertTab)
	}
	send(&value, key('a', "a"))
	if value.tabIndex != 1 {
		t.Fatalf("a selected tab %d, want the notified second tab", value.tabIndex)
	}
}

// Another failure while the daemon is down is shown, with the daemon named in
// the rail.
func TestErrorsStayVisibleWhileTheDaemonIsDown(t *testing.T) {
	value := newDashboard(&fakeBackend{}, projectsSnapshot(nil))
	value.width, value.height = 120, 14
	send(&value, agentSnapshotMsg{err: gone})
	value.setError(gitError, notAGitRepository)
	screen := ansi.Strip(value.render())
	if !strings.Contains(screen, notAGitRepository) || !strings.Contains(screen, "daemon stopped · F5 starts it") {
		t.Fatalf("screen hides one of the two:\n%s", screen)
	}
}

// / narrows the tree to the workspaces that match, collapsed roots included;
// Enter opens the match and Esc brings the whole tree back.
func TestTreeFilterFindsAWorkspaceAnywhere(t *testing.T) {
	value := newDashboardWithConfig(&fakeBackend{}, collapseSnapshot(), "", Config{CollapsedRoots: []string{projectsRoot.Path}})
	value.width, value.height = 110, 16
	value.focus = leftPane
	send(&value, key('/', "/"))
	typeText(&value, "BRA")
	if got := navigationText(value); len(got) != 2 || got[1] != "- bravo" {
		t.Fatalf("filtered tree = %q, want projects and bravo", got)
	}
	if value.cursorPath != collapseBravo.Path {
		t.Fatalf("cursor = %q, want the first match", value.cursorPath)
	}
	if screen := ansi.Strip(value.render()); !strings.Contains(screen, " FIND  BRA") {
		t.Fatalf("rail does not show the filter:\n%s", screen)
	}
	typeText(&value, "zz")
	if screen := ansi.Strip(value.render()); !strings.Contains(screen, "No workspace matches") {
		t.Fatalf("an empty filter result is not explained:\n%s", screen)
	}
	send(&value, key(tea.KeyBackspace, ""))
	send(&value, key(tea.KeyBackspace, ""))
	command := send(&value, key(tea.KeyEnter, ""))
	if value.treeFiltering || value.treeQuery != "" || command == nil {
		t.Fatal("Enter did not open the match and end the filter")
	}
	if value.cursorPath != collapseBravo.Path || slices.Contains(value.config.CollapsedRoots, projectsRoot.Path) {
		t.Fatalf("after Enter = (cursor %q, collapsed %q), want bravo shown", value.cursorPath, value.config.CollapsedRoots)
	}

	send(&value, key('/', "/"))
	typeText(&value, "arch")
	send(&value, key(tea.KeyEscape, ""))
	if value.treeFiltering || len(navigationText(value)) < 4 {
		t.Fatalf("Esc left the tree filtered: %q", navigationText(value))
	}
}

// > moves the tab under the cursor right and the cursor follows it; the
// new-tab slot does not move.
func TestTreeMovesTheSelectedTab(t *testing.T) {
	snapshot, _ := alphaWorkspaceSnapshot(openTabs(3))
	backend := &fakeBackend{snapshot: snapshot}
	value := newDashboard(backend, snapshot)
	value.focus = leftPane
	value.setNavigation(1)
	value.tabIndex = 0
	command := send(&value, key('>', ">"))
	if command == nil || value.tabIndex != 1 {
		t.Fatalf("> = (command %v, cursor %d), want a move and the cursor on 1", command, value.tabIndex)
	}
	command()
	if backend.movedTabID != "tab-1" || backend.movedOffset != 1 {
		t.Fatalf("moved = (%q, %d), want tab-1 right", backend.movedTabID, backend.movedOffset)
	}
	value.tabIndex = 3
	if command := send(&value, key('>', ">")); command != nil {
		t.Fatal("the new-tab slot moved")
	}
}
