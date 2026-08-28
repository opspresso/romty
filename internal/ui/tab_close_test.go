package ui

import (
	"errors"
	"io"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

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
		width := lipgloss.Width("  " + displayText(tab.Name) + "  × ")
		if index == want {
			return position + width - 2
		}
		position += width + 1
	}
	return -1
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
	origin := value.dimensions().leftWidth + value.dimensions().separator

	updated, command := value.Update(tea.MouseClickMsg{
		X: origin + tabCloseLocalX(tabs, 1), Y: 0, Button: tea.MouseLeft,
	})
	value = updated.(dashboard)
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
	origin := value.dimensions().leftWidth + value.dimensions().separator

	updated, closeCommand := value.Update(tea.MouseClickMsg{
		X: origin + tabCloseLocalX(tabs, 1), Y: 0, Button: tea.MouseLeft,
	})
	value = updated.(dashboard)
	closingTerminal := value.terminal
	updated, _ = value.Update(terminalOutputMsg{terminal: closingTerminal, err: io.EOF})
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
	origin := value.dimensions().leftWidth + value.dimensions().separator
	updated, closeCommand := value.Update(tea.MouseClickMsg{
		X: origin + tabCloseLocalX(tabs, 0), Y: 0, Button: tea.MouseLeft,
	})
	value = updated.(dashboard)
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
