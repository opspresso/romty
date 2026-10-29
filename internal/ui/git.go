package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/opspresso/romty/internal/gitrepo"
)

func (m dashboard) readGitStatus(forceFetch, reschedule bool) tea.Cmd {
	if m.events != nil {
		// The daemon reads Git for every client; asking it is enough, and
		// the result arrives on the stream.
		backend := m.backend
		return func() tea.Msg {
			if err := backend.RefreshGit(forceFetch); err != nil {
				return gitRefreshFailedMsg{err: err}
			}
			return nil
		}
	}
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

// gitRefreshFailedMsg is a refresh the daemon would not take.
type gitRefreshFailedMsg struct {
	err error
}

// What the status bar says about a workspace that cannot answer the request
// made of it. Each is raised from more than one place, and a sentence a user
// reads twice should read the same both times.
const (
	notAGitRepository = "selected workspace is not a Git repository"
	noRunningTerminal = "selected workspace has no running terminal"
)
