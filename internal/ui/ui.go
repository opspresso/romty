// Package ui is romty's dashboard: the workspace tree, the terminal it opens
// beside it, and the state machine between them. It talks to the daemon
// through a Backend and owns nothing that survives the TUI closing.
package ui

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/agenthooks"
	"github.com/opspresso/romty/internal/gitrepo"
	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/sound"
)

const (
	// tagline is what About and help say romty is.
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
	// minimumModalWidth and the two caps bound a modal box. The box shrinks to
	// its content between them, so a question of one sentence is one sentence
	// wide; the caps keep a long path or a wide diff off the screen edges.
	minimumModalWidth     = 32
	maximumModalWidth     = 72
	maximumWideModalWidth = 80

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
	ReopenTab(workspaceID, closedID string, columns, rows uint16) (model.Tab, error)
	CloseTab(tabID string) (model.Snapshot, error)
	RenameTab(tabID, name string) (model.Snapshot, error)
	MoveTab(tabID string, offset int) (model.Snapshot, error)
	// Subscribe opens the daemon's stream of changes; a daemon that predates
	// it returns model.ErrNeedsNewerDaemon and the dashboard polls instead.
	Subscribe() (model.EventStream, error)
	// RefreshGit asks the daemon to read Git state now, fetching first when
	// fetch is set; the result arrives on the stream.
	RefreshGit(fetch bool) error
	DaemonInfo() (model.DaemonInfo, error)
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
	inputModal
	branchModal
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
	hoverModalClose
	hoverBrowseRow
	hoverWorkspaceAction
	hoverGitAction
	hoverGitResult
	hoverConfigRow
	hoverBranchRow
)

type dashboard struct {
	backend Backend
	state   model.Snapshot
	result  Result
	// Shared across model copies and terminal replacements so an old
	// attachment's resize cannot arrive after its replacement's size.
	resizeMu *sync.Mutex
	width    int
	height   int
	focus    pane
	// config holds every persisted setting. It is the document as loaded,
	// so saving keeps the fields this build does not know.
	config     Config
	configPath string
	// homePath is where the root picker opens, resolved once at startup.
	homePath string
	// hostname names the machine in the tree title, resolved once at startup.
	hostname         string
	styles           *uiStyles
	hover            hoverTarget
	navigationResize bool

	// Each feature's state, embedded so its fields read as the dashboard's
	// own while their ownership is written down in one place.
	daemonLink
	alertState
	treeState
	tabState
	terminalState
	modalState
	statusState
	gitViewState
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
	value      map[string]gitrepo.State
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
	// reopenID names the closed session the created tab restores.
	reopenID string
	err      error
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

// configSavedMsg reports one save and the settings it wrote.
type configSavedMsg struct {
	saved Config
	err   error
}

type daemonStoppedMsg struct {
	err error
}

type hooksInstalledMsg struct {
	results []agenthooks.Result
	err     error
}

// resizeFailedMsg reports a failed resize. It is not a snapshotMsg, because
// that message means "a new snapshot arrived" and its handler clears the
// status bar.
//
// It names its tab, like every other answer romty waits on. A resize is a
// round trip, so a switch of tabs can leave one in flight for the tab just
// left, and its failure must not be reported against the terminal on screen.
type resizeFailedMsg struct {
	tabID string
	err   error
}

// reopenTerminalMsg arrives after a backoff, so a terminal that keeps dropping
// is retried at a pace that leaves the daemon and the UI usable.
type reopenTerminalMsg struct {
	tabID string
}

var playSound = sound.Play

// Run runs the dashboard. restartDaemon starts the daemon again when it stops
// while the dashboard is open; nil leaves F5 a refresh.
func Run(backend Backend, initial model.Snapshot, configPath string, hookStatuses []agenthooks.Status, restartDaemon func() error) (Result, error) {
	config, err := loadConfig(configPath)
	if err != nil {
		return Result{}, fmt.Errorf("load UI config: %w", err)
	}
	value := newDashboardWithConfig(backend, initial, configPath, config)
	value.restartDaemon = restartDaemon
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

var osHostname = os.Hostname

// shortHostname is the machine name without its domain, or empty when the
// system cannot report one.
func shortHostname() string {
	name, err := osHostname()
	if err != nil {
		return ""
	}
	name, _, _ = strings.Cut(name, ".")
	return name
}

func newDashboardWithConfig(backend Backend, initial model.Snapshot, configPath string, config Config) dashboard {
	value := dashboard{
		backend:    backend,
		resizeMu:   &sync.Mutex{},
		state:      initial,
		width:      80,
		height:     24,
		configPath: configPath,
		homePath:   userHomeDirectory(),
		hostname:   shortHostname(),
		config:     config,
		styles:     newUIStyles(true),
	}
	value.gitFetchedAt = now()
	value.ensureWorkspaceCursor()
	value.restoreSelection()
	value.heardAgents = make(map[string]model.AgentStatus)
	for tab := range initial.Tabs() {
		value.heardAgents[tab.ID] = tab.AgentStatus()
	}
	value.agentAnimationActive = value.hasAnimatedAgent()
	value.agentAnimationPending = value.agentAnimationActive || value.hasPendingActivity()
	return value
}

func (m dashboard) Init() tea.Cmd {
	// The host starts with alternate scroll on, which is what turns a wheel
	// notch into arrow keys romty cannot tell from typed ones. Only scrollback
	// wants them, so the mode is off until it opens.
	commands := []tea.Cmd{tea.RequestBackgroundColor, m.subscribe(), altScrollCommand(false), m.readDaemonInfo()}
	// Bubble Tea only asks terminals it can name for synchronized output and
	// skips SSH sessions outright — but an SSH session is where romty lives.
	// Asked blind, a terminal that supports the mode reports it and every
	// frame is then drawn atomically, which is what stops a remote terminal
	// from showing half-painted frames and a blinking cursor; one that does
	// not says so or stays silent, and nothing changes. Unicode core rides
	// along the same way it does upstream, so a terminal that measures
	// graphemes is taken at its word about the widths romty lays out.
	commands = append(commands, tea.Raw(ansi.RequestModeSynchronizedOutput+ansi.RequestModeUnicodeCore))
	if m.restorePending {
		commands = append(commands, m.openSelectedTerminal())
	}
	if m.agentAnimationPending {
		commands = append(commands, animateAgentMarker())
	}
	if releaseCheckEnabled() {
		commands = append(commands, checkRelease())
	}
	return tea.Batch(commands...)
}

// Update runs the state machine, then brings the state derived from the model
// in line with it: the agent animation timer, the PTY size the narrow layout
// implies, and the host's alternate scroll. Every transition passes through
// here, so none of them has to remember those three.
func (m dashboard) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	updated, command := m.update(message)
	value, ok := updated.(dashboard)
	if !ok {
		return updated, command
	}
	value, command = value.syncAgentAnimation(command)
	value, command = value.syncTerminalSize(command)
	value.offerDeferredHooks()
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
	wanted := m.scrollback && !m.config.ScrollbackMouse
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
		if m.modal == workspaceActionsModal {
			m.ensureWorkspaceActionVisible()
		}
		return m, m.resizeTerminal()
	case tea.BackgroundColorMsg:
		m.styles = newUIStyles(message.IsDark())
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(message)
	case tea.MouseClickMsg, tea.MouseReleaseMsg, tea.MouseWheelMsg, tea.MouseMotionMsg:
		mouse := message.(tea.MouseMsg)
		m.hover = m.hoverTargetAt(mouse.Mouse())
		if click, ok := message.(tea.MouseClickMsg); ok && (click.Button != tea.MouseLeft || m.hover.kind != hoverTab) {
			m.lastTabClick = tabClick{}
		}
		if m.modal != noModal {
			if updated, command, handled := m.handleModalMouse(mouse); handled {
				return updated, command
			}
			if m.modal == helpModal {
				return m.handleHelpMouse(mouse)
			}
			return m, nil
		}
		if m.fileView.active {
			return m.handleFileViewMouse(mouse)
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
		if m.requestPending() {
			return m, nil
		} else if m.modal == inputModal {
			m.input.text += message.Content
			m.input.err = ""
		} else if m.modal == branchModal && m.branches.confirming == nil {
			m.branches.query += message.Content
			m.branches.index, m.branches.offset = m.firstBranchChoice(), 0
		} else if m.modal == removeSelectionModal && !m.removeTarget.isRoot {
			// Pasting the name is how a name that is hard to type, such as
			// one with characters the keyboard lacks, is confirmed.
			m.removeConfirm += message.Content
		} else if m.modal != noModal {
			return m, nil
		} else if m.scrollback && m.search.typing {
			m.search.query += message.Content
		} else if m.scrollback && m.terminal != nil {
			m.stopScrollback()
			m.terminal.paste(message.Content)
		} else if !m.fileView.active && m.focus == terminalPane && m.terminal != nil {
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
			if !m.noteDaemonError(message.err) {
				m.setError(treeError, message.err.Error())
			}
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
		paths := m.workspacePaths()
		if !m.applySnapshot(order, message.value) {
			return m, nil
		}
		m.syncSelection()
		m.ensureWorkspaceCursor()
		m.clampTabIndex()
		bells := m.noteBells()
		// A refresh says the tree is current; it says nothing about a failed
		// resize or a daemon that would not stop, so it clears tree errors
		// only.
		m.clearError(treeError)
		// A workspace that just appeared, such as one under a root the user
		// added, would otherwise show no Git state and offer no Git actions
		// until the next periodic read.
		var readGit tea.Cmd
		if !slices.Equal(paths, m.workspacePaths()) {
			readGit = m.readGitStatus(false, false)
		}
		if m.terminalExited {
			settled, command := m.settleAfterExit()
			return settled, tea.Batch(command, readGit, bells)
		}
		return m, tea.Batch(readGit, bells)
	case agentSnapshotMsg:
		if message.err == nil {
			alerts := m.applyAgentStatuses(message.value)
			if m.daemonDown {
				// Something else, such as romty in another terminal, started
				// the daemon again. Its tree is the one to show.
				m.daemonDown = false
				m.clearError(daemonError)
				alerts = append(alerts, m.refresh())
			}
			return m, tea.Batch(append(alerts, m.refreshAgents())...)
		}
		m.noteDaemonError(message.err)
		return m, m.refreshAgents()
	case gitRefreshFailedMsg:
		if !m.noteDaemonError(message.err) {
			m.setError(gitError, message.err.Error())
		}
		return m, nil
	case subscribedMsg:
		return m.handleSubscribed(message)
	case eventMsg:
		return m.handleEvent(message)
	case resubscribeMsg:
		if m.events != nil || m.polling {
			return m, nil
		}
		return m, m.subscribe()
	case daemonRestartedMsg:
		return m.handleDaemonRestarted(message)
	case daemonInfoMsg:
		return m.handleDaemonInfo(message)
	case soundPlayedMsg:
		return m, nil
	case releaseCheckedMsg:
		return m.handleReleaseCheck(message)
	case releaseCheckDueMsg:
		return m, checkRelease()
	case agentAnimationMsg:
		m.agentAnimationPending = false
		if m.agentAnimationActive || m.hasPendingActivity() {
			m.agentAnimationFrame = (m.agentAnimationFrame + 1) % len(agentAnimationFrames)
		}
		return m, nil
	case gitStatusMsg:
		// A repository takes two rows and a plain directory one, so a Git
		// read can change the tree's height under the cursor. A cursor that
		// was on screen stays there; one the wheel scrolled away is left be.
		_, cursorShown := m.cursorOnScreen()
		m.gitStates = message.value
		if cursorShown {
			m.ensureNavigationVisible()
		}
		if message.fetchedAt.After(m.gitFetchedAt) {
			m.gitFetchedAt = message.fetchedAt
		}
		if message.reschedule {
			return m, m.refreshGitStatus()
		}
		return m, nil
	case branchListMsg:
		return m.handleBranchList(message)
	case tabMovedMsg:
		return m.handleTabMoved(message)
	case gitActionMsg:
		return m.handleGitActionResult(message)
	case fileListMsg:
		return m.handleFileList(message)
	case fileContentMsg:
		return m.handleFileContent(message)
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
		// What the save reported comes first, even when a later change has
		// already made this save out of date: a failed save must reach the
		// user.
		if message.err != nil {
			m.setError(settingError, message.err.Error())
		} else {
			m.clearError(settingError)
		}
		if !sameConfig(message.saved, m.config) {
			// A setting changed while this save was being written, so the
			// file is already out of date. A later save answers whatever
			// this one said.
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
	case inputResultMsg:
		return m.handleInputResult(message)
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
	// F1 is help, as it is elsewhere, and it works in the terminal pane where
	// `?` goes to the shell.
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
	"ctrl+shift+f": func(m dashboard) (tea.Model, tea.Cmd) { return m.toggleFileView() },
	// Ctrl+Shift+Left/Right switches tabs from either pane. It is the chord a
	// terminal with tabs binds, and a
	// terminal reports it distinctly — Ctrl+Shift+Tab is not, because most
	// terminals send it as a plain Shift+Tab. The unshifted arrows stay with
	// the shell, where word-wise movement lives.
	"ctrl+shift+left":  func(m dashboard) (tea.Model, tea.Cmd) { return m.switchTab(-1) },
	"ctrl+shift+right": func(m dashboard) (tea.Model, tea.Cmd) { return m.switchTab(1) },
	// The other axis of the same chord: Left and Right move along one
	// workspace's terminals, Up and Down move between the workspaces that
	// have any, without leaving the terminal pane.
	"ctrl+shift+up":   func(m dashboard) (tea.Model, tea.Cmd) { return m.switchWorkspace(-1) },
	"ctrl+shift+down": func(m dashboard) (tea.Model, tea.Cmd) { return m.switchWorkspace(1) },
	// The third axis: not where the user was going, but where an agent is
	// waiting for them.
	"ctrl+shift+a": func(m dashboard) (tea.Model, tea.Cmd) { return m.jumpToWaitingAgent() },
	// The chord terminals with tabs use to move one. < and > do the same in
	// the workspace pane, where every terminal can send them.
	"ctrl+shift+pgup":   func(m dashboard) (tea.Model, tea.Cmd) { return m.moveSelectedTab(-1) },
	"ctrl+shift+pgdown": func(m dashboard) (tea.Model, tea.Cmd) { return m.moveSelectedTab(1) },
}

func (m dashboard) handleKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// While a request is out no key can take it back, so the only one that
	// still applies is the one that leaves romty.
	if m.requestPending() {
		if message.String() == "f4" {
			return m.quit()
		}
		return m, nil
	}
	if m.modal == inputModal {
		return m.handleInput(message)
	}
	if m.modal == hookInstallModal {
		if message.String() == "f4" {
			return m.quit()
		}
		return m.handleModalKey(message)
	}
	if m.fileView.active && m.modal != noModal {
		if action, ok := globalKeys[message.String()]; ok {
			return action(m)
		}
		return m.handleModalKey(message)
	}
	if m.fileView.active {
		if message.String() == "ctrl+shift+f" {
			return m.toggleFileView()
		}
		return m.handleFileViewKey(message)
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
	if m.treeFiltering && m.focus == leftPane {
		return m.handleTreeFilterKey(message)
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
		return m.activateWorkspace()
	case "space":
		return m.toggleRoot()
	case "/":
		m.treeFiltering = true
	case "<":
		return m.moveSelectedTab(-1)
	case ">":
		return m.moveSelectedTab(1)
	// One letter for each Ctrl+Shift chord. A terminal without the Kitty
	// keyboard protocol sends Ctrl+Shift+A as the same byte as Ctrl+A, so the
	// chords cannot reach romty there. Ctrl+/ and then the letter works in
	// every terminal, phone SSH clients included.
	case "a":
		return m.jumpToWaitingAgent()
	case "t":
		return m.newTab()
	case "g":
		return m.openGitActions()
	case "f":
		return m.toggleFileView()
	}
	return m, nil
}

func (m dashboard) quit() (tea.Model, tea.Cmd) {
	if m.events != nil {
		_ = m.events.Close()
	}
	m.cancelGitAction()
	m.closeTerminal()
	m.result.Quit = true
	return m, tea.Quit
}
