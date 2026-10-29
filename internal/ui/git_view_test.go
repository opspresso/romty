package ui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/opspresso/romty/internal/gitrepo"
	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/testutil"
)

// The file view has two panes of its own, so the narrow layout does not apply
// to it — and the terminal it would widen is not on screen, so opening the view
// must not resize the PTY and make a full-screen guest reflow for a split it
// never appears in.
func TestDashboardKeepsTheFileViewSplitOnANarrowScreen(t *testing.T) {
	value := narrowDashboard(t, 60)
	before, _ := value.terminalSize()

	value.fileView = fileViewState{
		active:    true,
		target:    model.Workspace{Name: "alpha", Path: "/projects/alpha"},
		files:     []gitrepo.ChangedFile{{Path: "file.go", WorkTreeStatus: 'M'}},
		fileIndex: 0,
	}
	if view := value.fileViewLayout(); view.leftWidth == 0 || view.separator != separatorWidth {
		t.Fatalf("file view layout = (left %d, separator %d), want the split",
			view.leftWidth, view.separator)
	}
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "file.go") {
		t.Fatalf("file view is not drawn:\n%s", rendered)
	}
	if after, _ := value.terminalSize(); after != before {
		t.Fatalf("PTY width under the file view = %d, want the %d it had at %d columns",
			after, before, value.width)
	}
}

func TestNavigationShowsGitStateOnSecondLine(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "SnowClash", Path: "/projects/SnowClash"}
	snapshot := projectsSnapshot([]model.WorkspaceView{{
		Workspace: workspace,
		Tabs:      []model.Tab{{ID: "tab-1", Running: true}},
	}})
	value := newDashboard(&fakeBackend{}, snapshot)
	value.gitStates = map[string]gitrepo.State{workspace.Path: {
		Branch: "main",
		Dirty:  true,
		Ahead:  1,
		Behind: 3,
	}}
	value.width = 120
	value.height = 40

	navigation := ansi.Strip(strings.Join(value.renderNavigation(28, 36), "\n"))
	lines := strings.Split(navigation, "\n")
	for index, line := range lines {
		if strings.Contains(line, workspace.Name) {
			if !strings.HasSuffix(strings.TrimRight(line, " "), "●") {
				t.Fatalf("workspace row = %q, want terminal marker", line)
			}
			if index+1 >= len(lines) || strings.TrimSpace(lines[index+1]) != "(main*) ↑1 ↓3" {
				t.Fatalf("Git row after %q = %q, want branch and status", line, lines[index+1])
			}
			return
		}
	}
	t.Fatalf("navigation does not contain workspace %q:\n%s", workspace.Name, navigation)
}

func TestGitStatusUpdatesStateAndSchedulesAnotherRefresh(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.gitStates = map[string]gitrepo.State{"/old": {Behind: 2}}

	command := send(&value, gitStatusMsg{value: map[string]gitrepo.State{"/new": {Behind: 1}}, reschedule: true})
	if !reflect.DeepEqual(value.gitStates, map[string]gitrepo.State{"/new": {Behind: 1}}) {
		t.Fatalf("Git status = %#v, want only the current result", value.gitStates)
	}
	if command == nil {
		t.Fatal("Git status did not schedule another refresh")
	}
}

// A root the user just added has workspaces the periodic read has never seen,
// so their Git state and Git actions must not wait for its next tick.
func TestDashboardReadsGitStateForNewWorkspaces(t *testing.T) {
	repository := t.TempDir()
	testutil.RunGit(t, "init", "-q", "-b", "main", repository)
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	added := projectsSnapshot([]model.WorkspaceView{{
		Workspace: model.Workspace{ID: "w", RootID: "root-1", Name: "alpha", Path: repository},
	}})
	added.Revision = 1

	command := send(&value, snapshotMsg{value: added})
	status := commandMessage[gitStatusMsg](t, command)
	if state, ok := status.value[repository]; !ok || state.Branch != "main" {
		t.Fatalf("Git read = %v, want the new workspace on main", status.value)
	}
	if status.reschedule {
		t.Fatal("the extra read scheduled a second periodic loop")
	}

	// The same tree again is an ordinary refresh with nothing new to read.
	if command := send(&value, snapshotMsg{value: added}); command != nil {
		t.Fatalf("an unchanged tree produced %v, want no Git read", command)
	}
}

func TestGitStatusFetchesAtStartupAndAfterInterval(t *testing.T) {
	current := time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC)
	testutil.Set(t, &now, func() time.Time { return current })

	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	beforeInterval := value.readGitStatus(false, true)().(gitStatusMsg)
	if !beforeInterval.fetchedAt.IsZero() {
		t.Fatalf("Git fetch before interval = %v, want local status only", beforeInterval.fetchedAt)
	}

	current = current.Add(gitFetchInterval)
	afterInterval := value.readGitStatus(false, true)().(gitStatusMsg)
	if afterInterval.fetchedAt != current {
		t.Fatalf("Git fetch after interval = %v, want %v", afterInterval.fetchedAt, current)
	}
}

func TestInitialGitStatusSeparatesLocalReadFromFetch(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root-1", Path: "/projects"},
		Directories: []model.WorkspaceView{{
			Workspace: model.Workspace{ID: "workspace-1", RootID: "root-1", Path: "/projects/first"},
		}},
	}}})
	if paths := value.workspacePaths(); !reflect.DeepEqual(paths, []string{"/projects/first"}) {
		t.Fatalf("Git paths = %#v, want workspaces without the root", paths)
	}
	var local, remote int
	for _, message := range commandMessages(value.initialGitStatus()) {
		status, ok := message.(gitStatusMsg)
		if !ok {
			continue
		}
		if status.fetchedAt.IsZero() && status.reschedule {
			local++
		}
		if !status.fetchedAt.IsZero() && !status.reschedule {
			remote++
		}
	}
	if local != 1 || remote != 1 {
		t.Fatalf("initial Git reads = (local %d, remote %d), want one of each", local, remote)
	}
}
