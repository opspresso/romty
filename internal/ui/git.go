package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/opspresso/romty/internal/gitrepo"
)

func (m dashboard) readGitStatus(forceFetch, reschedule bool) tea.Cmd {
	paths := m.workspacePaths()
	fetch := forceFetch || m.gitFetchedAt.IsZero() || now().Sub(m.gitFetchedAt) >= gitFetchInterval
	fetchedAt := time.Time{}
	if fetch {
		fetchedAt = now()
	}
	return func() tea.Msg {
		return gitStatusMsg{value: gitrepo.States(paths, fetch), fetchedAt: fetchedAt, reschedule: reschedule}
	}
}

func (m dashboard) initialGitStatus() tea.Cmd {
	return tea.Batch(m.readGitStatus(false, true), m.readGitStatus(true, false))
}

func (m dashboard) refreshGitStatus() tea.Cmd {
	read := m.readGitStatus(false, true)
	return tea.Tick(gitRefreshInterval, func(time.Time) tea.Msg {
		return read()
	})
}

func (m dashboard) workspacePaths() []string {
	paths := make([]string, 0)
	for _, root := range m.state.Roots {
		for _, directory := range root.Directories {
			paths = append(paths, directory.Workspace.Path)
		}
	}
	return paths
}
