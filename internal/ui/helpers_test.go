package ui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/opspresso/romty/internal/model"
)

// Test conveniences over the production entry points.

func executeGitAction(path string, action gitAction) (string, error) {
	return executeGitActionContext(context.Background(), path, action)
}

func gitDiffTreeRows(files []gitChangedFile) []gitDiffTreeRow {
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
