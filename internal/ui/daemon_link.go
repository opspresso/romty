// The connection to the daemon: requests and the order of their answers,
// the stopped state and its recovery, and the daemon's report on itself.

package ui

import (
	"errors"

	tea "charm.land/bubbletea/v2"
	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/version"
)

// daemonStoppedMessage is what the status bar says while the daemon is gone.
const daemonStoppedMessage = "romty daemon stopped; press F5 to start it again"

func (m dashboard) shutdownDaemon() tea.Cmd {
	return func() tea.Msg {
		return daemonStoppedMsg{err: m.backend.Shutdown()}
	}
}

func (m *dashboard) refresh() tea.Cmd {
	return m.snapshotCommand(Backend.Snapshot)
}

func (m dashboard) refreshAll() (tea.Model, tea.Cmd) {
	if m.daemonDown && m.restartDaemon != nil {
		if m.daemonRestarting {
			return m, nil
		}
		m.daemonRestarting = true
		restart := m.restartDaemon
		return m, func() tea.Msg { return daemonRestartedMsg{err: restart()} }
	}
	tree := m.refresh()
	return m, tea.Batch(tree, m.readGitStatus(false, false), m.readGitStatus(true, false))
}

// daemonRestartedMsg is the answer to F5 starting a daemon that stopped.
type daemonRestartedMsg struct {
	err error
}

// daemonInfoMsg is the running daemon's report on itself.
type daemonInfoMsg struct {
	info model.DaemonInfo
	err  error
}

func (m dashboard) readDaemonInfo() tea.Cmd {
	backend := m.backend
	return func() tea.Msg {
		info, err := backend.DaemonInfo()
		return daemonInfoMsg{info: info, err: err}
	}
}

// handleDaemonInfo notes a daemon older than this romty. The status bar says
// so once, and About keeps saying it until the daemon restarts.
func (m dashboard) handleDaemonInfo(message daemonInfoMsg) (tea.Model, tea.Cmd) {
	if message.err != nil {
		m.noteDaemonError(message.err)
		return m, nil
	}
	m.daemonBuild = message.info.Version
	m.daemonOutdated = message.info.Version != version.String()
	if m.daemonOutdated && m.errorMessage == "" {
		m.setNotice(settingError, m.daemonUpdateNote())
	}
	return m, nil
}

// daemonUpdateNote says which daemon runs and how to bring it up to date.
func (m dashboard) daemonUpdateNote() string {
	return "the daemon is " + version.DaemonLabel(m.daemonBuild) + ", this romty is " + version.String() +
		"; " + version.DaemonRestartAdvice
}

// keepingAwake reports what the daemon does with the machine: it holds it
// awake while an agent is busy unless config.json allows sleep. Both read
// the same setting and the same agent phases, so the tree's mark matches.
func (m dashboard) keepingAwake() bool {
	if m.config.AllowSleep {
		return false
	}
	for tab := range m.state.Tabs() {
		if tab.Running && tab.AgentPhase.Busy() {
			return true
		}
	}
	return false
}

// noteDaemonError reports whether err says the daemon is gone, and if so
// puts the dashboard in the stopped state: one message that names the way
// back, in place of the socket error each request would otherwise show.
func (m *dashboard) noteDaemonError(err error) bool {
	if !errors.Is(err, model.ErrDaemonUnavailable) {
		return false
	}
	m.daemonDown = true
	m.setError(daemonError, daemonStoppedMessage)
	return true
}

// handleDaemonRestarted reloads everything from the new daemon. The shells
// that ran in the old one have ended; a stop saved them as closed sessions,
// which Enter on their workspace offers to reopen.
func (m dashboard) handleDaemonRestarted(message daemonRestartedMsg) (tea.Model, tea.Cmd) {
	m.daemonRestarting = false
	if message.err != nil {
		m.setError(daemonError, "romty daemon did not start: "+message.err.Error())
		return m, nil
	}
	m.daemonDown = false
	m.setNotice(daemonError, "romty daemon started again; Enter on a workspace reopens its saved sessions")
	updated, command := m.refreshAll()
	commands := []tea.Cmd{command, m.readDaemonInfo()}
	if m.events == nil && !m.polling {
		commands = append(commands, m.subscribe())
	}
	return updated, tea.Batch(commands...)
}

// requestPending reports a request a modal is waiting on: a daemon shutdown, a
// Git command, a hook install, or an input modal's submit. The modal stays on
// screen and takes no input until the answer arrives.
func (m dashboard) requestPending() bool {
	return m.shutdownPending || m.gitRun.pending || m.hookInstallPending || m.input.pending
}

func (m dashboard) hasPendingActivity() bool {
	return m.requestPending() || m.tabPending || m.tabClosePending != "" || m.restorePending ||
		m.modal == browseModal && m.browse.loading
}

func (m *dashboard) nextSnapshotOrder() uint64 {
	m.snapshotOrder++
	return m.snapshotOrder
}

// snapshotRequest prepares a daemon call that answers with a snapshot. The
// order is taken when the request is made, so answers apply in request order
// whatever order they arrive in.
func (m *dashboard) snapshotRequest(call func(Backend) (model.Snapshot, error)) func() snapshotMsg {
	order, backend := m.nextSnapshotOrder(), m.backend
	return func() snapshotMsg {
		value, err := call(backend)
		return snapshotMsg{value: value, order: order, err: err}
	}
}

func (m *dashboard) snapshotCommand(call func(Backend) (model.Snapshot, error)) tea.Cmd {
	request := m.snapshotRequest(call)
	return func() tea.Msg { return request() }
}

func (m *dashboard) applySnapshot(order uint64, snapshot model.Snapshot) bool {
	if order == 0 {
		order = m.snapshotOrder
	}
	if snapshot.Revision < m.state.Revision ||
		(snapshot.Revision == m.state.Revision && order < m.snapshotApplied) {
		return false
	}
	m.state = snapshot
	m.snapshotApplied = order
	m.agentAnimationActive = m.hasAnimatedAgent()
	return true
}
