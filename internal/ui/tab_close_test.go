package ui

import (
	"errors"
	"io"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/opspresso/romty/internal/display"
	"github.com/opspresso/romty/internal/model"
)

func tabCloseSnapshot(tabs []model.Tab) (model.Snapshot, model.Workspace) {
	workspace := model.Workspace{
		ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha",
	}
	return model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace, Tabs: tabs}},
	}}}, workspace
}

func tabCloseLocalX(tabs []model.Tab, want int) int {
	position := 0
	for index, tab := range tabs {
		width := lipgloss.Width("  " + display.Text(tab.Name) + "  × ")
		if index == want {
			return position + width - 2
		}
		position += width + 1
	}
	return -1
}

// clickTabClose presses a tab's close button and answers the confirmation it
// opens, which is the whole path a click takes to reach the daemon.
func clickTabClose(t *testing.T, value dashboard, tabs []model.Tab, index int) (dashboard, tea.Cmd) {
	t.Helper()
	origin := value.dimensions().leftWidth + value.dimensions().separator
	updated, _ := value.Update(tea.MouseClickMsg{
		X: origin + tabCloseLocalX(tabs, index), Y: 0, Button: tea.MouseLeft,
	})
	value = updated.(dashboard)
	if value.modal != closeTabModal || value.closeTabTarget.ID != tabs[index].ID {
		t.Fatalf("close click = (modal %v, target %q), want the confirmation for %q",
			value.modal, value.closeTabTarget.ID, tabs[index].ID)
	}
	updated, command := value.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	return updated.(dashboard), command
}

// A close button that fires on the click alone would end a shell romty exists
// to keep running, so the confirmation has to stand between the two.
func TestDashboardKeepsTheTabUntilTheCloseIsConfirmed(t *testing.T) {
	tabs := []model.Tab{
		{ID: "tab-1", WorkspaceID: "workspace-1", Name: "1", Running: true},
		{ID: "tab-2", WorkspaceID: "workspace-1", Name: "2", Running: true},
	}
	snapshot, workspace := tabCloseSnapshot(tabs)
	backend := &fakeBackend{snapshot: snapshot}
	value := newDashboard(backend, snapshot)
	value.selectedWorkspaceID, value.selectedPath = workspace.ID, workspace.Path
	value.focus = terminalPane
	origin := value.dimensions().leftWidth + value.dimensions().separator

	updated, command := value.Update(tea.MouseClickMsg{
		X: origin + tabCloseLocalX(tabs, 1), Y: 0, Button: tea.MouseLeft,
	})
	value = updated.(dashboard)
	if command != nil || value.tabClosePending != "" || value.modal != closeTabModal {
		t.Fatalf("close click = (command %v, pending %q, modal %v), want only the confirmation",
			command, value.tabClosePending, value.modal)
	}
	updated, cancelled := value.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	value = updated.(dashboard)
	if cancelled != nil || value.modal != noModal || backend.closedTabID != "" {
		t.Fatalf("cancelled close = (command %v, modal %v, closed %q), want the tab left alone",
			cancelled, value.modal, backend.closedTabID)
	}
}

func TestTabHitSeparatesTheLabelAndCloseButton(t *testing.T) {
	tabs := []model.Tab{{Name: "1"}, {Name: "two"}}
	for _, probe := range []struct {
		x     int
		index int
		close bool
	}{
		{x: 2, index: 0},
		{x: tabCloseLocalX(tabs, 0), index: 0, close: true},
		{x: 10, index: 1},
		{x: tabCloseLocalX(tabs, 1), index: 1, close: true},
	} {
		hit, ok := tabHitAtX(tabs, probe.x)
		if !ok || hit.index != probe.index || hit.close != probe.close {
			t.Errorf("tabHitAtX(%d) = (%+v, %v), want index %d close=%v",
				probe.x, hit, ok, probe.index, probe.close)
		}
	}
}

func TestDashboardClosesAnInactiveTabWithoutReplacingTheOpenTerminal(t *testing.T) {
	tabs := []model.Tab{
		{ID: "tab-1", WorkspaceID: "workspace-1", Name: "1", Running: true},
		{ID: "tab-2", WorkspaceID: "workspace-1", Name: "2", Running: true},
		{ID: "tab-3", WorkspaceID: "workspace-1", Name: "3", Running: true},
	}
	snapshot, workspace := tabCloseSnapshot(tabs)
	backend := &fakeBackend{snapshot: snapshot}
	value := newDashboard(backend, snapshot)
	value.selectedWorkspaceID, value.selectedPath = workspace.ID, workspace.Path
	value.focus = terminalPane
	value.terminal = newEmbeddedTerminal(tabs[0].ID, newMemoryStream(""), 40, 10)
	t.Cleanup(value.closeTerminal)

	value, command := clickTabClose(t, value, tabs, 1)
	if command == nil || value.tabClosePending != tabs[1].ID {
		t.Fatalf("close click = (command %v, pending %q), want tab-2", command, value.tabClosePending)
	}
	updated, next := value.Update(commandMessage[tabClosedMsg](t, command))
	value = updated.(dashboard)
	if next != nil || backend.closedTabID != tabs[1].ID || value.terminal == nil || value.terminal.id != tabs[0].ID ||
		len(value.selectedTabs()) != 2 {
		t.Fatalf("inactive close = (next %v, closed %q, terminal %v, tabs %d)",
			next, backend.closedTabID, value.terminal != nil, len(value.selectedTabs()))
	}
}

func TestDashboardClosesTheActiveTabAndOpensItsNextSibling(t *testing.T) {
	tabs := []model.Tab{
		{ID: "tab-1", WorkspaceID: "workspace-1", Name: "1", Running: true},
		{ID: "tab-2", WorkspaceID: "workspace-1", Name: "2", Running: true},
		{ID: "tab-3", WorkspaceID: "workspace-1", Name: "3", Running: true},
	}
	snapshot, workspace := tabCloseSnapshot(tabs)
	backend := &fakeBackend{snapshot: snapshot}
	value := newDashboard(backend, snapshot)
	value.selectedWorkspaceID, value.selectedPath = workspace.ID, workspace.Path
	value.focus, value.tabIndex = terminalPane, 1
	value.terminal = newEmbeddedTerminal(tabs[1].ID, newMemoryStream(""), 40, 10)

	value, closeCommand := clickTabClose(t, value, tabs, 1)
	closingTerminal := value.terminal
	updated, _ := value.Update(terminalOutputMsg{terminal: closingTerminal, err: io.EOF})
	value = updated.(dashboard)
	updated, openCommand := value.Update(commandMessage[tabClosedMsg](t, closeCommand))
	value = updated.(dashboard)
	if openCommand == nil || value.tabIndex != 1 || value.terminal != nil {
		t.Fatalf("active close = (open %v, index %d, terminal %v), want next sibling", openCommand, value.tabIndex, value.terminal)
	}
	updated, _ = value.Update(commandMessage[terminalOpenedMsg](t, openCommand))
	value = updated.(dashboard)
	t.Cleanup(value.closeTerminal)
	if value.terminal == nil || value.terminal.id != tabs[2].ID {
		t.Fatalf("opened terminal = %v, want %q", value.terminal, tabs[2].ID)
	}
}

func TestDashboardClosesTheOnlyTabAndReturnsToTheWorkspacePane(t *testing.T) {
	tabs := []model.Tab{{ID: "tab-1", WorkspaceID: "workspace-1", Name: "1", Running: true}}
	snapshot, workspace := tabCloseSnapshot(tabs)
	backend := &fakeBackend{snapshot: snapshot}
	value := newDashboard(backend, snapshot)
	value.selectedWorkspaceID, value.selectedPath = workspace.ID, workspace.Path
	value.focus = terminalPane
	value.terminal = newEmbeddedTerminal(tabs[0].ID, newMemoryStream(""), 40, 10)

	value, closeCommand := clickTabClose(t, value, tabs, 0)
	updated, next := value.Update(commandMessage[tabClosedMsg](t, closeCommand))
	value = updated.(dashboard)
	if next != nil || value.terminal != nil || value.focus != leftPane || value.tabIndex != 0 {
		t.Fatalf("only close = (next %v, terminal %v, focus %v, index %d)",
			next, value.terminal, value.focus, value.tabIndex)
	}
}

func TestDashboardKeepsTheTabWhenCloseFails(t *testing.T) {
	tabs := []model.Tab{{ID: "tab-1", WorkspaceID: "workspace-1", Name: "1", Running: true}}
	snapshot, workspace := tabCloseSnapshot(tabs)
	backend := &fakeBackend{snapshot: snapshot}
	backend.fail("CloseTab", errors.New("daemon unavailable"))
	value := newDashboard(backend, snapshot)
	value.selectedWorkspaceID, value.selectedPath = workspace.ID, workspace.Path
	updated, closeCommand := value.closeTab(tabs[0], 0)
	value = updated.(dashboard)
	updated, next := value.Update(commandMessage[tabClosedMsg](t, closeCommand))
	value = updated.(dashboard)
	if next != nil || value.tabClosePending != "" || len(value.selectedTabs()) != 1 ||
		!strings.Contains(value.errorMessage, "daemon unavailable") {
		t.Fatalf("failed close = (next %v, pending %q, tabs %d, error %q)",
			next, value.tabClosePending, len(value.selectedTabs()), value.errorMessage)
	}
}

// A root holds terminals of its own, but a snapshot names the workspace they
// belong to nowhere except on the tabs themselves. Closing one from the
// workspace pane has to move the tab cursor the way a directory's tab does.
func TestDashboardClosesARootTabAndMovesItsCursor(t *testing.T) {
	tabs := []model.Tab{
		{ID: "tab-1", WorkspaceID: "workspace-root", Name: "1", Running: true},
		{ID: "tab-2", WorkspaceID: "workspace-root", Name: "2", Running: true},
	}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Tabs: tabs,
	}}}
	backend := &fakeBackend{snapshot: snapshot}
	value := newDashboard(backend, snapshot)
	value.focus, value.navIndex, value.tabIndex = leftPane, 0, 1

	updated, closeCommand := value.closeTab(tabs[1], 1)
	value = updated.(dashboard)
	updated, next := value.Update(commandMessage[tabClosedMsg](t, closeCommand))
	value = updated.(dashboard)
	if next != nil || len(value.navigationTabs()) != 1 || value.tabIndex != 0 {
		t.Fatalf("root close = (next %v, tabs %d, index %d), want the cursor on the remaining tab",
			next, len(value.navigationTabs()), value.tabIndex)
	}
}
