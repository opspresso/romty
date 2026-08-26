package ui

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/agenthooks"
	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/version"
)

const (
	// tagline is what About says romty is, and what help repeats now that
	// About has no function key. Two copies of one sentence drift apart.
	tagline = "Persistent terminal workspace manager"

	terminalTop        = 2
	helpKeyColumnWidth = 34
	// separatorWidth is the width of what paneSeparators draws between the
	// panes. The terminal's origin, the right pane's width and the mouse
	// translation all depend on it agreeing with what is rendered.
	separatorWidth = 3

	maximumReattachAttempts = 3
	initialReattachBackoff  = 250 * time.Millisecond
	maximumReattachBackoff  = 2 * time.Second
	agentRefreshInterval    = 2 * time.Second
	agentAnimationInterval  = 120 * time.Millisecond
	gitRefreshInterval      = 10 * time.Second
	gitFetchInterval        = 5 * time.Minute
	// healthyAttachInterval is how long a terminal has to stay attached before
	// a later drop counts as a fresh incident rather than another turn of the
	// loop. What the backoff damps — replay, fall behind, get cut — turns over
	// in well under a second, so a terminal that lasted this long was not in it.
	healthyAttachInterval = 10 * time.Second
)

// now is a variable so tests need not wait out healthyAttachInterval, the way
// the daemon's request timeout is one so they need not wait out a handshake.
var now = time.Now

var agentAnimationFrames = [...]string{"◐", "◓", "◑", "◒"}

type Backend interface {
	AddRoot(path string) (model.Snapshot, error)
	Snapshot() (model.Snapshot, error)
	AgentStatuses() (map[string]model.AgentStatus, error)
	RemoveRoot(rootID string) (model.Snapshot, error)
	RemoveWorkspace(rootID, path string) (model.Snapshot, error)
	EnsureWorkspace(rootID, path string) (model.Workspace, error)
	CreateTab(workspaceID string, columns, rows uint16) (model.Tab, error)
	OpenTerminal(tabID string) (io.ReadWriteCloser, []byte, error)
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
	// gitError is about an explicit Git action and is cleared only by another
	// Git action, not by a workspace or terminal refresh.
	gitError
)

type modal int

const (
	noModal modal = iota
	aboutModal
	helpModal
	configModal
	browseModal
	gitActionsModal
	removeSelectionModal
	shutdownModal
	hookInstallModal
)

type navItem struct {
	root      model.Root
	workspace model.Workspace
	tabs      []model.Tab
	git       gitState
	hasGit    bool
	isRoot    bool
	separator bool
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
	// snapshotOrder breaks ties between snapshots of the same daemon state,
	// such as two directory refreshes that complete in reverse order.
	snapshotOrder    uint64
	snapshotApplied  uint64
	selectionRequest uint64
	tabPending       bool

	width    int
	height   int
	focus    pane
	navIndex int
	// cursorPath is what the cursor is actually on. navIndex is only where
	// that lands in the tree as it stands, and the tree is rebuilt on every
	// refresh.
	cursorPath              string
	tabIndex                int
	selectedWorkspaceID     string
	selectedPath            string
	rememberedWorkspacePath string
	rememberedTabID         string
	restorePending          bool
	inputMode               bool
	input                   string
	errorMessage            string
	errorFrom               errorSource
	noticeMessage           bool
	terminal                *embeddedTerminal
	modal                   modal
	shutdownPending         bool
	hookStatuses            []agenthooks.Status
	hookInstallPending      bool
	agentAnimationFrame     int
	agentAnimationActive    bool
	agentAnimationPending   bool
	// removeTarget is the item the confirmation modal is asking about, held so
	// the answer applies to the item the question named.
	removeTarget   navItem
	terminalExited bool
	// reattachTab and reattachAttempts damp the loop a dropped connection
	// used to start: romty reattached at once, the daemon replayed the whole
	// recording, the client fell behind, and the daemon cut it off again.
	reattachTab      string
	reattachAttempts int
	// terminalOpenedAt is when the open terminal attached, which is what tells
	// a drop that continues a loop from one that starts a new incident.
	terminalOpenedAt time.Time
	scrollback       bool
	// altScroll is the host's alternate scroll as romty last set it, so the
	// sequence is sent on the transitions and not on every message.
	altScroll    bool
	scrollOffset int
	helpOffset   int
	configPath   string
	// homePath is where the root picker opens, resolved once at startup.
	homePath string
	// browse is the root picker's state, kept on the dashboard so the modal
	// renders from it and the keys move it.
	browse browser
	// config is the document as loaded, kept so saving edits it instead of
	// reconstructing it from fields.
	config            Config
	leftWidth         int
	mousePassthrough  bool
	gitStates         map[string]gitState
	gitFetchedAt      time.Time
	gitActionTarget   model.Workspace
	gitActionIndex    int
	gitAction         gitAction
	gitActionPending  bool
	gitActionComplete bool
	gitActionOutput   string
	gitActionError    string
	gitActionOffset   int
	gitActionCancel   func()
	gitDiff           gitDiffView
	gitDiffSplit      bool
	styles            *uiStyles
}

type snapshotMsg struct {
	value model.Snapshot
	order uint64
	err   error
}

type agentSnapshotMsg struct {
	value map[string]model.AgentStatus
	err   error
}

type agentAnimationMsg struct{}

type gitStatusMsg struct {
	value      map[string]gitState
	fetchedAt  time.Time
	reschedule bool
}

type workspaceMsg struct {
	value     model.Workspace
	snapshot  model.Snapshot
	order     uint64
	selection uint64
	tabID     string
	createTab bool
	err       error
}

type tabMsg struct {
	value     model.Tab
	snapshot  model.Snapshot
	order     uint64
	selection uint64
	err       error
}

type terminalOpenedMsg struct {
	tabID  string
	stream io.ReadWriteCloser
	replay []byte
	err    error
}

type configSavedMsg struct {
	leftWidth         int
	gitDiffView       string
	lastWorkspacePath string
	lastTabID         string
	err               error
}

type daemonStoppedMsg struct {
	err error
}

type hooksInstalledMsg struct {
	results []agenthooks.Result
	err     error
}

// resizeFailedMsg keeps a failed resize out of snapshotMsg, which means "a new
// snapshot arrived" and whose handler clears the status bar. Borrowing it made
// a resize failure erase itself and, worse, made every snapshotMsg handler
// unable to trust message.value.
//
// It names its tab, like every other answer romty waits on. A resize is a
// round trip, so dragging a window and then switching tabs leaves one in
// flight for the tab just left: the daemon answers "running terminal session
// not found" for a terminal nobody is looking at any more, and the status bar
// hung that on whichever terminal had taken its place.
type resizeFailedMsg struct {
	tabID string
	err   error
}

// reopenTerminalMsg arrives after a backoff, so a terminal that keeps dropping
// is retried at a pace that leaves the daemon and the UI usable.
type reopenTerminalMsg struct {
	tabID string
}

var installHookProviders = agenthooks.Install

func Run(backend Backend, initial model.Snapshot, configPath string, hookStatuses []agenthooks.Status) (Result, error) {
	config, err := loadConfig(configPath)
	if err != nil {
		return Result{}, fmt.Errorf("load UI config: %w", err)
	}
	value := newDashboardWithConfig(backend, initial, configPath, config)
	value.offerAgentHooks(hookStatuses)
	program := tea.NewProgram(value)
	final, err := program.Run()
	// romty held the host's alternate scroll off while it ran. Leaving it that
	// way would follow the user out of romty and into every full-screen program
	// they open next, so it goes back on — the state terminals ship with.
	fmt.Print(ansi.SetMode(altScrollMode))
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
		homePath:         userHomeDirectory(),
		config:           config,
		leftWidth:        config.LeftWidth,
		mousePassthrough: config.MousePassthrough,
		gitDiffSplit:     config.GitDiffView == gitDiffViewSplit,
		styles:           newUIStyles(true),
		gitFetchedAt:     now(),
	}
	value.ensureWorkspaceCursor()
	value.restoreSelection()
	value.agentAnimationActive = value.hasAnimatedAgent()
	value.agentAnimationPending = value.agentAnimationActive
	return value
}

func (m dashboard) Init() tea.Cmd {
	// The host starts with alternate scroll on, which is what turns a wheel
	// notch into arrow keys romty cannot tell from typed ones. Only scrollback
	// wants them, so the mode is off until it opens.
	commands := []tea.Cmd{tea.RequestBackgroundColor, m.refreshAgents(), m.initialGitStatus(), altScrollCommand(false)}
	if m.restorePending {
		commands = append(commands, m.openSelectedTerminal())
	}
	if m.agentAnimationPending {
		commands = append(commands, animateAgentMarker())
	}
	return tea.Batch(commands...)
}

// Update wraps the state machine so every path that opens or leaves scrollback
// moves the host's alternate scroll with it, rather than each of the seven
// transitions having to remember the sequence.
func (m dashboard) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	updated, command := m.update(message)
	value, ok := updated.(dashboard)
	if !ok {
		return updated, command
	}
	value, command = value.syncAgentAnimation(command)
	return value.syncAltScroll(command)
}

func (m dashboard) syncAgentAnimation(command tea.Cmd) (dashboard, tea.Cmd) {
	if m.agentAnimationActive && !m.agentAnimationPending {
		m.agentAnimationPending = true
		command = tea.Batch(command, animateAgentMarker())
	}
	return m, command
}

// syncAltScroll asks the host for alternate scroll while scrollback is open and
// gives it up on the way out. Outside scrollback the arrow keys it sends have
// nowhere good to go: over the workspace pane they walk the tree three rows per
// notch, and over the terminal they reach the shell as history keys nobody
// pressed.
func (m dashboard) syncAltScroll(command tea.Cmd) (tea.Model, tea.Cmd) {
	if m.altScroll == m.scrollback {
		return m, command
	}
	m.altScroll = m.scrollback
	return m, tea.Batch(command, altScrollCommand(m.altScroll))
}

// altScrollMode is DEC private mode 1007, which the ansi package has no
// constant for. A terminal that does not implement it ignores both sequences,
// leaving the wheel exactly as it behaves today.
const altScrollMode = ansi.DECMode(1007)

func altScrollCommand(enabled bool) tea.Cmd {
	if enabled {
		return tea.Raw(ansi.SetMode(altScrollMode))
	}
	return tea.Raw(ansi.ResetMode(altScrollMode))
}

func (m dashboard) update(message tea.Msg) (tea.Model, tea.Cmd) {
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
		if m.modal == helpModal {
			return m.handleHelpMouse(message.(tea.MouseMsg))
		}
		if m.gitDiff.active {
			return m.handleGitDiffMouse(message.(tea.MouseMsg))
		}
		mouse := message.(tea.MouseMsg)
		if wheel, ok := message.(tea.MouseWheelMsg); ok && !m.guestOwnsMouse() {
			return m.handleTerminalWheel(wheel)
		}
		if m.guestOwnsMouse() {
			return m.forwardMouse(mouse)
		}
		return m, nil
	case tea.PasteMsg:
		if m.inputMode {
			m.input += message.Content
		} else if m.scrollback && m.terminal != nil {
			m.stopScrollback()
			m.terminal.paste(message.Content)
		} else if !m.gitDiff.active && m.focus == terminalPane && m.terminal != nil {
			m.terminal.paste(message.Content)
		}
		return m, nil
	case snapshotMsg:
		order := message.order
		if order == 0 {
			order = m.snapshotOrder
		}
		if message.err != nil {
			if order < m.snapshotApplied {
				return m, nil
			}
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
		if !m.applySnapshot(order, message.value) {
			return m, nil
		}
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
	case agentSnapshotMsg:
		if message.err == nil {
			m.updateAgents(message.value)
		}
		return m, m.refreshAgents()
	case agentAnimationMsg:
		m.agentAnimationPending = false
		if m.agentAnimationActive {
			m.agentAnimationFrame = (m.agentAnimationFrame + 1) % len(agentAnimationFrames)
		}
		return m, nil
	case gitStatusMsg:
		m.gitStates = message.value
		if message.fetchedAt.After(m.gitFetchedAt) {
			m.gitFetchedAt = message.fetchedAt
		}
		if message.reschedule {
			return m, m.refreshGitStatus()
		}
		return m, nil
	case gitActionMsg:
		return m.handleGitActionResult(message)
	case gitChangedFilesMsg:
		return m.handleGitChangedFiles(message)
	case gitFileDiffMsg:
		return m.handleGitFileDiff(message)
	case workspaceMsg:
		return m.handleWorkspace(message)
	case tabMsg:
		return m.handleCreatedTab(message)
	case terminalOpenedMsg:
		return m.handleOpenedTerminal(message)
	case terminalOutputMsg:
		return m.handleTerminalOutput(message)
	case configSavedMsg:
		// What the save reported comes first. Holding an arrow key in the
		// config modal outruns the disk, and reading the width before the
		// error meant a save that failed under a width that had already moved
		// on was dropped without a word — the one outcome the user has to
		// hear about.
		if message.err != nil {
			m.setError(settingError, message.err.Error())
		} else {
			m.clearError(settingError)
		}
		viewChanged := message.gitDiffView != "" && message.gitDiffView != gitDiffViewSetting(m.gitDiffSplit)
		selectionChanged := message.lastWorkspacePath != m.rememberedWorkspacePath ||
			message.lastTabID != m.rememberedTabID
		if message.leftWidth != m.leftWidth || viewChanged || selectionChanged {
			// The width moved while this one was being written, so it is
			// already out of date. A later save answers whatever this one said.
			return m, m.saveConfig()
		}
		return m, m.resizeTerminal()
	case resizeFailedMsg:
		if m.terminal == nil || m.terminal.id != message.tabID {
			// The terminal it was sent for is gone, and the one on screen
			// resizes on its own.
			return m, nil
		}
		// The emulator has already taken the new size, so it and the PTY now
		// disagree until the next successful resize.
		m.setError(terminalError, "resize terminal: "+message.err.Error())
		return m, nil
	case browserMsg:
		return m.handleBrowserRead(message)
	case reopenTerminalMsg:
		if message.tabID != m.selectedTabID() {
			return m, nil
		}
		return m, m.openSelectedTerminal()
	case daemonStoppedMsg:
		if message.err != nil {
			m.modal = noModal
			m.shutdownPending = false
			m.setError(settingError, "stop daemon: "+message.err.Error())
			return m, nil
		}
		return m.quit()
	case hooksInstalledMsg:
		m.hookInstallPending = false
		m.hookStatuses = nil
		m.modal = noModal
		if message.err != nil {
			m.setError(settingError, "install agent hooks: "+message.err.Error())
			return m, nil
		}
		m.setNotice(settingError, installedHooksNotice(message.results))
		return m, nil
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
	"f2": func(m dashboard) (tea.Model, tea.Cmd) { return m.startBrowse() },
	"f3": func(m dashboard) (tea.Model, tea.Cmd) { return m.openModal(configModal) },
	"f4": func(m dashboard) (tea.Model, tea.Cmd) { return m.quit() },
	"f5": func(m dashboard) (tea.Model, tea.Cmd) { return m.refreshAll() },
	"f6": func(m dashboard) (tea.Model, tea.Cmd) { return m.toggleScrollback() },
	"f7": func(m dashboard) (tea.Model, tea.Cmd) { return m.toggleFocus() },
	// F8 and F9 are deliberately absent. They belong to the workspace pane
	// only, so romty keeps taking exactly F1-F7 from the shell and no more.
	// A full-screen program binds the whole row — htop puts Kill on F9 and
	// answers it with Enter, which is the same Enter that confirms stopping
	// the daemon. Intercepting F9 would turn that habit into every session
	// dying at once.
	//
	// Shift+PgUp reaches the history in one press by entering scrollback itself.
	"shift+pgup":   func(m dashboard) (tea.Model, tea.Cmd) { return m.pageHistory(1) },
	"shift+pgdown": func(m dashboard) (tea.Model, tea.Cmd) { return m.pageHistory(-1) },
	"ctrl+shift+\\": func(m dashboard) (tea.Model, tea.Cmd) {
		return m.toggleScrollback()
	},
	"ctrl+shift+t": func(m dashboard) (tea.Model, tea.Cmd) { return m.newTab() },
	"ctrl+shift+g": func(m dashboard) (tea.Model, tea.Cmd) { return m.openGitActions() },
	"ctrl+shift+f": func(m dashboard) (tea.Model, tea.Cmd) { return m.toggleGitDiffView() },
	// Switching tabs from the terminal pane took Ctrl+\ and then two more keys.
	// Ctrl+Shift+Left/Right is the chord a terminal with tabs binds, and a
	// terminal reports it distinctly — Ctrl+Shift+Tab is not, because most
	// terminals send it as a plain Shift+Tab. The unshifted arrows stay with
	// the shell, where word-wise movement lives.
	"ctrl+shift+left":  func(m dashboard) (tea.Model, tea.Cmd) { return m.switchTab(-1) },
	"ctrl+shift+right": func(m dashboard) (tea.Model, tea.Cmd) { return m.switchTab(1) },
	// The other axis of the same chord: Left and Right move along one
	// workspace's terminals, Up and Down move between the workspaces that
	// have any. Reaching a terminal in another workspace took leaving the
	// terminal pane, walking the tree and pressing Enter, which is three
	// keys and a change of pane for the move a user makes most often.
	"ctrl+shift+up":   func(m dashboard) (tea.Model, tea.Cmd) { return m.switchWorkspace(-1) },
	"ctrl+shift+down": func(m dashboard) (tea.Model, tea.Cmd) { return m.switchWorkspace(1) },
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
	if m.hookInstallPending {
		if message.String() == "f4" {
			return m.quit()
		}
		return m, nil
	}
	if m.gitActionPending {
		if message.String() == "f4" {
			return m.quit()
		}
		return m, nil
	}
	if m.modal == hookInstallModal {
		if message.String() == "f4" {
			return m.quit()
		}
		return m.handleModalKey(message)
	}
	if m.gitDiff.active && m.modal != noModal {
		if action, ok := globalKeys[message.String()]; ok {
			return action(m)
		}
		return m.handleModalKey(message)
	}
	if m.gitDiff.active {
		if message.String() == "ctrl+shift+f" {
			return m.toggleGitDiffView()
		}
		return m.handleGitDiffKey(message)
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
	if message.String() == "ctrl+\\" {
		return m.toggleFocus()
	}
	if m.focus == terminalPane {
		if m.terminal != nil {
			m.terminal.sendKey(message)
		}
		return m, nil
	}

	switch message.String() {
	case "ctrl+c":
		return m.quit()
	case "tab":
		m.focusTerminal()
	case "i":
		return m.openModal(aboutModal)
	case ",":
		return m.openModal(configModal)
	case "?":
		return m.openModal(helpModal)
	case "f8":
		return m.confirmRemoveSelection()
	case "f9":
		return m.openModal(shutdownModal)
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
	m.cancelGitAction()
	m.closeTerminal()
	m.result.Quit = true
	return m, tea.Quit
}

// setError takes the status bar for one source; clearError gives it up only if
// that source still holds it.
func (m *dashboard) setError(source errorSource, message string) {
	m.errorMessage = message
	m.errorFrom = source
	m.noticeMessage = false
}

// setNotice takes the status bar the way setError does, for a state romty is
// reporting rather than a failure. Scrollback with nothing to show is not a
// fault of the terminal or of the user, so the bar says NOTE in the muted
// colours instead of raising a red ERROR.
func (m *dashboard) setNotice(source errorSource, message string) {
	m.setError(source, message)
	m.noticeMessage = true
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

func (m *dashboard) offerAgentHooks(statuses []agenthooks.Status) {
	m.hookStatuses = statuses
	if len(agenthooks.Pending(statuses)) > 0 {
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
	return strings.Join(names, " and ") + " hooks installed; restart running agent sessions"
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
		if m.modal == hookInstallModal {
			m.hookStatuses = nil
		}
		m.modal = noModal
		return m, nil
	}
	switch m.modal {
	case browseModal:
		return m.handleBrowseKey(message)
	case gitActionsModal:
		if m.gitActionComplete {
			page := max(modalCapacity(m.dimensions().bodyHeight)-3, 1)
			switch message.String() {
			case "enter":
				return m.resetGitActionResult(), nil
			case "up", "k":
				return m.scrollGitAction(-1)
			case "down", "j":
				return m.scrollGitAction(1)
			case "pgup", "ctrl+b":
				return m.scrollGitAction(-page)
			case "pgdown", "ctrl+f":
				return m.scrollGitAction(page)
			case "home", "g":
				return m.scrollGitAction(-len(m.gitActionResultLines()))
			case "end", "G":
				return m.scrollGitAction(len(m.gitActionResultLines()))
			}
			return m, nil
		}
		switch message.String() {
		case "up", "k":
			m.gitActionIndex = (m.gitActionIndex - 1 + len(gitActionChoices)) % len(gitActionChoices)
		case "down", "j":
			m.gitActionIndex = (m.gitActionIndex + 1) % len(gitActionChoices)
		case "enter":
			return m.startGitAction()
		}
		return m, nil
	case removeSelectionModal:
		if message.String() == "enter" {
			m.modal = noModal
			return m, m.removeSelection()
		}
	case shutdownModal:
		if message.String() == "enter" {
			m.shutdownPending = true
			return m, m.shutdownDaemon()
		}
	case hookInstallModal:
		if message.String() == "enter" {
			m.hookInstallPending = true
			return m, m.installAgentHooks()
		}
	case helpModal:
		page := max(modalCapacity(m.dimensions().bodyHeight)-1, 1)
		switch message.String() {
		case "up", "k":
			return m.scrollHelp(-1)
		case "down", "j":
			return m.scrollHelp(1)
		case "pgup", "ctrl+b":
			return m.scrollHelp(-page)
		case "pgdown", "ctrl+f":
			return m.scrollHelp(page)
		case "home", "g":
			return m.scrollHelp(-len(m.helpEntries()))
		case "end", "G":
			return m.scrollHelp(len(m.helpEntries()))
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

// Scrollback mode is the only state where romty wants the wheel, and it takes
// it as the arrow keys the host's alternate scroll sends rather than by asking
// for mouse events, which would take the host's native drag selection away —
// the very thing scrollback exists to give you.
func (m dashboard) handleScrollbackKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.terminal == nil {
		m.stopScrollback()
		return m, nil
	}
	switch message.String() {
	case "esc":
		m.stopScrollback()
	case "up":
		m.scrollTerminal(1)
	case "down":
		m.scrollTerminal(-1)
	default:
		m.stopScrollback()
		m.terminal.sendKey(message)
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

func (m dashboard) handleTerminalWheel(wheel tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	if m.terminal == nil {
		return m, nil
	}
	if _, inside := m.translateMouse(wheel.Mouse()); !inside && !m.scrollback {
		return m, nil
	}
	switch wheel.Button {
	case tea.MouseWheelUp:
		if m.scrollback || m.startScrollback() {
			m.scrollTerminal(3)
		}
	case tea.MouseWheelDown:
		if m.scrollback {
			m.scrollTerminal(-3)
		}
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

// toggleFocus moves between the panes in one key. Both F7 and Ctrl+\ use it so
// either chord can stand in for one intercepted by a desktop environment.
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
	m.focusTerminal()
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
		m.setNotice(terminalError, m.scrollbackUnavailable())
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
	// terminal rather than back in the workspace tree — including when
	// scrollback was opened from the tree, because the tree is not what was
	// on screen.
	m.focusTerminal()
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

func (m dashboard) handleHelpMouse(message tea.MouseMsg) (tea.Model, tea.Cmd) {
	wheel, ok := message.(tea.MouseWheelMsg)
	if !ok {
		return m, nil
	}
	switch wheel.Button {
	case tea.MouseWheelUp:
		return m.scrollHelp(-3)
	case tea.MouseWheelDown:
		return m.scrollHelp(3)
	}
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
	config.GitDiffView = gitDiffViewSetting(m.gitDiffSplit)
	config.LastWorkspacePath = m.rememberedWorkspacePath
	config.LastTabID = m.rememberedTabID
	return func() tea.Msg {
		return configSavedMsg{
			leftWidth: config.LeftWidth, gitDiffView: config.GitDiffView,
			lastWorkspacePath: config.LastWorkspacePath, lastTabID: config.LastTabID,
			err: saveConfig(path, config),
		}
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
		return m, m.addRoot(path)
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

// addRoot is shared by the typed prompt and the picker: both name a directory
// and both leave the daemon to canonicalise and judge it.
func (m *dashboard) addRoot(path string) tea.Cmd {
	order := m.nextSnapshotOrder()
	backend := m.backend
	return func() tea.Msg {
		value, err := backend.AddRoot(path)
		return snapshotMsg{value: value, order: order, err: err}
	}
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
	if message.selection != 0 && message.selection != m.selectionRequest {
		if message.err == nil {
			m.applySnapshot(message.order, message.snapshot)
		}
		return m, nil
	}
	m.tabPending = false
	if message.err != nil {
		m.setError(terminalError, message.err.Error())
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
		m.setError(terminalError, message.err.Error())
		m.focus = leftPane
		return m, nil
	}
	m.closeTerminal()
	columns, rows := m.terminalSize()
	terminal := newEmbeddedTerminal(message.tabID, message.stream, int(columns), int(rows))
	terminal.writeOutput(message.replay)
	m.terminal = terminal
	m.terminalOpenedAt = now()
	// The scrollback on screen belongs to the terminal that just went away, so
	// a switch made from it lands on the new terminal's live screen.
	m.stopScrollback()
	m.focus = terminalPane
	// A terminal that opened supersedes any complaint about terminals.
	m.clearError(terminalError)
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
// attached long enough that the drop starts a fresh incident. The counter only
// ever went up without it: it measured how many times a tab had dropped rather
// than whether it was dropping now, so four drops spread over an afternoon were
// damped exactly like four in a second and a working terminal was eventually
// left saying it keeps disconnecting.
func (m dashboard) attachWasHealthy() bool {
	return !m.terminalOpenedAt.IsZero() && now().Sub(m.terminalOpenedAt) >= healthyAttachInterval
}

// reattachBackoff doubles with each consecutive retry so a terminal that keeps
// dropping cannot spin, while a one-off drop still reconnects promptly.
func reattachBackoff(attempt int) time.Duration {
	backoff := initialReattachBackoff << (attempt - 1)
	return min(backoff, maximumReattachBackoff)
}

// The item is captured here rather than read again on confirmation: a snapshot
// arriving while the modal is open can move the cursor, and the answer has to
// apply to the item the question named.
func (m dashboard) confirmRemoveSelection() (tea.Model, tea.Cmd) {
	item, ok := m.navigationItem()
	if !ok {
		return m, nil
	}
	m.removeTarget = item
	return m.openModal(removeSelectionModal)
}

func (m *dashboard) removeSelection() tea.Cmd {
	rootID := m.removeTarget.root.ID
	if rootID == "" {
		return nil
	}
	order := m.nextSnapshotOrder()
	backend := m.backend
	target := m.removeTarget
	return func() tea.Msg {
		var value model.Snapshot
		var err error
		if target.isRoot {
			value, err = backend.RemoveRoot(rootID)
		} else {
			value, err = backend.RemoveWorkspace(rootID, target.workspace.Path)
		}
		return snapshotMsg{value: value, order: order, err: err}
	}
}

func (m *dashboard) refresh() tea.Cmd {
	order := m.nextSnapshotOrder()
	backend := m.backend
	return func() tea.Msg {
		value, err := backend.Snapshot()
		return snapshotMsg{value: value, order: order, err: err}
	}
}

func (m dashboard) refreshAll() (tea.Model, tea.Cmd) {
	tree := m.refresh()
	return m, tea.Batch(tree, m.readGitStatus(false, false), m.readGitStatus(true, false))
}

func (m dashboard) refreshAgents() tea.Cmd {
	backend := m.backend
	return tea.Tick(agentRefreshInterval, func(time.Time) tea.Msg {
		value, err := backend.AgentStatuses()
		return agentSnapshotMsg{value: value, err: err}
	})
}

func animateAgentMarker() tea.Cmd {
	return tea.Tick(agentAnimationInterval, func(time.Time) tea.Msg {
		return agentAnimationMsg{}
	})
}

func (m *dashboard) updateAgents(statuses map[string]model.AgentStatus) {
	for rootIndex := range m.state.Roots {
		root := &m.state.Roots[rootIndex]
		for tabIndex := range root.Tabs {
			status := statuses[root.Tabs[tabIndex].ID]
			root.Tabs[tabIndex].Agent = status.Agent
			root.Tabs[tabIndex].AgentPhase = status.Phase
		}
		for workspaceIndex := range root.Directories {
			tabs := root.Directories[workspaceIndex].Tabs
			for tabIndex := range tabs {
				status := statuses[tabs[tabIndex].ID]
				tabs[tabIndex].Agent = status.Agent
				tabs[tabIndex].AgentPhase = status.Phase
			}
		}
	}
	m.agentAnimationActive = m.hasAnimatedAgent()
}

func (m dashboard) hasAnimatedAgent() bool {
	for _, root := range m.state.Roots {
		for _, tab := range root.Tabs {
			if tab.Running && animatedAgentPhase(tab.AgentPhase) {
				return true
			}
		}
		for _, workspace := range root.Directories {
			for _, tab := range workspace.Tabs {
				if tab.Running && animatedAgentPhase(tab.AgentPhase) {
					return true
				}
			}
		}
	}
	return false
}

func animatedAgentPhase(phase model.AgentPhase) bool {
	switch phase {
	case model.AgentPhaseThinking, model.AgentPhaseWorking, model.AgentPhasePlanning,
		model.AgentPhaseCompacting, model.AgentPhaseBackground:
		return true
	default:
		return false
	}
}

func (m dashboard) readGitStatus(forceFetch, reschedule bool) tea.Cmd {
	paths := m.workspacePaths()
	fetch := forceFetch || m.gitFetchedAt.IsZero() || now().Sub(m.gitFetchedAt) >= gitFetchInterval
	fetchedAt := time.Time{}
	if fetch {
		fetchedAt = now()
	}
	return func() tea.Msg {
		return gitStatusMsg{value: gitStates(paths, fetch), fetchedAt: fetchedAt, reschedule: reschedule}
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

func (m *dashboard) selectWorkspace() tea.Cmd {
	if m.tabPending {
		return nil
	}
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
			selection: selection, tabID: tabID, createTab: createTab, err: err}
	}
}

func (m *dashboard) createTab() tea.Cmd {
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
		tab, err := backend.CreateTab(workspaceID, columns, rows)
		if err != nil {
			return tabMsg{selection: selection, err: err}
		}
		snapshot, err := backend.Snapshot()
		return tabMsg{value: tab, snapshot: snapshot, order: order, selection: selection, err: err}
	}
}

func (m *dashboard) nextSnapshotOrder() uint64 {
	m.snapshotOrder++
	return m.snapshotOrder
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

func (m dashboard) resizeTerminal() tea.Cmd {
	if m.terminal == nil {
		return nil
	}
	columns, rows := m.terminalSize()
	m.terminal.resize(int(columns), int(rows))
	terminal := m.terminal
	return func() tea.Msg {
		// The size is read here, not captured above, so a resize that lost the
		// race still tells the daemon what is on screen.
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

func (m *dashboard) moveNavigation(delta int) {
	items := m.navigationItems()
	if len(items) == 0 {
		m.navIndex, m.cursorPath = 0, ""
		m.tabIndex = 0
		return
	}
	// Stopping at the ends rather than wrapping, as the picker, help and
	// scrollback all do; the tree used to be alone in sending a press past the
	// last row back to the first.
	m.navIndex = min(max(m.navIndex+delta, 0), len(items)-1)
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

func (m dashboard) newTab() (tea.Model, tea.Cmd) {
	if m.modal != noModal || m.tabPending {
		return m, nil
	}
	if m.focus == leftPane && !m.scrollback {
		if _, ok := m.navigationItem(); !ok {
			return m, nil
		}
		m.tabIndex = len(m.navigationTabs())
		return m, m.selectWorkspace()
	}
	if m.terminal == nil || m.selectedWorkspaceID == "" {
		return m, nil
	}
	m.tabIndex = len(m.selectedTabs())
	m.tabPending = true
	return m, m.createTab()
}

// switchTab opens the tab one step along the row the user is looking at, which
// is what Left/Right and Enter do together. The row is the one renderTerminal
// draws: the open terminal's tabs everywhere but the workspace pane, where the
// cursor may sit on a workspace other than the open one. Switching along the
// row that is not on screen would open a tab from a workspace the user is not
// looking at.
func (m dashboard) switchTab(delta int) (tea.Model, tea.Cmd) {
	if m.modal != noModal {
		// A modal is a question waiting for an answer, and the switch would
		// land behind it.
		return m, nil
	}
	cursorRow := m.focus == leftPane && !m.scrollback
	tabs := m.selectedTabs()
	if cursorRow {
		tabs = m.navigationTabs()
	}
	next, ok := m.nextTab(tabs, delta)
	if !ok {
		return m, nil
	}
	m.tabIndex = next
	if cursorRow {
		// The workspace under the cursor is not necessarily the open one, so
		// it has to be selected before one of its tabs can be opened.
		return m, m.selectWorkspace()
	}
	return m, m.openSelectedTerminal()
}

// switchWorkspace opens a terminal in the workspace delta steps along the ones
// that have a terminal running.
//
// Only those are stops. A root lists every child directory whether it has ever
// been used or not, so stepping through all of them would make the chord a
// slower way to hold Down — the tree already has plain Up and Down for walking
// everything, and the tab markers are what say where the work is.
func (m dashboard) switchWorkspace(delta int) (tea.Model, tea.Cmd) {
	if m.modal != noModal {
		// A modal is a question waiting for an answer, and the switch would
		// land behind it.
		return m, nil
	}
	items := m.navigationItems()
	// Anchored on the workspace that is open rather than on the cursor, so the
	// chord walks one cycle from either pane and every press lands somewhere
	// new. Anchoring on the cursor would make the key do nothing whenever it
	// pointed at the workspace before the open one. The cursor stands in only
	// when nothing is open for the walk to start from.
	anchor := workspaceIndex(items, m.selectedPath)
	if anchor < 0 {
		anchor = m.navIndex
	}
	next, ok := nextOccupiedWorkspace(items, anchor, delta)
	if !ok {
		return m, nil
	}
	m.setNavigation(next)
	// The first terminal of the workspace being moved to. Which of its tabs to
	// land on is what Left and Right are for.
	m.tabIndex = 0
	return m, m.selectWorkspace()
}

// nextOccupiedWorkspace is the workspace with a terminal running that lies one
// step from anchor in the direction of delta, wrapping at both ends. The
// anchor need not be one itself: a cursor sitting on an empty directory is
// between two stops, and stepping from there lands on the nearer one.
//
// It reports false when there is nowhere to go — no workspace has a terminal,
// or the only one that does is where the anchor already sits, and reopening
// that would tear a live terminal down and attach a new one in its place.
func nextOccupiedWorkspace(items []navItem, anchor, delta int) (int, bool) {
	occupied := make([]int, 0, len(items))
	for index, item := range items {
		if len(runningTabs(item.tabs)) > 0 {
			occupied = append(occupied, index)
		}
	}
	if len(occupied) == 0 {
		return 0, false
	}
	if delta >= 0 {
		for _, index := range occupied {
			if index > anchor {
				return index, true
			}
		}
		first := occupied[0]
		return first, first != anchor
	}
	for position := len(occupied) - 1; position >= 0; position-- {
		if occupied[position] < anchor {
			return occupied[position], true
		}
	}
	last := occupied[len(occupied)-1]
	return last, last != anchor
}

// workspaceIndex is where the workspace at path sits in the tree, or -1 when
// it is not in it: a root that was forgotten takes its workspaces with it, and
// nothing is open before the first terminal is.
func workspaceIndex(items []navItem, path string) int {
	if path == "" {
		return -1
	}
	for index, item := range items {
		if item.workspace.Path == path {
			return index
		}
	}
	return -1
}

// nextTab is the tab delta steps along tabs, wrapping at both ends and never
// landing on the new-tab slot: a key that switches tabs must not create one. It
// reports false when there is nothing to switch to, which includes the tab that
// is already open — reopening it would tear a live terminal down and attach a
// new one in its place.
func (m dashboard) nextTab(tabs []model.Tab, delta int) (int, bool) {
	if len(tabs) == 0 {
		return 0, false
	}
	current := m.tabIndex
	if current >= len(tabs) {
		// The new-tab slot sits past the last tab, so stepping right from it
		// wraps to the first tab and stepping left lands on the last.
		current = len(tabs)
		if delta > 0 {
			current = -1
		}
	}
	next := (current + delta + len(tabs)) % len(tabs)
	if m.terminal != nil && m.terminal.id == tabs[next].ID {
		return 0, false
	}
	return next, true
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

// focusTerminal moves the keyboard into the terminal, if there is one still
// running to move into. A terminal whose shell has exited is not somewhere the
// keyboard can go, so the workspace pane keeps it.
func (m *dashboard) focusTerminal() {
	if m.terminal == nil {
		return
	}
	m.focus = terminalPane
	m.syncTabCursor(m.selectedTabs())
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

func (m *dashboard) restoreSelection() {
	path, tabID := m.config.LastWorkspacePath, m.config.LastTabID
	if path == "" || tabID == "" {
		return
	}
	for navIndex, item := range m.navigationItems() {
		if item.workspace.Path != path {
			continue
		}
		for tabIndex, tab := range runningTabs(item.tabs) {
			if tab.ID != tabID {
				continue
			}
			m.navIndex = navIndex
			m.cursorPath = path
			m.tabIndex = tabIndex
			m.selectedWorkspaceID = tab.WorkspaceID
			m.selectedPath = path
			m.rememberedWorkspacePath = path
			m.rememberedTabID = tabID
			m.restorePending = true
			return
		}
	}
}

func (m *dashboard) rememberSelection(tabID string) bool {
	for _, item := range m.navigationItems() {
		if item.workspace.Path != m.selectedPath {
			continue
		}
		for _, tab := range runningTabs(item.tabs) {
			if tab.ID != tabID {
				continue
			}
			changed := m.rememberedWorkspacePath != item.workspace.Path || m.rememberedTabID != tab.ID
			m.rememberedWorkspacePath = item.workspace.Path
			m.rememberedTabID = tab.ID
			return changed
		}
	}
	return false
}

func (m dashboard) navigationItems() []navItem {
	result := make([]navItem, 0)
	for rootIndex, root := range m.state.Roots {
		result = append(result, navItem{
			root: root.Root,
			workspace: model.Workspace{
				RootID: root.Root.ID,
				Name:   root.Root.Name,
				Path:   root.Root.Path,
			},
			tabs:      root.Tabs,
			isRoot:    true,
			separator: rootIndex > 0,
			failure:   root.Error,
		})
		for _, directory := range root.Directories {
			directoryGit, directoryHasGit := m.gitStates[directory.Workspace.Path]
			result = append(result, navItem{
				root:      root.Root,
				workspace: directory.Workspace,
				tabs:      directory.Tabs,
				git:       directoryGit,
				hasGit:    directoryHasGit,
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

func openTabMarkers(styles *uiStyles, base lipgloss.Style, tabs []model.Tab, animationFrame int) string {
	var result strings.Builder
	for _, tab := range tabs {
		if !tab.Running {
			continue
		}
		style := base
		switch tab.Agent {
		case model.AgentClaude:
			style = style.Foreground(styles.agentClaude.GetForeground())
		case model.AgentCodex:
			style = style.Foreground(styles.agentCodex.GetForeground())
		}
		marker := "●"
		switch tab.AgentPhase {
		case model.AgentPhaseThinking, model.AgentPhaseWorking, model.AgentPhasePlanning,
			model.AgentPhaseCompacting, model.AgentPhaseBackground:
			marker = agentAnimationFrames[animationFrame%len(agentAnimationFrames)]
		case model.AgentPhaseIdle:
			marker = "○"
		case model.AgentPhaseWaitingInput:
			marker = "▲"
		case model.AgentPhaseWaitingApproval:
			marker = "■"
		case model.AgentPhaseError:
			marker = "★"
		}
		result.WriteString(style.Render(marker))
	}
	return result.String()
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

// selectedTabID names the tab the cursor is on among the open workspace's
// tabs, and is empty on the new-tab slot. It reads exactly what
// openSelectedTerminal reads, so it answers "is this still the tab romty asked
// for" rather than approximating it.
func (m dashboard) selectedTabID() string {
	tabs := m.selectedTabs()
	if m.tabIndex < 0 || m.tabIndex >= len(tabs) {
		return ""
	}
	return tabs[m.tabIndex].ID
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
	view.KeyboardEnhancements.ReportAlternateKeys = true
	if !m.gitDiff.active && !m.scrollback && m.focus == terminalPane && m.terminal != nil {
		originX, originY := m.dimensions().terminalOrigin()
		position := m.terminal.cursorPosition()
		view.Cursor = tea.NewCursor(originX+position.X, originY+position.Y)
	}
	return view
}

// mouseMode claims the live terminal's wheel so it can enter scrollback. Copy
// mode gives the mouse back to the host for native selection and takes its
// wheel through alternate scroll instead. A guest application that asked for
// the mouse takes precedence only when the user opted into passthrough.
func (m dashboard) mouseMode() tea.MouseMode {
	if m.modal == helpModal {
		return tea.MouseModeCellMotion
	}
	if m.gitDiff.active {
		return tea.MouseModeCellMotion
	}
	if m.scrollback || m.terminal == nil {
		return tea.MouseModeNone
	}
	if m.guestOwnsMouse() {
		return m.terminal.guestMouseMode()
	}
	return tea.MouseModeCellMotion
}

func (m dashboard) guestOwnsMouse() bool {
	return m.mousePassthrough && m.terminal != nil && m.terminal.guestMouseMode() != tea.MouseModeNone
}

func (m dashboard) render() string {
	view := m.dimensions()
	width := max(m.width, 40)
	// Scrollback replaces the split rather than drawing over it, so the panes
	// it hides are not built at all: rendering both meant every frame drew the
	// workspace tree and a second terminal viewport it then threw away.
	var lines []string
	if m.gitDiff.active {
		lines = m.renderGitDiffPanes(view.leftWidth, view.rightWidth, view.bodyHeight)
	} else if m.scrollback {
		lines = m.renderRows(m.renderTerminal(width), width, view.bodyHeight)
	} else {
		lines = m.renderPanes(view.leftWidth, view.rightWidth, view.bodyHeight)
	}
	if m.modal != noModal {
		lines = m.overlayModal(width, view.bodyHeight)
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
			m.styles.promptLabel.Render(" ROOT ")+" "+m.styles.promptText.Render(displayText(m.input))+m.styles.dividerActive.Render("█"),
			width,
		)
	case m.errorMessage != "":
		label, text, title := m.styles.errorLabel, m.styles.errorText, " ERROR "
		if m.noticeMessage {
			label, text, title = m.styles.noticeLabel, m.styles.noticeText, " NOTE "
		}
		status = truncate(label.Render(title)+" "+text.Render(displayText(m.errorMessage)), width)
	case m.modal == helpModal:
		shortcuts := []shortcut{{key: "Esc", description: "close"}}
		if m.maximumHelpOffset(bodyHeight) > 0 {
			shortcuts = append([]shortcut{{key: "↑/↓/Wheel", description: "scroll"}}, shortcuts...)
		}
		status = renderShortcuts(m.styles, width, shortcuts...)
	case m.modal == aboutModal:
		status = renderShortcuts(m.styles, width, shortcut{key: "Esc", description: "close"})
	case m.modal == configModal:
		status = renderShortcuts(m.styles, width,
			shortcut{key: "←/→", description: "adjust width"},
			shortcut{key: "Esc", description: "close"},
		)
	case m.modal == gitActionsModal && m.gitActionPending:
		status = truncate(
			m.styles.promptLabel.Render(" RUNNING ")+" "+m.styles.shortcutDescription.Render(m.gitAction.label()+" in "+displayText(m.gitActionTarget.Name)),
			width,
		)
	case m.modal == gitActionsModal && m.gitActionComplete:
		shortcuts := []shortcut{
			{key: "Enter", description: "actions"},
			{key: "Esc", description: "close"},
		}
		if m.maximumGitActionOffset(bodyHeight) > 0 {
			shortcuts = append([]shortcut{{key: "↑/↓", description: "scroll"}}, shortcuts...)
		}
		status = renderShortcuts(m.styles, width, shortcuts...)
	case m.modal == gitActionsModal:
		status = renderShortcuts(m.styles, width,
			shortcut{key: "↑/↓", description: "select"},
			shortcut{key: "Enter", description: "run"},
			shortcut{key: "Esc", description: "cancel"},
		)
	case m.modal == shutdownModal && m.shutdownPending:
		// The request is already out; no key can take it back.
		status = truncate(
			m.styles.promptLabel.Render(" STOPPING ")+" "+m.styles.shortcutDescription.Render("waiting for the daemon to stop"),
			width,
		)
	case m.modal == browseModal:
		status = renderShortcuts(m.styles, width,
			shortcut{key: "↑/↓", description: "select"},
			shortcut{key: "→", description: "open"},
			shortcut{key: "←", description: "up"},
			shortcut{key: "Enter", description: "add root"},
			shortcut{key: "/", description: "type a path"},
			shortcut{key: "Esc", description: "cancel"},
		)
	case m.modal == removeSelectionModal:
		action := "forget root"
		if !m.removeTarget.isRoot {
			action = "delete workspace"
		}
		status = renderShortcuts(m.styles, width,
			shortcut{key: "Enter", description: action},
			shortcut{key: "Esc", description: "cancel"},
		)
	case m.modal == shutdownModal:
		status = renderShortcuts(m.styles, width,
			shortcut{key: "Enter", description: "stop daemon"},
			shortcut{key: "Esc", description: "cancel"},
		)
	case m.modal == hookInstallModal && m.hookInstallPending:
		status = truncate(
			m.styles.promptLabel.Render(" INSTALLING ")+" "+m.styles.shortcutDescription.Render("updating agent status hooks"),
			width,
		)
	case m.modal == hookInstallModal:
		status = renderShortcuts(m.styles, width,
			shortcut{key: "Enter", description: "install hooks"},
			shortcut{key: "Esc", description: "skip"},
		)
	case m.gitDiff.active:
		rail = renderShortcutRail(m.styles, width,
			shortcut{key: "Ctrl+Shift+F", description: "close file view"},
		)
		status = renderShortcuts(m.styles, width,
			shortcut{key: "↑/↓", description: "file"},
			shortcut{key: "F6", description: "layout"},
			shortcut{key: "Ctrl+↑/↓", description: "line"},
			shortcut{key: "PgUp/PgDn", description: "diff"},
			shortcut{key: "Home/End", description: "first/last"},
			shortcut{key: "F5", description: "refresh"},
			shortcut{key: "Esc", description: "close"},
		)
	case m.scrollback:
		status = truncate(
			m.styles.promptLabel.Render(" SCROLLBACK ")+" "+
				m.styles.shortcutDescription.Render(m.scrollbackPosition())+"  "+
				renderShortcuts(m.styles, width,
					shortcut{key: "↑/↓", description: "line"},
					shortcut{key: "PgUp/PgDn", description: "page"},
					shortcut{key: "Ctrl+Shift+\\", description: "exit"},
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
			// F7 is already in the status row below, so naming it here too
			// stacked one keycap over itself under two different labels. The
			// rail carries what the row cannot.
			contextShortcuts = []shortcut{{key: "Ctrl+\\", description: "navigation"}}
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

// overlayModal replaces the body with a blank backdrop before centering the
// modal, so workspace and terminal content cannot show around it.
func (m dashboard) overlayModal(width, height int) []string {
	modalLines := m.renderModal(width, height)
	lines := make([]string, height)
	for row := range lines {
		lines[row] = strings.Repeat(" ", width)
	}
	top := max((height-len(modalLines))/2, 0)
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
	modalWidth := min(max(width-4, 32), 72)
	if m.modal == helpModal {
		modalWidth = min(max(width-4, 32), 80)
		return m.renderHelpModal(modalWidth, height)
	}
	if m.modal == browseModal {
		// Paths are long, so the picker gets the wider box help uses.
		return m.renderBrowseModal(min(max(width-4, 32), 80), height)
	}
	if m.modal == gitActionsModal {
		return m.renderGitActionsModal(modalWidth, height)
	}
	if m.modal == removeSelectionModal {
		if m.removeTarget.isRoot {
			return modalBox(m.styles, modalWidth, "Forget root",
				"",
				m.styles.modalStrong.Render("Forget "+displayText(m.removeTarget.root.Name)+"?"),
				"",
				m.styles.modalBody.Render("The directory stays on disk."),
				m.styles.errorText.Render("Its running shells will be terminated."),
				"",
			)
		}
		return modalBox(m.styles, modalWidth, "Delete workspace",
			"",
			m.styles.modalStrong.Render("Delete "+displayText(m.removeTarget.workspace.Name)+"?"),
			"",
			m.styles.errorText.Render("This permanently deletes all contents."),
			m.styles.errorText.Render("Its running shells will be terminated."),
			m.styles.modalBody.Render(displayText(m.removeTarget.workspace.Path)),
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
	if m.modal == hookInstallModal {
		lines := []string{
			"",
			m.styles.modalStrong.Render("Install or update agent status hooks?"),
			"",
		}
		for _, status := range m.hookStatuses {
			var action string
			switch status.State {
			case agenthooks.StateMissing:
				action = "install"
			case agenthooks.StateOutdated:
				action = "update"
			case agenthooks.StateInvalid:
				action = "invalid settings"
			default:
				continue
			}
			lines = append(lines, m.styles.modalBody.Render(status.Provider.DisplayName()+": "+action))
		}
		lines = append(lines, "", m.styles.modalBody.Render("Existing settings and other hooks are preserved."), "")
		return modalBox(m.styles, modalWidth, "Agent hooks", lines...)
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
		m.styles.modalStrong.Render("romty")+"  "+m.styles.empty.Render(version.String()),
		m.styles.modalBody.Render(tagline),
		"",
	)
}

func (m dashboard) helpEntries() []string {
	return []string{
		m.styles.modalStrong.Render("romty") + m.styles.empty.Render("  "+version.String()) +
			m.styles.modalBody.Render("  "+tagline),
		renderHelpSection(m.styles, "GLOBAL", "function keys both panes; other keys contextual"),
		renderHelpShortcut(m.styles, "Help", "F1", "?"),
		renderHelpShortcut(m.styles, "Add root", "F2"),
		renderHelpShortcut(m.styles, "Config", "F3", ","),
		renderHelpShortcut(m.styles, "Quit", "F4", "Ctrl+C"),
		renderHelpShortcut(m.styles, "Refresh workspaces/files", "F5"),
		renderHelpShortcut(m.styles, "Toggle scrollback", "F6", "Ctrl+Shift+\\"),
		renderHelpShortcut(m.styles, "Toggle pane focus", "F7", "Ctrl+\\"),
		renderHelpSection(m.styles, "WORKSPACE", "workspace pane only"),
		renderHelpShortcut(m.styles, "Remove selection", "F8"),
		renderHelpShortcut(m.styles, "Stop daemon", "F9"),
		renderHelpShortcut(m.styles, "About", "i"),
		renderHelpShortcut(m.styles, "Focus terminal", "Tab"),
		renderHelpSection(m.styles, "SWITCH", "workspace and terminal context"),
		renderHelpShortcut(m.styles, "New tab", "Ctrl+Shift+T"),
		renderHelpShortcut(m.styles, "Git actions", "Ctrl+Shift+G"),
		renderHelpShortcut(m.styles, "Toggle file view", "Ctrl+Shift+F"),
		renderHelpShortcut(m.styles, "Switch tab", "Ctrl+Shift+←/→"),
		renderHelpShortcut(m.styles, "Switch workspace", "Ctrl+Shift+↑/↓"),
		renderHelpSection(m.styles, "MOVE", "lists, output and file view"),
		renderHelpShortcut(m.styles, "Move one item / line", "↑/↓", "k/j"),
		renderHelpShortcut(m.styles, "Tab; picker child/parent", "←/→", "h/l"),
		renderHelpShortcut(m.styles, "Previous / next page", "PgUp/PgDn", "Ctrl+B/F"),
		renderHelpShortcut(m.styles, "First / last item/line", "Home/End", "g/G"),
		renderHelpShortcut(m.styles, "Enter / page scrollback", "Shift+PgUp/PgDn"),
		renderHelpShortcut(m.styles, "Scroll Help/history/diff", "Wheel"),
		renderHelpSection(m.styles, "FILE DIFF", "changed file tree and diff"),
		renderHelpShortcut(m.styles, "Toggle diff layout", "F6"),
		renderHelpShortcut(m.styles, "Scroll diff one line", "Ctrl+↑/↓"),
		renderHelpSection(m.styles, "CONTEXT", "workspace, picker, modals and prompts"),
		renderHelpShortcut(m.styles, "Activate / submit", "Enter"),
		renderHelpShortcut(m.styles, "Close / cancel / leave", "Esc"),
		renderHelpShortcut(m.styles, "Type a picker path", "/"),
		renderHelpShortcut(m.styles, "Erase path character", "Backspace"),
		renderHelpShortcut(m.styles, "Adjust pane width", "←/→", "[/]"),
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
	available := max(height-len(lines), 0)
	start, end := navigationWindow(items, m.navIndex, available)
	for index := start; index < end; index++ {
		itemLines := m.renderNavigationItem(items[index], index, width)
		remaining := max(height-len(lines), 0)
		lines = append(lines, itemLines[:min(len(itemLines), remaining)]...)
	}
	if len(items) == 0 {
		lines = append(lines,
			m.styles.empty.Render("  No roots"),
			m.styles.empty.Render("  Press F2 to add one"),
		)
	}
	return lines
}

func navigationWindow(items []navItem, cursor, available int) (int, int) {
	if len(items) == 0 || available <= 0 {
		return 0, 0
	}
	cursor = min(max(cursor, 0), len(items)-1)
	start := cursor
	used := navigationRows(items[cursor])
	for start > 0 && used+navigationRows(items[start-1]) <= available/2 {
		start--
		used += navigationRows(items[start])
	}
	end := start
	used = 0
	for end < len(items) && used+navigationRows(items[end]) <= available {
		used += navigationRows(items[end])
		end++
	}
	for end == len(items) && start > 0 && used+navigationRows(items[start-1]) <= available {
		start--
		used += navigationRows(items[start])
	}
	if end == start {
		end++
	}
	return start, end
}

func navigationRows(item navItem) int {
	if item.isRoot {
		if item.separator {
			return 2
		}
		return 1
	}
	return 2
}

func (m dashboard) renderNavigationItem(item navItem, index, width int) []string {
	isCurrent := item.workspace.Path == m.selectedPath
	isSelected := m.focus == leftPane && index == m.navIndex
	indicator := " "
	if isCurrent {
		indicator = "▎"
	}
	if isSelected {
		indicator = "▌"
	}
	branch := "-"
	name := indicator + " " + branch + " " + displayText(item.workspace.Name)
	style := m.styles.navigationItem
	if item.isRoot {
		name = indicator + "▾ " + displayText(item.root.Name)
		style = m.styles.navigationRoot
		if item.failure != "" {
			name = indicator + "✗ " + displayText(item.root.Name)
			style = m.styles.errorText
		}
	}
	if isCurrent {
		style = m.styles.navigationCurrent
	}
	if isSelected {
		style = m.styles.navigationSelected
	}
	markers := openTabMarkers(m.styles, style, item.tabs, m.agentAnimationFrame)
	var nameLine string
	if markers != "" {
		available := width - lipgloss.Width(markers) - 2
		if available > 0 {
			name = truncate(name, available)
			name += strings.Repeat(" ", max(width-lipgloss.Width(name)-lipgloss.Width(markers), 2))
			nameLine = style.Render(name) + markers
		}
	}
	if nameLine == "" {
		nameLine = style.Render(pad(truncate(name, width), width))
	}
	if item.isRoot {
		if item.separator {
			return []string{"", nameLine}
		}
		return []string{nameLine}
	}
	return []string{nameLine, m.renderNavigationGit(item, indicator, style, width)}
}

func (m dashboard) renderNavigationGit(item navItem, indicator string, style lipgloss.Style, width int) string {
	prefix := indicator + "   "
	if !item.hasGit {
		return style.Render(pad(prefix, width))
	}

	branch := displayText(item.git.Branch)
	if item.git.Detached {
		revision := item.git.Revision
		if len(revision) > 7 {
			revision = revision[:7]
		}
		branch = "@" + revision
	}
	marker := ""
	if item.git.Conflicted {
		marker = "!"
	} else if item.git.Dirty {
		marker = "*"
	}
	sync := ""
	if item.git.Ahead > 0 {
		sync += fmt.Sprintf(" ↑%d", item.git.Ahead)
	}
	if item.git.Behind > 0 {
		sync += fmt.Sprintf(" ↓%d", item.git.Behind)
	}

	available := max(width-lipgloss.Width(prefix)-lipgloss.Width(marker)-lipgloss.Width(sync)-2, 0)
	branch = truncate(branch, available)
	branchStyle := style.Foreground(m.styles.gitBranch.GetForeground())
	statusStyle := style.Foreground(m.styles.gitStatus.GetForeground())
	conflictStyle := style.Foreground(m.styles.gitConflict.GetForeground())
	line := style.Render(prefix) + branchStyle.Render("("+branch)
	if marker == "!" {
		line += conflictStyle.Render(marker)
	} else if marker != "" {
		line += statusStyle.Render(marker)
	}
	line += branchStyle.Render(")") + statusStyle.Render(sync)
	return line + style.Render(strings.Repeat(" ", max(width-lipgloss.Width(line), 0)))
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
		labels = append(labels, " "+displayText(tab.Name)+" ")
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

func displayText(value string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return '�'
		}
		return character
	}, value)
}

func pad(value string, width int) string {
	missing := width - lipgloss.Width(value)
	if missing <= 0 {
		return value
	}
	return value + strings.Repeat(" ", missing)
}
