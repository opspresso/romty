// Terminal tabs: opening a workspace's terminal, creating, attaching,
// reattaching, closing and resizing tabs.

package ui

import (
	"fmt"
	"maps"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/opspresso/romty/internal/model"
)

// doubleClickInterval is the longest gap between two clicks on one tab that
// still counts as a double click.
const doubleClickInterval = 500 * time.Millisecond

type tabClick struct {
	tabID string
	at    time.Time
}

// isDoubleClick records a click on a tab label and reports whether it is the
// second of two quick clicks on the same tab. update clears lastTabClick on any
// other click, so a click elsewhere in between breaks the pair.
func (m *dashboard) isDoubleClick(tabID string) bool {
	at := now()
	previous := m.lastTabClick
	if previous.tabID == tabID && at.Sub(previous.at) <= doubleClickInterval {
		m.lastTabClick = tabClick{}
		return true
	}
	m.lastTabClick = tabClick{tabID: tabID, at: at}
	return false
}

func (m dashboard) handleWorkspace(message workspaceMsg) (tea.Model, tea.Cmd) {
	if message.selection != 0 && message.selection != m.selectionRequest {
		if message.err == nil {
			m.applySnapshot(message.order, message.snapshot)
		}
		return m, nil
	}
	if message.err != nil {
		m.setError(treeError, message.err.Error())
		return m, nil
	}
	m.applySnapshot(message.order, message.snapshot)
	m.selectedWorkspaceID = message.value.ID
	m.selectedPath = message.value.Path
	m.reattachTab, m.reattachAttempts = "", 0
	tabs := m.selectedTabs()
	m.focus = leftPane
	// The user's selection succeeded, so whatever was on the status bar is
	// answered whichever part of romty put it there.
	m.clearAnyError()
	if message.createTab {
		m.tabIndex = len(tabs)
		m.tabPending = true
		return m, m.createTabFrom(message.reopenID)
	}
	for index, tab := range tabs {
		if tab.ID == message.tabID {
			m.tabIndex = index
			return m, m.openSelectedTerminal()
		}
	}
	m.clampTabIndex()
	m.setError(terminalError, "selected terminal is no longer running")
	return m, nil
}

func (m dashboard) handleCreatedTab(message tabMsg) (tea.Model, tea.Cmd) {
	if message.selection != 0 && message.selection != m.selectionRequest {
		if message.err == nil {
			m.applySnapshot(message.order, message.snapshot)
		}
		return m, nil
	}
	m.tabPending = false
	if message.err != nil {
		if !m.noteDaemonError(message.err) {
			m.setError(terminalError, message.err.Error())
		}
		return m, nil
	}
	m.applySnapshot(message.order, message.snapshot)
	tabs := m.selectedTabs()
	for index, tab := range tabs {
		if tab.ID == message.value.ID {
			m.tabIndex = index
			break
		}
	}
	m.clearAnyError()
	return m, m.openSelectedTerminal()
}

func (m dashboard) handleOpenedTerminal(message terminalOpenedMsg) (tea.Model, tea.Cmd) {
	if message.tabID != m.selectedTabID() {
		// A second switch overtook the first. Opening a terminal is a round
		// trip to the daemon, so two quick presses of Ctrl+Shift+Right leave
		// two of them in flight, and nothing orders their answers: the one for
		// the tab the user left could land last and be adopted, showing that
		// terminal under the next tab's name and typing into it.
		if message.stream != nil {
			message.stream.Close()
		}
		return m, nil
	}
	m.restorePending = false
	if message.err != nil {
		if !m.noteDaemonError(message.err) {
			m.setError(terminalError, message.err.Error())
		}
		m.focus = leftPane
		return m, nil
	}
	m.closeTerminal()
	columns, rows := m.terminalSize()
	terminal := newEmbeddedTerminalWithReplay(
		message.tabID, message.stream, message.replay, int(columns), int(rows),
	)
	m.terminal = terminal
	m.terminalOpenedAt = now()
	m.markBellSeen(message.tabID)
	if tab, ok := m.openTab(); ok {
		lastTabs := maps.Clone(m.lastTabs)
		if lastTabs == nil {
			lastTabs = make(map[string]string)
		}
		lastTabs[tab.WorkspaceID] = tab.ID
		m.lastTabs = lastTabs
	}
	// The scrollback on screen belongs to the terminal that just went away, so
	// a switch made from it lands on the new terminal's live screen.
	m.stopScrollback()
	m.focus = terminalPane
	// A terminal that opened supersedes any complaint about terminals.
	m.clearError(terminalError)
	// Size only after focus hides the navigation pane on a phone. Resizing the
	// replay to the temporary split first would erase its right-hand cells.
	commands := []tea.Cmd{m.terminal.read(), m.resizeTerminal()}
	if m.rememberSelection(message.tabID) && m.configPath != "" {
		commands = append(commands, m.saveConfig())
	}
	return m, tea.Batch(commands...)
}

func (m dashboard) handleTerminalOutput(message terminalOutputMsg) (tea.Model, tea.Cmd) {
	if m.terminal == nil || message.terminal != m.terminal {
		return m, nil
	}
	if len(message.data) > 0 {
		before := m.terminal.scrollbackLen()
		m.terminal.writeOutput(message.data)
		switch {
		case !m.scrollback:
		case m.terminal.scrollbackLen() == 0:
			// The application took over the screen; its history is its own.
			m.stopScrollback()
			m.setNotice(terminalError, m.scrollbackUnavailable())
		default:
			// Hold the viewport on the same content as new output pushes
			// older lines into the scrollback.
			m.scrollTerminal(m.terminal.scrollbackLen() - before)
		}
		if effects := m.terminalEffects(); effects != nil {
			if message.err == nil {
				return m, tea.Batch(effects, m.terminal.read())
			}
		}
	}
	if message.err != nil {
		if message.inputFailure {
			m.setError(terminalError, message.err.Error())
		}
		// Drop the dead terminal now, but let the fresh snapshot decide where
		// the cursor lands: only the daemon knows whether the tab is gone.
		m.closeTerminal()
		m.stopScrollback()
		m.terminalExited = true
		return m, m.refresh()
	}
	return m, m.terminal.read()
}

// settleAfterExit moves off a terminal whose stream ended. A shell that exited
// leaves the tab behind in the daemon, so the cursor goes to a sibling tab of
// the same workspace, or back to the workspace pane when none are left. A
// connection that merely dropped leaves the tab running, and the same walk
// reattaches to it.
func (m dashboard) settleAfterExit() (tea.Model, tea.Cmd) {
	m.terminalExited = false
	if m.daemonDown {
		// The terminal ended because the daemon did; reattaching would fail
		// until F5 starts it again.
		m.focusNavigation()
		return m, nil
	}
	if m.focus != terminalPane {
		return m, nil
	}
	tabs := m.selectedTabs()
	if len(tabs) == 0 {
		m.focusNavigation()
		return m, nil
	}
	m.tabIndex = min(m.tabIndex, len(tabs)-1)

	// Reattaching to the tab that just dropped is a retry, not a move, and a
	// retry that fails the same way immediately is a loop. Space them out and
	// stop after a few, leaving the choice to reconnect with the user.
	tab := tabs[m.tabIndex]
	if tab.ID != m.reattachTab || m.attachWasHealthy() {
		m.reattachTab, m.reattachAttempts = tab.ID, 0
		return m, m.openSelectedTerminal()
	}
	m.reattachAttempts++
	if m.reattachAttempts > maximumReattachAttempts {
		m.focusNavigation()
		m.setError(terminalError, "terminal keeps disconnecting; press Enter to try again")
		m.reattachTab, m.reattachAttempts = "", 0
		return m, nil
	}
	return m, tea.Tick(reattachBackoff(m.reattachAttempts), func(time.Time) tea.Msg {
		return reopenTerminalMsg{tabID: tab.ID}
	})
}

// attachWasHealthy reports whether the terminal that just dropped had been
// attached long enough that the drop starts a fresh incident. The retry count
// then restarts, so drops spread over hours are not damped like drops in one
// second.
func (m dashboard) attachWasHealthy() bool {
	return !m.terminalOpenedAt.IsZero() && now().Sub(m.terminalOpenedAt) >= healthyAttachInterval
}

// reattachBackoff doubles with each consecutive retry so a terminal that keeps
// dropping cannot spin, while a one-off drop still reconnects promptly.
func reattachBackoff(attempt int) time.Duration {
	backoff := initialReattachBackoff << (attempt - 1)
	return min(backoff, maximumReattachBackoff)
}

func (m *dashboard) selectWorkspace() tea.Cmd {
	return m.selectWorkspaceFor("")
}

// selectWorkspaceFor opens the selected workspace. A non-empty reopenID
// creates its tab from that closed session.
func (m *dashboard) selectWorkspaceFor(reopenID string) tea.Cmd {
	if m.tabPending {
		return nil
	}
	item, ok := m.navigationItem()
	if !ok {
		return nil
	}
	tabs := runningTabs(item.tabs)
	createTab := reopenID != "" || m.tabIndex >= len(tabs)
	tabID := ""
	if !createTab {
		tabID = tabs[m.tabIndex].ID
	}
	m.selectionRequest++
	selection := m.selectionRequest
	order := m.nextSnapshotOrder()
	backend := m.backend
	return func() tea.Msg {
		workspace, err := backend.EnsureWorkspace(item.root.ID, item.workspace.Path)
		if err != nil {
			return workspaceMsg{selection: selection, err: err}
		}
		snapshot, err := backend.Snapshot()
		return workspaceMsg{value: workspace, snapshot: snapshot, order: order,
			selection: selection, tabID: tabID, createTab: createTab, reopenID: reopenID, err: err}
	}
}

func (m *dashboard) createTab() tea.Cmd {
	return m.createTabFrom("")
}

func (m *dashboard) createTabFrom(reopenID string) tea.Cmd {
	selection := m.selectionRequest
	if m.selectedWorkspaceID == "" {
		return func() tea.Msg {
			return tabMsg{selection: selection, err: fmt.Errorf("select a workspace first")}
		}
	}
	columns, rows := m.terminalSize()
	order := m.nextSnapshotOrder()
	backend := m.backend
	workspaceID := m.selectedWorkspaceID
	return func() tea.Msg {
		var tab model.Tab
		var err error
		if reopenID != "" {
			tab, err = backend.ReopenTab(workspaceID, reopenID, columns, rows)
		} else {
			tab, err = backend.CreateTab(workspaceID, columns, rows)
		}
		if err != nil {
			return tabMsg{selection: selection, err: err}
		}
		snapshot, err := backend.Snapshot()
		return tabMsg{value: tab, snapshot: snapshot, order: order, selection: selection, err: err}
	}
}

func (m dashboard) openSelectedTerminal() tea.Cmd {
	tabs := m.selectedTabs()
	if len(tabs) == 0 || m.tabIndex >= len(tabs) {
		return nil
	}
	tab := tabs[m.tabIndex]
	if !tab.Running {
		return func() tea.Msg {
			// Named, like every other answer, so the handler can tell a
			// failure meant for the tab on screen from one the user left.
			return terminalOpenedMsg{tabID: tab.ID, err: fmt.Errorf("terminal session has exited")}
		}
	}
	return func() tea.Msg {
		stream, replay, err := m.backend.OpenTerminal(tab.ID)
		if err != nil && stream != nil {
			stream.Close()
		}
		return terminalOpenedMsg{tabID: tab.ID, stream: stream, replay: replay, err: err}
	}
}

// confirmCloseTab asks before a click terminates a shell. Closing a tab kills
// what runs in it, which is the one thing romty exists to keep alive, so it
// asks the way deleting a workspace and stopping the daemon do.
func (m dashboard) confirmCloseTab(tab model.Tab, index int) (tea.Model, tea.Cmd) {
	if tab.ID == "" || m.tabClosePending != "" {
		return m, nil
	}
	m.closeTabTarget, m.closeTabIndex = tab, index
	return m.openModal(closeTabModal)
}

func (m dashboard) closeTab(tab model.Tab, index int) (tea.Model, tea.Cmd) {
	if tab.ID == "" || m.tabClosePending != "" {
		return m, nil
	}
	m.tabClosePending = tab.ID
	m.tabCloseActive = m.terminal != nil && m.terminal.id == tab.ID
	order := m.nextSnapshotOrder()
	backend := m.backend
	return m, func() tea.Msg {
		snapshot, err := backend.CloseTab(tab.ID)
		return tabClosedMsg{
			tabID: tab.ID, workspaceID: tab.WorkspaceID, index: index,
			snapshot: snapshot, order: order, err: err,
		}
	}
}

func (m dashboard) handleClosedTab(message tabClosedMsg) (tea.Model, tea.Cmd) {
	if message.tabID != m.tabClosePending {
		return m, nil
	}
	wasActive := m.tabCloseActive
	m.tabClosePending = ""
	m.tabCloseActive = false
	if message.err != nil {
		m.setError(terminalError, "close tab: "+message.err.Error())
		return m, nil
	}
	activeID := ""
	if m.terminal != nil {
		activeID = m.terminal.id
	}
	if !m.applySnapshot(message.order, message.snapshot) {
		return m, nil
	}
	m.syncSelection()
	m.ensureWorkspaceCursor()
	if wasActive {
		if activeID != "" && activeID != message.tabID {
			for index, tab := range m.selectedTabs() {
				if tab.ID == activeID {
					m.tabIndex = index
					return m, nil
				}
			}
		}
		if activeID == message.tabID {
			m.closeTerminal()
		}
		m.stopScrollback()
		m.terminalExited = false
		tabs := m.selectedTabs()
		if len(tabs) == 0 {
			m.focusNavigation()
			m.tabIndex = 0
			return m, nil
		}
		m.tabIndex = min(message.index, len(tabs)-1)
		return m, m.openSelectedTerminal()
	}
	if item, ok := m.navigationItem(); m.focus == leftPane && ok && item.workspace.ID == message.workspaceID {
		tabs := runningTabs(item.tabs)
		if len(tabs) == 0 {
			m.tabIndex = 0
		} else {
			m.tabIndex = min(message.index, len(tabs)-1)
		}
		return m, nil
	}
	if activeID != "" {
		for index, tab := range m.selectedTabs() {
			if tab.ID == activeID {
				m.tabIndex = index
				return m, nil
			}
		}
	}
	return m, nil
}

func (m dashboard) resizeTerminal() tea.Cmd {
	if m.terminal == nil {
		return nil
	}
	columns, rows := m.terminalSize()
	m.terminal.resize(int(columns), int(rows))
	terminal := m.terminal
	return func() tea.Msg {
		// Serialize the whole round trip before reading the latest size. Reading
		// it alone cannot stop an older in-flight request from arriving last.
		m.resizeMu.Lock()
		defer m.resizeMu.Unlock()
		if terminal.closed.Load() {
			return nil
		}
		columns, rows := terminal.size()
		if err := m.backend.Resize(terminal.id, columns, rows); err != nil {
			return resizeFailedMsg{tabID: terminal.id, err: err}
		}
		return nil
	}
}

func (m *dashboard) closeTerminal() {
	if m.terminal != nil {
		m.terminal.close()
		m.terminal = nil
	}
}
