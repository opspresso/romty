package ui

import (
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nalbam/romty/internal/model"
)

const (
	terminalTop    = 2
	focusStyle     = "\x1b[1;96m"
	selectStyle    = "\x1b[1;92m"
	activeTabStyle = "\x1b[1;30;106m"
	tabStyle       = "\x1b[36m"
	resetStyle     = "\x1b[0m"
)

type Backend interface {
	AddRoot(path string) (model.Snapshot, error)
	Snapshot() (model.Snapshot, error)
	EnsureWorkspace(rootID, path string) (model.Workspace, error)
	CreateTab(workspaceID string, columns, rows uint16) (model.Tab, error)
	OpenTerminal(tabID string) (io.ReadWriteCloser, error)
	Resize(tabID string, columns, rows uint16) error
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
	configModal
)

type navItem struct {
	root      model.Root
	workspace model.Workspace
	tabs      []model.Tab
	isRoot    bool
}

type styledSegment struct {
	text  string
	style string
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
	configPath          string
	leftWidth           int
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
		backend:    backend,
		state:      initial,
		width:      80,
		height:     24,
		configPath: configPath,
		leftWidth:  config.LeftWidth,
	}
	value.ensureWorkspaceCursor()
	return value
}

func (m dashboard) Init() tea.Cmd {
	return nil
}

func (m dashboard) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		m.height = message.Height
		return m, m.resizeTerminal()
	case tea.KeyPressMsg:
		return m.handleKey(message)
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
	}
	return m, nil
}

func (m dashboard) handleKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.inputMode {
		return m.handleInput(message)
	}
	if m.modal != noModal {
		return m.handleModalKey(message)
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
	case "ctrl+c", "q":
		m.closeTerminal()
		m.result.Quit = true
		return m, tea.Quit
	case "tab":
		if m.terminal != nil && m.terminal.active {
			m.focus = terminalPane
			m.syncTabCursor(m.selectedTabs())
		}
	case "a", "f2":
		m.inputMode = true
		m.input = ""
		m.errorMessage = ""
	case "r", "f5":
		return m, m.refresh()
	case "?", "f1":
		m.modal = aboutModal
	case ",":
		m.modal = configModal
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

func (m dashboard) handleModalKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if message.String() == "esc" {
		m.modal = noModal
		return m, nil
	}
	if m.modal != configModal {
		return m, nil
	}
	switch message.String() {
	case "left", "[":
		return m.adjustLeftWidth(-1)
	case "right", "]":
		return m.adjustLeftWidth(1)
	}
	return m, nil
}

func (m dashboard) adjustLeftWidth(delta int) (tea.Model, tea.Cmd) {
	current, _, _, _ := m.dimensions()
	maximum := min(maximumLeftWidth, max(m.width, 40)-20)
	m.leftWidth = min(max(current+delta, minimumLeftWidth), maximum)
	return m, m.saveConfig()
}

func (m dashboard) saveConfig() tea.Cmd {
	path := m.configPath
	config := Config{LeftWidth: m.leftWidth}
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
		m.terminal.writeOutput(message.data)
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
		for _, directory := range root.Directories {
			result = append(result, navItem{root: root.Root, workspace: directory.Workspace, tabs: directory.Tabs})
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
	view.MouseMode = tea.MouseModeNone
	if m.focus == terminalPane && m.terminal != nil && m.terminal.active {
		leftWidth, _, _, _ := m.dimensions()
		position := m.terminal.cursorPosition()
		view.Cursor = tea.NewCursor(leftWidth+3+position.X, terminalTop+position.Y)
	}
	return view
}

func (m dashboard) render() string {
	leftWidth, rightWidth, bodyHeight, _ := m.dimensions()
	left := m.renderNavigation()
	right := m.renderTerminal(rightWidth)
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
		leftLine = pad(truncate(leftLine, leftWidth), leftWidth)
		if row == m.navIndex+2 {
			leftLine = selectStyle + leftLine + resetStyle
		}
		lines = append(lines, leftLine+m.paneSeparator(row)+rightLine)
	}
	width := max(m.width, 40)
	if m.modal != noModal {
		lines = m.overlayModal(lines, width)
	}
	status := renderShortcuts(width,
		shortcut{key: "F1", description: "about"},
		shortcut{key: ",", description: "config"},
		shortcut{key: "F2", description: "add"},
		shortcut{key: "F5", description: "refresh"},
		shortcut{key: "↑/↓", description: "tree"},
		shortcut{key: "←/→", description: "tabs/+"},
		shortcut{key: "Enter", description: "select"},
		shortcut{key: "Tab", description: "terminal"},
		shortcut{key: "Ctrl+C", description: "quit"},
	)
	if m.focus == terminalPane {
		status = renderShortcuts(width,
			shortcut{key: "Ctrl+\\", description: "navigation"},
		)
	}
	if m.inputMode {
		status = truncate("Root folder: "+m.input+"_", width)
	} else if m.errorMessage != "" {
		status = truncate("Error: "+m.errorMessage, width)
	} else if m.modal == aboutModal {
		status = renderShortcuts(width, shortcut{key: "Esc", description: "close"})
	} else if m.modal == configModal {
		status = renderShortcuts(width,
			shortcut{key: "←/→", description: "adjust width"},
			shortcut{key: "Esc", description: "close"},
		)
	}
	lines = append(lines, strings.Repeat("─", min(width, 120)), status)
	return strings.Join(lines, "\n")
}

func (m dashboard) overlayModal(lines []string, width int) []string {
	modalLines := m.renderModal(width)
	top := max((len(lines)-len(modalLines))/2, 0)
	for index, line := range modalLines {
		row := top + index
		if row >= len(lines) {
			break
		}
		left := max((width-len([]rune(line)))/2, 0)
		right := max(width-left-len([]rune(line)), 0)
		lines[row] = strings.Repeat(" ", left) + focusStyle + line + resetStyle + strings.Repeat(" ", right)
	}
	return lines
}

func (m dashboard) renderModal(width int) []string {
	modalWidth := min(max(width-8, 32), 56)
	if m.modal == configModal {
		leftWidth, _, _, _ := m.dimensions()
		return modalBox(modalWidth,
			"Config",
			"",
			fmt.Sprintf("Left pane width: %d", leftWidth),
			"",
			"←/→ or [/]  Adjust",
			"Esc          Close",
		)
	}
	return modalBox(modalWidth,
		"About",
		"",
		"romty",
		"Persistent terminal workspace manager",
		"",
		"Esc  Close",
	)
}

func modalBox(width int, values ...string) []string {
	interior := width - 2
	lines := make([]string, 0, len(values)+2)
	lines = append(lines, "╭"+strings.Repeat("─", interior)+"╮")
	for _, value := range values {
		lines = append(lines, "│"+pad(truncate(value, interior), interior)+"│")
	}
	return append(lines, "╰"+strings.Repeat("─", interior)+"╯")
}

func (m dashboard) paneSeparator(row int) string {
	if row != 0 {
		return " │ "
	}
	if m.focus == leftPane {
		return focusStyle + "◀" + resetStyle + "│ "
	}
	return " │" + focusStyle + "▶" + resetStyle
}

func (m dashboard) renderNavigation() []string {
	lines := []string{"Workspaces", ""}
	items := m.navigationItems()
	for _, item := range items {
		if item.isRoot {
			line := "▾ " + item.root.Name
			if markers := openTabMarkers(item.tabs); markers != "" {
				line += "  " + markers
			}
			lines = append(lines, line)
		} else {
			line := "  " + item.workspace.Name
			if markers := openTabMarkers(item.tabs); markers != "" {
				line += "  " + markers
			}
			lines = append(lines, line)
		}
	}
	if len(items) == 0 {
		lines = append(lines, "  Press F2 to add a root")
	}
	return lines
}

func (m dashboard) renderTerminal(width int) []string {
	tabs := m.selectedTabs()
	if m.focus == leftPane {
		tabs = m.navigationTabs()
	}
	lines := []string{renderTabBar(tabs, m.tabIndex, width), strings.Repeat("─", width)}
	if m.terminal != nil {
		return append(lines, m.terminal.render()...)
	}
	if _, ok := m.navigationItem(); m.focus == leftPane && !ok {
		return append(lines, "Select a root or workspace on the left")
	}
	if len(tabs) == 0 {
		return append(lines, "Select + and press Enter to create a terminal")
	}
	return append(lines, "Select a tab and press Enter")
}

func renderTabBar(tabs []model.Tab, active, width int) string {
	segments := make([]styledSegment, 0, len(tabs)*2+2)
	for index, tab := range tabs {
		if index > 0 {
			segments = append(segments, styledSegment{text: " "})
		}
		style := tabStyle
		if index == active {
			style = activeTabStyle
		}
		segments = append(segments, styledSegment{text: " " + tab.Name + " ", style: style})
	}
	if len(tabs) > 0 {
		segments = append(segments, styledSegment{text: " "})
	}
	style := tabStyle
	if active == len(tabs) {
		style = activeTabStyle
	}
	segments = append(segments, styledSegment{text: " + ", style: style})
	return renderStyled(width, segments)
}

func renderShortcuts(width int, values ...shortcut) string {
	segments := make([]styledSegment, 0, len(values)*3)
	for index, value := range values {
		if index > 0 {
			segments = append(segments, styledSegment{text: "  "})
		}
		segments = append(segments,
			styledSegment{text: "[" + value.key + "]", style: focusStyle},
			styledSegment{text: " " + value.description},
		)
	}
	return renderStyled(width, segments)
}

func renderStyled(width int, segments []styledSegment) string {
	var result strings.Builder
	remaining := width
	for _, segment := range segments {
		if remaining <= 0 {
			break
		}
		text := truncate(segment.text, remaining)
		if segment.style != "" {
			result.WriteString(segment.style)
		}
		result.WriteString(text)
		if segment.style != "" {
			result.WriteString(resetStyle)
		}
		remaining -= len([]rune(text))
	}
	return result.String()
}

func truncate(value string, width int) string {
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width <= 1 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
}

func pad(value string, width int) string {
	missing := width - len([]rune(value))
	if missing <= 0 {
		return value
	}
	return value + strings.Repeat(" ", missing)
}
