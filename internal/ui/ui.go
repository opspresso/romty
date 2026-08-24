package ui

import (
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/nalbam/romty/internal/model"
)

const (
	terminalTop        = 2
	helpKeyColumnWidth = 20
)

type Backend interface {
	AddRoot(path string) (model.Snapshot, error)
	Snapshot() (model.Snapshot, error)
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

type modal int

const (
	noModal modal = iota
	aboutModal
	helpModal
	configModal
	shutdownModal
)

type navItem struct {
	root      model.Root
	workspace model.Workspace
	tabs      []model.Tab
	isRoot    bool
	lastChild bool
}

type shortcut struct {
	key         string
	description string
}

type dashboard struct {
	backend Backend
	state   model.Snapshot
	result  Result

	width               int
	height              int
	focus               pane
	navIndex            int
	tabIndex            int
	selectedWorkspaceID string
	selectedPath        string
	inputMode           bool
	input               string
	errorMessage        string
	terminal            *embeddedTerminal
	modal               modal
	shutdownPending     bool
	scrollback          bool
	scrollOffset        int
	helpOffset          int
	configPath          string
	leftWidth           int
	mousePassthrough    bool
	styles              *uiStyles
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
			m.errorMessage = message.err.Error()
			return m, nil
		}
		m.state = message.value
		m.syncSelection()
		m.ensureWorkspaceCursor()
		m.clampTabIndex()
		m.errorMessage = ""
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
			m.errorMessage = message.err.Error()
			return m, m.resizeTerminal()
		}
		m.errorMessage = ""
		return m, m.resizeTerminal()
	case daemonStoppedMsg:
		if message.err != nil {
			m.modal = noModal
			m.shutdownPending = false
			m.errorMessage = "stop daemon: " + message.err.Error()
			return m, nil
		}
		return m.quit()
	}
	return m, nil
}

// globalKeys are handled in both panes and with a modal open. Routing every
// function key through one table keeps their precedence at a single point.
var globalKeys = map[string]func(dashboard) (tea.Model, tea.Cmd){
	"f1": func(m dashboard) (tea.Model, tea.Cmd) { return m.openModal(aboutModal) },
	"f2": func(m dashboard) (tea.Model, tea.Cmd) { return m.startRootInput() },
	"f3": func(m dashboard) (tea.Model, tea.Cmd) { return m.openModal(configModal) },
	"f4": func(m dashboard) (tea.Model, tea.Cmd) { return m.quit() },
	"f5": func(m dashboard) (tea.Model, tea.Cmd) { return m, m.refresh() },
	"f6": func(m dashboard) (tea.Model, tea.Cmd) { return m.openModal(shutdownModal) },
	"f7": func(m dashboard) (tea.Model, tea.Cmd) { return m.toggleScrollback() },
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

func (m dashboard) openModal(value modal) (tea.Model, tea.Cmd) {
	m.modal = value
	m.helpOffset = 0
	m.errorMessage = ""
	return m, nil
}

func (m dashboard) startRootInput() (tea.Model, tea.Cmd) {
	m.modal = noModal
	m.inputMode = true
	m.input = ""
	m.errorMessage = ""
	return m, nil
}

func (m dashboard) handleModalKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if message.String() == "esc" {
		m.modal = noModal
		return m, nil
	}
	switch m.modal {
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
	leftWidth, rightWidth, _, terminalHeight := m.dimensions()
	translated := uv.Mouse(mouse)
	translated.X = mouse.X - leftWidth - 3
	translated.Y = mouse.Y - terminalTop
	if translated.X < 0 || translated.X >= rightWidth || translated.Y < 0 || translated.Y >= terminalHeight {
		return uv.Mouse{}, false
	}
	return translated, true
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
		// unmodified key: such applications bind plain PgUp/PgDn, and the
		// emulator has no encoding for Shift with a special key anyway.
		if pages != 0 && m.focus == terminalPane && m.terminal != nil && m.terminal.altScreen() {
			m.terminal.sendKey(pagingKey(pages))
			return m, nil
		}
		m.errorMessage = m.scrollbackUnavailable()
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
	m.errorMessage = ""
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
	_, _, _, terminalHeight := m.dimensions()
	return max(terminalHeight-1, 1)
}

func (m dashboard) scrollHelp(delta int) (tea.Model, tea.Cmd) {
	_, _, bodyHeight, _ := m.dimensions()
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
	current, _, _, _ := m.dimensions()
	maximum := min(maximumLeftWidth, max(m.width, 40)-20)
	m.leftWidth = min(max(current+delta, minimumLeftWidth), maximum)
	return m, m.saveConfig()
}

func (m dashboard) saveConfig() tea.Cmd {
	path := m.configPath
	// Carry every field, or adjusting the pane width would erase the rest.
	config := Config{LeftWidth: m.leftWidth, MousePassthrough: m.mousePassthrough}
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
			m.errorMessage = "root path is required"
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
		m.errorMessage = message.err.Error()
		return m, nil
	}
	m.selectedWorkspaceID = message.value.ID
	m.selectedPath = message.value.Path
	m.state = message.snapshot
	tabs := m.selectedTabs()
	m.focus = leftPane
	m.errorMessage = ""
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
	m.errorMessage = "selected terminal is no longer running"
	return m, nil
}

func (m dashboard) handleCreatedTab(message tabMsg) (tea.Model, tea.Cmd) {
	if message.err != nil {
		m.errorMessage = message.err.Error()
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
	m.errorMessage = ""
	return m, m.openSelectedTerminal()
}

func (m dashboard) handleOpenedTerminal(message terminalOpenedMsg) (tea.Model, tea.Cmd) {
	if message.err != nil {
		m.errorMessage = message.err.Error()
		m.focus = leftPane
		return m, nil
	}
	m.closeTerminal()
	columns, rows := m.terminalSize()
	m.terminal = newEmbeddedTerminal(message.tabID, message.stream, int(columns), int(rows))
	m.focus = terminalPane
	m.errorMessage = ""
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
			m.errorMessage = m.scrollbackUnavailable()
		default:
			// Hold the viewport on the same content as new output pushes
			// older lines into the scrollback.
			m.scrollTerminal(m.terminal.scrollbackLen() - before)
		}
	}
	if message.err != nil {
		m.terminal.disconnect()
		m.focus = leftPane
		m.errorMessage = "terminal session disconnected"
		return m, m.refresh()
	}
	return m, m.terminal.read()
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
			return snapshotMsg{err: err}
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
		m.navIndex = 0
		m.tabIndex = 0
		return
	}
	m.navIndex = (m.navIndex + delta + len(items)) % len(items)
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

func (m *dashboard) ensureWorkspaceCursor() {
	if _, ok := m.navigationItem(); ok {
		return
	}
	m.navIndex = 0
}

func (m *dashboard) focusNavigation() {
	m.focus = leftPane
	items := m.navigationItems()
	for index, item := range items {
		if item.workspace.Path == m.selectedPath {
			m.navIndex = index
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
			tabs:   root.Tabs,
			isRoot: true,
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

func (m dashboard) dimensions() (int, int, int, int) {
	width := max(m.width, 40)
	height := max(m.height, 10)
	leftWidth := m.leftWidth
	if leftWidth == 0 {
		leftWidth = min(max(width/4, minimumLeftWidth), 28)
	}
	leftWidth = min(leftWidth, width-20)
	rightWidth := max(width-leftWidth-3, 17)
	bodyHeight := height - 2
	return leftWidth, rightWidth, bodyHeight, max(bodyHeight-terminalTop, 1)
}

func (m dashboard) terminalSize() (uint16, uint16) {
	_, rightWidth, _, terminalHeight := m.dimensions()
	return uint16(min(rightWidth, 65535)), uint16(min(terminalHeight, 65535))
}

func (m dashboard) View() tea.View {
	content := m.render()
	view := tea.NewView(content)
	view.AltScreen = true
	view.WindowTitle = "romty"
	view.MouseMode = m.mouseMode()
	if !m.scrollback && m.focus == terminalPane && m.terminal != nil && m.terminal.active {
		leftWidth, _, _, _ := m.dimensions()
		position := m.terminal.cursorPosition()
		view.Cursor = tea.NewCursor(leftWidth+3+position.X, terminalTop+position.Y)
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
	leftWidth, rightWidth, bodyHeight, _ := m.dimensions()
	width := max(m.width, 40)
	lines := m.renderPanes(leftWidth, rightWidth, bodyHeight)
	if m.scrollback {
		lines = m.renderRows(m.renderTerminal(width), width, bodyHeight)
	}
	if m.modal != noModal {
		lines = m.overlayModal(lines, width, bodyHeight)
	}
	lines = append(lines, m.renderStatus(width, bodyHeight)...)
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
			contextShortcuts = []shortcut{{key: "Ctrl+\\", description: "navigation"}}
		}
		rail = renderShortcutRail(m.styles, width, contextShortcuts...)
		status = renderShortcuts(m.styles, width,
			shortcut{key: "F1", description: "about"},
			shortcut{key: "F2", description: "add root"},
			shortcut{key: "F3", description: "config"},
			shortcut{key: "F4", description: "quit"},
			shortcut{key: "F5", description: "refresh"},
			shortcut{key: "F6", description: "stop daemon"},
			shortcut{key: "F7", description: "scrollback"},
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
		leftWidth, _, _, _ := m.dimensions()
		return modalBox(m.styles, modalWidth, "Config",
			"",
			m.styles.modalStrong.Render(fmt.Sprintf("Left pane width: %d", leftWidth)),
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
		renderHelpSection(m.styles, "COMMANDS", "F-keys work in both areas"),
		renderHelpShortcut(m.styles, "About", "i", "F1"),
		renderHelpShortcut(m.styles, "Add root", "a", "F2"),
		renderHelpShortcut(m.styles, "Config", ",", "F3"),
		renderHelpShortcut(m.styles, "Quit", "q", "F4"),
		renderHelpShortcut(m.styles, "Refresh", "r", "F5"),
		renderHelpShortcut(m.styles, "Stop daemon", "F6"),
		renderHelpShortcut(m.styles, "Scrollback", "F7"),
		renderHelpShortcut(m.styles, "Help", "?"),
		renderHelpSection(m.styles, "NAVIGATION", "workspace area"),
		renderHelpShortcut(m.styles, "Select workspace", "↑/↓", "j/k"),
		renderHelpShortcut(m.styles, "Select tab / +", "←/→", "h/l"),
		renderHelpShortcut(m.styles, "Open / confirm", "Enter"),
		renderHelpShortcut(m.styles, "Focus terminal", "Tab"),
		renderHelpSection(m.styles, "TERMINAL", "terminal area"),
		renderHelpShortcut(m.styles, "Focus workspace", "Ctrl+\\"),
		renderHelpSection(m.styles, "SCROLLBACK", "mouse works here only"),
		renderHelpShortcut(m.styles, "Enter / leave", "F7", "Ctrl+\\"),
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
