// The offer to install agent status hooks, and what installing reports.

package ui

import (
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/opspresso/romty/internal/agenthooks"
)

var installHookProviders = agenthooks.Install

// offerDeferredHooks shows the agent hook offer a first run held back, once
// there is a root and nothing else is being asked. Checked after every
// message, so a root added from anywhere and a modal closed by any key both
// let it through.
func (m *dashboard) offerDeferredHooks() {
	if m.hooksDeferred && len(m.state.Roots) > 0 && m.modal == noModal && !m.fileView.active {
		m.hooksDeferred = false
		m.modal = hookInstallModal
	}
}

func (m *dashboard) offerAgentHooks(statuses []agenthooks.Status) {
	m.hookStatuses = statuses
	if len(agenthooks.Pending(statuses)) > 0 {
		if len(m.state.Roots) == 0 {
			// On a first run the screen explains how to add a root. Asking
			// about agent hooks before there is anywhere to run an agent
			// would put a question in front of that answer.
			m.hooksDeferred = true
			return
		}
		m.modal = hookInstallModal
		return
	}
	if err := hookStatusErrors(statuses); err != nil {
		m.setError(settingError, "check agent hooks: "+err.Error())
	}
}

func (m dashboard) installAgentHooks() tea.Cmd {
	providers := agenthooks.Pending(m.hookStatuses)
	statuses := append([]agenthooks.Status(nil), m.hookStatuses...)
	return func() tea.Msg {
		results, err := installHookProviders(providers)
		return hooksInstalledMsg{results: results, err: errors.Join(err, hookStatusErrors(statuses))}
	}
}

func hookStatusErrors(statuses []agenthooks.Status) error {
	var failures []error
	for _, status := range statuses {
		if status.State == agenthooks.StateInvalid {
			failures = append(failures, fmt.Errorf("%s: %w", status.Provider.DisplayName(), status.Err))
		}
	}
	return errors.Join(failures...)
}

func installedHooksNotice(results []agenthooks.Result) string {
	names := make([]string, 0, len(results))
	for _, result := range results {
		names = append(names, result.Provider.DisplayName())
	}
	list := strings.Join(names, " and ")
	if len(names) > 2 {
		list = strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
	return list + " hooks installed; restart running agent sessions"
}
