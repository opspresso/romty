package ui

import (
	"fmt"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/nalbam/romty/internal/model"
)

const (
	terminalTop        = 2
	helpKeyColumnWidth = 20
	// separatorWidth is the width of what paneSeparators draws between the
	// panes. The terminal's origin, the right pane's width and the mouse
	// translation all depend on it agreeing with what is rendered.
	separatorWidth = 3

	maximumReattachAttempts = 3
	initialReattachBackoff  = 250 * time.Millisecond
	maximumReattachBackoff  = 2 * time.Second
)

type Backend interface {
	AddRoot(path string) (model.Snapshot, error)
	Snapshot() (model.Snapshot, error)
	RemoveRoot(rootID string) (model.Snapshot, error)
	EnsureWorkspace(rootID, path string) (model.Workspace, error)
	CreateTab(workspaceID string, columns, rows uint16) (model.Tab, error)
	OpenTerminal(tabID string) (io.ReadWriteCloser, error)
	Resize(tabID string, columns, rows uint16) error
	Shutdown() error
}

type Result struct {
	Quit bool
}

type pane int

const (
	leftPane pane = iota
	terminalPane
)

// errorSource records what raised the message in the status bar. There is one
// slot, so without an owner any handler could erase any other's failure — and
// the refresh a user runs right after a failure did exactly that.
type errorSource int

const (
	noError errorSource = iota
	// treeError is about the workspace tree or the daemon behind it, so a
	// fresh snapshot answers it.
	treeError
	// terminalError is about the open terminal and stands until that terminal
	// is opened, closed, or replaced.
	terminalError
	// settingError is about config or shutting the daemon down, which a
	// snapshot says nothing about.
	settingError
)

type modal int

const (
	noModal modal = iota
	aboutModal
	helpModal
	configModal
	removeRootModal
	shutdownModal
)

type navItem struct {
	root      model.Root
	workspace model.Workspace
	tabs      []model.Tab
	isRoot    bool
	lastChild bool
	// failure is set on a root romty could not read, so the tree can say why
	// it is empty instead of looking as though the root has no directories.
	failure string
}

type shortcut struct {
	key         string
	description string
}

type dashboard struct {
	backend Backend
	state   model.Snapshot
	result  Result

	width    int
	height   int
	focus    pane
	navIndex int
	// cursorPath is what the cursor is actually on. navIndex is only where
	// that lands in the tree as it stands, and the tree is rebuilt on every
	// refresh.
	cursorPath          string
	tabIndex            int
	selectedWorkspaceID string
	selectedPath        string
	inputMode           bool
	input               string
	errorMessage        string
	errorFrom           errorSource
	terminal            *embeddedTerminal
	modal               modal
	shutdownPending     bool
	// removeTarget is the root the confirmation modal is asking about, held
	// so the answer applies to the root the question named.
	removeTarget   model.Root
	terminalExited bool
	// reattachTab and reattachAttempts damp the loop a dropped connection
	// used to start: romty reattached at once, the daemon replayed the whole
	// recording, the client fell behind, and the daemon cut it off again.
	reattachTab      string
	reattachAttempts int
	scrollback       bool
	scrollOffset     int
	helpOffset       int
	configPath       string
	// config is the document as loaded, kept so saving edits it instead of
	// reconstructing it from fields.
	config           Config
	leftWidth        int
	mousePassthrough bool
	styles           *uiStyles
}

type snapshotMsg struct {
	value model.Snapshot
	err   error
}

type workspaceMsg struct {
	value     model.Workspace
	snapshot  model.Snapshot
	tabID     string
	createTab bool
	err       error
}

type tabMsg struct {
	value    model.Tab
	snapshot model.Snapshot
	err      error
}

type terminalOpenedMsg struct {
	tabID  string
	stream io.ReadWriteCloser
	err    error
}

type configSavedMsg struct {
	leftWidth int
	err       error
}

type daemonStoppedMsg struct {
	err error
}

// resizeFailedMsg keeps a failed resize out of snapshotMsg, which means "a new
// snapshot arrived" and whose handler clears the status bar. Borrowing it made
// a resize failure erase itself and, worse, made every snapshotMsg handler
// unable to trust message.value.
type resizeFailedMsg struct {
	err error
}

// reopenTerminalMsg arrives after a backoff, so a terminal that keeps dropping
// is retried at a pace that leaves the daemon and the UI usable.
type reopenTerminalMsg struct{}

func Run(backend Backend, initial model.Snapshot, configPath string) (Result, error) {
	config, err := loadConfig(configPath)
	if err != nil {
		return Result{}, fmt.Errorf("load UI config: %w", err)
	}
	program := tea.NewProgram(newDashboardWithConfig(backend, initial, configPath, config))
	final, err := program.Run()
	if err != nil {
		return Result{}, fmt.Errorf("run dashboard: %w", err)
	}
	value, ok := final.(dashboard)
	if !ok {
		return Result{}, fmt.Errorf("dashboard returned unexpected model %T", final)
	}
	value.closeTerminal()
	return value.result, nil
}

func newDashboard(backend Backend, initial model.Snapshot) dashboard {
	return newDashboardWithConfig(backend, initial, "", Config{})
}

func newDashboardWithConfig(backend Backend, initial model.Snapshot, configPath string, config Config) dashboard {
	value := dashboard{
		backend:          backend,
		state:            initial,
		width:            80,
		height:           24,
		configPath:       configPath,
		config:           config,
		leftWidth:        config.LeftWidth,
		mousePassthrough: config.MousePassthrough,
		styles:           newUIStyles(true),
	}
	value.ensureWorkspaceCursor()
	return value
}

func (m dashboard) Init() tea.Cmd {
	return tea.RequestBackgroundColor
}

func (m dashboard) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		m.height = message.Height
		return m, m.resizeTerminal()
	case tea.BackgroundColorMsg:
		m.styles = newUIStyles(message.IsDark())
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(message)
	case tea.MouseClickMsg, tea.MouseReleaseMsg, tea.MouseWheelMsg, tea.MouseMotionMsg:
		return m.forwardMouse(message.(tea.MouseMsg))
	case tea.PasteMsg:
		if m.inputMode {
			m.input += message.Content
		} else if m.focus == terminalPane && m.terminal != nil {
			m.terminal.paste(message.Content)
		}
		return m, nil
	case snapshotMsg:
		if message.err != nil {
			m.setError(treeError, message.err.Error())
			if m.terminalExited {
				// The snapshot that would have picked a sibling tab never
				// arrived. Settle on the workspace pane now: leaving the move
				// pending would fire it on an unrelated refresh much later and
				// tear down a healthy terminal.
				m.terminalExited = false
				m.focusNavigation()
			}
			return m, nil
		}
		m.state = message.value
		m.syncSelection()
		m.ensureWorkspaceCursor()
		m.clampTabIndex()
		// A refresh says the tree is current; it says nothing about a failed
		// resize or a daemon that would not stop. Clearing unconditionally
		// meant the F5 a user presses after a failure erased the reason for
		// it, and that an in-flight refresh could erase one at random.
		m.clearError(treeError)
		if m.terminalExited {
			return m.settleAfterExit()
		}
		return m, nil
	case workspaceMsg:
		return m.handleWorkspace(message)
	case tabMsg:
		return m.handleCreatedTab(message)
	case terminalOpenedMsg:
		return m.handleOpenedTerminal(message)
	case terminalOutputMsg:
		return m.handleTerminalOutput(message)
	case configSavedMsg:
		if message.leftWidth != m.leftWidth {
			return m, m.saveConfig()
		}
		if message.err != nil {
			m.setError(settingError, message.err.Error())
			return m, m.resizeTerminal()
		}
		m.clearError(settingError)
		return m, m.resizeTerminal()
	case resizeFailedMsg:
		// The emulator has already taken the new size, so it and the PTY now
		// disagree until the next successful resize.
		m.setError(terminalError, "resize terminal: "+message.err.Error())
		return m, nil
	case reopenTerminalMsg:
		return m, m.openSelectedTerminal()
	case daemonStoppedMsg:
		if message.err != nil {
			m.modal = noModal
			m.shutdownPending = false
			m.setError(settingError, "stop daemon: "+message.err.Error())
			return m, nil
		}
		return m.quit()
	}
	return m, nil
}

// globalKeys are handled in both panes and with a modal open. Routing every
// function key through one table keeps their precedence at a single point.
var globalKeys = map[string]func(dashboard) (tea.Model, tea.Cmd){
	// F1 is help everywhere else in computing, and romty used to spend it on
	// About while help sat on `?` in the workspace pane only. That left the
	// terminal pane, where the shortcuts are hardest to remember, with no way
	// to look them up at all.
	"f1": func(m dashboard) (tea.Model, tea.Cmd) { return m.openModal(helpModal) },
	"f2": func(m dashboard) (tea.Model, tea.Cmd) { return m.startRootInput() },
	"f3": func(m dashboard) (tea.Model, tea.Cmd) { return m.openModal(configModal) },
	"f4": func(m dashboard) (tea.Model, tea.Cmd) { return m.quit() },
	"f5": func(m dashboard) (tea.Model, tea.Cmd) { return m, m.refresh() },
	"f6": func(m dashboard) (tea.Model, tea.Cmd) { return m.toggleScrollback() },
	"f7": func(m dashboard) (tea.Model, tea.Cmd) { return m.toggleFocus() },
	// F8 is delete in the file managers this layout comes from. Removing a
	// root was reachable from the workspace pane only, which left the terminal
	// pane without it.
	"f8": func(m dashboard) (tea.Model, tea.Cmd) { return m.confirmRemoveRoot() },
	// Stopping the daemon kills every running shell. It used to sit at F6,
	// one key from refresh; the destructive actions belong at the far end of
	// the row instead, where a mistyped F5 cannot reach them.
	"f9": func(m dashboard) (tea.Model, tea.Cmd) { return m.openModal(shutdownModal) },
	// Shift+PgUp reaches the history in one press by entering scrollback itself.
	"shift+pgup":   func(m dashboard) (tea.Model, tea.Cmd) { return m.pageHistory(1) },
	"shift+pgdown": func(m dashboard) (tea.Model, tea.Cmd) { return m.pageHistory(-1) },
}

func (m dashboard) handleKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.inputMode {
		return m.handleInput(message)
	}
	if m.shutdownPending {
		// The daemon is already stopping; only quitting the TUI still applies.
		if message.String() == "f4" {
			return m.quit()
		}
		return m, nil
	}
	if action, ok := globalKeys[message.String()]; ok {
		return action(m)
	}
	if m.modal != noModal {
		return m.handleModalKey(message)
	}
	if m.scrollback {
		return m.handleScrollbackKey(message)
	}
	if m.focus == terminalPane {
		if message.String() == "ctrl+\\" {
			m.focusNavigation()
			return m, m.refresh()
		}
		if m.terminal != nil {
			m.terminal.sendKey(message)
		}
		return m, nil
	}

	switch message.String() {
	case "ctrl+c":
		return m.quit()
	case "tab":
		if m.terminal != nil && m.terminal.active {
			m.focus = terminalPane
			m.syncTabCursor(m.selectedTabs())
		}
	case "i":
		return m.openModal(aboutModal)
	case "a":
		return m.startRootInput()
	case ",":
		return m.openModal(configModal)
	case "q":
		return m.quit()
	case "r":
		return m, m.refresh()
	case "?":
		return m.openModal(helpModal)
	case "d":
		return m.confirmRemoveRoot()
	case "ctrl+\\":
		// A second Ctrl+\ after leaving the terminal opens its scrollback.
		return m.toggleScrollback()
	case "up", "k":
		m.moveNavigation(-1)
	case "down", "j":
		m.moveNavigation(1)
	case "left", "h":
		m.moveTab(-1)
	case "right", "l":
		m.moveTab(1)
	case "enter":
		return m, m.selectWorkspace()
	}
	return m, nil
}

func (m dashboard) quit() (tea.Model, tea.Cmd) {
	m.closeTerminal()
	m.result.Quit = true
	return m, tea.Quit
}

// setError takes the status bar for one source; clearError gives it up only if
// that source still holds it.
func (m *dashboard) setError(source errorSource, message string) {
	m.errorMessage = message
	m.errorFrom = source
}

func (m *dashboard) clearError(source errorSource) {
	if m.errorFrom == source || m.errorFrom == noError {
		m.errorMessage = ""
		m.errorFrom = noError
	}
}

func (m *dashboard) clearAnyError() {
	m.errorMessage = ""
	m.errorFrom = noError
}

func (m dashboard) openModal(value modal) (tea.Model, tea.Cmd) {
	m.modal = value
	m.helpOffset = 0
	m.clearAnyError()
	return m, nil
}

func (m dashboard) startRootInput() (tea.Model, tea.Cmd) {
	m.modal = noModal
	m.inputMode = true
	m.input = ""
	m.clearAnyError()
	return m, nil
}

func (m dashboard) handleModalKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if message.String() == "esc" {
		m.modal = noModal
		return m, nil
	}
	switch m.modal {
	case removeRootModal:
		if message.String() == "enter" {
			m.modal = noModal
			return m, m.removeRoot()
		}
	case shutdownModal:
		if message.String() == "enter" {
			m.shutdownPending = true
			return m, m.shutdownDaemon()
		}
	case helpModal:
		switch message.String() {
		case "up", "k":
			return m.scrollHelp(-1)
		case "down", "j":
			return m.scrollHelp(1)
		}
	case configModal:
		switch message.String() {
		case "left", "[":
			return m.adjustLeftWidth(-1)
		case "right", "]":
			return m.adjustLeftWidth(1)
		}
	}
	return m, nil
}

// Scrollback mode is the only state where romty asks the host terminal for
// mouse events. Everywhere else the mouse belongs to the host so its native
// drag selection keeps working, which is why this is an explicit mode.
func (m dashboard) handleScrollbackKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.terminal == nil {
		m.stopScrollback()
		return m, nil
	}
	page := m.scrollbackPage()
	switch message.String() {
	case "esc", "q", "ctrl+\\":
		m.stopScrollback()
	case "up", "k":
		m.scrollTerminal(1)
	case "down", "j":
		m.scrollTerminal(-1)
	case "pgup", "ctrl+b":
		m.scrollTerminal(page)
	case "pgdown", "ctrl+f":
		m.scrollTerminal(-page)
	case "home", "g":
		m.scrollTerminal(m.terminal.scrollbackLen())
	case "end", "G":
		m.scrollTerminal(-m.terminal.scrollbackLen())
	}
	return m, nil
}

// forwardMouse relays a host mouse event to the guest application. romty only
// receives these when passthrough handed the mouse over, so there is nothing to
// do otherwise.
func (m dashboard) forwardMouse(message tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.mouseMode() == tea.MouseModeNone {
		return m, nil
	}
	mouse, inside := m.translateMouse(message.Mouse())
	if !inside {
		return m, nil
	}
	switch message.(type) {
	case tea.MouseClickMsg:
		m.terminal.sendMouse(uv.MouseClickEvent(mouse))
	case tea.MouseReleaseMsg:
		m.terminal.sendMouse(uv.MouseReleaseEvent(mouse))
	case tea.MouseWheelMsg:
		m.terminal.sendMouse(uv.MouseWheelEvent(mouse))
	case tea.MouseMotionMsg:
		m.terminal.sendMouse(uv.MouseMotionEvent(mouse))
	}
	return m, nil
}

// translateMouse moves a host screen position into the terminal pane's own
// coordinate space and reports false for events outside the pane.
func (m dashboard) translateMouse(mouse tea.Mouse) (uv.Mouse, bool) {
	view := m.dimensions()
	originX, originY := view.terminalOrigin()
	translated := uv.Mouse(mouse)
	translated.X = mouse.X - originX
	translated.Y = mouse.Y - originY
	if translated.X < 0 || translated.X >= view.rightWidth || translated.Y < 0 || translated.Y >= view.terminalHeight {
		return uv.Mouse{}, false
	}
	return translated, true
}

// toggleFocus moves between the panes in one key. Tab and Ctrl+\ each did it
// in one direction only, and on Windows Ctrl+\ often never arrives: whatever
// claimed it as a global hotkey — 1Password, for one — takes it before the
// terminal sees it, which left that pane with no way out.
func (m dashboard) toggleFocus() (tea.Model, tea.Cmd) {
	if m.scrollback {
		// Scrollback fills the screen with the terminal, so leaving the
		// terminal means leaving scrollback with it. stopScrollback lands in
		// the terminal, which the branch below then moves away from.
		m.stopScrollback()
	}
	if m.focus == terminalPane {
		m.focusNavigation()
		return m, m.refresh()
	}
	if m.terminal != nil && m.terminal.active {
		m.focus = terminalPane
		m.syncTabCursor(m.selectedTabs())
	}
	return m, nil
}

func (m dashboard) toggleScrollback() (tea.Model, tea.Cmd) {
	if m.scrollback {
		m.stopScrollback()
		return m, nil
	}
	return m.pageHistory(0)
}

func (m dashboard) pageHistory(pages int) (tea.Model, tea.Cmd) {
	if !m.scrollback && !m.startScrollback() {
		// An application that owns the screen pages its own output. Send it the
		// unmodified key, because that is what such applications bind.
		if pages != 0 && m.focus == terminalPane && m.terminal != nil && m.terminal.altScreen() {
			m.terminal.sendKey(pagingKey(pages))
			return m, nil
		}
		m.setError(terminalError, m.scrollbackUnavailable())
		return m, nil
	}
	m.scrollTerminal(pages * m.scrollbackPage())
	return m, nil
}

func pagingKey(pages int) tea.KeyPressMsg {
	if pages < 0 {
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown})
	}
	return tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp})
}

func (m *dashboard) startScrollback() bool {
	if m.terminal == nil || m.terminal.scrollbackLen() == 0 {
		return false
	}
	m.scrollback = true
	m.modal = noModal
	m.clearAnyError()
	return true
}

// scrollbackUnavailable explains why there is nothing for scrollback mode to
// show, so an application that owns the screen is not mistaken for a bug.
func (m dashboard) scrollbackUnavailable() string {
	switch {
	case m.terminal == nil:
		return "open a terminal to scroll its output"
	case m.terminal.altScreen():
		return "the running application owns the screen; scroll inside it"
	default:
		return "no output has scrolled off this terminal yet"
	}
}

func (m *dashboard) stopScrollback() {
	m.scrollback = false
	m.scrollOffset = 0
	// Copy mode fills the screen with the terminal, so leaving it lands in the
	// terminal rather than back in the workspace tree.
	if m.terminal != nil && m.terminal.active {
		m.focus = terminalPane
		m.syncTabCursor(m.selectedTabs())
	}
}

// scrollTerminal moves the viewport by delta lines, positive towards the oldest
// output. Offset zero is the live screen.
func (m *dashboard) scrollTerminal(delta int) {
	if m.terminal == nil {
		return
	}
	m.scrollOffset = min(max(m.scrollOffset+delta, 0), m.terminal.scrollbackLen())
}

func (m dashboard) scrollbackPage() int {
	terminalHeight := m.dimensions().terminalHeight
	return max(terminalHeight-1, 1)
}

func (m dashboard) scrollHelp(delta int) (tea.Model, tea.Cmd) {
	bodyHeight := m.dimensions().bodyHeight
	m.helpOffset = min(max(m.helpOffset+delta, 0), m.maximumHelpOffset(bodyHeight))
	return m, nil
}

func (m dashboard) maximumHelpOffset(height int) int {
	return max(len(m.helpEntries())-modalCapacity(height), 0)
}

func (m dashboard) shutdownDaemon() tea.Cmd {
	return func() tea.Msg {
		return daemonStoppedMsg{err: m.backend.Shutdown()}
	}
}

func (m dashboard) adjustLeftWidth(delta int) (tea.Model, tea.Cmd) {
	current := m.dimensions().leftWidth
	maximum := min(maximumLeftWidth, max(m.width, 40)-20)
	m.leftWidth = min(max(current+delta, minimumLeftWidth), maximum)
	return m, m.saveConfig()
}

func (m dashboard) saveConfig() tea.Cmd {
	path := m.configPath
	// Edit the document romty loaded rather than rebuilding it from fields.
	// Rebuilding meant a setting nobody remembered to copy here was erased the
	// moment the user touched the pane width, and an older romty would erase
	// whatever a newer one had written.
	config := m.config
	config.LeftWidth = m.leftWidth
	return func() tea.Msg {
		return configSavedMsg{leftWidth: config.LeftWidth, err: saveConfig(path, config)}
	}
}

func (m dashboard) handleInput(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "esc":
		m.inputMode = false
		m.input = ""
	case "enter":
		if strings.TrimSpace(m.input) == "" {
			m.setError(treeError, "root path is required")
			return m, nil
		}
		path := strings.TrimSpace(m.input)
		m.inputMode = false
		m.input = ""
		return m, func() tea.Msg {
			value, err := m.backend.AddRoot(path)
			return snapshotMsg{value: value, err: err}
		}
	case "backspace":
		runes := []rune(m.input)
		if len(runes) > 0 {
			m.input = string(runes[:len(runes)-1])
		}
	default:
		if message.Text != "" {
			m.input += message.Text
		}
	}
	return m, nil
}

func (m dashboard) handleWorkspace(message workspaceMsg) (tea.Model, tea.Cmd) {
	if message.err != nil {
		m.setError(treeError, message.err.Error())
		return m, nil
	}
	m.selectedWorkspaceID = message.value.ID
	m.selectedPath = message.value.Path
	m.reattachTab, m.reattachAttempts = "", 0
	m.state = message.snapshot
	tabs := m.selectedTabs()
	m.focus = leftPane
	// The user's selection succeeded, so whatever was on the status bar is
	// answered whichever part of romty put it there.
	m.clearAnyError()
	if message.createTab {
		m.tabIndex = len(tabs)
		return m, m.createTab()
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
	if message.err != nil {
		m.setError(terminalError, message.err.Error())
		return m, nil
	}
	m.state = message.snapshot
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
	if message.err != nil {
		m.setError(terminalError, message.err.Error())
		m.focus = leftPane
		return m, nil
	}
	m.closeTerminal()
	columns, rows := m.terminalSize()
	m.terminal = newEmbeddedTerminal(message.tabID, message.stream, int(columns), int(rows))
	m.focus = terminalPane
	// A terminal that opened supersedes any complaint about terminals.
	m.clearError(terminalError)
	return m, m.terminal.read()
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
			m.setError(terminalError, m.scrollbackUnavailable())
		default:
			// Hold the viewport on the same content as new output pushes
			// older lines into the scrollback.
			m.scrollTerminal(m.terminal.scrollbackLen() - before)
		}
	}
	if message.err != nil {
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
	if tab.ID != m.reattachTab {
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
		return reopenTerminalMsg{}
	})
}

// reattachBackoff doubles with each consecutive retry so a terminal that keeps
// dropping cannot spin, while a one-off drop still reconnects promptly.
func reattachBackoff(attempt int) time.Duration {
	backoff := initialReattachBackoff << (attempt - 1)
	return min(backoff, maximumReattachBackoff)
}

// confirmRemoveRoot asks about the root the cursor is on. It used to go
// straight through on one keypress, which stopping the daemon — the other
// action with no undo — has always asked about first.
//
// The root is captured here rather than read again on confirmation: a snapshot
// arriving while the modal is open can move the cursor, and the answer has to
// apply to the root the question named.
func (m dashboard) confirmRemoveRoot() (tea.Model, tea.Cmd) {
	item, ok := m.navigationItem()
	if !ok {
		return m, nil
	}
	m.removeTarget = item.root
	return m.openModal(removeRootModal)
}

// removeRoot forgets the root the modal named. Terminals under it keep
// running in the daemon; they are simply no longer listed.
func (m dashboard) removeRoot() tea.Cmd {
	rootID := m.removeTarget.ID
	if rootID == "" {
		return nil
	}
	return func() tea.Msg {
		value, err := m.backend.RemoveRoot(rootID)
		return snapshotMsg{value: value, err: err}
	}
}

func (m dashboard) refresh() tea.Cmd {
	return func() tea.Msg {
		value, err := m.backend.Snapshot()
		return snapshotMsg{value: value, err: err}
	}
}

func (m dashboard) selectWorkspace() tea.Cmd {
	item, ok := m.navigationItem()
	if !ok {
		return nil
	}
	tabs := runningTabs(item.tabs)
	createTab := m.tabIndex >= len(tabs)
	tabID := ""
	if !createTab {
		tabID = tabs[m.tabIndex].ID
	}
	return func() tea.Msg {
		workspace, err := m.backend.EnsureWorkspace(item.root.ID, item.workspace.Path)
		if err != nil {
			return workspaceMsg{err: err}
		}
		snapshot, err := m.backend.Snapshot()
		return workspaceMsg{value: workspace, snapshot: snapshot, tabID: tabID, createTab: createTab, err: err}
	}
}

func (m dashboard) createTab() tea.Cmd {
	if m.selectedWorkspaceID == "" {
		return func() tea.Msg { return tabMsg{err: fmt.Errorf("select a workspace first")} }
	}
	columns, rows := m.terminalSize()
	return func() tea.Msg {
		tab, err := m.backend.CreateTab(m.selectedWorkspaceID, columns, rows)
		if err != nil {
			return tabMsg{err: err}
		}
		snapshot, err := m.backend.Snapshot()
		return tabMsg{value: tab, snapshot: snapshot, err: err}
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
			return terminalOpenedMsg{err: fmt.Errorf("terminal session has exited")}
		}
	}
	columns, rows := m.terminalSize()
	return func() tea.Msg {
		stream, err := m.backend.OpenTerminal(tab.ID)
		if err == nil {
			err = m.backend.Resize(tab.ID, columns, rows)
		}
		if err != nil && stream != nil {
			stream.Close()
		}
		return terminalOpenedMsg{tabID: tab.ID, stream: stream, err: err}
	}
}

func (m dashboard) resizeTerminal() tea.Cmd {
	if m.terminal == nil || !m.terminal.active {
		return nil
	}
	columns, rows := m.terminalSize()
	m.terminal.resize(int(columns), int(rows))
	tabID := m.terminal.id
	return func() tea.Msg {
		if err := m.backend.Resize(tabID, columns, rows); err != nil {
			return resizeFailedMsg{err: err}
		}
		return nil
	}
}

func (m *dashboard) closeTerminal() {
	if m.terminal != nil {
		m.terminal.closeTerminal()
		m.terminal = nil
	}
}

func (m *dashboard) moveNavigation(delta int) {
	items := m.navigationItems()
	if len(items) == 0 {
		m.navIndex, m.cursorPath = 0, ""
		m.tabIndex = 0
		return
	}
	m.navIndex = (m.navIndex + delta + len(items)) % len(items)
	m.cursorPath = items[m.navIndex].workspace.Path
	m.syncTabCursor(runningTabs(items[m.navIndex].tabs))
}

func (m *dashboard) moveTab(delta int) {
	if _, ok := m.navigationItem(); !ok {
		m.tabIndex = 0
		return
	}
	count := len(m.navigationTabs()) + 1
	m.tabIndex = (m.tabIndex + delta + count) % count
}

func (m *dashboard) clampTabIndex() {
	if _, ok := m.navigationItem(); !ok {
		m.tabIndex = 0
		return
	}
	count := len(m.navigationTabs())
	if m.tabIndex > count {
		m.tabIndex = count
	}
}

// ensureWorkspaceCursor keeps the cursor on the item it was on, by path rather
// than by position. The tree is rebuilt from the daemon on every refresh, so a
// directory appearing above the cursor — a `git clone` finishing in another
// terminal — used to slide the highlight onto its neighbour without the user
// touching a key, and Enter then opened the wrong workspace.
func (m *dashboard) ensureWorkspaceCursor() {
	items := m.navigationItems()
	if len(items) == 0 {
		m.navIndex, m.cursorPath = 0, ""
		return
	}
	if m.cursorPath != "" {
		for index, item := range items {
			if item.workspace.Path == m.cursorPath {
				m.navIndex = index
				return
			}
		}
	}
	// The remembered item is gone, or there is none yet: fall back to the
	// position and record whatever that lands on.
	m.navIndex = min(max(m.navIndex, 0), len(items)-1)
	m.cursorPath = items[m.navIndex].workspace.Path
}

// setNavigation moves the cursor and records what it landed on, so a rebuilt
// tree can find the same item again. Setting navIndex alone leaves the two
// disagreeing, and the next refresh undoes the move.
func (m *dashboard) setNavigation(index int) {
	m.navIndex = index
	if item, ok := m.navigationItem(); ok {
		m.cursorPath = item.workspace.Path
	}
}

func (m *dashboard) focusNavigation() {
	m.focus = leftPane
	items := m.navigationItems()
	for index, item := range items {
		if item.workspace.Path == m.selectedPath {
			m.navIndex = index
			m.cursorPath = item.workspace.Path
			m.syncTabCursor(runningTabs(item.tabs))
			return
		}
	}
}

func (m *dashboard) syncTabCursor(tabs []model.Tab) {
	m.tabIndex = 0
	if m.terminal == nil {
		return
	}
	for index, tab := range tabs {
		if tab.ID == m.terminal.id {
			m.tabIndex = index
			return
		}
	}
}

func (m *dashboard) syncSelection() {
	for _, root := range m.state.Roots {
		if root.Root.Path == m.selectedPath {
			if len(root.Tabs) > 0 {
				m.selectedWorkspaceID = root.Tabs[0].WorkspaceID
			}
			return
		}
		for _, directory := range root.Directories {
			if directory.Workspace.Path == m.selectedPath {
				m.selectedWorkspaceID = directory.Workspace.ID
				return
			}
		}
	}
	m.selectedWorkspaceID = ""
	m.selectedPath = ""
}

func (m dashboard) navigationItems() []navItem {
	result := make([]navItem, 0)
	for _, root := range m.state.Roots {
		result = append(result, navItem{
			root: root.Root,
			workspace: model.Workspace{
				RootID: root.Root.ID,
				Name:   root.Root.Name,
				Path:   root.Root.Path,
			},
			tabs:    root.Tabs,
			isRoot:  true,
			failure: root.Error,
		})
		for index, directory := range root.Directories {
			result = append(result, navItem{
				root:      root.Root,
				workspace: directory.Workspace,
				tabs:      directory.Tabs,
				lastChild: index == len(root.Directories)-1,
			})
		}
	}
	return result
}

func (m dashboard) navigationItem() (navItem, bool) {
	items := m.navigationItems()
	if m.navIndex < 0 || m.navIndex >= len(items) {
		return navItem{}, false
	}
	return items[m.navIndex], true
}

func (m dashboard) navigationTabs() []model.Tab {
	item, ok := m.navigationItem()
	if !ok {
		return nil
	}
	return runningTabs(item.tabs)
}

func openTabMarkers(tabs []model.Tab) string {
	count := 0
	for _, tab := range tabs {
		if tab.Running {
			count++
		}
	}
	return strings.Repeat("●", count)
}

func (m dashboard) selectedTabs() []model.Tab {
	for _, root := range m.state.Roots {
		for _, tab := range root.Tabs {
			if tab.WorkspaceID == m.selectedWorkspaceID {
				return runningTabs(root.Tabs)
			}
		}
		for _, directory := range root.Directories {
			if directory.Workspace.ID == m.selectedWorkspaceID {
				return runningTabs(directory.Tabs)
			}
		}
	}
	return nil
}

func runningTabs(tabs []model.Tab) []model.Tab {
	result := make([]model.Tab, 0, len(tabs))
	for _, tab := range tabs {
		if tab.Running {
			result = append(result, tab)
		}
	}
	return result
}

// layout is the one description of where things sit. It used to be four
// unnamed ints, which every one of nineteen call sites unpacked positionally:
// swapping two widths compiled, rendered, and only looked wrong.
type layout struct {
	leftWidth      int
	rightWidth     int
	bodyHeight     int
	terminalHeight int
}

// terminalOrigin is where the terminal's own first cell lands on screen, which
// the cursor and mouse translation both need. Deriving it here keeps it tied
// to the separator and tab bar rather than repeated as a literal.
func (l layout) terminalOrigin() (int, int) {
	return l.leftWidth + separatorWidth, terminalTop
}

func (m dashboard) dimensions() layout {
	width := max(m.width, 40)
	height := max(m.height, 10)
	leftWidth := m.leftWidth
	if leftWidth == 0 {
		leftWidth = min(max(width/4, minimumLeftWidth), 28)
	}
	leftWidth = min(leftWidth, width-20)
	bodyHeight := height - 2
	return layout{
		leftWidth:      leftWidth,
		rightWidth:     max(width-leftWidth-separatorWidth, 17),
		bodyHeight:     bodyHeight,
		terminalHeight: max(bodyHeight-terminalTop, 1),
	}
}

func (m dashboard) terminalSize() (uint16, uint16) {
	view := m.dimensions()
	return uint16(min(view.rightWidth, 65535)), uint16(min(view.terminalHeight, 65535))
}

func (m dashboard) View() tea.View {
	content := m.render()
	view := tea.NewView(content)
	view.AltScreen = true
	view.WindowTitle = "romty"
	view.MouseMode = m.mouseMode()
	if !m.scrollback && m.focus == terminalPane && m.terminal != nil && m.terminal.active {
		originX, originY := m.dimensions().terminalOrigin()
		position := m.terminal.cursorPosition()
		view.Cursor = tea.NewCursor(originX+position.X, originY+position.Y)
	}
	return view
}

// mouseMode keeps the mouse with the host terminal, where its native drag
// selection lives. Copy mode relies on the terminal's alternate scroll to turn
// the wheel into arrow keys instead of claiming the mouse. The only handover is
// to a guest application that asked for the mouse, and only when the user
// opted in, which is the same trade tmux makes for `set -g mouse on`.
func (m dashboard) mouseMode() tea.MouseMode {
	if !m.mousePassthrough || m.scrollback || m.terminal == nil || !m.terminal.active {
		return tea.MouseModeNone
	}
	return m.terminal.guestMouseMode()
}

func (m dashboard) render() string {
	view := m.dimensions()
	width := max(m.width, 40)
	lines := m.renderPanes(view.leftWidth, view.rightWidth, view.bodyHeight)
	if m.scrollback {
		lines = m.renderRows(m.renderTerminal(width), width, view.bodyHeight)
	}
	if m.modal != noModal {
		lines = m.overlayModal(lines, width, view.bodyHeight)
	}
	lines = append(lines, m.renderStatus(width, view.bodyHeight)...)
	return strings.Join(lines, "\n")
}

func (m dashboard) renderPanes(leftWidth, rightWidth, bodyHeight int) []string {
	left := m.renderNavigation(leftWidth, bodyHeight)
	right := m.renderTerminal(rightWidth)
	headSeparator, bodySeparator := m.paneSeparators()
	lines := make([]string, 0, bodyHeight+2)
	for row := 0; row < bodyHeight; row++ {
		leftLine := ""
		if row < len(left) {
			leftLine = left[row]
		}
		rightLine := ""
		if row < len(right) {
			rightLine = right[row]
		}
		separator := bodySeparator
		if row == 0 {
			separator = headSeparator
		}
		lines = append(lines, pad(truncate(leftLine, leftWidth), leftWidth)+separator+truncate(rightLine, rightWidth))
	}
	return lines
}

// renderRows lays out a single full-width pane. Copy mode uses it so every row
// holds terminal output alone and a plain drag in the host terminal selects
// exactly what is on screen, with no workspace tree spliced into each line.
func (m dashboard) renderRows(rows []string, width, height int) []string {
	lines := make([]string, 0, height+2)
	for row := range height {
		line := ""
		if row < len(rows) {
			line = rows[row]
		}
		lines = append(lines, truncate(line, width))
	}
	return lines
}

// renderStatus returns the shortcut rail and the status bar. Only the default
// branch keeps a contextual rail; every other state uses a plain full-width one.
func (m dashboard) renderStatus(width, bodyHeight int) []string {
	rail := m.styles.tabRail.Render(strings.Repeat("─", width))
	var status string
	switch {
	case m.inputMode:
		status = truncate(
			m.styles.promptLabel.Render(" ROOT ")+" "+m.styles.promptText.Render(m.input)+m.styles.dividerActive.Render("█"),
			width,
		)
	case m.errorMessage != "":
		status = truncate(m.styles.errorLabel.Render(" ERROR ")+" "+m.styles.errorText.Render(m.errorMessage), width)
	case m.modal == helpModal:
		shortcuts := []shortcut{{key: "Esc", description: "close"}}
		if m.maximumHelpOffset(bodyHeight) > 0 {
			shortcuts = append([]shortcut{{key: "↑/↓", description: "scroll"}}, shortcuts...)
		}
		status = renderShortcuts(m.styles, width, shortcuts...)
	case m.modal == aboutModal:
		status = renderShortcuts(m.styles, width, shortcut{key: "Esc", description: "close"})
	case m.modal == configModal:
		status = renderShortcuts(m.styles, width,
			shortcut{key: "←/→", description: "adjust width"},
			shortcut{key: "Esc", description: "close"},
		)
	case m.modal == shutdownModal && m.shutdownPending:
		// The request is already out; no key can take it back.
		status = truncate(
			m.styles.promptLabel.Render(" STOPPING ")+" "+m.styles.shortcutDescription.Render("waiting for the daemon to stop"),
			width,
		)
	case m.modal == removeRootModal:
		status = renderShortcuts(m.styles, width,
			shortcut{key: "Enter", description: "remove root"},
			shortcut{key: "Esc", description: "cancel"},
		)
	case m.modal == shutdownModal:
		status = renderShortcuts(m.styles, width,
			shortcut{key: "Enter", description: "stop daemon"},
			shortcut{key: "Esc", description: "cancel"},
		)
	case m.scrollback:
		status = truncate(
			m.styles.promptLabel.Render(" SCROLLBACK ")+" "+
				m.styles.shortcutDescription.Render(m.scrollbackPosition())+"  "+
				renderShortcuts(m.styles, width,
					shortcut{key: "↑/↓", description: "line"},
					shortcut{key: "PgUp/PgDn", description: "page"},
					shortcut{key: "Esc", description: "exit"},
				),
			width,
		)
	default:
		contextShortcuts := []shortcut{
			{key: "↑/↓", description: "workspace"},
			{key: "←/→", description: "tabs"},
			{key: "Enter", description: "open"},
			{key: "Tab", description: "terminal"},
		}
		if m.focus == terminalPane {
			contextShortcuts = []shortcut{{key: "F7", description: "navigation"}, {key: "Ctrl+\\", description: "navigation"}}
		}
		rail = renderShortcutRail(m.styles, width, contextShortcuts...)
		status = renderShortcuts(m.styles, width,
			shortcut{key: "F1", description: "help"},
			shortcut{key: "F2", description: "add root"},
			shortcut{key: "F3", description: "config"},
			shortcut{key: "F4", description: "quit"},
			shortcut{key: "F5", description: "refresh"},
			shortcut{key: "F6", description: "scrollback"},
			shortcut{key: "F7", description: "focus"},
		)
	}
	return []string{rail, status}
}

// scrollbackPosition reports how far back the viewport sits in the history.
func (m dashboard) scrollbackPosition() string {
	if m.terminal == nil {
		return "0/0"
	}
	return fmt.Sprintf("%d/%d", m.scrollOffset, m.terminal.scrollbackLen())
}

func (m dashboard) overlayModal(lines []string, width, height int) []string {
	modalLines := m.renderModal(width, height)
	top := max((len(lines)-len(modalLines))/2, 0)
	for index, line := range modalLines {
		row := top + index
		if row >= len(lines) {
			break
		}
		lineWidth := lipgloss.Width(line)
		left := max((width-lineWidth)/2, 0)
		right := max(width-left-lineWidth, 0)
		lines[row] = strings.Repeat(" ", left) + line + strings.Repeat(" ", right)
	}
	return lines
}

func (m dashboard) renderModal(width, height int) []string {
	modalWidth := min(max(width-8, 32), 56)
	if m.modal == helpModal {
		modalWidth = min(max(width-4, 40), 64)
		return m.renderHelpModal(modalWidth, height)
	}
	if m.modal == removeRootModal {
		return modalBox(m.styles, modalWidth, "Remove root",
			"",
			m.styles.modalStrong.Render("Forget "+m.removeTarget.Name+"?"),
			"",
			m.styles.modalBody.Render("Terminals under it keep running."),
			"",
		)
	}
	if m.modal == shutdownModal {
		return modalBox(m.styles, modalWidth, "Stop daemon",
			"",
			m.styles.modalStrong.Render("Stop daemon and all running terminal sessions?"),
			"",
			m.styles.errorText.Render("Running shells will be terminated."),
			"",
		)
	}
	if m.modal == configModal {
		return modalBox(m.styles, modalWidth, "Config",
			"",
			m.styles.modalStrong.Render(fmt.Sprintf("Left pane width: %d", m.dimensions().leftWidth)),
			"",
			m.styles.modalBody.Render("Use ←/→ or [/] to adjust"),
			"",
		)
	}
	return modalBox(m.styles, modalWidth, "About",
		"",
		m.styles.modalStrong.Render("romty"),
		m.styles.modalBody.Render("Persistent terminal workspace manager"),
		"",
	)
}

func (m dashboard) helpEntries() []string {
	return []string{
		// About lost its function key to help, so help carries what it said.
		m.styles.modalStrong.Render("romty") + m.styles.modalBody.Render("  Persistent terminal workspace manager"),
		renderHelpSection(m.styles, "COMMANDS", "F-keys work in both areas"),
		renderHelpShortcut(m.styles, "Help", "F1", "?"),
		renderHelpShortcut(m.styles, "Add root", "F2", "a"),
		renderHelpShortcut(m.styles, "Remove root", "F8", "d"),
		renderHelpShortcut(m.styles, "Config", "F3", ","),
		renderHelpShortcut(m.styles, "Quit", "F4", "q"),
		renderHelpShortcut(m.styles, "Refresh", "F5", "r"),
		renderHelpShortcut(m.styles, "Scrollback", "F6"),
		renderHelpShortcut(m.styles, "Switch pane", "F7"),
		renderHelpShortcut(m.styles, "Stop daemon", "F9"),
		renderHelpShortcut(m.styles, "About", "i"),
		renderHelpSection(m.styles, "NAVIGATION", "workspace area"),
		renderHelpShortcut(m.styles, "Select workspace", "↑/↓", "j/k"),
		renderHelpShortcut(m.styles, "Select tab / +", "←/→", "h/l"),
		renderHelpShortcut(m.styles, "Open / confirm", "Enter"),
		renderHelpShortcut(m.styles, "Focus terminal", "Tab", "F7"),
		renderHelpSection(m.styles, "TERMINAL", "terminal area"),
		renderHelpShortcut(m.styles, "Focus workspace", "F7", "Ctrl+\\"),
		renderHelpSection(m.styles, "SCROLLBACK", "mouse works here only"),
		renderHelpShortcut(m.styles, "Enter / leave", "F6", "Ctrl+\\"),
		renderHelpShortcut(m.styles, "Scroll a line", "↑/↓", "j/k"),
		renderHelpShortcut(m.styles, "Scroll a page", "PgUp/PgDn"),
		renderHelpShortcut(m.styles, "Scroll with the mouse", "Wheel"),
		renderHelpShortcut(m.styles, "Enter at a page back", "Shift+PgUp"),
		renderHelpShortcut(m.styles, "Oldest / newest", "Home/End"),
		renderHelpSection(m.styles, "OTHER", "contextual"),
		renderHelpShortcut(m.styles, "Quit", "Ctrl+C"),
		renderHelpShortcut(m.styles, "Resize workspace pane", "←/→", "[/]"),
		renderHelpShortcut(m.styles, "Close / cancel", "Esc"),
	}
}

// renderHelpModal windows the shortcut list so the box always fits the body and
// stays terminated; the title carries the visible range when it has to scroll.
func (m dashboard) renderHelpModal(width, height int) []string {
	entries := m.helpEntries()
	capacity := modalCapacity(height)
	if len(entries) <= capacity {
		return modalBox(m.styles, width, "Help", entries...)
	}
	offset := min(max(m.helpOffset, 0), len(entries)-capacity)
	title := fmt.Sprintf("Help %d-%d/%d", offset+1, offset+capacity, len(entries))
	return modalBox(m.styles, width, title, entries[offset:offset+capacity]...)
}

// modalCapacity is the number of content lines a modal box can hold without
// losing its top and bottom borders.
func modalCapacity(height int) int {
	return max(height-2, 1)
}

func renderHelpSection(styles *uiStyles, title, note string) string {
	return styles.modalBorder.Render("── ") + styles.modalTitle.Render(title) + "  " + styles.empty.Render(note)
}

func renderHelpShortcut(styles *uiStyles, description string, keys ...string) string {
	keycaps := make([]string, 0, len(keys))
	for _, key := range keys {
		keycaps = append(keycaps, styles.shortcutKey.Render(" "+key+" "))
	}
	separator := styles.empty.Render(" or ")
	return pad(strings.Join(keycaps, separator), helpKeyColumnWidth) + styles.modalBody.Render(description)
}

func modalBox(styles *uiStyles, width int, title string, values ...string) []string {
	interior := width - 2
	lines := make([]string, 0, len(values)+2)
	title = " " + title + " "
	topFill := max(width-lipgloss.Width(title)-3, 0)
	lines = append(lines,
		styles.modalBorder.Render("╭─")+
			styles.modalTitle.Render(title)+
			styles.modalBorder.Render(strings.Repeat("─", topFill)+"╮"),
	)
	contentWidth := max(interior-4, 0)
	for _, value := range values {
		content := pad(truncate(value, contentWidth), contentWidth)
		lines = append(lines, styles.modalBorder.Render("│")+"  "+content+"  "+styles.modalBorder.Render("│"))
	}
	return append(lines, styles.modalBorder.Render("╰"+strings.Repeat("─", interior)+"╯"))
}

// paneSeparators returns the divider for the first body row, which carries the
// focus arrow, and the one shared by every remaining row.
func (m dashboard) paneSeparators() (string, string) {
	divider := m.styles.divider.Render("│")
	if m.focus == leftPane {
		return m.styles.dividerActive.Render("◀") + divider + " ", " " + divider + " "
	}
	return " " + divider + m.styles.dividerActive.Render("▶"), " " + divider + " "
}

// renderNavigation windows the tree around the cursor so the selected item stays
// visible when the item count exceeds the pane height.
func (m dashboard) renderNavigation(width, height int) []string {
	titleStyle := m.styles.paneTitle
	if m.focus == leftPane {
		titleStyle = m.styles.paneTitleActive
	}
	title := titleStyle.Render(" romty ")
	header := title + m.styles.tabRail.Render(strings.Repeat("─", max(width-lipgloss.Width(title), 0)))
	lines := []string{header, ""}
	items := m.navigationItems()
	capacity := max(height-len(lines), 1)
	start := 0
	if len(items) > capacity {
		start = min(max(m.navIndex-capacity/2, 0), len(items)-capacity)
	}
	for index := start; index < len(items) && index-start < capacity; index++ {
		lines = append(lines, m.renderNavigationItem(items[index], index, width))
	}
	if len(items) == 0 {
		lines = append(lines,
			m.styles.empty.Render("  No roots"),
			m.styles.empty.Render("  Press F2 to add one"),
		)
	}
	return lines
}

func (m dashboard) renderNavigationItem(item navItem, index, width int) string {
	isCurrent := item.workspace.Path == m.selectedPath
	isSelected := m.focus == leftPane && index == m.navIndex
	indicator := " "
	if isCurrent {
		indicator = "▎"
	}
	if isSelected {
		indicator = "▌"
	}
	branch := "├─"
	if item.lastChild {
		branch = "└─"
	}
	name := indicator + " " + branch + " " + item.workspace.Name
	style := m.styles.navigationItem
	if item.isRoot {
		name = indicator + " ▾ " + item.root.Name
		style = m.styles.navigationRoot
		if item.failure != "" {
			name = indicator + " ✗ " + item.root.Name
			style = m.styles.errorText
		}
	}
	if isCurrent {
		style = m.styles.navigationCurrent
	}
	if isSelected {
		style = m.styles.navigationSelected
	}
	markers := openTabMarkers(item.tabs)
	if markers != "" {
		available := width - lipgloss.Width(markers) - 2
		if available > 0 {
			name = truncate(name, available)
			name += strings.Repeat(" ", max(width-lipgloss.Width(name)-lipgloss.Width(markers), 2)) + markers
		}
	}
	return style.Render(pad(truncate(name, width), width))
}

func (m dashboard) renderTerminal(width int) []string {
	tabs := m.selectedTabs()
	if m.focus == leftPane {
		tabs = m.navigationTabs()
	}
	lines := renderTabBar(m.styles, tabs, m.tabIndex, width)
	if m.terminal != nil {
		return append(lines, m.terminal.renderViewport(m.scrollOffset)...)
	}
	if _, ok := m.navigationItem(); m.focus == leftPane && !ok {
		return append(lines, m.styles.empty.Render("  Select a root or workspace"))
	}
	if len(tabs) == 0 {
		return append(lines, m.styles.empty.Render("  Select + and press Enter to create a terminal"))
	}
	return append(lines, m.styles.empty.Render("  Select a tab and press Enter"))
}

func renderTabBar(styles *uiStyles, tabs []model.Tab, active, width int) []string {
	labels := make([]string, 0, len(tabs)+1)
	for _, tab := range tabs {
		labels = append(labels, " "+tab.Name+" ")
	}
	labels = append(labels, " + ")

	var tabsLine strings.Builder
	var railLine strings.Builder
	for index, label := range labels {
		if index > 0 {
			tabsLine.WriteString(" ")
			railLine.WriteString(styles.tabRail.Render("─"))
		}
		style := styles.tab
		railStyle := styles.tabRail
		railCharacter := "─"
		if index == active {
			style = styles.tabSelected
			railStyle = styles.tabRailSelected
			railCharacter = "━"
		}
		tabsLine.WriteString(style.Render(label))
		railLine.WriteString(railStyle.Render(strings.Repeat(railCharacter, lipgloss.Width(label))))
	}
	remaining := width - lipgloss.Width(railLine.String())
	if remaining > 0 {
		railLine.WriteString(styles.tabRail.Render(strings.Repeat("─", remaining)))
	}
	return []string{truncate(tabsLine.String(), width), truncate(railLine.String(), width)}
}

func renderShortcuts(styles *uiStyles, width int, values ...shortcut) string {
	segments := make([]string, 0, len(values))
	for _, value := range values {
		segments = append(segments,
			styles.shortcutKey.Render(" "+value.key+" ")+" "+styles.shortcutDescription.Render(value.description),
		)
	}
	return truncate(strings.Join(segments, "  "), width)
}

func renderShortcutRail(styles *uiStyles, width int, values ...shortcut) string {
	shortcuts := renderShortcuts(styles, width, values...)
	fill := max(width-lipgloss.Width(shortcuts)-1, 0)
	if fill == 0 {
		return shortcuts
	}
	return styles.tabRail.Render(strings.Repeat("─", fill)) + " " + shortcuts
}

func truncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	return ansi.Truncate(value, width, "…")
}

func pad(value string, width int) string {
	missing := width - lipgloss.Width(value)
	if missing <= 0 {
		return value
	}
	return value + strings.Repeat(" ", missing)
}
