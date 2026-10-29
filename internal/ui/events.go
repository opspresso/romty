// The daemon's stream of changes: the tree, the agent statuses and the Git
// states arrive when they change instead of being polled, and one daemon reads
// them for every open TUI.

package ui

import (
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/opspresso/romty/internal/model"
)

// resubscribeDelay is how long a dashboard waits to subscribe again after the
// stream ended. It is how a daemon started from elsewhere is noticed.
var resubscribeDelay = 2 * time.Second

type subscribedMsg struct {
	stream model.EventStream
	err    error
}

type eventMsg struct {
	stream model.EventStream
	event  model.Event
	err    error
}

type resubscribeMsg struct{}

func (m dashboard) subscribe() tea.Cmd {
	backend := m.backend
	return func() tea.Msg {
		stream, err := backend.Subscribe()
		return subscribedMsg{stream: stream, err: err}
	}
}

func nextEvent(stream model.EventStream) tea.Cmd {
	return func() tea.Msg {
		event, err := stream.Next()
		return eventMsg{stream: stream, event: event, err: err}
	}
}

// handleSubscribed starts reading the stream, or falls back to polling a
// daemon that predates it. A daemon that is not there is noted, and the
// dashboard tries again.
func (m dashboard) handleSubscribed(message subscribedMsg) (tea.Model, tea.Cmd) {
	switch {
	case message.err == nil:
		m.events = message.stream
		if m.daemonDown {
			m.daemonDown = false
			m.clearError(daemonError)
		}
		return m, nextEvent(message.stream)
	case errors.Is(message.err, model.ErrNeedsNewerDaemon):
		if m.polling {
			return m, nil
		}
		m.polling = true
		return m, tea.Batch(m.refreshAgents(), m.initialGitStatus())
	default:
		m.noteDaemonError(message.err)
		return m, tea.Tick(resubscribeDelay, func(time.Time) tea.Msg { return resubscribeMsg{} })
	}
}

// handleEvent applies one event through the same paths a polled snapshot,
// agent report and Git read take, then waits for the next.
func (m dashboard) handleEvent(message eventMsg) (tea.Model, tea.Cmd) {
	if message.stream != m.events {
		// A stream this dashboard replaced.
		return m, nil
	}
	if message.err != nil {
		m.events = nil
		_ = message.stream.Close()
		m.noteDaemonError(message.err)
		return m, tea.Tick(resubscribeDelay, func(time.Time) tea.Msg { return resubscribeMsg{} })
	}
	commands := []tea.Cmd{nextEvent(message.stream)}
	var model tea.Model = m
	if message.event.Snapshot != nil {
		var command tea.Cmd
		model, command = m.update(snapshotMsg{value: *message.event.Snapshot})
		commands = append(commands, command)
	}
	value := model.(dashboard)
	if message.event.AgentStatuses != nil {
		commands = append(commands, value.applyAgentStatuses(message.event.AgentStatuses)...)
	}
	if message.event.GitStates != nil {
		var command tea.Cmd
		model, command = value.update(gitStatusMsg{value: message.event.GitStates})
		value = model.(dashboard)
		commands = append(commands, command)
	}
	return value, tea.Batch(commands...)
}
