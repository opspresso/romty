package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/opspresso/romty/internal/gitrepo"
	"github.com/opspresso/romty/internal/model"
)

// Test conveniences over the production entry points.

func fileTreeRowsOf(files []gitrepo.ChangedFile) []fileTreeRow {
	return buildFileViewTreeRows(files, false)
}

func renderTabBar(styles *uiStyles, tabs []model.Tab, active, width int) []string {
	return renderTabBarWithHover(styles, tabs, active, -1, -1, 0, width)
}

func newDashboard(backend Backend, initial model.Snapshot) dashboard {
	return newDashboardWithConfig(backend, initial, "", Config{})
}

// send delivers message the way the program does: *value becomes the updated
// dashboard, and the command it scheduled is returned.
func send(value *dashboard, message tea.Msg) tea.Cmd {
	updated, command := value.Update(message)
	*value = updated.(dashboard)
	return command
}

// projectsRoot is the root the dashboard fixtures open.
var projectsRoot = model.Root{ID: "root-1", Name: "projects", Path: "/projects"}

// projectsSnapshot is a snapshot of projectsRoot holding directories.
func projectsSnapshot(directories []model.WorkspaceView) model.Snapshot {
	return model.Snapshot{Roots: []model.RootView{{Root: projectsRoot, Directories: directories}}}
}

// repositoryStates gives every directory a Git state, so each workspace takes
// the two rows a repository does.
func repositoryStates(directories []model.WorkspaceView) map[string]gitrepo.State {
	states := make(map[string]gitrepo.State, len(directories))
	for _, directory := range directories {
		states[directory.Workspace.Path] = gitrepo.State{Branch: "main"}
	}
	return states
}
