package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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
