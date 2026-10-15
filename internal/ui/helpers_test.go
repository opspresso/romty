package ui

import (
	"context"

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
