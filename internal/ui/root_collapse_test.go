package ui

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/model"
)

var (
	collapseAlpha = model.Workspace{ID: "workspace-1", RootID: projectsRoot.ID, Name: "alpha", Path: "/projects/alpha"}
	collapseBravo = model.Workspace{ID: "workspace-2", RootID: projectsRoot.ID, Name: "bravo", Path: "/projects/bravo"}
	archiveRoot   = model.Root{ID: "root-2", Name: "archive", Path: "/archive"}
)

func collapseSnapshot(bravoTabs ...model.Tab) model.Snapshot {
	return model.Snapshot{Roots: []model.RootView{
		{Root: projectsRoot, Directories: []model.WorkspaceView{
			{Workspace: collapseAlpha},
			{Workspace: collapseBravo, Tabs: bravoTabs},
		}},
		{Root: archiveRoot},
	}}
}

func navigationText(value dashboard) []string {
	lines := value.renderNavigation(28, 20)
	plain := make([]string, 0, len(lines))
	for _, line := range lines[2:] {
		// The cursor and current marks are not what these tests are about.
		if text := strings.TrimLeft(strings.TrimSpace(ansi.Strip(line)), "▌▎ "); text != "" {
			plain = append(plain, text)
		}
	}
	return plain
}

func runSave(t *testing.T, command tea.Cmd) {
	t.Helper()
	if command == nil {
		t.Fatal("no command, want the config saved")
	}
	for _, message := range commandMessages(command) {
		if saved, ok := message.(configSavedMsg); ok {
			if saved.err != nil {
				t.Fatalf("save error = %v", saved.err)
			}
			return
		}
	}
	t.Fatal("command did not save the config")
}

func TestSpaceCollapsesAndExpandsTheRootUnderTheCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	value := newDashboardWithConfig(&fakeBackend{}, collapseSnapshot(), path, Config{})
	value.focus = leftPane

	runSave(t, send(&value, key(tea.KeySpace, " ")))
	if got, want := navigationText(value), []string{"▸ projects", "▾ archive"}; !slices.Equal(got, want) {
		t.Fatalf("collapsed tree = %q, want %q", got, want)
	}
	config, err := loadConfig(path)
	if err != nil || !slices.Equal(config.CollapsedRoots, []string{projectsRoot.Path}) {
		t.Fatalf("saved collapsed roots = (%q, %v)", config.CollapsedRoots, err)
	}

	// Down now lands on the next root, not on a hidden workspace.
	send(&value, key(tea.KeyDown, ""))
	if value.cursorPath != archiveRoot.Path {
		t.Fatalf("cursor after Down = %q, want the archive root", value.cursorPath)
	}

	send(&value, key(tea.KeyUp, ""))
	runSave(t, send(&value, key(tea.KeySpace, " ")))
	if got := navigationText(value); len(got) != 4 || got[0] != "▾ projects" || got[1] != "- alpha" {
		t.Fatalf("expanded tree = %q", got)
	}
	if value.config.CollapsedRoots != nil {
		t.Fatalf("collapsed roots = %q, want none", value.config.CollapsedRoots)
	}
}

func TestSpaceOnAWorkspaceCollapsesItsRootAndKeepsTheCursorOnTheRoot(t *testing.T) {
	value := newDashboard(&fakeBackend{}, collapseSnapshot())
	value.focus = leftPane
	send(&value, key(tea.KeyDown, ""))
	send(&value, key(tea.KeyDown, ""))
	if value.cursorPath != collapseBravo.Path {
		t.Fatalf("cursor = %q, want bravo", value.cursorPath)
	}

	send(&value, key(tea.KeySpace, " "))
	if value.navIndex != 0 || value.cursorPath != projectsRoot.Path {
		t.Fatalf("cursor = (%d, %q), want the projects root", value.navIndex, value.cursorPath)
	}
	if !slices.Equal(value.config.CollapsedRoots, []string{projectsRoot.Path}) {
		t.Fatalf("collapsed roots = %q", value.config.CollapsedRoots)
	}
}

// A collapsed root still shows what runs under it, and stands in for the open
// workspace it hides.
func TestCollapsedRootShowsHiddenTerminalsAndTheOpenWorkspace(t *testing.T) {
	tab := model.Tab{ID: "tab-1", WorkspaceID: collapseBravo.ID, Name: "1", Running: true}
	value := newDashboardWithConfig(&fakeBackend{}, collapseSnapshot(tab), "",
		Config{CollapsedRoots: []string{projectsRoot.Path}})
	value.focus = terminalPane
	value.selectedWorkspaceID = collapseBravo.ID
	value.selectedPath = collapseBravo.Path

	lines := value.renderNavigation(28, 20)
	root := ansi.Strip(lines[2])
	if !strings.HasPrefix(root, "▎▸ projects") || !strings.Contains(root, "●") {
		t.Fatalf("collapsed root row = %q, want the current mark and bravo's tab marker", root)
	}

	// Leaving the terminal puts the cursor on the root without expanding it.
	value.focusNavigation()
	if value.cursorPath != projectsRoot.Path || len(value.config.CollapsedRoots) != 1 {
		t.Fatalf("focusNavigation = (cursor %q, collapsed %q)", value.cursorPath, value.config.CollapsedRoots)
	}
	if !value.rememberSelection(tab.ID) {
		t.Fatal("rememberSelection ignored a tab inside a collapsed root")
	}
}

func TestClickingTheRootArrowTogglesWithoutOpeningTheRoot(t *testing.T) {
	value := newDashboard(&fakeBackend{}, collapseSnapshot())
	value.width, value.height = 120, 30

	runSave(t, send(&value, tea.MouseClickMsg{X: rootArrowColumn, Y: 2, Button: tea.MouseLeft}))
	if value.modal != noModal || !slices.Equal(value.config.CollapsedRoots, []string{projectsRoot.Path}) {
		t.Fatalf("arrow click = (modal %v, collapsed %q), want the root folded and nothing opened",
			value.modal, value.config.CollapsedRoots)
	}

	// The name still opens the root, here as a session choice.
	send(&value, tea.MouseClickMsg{X: 4, Y: 2, Button: tea.MouseLeft})
	if value.modal != workspaceActionsModal || len(value.config.CollapsedRoots) != 1 {
		t.Fatalf("name click = (modal %v, collapsed %q), want the root opened and still folded",
			value.modal, value.config.CollapsedRoots)
	}
}

// Jumping to a waiting agent inside a collapsed root expands the root, so the
// cursor lands on the workspace that is opened.
func TestJumpingToAWaitingAgentExpandsItsRoot(t *testing.T) {
	tab := model.Tab{ID: "tab-1", WorkspaceID: collapseBravo.ID, Name: "1", Running: true,
		Agent: model.AgentClaude, AgentPhase: model.AgentPhaseWaitingInput}
	snapshot := collapseSnapshot(tab)
	backend := &fakeBackend{snapshot: snapshot, workspace: collapseBravo}
	value := newDashboardWithConfig(backend, snapshot, "", Config{CollapsedRoots: []string{projectsRoot.Path}})

	// The jump batches the config save with the switch; the save is checked
	// on its own and the switch is settled the way every other jump is.
	command := send(&value, jumpToWaitingAgentKey())
	batch, ok := command().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("jump command = %#v, want the save batched with the switch", batch)
	}
	runSave(t, batch[0])
	settleSwitch(&value, batch[1])
	t.Cleanup(func() { value.closeTerminal() })
	if value.config.CollapsedRoots != nil {
		t.Fatalf("collapsed roots = %q, want the root expanded", value.config.CollapsedRoots)
	}
	if backend.openedTab != tab.ID {
		t.Fatalf("opened tab = %q, want bravo's waiting terminal", backend.openedTab)
	}
	if item, ok := value.navigationItem(); !ok || item.workspace.Path != collapseBravo.Path {
		t.Fatalf("cursor = %+v, want it on bravo", item)
	}
}

// The terminal reopened at startup does not need the cursor on its workspace,
// so a collapsed root stays collapsed and the cursor waits on the root.
func TestRestoringATerminalInsideACollapsedRootKeepsTheRootCollapsed(t *testing.T) {
	tab := model.Tab{ID: "tab-1", WorkspaceID: collapseBravo.ID, Name: "1", Running: true}
	value := newDashboardWithConfig(&fakeBackend{}, collapseSnapshot(tab), "", Config{
		CollapsedRoots:    []string{projectsRoot.Path},
		LastWorkspacePath: collapseBravo.Path,
		LastTabID:         tab.ID,
	})
	if !value.restorePending || value.selectedPath != collapseBravo.Path || value.tabIndex != 0 {
		t.Fatalf("restore = (pending %v, selected %q, tab %d)", value.restorePending, value.selectedPath, value.tabIndex)
	}
	if value.cursorPath != projectsRoot.Path || len(value.config.CollapsedRoots) != 1 {
		t.Fatalf("restore = (cursor %q, collapsed %q), want the cursor on the folded root",
			value.cursorPath, value.config.CollapsedRoots)
	}
}
