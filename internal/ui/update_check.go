// Release check: whether a newer romty has been published, which the
// workspace pane title marks with an asterisk.

package ui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/opspresso/romty/internal/update"
	"github.com/opspresso/romty/internal/version"
)

const (
	// releaseCheckInterval spaces the checks for a dashboard left open for
	// days. GitHub allows 60 unauthenticated API requests an hour per address.
	releaseCheckInterval = 12 * time.Hour
	releaseCheckTimeout  = 10 * time.Second
)

// releaseCheckEnabled limits the check to release builds. A development build
// has no release to compare, and tests must not reach the network.
var releaseCheckEnabled = version.IsRelease

var currentVersion = version.String

var latestRelease = update.LatestRelease

type releaseCheckedMsg struct {
	latest string
	err    error
}

type releaseCheckDueMsg struct{}

func checkRelease() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), releaseCheckTimeout)
		defer cancel()
		latest, err := latestRelease(ctx)
		return releaseCheckedMsg{latest: latest, err: err}
	}
}

func scheduleReleaseCheck() tea.Cmd {
	return tea.Tick(releaseCheckInterval, func(time.Time) tea.Msg { return releaseCheckDueMsg{} })
}

// handleReleaseCheck keeps the last known answer when a check fails. Being
// offline or rate limited says nothing about which release is newest, and the
// status bar is for failures the user has to act on.
func (m dashboard) handleReleaseCheck(message releaseCheckedMsg) (tea.Model, tea.Cmd) {
	if message.err == nil {
		m.newerRelease = ""
		if update.Newer(currentVersion(), message.latest) {
			m.newerRelease = message.latest
		}
	}
	return m, scheduleReleaseCheck()
}
