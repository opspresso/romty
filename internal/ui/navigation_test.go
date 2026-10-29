package ui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/opspresso/romty/internal/gitrepo"
	"github.com/opspresso/romty/internal/model"
)

func TestDashboardSupportsRemainingWorkspaceShortcuts(t *testing.T) {
	backend := &fakeBackend{}

	value := newDashboard(backend, model.Snapshot{})
	command := send(&value, key('i', "i"))
	if value.modal != aboutModal || command != nil {
		t.Fatalf("i result = (modal %v, command %v), want about modal", value.modal, command)
	}

	value = newDashboard(backend, model.Snapshot{})
	command = send(&value, key(',', ","))
	if value.modal != configModal || command != nil {
		t.Fatalf(", result = (modal %v, command %v), want config modal", value.modal, command)
	}
}

// The cursor stops at the ends of the tree. The wheel can reach romty as arrow
// keys — three per notch, through the terminal's alternate scroll — so a tree
// that wrapped would send one notch at the bottom back to the top.
func TestDashboardNavigationStopsAtBothEnds(t *testing.T) {
	snapshot := projectsSnapshot([]model.WorkspaceView{
		{Workspace: model.Workspace{RootID: "root-1", Name: "api", Path: "/projects/api"}},
		{Workspace: model.Workspace{RootID: "root-1", Name: "web", Path: "/projects/web"}},
	})
	value := newDashboard(&fakeBackend{}, snapshot)

	for range 3 {
		send(&value, key(tea.KeyUp, ""))
	}
	if value.navIndex != 0 {
		t.Fatalf("cursor after ↑ past the top = %d, want 0", value.navIndex)
	}

	last := len(value.navigationItems()) - 1
	for range 5 {
		send(&value, key(tea.KeyDown, ""))
	}
	if value.navIndex != last {
		t.Fatalf("cursor after ↓ past the end = %d, want %d", value.navIndex, last)
	}
}

func TestDashboardSupportsIMEIndependentShortcuts(t *testing.T) {
	backend := &fakeBackend{}

	value := newDashboard(backend, model.Snapshot{})
	command := send(&value, key(tea.KeyF1, ""))
	if value.modal != helpModal || command != nil {
		t.Fatalf("F1 result = (modal %v, command %v), want help modal", value.modal, command)
	}

	value = newDashboard(backend, model.Snapshot{})
	command = send(&value, key(tea.KeyF2, ""))
	if value.modal != browseModal || command == nil {
		t.Fatalf("F2 result = (modal %v, command %v), want the root picker reading", value.modal, command)
	}

	value = newDashboard(backend, model.Snapshot{})
	command = send(&value, key(tea.KeyF3, ""))
	if value.modal != configModal || command != nil {
		t.Fatalf("F3 result = (modal %v, command %v), want config modal", value.modal, command)
	}

	value = newDashboard(backend, model.Snapshot{})
	command = send(&value, key(tea.KeyF5, ""))
	if command == nil {
		t.Fatal("F5 refresh command = nil")
	}
	var forcedGitFetch bool
	for _, message := range commandMessages(command) {
		if status, ok := message.(gitStatusMsg); ok {
			forcedGitFetch = forcedGitFetch || !status.fetchedAt.IsZero()
			if _, followup := value.Update(message); followup != nil {
				t.Fatal("F5 Git fetch started another refresh loop")
			}
			continue
		}
		value.Update(message)
	}
	if backend.snapshotCount != 1 {
		t.Fatalf("F5 snapshot calls = %d, want 1", backend.snapshotCount)
	}
	if !forcedGitFetch {
		t.Fatal("F5 did not force a Git fetch")
	}

	value = newDashboard(backend, model.Snapshot{})
	command = send(&value, tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	if !value.result.Quit || command == nil {
		t.Fatalf("Ctrl+C result = (quit %v, command %v), want quit", value.result.Quit, command)
	}

	terminal := newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)
	value = newDashboard(backend, model.Snapshot{})
	value.focus = terminalPane
	value.terminal = terminal
	value.width = 120
	if rendered := value.render(); !shortcutOrder(rendered, "F1", "F2", "F3", "F4", "F5", "F6") {
		t.Fatalf("terminal shortcuts are not in F1-F6 order:\n%s", rendered)
	} else if !strings.Contains(rendered, value.styles.shortcutKey.Render(" Ctrl+/ ")) {
		// Assert on a key the fixed status row does not also carry, or the
		// check passes with the contextual rail deleted outright.
		t.Fatalf("terminal status bar does not contain navigation shortcut:\n%s", rendered)
	}
	command = send(&value, key(tea.KeyF3, ""))
	if value.modal != configModal || command != nil {
		t.Fatalf("terminal F3 result = (modal %v, command %v), want config modal", value.modal, command)
	}
	command = send(&value, key(tea.KeyF4, ""))
	if !value.result.Quit || command == nil || value.terminal != nil {
		t.Fatalf("F4 result = (quit %v, command %v, terminal %v), want global quit", value.result.Quit, command, value.terminal)
	}

	// A tree with a root: before the first root the rail names F2 alone.
	value = newDashboard(backend, projectsSnapshot(nil))
	value.width = 120
	rendered := value.render()
	for _, status := range []string{"F1", "F2", "F3", "F4", "F5", "F6"} {
		if !strings.Contains(rendered, value.styles.shortcutKey.Render(" "+status+" ")) {
			t.Fatalf("status bar does not contain %s:\n%s", status, rendered)
		}
	}
	if !shortcutOrder(rendered, "F1", "F2", "F3", "F4", "F5", "F6") {
		t.Fatalf("workspace shortcuts are not in F1-F6 order:\n%s", rendered)
	}
	for _, key := range []string{"↑/↓", "←/→", "Enter", "Tab"} {
		if !strings.Contains(rendered, value.styles.shortcutKey.Render(" "+key+" ")) {
			t.Fatalf("workspace status bar does not contain %q navigation shortcut:\n%s", key, rendered)
		}
	}
	for _, key := range []string{"i", "a", ",", "q", "r", "+", "?"} {
		if strings.Contains(rendered, value.styles.shortcutKey.Render(" "+key+" ")) {
			t.Fatalf("hidden shortcut %q is shown in the status bar:\n%s", key, rendered)
		}
	}
	for _, description := range []string{"add root", "quit"} {
		if !strings.Contains(rendered, value.styles.shortcutDescription.Render(description)) {
			t.Fatalf("status bar does not contain %q description:\n%s", description, rendered)
		}
	}
}

func TestDashboardHighlightsMouseTargetsOnHover(t *testing.T) {
	tab := model.Tab{ID: "tab-1", WorkspaceID: "workspace-1", Name: "1", Running: true}
	snapshot, _ := alphaWorkspaceSnapshot([]model.Tab{tab})
	value := newDashboard(&fakeBackend{}, snapshot)
	value.width, value.height = 120, 30
	value.setNavigation(1)
	_, defaultDivider := value.paneSeparators()

	before := value.render()
	send(&value, tea.MouseMotionMsg{X: 2, Y: 2})
	if value.hover.kind != hoverNavigation || value.hover.index != 0 || value.render() == before {
		t.Fatalf("navigation hover = %#v", value.hover)
	}

	seam := value.dimensions().leftWidth + 1
	before = value.render()
	send(&value, tea.MouseMotionMsg{X: seam, Y: 5})
	if value.hover.kind != hoverDivider || value.render() == before {
		t.Fatalf("divider hover = %#v", value.hover)
	}
	if _, hoveredDivider := value.paneSeparators(); hoveredDivider == defaultDivider {
		t.Fatal("divider hover kept its default color")
	}
	send(&value, tea.MouseMotionMsg{X: seam + 10, Y: 5})
	if value.hover.kind != hoverNone {
		t.Fatalf("mouse out hover = %#v, want none", value.hover)
	}
	if _, divider := value.paneSeparators(); divider != defaultDivider {
		t.Fatal("divider did not return to its default color after mouse out")
	}

	origin := value.dimensions().leftWidth + value.dimensions().separator
	before = value.render()
	send(&value, tea.MouseMotionMsg{X: origin + tabCloseLocalX([]model.Tab{tab}, 0), Y: 0})
	if value.hover.kind != hoverTabClose || value.hover.index != 0 || value.render() == before {
		t.Fatalf("tab close hover = %#v", value.hover)
	}

	before = value.render()
	send(&value, tea.MouseMotionMsg{X: origin + 11, Y: 0})
	if value.hover.kind != hoverTab || value.hover.index != 1 || value.render() == before {
		t.Fatalf("tab hover = %#v", value.hover)
	}
}

func TestDashboardHighlightsDialogMouseTargets(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 100, 24
	send(&value, key(tea.KeyF9, ""))
	hit := value.modalActionHits(value.modalGeometry(value.width, value.dimensions().bodyHeight))[0]
	before := value.render()
	send(&value, tea.MouseMotionMsg{X: hit.left, Y: hit.row})
	if value.hover.kind != hoverModalAction || value.hover.index != 0 || value.render() == before {
		t.Fatalf("modal action hover = %#v", value.hover)
	}

	value.modal = configModal
	left, top := value.modalGeometry(value.width, value.dimensions().bodyHeight).contentOrigin()
	before = value.render()
	send(&value, tea.MouseMotionMsg{X: left + 2, Y: top + 2})
	if value.hover.kind != hoverConfigRow || value.hover.index != 1 || value.render() == before {
		t.Fatalf("config hover = %#v", value.hover)
	}
}

func TestDashboardKeepsMouseWithTheHostUnlessPassthroughIsOn(t *testing.T) {
	value := scrolledDashboard(t, 200)
	// A guest that wants the mouse, the way Claude Code and htop do.
	value.terminal.writeOutput([]byte("\x1b[?1003h\x1b[?1006h"))

	if value.terminal.guestMouseMode() != tea.MouseModeAllMotion {
		t.Fatalf("guest mouse mode = %v, want all motion", value.terminal.guestMouseMode())
	}
	if value.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("mouse mode = %v, want romty to capture the live wheel", value.View().MouseMode)
	}
	send(&value, tea.MouseWheelMsg{X: 40, Y: 6, Button: tea.MouseWheelUp})
	if !value.scrollback {
		t.Fatal("wheel did not open scrollback while passthrough was disabled")
	}
	waitForGuestSilence(t, value.terminal, "")
	send(&value, key(tea.KeyEscape, ""))

	value.config.MousePassthrough = true
	if value.View().MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("mouse mode = %v, want the guest's own mode mirrored", value.View().MouseMode)
	}
	leftWidth := value.dimensions().leftWidth
	value.Update(tea.MouseWheelMsg{X: leftWidth + 3 + 4, Y: terminalTop + 2, Button: tea.MouseWheelUp})
	waitForGuest(t, guest(value), "\x1b[<64;5;3")
	sent := guest(value).String()

	// Events over the workspace pane are not the guest's business.
	value.Update(tea.MouseWheelMsg{X: 1, Y: terminalTop + 2, Button: tea.MouseWheelUp})
	waitForGuestSilence(t, value.terminal, sent)

	// Copy mode takes the mouse back so the host can select the scrolled page.
	send(&value, key(tea.KeyF6, ""))
	if !value.scrollback || value.View().MouseMode != tea.MouseModeNone {
		t.Fatalf("copy mode = (scrollback %v, mouse %v), want the mouse returned to the host",
			value.scrollback, value.View().MouseMode)
	}
}

// Above the threshold the split is worth its width, so a desktop keeps the
// layout it had and its PTY is never resized by a focus change.
func TestDashboardKeepsWorkspacePaneOnAWideScreen(t *testing.T) {
	value := narrowDashboard(t, 120)
	if value.navigationHidden() {
		t.Fatal("a 120 column screen hid the workspace pane")
	}
	send(&value, key(tea.KeyF7, ""))
	send(&value, key(tea.KeyF7, ""))
	if view := value.dimensions(); view.leftWidth == 0 || view.separator != separatorWidth {
		t.Fatalf("wide layout after two toggles = (left %d, separator %d), want the split",
			view.leftWidth, view.separator)
	}
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "alpha") {
		t.Fatalf("workspace tree is missing from a wide screen:\n%s", rendered)
	}
}

// A shell exiting in the background must not pull the user out of the tree.
func TestDashboardKeepsWorkspaceFocusWhenABackgroundShellExits(t *testing.T) {
	tabs := openTabs(2)
	value, backend, _ := exitedDashboard(t, tabs, 0)
	value.focus = leftPane
	value.terminalExited = true

	backend.snapshot.Roots[0].Directories[0].Tabs = tabs[1:]
	open := send(&value, snapshotMsg{value: backend.snapshot})
	if open != nil || value.focus != leftPane {
		t.Fatalf("background exit = (command %v, focus %v), want the workspace pane untouched", open, value.focus)
	}
}

// Opening a terminal can fail between the snapshot and the attach, because the
// shell may exit in between. The dashboard has to fall back to the tree.
func TestDashboardReturnsToTheTreeWhenOpeningFails(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.focus = terminalPane

	send(&value, terminalOpenedMsg{err: errors.New("daemon: running terminal session not found")})
	if value.focus != leftPane || value.terminal != nil {
		t.Fatalf("failed open = (focus %v, terminal %v), want the workspace pane", value.focus, value.terminal != nil)
	}
	if !strings.Contains(value.errorMessage, "session not found") {
		t.Fatalf("error message = %q, want the attach failure", value.errorMessage)
	}
}

func TestDashboardLeavesLettersUnboundInTheWorkspacePane(t *testing.T) {
	for _, letter := range []rune{'a', 'q', 'r', 's', 'd', 't'} {
		t.Run(string(letter), func(t *testing.T) {
			value := scrolledDashboard(t, 200)
			value.focus = leftPane

			command := send(&value, key(letter, string(letter)))
			if command != nil || value.scrollback || value.modal != noModal {
				t.Fatalf("%q = (command %v, scrollback %v, modal %v), want no shortcut action",
					letter, command, value.scrollback, value.modal)
			}
		})
	}
}

// Esc has to mean no. Removing a root has no undo beyond adding it back and
// rebuilding the tree by hand.
func TestDashboardCancelsRemovingARoot(t *testing.T) {
	snapshot := twoRootSnapshot()
	backend := &fakeBackend{snapshot: snapshot}
	value := newDashboard(backend, snapshot)

	updated, _ := value.confirmRemoveSelection(cursorItem(t, value))
	value = updated.(dashboard)
	if value.modal != removeSelectionModal {
		t.Fatalf("remove modal = %v, want the confirmation", value.modal)
	}
	command := send(&value, key(tea.KeyEscape, ""))
	if value.modal != noModal || command != nil {
		t.Fatalf("Esc = (modal %v, command %v), want the confirmation closed", value.modal, command)
	}
	if backend.removedRootID != "" {
		t.Fatalf("Esc removed %q", backend.removedRootID)
	}
}

// The tree is rebuilt from the daemon on every refresh. A directory appearing
// above the cursor — a `git clone` finishing in another terminal — must not
// slide the highlight onto a workspace the user did not choose.
func TestDashboardKeepsTheCursorOnTheSameWorkspace(t *testing.T) {
	tree := func(names ...string) model.Snapshot {
		directories := make([]model.WorkspaceView, 0, len(names))
		for _, name := range names {
			directories = append(directories, model.WorkspaceView{
				Workspace: model.Workspace{ID: name, RootID: "root-1", Name: name, Path: "/projects/" + name},
			})
		}
		return model.Snapshot{Roots: []model.RootView{{
			Root:        projectsRoot,
			Directories: directories,
		}}}
	}

	value := newDashboard(&fakeBackend{}, tree("beta", "gamma"))
	value.width = 120
	value.height = 40
	// The tree is root, beta, gamma; move onto the last of them.
	value.moveNavigation(1)
	value.moveNavigation(1)
	item, _ := value.navigationItem()
	if item.workspace.Name != "gamma" {
		t.Fatalf("the cursor starts on %q, want gamma", item.workspace.Name)
	}

	// A clone finishes and sorts above the cursor.
	send(&value, snapshotMsg{value: tree("alpha", "beta", "gamma")})
	item, ok := value.navigationItem()
	if !ok || item.workspace.Name != "gamma" {
		t.Fatalf("after the refresh the cursor is on %q, want gamma", item.workspace.Name)
	}
	if !strings.Contains(ansi.Strip(value.render()), "▌ - gamma") {
		t.Fatalf("the highlight is not on gamma:\n%s", ansi.Strip(value.render()))
	}

	// The workspace the cursor was on disappears: fall back rather than
	// pointing at nothing.
	send(&value, snapshotMsg{value: tree("alpha", "beta")})
	if _, ok := value.navigationItem(); !ok {
		t.Fatal("the cursor points outside the tree after its workspace was removed")
	}
}

// Ctrl+Shift+Up/Down is the other axis of the same chord: it reaches a
// terminal in another workspace without leaving the terminal pane. Only
// workspaces with a terminal running are stops, because a root lists every
// child directory whether it has been used or not.
func TestDashboardSwitchesWorkspacesWithCtrlShiftArrows(t *testing.T) {
	root := projectsRoot
	// alpha and charlie have terminals; bravo is an empty directory between
	// them, and delta an empty one after.
	alpha := model.Workspace{ID: "workspace-1", RootID: root.ID, Name: "alpha", Path: "/projects/alpha"}
	bravo := model.Workspace{RootID: root.ID, Name: "bravo", Path: "/projects/bravo"}
	charlie := model.Workspace{ID: "workspace-3", RootID: root.ID, Name: "charlie", Path: "/projects/charlie"}
	delta := model.Workspace{RootID: root.ID, Name: "delta", Path: "/projects/delta"}
	alphaTab := model.Tab{ID: "tab-1", WorkspaceID: alpha.ID, Name: "1", Running: true}
	charlieTab := model.Tab{ID: "tab-3", WorkspaceID: charlie.ID, Name: "1", Running: true}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: root,
		Directories: []model.WorkspaceView{
			{Workspace: alpha, Tabs: []model.Tab{alphaTab}},
			{Workspace: bravo},
			{Workspace: charlie, Tabs: []model.Tab{charlieTab}},
			{Workspace: delta},
		},
	}}}

	backend := &fakeBackend{snapshot: snapshot, workspace: charlie}
	value := newDashboard(backend, snapshot)
	value.selectedWorkspaceID = alpha.ID
	value.selectedPath = alpha.Path
	value.terminal = newEmbeddedTerminal(alphaTab.ID, newMemoryStream(""), 40, 10)
	value.focus = terminalPane
	t.Cleanup(value.closeTerminal)

	// Down skips bravo, which has nothing running, and opens charlie's.
	value = pressSwitchTab(t, value, tea.KeyDown)
	if backend.openedTab != charlieTab.ID || value.terminal.id != charlieTab.ID {
		t.Fatalf("Ctrl+Shift+Down = (opened %q, terminal %q), want charlie's terminal",
			backend.openedTab, value.terminal.id)
	}
	if value.selectedPath != charlie.Path {
		t.Fatalf("selected path = %q, want %q", value.selectedPath, charlie.Path)
	}
	// The cursor follows, so the tree shows where the keyboard now is.
	if item, ok := value.navigationItem(); !ok || item.workspace.Path != charlie.Path {
		t.Fatalf("cursor = %+v, want it on charlie", item)
	}

	// Down again wraps past delta and the root to alpha rather than stopping.
	backend.workspace = alpha
	value = pressSwitchTab(t, value, tea.KeyDown)
	if backend.openedTab != alphaTab.ID || value.terminal.id != alphaTab.ID {
		t.Fatalf("Ctrl+Shift+Down twice = (opened %q, terminal %q), want it wrapped to alpha",
			backend.openedTab, value.terminal.id)
	}

	// Up from alpha wraps the other way, to the last workspace with a terminal.
	backend.workspace = charlie
	value = pressSwitchTab(t, value, tea.KeyUp)
	if value.terminal.id != charlieTab.ID {
		t.Fatalf("Ctrl+Shift+Up = terminal %q, want it wrapped to charlie", value.terminal.id)
	}
}

// The chord is anchored on the workspace that is open, not on the cursor. A
// cursor anchor makes the key do nothing whenever it points at the workspace
// before the open one, which from the workspace pane is where it often sits.
func TestDashboardSwitchesWorkspacesFromTheOpenOneNotTheCursor(t *testing.T) {
	root := projectsRoot
	alpha := model.Workspace{ID: "workspace-1", RootID: root.ID, Name: "alpha", Path: "/projects/alpha"}
	bravo := model.Workspace{ID: "workspace-2", RootID: root.ID, Name: "bravo", Path: "/projects/bravo"}
	alphaTab := model.Tab{ID: "tab-1", WorkspaceID: alpha.ID, Name: "1", Running: true}
	bravoTab := model.Tab{ID: "tab-2", WorkspaceID: bravo.ID, Name: "1", Running: true}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: root,
		Directories: []model.WorkspaceView{
			{Workspace: alpha, Tabs: []model.Tab{alphaTab}},
			{Workspace: bravo, Tabs: []model.Tab{bravoTab}},
		},
	}}}

	backend := &fakeBackend{snapshot: snapshot, workspace: alpha}
	value := newDashboard(backend, snapshot)
	// bravo's terminal is open while the cursor sits on alpha, the workspace
	// just above it.
	value.selectedWorkspaceID = bravo.ID
	value.selectedPath = bravo.Path
	value.terminal = newEmbeddedTerminal(bravoTab.ID, newMemoryStream(""), 40, 10)
	value.focus = leftPane
	value.setNavigation(1)
	t.Cleanup(value.closeTerminal)

	value = pressSwitchTab(t, value, tea.KeyDown)
	if backend.openedTab != alphaTab.ID || value.terminal.id != alphaTab.ID {
		t.Fatalf("Ctrl+Shift+Down = (opened %q, terminal %q), want alpha's terminal rather than nothing",
			backend.openedTab, value.terminal.id)
	}
}

// Nothing running anywhere, or only the workspace already open: there is
// nowhere to go, and reopening the one that is open would tear a live terminal
// down and attach a new one in its place.
func TestDashboardKeepsTheWorkspaceWhenThereIsNowhereToSwitch(t *testing.T) {
	root := projectsRoot
	only := model.Workspace{ID: "workspace-1", RootID: root.ID, Name: "alpha", Path: "/projects/alpha"}
	empty := model.Workspace{RootID: root.ID, Name: "bravo", Path: "/projects/bravo"}

	for _, probe := range []struct {
		name string
		tabs []model.Tab
	}{
		{name: "no terminal anywhere"},
		{name: "only the open workspace", tabs: []model.Tab{{ID: "tab-1", WorkspaceID: only.ID, Name: "1", Running: true}}},
	} {
		t.Run(probe.name, func(t *testing.T) {
			snapshot := model.Snapshot{Roots: []model.RootView{{
				Root: root,
				Directories: []model.WorkspaceView{
					{Workspace: only, Tabs: probe.tabs},
					{Workspace: empty},
				},
			}}}
			backend := &fakeBackend{snapshot: snapshot, workspace: only}
			value := newDashboard(backend, snapshot)
			value.selectedWorkspaceID = only.ID
			value.selectedPath = only.Path

			for _, code := range []rune{tea.KeyDown, tea.KeyUp} {
				command := send(&value, switchTabKey(code))
				if command != nil || backend.openedTab != "" {
					t.Fatalf("Ctrl+Shift+%s = (command %v, opened %q), want nothing done",
						switchTabKey(code).String(), command, backend.openedTab)
				}
			}
		})
	}
}

// A repository gets a second line for its Git state; a plain directory does
// not, so the tree holds more workspaces on a short screen.
func TestNavigationGivesOnlyRepositoriesASecondLine(t *testing.T) {
	value := newDashboard(&fakeBackend{}, projectsSnapshot([]model.WorkspaceView{
		{Workspace: model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "first", Path: "/projects/first"}},
		{Workspace: model.Workspace{ID: "workspace-2", RootID: "root-1", Name: "last", Path: "/projects/last"}},
	}))
	value.focus = terminalPane
	value.selectedPath = "/projects/first"
	value.gitStates = map[string]gitrepo.State{"/projects/first": {Branch: "main"}}
	lines := value.renderNavigation(28, 20)
	if len(lines) != 6 {
		t.Fatalf("navigation lines = %d, want header, the root, two lines for first and one for last\n%s", len(lines), strings.Join(lines, "\n"))
	}
	plain := make([]string, len(lines))
	for index, line := range lines {
		plain[index] = strings.TrimRight(ansi.Strip(line), " ")
	}
	if plain[2] != " ▾ projects" || plain[3] != "▎ - first" || plain[4] != "▎   (main)" {
		t.Fatalf("root and workspace rows = %#v", plain[2:5])
	}
	if plain[5] != "  - last" {
		t.Fatalf("plain directory row = %q, want one line", plain[5])
	}
}

func TestNavigationSeparatesRootsWithOneBlankLine(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{Roots: []model.RootView{
		{Root: projectsRoot},
		{Root: model.Root{ID: "root-2", Name: "archive", Path: "/archive"}},
	}})
	lines := value.renderNavigation(28, 20)
	if len(lines) != 5 {
		t.Fatalf("navigation lines = %d, want header, two roots, and one separator\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if strings.TrimSpace(ansi.Strip(lines[3])) != "" || strings.TrimSpace(ansi.Strip(lines[4])) != "▾ archive" {
		t.Fatalf("second root rows = %#v, want a blank line before archive", lines[3:5])
	}
}

// One blank row sets a root apart from the item above it. A workspace that is
// not a Git repository already ends in a blank row, so the gap stays one row.
func TestNavigationKeepsOneBlankLineAboveARoot(t *testing.T) {
	for _, probe := range []struct {
		name   string
		hasGit bool
		want   []string
		row    int
	}{
		{name: "after a repository", hasGit: true,
			want: []string{"", "▾ projects", "- first", "(main)", "", "▾ archive"}, row: 6},
		{name: "after a plain directory",
			want: []string{"", "▾ projects", "- first", "", "▾ archive"}, row: 5},
	} {
		t.Run(probe.name, func(t *testing.T) {
			value := newDashboard(&fakeBackend{}, model.Snapshot{Roots: []model.RootView{
				{Root: projectsRoot, Directories: []model.WorkspaceView{
					{Workspace: model.Workspace{RootID: projectsRoot.ID, Name: "first", Path: "/projects/first"}},
				}},
				{Root: model.Root{ID: "root-2", Name: "archive", Path: "/archive"}},
			}})
			value.focus = terminalPane
			if probe.hasGit {
				value.gitStates = map[string]gitrepo.State{"/projects/first": {Branch: "main"}}
			}
			lines := value.renderNavigation(28, 20)
			plain := make([]string, len(lines))
			for index, line := range lines {
				plain[index] = strings.TrimSpace(ansi.Strip(line))
			}
			if !slices.Equal(plain[1:], probe.want) {
				t.Fatalf("navigation rows = %#v, want %#v", plain[1:], probe.want)
			}
			if index, ok := value.navigationIndexAtRow(probe.row, 20); !ok || index != 2 {
				t.Fatalf("row %d = (item %d, %v), want archive", probe.row, index, ok)
			}
		})
	}
}

func TestDashboardKeepsWideWorkspaceNamesWithinViewport(t *testing.T) {
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root-1", Name: "플랫폼-프로젝트-모음", Path: "/projects"},
		Directories: []model.WorkspaceView{{
			Workspace: model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "아주-긴-워크스페이스-이름", Path: "/projects/long"},
		}},
	}}}
	value := newDashboard(&fakeBackend{}, snapshot)
	value.width = 40
	value.height = 12
	value.setNavigation(1)
	value.gitStates = map[string]gitrepo.State{"/projects/long": {
		Branch: "feat/a-very-long-workspace-branch-name",
		Dirty:  true,
		Ahead:  123,
		Behind: 456,
	}}

	for index, line := range strings.Split(value.render(), "\n") {
		if lineWidth := lipgloss.Width(line); lineWidth > value.width {
			t.Fatalf("rendered line %d width = %d, want at most %d:\n%s", index, lineWidth, value.width, line)
		}
	}
}

func TestDashboardKeepsNavigationCursorVisible(t *testing.T) {
	directories := make([]model.WorkspaceView, 0, 40)
	for index := range 40 {
		name := fmt.Sprintf("workspace-%02d", index)
		directories = append(directories, model.WorkspaceView{
			Workspace: model.Workspace{ID: name, RootID: "root-1", Name: name, Path: "/projects/" + name},
		})
	}
	value := newDashboard(&fakeBackend{}, model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "nalbam", Path: "/projects"},
		Directories: directories,
	}}})
	value.gitStates = repositoryStates(directories)
	value.width = 120
	value.height = 12
	view := value.dimensions()
	leftWidth, bodyHeight := view.leftWidth, view.bodyHeight

	// Moving inside the current page leaves the viewport still. Only the next
	// move, which would put the cursor below it, advances by the one root row
	// needed to make the workspace fit.
	for range 3 {
		value.moveNavigation(1)
	}
	if value.navOffset != 0 {
		t.Fatalf("viewport moved to %d while the cursor was still visible", value.navOffset)
	}
	value.moveNavigation(1)
	if value.navOffset != 1 {
		t.Fatalf("viewport moved to %d at the lower edge, want the minimal offset 1", value.navOffset)
	}

	for _, navIndex := range []int{0, 20, len(directories)} {
		value.setNavigation(navIndex)
		lines := value.renderNavigation(leftWidth, bodyHeight)
		if len(lines) > bodyHeight {
			t.Fatalf("navigation at index %d rendered %d lines, want at most %d", navIndex, len(lines), bodyHeight)
		}
		item, ok := value.navigationItem()
		if !ok {
			t.Fatalf("navigation item at index %d is missing", navIndex)
		}
		if !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), item.workspace.Name) {
			t.Fatalf("navigation at index %d does not show the selected %q:\n%s",
				navIndex, item.workspace.Name, ansi.Strip(strings.Join(lines, "\n")))
		}
	}
}

// The shortcuts are what the rail is for; a note that does not fit beside them
// is dropped rather than pushing them off the row.
func TestShortcutRailDropsANoteThatDoesNotFit(t *testing.T) {
	styles := newUIStyles(true)
	keys := []shortcut{{key: "Ctrl+/", description: "navigation"}}
	full := ansi.Strip(renderShortcutRailNote(styles, 20, "344k ctx  $1.25", keys...))
	if strings.Contains(full, "ctx") {
		t.Fatalf("narrow rail = %q, want the note dropped", full)
	}
	if !strings.Contains(full, "navigation") {
		t.Fatalf("narrow rail = %q, want the shortcuts kept", full)
	}
	wide := renderShortcutRailNote(styles, 120, "344k ctx  $1.25", keys...)
	if got := lipgloss.Width(wide); got != 120 {
		t.Fatalf("rail width = %d, want 120", got)
	}
}
