package ui

import (
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nalbam/romty/internal/model"
)

const (
	terminalTop    = 4
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
	mouseFocusMode      bool
	input               string
	errorMessage        string
	terminal            *embeddedTerminal
}

type snapshotMsg struct {
	value model.Snapshot
	err   error
}

type workspaceMsg struct {
	value    model.Workspace
	snapshot model.Snapshot
	err      error
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

func Run(backend Backend, initial model.Snapshot) (Result, error) {
	program := tea.NewProgram(newDashboard(backend, initial))
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
	return dashboard{backend: backend, state: initial, width: 80, height: 24}
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
	case tea.MouseClickMsg:
		return m.handleMouseClick(message)
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
	}
	return m, nil
}

func (m dashboard) handleKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.inputMode {
		return m.handleInput(message)
	}
	if message.String() == "ctrl+g" {
		m.mouseFocusMode = true
		return m, nil
	}
	if m.mouseFocusMode && message.String() == "esc" {
		m.mouseFocusMode = false
		return m, nil
	}
	if m.focus == terminalPane {
		if message.String() == "ctrl+\\" {
			m.focus = leftPane
			return m, nil
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
		}
	case "a":
		m.inputMode = true
		m.input = ""
		m.errorMessage = ""
	case "r":
		return m, m.refresh()
	case "+":
		return m, m.createTab()
	case "up", "k":
		m.moveNavigation(-1)
	case "down", "j":
		m.moveNavigation(1)
	case "left", "h":
		m.moveTab(-1)
		return m, m.openSelectedTerminal()
	case "right", "l":
		m.moveTab(1)
		return m, m.openSelectedTerminal()
	case "enter":
		return m, m.selectWorkspace()
	}
	return m, nil
}

func (m dashboard) handleMouseClick(message tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if !m.mouseFocusMode || message.Button != tea.MouseLeft {
		return m, nil
	}
	m.mouseFocusMode = false
	leftWidth, _, bodyHeight, _ := m.dimensions()
	if message.Y < 0 || message.Y >= bodyHeight {
		return m, nil
	}
	if message.X >= 0 && message.X < leftWidth {
		m.focus = leftPane
	} else if message.X >= leftWidth+3 && m.terminal != nil && m.terminal.active {
		m.focus = terminalPane
	}
	return m, nil
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
	m.closeTerminal()
	m.selectedWorkspaceID = message.value.ID
	m.selectedPath = message.value.Path
	m.state = message.snapshot
	tabs := m.selectedTabs()
	m.tabIndex = firstRunningTab(tabs)
	m.focus = leftPane
	m.errorMessage = ""
	if len(tabs) == 0 {
		return m, m.createTab()
	}
	return m, m.openSelectedTerminal()
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
	items := m.navigationItems()
	if len(items) == 0 || m.navIndex >= len(items) || items[m.navIndex].isRoot {
		return nil
	}
	item := items[m.navIndex]
	return func() tea.Msg {
		workspace, err := m.backend.EnsureWorkspace(item.root.ID, item.workspace.Path)
		if err != nil {
			return workspaceMsg{err: err}
		}
		snapshot, err := m.backend.Snapshot()
		return workspaceMsg{value: workspace, snapshot: snapshot, err: err}
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
	count := len(m.navigationItems())
	if count == 0 {
		m.navIndex = 0
		return
	}
	m.navIndex = (m.navIndex + delta + count) % count
}

func (m *dashboard) moveTab(delta int) {
	count := len(m.selectedTabs())
	if count == 0 {
		m.tabIndex = 0
		return
	}
	m.tabIndex = (m.tabIndex + delta + count) % count
}

func (m *dashboard) clampTabIndex() {
	count := len(m.selectedTabs())
	if count == 0 {
		m.tabIndex = 0
	} else if m.tabIndex >= count {
		m.tabIndex = count - 1
	}
}

func (m *dashboard) syncSelection() {
	for _, root := range m.state.Roots {
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
		result = append(result, navItem{root: root.Root, isRoot: true})
		for _, directory := range root.Directories {
			result = append(result, navItem{root: root.Root, workspace: directory.Workspace, tabs: directory.Tabs})
		}
	}
	return result
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
		for _, directory := range root.Directories {
			if directory.Workspace.ID == m.selectedWorkspaceID {
				result := make([]model.Tab, 0, len(directory.Tabs))
				for _, tab := range directory.Tabs {
					if tab.Running {
						result = append(result, tab)
					}
				}
				return result
			}
		}
	}
	return nil
}

func firstRunningTab(tabs []model.Tab) int {
	for index, tab := range tabs {
		if tab.Running {
			return index
		}
	}
	return 0
}

func (m dashboard) dimensions() (int, int, int, int) {
	width := max(m.width, 40)
	height := max(m.height, 10)
	leftWidth := min(max(width/3, 20), 40)
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
	if m.mouseFocusMode {
		view.MouseMode = tea.MouseModeCellMotion
	}
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
		if row == 0 {
			if m.focus == leftPane {
				leftLine = focusStyle + leftLine + resetStyle
			} else {
				rightLine = focusStyle + rightLine + resetStyle
			}
		} else if row == m.navIndex+2 {
			leftLine = selectStyle + leftLine + resetStyle
		}
		lines = append(lines, leftLine+" │ "+rightLine)
	}
	width := max(m.width, 40)
	status := renderShortcuts(width,
		shortcut{key: "a", description: "add root"},
		shortcut{key: "Enter", description: "workspace"},
		shortcut{key: "+", description: "new tab"},
		shortcut{key: "h/l", description: "tabs"},
		shortcut{key: "Tab", description: "terminal"},
		shortcut{key: "Ctrl+G", description: "mouse"},
		shortcut{key: "q", description: "quit"},
	)
	if m.focus == terminalPane {
		status = renderShortcuts(width,
			shortcut{key: "Ctrl+G", description: "mouse focus"},
			shortcut{key: "Ctrl+\\", description: "workspaces"},
		)
	}
	if m.inputMode {
		status = truncate("Root folder: "+m.input+"_", width)
	} else if m.mouseFocusMode {
		status = renderShortcuts(width,
			shortcut{key: "Click", description: "focus pane"},
			shortcut{key: "Esc", description: "cancel"},
		)
	} else if m.errorMessage != "" {
		status = truncate("Error: "+m.errorMessage, width)
	}
	lines = append(lines, strings.Repeat("─", min(width, 120)), status)
	return strings.Join(lines, "\n")
}

func (m dashboard) renderNavigation() []string {
	lines := []string{"Workspaces", ""}
	items := m.navigationItems()
	for _, item := range items {
		if item.isRoot {
			lines = append(lines, "▾ "+item.root.Name)
		} else {
			line := "  " + item.workspace.Name
			if markers := openTabMarkers(item.tabs); markers != "" {
				line += "  " + markers
			}
			lines = append(lines, line)
		}
	}
	if len(items) == 0 {
		lines = append(lines, "  Press a to add a root")
	}
	return lines
}

func (m dashboard) renderTerminal(width int) []string {
	lines := []string{"Terminal"}
	if m.selectedWorkspaceID == "" {
		return append(lines, "", "Select a workspace on the left")
	}
	lines = append(lines, truncate(m.selectedPath, width))
	tabs := m.selectedTabs()
	if len(tabs) == 0 {
		return append(lines, renderTabBar(tabs, m.tabIndex, width), strings.Repeat("─", width), "Creating a terminal...")
	}
	lines = append(lines, renderTabBar(tabs, m.tabIndex, width), strings.Repeat("─", width))
	if m.terminal == nil {
		return append(lines, "Select a running tab with h/l")
	}
	return append(lines, m.terminal.render()...)
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
	segments = append(segments, styledSegment{text: " + ", style: tabStyle})
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
