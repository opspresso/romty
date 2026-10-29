package ui

import (
	"errors"
	"fmt"
	"image/color"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/testutil"
)

func TestDashboardSelectsWorkspaceAndCreatesTerminal(t *testing.T) {
	snapshot, workspace := alphaWorkspaceSnapshot(nil)
	backend := &fakeBackend{
		snapshot:   snapshot,
		workspace:  workspace,
		createdTab: model.Tab{ID: "tab-1", WorkspaceID: workspace.ID, Name: "1", Running: true},
	}
	value := newDashboard(backend, snapshot)
	value.width = 120
	value.height = 40

	send(&value, key(tea.KeyDown, ""))
	command := chooseNewSession(t, &value)
	if command == nil {
		t.Fatal("select workspace command = nil")
	}
	command = send(&value, commandMessage[workspaceMsg](t, command))
	if value.selectedWorkspaceID != workspace.ID || value.focus != leftPane {
		t.Fatalf("selected workspace = %q, focus = %v", value.selectedWorkspaceID, value.focus)
	}
	if command == nil {
		t.Fatal("workspace without tabs did not create a terminal")
	}
	openCommand := send(&value, commandMessage[tabMsg](t, command))
	if openCommand == nil {
		t.Fatal("created tab did not open an embedded terminal")
	}
	readCommand := send(&value, commandMessage[terminalOpenedMsg](t, openCommand))
	defer value.closeTerminal()
	if value.terminal == nil || value.focus != terminalPane || readCommand == nil {
		t.Fatalf("terminal = %v, focus = %v, read command = %v", value.terminal, value.focus, readCommand)
	}
	if backend.createdColumns != 89 || backend.createdRows != 36 {
		t.Fatalf("terminal size = %dx%d, want right pane size 89x36", backend.createdColumns, backend.createdRows)
	}
	if backend.createCount != 1 {
		t.Fatalf("CreateTab() calls = %d, want 1", backend.createCount)
	}

	followup := readCommand()
	commands, ok := followup.(tea.BatchMsg)
	if !ok {
		t.Fatalf("terminal followup = %T, want read and resize commands", followup)
	}
	for _, command := range commands {
		if message := command(); message != nil {
			send(&value, message)
		}
	}
	rendered := value.render()
	if !strings.Contains(rendered, "projects") || !strings.Contains(rendered, "embedded terminal") {
		t.Fatalf("rendered dashboard does not contain both panes:\n%s", rendered)
	}
	send(&value, controlSlashKey())
	if value.focus != leftPane || value.terminal == nil {
		t.Fatalf("Ctrl+/ focus = %v, terminal = %v", value.focus, value.terminal)
	}
}

func TestDashboardSelectsRootAndCreatesTerminal(t *testing.T) {
	root := projectsRoot
	rootWorkspace := model.Workspace{ID: "root-workspace", RootID: root.ID, Name: root.Name, Path: root.Path}
	tab := model.Tab{ID: "root-tab", WorkspaceID: rootWorkspace.ID, Name: "1", Running: true}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: root,
		Directories: []model.WorkspaceView{{
			Workspace: model.Workspace{RootID: root.ID, Name: "cloned", Path: "/projects/cloned"},
		}},
	}}}
	backend := &fakeBackend{snapshot: snapshot, workspace: rootWorkspace, createdTab: tab}
	value := newDashboard(backend, snapshot)
	value.width = 120
	value.height = 40
	value.setNavigation(0)

	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "▌▾ projects") || !strings.Contains(rendered, "  - cloned") {
		t.Fatalf("root and indented workspace are not distinguishable:\n%s", rendered)
	}
	selectCommand := chooseNewSession(t, &value)
	if selectCommand == nil {
		t.Fatal("root selection command = nil")
	}
	createCommand := send(&value, commandMessage[workspaceMsg](t, selectCommand))
	if createCommand == nil || backend.ensuredRootID != root.ID || backend.ensuredPath != root.Path {
		t.Fatalf("root selection = (command %v, root %q, path %q)", createCommand, backend.ensuredRootID, backend.ensuredPath)
	}
	openCommand := send(&value, commandMessage[tabMsg](t, createCommand))
	if openCommand == nil || backend.createCount != 1 {
		t.Fatalf("root tab creation = (command %v, count %d)", openCommand, backend.createCount)
	}
	readCommand := send(&value, commandMessage[terminalOpenedMsg](t, openCommand))
	defer value.closeTerminal()
	if value.terminal == nil || value.terminal.id != tab.ID || readCommand == nil {
		t.Fatalf("root terminal = %v, read command = %v", value.terminal, readCommand)
	}
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "▎▾ projects") || !strings.Contains(rendered, "●") {
		t.Fatalf("root tab marker is missing:\n%s", rendered)
	}
}

func TestDashboardReloadsWorkspacesWhenReturningFromTerminal(t *testing.T) {
	root := projectsRoot
	rootWorkspace := model.Workspace{ID: "root-workspace", RootID: root.ID, Name: root.Name, Path: root.Path}
	tab := model.Tab{ID: "root-tab", WorkspaceID: rootWorkspace.ID, Name: "1", Running: true}
	initial := model.Snapshot{Roots: []model.RootView{{Root: root, Tabs: []model.Tab{tab}}}}
	refreshed := model.Snapshot{Roots: []model.RootView{{
		Root: root,
		Tabs: []model.Tab{tab},
		Directories: []model.WorkspaceView{{
			Workspace: model.Workspace{RootID: root.ID, Name: "cloned", Path: "/projects/cloned"},
		}},
	}}}
	backend := &fakeBackend{snapshot: refreshed, workspace: rootWorkspace}
	terminal := newEmbeddedTerminal(tab.ID, newMemoryStream(""), 40, 10)
	defer terminal.close()
	value := newDashboard(backend, initial)
	value.focus = terminalPane
	value.selectedWorkspaceID = rootWorkspace.ID
	value.selectedPath = root.Path
	value.terminal = terminal

	refreshCommand := send(&value, controlSlashKey())
	if value.focus != leftPane || refreshCommand == nil {
		t.Fatalf("Ctrl+/ result = (focus %v, command %v)", value.focus, refreshCommand)
	}
	send(&value, refreshCommand())
	if backend.snapshotCount != 1 || !strings.Contains(ansi.Strip(value.render()), "  - cloned") {
		t.Fatalf("reloaded workspace list = (snapshots %d):\n%s", backend.snapshotCount, value.render())
	}
}

func TestDashboardOpensExistingWorkspaceTabWithoutCreatingOne(t *testing.T) {
	tab := model.Tab{ID: "tab-1", WorkspaceID: "workspace-1", Name: "1", Running: true}
	snapshot, workspace := alphaWorkspaceSnapshot([]model.Tab{tab})
	backend := &fakeBackend{snapshot: snapshot, workspace: workspace}
	value := newDashboard(backend, snapshot)
	value.setNavigation(1)

	command := value.selectWorkspace()
	openCommand := send(&value, command())
	if openCommand == nil {
		t.Fatal("existing terminal was not opened")
	}
	if backend.createCount != 0 {
		t.Fatalf("CreateTab() calls = %d, want 0", backend.createCount)
	}
}

func TestDashboardCreatesTabWithCtrlShiftT(t *testing.T) {
	existing := model.Tab{ID: "tab-1", WorkspaceID: "workspace-1", Name: "1", Running: true}
	created := model.Tab{ID: "tab-2", WorkspaceID: "workspace-1", Name: "2", Running: true}
	snapshot, workspace := alphaWorkspaceSnapshot([]model.Tab{existing})
	backend := &fakeBackend{snapshot: snapshot, workspace: workspace, createdTab: created}
	value := newDashboard(backend, snapshot)
	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path
	value.terminal = newEmbeddedTerminal(existing.ID, newMemoryStream(""), 40, 10)
	value.focus = terminalPane
	t.Cleanup(value.closeTerminal)

	createCommand := send(&value, newTabKey())
	if createCommand == nil || !value.tabPending {
		t.Fatalf("Ctrl+Shift+T = (command %v, pending %t), want tab creation", createCommand, value.tabPending)
	}
	openCommand := send(&value, commandMessage[tabMsg](t, createCommand))
	if openCommand == nil || backend.createCount != 1 || value.tabPending {
		t.Fatalf("created tab = (command %v, calls %d, pending %t), want one tab ready to open",
			openCommand, backend.createCount, value.tabPending)
	}
	readCommand := send(&value, commandMessage[terminalOpenedMsg](t, openCommand))
	if readCommand == nil || value.terminal == nil || value.terminal.id != created.ID {
		t.Fatalf("opened terminal = (command %v, terminal %v), want %q", readCommand, value.terminal, created.ID)
	}
}

func TestDashboardCreatesTabForWorkspaceCursorWithCtrlShiftT(t *testing.T) {
	root := projectsRoot
	alpha := model.Workspace{ID: "workspace-1", RootID: root.ID, Name: "alpha", Path: "/projects/alpha"}
	bravo := model.Workspace{ID: "workspace-2", RootID: root.ID, Name: "bravo", Path: "/projects/bravo"}
	alphaTab := model.Tab{ID: "tab-1", WorkspaceID: alpha.ID, Name: "1", Running: true}
	created := model.Tab{ID: "tab-2", WorkspaceID: bravo.ID, Name: "1", Running: true}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: root,
		Directories: []model.WorkspaceView{
			{Workspace: alpha, Tabs: []model.Tab{alphaTab}},
			{Workspace: bravo},
		},
	}}}
	backend := &fakeBackend{snapshot: snapshot, workspace: bravo, createdTab: created}
	value := newDashboard(backend, snapshot)
	value.selectedWorkspaceID = alpha.ID
	value.selectedPath = alpha.Path
	value.terminal = newEmbeddedTerminal(alphaTab.ID, newMemoryStream(""), 40, 10)
	value.focus = leftPane
	value.setNavigation(2)
	t.Cleanup(value.closeTerminal)

	selectCommand := send(&value, newTabKey())
	if selectCommand == nil {
		t.Fatal("Ctrl+Shift+T on the workspace cursor produced no selection command")
	}
	createCommand := send(&value, commandMessage[workspaceMsg](t, selectCommand))
	if createCommand == nil || backend.ensuredPath != bravo.Path {
		t.Fatalf("selected workspace = (command %v, path %q), want %q", createCommand, backend.ensuredPath, bravo.Path)
	}
	openCommand := send(&value, commandMessage[tabMsg](t, createCommand))
	if openCommand == nil || backend.createCount != 1 || value.selectedWorkspaceID != bravo.ID {
		t.Fatalf("created tab = (command %v, calls %d, workspace %q), want one in %q",
			openCommand, backend.createCount, value.selectedWorkspaceID, bravo.ID)
	}
}

func TestDashboardIgnoresCtrlShiftTWhileTabCreationCannotStart(t *testing.T) {
	snapshot, _ := alphaWorkspaceSnapshot(nil)

	for _, probe := range []struct {
		name    string
		prepare func(*dashboard)
	}{
		{name: "modal open", prepare: func(value *dashboard) { value.modal = helpModal }},
		{name: "tab pending", prepare: func(value *dashboard) { value.tabPending = true }},
	} {
		t.Run(probe.name, func(t *testing.T) {
			value := newDashboard(&fakeBackend{}, snapshot)
			value.setNavigation(1)
			probe.prepare(&value)

			command := send(&value, newTabKey())
			for _, message := range commandMessages(command) {
				switch message.(type) {
				case workspaceMsg, tabMsg:
					t.Fatalf("Ctrl+Shift+T produced a workspace or tab command")
				}
			}
		})
	}
}

func TestDashboardMouseSelectsWorkspaceAndTab(t *testing.T) {
	tabs := openTabs(2)
	snapshot, workspace := alphaWorkspaceSnapshot(tabs)
	backend := &fakeBackend{snapshot: snapshot, workspace: workspace}
	value := newDashboard(backend, snapshot)
	value.width, value.height = 120, 30

	command := send(&value, tea.MouseClickMsg{X: 2, Y: 3, Button: tea.MouseLeft})
	if value.navIndex != 1 || value.cursorPath != workspace.Path || command == nil {
		t.Fatalf("workspace click = (index %d, path %q, command %v)", value.navIndex, value.cursorPath, command)
	}

	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path
	value.focus = terminalPane
	rightX := value.dimensions().leftWidth + value.dimensions().separator
	command = send(&value, tea.MouseClickMsg{X: rightX + 11, Y: 0, Button: tea.MouseLeft})
	if value.tabIndex != 1 || command == nil {
		t.Fatalf("tab click = (index %d, command %v), want second tab opened", value.tabIndex, command)
	}
}

func TestDashboardClaimsTheMouseWithoutAnOpenTerminal(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	if got := value.View().MouseMode; got != tea.MouseModeAllMotion {
		t.Fatalf("mouse mode = %v, want dashboard clicks enabled", got)
	}
}

// A terminal whose shell has exited is not somewhere the keyboard can go.
func TestDashboardKeepsWorkspaceFocusWhenSwitchingToADeadTerminal(t *testing.T) {
	value := scrolledDashboard(t, 200)
	value.focus = leftPane
	value.closeTerminal()

	send(&value, key(tea.KeyF7, ""))
	if value.focus != leftPane {
		t.Fatalf("F7 onto a dead terminal = focus %v, want the workspace pane", value.focus)
	}
}

func TestDashboardKeepsWorkspaceFocusWhenTheTerminalIsGone(t *testing.T) {
	value := scrolledDashboard(t, 200)
	value.focus = leftPane
	send(&value, key(tea.KeyF6, ""))
	value.closeTerminal()

	send(&value, key(tea.KeyEscape, ""))
	if value.scrollback || value.focus == terminalPane {
		t.Fatalf("leaving scrollback = (scrollback %v, focus %v), want the workspace pane for a dead terminal",
			value.scrollback, value.focus)
	}
}

func TestDashboardMovesToASiblingTabWhenTheShellExits(t *testing.T) {
	tabs := openTabs(3)
	value, backend, refresh := exitedDashboard(t, tabs, 1)

	// The daemon drops the tab whose shell exited.
	backend.snapshot.Roots[0].Directories[0].Tabs = []model.Tab{tabs[0], tabs[2]}
	open := send(&value, refresh())
	if open == nil {
		t.Fatal("no sibling tab was opened after the shell exited")
	}
	if value.tabIndex != 1 {
		t.Fatalf("tab index = %d, want the tab that took the exited one's place", value.tabIndex)
	}
	send(&value, open())
	defer value.closeTerminal()
	if backend.openedTab != "tab-3" || value.focus != terminalPane {
		t.Fatalf("opened %q with focus %v, want tab-3 in the terminal pane", backend.openedTab, value.focus)
	}
}

func TestDashboardMovesToTheLastTabWhenTheLastShellExits(t *testing.T) {
	tabs := openTabs(2)
	value, backend, refresh := exitedDashboard(t, tabs, 1)

	backend.snapshot.Roots[0].Directories[0].Tabs = tabs[:1]
	open := send(&value, refresh())
	if open == nil || value.tabIndex != 0 {
		t.Fatalf("trailing exit = (command %v, tab index %d), want the previous tab", open, value.tabIndex)
	}
	send(&value, open())
	defer value.closeTerminal()
	if backend.openedTab != "tab-1" {
		t.Fatalf("opened %q, want tab-1", backend.openedTab)
	}
}

func TestDashboardReturnsToTheWorkspaceWhenTheLastTabExits(t *testing.T) {
	tabs := openTabs(1)
	value, backend, refresh := exitedDashboard(t, tabs, 0)

	backend.snapshot.Roots[0].Directories[0].Tabs = nil
	open := send(&value, refresh())
	if open != nil {
		t.Fatalf("a terminal was opened with no tabs left: %v", open)
	}
	if value.focus != leftPane || value.navIndex != 1 {
		t.Fatalf("focus = %v at nav index %d, want the workspace pane on the same workspace",
			value.focus, value.navIndex)
	}
	rendered := ansi.Strip(value.render())
	if !strings.Contains(rendered, "Press Enter to open a terminal here") || value.View().Cursor != nil {
		t.Fatalf("the exited terminal is still on screen:\n%s", rendered)
	}
}

// A stream that drops while the tab keeps running is a lost connection, not an
// exit, and the same walk reattaches to it.
func TestDashboardReattachesWhenTheConnectionDrops(t *testing.T) {
	tabs := openTabs(1)
	value, backend, refresh := exitedDashboard(t, tabs, 0)

	open := send(&value, refresh())
	if open == nil {
		t.Fatal("a still running tab was not reattached")
	}
	send(&value, open())
	defer value.closeTerminal()
	if backend.openedTab != "tab-1" || value.terminal == nil {
		t.Fatalf("reattached to %q with terminal %v, want tab-1 open", backend.openedTab, value.terminal)
	}
}

// A tab whose creation failed must not leave the dashboard pointing at a
// terminal that was never opened.
func TestDashboardReportsAFailedTabCreation(t *testing.T) {
	snapshot, workspace := alphaWorkspaceSnapshot(nil)
	backend := &fakeBackend{snapshot: snapshot, workspace: workspace}
	backend.fail("CreateTab", errors.New("daemon: start shell PTY: permission denied"))
	value := newDashboard(backend, backend.snapshot)
	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path

	send(&value, tabMsg{err: backend.failure("CreateTab")})
	if value.terminal != nil || !strings.Contains(value.errorMessage, "permission denied") {
		t.Fatalf("failed creation = (terminal %v, error %q)", value.terminal != nil, value.errorMessage)
	}
}

// Recorded output is already in the emulator when the terminal becomes part
// of the dashboard. The event loop therefore renders only the final restored
// screen, while keeping the history available to scrollback.
func TestDashboardAdoptsACompletedTerminalReplay(t *testing.T) {
	snapshot, workspace := alphaWorkspaceSnapshot(openTabs(1))
	value := newDashboard(&fakeBackend{}, snapshot)
	value.width = 80
	value.height = 12
	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path
	var replay strings.Builder
	for line := 1; line <= 200; line++ {
		fmt.Fprintf(&replay, "line %03d\r\n", line)
	}
	stream := newMemoryStream("")

	command := send(&value, terminalOpenedMsg{
		tabID:  "tab-1",
		stream: stream,
		replay: []byte(replay.String()),
	})
	t.Cleanup(value.closeTerminal)

	if value.terminal == nil {
		t.Fatal("the restored terminal was not adopted")
	}
	if value.terminal.scrollbackLen() == 0 {
		t.Fatal("the restored terminal lost its scrollback")
	}
	if rendered := strings.Join(plainRows(value.terminal.render()), "\n"); !strings.Contains(rendered, "line 200") {
		t.Fatalf("restored screen did not land on the final output:\n%s", rendered)
	}
	if command == nil {
		t.Fatal("the restored terminal did not start reading live output")
	}
}

func TestDashboardReconnectsWhenTerminalInputFails(t *testing.T) {
	backend := &fakeBackend{}
	value := newDashboard(backend, model.Snapshot{})
	stream := newMemoryStream("")
	stream.writeErr = errors.New("connection reset")
	value.terminal = newEmbeddedTerminal("tab-1", stream, 40, 10)
	value.focus = terminalPane

	value.terminal.sendKey(key('x', "x"))
	select {
	case <-value.terminal.inputDone:
	case <-time.After(time.Second):
		t.Fatal("terminal input copy did not report the write failure")
	}
	message := value.terminal.read()()
	command := send(&value, message)

	if value.terminal != nil || !value.terminalExited {
		t.Fatalf("failed input = (terminal %v, exited %v), want reconnect", value.terminal != nil, value.terminalExited)
	}
	if !strings.Contains(value.errorMessage, "write terminal input: connection reset") {
		t.Fatalf("error message = %q, want the input failure", value.errorMessage)
	}
	if command == nil {
		t.Fatal("failed input did not refresh the daemon snapshot")
	}
}

// A connection that keeps dropping must not loop: each reattach replays the
// whole recording, and a client that falls behind is cut off again. Retries are
// spaced and bounded, and the last word is the user's.
func TestDashboardBacksOffRepeatedReattaches(t *testing.T) {
	tabs := openTabs(1)
	value, backend, refresh := exitedDashboard(t, tabs, 0)

	// drop reopens the terminal the way a completed attach does, then ends its
	// stream, which is what a connection the daemon cut off looks like.
	drop := func(t *testing.T, value dashboard, open tea.Cmd) dashboard {
		t.Helper()
		send(&value, open())
		if value.terminal == nil {
			t.Fatal("the reattach did not open a terminal")
		}
		send(&value, terminalOutputMsg{terminal: value.terminal, err: io.EOF})
		return value
	}

	// The first drop reconnects at once: a one-off should recover unnoticed.
	open := send(&value, refresh())
	if open == nil || value.reattachAttempts != 0 {
		t.Fatalf("first reattach = (command %v, retries %d), want an immediate reconnect",
			open, value.reattachAttempts)
	}
	value = drop(t, value, open)

	// Each further drop is a retry: spaced, and counted.
	for attempt := 1; attempt <= maximumReattachAttempts; attempt++ {
		delayed := send(&value, refresh())
		if delayed == nil {
			t.Fatalf("retry %d produced no command", attempt)
		}
		if value.reattachAttempts != attempt {
			t.Fatalf("retry %d recorded %d attempts", attempt, value.reattachAttempts)
		}
		// The command must be a timer rather than an immediate open, or the
		// loop this exists to damp is still a loop.
		waited := time.Now()
		message := delayed()
		if _, ok := message.(reopenTerminalMsg); !ok {
			t.Fatalf("retry %d returned %T, want a delayed reopen", attempt, message)
		}
		if elapsed := time.Since(waited); elapsed < initialReattachBackoff {
			t.Fatalf("retry %d waited %v, want at least %v", attempt, elapsed, initialReattachBackoff)
		}
		open = send(&value, message)
		if open == nil {
			t.Fatal("the backoff timer did not lead to a reattach")
		}
		value = drop(t, value, open)
	}

	// Past the ceiling romty stops on its own and says so.
	command := send(&value, refresh())
	if command != nil {
		t.Fatalf("romty kept retrying past %d attempts", maximumReattachAttempts)
	}
	if value.focus != leftPane || !strings.Contains(value.errorMessage, "keeps disconnecting") {
		t.Fatalf("giving up = (focus %v, error %q)", value.focus, value.errorMessage)
	}
	if backend.openedTab != "tab-1" {
		t.Fatalf("opened %q, want the dropping tab", backend.openedTab)
	}
}

func TestReattachBackoffGrowsAndIsCapped(t *testing.T) {
	previous := time.Duration(0)
	for attempt := 1; attempt <= 8; attempt++ {
		backoff := reattachBackoff(attempt)
		if backoff < previous {
			t.Fatalf("backoff shrank at attempt %d: %v after %v", attempt, backoff, previous)
		}
		if backoff > maximumReattachBackoff {
			t.Fatalf("backoff at attempt %d = %v, want at most %v", attempt, backoff, maximumReattachBackoff)
		}
		previous = backoff
	}
	if reattachBackoff(1) != initialReattachBackoff {
		t.Fatalf("first backoff = %v, want %v", reattachBackoff(1), initialReattachBackoff)
	}
}

func TestDashboardMovesWorkspaceAndTabCursorBeforeConfirming(t *testing.T) {
	firstWorkspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	secondWorkspace := model.Workspace{ID: "workspace-2", RootID: "root-1", Name: "beta", Path: "/projects/beta"}
	firstTab := model.Tab{ID: "tab-1", WorkspaceID: firstWorkspace.ID, Name: "one", Running: true}
	secondTab := model.Tab{ID: "tab-2", WorkspaceID: secondWorkspace.ID, Name: "two", Running: true}
	thirdTab := model.Tab{ID: "tab-3", WorkspaceID: secondWorkspace.ID, Name: "three", Running: true}
	snapshot := projectsSnapshot([]model.WorkspaceView{
		{Workspace: firstWorkspace, Tabs: []model.Tab{firstTab}},
		{Workspace: secondWorkspace, Tabs: []model.Tab{secondTab, thirdTab}},
	})
	backend := &fakeBackend{snapshot: snapshot, workspace: secondWorkspace}
	terminal := newEmbeddedTerminal(firstTab.ID, newMemoryStream(""), 40, 10)
	defer terminal.close()
	value := newDashboard(backend, snapshot)
	value.selectedWorkspaceID = firstWorkspace.ID
	value.selectedPath = firstWorkspace.Path
	value.terminal = terminal

	command := send(&value, key(tea.KeyDown, ""))
	if command != nil || value.navIndex != 1 {
		t.Fatalf("first workspace cursor move = (command %v, index %d)", command, value.navIndex)
	}
	command = send(&value, key(tea.KeyDown, ""))
	if command != nil || value.navIndex != 2 || value.terminal.id != firstTab.ID {
		t.Fatalf("workspace cursor move = (command %v, index %d, terminal %q)", command, value.navIndex, value.terminal.id)
	}
	if rendered := value.render(); !strings.Contains(rendered, value.styles.tabSelected.Render(" two  × ")) {
		t.Fatalf("candidate workspace tabs are not visible:\n%s", rendered)
	}

	command = send(&value, key(tea.KeyRight, ""))
	if command != nil || value.tabIndex != 1 || backend.openedTab != "" || value.terminal.id != firstTab.ID {
		t.Fatalf("tab cursor move opened a terminal: command=%v index=%d opened=%q terminal=%q", command, value.tabIndex, backend.openedTab, value.terminal.id)
	}

	selectCommand := send(&value, key(tea.KeyEnter, ""))
	if selectCommand == nil {
		t.Fatal("Enter did not select the workspace and tab")
	}
	openCommand := send(&value, selectCommand())
	if openCommand == nil {
		t.Fatal("confirmed tab did not produce an open command")
	}
	openCommand()
	if backend.openedTab != thirdTab.ID {
		t.Fatalf("opened tab = %q, want %q", backend.openedTab, thirdTab.ID)
	}
}

// Ctrl+Shift+Left/Right is the one tab key that reaches the terminal pane, so
// it has to switch on its own: moving the cursor and waiting for Enter is what
// the plain arrows already do.
func TestDashboardSwitchesTabsWithCtrlShiftArrows(t *testing.T) {
	tabs := openTabs(3)
	snapshot, workspace := alphaWorkspaceSnapshot(tabs)
	backend := &fakeBackend{snapshot: snapshot, workspace: workspace}
	value := newDashboard(backend, snapshot)
	value.setNavigation(1)
	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path
	value.terminal = newEmbeddedTerminal(tabs[0].ID, newMemoryStream(""), 40, 10)
	value.focus = terminalPane
	defer func() { value.closeTerminal() }()

	// The terminal pane keeps the key rather than sending it to the shell.
	value = pressSwitchTab(t, value, tea.KeyRight)
	if value.tabIndex != 1 || backend.openedTab != tabs[1].ID || value.terminal.id != tabs[1].ID {
		t.Fatalf("Ctrl+Shift+Right = (index %d, opened %q, terminal %q), want the second tab open",
			value.tabIndex, backend.openedTab, value.terminal.id)
	}

	// Left from the first tab wraps to the last rather than stopping there.
	value = pressSwitchTab(t, value, tea.KeyLeft)
	value = pressSwitchTab(t, value, tea.KeyLeft)
	last := len(tabs) - 1
	if value.tabIndex != last || value.terminal.id != tabs[last].ID {
		t.Fatalf("Ctrl+Shift+Left twice = (index %d, terminal %q), want the last tab open",
			value.tabIndex, value.terminal.id)
	}

	// The new-tab slot is a cursor position, not a tab to switch to.
	value.tabIndex = len(tabs)
	value = pressSwitchTab(t, value, tea.KeyRight)
	if value.tabIndex != 0 || value.terminal.id != tabs[0].ID {
		t.Fatalf("Ctrl+Shift+Right from + = (index %d, terminal %q), want the first tab open",
			value.tabIndex, value.terminal.id)
	}

	// The tab that is already open is left alone: reopening it would tear a
	// live terminal down and attach a new one in its place.
	backend.openedTab = ""
	value.tabIndex = 1
	command := send(&value, switchTabKey(tea.KeyLeft))
	if command != nil || backend.openedTab != "" || value.tabIndex != 1 {
		t.Fatalf("switching to the open tab = (command %v, opened %q, index %d), want nothing done",
			command, backend.openedTab, value.tabIndex)
	}

	// Scrollback belongs to the terminal it was opened on, so a switch made
	// from it lands on the new terminal's live screen.
	value.tabIndex = 0
	value.scrollback = true
	// altScroll comes with scrollback everywhere else, so the shortcut sets it
	// too: a dashboard that disagreed with the host would spend this switch
	// correcting the mode instead of opening the tab.
	value.altScroll = true
	value.scrollOffset = 5
	value = pressSwitchTab(t, value, tea.KeyRight)
	if value.scrollback || value.scrollOffset != 0 || value.terminal.id != tabs[1].ID {
		t.Fatalf("switching from scrollback = (scrollback %v, offset %d, terminal %q), want the live screen",
			value.scrollback, value.scrollOffset, value.terminal.id)
	}

	// A modal is a question waiting for an answer; switching behind it would
	// change the terminal the answer applies to.
	value.modal = shutdownModal
	backend.openedTab = ""
	command = send(&value, switchTabKey(tea.KeyRight))
	if command != nil || backend.openedTab != "" {
		t.Fatalf("switching with a modal open = (command %v, opened %q), want nothing done",
			command, backend.openedTab)
	}
}

// The tab rail in the terminal pane shows the open terminal's tabs, not the
// cursor's: the workspace cursor is free to wander while a terminal is open.
// Switching has to move along the row on screen.
func TestDashboardSwitchesTabsOfTheOpenTerminalNotTheCursor(t *testing.T) {
	open := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	other := model.Workspace{ID: "workspace-2", RootID: "root-1", Name: "beta", Path: "/projects/beta"}
	openTabs := []model.Tab{
		{ID: "tab-1", WorkspaceID: open.ID, Name: "1", Running: true},
		{ID: "tab-2", WorkspaceID: open.ID, Name: "2", Running: true},
	}
	otherTab := model.Tab{ID: "tab-3", WorkspaceID: other.ID, Name: "1", Running: true}
	snapshot := projectsSnapshot([]model.WorkspaceView{
		{Workspace: open, Tabs: openTabs},
		{Workspace: other, Tabs: []model.Tab{otherTab}},
	})
	backend := &fakeBackend{snapshot: snapshot, workspace: other}
	value := newDashboard(backend, snapshot)
	value.selectedWorkspaceID = open.ID
	value.selectedPath = open.Path
	value.terminal = newEmbeddedTerminal(openTabs[0].ID, newMemoryStream(""), 40, 10)
	value.focus = terminalPane
	// The cursor was left on the other workspace, which is what the workspace
	// pane would switch along.
	value.setNavigation(2)
	defer func() { value.closeTerminal() }()

	value = pressSwitchTab(t, value, tea.KeyRight)
	if value.terminal.id != openTabs[1].ID || backend.ensuredPath != "" {
		t.Fatalf("Ctrl+Shift+Right = (terminal %q, ensured %q), want the open workspace's second tab",
			value.terminal.id, backend.ensuredPath)
	}
}

func TestDashboardSelectsPlusAndCreatesTabOnEnter(t *testing.T) {
	tabs := openTabs(2)
	snapshot, workspace := alphaWorkspaceSnapshot(tabs)
	backend := &fakeBackend{
		snapshot:   snapshot,
		workspace:  workspace,
		createdTab: model.Tab{ID: "tab-3", WorkspaceID: workspace.ID, Name: "3", Running: true},
	}
	value := newDashboard(backend, snapshot)
	value.setNavigation(1)
	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path

	command := send(&value, key(tea.KeyLeft, ""))
	if command != nil || value.tabIndex != len(tabs) {
		t.Fatalf("left from first tab = (command %v, index %d), want + index %d", command, value.tabIndex, len(tabs))
	}
	if rendered := value.render(); !strings.Contains(rendered, value.styles.tabSelected.Render("  +  ")) {
		t.Fatalf("+ cursor is not visible:\n%s", rendered)
	}

	selectCommand := send(&value, key(tea.KeyEnter, ""))
	if selectCommand == nil {
		t.Fatal("Enter on + did not select the workspace")
	}
	createCommand := send(&value, commandMessage[workspaceMsg](t, selectCommand))
	if createCommand == nil {
		t.Fatal("Enter on + did not produce a create command")
	}
	createdMessage := commandMessage[tabMsg](t, createCommand)
	if backend.createCount != 1 || backend.openedTab != "" {
		t.Fatalf("Enter on + created %d tabs and opened %q", backend.createCount, backend.openedTab)
	}
	openCommand := send(&value, createdMessage)
	if openCommand == nil {
		t.Fatal("created tab did not produce an open command")
	}
}

func TestDashboardHighlightsNavigationAndShowsOpenTabs(t *testing.T) {
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root-1", Name: "nalbam", Path: "/projects"},
		Directories: []model.WorkspaceView{
			{
				Workspace: model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "SnowClash", Path: "/projects/SnowClash"},
				Tabs: []model.Tab{
					{ID: "tab-1", Name: "1", Running: true},
					{ID: "tab-2", Name: "2", Running: false},
					{ID: "tab-3", Name: "3", Running: true},
				},
			},
			{Workspace: model.Workspace{ID: "workspace-2", RootID: "root-1", Name: "TankClash", Path: "/projects/TankClash"}},
		},
	}}}
	value := newDashboard(&fakeBackend{}, snapshot)
	value.width = 120
	value.height = 40
	value.setNavigation(1)
	value.selectedWorkspaceID = "workspace-1"
	value.selectedPath = "/projects/SnowClash"

	rendered := value.render()
	plain := ansi.Strip(rendered)
	if strings.Contains(rendered, "Terminal") || strings.Contains(rendered, "/projects/SnowClash") {
		t.Fatalf("terminal title or workspace path is visible:\n%s", rendered)
	}
	if strings.Contains(plain, "> ") {
		t.Fatalf("navigation draws an arrow cursor:\n%s", rendered)
	}
	if !strings.Contains(plain, "▾ nalbam") || !strings.Contains(plain, "▌ - SnowClash") {
		t.Fatalf("navigation tree or selection color is missing:\n%s", rendered)
	}
	view := value.dimensions()
	leftWidth, bodyHeight := view.leftWidth, view.bodyHeight
	navigation := ansi.Strip(strings.Join(value.renderNavigation(leftWidth, bodyHeight), "\n"))
	snowClash := ""
	for line := range strings.SplitSeq(navigation, "\n") {
		if strings.Contains(line, "SnowClash") {
			snowClash = strings.TrimRight(line, " ")
		}
	}
	if !strings.HasSuffix(snowClash, "●●") || strings.HasSuffix(snowClash, "●●●") {
		t.Fatalf("SnowClash row = %q, want markers for exactly the two running tabs", snowClash)
	}
	if strings.Contains(plain, "2 exited") {
		t.Fatalf("exited tab was included in terminal tabs:\n%s", rendered)
	}
	if !strings.Contains(rendered, value.styles.tabSelected.Render(" 1  × ")) || !strings.Contains(rendered, value.styles.tab.Render(" 3  × ")) {
		t.Fatalf("terminal tabs are not styled as active and inactive tabs:\n%s", rendered)
	}
	status := value.styles.shortcutKey.Render(" F2 ") + " " + value.styles.shortcutDescription.Render("add root")
	if !strings.Contains(rendered, status) {
		t.Fatalf("status shortcuts and descriptions are not separated:\n%s", rendered)
	}
	if !strings.Contains(plain, "  - TankClash") {
		t.Fatalf("workspace without tabs is missing:\n%s", rendered)
	}

	value.tabIndex = 3
	send(&value, snapshotMsg{value: snapshot})
	if value.tabIndex != 2 {
		t.Fatalf("tab index after exited tab removal = %d, want + index 2", value.tabIndex)
	}
}

func TestDashboardRestoresTheLastWorkspaceAndTab(t *testing.T) {
	alpha := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	bravo := model.Workspace{ID: "workspace-2", RootID: "root-1", Name: "bravo", Path: "/projects/bravo"}
	tabs := []model.Tab{
		{ID: "tab-2", WorkspaceID: bravo.ID, Name: "1", Running: true},
		{ID: "tab-3", WorkspaceID: bravo.ID, Name: "2", Running: true},
	}
	snapshot := projectsSnapshot([]model.WorkspaceView{
		{Workspace: alpha, Tabs: []model.Tab{{ID: "tab-1", WorkspaceID: alpha.ID, Name: "1", Running: true}}},
		{Workspace: bravo, Tabs: tabs},
	})
	backend := &fakeBackend{snapshot: snapshot}
	value := newDashboardWithConfig(backend, snapshot, "", Config{
		LastWorkspacePath: bravo.Path,
		LastTabID:         tabs[1].ID,
	})

	if value.selectedPath != bravo.Path || value.selectedWorkspaceID != bravo.ID ||
		value.navIndex != 2 || value.tabIndex != 1 {
		t.Fatalf("restored selection = (path %q, workspace %q, nav %d, tab %d)",
			value.selectedPath, value.selectedWorkspaceID, value.navIndex, value.tabIndex)
	}

	var opened terminalOpenedMsg
	for _, message := range commandMessages(value.Init()) {
		if candidate, ok := message.(terminalOpenedMsg); ok {
			opened = candidate
		}
	}
	if opened.tabID != tabs[1].ID || backend.openedTab != tabs[1].ID {
		t.Fatalf("startup opened tab = (%q, %q), want %q", opened.tabID, backend.openedTab, tabs[1].ID)
	}
	send(&value, opened)
	t.Cleanup(value.closeTerminal)
	if value.terminal == nil || value.terminal.id != tabs[1].ID || value.focus != terminalPane {
		t.Fatalf("restored terminal = (%v, focus %v), want %q", value.terminal, value.focus, tabs[1].ID)
	}
}

func TestDashboardDoesNotRestoreAMissingWorkspaceOrTab(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	snapshot := projectsSnapshot([]model.WorkspaceView{{
		Workspace: workspace,
		Tabs: []model.Tab{
			{ID: "running", WorkspaceID: workspace.ID, Running: true},
			{ID: "stopped", WorkspaceID: workspace.ID},
		},
	}})

	for _, probe := range []Config{
		{LastWorkspacePath: "/projects/missing", LastTabID: "running"},
		{LastWorkspacePath: workspace.Path, LastTabID: "missing"},
		{LastWorkspacePath: workspace.Path, LastTabID: "stopped"},
	} {
		value := newDashboardWithConfig(&fakeBackend{}, snapshot, "", probe)
		if value.selectedPath != "" || value.selectedWorkspaceID != "" || value.restorePending {
			t.Fatalf("invalid config %#v restored selection = (path %q, workspace %q, pending %t)",
				probe, value.selectedPath, value.selectedWorkspaceID, value.restorePending)
		}
	}
}

func TestDashboardPersistsTheLastOpenedWorkspaceAndTab(t *testing.T) {
	tabs := openTabs(2)
	snapshot, workspace := alphaWorkspaceSnapshot(tabs)
	configPath := filepath.Join(t.TempDir(), "config.json")
	value := newDashboardWithConfig(&fakeBackend{}, snapshot, configPath, Config{})
	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path
	value.tabIndex = 1

	command := send(&value, terminalOpenedMsg{tabID: tabs[1].ID, stream: newMemoryStream("")})
	t.Cleanup(value.closeTerminal)
	if command == nil {
		t.Fatal("opening a different tab did not save its selection")
	}
	for _, message := range commandMessages(command) {
		if saved, ok := message.(configSavedMsg); ok {
			send(&value, saved)
		}
	}
	loaded, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if loaded.LastWorkspacePath != workspace.Path || loaded.LastTabID != tabs[1].ID {
		t.Fatalf("saved selection = (%q, %q), want (%q, %q)",
			loaded.LastWorkspacePath, loaded.LastTabID, workspace.Path, tabs[1].ID)
	}
}

func TestDashboardAdaptsChromeToTerminalBackground(t *testing.T) {
	testutil.Set(t, &osHostname, func() (string, error) { return "host", nil })
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	if value.Init() == nil {
		t.Fatal("Init() did not request the terminal background color")
	}
	darkAccent := value.styles.paneTitleActive.GetForeground()

	command := send(&value, tea.BackgroundColorMsg{Color: color.White})
	if command != nil {
		t.Fatalf("background color update command = %v, want nil", command)
	}
	if reflect.DeepEqual(darkAccent, value.styles.paneTitleActive.GetForeground()) {
		t.Fatal("light and dark palettes use the same accent color")
	}
	if !strings.Contains(value.render(), value.styles.paneTitleActive.Render(" romty · host ")) {
		t.Fatal("light palette was not applied to the dashboard")
	}
}

func TestDashboardDoesNotCaptureWorkspaceShortcutsInTerminal(t *testing.T) {
	terminal := newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)
	defer terminal.close()
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.focus = terminalPane
	value.terminal = terminal

	for _, message := range []tea.KeyPressMsg{
		key('i', "i"), key('a', "a"), key(',', ","), key('q', "q"), key('r', "r"), key('+', "+"), key('?', "?"),
	} {
		command := send(&value, message)
		if command != nil || value.modal != noModal {
			t.Fatalf("terminal key %q was captured: command=%v modal=%v", message.String(), command, value.modal)
		}
	}
}

func TestDashboardIgnoresAReattachForTheTabItLeft(t *testing.T) {
	tabs := openTabs(2)
	value, _, refresh := exitedDashboard(t, tabs, 0)

	open := send(&value, refresh())
	send(&value, open())
	refresh = send(&value, terminalOutputMsg{terminal: value.terminal, err: io.EOF})
	delayed := send(&value, refresh())
	if delayed == nil {
		t.Fatal("the repeated drop produced no delayed reattach")
	}

	send(&value, switchTabKey(tea.KeyRight))
	if value.selectedTabID() != "tab-2" {
		t.Fatalf("selected tab = %q, want tab-2", value.selectedTabID())
	}
	stale := send(&value, delayed())
	if stale != nil {
		t.Fatal("a delayed reattach for tab-1 reopened tab-2")
	}
}

// Opening a terminal is a round trip to the daemon, so two quick switches
// leave two of them in flight and nothing orders the answers. The one for the
// tab the user left must not be adopted: it would show that terminal under the
// next tab's name and take the keyboard with it.
func TestDashboardIgnoresATerminalOpenedForTheTabItLeft(t *testing.T) {
	tabs := openTabs(3)
	snapshot, workspace := alphaWorkspaceSnapshot(tabs)
	backend := &fakeBackend{snapshot: snapshot, workspace: workspace}
	value := newDashboard(backend, snapshot)
	value.setNavigation(1)
	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path
	value.terminal = newEmbeddedTerminal(tabs[0].ID, newMemoryStream(""), 40, 10)
	value.focus = terminalPane
	t.Cleanup(value.closeTerminal)

	// Two presses before either answer lands.
	toSecond := send(&value, switchTabKey(tea.KeyRight))
	toThird := send(&value, switchTabKey(tea.KeyRight))
	if toSecond == nil || toThird == nil {
		t.Fatal("two switches did not both ask the daemon for a terminal")
	}
	second, third := toSecond().(terminalOpenedMsg), toThird().(terminalOpenedMsg)

	// The later answer arrives first, which is what the daemon is free to do.
	send(&value, third)
	send(&value, second)

	if value.terminal == nil || value.terminal.id != tabs[2].ID {
		t.Fatalf("terminal = %v, want the tab the cursor is on", value.terminal)
	}
	stale, ok := second.stream.(*memoryStream)
	if !ok {
		t.Fatalf("stale stream = %T, want a memory stream", second.stream)
	}
	if !stale.isClosed() {
		t.Fatal("the terminal romty walked away from is still attached to the daemon")
	}
}

func TestDashboardSizesTerminalWhenAttachCompletes(t *testing.T) {
	snapshot, workspace := alphaWorkspaceSnapshot(openTabs(1))
	backend := &fakeBackend{snapshot: snapshot, workspace: workspace}
	value := newDashboard(backend, snapshot)
	value.width = 80
	value.height = 24
	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path
	value.setNavigation(1)

	open := value.openSelectedTerminal()
	if open == nil {
		t.Fatal("running tab did not produce an attach command")
	}
	resize := send(&value, tea.WindowSizeMsg{Width: 120, Height: 40})
	if resize != nil {
		t.Fatal("window resize without an attached terminal contacted the daemon")
	}

	followup := send(&value, open())
	defer value.closeTerminal()
	if value.terminal == nil || followup == nil {
		t.Fatalf("attach result = (terminal %v, followup %v), want an open terminal", value.terminal, followup)
	}
	if commands, ok := followup().(tea.BatchMsg); ok {
		for _, command := range commands {
			if command != nil {
				command()
			}
		}
	}
	wantColumns, wantRows := value.terminalSize()
	if backend.createdColumns != wantColumns || backend.createdRows != wantRows {
		t.Fatalf("attached PTY size = %dx%d, want current window size %dx%d",
			backend.createdColumns, backend.createdRows, wantColumns, wantRows)
	}
}
