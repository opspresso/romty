package ui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/model"
)

func TestDashboardRunsGitActionForContextWorkspace(t *testing.T) {
	alpha := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	bravo := model.Workspace{ID: "workspace-2", RootID: "root-1", Name: "bravo", Path: "/projects/bravo"}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{
			{Workspace: alpha},
			{Workspace: bravo},
		},
	}}}

	for _, probe := range []struct {
		name       string
		prepare    func(*dashboard)
		wantTarget model.Workspace
	}{
		{
			name: "workspace cursor",
			prepare: func(value *dashboard) {
				value.setNavigation(2)
			},
			wantTarget: bravo,
		},
		{
			name: "open terminal",
			prepare: func(value *dashboard) {
				value.focus = terminalPane
				value.selectedWorkspaceID = alpha.ID
				value.selectedPath = alpha.Path
				value.terminal = newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)
			},
			wantTarget: alpha,
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			value := newDashboard(&fakeBackend{}, snapshot)
			value.gitStates = map[string]gitState{
				alpha.Path: {Branch: "main"},
				bravo.Path: {Branch: "feature"},
			}
			probe.prepare(&value)

			updated, command := value.Update(gitActionsKey())
			value = updated.(dashboard)
			if command != nil || value.modal != gitActionsModal || value.gitActionTarget.Path != probe.wantTarget.Path {
				t.Fatalf("Ctrl+Shift+G = (command %v, modal %v, target %q), want Git actions for %q",
					command, value.modal, value.gitActionTarget.Path, probe.wantTarget.Path)
			}

			updated, _ = value.Update(key(tea.KeyDown, ""))
			value = updated.(dashboard)
			if value.gitActionIndex != 1 {
				t.Fatalf("Git action index = %d, want Fetch at 1", value.gitActionIndex)
			}
		})
	}
}

func TestDashboardRunsSelectedGitActionAndShowsResult(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace}},
	}}}
	value := newDashboard(&fakeBackend{}, snapshot)
	value.gitStates = map[string]gitState{workspace.Path: {Branch: "main"}}
	value.setNavigation(1)

	updated, _ := value.Update(gitActionsKey())
	value = updated.(dashboard)
	updated, _ = value.Update(key(tea.KeyDown, ""))
	value = updated.(dashboard)
	updated, command := value.Update(key(tea.KeyEnter, ""))
	value = updated.(dashboard)
	if command == nil || !value.gitActionPending || value.gitAction != gitFetchAction {
		t.Fatalf("Fetch selection = (command %v, pending %t, action %v)", command, value.gitActionPending, value.gitAction)
	}

	updated, refresh := value.Update(gitActionMsg{
		path:   workspace.Path,
		action: gitFetchAction,
		output: "Fetched origin",
	})
	value = updated.(dashboard)
	if refresh == nil || value.gitActionPending || value.gitActionOutput != "Fetched origin" {
		t.Fatalf("Fetch result = (refresh %v, pending %t, output %q)", refresh, value.gitActionPending, value.gitActionOutput)
	}
	plain := ansi.Strip(strings.Join(value.renderModal(100, 30), "\n"))
	if !strings.Contains(plain, "Fetch") || !strings.Contains(plain, "Fetched origin") {
		t.Fatalf("Git result modal does not show action and output:\n%s", plain)
	}
}

func TestDashboardReportsGitActionFailure(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.modal = gitActionsModal
	value.gitActionPending = true
	value.gitActionTarget = model.Workspace{Path: "/projects/alpha"}
	value.gitAction = gitPushAction

	updated, refresh := value.Update(gitActionMsg{
		path:   "/projects/alpha",
		action: gitPushAction,
		output: "rejected",
		err:    errors.New("exit status 1"),
	})
	value = updated.(dashboard)
	if refresh == nil || value.gitActionPending || value.gitActionError == "" {
		t.Fatalf("Push failure = (refresh %v, pending %t, error %q)", refresh, value.gitActionPending, value.gitActionError)
	}
	plain := ansi.Strip(strings.Join(value.renderModal(100, 30), "\n"))
	if !strings.Contains(plain, "rejected") || !strings.Contains(plain, "exit status 1") {
		t.Fatalf("Git failure modal does not show command output and error:\n%s", plain)
	}
}

func TestDashboardScrollsGitActionResult(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 100
	value.height = 10
	value.modal = gitActionsModal
	value.gitActionComplete = true
	value.gitActionTarget = model.Workspace{Name: "alpha", Path: "/projects/alpha"}
	value.gitActionOutput = strings.Join([]string{"one", "two", "three", "four", "five", "six", "seven"}, "\n")

	before := ansi.Strip(strings.Join(value.renderModal(value.width, value.dimensions().bodyHeight), "\n"))
	updated, _ := value.Update(key(tea.KeyEnd, ""))
	value = updated.(dashboard)
	after := ansi.Strip(strings.Join(value.renderModal(value.width, value.dimensions().bodyHeight), "\n"))
	if value.gitActionOffset == 0 || strings.Contains(before, "seven") || !strings.Contains(after, "seven") {
		t.Fatalf("Git result scroll = (offset %d)\nbefore:\n%s\nafter:\n%s", value.gitActionOffset, before, after)
	}
}

func TestDashboardHoldsGitActionModalWhileCommandRuns(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.modal = gitActionsModal
	value.gitActionPending = true
	value.gitAction = gitPullAction
	cancelled := false
	value.gitActionCancel = func() { cancelled = true }

	for _, message := range []tea.KeyPressMsg{key(tea.KeyEscape, ""), key(tea.KeyF1, "")} {
		updated, command := value.Update(message)
		value = updated.(dashboard)
		if command != nil || value.modal != gitActionsModal || value.result.Quit || cancelled {
			t.Fatalf("%q during Git action = (command %v, modal %v, quit %t, cancelled %t)",
				message.String(), command, value.modal, value.result.Quit, cancelled)
		}
	}

	updated, command := value.Update(key(tea.KeyF4, ""))
	value = updated.(dashboard)
	if command == nil || !value.result.Quit || !cancelled {
		t.Fatalf("F4 during Git action = (command %v, quit %t, cancelled %t)", command, value.result.Quit, cancelled)
	}
}

func TestDashboardDoesNotOpenGitActionsOutsideRepository(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace}},
	}}}
	value := newDashboard(&fakeBackend{}, snapshot)
	value.setNavigation(1)

	updated, command := value.Update(gitActionsKey())
	value = updated.(dashboard)
	if command != nil || value.modal != noModal || !strings.Contains(value.errorMessage, "not a Git repository") {
		t.Fatalf("non-repository Git actions = (command %v, modal %v, error %q)", command, value.modal, value.errorMessage)
	}
}

func TestDashboardRecognisesCtrlShiftGAcrossInputLayouts(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace}},
	}}}
	value := newDashboard(&fakeBackend{}, snapshot)
	value.gitStates = map[string]gitState{workspace.Path: {Branch: "main"}}
	value.setNavigation(1)
	physicalG := tea.KeyPressMsg(tea.Key{
		Code: 'ㅎ', BaseCode: 'g', Mod: tea.ModCtrl | tea.ModShift,
	})

	updated, command := value.Update(physicalG)
	value = updated.(dashboard)
	if command != nil || value.modal != gitActionsModal {
		t.Fatalf("Ctrl+Shift+G with a Korean layout = (command %v, modal %v)", command, value.modal)
	}
}

func TestGitActionArguments(t *testing.T) {
	probes := []struct {
		action gitAction
		want   []string
	}{
		{action: gitStatusAction, want: []string{"status", "--short", "--branch"}},
		{action: gitFetchAction, want: []string{"fetch"}},
		{action: gitPullAction, want: []string{"pull", "--ff-only"}},
		{action: gitPushAction, want: []string{"push"}},
	}
	for _, probe := range probes {
		arguments, err := probe.action.arguments()
		if err != nil || !reflect.DeepEqual(arguments, probe.want) {
			t.Fatalf("%s arguments = (%v, %v), want %v", probe.action.label(), arguments, err, probe.want)
		}
	}
	if _, err := gitAction(99).arguments(); err == nil {
		t.Fatal("unknown Git action did not fail")
	}
}

func TestExecuteGitStatusAction(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repository := t.TempDir()
	command := exec.CommandContext(t.Context(), "git", "init", "--initial-branch=main", repository)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("initialize Git repository: %v\n%s", err, output)
	}
	if err := os.WriteFile(filepath.Join(repository, "untracked.txt"), []byte("new"), 0o600); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	output, err := executeGitAction(repository, gitStatusAction)
	if err != nil || !strings.Contains(output, "## No commits yet on main") || !strings.Contains(output, "?? untracked.txt") {
		t.Fatalf("Git status = (%q, %v), want branch and untracked file", output, err)
	}
}

func gitActionsKey() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: 'g', ShiftedCode: 'G', Mod: tea.ModCtrl | tea.ModShift})
}

// The Git action list is where Push and Pull live, so its cursor is the same
// highlighted bar the root picker draws rather than a bold row behind a chevron.
func TestDashboardHighlightsTheSelectedGitAction(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.modal = gitActionsModal
	value.gitActionTarget = model.Workspace{Name: "alpha", Path: "/projects/alpha"}
	value.gitActionIndex = 1

	rendered := strings.Join(value.renderModal(100, 30), "\n")
	selected := value.styles.navigationSelected.Render(pad("▌ "+pad("Fetch", 8)+"Update remote refs", 66))
	if !strings.Contains(rendered, selected) {
		t.Fatalf("the selected Git action does not carry the picker's bar:\n%s", rendered)
	}
	unselected := value.styles.modalBody.Render(pad("  "+pad("Status", 8)+"Show changed files", 66))
	if !strings.Contains(rendered, unselected) {
		t.Fatalf("an unselected Git action is not drawn as a plain row:\n%s", rendered)
	}
}
