// Package ui is romty's dashboard: the workspace tree, the terminal it opens
// beside it, and the state machine between them. It talks to the daemon
// through a Backend and owns nothing that survives the TUI closing.
package ui

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/agenthooks"
	"github.com/opspresso/romty/internal/display"
	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/sound"
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
	// wheelLines is how far one notch of the wheel moves. It is what a terminal
	// sends for alternate scroll, so a guest that is given cursor keys instead
	// of mouse reports moves by the same amount romty's own scrollback does.
	wheelLines = 3
	// narrowLayoutWidth is the width below which the workspace pane gives way
	// to the terminal it is focused away from. A phone's SSH client is around
	// 40 to 60 columns, where the pane's 18 column minimum and its separator
	// take half the screen and leave the terminal unusable. Above this the
	// split is worth its width, so the layout does not move under a desktop.
	narrowLayoutWidth = 80

	maximumReattachAttempts = 3
	initialReattachBackoff  = 250 * time.Millisecond
	maximumReattachBackoff  = 2 * time.Second
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

type Backend interface {
	AddRoot(path string) (model.Snapshot, error)
	Snapshot() (model.Snapshot, error)
	AgentStatuses() (map[string]model.AgentStatus, error)
	RemoveRoot(rootID string) (model.Snapshot, error)
	RemoveWorkspace(rootID, path string) (model.Snapshot, error)
	EnsureWorkspace(rootID, path string) (model.Workspace, error)
	CreateTab(workspaceID string, columns, rows uint16) (model.Tab, error)
	CloseTab(tabID string) (model.Snapshot, error)
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
	workspaceActionsModal
	gitActionsModal
	closeTabModal
	removeSelectionModal
	shutdownModal
	hookInstallModal
)

type shortcut struct {
	key         string
	description string
}

const (
	hoverNone hoverKind = iota
	hoverNavigation
	hoverTab
	hoverTabClose
	hoverDivider
	hoverModalAction
	hoverBrowseRow
	hoverWorkspaceAction
	hoverGitAction
	hoverGitResult
	hoverConfigRow
)

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
	tabClosePending  string
	tabCloseActive   bool

	width    int
	height   int
	focus    pane
	navIndex int
	// navOffset is the first workspace tree item in the viewport. The wheel
	// moves it without moving the cursor; keyboard navigation changes it only
	// when the cursor would otherwise leave the screen.
	navOffset int
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
	removeTarget navItem
	// closeTabTarget is the tab its confirmation is asking about, held for the
	// same reason, with the position it was drawn at so the cursor can take
	// the place of the tab that leaves.
	closeTabTarget model.Tab
	closeTabIndex  int
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
	altScroll bool
	// terminalFullWidth is the narrow layout as the PTY was last told it, so
	// hiding or restoring the workspace pane resizes once rather than every
	// message asking the daemon for the size it already has.
	terminalFullWidth bool
	scrollOffset      int
	helpOffset        int
	configPath        string
	// homePath is where the root picker opens, resolved once at startup.
	homePath string
	// browse is the root picker's state, kept on the dashboard so the modal
	// renders from it and the keys move it.
	browse browser
	// workspaceActionTarget is captured when its action palette opens, so a
	// refresh cannot retarget a destructive or remote action behind the modal.
	workspaceActionTarget  navItem
	workspaceActionIndex   int
	workspaceActionOffset  int
	workspaceActionAnchorX int
	workspaceActionAnchorY int
	// config is the document as loaded, kept so saving edits it instead of
	// reconstructing it from fields.
	config           Config
	leftWidth        int
	navigationResize bool
	hover            hoverTarget
	mousePassthrough bool
	scrollbackMouse  bool
	// searchMode is scrollback's find prompt; searchQuery what has been typed
	// into it. searchMatches are the absolute lines the confirmed query is on,
	// oldest first, and searchIndex which of them the viewport sits on.
	searchMode        bool
	searchQuery       string
	searchMatches     []int
	searchIndex       int
	soundOnDone       bool
	soundOnWaiting    bool
	agentSoundReady   bool
	gitStates         map[string]gitState
	gitFetchedAt      time.Time
	gitActionTarget   model.Workspace
	gitActionReturn   modal
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

type soundPlayedMsg struct {
	kind sound.Kind
}

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

type tabClosedMsg struct {
	tabID       string
	workspaceID string
	index       int
	snapshot    model.Snapshot
	order       uint64
	err         error
}

type terminalOpenedMsg struct {
	tabID  string
	stream io.ReadWriteCloser
	replay []byte
	err    error
}

type configSavedMsg struct {
	leftWidth         int
	scrollbackMouse   bool
	soundOnDone       bool
	soundOnWaiting    bool
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
var playSound = sound.Play

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
		scrollbackMouse:  config.ScrollbackMouse,
		soundOnDone:      config.SoundOnDone,
		soundOnWaiting:   config.SoundOnWaiting,
		gitDiffSplit:     config.GitDiffView == gitDiffViewSplit,
		styles:           newUIStyles(true),
		gitFetchedAt:     now(),
	}
	value.ensureWorkspaceCursor()
	value.restoreSelection()
	value.agentAnimationActive = value.hasAnimatedAgent()
	value.agentAnimationPending = value.agentAnimationActive || value.hasPendingActivity()
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
	value, command = value.syncTerminalSize(command)
	return value.syncAltScroll(command)
}

// syncTerminalSize reports the width the narrow layout just gave or took back.
// Hiding the workspace pane resizes the terminal as surely as dragging the
// window does, and focus moves from the toggle, Tab, an opened terminal and a
// closed scrollback alike — none of which can return a command from where they
// set it. Every one of them passes through Update, so this is the one place
// that sees them all.
func (m dashboard) syncTerminalSize(command tea.Cmd) (dashboard, tea.Cmd) {
	hidden := m.navigationHidden()
	if hidden == m.terminalFullWidth {
		return m, command
	}
	m.terminalFullWidth = hidden
	return m, tea.Batch(command, m.resizeTerminal())
}

func (m dashboard) syncAgentAnimation(command tea.Cmd) (dashboard, tea.Cmd) {
	if (m.agentAnimationActive || m.hasPendingActivity()) && !m.agentAnimationPending {
		m.agentAnimationPending = true
		command = tea.Batch(command, animateAgentMarker())
	}
	return m, command
}

// syncAltScroll asks the host for alternate scroll while scrollback is open and
// gives it up on the way out. Outside scrollback the arrow keys it sends have
// nowhere good to go: over the workspace pane they walk the tree three rows per
// notch, and over the terminal they reach the shell as history keys nobody
// pressed. It is not asked for when romty is keeping the mouse in scrollback,
// because a host that is reporting the mouse sends the wheel as mouse events
// and never as those arrow keys.
func (m dashboard) syncAltScroll(command tea.Cmd) (tea.Model, tea.Cmd) {
	wanted := m.scrollback && !m.scrollbackMouse
	if m.altScroll == wanted {
		return m, command
	}
	m.altScroll = wanted
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
		m.ensureNavigationVisible()
		return m, m.resizeTerminal()
	case tea.BackgroundColorMsg:
		m.styles = newUIStyles(message.IsDark())
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(message)
	case tea.MouseClickMsg, tea.MouseReleaseMsg, tea.MouseWheelMsg, tea.MouseMotionMsg:
		mouse := message.(tea.MouseMsg)
		m.hover = m.hoverTargetAt(mouse.Mouse())
		if m.modal != noModal {
			if updated, command, handled := m.handleModalMouse(mouse); handled {
				return updated, command
			}
			if m.modal == helpModal {
				return m.handleHelpMouse(mouse)
			}
			return m, nil
		}
		if m.gitDiff.active {
			return m.handleGitDiffMouse(mouse)
		}
		if updated, command, handled := m.handleDashboardMouse(mouse); handled {
			return updated, command
		}
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
			kind, ring := m.soundForAgentTransitions(message.value)
			ring = m.agentSoundReady && ring
			m.updateAgents(message.value)
			m.agentSoundReady = true
			if ring {
				return m, tea.Batch(m.refreshAgents(), soundAlert(kind))
			}
		}
		return m, m.refreshAgents()
	case soundPlayedMsg:
		return m, nil
	case agentAnimationMsg:
		m.agentAnimationPending = false
		if m.agentAnimationActive || m.hasPendingActivity() {
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
	case tabClosedMsg:
		return m.handleClosedTab(message)
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
		if message.leftWidth != m.leftWidth || message.scrollbackMouse != m.scrollbackMouse ||
			message.soundOnDone != m.soundOnDone || message.soundOnWaiting != m.soundOnWaiting ||
			viewChanged || selectionChanged {
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
	// Switching tabs from the terminal pane took Ctrl+/ and then two more keys.
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
	// The third axis: not where the user was going, but where an agent is
	// waiting for them.
	"ctrl+shift+a": func(m dashboard) (tea.Model, tea.Cmd) { return m.jumpToWaitingAgent() },
}

func (m dashboard) handleKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.inputMode {
		return m.handleInput(message)
	}
	// A shutdown already asked for, a Git command already running, an installer
	// already writing: the request is out and no key can take it back, so the
	// only one that still applies is the one that leaves romty.
	if m.shutdownPending || m.gitActionPending || m.hookInstallPending {
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
	if isFocusToggle(message.String()) {
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
		return m.openWorkspaceActions()
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

func (m dashboard) shutdownDaemon() tea.Cmd {
	return func() tea.Msg {
		return daemonStoppedMsg{err: m.backend.Shutdown()}
	}
}

func (m dashboard) adjustLeftWidth(delta int) (tea.Model, tea.Cmd) {
	m.setLeftWidth(m.paneWidth() + delta)
	return m, m.saveConfig()
}

func (m *dashboard) setLeftWidth(width int) {
	maximum := min(maximumLeftWidth, max(m.width, 40)-20)
	m.leftWidth = min(max(width, minimumLeftWidth), maximum)
}

func (m dashboard) toggleScrollbackMouse() (tea.Model, tea.Cmd) {
	m.scrollbackMouse = !m.scrollbackMouse
	return m, m.saveConfig()
}

func (m dashboard) toggleSoundOnDone() (tea.Model, tea.Cmd) {
	m.soundOnDone = !m.soundOnDone
	return m, m.saveConfig()
}

func (m dashboard) toggleSoundOnWaiting() (tea.Model, tea.Cmd) {
	m.soundOnWaiting = !m.soundOnWaiting
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
	config.ScrollbackMouse = m.scrollbackMouse
	config.SoundOnDone = m.soundOnDone
	config.SoundOnWaiting = m.soundOnWaiting
	config.GitDiffView = gitDiffViewSetting(m.gitDiffSplit)
	config.LastWorkspacePath = m.rememberedWorkspacePath
	config.LastTabID = m.rememberedTabID
	return func() tea.Msg {
		return configSavedMsg{
			leftWidth: config.LeftWidth, scrollbackMouse: config.ScrollbackMouse,
			soundOnDone: config.SoundOnDone, soundOnWaiting: config.SoundOnWaiting,
			gitDiffView:       config.GitDiffView,
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
	default:
		m.input = editText(m.input, message)
	}
	return m, nil
}

// editText applies one key to a line of typed text. The root prompt and the
// scrollback search both read their input through it, so a keystroke does the
// same thing in either.
func editText(value string, message tea.KeyPressMsg) string {
	if message.String() == "backspace" {
		runes := []rune(value)
		if len(runes) == 0 {
			return value
		}
		return string(runes[:len(runes)-1])
	}
	if message.Text != "" {
		return value + message.Text
	}
	return value
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
	terminal := newEmbeddedTerminalWithReplay(
		message.tabID, message.stream, message.replay, int(columns), int(rows),
	)
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

// confirmRemoveSelection asks before a root is forgotten or a workspace
// directory is deleted. The item is captured here rather than read again on
// confirmation: a snapshot arriving while the modal is open can move the
// cursor, and the answer has to apply to the item the question named.
func (m dashboard) confirmRemoveSelection(target navItem) (tea.Model, tea.Cmd) {
	m.removeTarget = target
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

func (m dashboard) hasPendingActivity() bool {
	return m.tabPending || m.tabClosePending != "" || m.restorePending || m.gitActionPending || m.shutdownPending ||
		m.hookInstallPending || m.modal == browseModal && m.browse.loading
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

func (m dashboard) renderNavigationGit(item navItem, indicator string, style lipgloss.Style, width int) string {
	prefix := indicator + "   "
	if !item.hasGit {
		return style.Render(pad(prefix, width))
	}

	branch := display.Text(item.git.Branch)
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

func truncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	return ansi.Truncate(value, width, "…")
}

func onOff(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func pad(value string, width int) string {
	missing := width - lipgloss.Width(value)
	if missing <= 0 {
		return value
	}
	return value + strings.Repeat(" ", missing)
}
