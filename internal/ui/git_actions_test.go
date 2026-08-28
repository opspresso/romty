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
	"charm.land/lipgloss/v2"
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

func TestDashboardClicksAndScrollsGitActions(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 100, 12
	value.modal = gitActionsModal
	value.gitActionTarget = model.Workspace{Name: "alpha", Path: "/projects/alpha"}
	left, top := value.modalGeometry(value.width, value.dimensions().bodyHeight).contentOrigin()

	updated, command := value.Update(tea.MouseClickMsg{X: left + 2, Y: top + 4, Button: tea.MouseLeft})
	value = updated.(dashboard)
	if value.gitActionIndex != 2 || !value.gitActionPending || command == nil {
		t.Fatalf("Git action click = (index %d, pending %v, command %v)",
			value.gitActionIndex, value.gitActionPending, command)
	}
	value.cancelGitAction()
	value.gitActionPending = false
	value.gitActionComplete = true
	value.gitActionOutput = strings.Join([]string{"one", "two", "three", "four", "five", "six", "seven"}, "\n")
	updated, _ = value.Update(tea.MouseWheelMsg{X: left + 2, Y: top + 3, Button: tea.MouseWheelDown})
	value = updated.(dashboard)
	if value.gitActionOffset == 0 {
		t.Fatal("Git result wheel did not scroll")
	}
}

func TestDashboardHighlightsHoveredGitActionAndResult(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 100, 12
	value.modal = gitActionsModal
	value.gitActionTarget = model.Workspace{Name: "alpha", Path: "/projects/alpha"}
	left, top := value.modalGeometry(value.width, value.dimensions().bodyHeight).contentOrigin()
	before := value.render()

	updated, _ := value.Update(tea.MouseMotionMsg{X: left + 2, Y: top + 4})
	value = updated.(dashboard)
	if value.hover.kind != hoverGitAction || value.hover.index != 2 || value.render() == before {
		t.Fatalf("Git action hover = %#v", value.hover)
	}

	value.gitActionComplete = true
	value.gitActionOutput = "one\ntwo\nthree\nfour\nfive\nsix"
	left, top = value.modalGeometry(value.width, value.dimensions().bodyHeight).contentOrigin()
	before = value.render()
	updated, _ = value.Update(tea.MouseMotionMsg{X: left + 2, Y: top + 3})
	value = updated.(dashboard)
	if value.hover.kind != hoverGitResult || value.hover.index != 3 || value.render() == before {
		t.Fatalf("Git result hover = %#v", value.hover)
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
		if value.modal != gitActionsModal || value.result.Quit || cancelled {
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

	lines := value.renderModal(100, 30)
	rendered := strings.Join(lines, "\n")
	// The box fits its content, so the row width is read back from what was
	// drawn rather than pinned to the old fixed width.
	width := lipgloss.Width(lines[0]) - 6
	selected := value.styles.navigationSelected.Render(
		pad("▌ "+pad("Fetch", gitActionLabelWidth)+"Update remote refs", width))
	if !strings.Contains(rendered, selected) {
		t.Fatalf("the selected Git action does not carry the picker's bar:\n%s", rendered)
	}
	unselected := value.styles.modalBody.Render(
		pad("  "+pad("Status", gitActionLabelWidth)+"Show changed files", width))
	if !strings.Contains(rendered, unselected) {
		t.Fatalf("an unselected Git action is not drawn as a plain row:\n%s", rendered)
	}
	// The box is only as wide as the rows need, not as wide as the cap.
	if boxWidth := lipgloss.Width(lines[0]); boxWidth >= 72 {
		t.Fatalf("Git actions box width = %d, want it fitted to its rows", boxWidth)
	}
}

// The result of a Git action is a block of text the eye has to find structure
// in. Status codes and a diffstat's + and - are that structure, and drawn flat
// they had to be read rather than glanced at.
func TestGitActionResultIsColoured(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 100, 24
	value.modal = gitActionsModal
	value.gitActionTarget = model.Workspace{Name: "alpha", Path: "/projects/alpha"}
	value.gitActionComplete = true

	for _, probe := range []struct {
		name    string
		line    string
		segment string
		style   func(*uiStyles) lipgloss.Style
	}{
		{
			name: "a branch header", line: "## main...origin/main",
			segment: "## main...origin/main",
			style:   func(s *uiStyles) lipgloss.Style { return s.gitBranch },
		},
		{
			name: "an untracked file", line: "?? new.go",
			segment: "??",
			style:   func(s *uiStyles) lipgloss.Style { return s.diffAdded },
		},
		{
			name: "a deleted file", line: " D gone.go",
			segment: " D",
			style:   func(s *uiStyles) lipgloss.Style { return s.diffRemoved },
		},
		{
			name: "a modified file", line: " M main.go",
			segment: " M",
			style:   func(s *uiStyles) lipgloss.Style { return s.gitStatus },
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			value.gitActionOutput = probe.line
			rendered := strings.Join(value.renderModal(value.width, value.dimensions().bodyHeight), "\n")
			if want := probe.style(value.styles).Render(probe.segment); !strings.Contains(rendered, want) {
				t.Fatalf("%q is not drawn in its own colour:\n%s", probe.segment, rendered)
			}
		})
	}

	// A diffstat's marks are split so additions and removals do not share one
	// colour, which is the only thing the run actually says.
	value.gitActionOutput = " main.go | 3 ++-"
	rendered := strings.Join(value.renderModal(value.width, value.dimensions().bodyHeight), "\n")
	if !strings.Contains(rendered, value.styles.diffAdded.Render("+")) {
		t.Fatalf("diffstat additions are not green:\n%s", rendered)
	}
	if !strings.Contains(rendered, value.styles.diffRemoved.Render("-")) {
		t.Fatalf("diffstat removals are not red:\n%s", rendered)
	}
	// The path before the bar is not part of the run.
	if strings.Contains(rendered, value.styles.diffAdded.Render("main.go")) {
		t.Fatalf("the path was coloured as a diffstat mark:\n%s", rendered)
	}
}

func TestGitDiffstatMarksFindsOnlyTheRun(t *testing.T) {
	for _, probe := range []struct {
		line string
		want int
	}{
		{line: " main.go | 3 ++-", want: 13},
		{line: " main.go | 3 +++", want: 13},
		{line: " main.go | 0", want: -1},
		{line: " 1 file changed, 1 insertion(+)", want: -1},
		{line: "Fast-forward", want: -1},
		{line: "## main...origin/main", want: -1},
	} {
		if got := gitDiffstatMarks(probe.line); got != probe.want {
			t.Errorf("gitDiffstatMarks(%q) = %d, want %d", probe.line, got, probe.want)
		}
	}
}
