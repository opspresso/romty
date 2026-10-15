package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/gitrepo"
	"github.com/opspresso/romty/internal/model"
)

// alphaWorkspaceSnapshot holds one workspace, alpha, with tabs.
func alphaWorkspaceSnapshot(tabs []model.Tab) (model.Snapshot, model.Workspace) {
	workspace := model.Workspace{
		ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha",
	}
	return projectsSnapshot([]model.WorkspaceView{{Workspace: workspace, Tabs: tabs}}), workspace
}

func workspaceActionIndexFor(t *testing.T, value dashboard, action workspaceAction) int {
	t.Helper()
	for index, choice := range value.workspaceActionChoices() {
		if choice.action == action {
			return index
		}
	}
	t.Fatalf("workspace action %d is missing", action)
	return 0
}

// runWorkspaceAction opens the actions for the selected workspace with F8 and
// runs action with Enter.
func runWorkspaceAction(t *testing.T, value dashboard, action workspaceAction) (dashboard, tea.Cmd) {
	t.Helper()
	send(&value, key(tea.KeyF8, ""))
	value.palette.index = workspaceActionIndexFor(t, value, action)
	command := send(&value, key(tea.KeyEnter, ""))
	return value, command
}

func TestWorkspaceActionsStartWithThePrimaryTerminalAction(t *testing.T) {
	running := openTabs(1)
	for _, probe := range []struct {
		name    string
		tabs    []model.Tab
		git     gitrepo.State
		hasGit  bool
		failure string
		want    workspaceAction
	}{
		{name: "running terminal", tabs: running, git: gitrepo.State{Branch: "main", Dirty: true}, hasGit: true, want: workspaceOpenTerminalAction},
		{name: "new terminal", want: workspaceNewTabAction},
		{name: "open terminal", tabs: running, want: workspaceOpenTerminalAction},
		{name: "unreadable root", failure: "not found", want: workspaceRemoveAction},
	} {
		t.Run(probe.name, func(t *testing.T) {
			snapshot, workspace := alphaWorkspaceSnapshot(probe.tabs)
			value := newDashboard(&fakeBackend{}, snapshot)
			value.setNavigation(1)
			target, _ := value.navigationItem()
			target.git, target.hasGit, target.failure = probe.git, probe.hasGit, probe.failure
			value.palette.target = target
			choices := value.workspaceActionChoices()
			if len(choices) == 0 || choices[0].action != probe.want {
				t.Fatalf("first action = %+v, want action %d for %s", choices, probe.want, workspace.Path)
			}
		})
	}
}

func TestWorkspaceActionsExposeEveryGitOperationDirectly(t *testing.T) {
	snapshot, workspace := alphaWorkspaceSnapshot(nil)
	value := newDashboard(&fakeBackend{}, snapshot)
	value.gitStates = map[string]gitrepo.State{workspace.Path: {Branch: "main", Dirty: true}}
	value.setNavigation(1)
	command := send(&value, key(tea.KeyF8, ""))
	if command != nil || value.modal != workspaceActionsModal || value.palette.target.workspace.Path != workspace.Path {
		t.Fatalf("F8 = (command %v, modal %v, target %q)", command, value.modal, value.palette.target.workspace.Path)
	}

	popup, _, _ := value.workspaceActionPopup(100, 30)
	plain := ansi.Strip(strings.Join(popup, "\n"))
	for _, label := range []string{"All files", "File changes", "Git status", "Git fetch", "Git pull", "Git push", "Delete workspace"} {
		if !strings.Contains(plain, label) {
			t.Fatalf("workspace actions do not contain %q:\n%s", label, plain)
		}
	}
	dividers := 0
	for _, line := range strings.Split(plain, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "│─") {
			dividers++
		}
	}
	if dividers != 2 {
		t.Fatalf("workspace action groups have no separators:\n%s", plain)
	}
}

func TestWorkspaceActionsRunAGitOperationAndReturnToThePalette(t *testing.T) {
	snapshot, workspace := alphaWorkspaceSnapshot(nil)
	value := newDashboard(&fakeBackend{}, snapshot)
	value.gitStates = map[string]gitrepo.State{workspace.Path: {Branch: "main"}}
	value.setNavigation(1)
	value, command := runWorkspaceAction(t, value, workspaceGitPullAction)
	if command == nil || value.modal != gitActionsModal || !value.gitRun.pending || value.gitRun.action != gitPullAction ||
		value.gitRun.target.Path != workspace.Path {
		t.Fatalf("Git pull = (command %v, modal %v, pending %v, action %v, target %q)",
			command, value.modal, value.gitRun.pending, value.gitRun.action, value.gitRun.target.Path)
	}
	value.cancelGitAction()
	value.gitRun.pending = false
	value.gitRun.complete = true
	value.gitRun.output = "Already up to date."
	send(&value, key(tea.KeyEnter, ""))
	if value.modal != workspaceActionsModal || value.gitRun.complete {
		t.Fatalf("Git result returned to modal %v with complete=%v, want workspace actions", value.modal, value.gitRun.complete)
	}
}

func TestWorkspaceActionsRunTerminalAndFileCommands(t *testing.T) {
	t.Run("new tab", func(t *testing.T) {
		snapshot, _ := alphaWorkspaceSnapshot(nil)
		value := newDashboard(&fakeBackend{}, snapshot)
		value.setNavigation(1)
		value, command := runWorkspaceAction(t, value, workspaceNewTabAction)
		if command == nil || value.modal != noModal || value.tabIndex != 0 {
			t.Fatalf("new tab = (command %v, modal %v, tab %d)", command, value.modal, value.tabIndex)
		}
	})

	t.Run("open terminal", func(t *testing.T) {
		tabs := openTabs(1)
		snapshot, _ := alphaWorkspaceSnapshot(tabs)
		value := newDashboard(&fakeBackend{}, snapshot)
		value.setNavigation(1)
		value, command := runWorkspaceAction(t, value, workspaceOpenTerminalAction)
		if command == nil || value.modal != noModal || value.tabIndex != 0 {
			t.Fatalf("open terminal = (command %v, modal %v, tab %d)", command, value.modal, value.tabIndex)
		}
	})

	t.Run("close tab", func(t *testing.T) {
		tabs := openTabs(2)
		snapshot, _ := alphaWorkspaceSnapshot(tabs)
		value := newDashboard(&fakeBackend{}, snapshot)
		value.setNavigation(1)
		value.tabIndex = 1
		value, command := runWorkspaceAction(t, value, workspaceCloseTabAction)
		if command != nil || value.modal != closeTabModal || value.closeTabTarget.ID != tabs[1].ID {
			t.Fatalf("close tab = (command %v, modal %v, target %q)", command, value.modal, value.closeTabTarget.ID)
		}
	})

	t.Run("no tab to close", func(t *testing.T) {
		snapshot, _ := alphaWorkspaceSnapshot(nil)
		value := newDashboard(&fakeBackend{}, snapshot)
		value.setNavigation(1)
		send(&value, key(tea.KeyF8, ""))
		for _, choice := range value.workspaceActionChoices() {
			if choice.action == workspaceCloseTabAction {
				t.Fatalf("actions = %+v, want no close entry without a running terminal",
					value.workspaceActionChoices())
			}
		}
	})

	t.Run("file changes", func(t *testing.T) {
		snapshot, workspace := alphaWorkspaceSnapshot(nil)
		value := newDashboard(&fakeBackend{}, snapshot)
		value.gitStates = map[string]gitrepo.State{workspace.Path: {Branch: "main", Dirty: true}}
		value.setNavigation(1)
		value, command := runWorkspaceAction(t, value, workspaceFileChangesAction)
		if command == nil || value.modal != noModal || !value.gitDiff.active || value.gitDiff.target.Path != workspace.Path {
			t.Fatalf("file changes = (command %v, modal %v, active %v, target %q)",
				command, value.modal, value.gitDiff.active, value.gitDiff.target.Path)
		}
	})

	t.Run("all files", func(t *testing.T) {
		snapshot, workspace := alphaWorkspaceSnapshot(nil)
		value := newDashboard(&fakeBackend{}, snapshot)
		value.setNavigation(1)
		value, command := runWorkspaceAction(t, value, workspaceAllFilesAction)
		if command == nil || value.modal != noModal || !value.gitDiff.active ||
			value.gitDiff.mode != allFilesView || value.gitDiff.target.Path != workspace.Path {
			t.Fatalf("all files = (command %v, modal %v, state %#v)", command, value.modal, value.gitDiff)
		}
	})
}

func TestWorkspaceActionsOpenRemovalConfirmationForCapturedTarget(t *testing.T) {
	snapshot, workspace := alphaWorkspaceSnapshot(nil)
	value := newDashboard(&fakeBackend{}, snapshot)
	value.setNavigation(1)
	value, command := runWorkspaceAction(t, value, workspaceRemoveAction)
	if command != nil || value.modal != removeSelectionModal || value.removeTarget.workspace.Path != workspace.Path {
		t.Fatalf("remove action = (command %v, modal %v, target %q)", command, value.modal, value.removeTarget.workspace.Path)
	}
}

func TestDashboardRightClickOpensActionsForThePointedWorkspace(t *testing.T) {
	snapshot, workspace := alphaWorkspaceSnapshot(nil)
	value := newDashboard(&fakeBackend{}, snapshot)
	value.width, value.height = 120, 30

	command := send(&value, tea.MouseClickMsg{X: 2, Y: 3, Button: tea.MouseRight})
	if command != nil || value.modal != workspaceActionsModal || value.navIndex != 1 ||
		value.palette.target.workspace.Path != workspace.Path {
		t.Fatalf("right click = (command %v, modal %v, index %d, target %q)",
			command, value.modal, value.navIndex, value.palette.target.workspace.Path)
	}

	popup, left, top := value.workspaceActionPopup(value.width, value.dimensions().bodyHeight)
	if left != 2 || top != 3 || len(popup) == 0 {
		t.Fatalf("popup anchor = (%d, %d), want the click at (2, 3)", left, top)
	}
	send(&value, tea.MouseMotionMsg{X: left + 1, Y: top + 1})
	if value.hover.kind != hoverWorkspaceAction || value.hover.index != 0 {
		t.Fatalf("workspace action hover = %#v, want the first row", value.hover)
	}
	command = send(&value, tea.MouseClickMsg{
		X: left + 1, Y: top + 1, Button: tea.MouseLeft,
	})
	if command == nil || value.modal != noModal {
		t.Fatalf("first action click = (command %v, modal %v), want new tab", command, value.modal)
	}
}

func TestWorkspaceActionsWindowACompactScreen(t *testing.T) {
	tabs := openTabs(1)
	snapshot, workspace := alphaWorkspaceSnapshot(tabs)
	value := newDashboard(&fakeBackend{}, snapshot)
	value.width, value.height = 80, 10
	value.gitStates = map[string]gitrepo.State{workspace.Path: {Branch: "main"}}
	value.setNavigation(1)
	send(&value, key(tea.KeyF8, ""))

	send(&value, key(tea.KeyEnd, ""))
	popup, _, _ := value.workspaceActionPopup(value.width, value.dimensions().bodyHeight)
	rendered := ansi.Strip(strings.Join(popup, "\n"))
	if value.palette.offset == 0 || !strings.Contains(rendered, "Delete workspace") ||
		len(popup) > value.dimensions().bodyHeight {
		t.Fatalf("compact workspace actions = (offset %d):\n%s", value.palette.offset, rendered)
	}
}

func TestWorkspaceActionsOverlayOnlyItsPopupRectangle(t *testing.T) {
	snapshot, _ := alphaWorkspaceSnapshot(nil)
	value := newDashboard(&fakeBackend{}, snapshot)
	value.width, value.height = 120, 30
	value.setNavigation(1)
	send(&value, tea.MouseClickMsg{X: 2, Y: 3, Button: tea.MouseRight})

	rendered := strings.Split(ansi.Strip(value.render()), "\n")
	if !strings.Contains(rendered[0], "romty") || !strings.Contains(strings.Join(rendered, "\n"), "Select +") {
		t.Fatalf("context popup hid the dashboard behind it:\n%s", strings.Join(rendered, "\n"))
	}
	if !strings.Contains(rendered[3], "╭") || !strings.Contains(rendered[4], "New tab") {
		t.Fatalf("context popup is not anchored at the click:\n%s", strings.Join(rendered, "\n"))
	}
	updated, _ := value.Update(tea.MouseClickMsg{X: 100, Y: 2, Button: tea.MouseLeft})
	if updated.(dashboard).modal != noModal {
		t.Fatal("clicking outside the context popup did not dismiss it")
	}
}

func TestWorkspaceActionsClampThePopupToTheScreen(t *testing.T) {
	snapshot, _ := alphaWorkspaceSnapshot(nil)
	value := newDashboard(&fakeBackend{}, snapshot)
	value.width, value.height = 80, 10
	value.setNavigation(1)
	target, _ := value.navigationItem()
	updated, _ := value.openWorkspaceActionsAt(target, 79, value.dimensions().bodyHeight-1)
	value = updated.(dashboard)

	popup, x, y := value.workspaceActionPopup(value.width, value.dimensions().bodyHeight)
	if len(popup) == 0 || x < 0 || y < 0 || x+ansi.StringWidth(popup[0]) > value.width ||
		y+len(popup) > value.dimensions().bodyHeight {
		t.Fatalf("clamped popup = (x %d, y %d, width %d, height %d) in %dx%d",
			x, y, ansi.StringWidth(popup[0]), len(popup), value.width, value.dimensions().bodyHeight)
	}
}
