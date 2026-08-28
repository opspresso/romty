// Laying the screen out and drawing it: the pane split, the workspace tree, the
// tab rail, the status row, and the pieces of text they are all built from.
package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/opspresso/romty/internal/model"
)

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

// layout is the one description of where things sit. It used to be four
// unnamed ints, which every one of nineteen call sites unpacked positionally:
// swapping two widths compiled, rendered, and only looked wrong.
type layout struct {
	leftWidth int
	// separator is what sits between the panes, which is zero while the
	// workspace pane is hidden. The origin and the mouse translation read it
	// rather than separatorWidth, because a hidden pane draws no divider.
	separator      int
	rightWidth     int
	bodyHeight     int
	terminalHeight int
}

// terminalOrigin is where the terminal's own first cell lands on screen, which
// the cursor and mouse translation both need. Deriving it here keeps it tied
// to the separator and tab bar rather than repeated as a literal.
func (l layout) terminalOrigin() (int, int) {
	return l.leftWidth + l.separator, terminalTop
}

// paneWidth is the workspace pane's width as configured, whether or not the
// narrow layout is currently showing it. Config reads and adjusts this one,
// because a hidden pane still has a width to come back to.
func (m dashboard) paneWidth() int {
	width := max(m.width, 40)
	leftWidth := m.leftWidth
	if leftWidth == 0 {
		leftWidth = min(max(width/4, minimumLeftWidth), 28)
	}
	return min(leftWidth, width-20)
}

// navigationHidden reports whether the terminal has the whole screen. The
// workspace tree is not what the keyboard is in, and on a narrow screen it is
// half of what the terminal could be, so focus takes it away and Ctrl+/ or F7
// brings it back.
func (m dashboard) navigationHidden() bool {
	if max(m.width, 40) >= narrowLayoutWidth {
		return false
	}
	return m.focus == terminalPane && m.terminal != nil
}

// gitDiffLayout is the file view's own split. The file view has two panes of
// its own, so the narrow layout does not apply to it, and it is kept out of
// dimensions rather than folded into it: the terminal is not on screen while
// the view is open, and sizing the PTY to a split it never appears in made a
// full-screen guest reflow on the way in and again on the way out.
func (m dashboard) gitDiffLayout() layout {
	width := max(m.width, 40)
	view := m.dimensions()
	view.leftWidth = m.paneWidth()
	view.separator = separatorWidth
	view.rightWidth = max(width-view.leftWidth-separatorWidth, 17)
	return view
}

func (m dashboard) dimensions() layout {
	width := max(m.width, 40)
	height := max(m.height, 10)
	leftWidth, separator := m.paneWidth(), separatorWidth
	if m.navigationHidden() {
		leftWidth, separator = 0, 0
	}
	bodyHeight := height - 2
	return layout{
		leftWidth:      leftWidth,
		separator:      separator,
		rightWidth:     max(width-leftWidth-separator, 17),
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

func (m dashboard) render() string {
	view := m.dimensions()
	width := max(m.width, 40)
	// Scrollback replaces the split rather than drawing over it, so the panes
	// it hides are not built at all: rendering both meant every frame drew the
	// workspace tree and a second terminal viewport it then threw away.
	var lines []string
	switch {
	case m.gitDiff.active:
		diff := m.gitDiffLayout()
		lines = m.renderGitDiffPanes(diff.leftWidth, diff.rightWidth, diff.bodyHeight)
	case m.scrollback || m.navigationHidden():
		// The narrow layout lays out the way scrollback does, because a pane
		// that is hidden is not a pane to leave empty beside a divider.
		lines = m.renderRows(m.renderTerminal(width), width, view.bodyHeight)
	default:
		lines = m.renderPanes(view.leftWidth, view.rightWidth, view.bodyHeight)
	}
	if m.modal == workspaceActionsModal {
		lines = m.overlayWorkspaceActions(lines, width, view.bodyHeight)
	} else if m.modal != noModal {
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
	case m.tabPending || m.restorePending:
		status = m.renderActivityStatus("OPENING", "preparing terminal", width)
	case m.tabClosePending != "":
		status = m.renderActivityStatus("CLOSING", "terminating terminal", width)
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
			shortcut{key: "m", description: "scrollback mouse"},
			shortcut{key: "d/b", description: "sounds"},
			shortcut{key: "s", description: "test"},
			shortcut{key: "Esc", description: "close"},
		)
	case m.modal == workspaceActionsModal:
		status = renderShortcuts(m.styles, width,
			shortcut{key: "↑/↓", description: "select"},
			shortcut{key: "Enter", description: "run"},
			shortcut{key: "Esc", description: "cancel"},
		)
	case m.modal == gitActionsModal && m.gitActionPending:
		status = m.renderActivityStatus("RUNNING", m.gitAction.label()+" in "+displayText(m.gitActionTarget.Name), width)
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
		status = m.renderActivityStatus("STOPPING", "waiting for the daemon to stop", width)
	case m.modal == browseModal && m.browse.loading:
		status = m.renderActivityStatus("READING", displayText(m.browse.path), width)
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
	case m.modal == closeTabModal:
		status = renderShortcuts(m.styles, width,
			shortcut{key: "Enter", description: "close tab"},
			shortcut{key: "Esc", description: "cancel"},
		)
	case m.modal == shutdownModal:
		status = renderShortcuts(m.styles, width,
			shortcut{key: "Enter", description: "stop daemon"},
			shortcut{key: "Esc", description: "cancel"},
		)
	case m.modal == hookInstallModal && m.hookInstallPending:
		status = m.renderActivityStatus("INSTALLING", "updating agent status hooks", width)
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
	case m.scrollback && m.searchMode:
		status = truncate(
			m.styles.promptLabel.Render(" FIND ")+" "+
				m.styles.promptText.Render(displayText(m.searchQuery))+
				m.styles.dividerActive.Render("█"),
			width,
		)
	case m.scrollback:
		shortcuts := []shortcut{
			{key: "↑/↓", description: "line"},
			{key: "PgUp/PgDn", description: "page"},
			{key: "/", description: "find"},
		}
		position := m.scrollbackPosition()
		if len(m.searchMatches) > 0 {
			shortcuts = append(shortcuts, shortcut{key: "n/N", description: "match"})
			position += fmt.Sprintf("  %s %d/%d", displayText(m.searchQuery),
				len(m.searchMatches)-m.searchIndex, len(m.searchMatches))
		}
		shortcuts = append(shortcuts, shortcut{key: "Ctrl+Shift+\\", description: "exit"})
		status = truncate(
			m.styles.promptLabel.Render(" SCROLLBACK ")+" "+
				m.styles.shortcutDescription.Render(position)+"  "+
				renderShortcuts(m.styles, width, shortcuts...),
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
			contextShortcuts = []shortcut{{key: "Ctrl+/", description: "navigation"}}
		}
		rail = renderShortcutRailNote(m.styles, width, m.agentLedger(), contextShortcuts...)
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

func (m dashboard) renderActivityStatus(title, description string, width int) string {
	frame := agentAnimationFrames[m.agentAnimationFrame%len(agentAnimationFrames)]
	label := m.styles.promptLabel.Render(" " + frame + " " + title + " ")
	return truncate(label+" "+m.styles.shortcutDescription.Render(description), width)
}

// paneSeparators returns the divider for the first body row, which carries the
// focus arrow, and the one shared by every remaining row.
func (m dashboard) paneSeparators() (string, string) {
	dividerStyle := m.styles.divider
	if m.navigationResize || m.hover.kind == hoverDivider {
		dividerStyle = m.styles.dividerActive
	}
	divider := dividerStyle.Render("│")
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
	start, end := navigationWindow(items, m.navOffset, available)
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

func navigationWindow(items []navItem, offset, available int) (int, int) {
	if len(items) == 0 || available <= 0 {
		return 0, 0
	}
	lastStart := len(items) - 1
	used := navigationRows(items[lastStart])
	for lastStart > 0 && used+navigationRows(items[lastStart-1]) <= available {
		lastStart--
		used += navigationRows(items[lastStart])
	}
	start := min(max(offset, 0), lastStart)
	end := start
	used = 0
	for end < len(items) && used+navigationRows(items[end]) <= available {
		used += navigationRows(items[end])
		end++
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
	} else if m.hover.kind == hoverNavigation && m.hover.index == index {
		style = m.styles.hovered(style)
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

func (m dashboard) renderTerminal(width int) []string {
	tabs := m.selectedTabs()
	if m.focus == leftPane {
		tabs = m.navigationTabs()
	}
	hover := -1
	closeHover := -1
	if m.hover.kind == hoverTab {
		hover = m.hover.index
	} else if m.hover.kind == hoverTabClose {
		closeHover = m.hover.index
	}
	lines := renderTabBarWithHover(m.styles, tabs, m.tabIndex, hover, closeHover, width)
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
	return renderTabBarWithHover(styles, tabs, active, -1, -1, width)
}

func renderTabBarWithHover(styles *uiStyles, tabs []model.Tab, active, hover, closeHover, width int) []string {
	labels := make([]string, 0, len(tabs)+1)
	for _, tab := range tabs {
		labels = append(labels, "  "+displayText(tab.Name)+"  × ")
	}
	labels = append(labels, "  +  ")

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
		} else if index == hover {
			style = styles.interactiveHover
			railStyle = styles.dividerActive
		}
		if index < len(tabs) && index == closeHover {
			tabsLine.WriteString(style.Render(strings.TrimSuffix(label, "× ")))
			tabsLine.WriteString(styles.interactiveHover.Render("× "))
		} else {
			tabsLine.WriteString(style.Render(label))
		}
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
	return renderShortcutRailNote(styles, width, "", values...)
}

// renderShortcutRailNote draws a note into the rule the rail is otherwise
// filled with, so a reading that is worth glancing at costs no row of its own.
// A note that does not fit is dropped rather than pushing the shortcuts off.
func renderShortcutRailNote(styles *uiStyles, width int, note string, values ...shortcut) string {
	shortcuts := renderShortcuts(styles, width, values...)
	fill := max(width-lipgloss.Width(shortcuts)-1, 0)
	if note != "" {
		rendered := styles.shortcutDescription.Render(note)
		if used := lipgloss.Width(rendered) + 3; used <= fill {
			return styles.tabRail.Render("─") + " " + rendered + " " +
				styles.tabRail.Render(strings.Repeat("─", fill-used)) + " " + shortcuts
		}
	}
	if fill == 0 {
		return shortcuts
	}
	return styles.tabRail.Render(strings.Repeat("─", fill)) + " " + shortcuts
}
