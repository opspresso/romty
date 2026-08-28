// Workspace actions: the context palette for the highlighted root or
// directory, including its direct terminal, Git, and removal operations.
package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type workspaceAction int

const (
	workspaceOpenTerminalAction workspaceAction = iota
	workspaceNewTabAction
	workspaceFileChangesAction
	workspaceGitStatusAction
	workspaceGitFetchAction
	workspaceGitPullAction
	workspaceGitPushAction
	workspaceRemoveAction
)

type workspaceActionGroup int

const (
	workspaceTerminalGroup workspaceActionGroup = iota
	workspaceGitGroup
	workspaceDestructiveGroup
)

type workspaceActionChoice struct {
	action      workspaceAction
	label       string
	group       workspaceActionGroup
	destructive bool
}

type workspaceActionRow struct {
	choiceIndex int
	divider     bool
}

func (m dashboard) openWorkspaceActions() (tea.Model, tea.Cmd) {
	if m.modal != noModal || m.gitActionPending {
		return m, nil
	}
	target, ok := m.navigationItem()
	if !ok {
		return m, nil
	}
	m.ensureNavigationVisible()
	row, ok := m.navigationRowForIndex(m.navIndex, m.dimensions().bodyHeight)
	if !ok {
		return m, nil
	}
	return m.openWorkspaceActionsAt(target, max(m.dimensions().leftWidth-1, 0), row)
}

func (m dashboard) openWorkspaceActionsAt(target navItem, x, y int) (tea.Model, tea.Cmd) {
	m.modal = workspaceActionsModal
	m.workspaceActionTarget = target
	m.workspaceActionIndex = 0
	m.workspaceActionOffset = 0
	m.workspaceActionAnchorX = x
	m.workspaceActionAnchorY = y
	m.clearAnyError()
	return m, nil
}

func (m dashboard) navigationRowForIndex(want, height int) (int, bool) {
	items := m.navigationItems()
	start, end := navigationWindow(items, m.navOffset, max(height-2, 0))
	row := 2
	for index := start; index < end; index++ {
		if index == want {
			if items[index].isRoot && items[index].separator {
				row++
			}
			return row, true
		}
		row += navigationRows(items[index])
	}
	return 0, false
}

func (m dashboard) workspaceActionChoices() []workspaceActionChoice {
	target := m.workspaceActionTarget
	choices := make([]workspaceActionChoice, 0, 8)
	if target.failure == "" {
		if len(runningTabs(target.tabs)) > 0 {
			choices = append(choices, workspaceActionChoice{
				action: workspaceOpenTerminalAction, label: "Open terminal",
			})
		}
		choices = append(choices, workspaceActionChoice{
			action: workspaceNewTabAction, label: "New tab",
		})
		if target.hasGit {
			choices = append(choices,
				workspaceActionChoice{action: workspaceFileChangesAction, label: "File changes", group: workspaceGitGroup},
				workspaceActionChoice{action: workspaceGitStatusAction, label: "Git status", group: workspaceGitGroup},
				workspaceActionChoice{action: workspaceGitFetchAction, label: "Git fetch", group: workspaceGitGroup},
				workspaceActionChoice{action: workspaceGitPullAction, label: "Git pull", group: workspaceGitGroup},
				workspaceActionChoice{action: workspaceGitPushAction, label: "Git push", group: workspaceGitGroup},
			)
		}
	}
	remove := workspaceActionChoice{
		action: workspaceRemoveAction, label: "Delete workspace", group: workspaceDestructiveGroup, destructive: true,
	}
	if target.isRoot {
		remove.label = "Forget root"
	}
	choices = append(choices, remove)
	return choices
}

func (m dashboard) workspaceActionRows() []workspaceActionRow {
	choices := m.workspaceActionChoices()
	rows := make([]workspaceActionRow, 0, len(choices)+2)
	for index, choice := range choices {
		if index > 0 && choice.group != choices[index-1].group {
			rows = append(rows, workspaceActionRow{divider: true})
		}
		rows = append(rows, workspaceActionRow{choiceIndex: index})
	}
	return rows
}

func (m dashboard) workspaceActionCapacity(height int) int {
	return max(height-2, 1)
}

func (m dashboard) workspaceActionWindow(height int) (int, int) {
	count := len(m.workspaceActionRows())
	capacity := m.workspaceActionCapacity(height)
	offset := min(max(m.workspaceActionOffset, 0), max(count-capacity, 0))
	return offset, min(offset+capacity, count)
}

func (m *dashboard) moveWorkspaceAction(delta int) {
	choices := m.workspaceActionChoices()
	if len(choices) == 0 {
		m.workspaceActionIndex, m.workspaceActionOffset = 0, 0
		return
	}
	m.workspaceActionIndex = (m.workspaceActionIndex + delta + len(choices)) % len(choices)
	m.ensureWorkspaceActionVisible()
}

func (m *dashboard) ensureWorkspaceActionVisible() {
	count := len(m.workspaceActionChoices())
	if count == 0 {
		m.workspaceActionOffset = 0
		return
	}
	capacity := m.workspaceActionCapacity(m.dimensions().bodyHeight)
	m.workspaceActionIndex = min(max(m.workspaceActionIndex, 0), count-1)
	rows := m.workspaceActionRows()
	selectedRow := 0
	for index, row := range rows {
		if !row.divider && row.choiceIndex == m.workspaceActionIndex {
			selectedRow = index
			break
		}
	}
	maximum := max(len(rows)-capacity, 0)
	m.workspaceActionOffset = min(max(m.workspaceActionOffset, 0), maximum)
	if selectedRow < m.workspaceActionOffset {
		m.workspaceActionOffset = selectedRow
	}
	if selectedRow >= m.workspaceActionOffset+capacity {
		m.workspaceActionOffset = selectedRow - capacity + 1
	}
}

func (m dashboard) handleWorkspaceActionKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	choices := m.workspaceActionChoices()
	if len(choices) == 0 {
		return m, nil
	}
	page := m.workspaceActionCapacity(m.dimensions().bodyHeight)
	switch message.String() {
	case "up", "k":
		m.moveWorkspaceAction(-1)
	case "down", "j":
		m.moveWorkspaceAction(1)
	case "pgup", "ctrl+b":
		m.moveWorkspaceAction(-page)
	case "pgdown", "ctrl+f":
		m.moveWorkspaceAction(page)
	case "home", "g":
		m.workspaceActionIndex = 0
		m.ensureWorkspaceActionVisible()
	case "end", "G":
		m.workspaceActionIndex = len(choices) - 1
		m.ensureWorkspaceActionVisible()
	case "enter":
		return m.runWorkspaceAction(choices[m.workspaceActionIndex].action)
	}
	return m, nil
}

func (m dashboard) runWorkspaceAction(action workspaceAction) (tea.Model, tea.Cmd) {
	target, ok := m.currentWorkspaceActionTarget()
	if !ok {
		m.modal = noModal
		m.setNotice(treeError, "selected workspace no longer exists")
		return m, nil
	}
	m.workspaceActionTarget = target
	switch action {
	case workspaceOpenTerminalAction:
		tabs := runningTabs(target.tabs)
		if len(tabs) == 0 {
			m.modal = noModal
			m.setNotice(terminalError, "selected workspace has no running terminal")
			return m, nil
		}
		m.tabIndex = min(m.tabIndex, len(tabs)-1)
		m.modal = noModal
		return m, m.selectWorkspace()
	case workspaceNewTabAction:
		m.tabIndex = len(runningTabs(target.tabs))
		m.modal = noModal
		return m, m.selectWorkspace()
	case workspaceFileChangesAction:
		m.modal = noModal
		return m.openGitDiffView(target)
	case workspaceGitStatusAction, workspaceGitFetchAction, workspaceGitPullAction, workspaceGitPushAction:
		return m.startWorkspaceGitAction(action, target)
	case workspaceRemoveAction:
		m.removeTarget = target
		return m.openModal(removeSelectionModal)
	default:
		m.modal = noModal
		m.setError(treeError, fmt.Sprintf("unknown workspace action %d", action))
		return m, nil
	}
}

func (m *dashboard) currentWorkspaceActionTarget() (navItem, bool) {
	target := m.workspaceActionTarget
	for index, item := range m.navigationItems() {
		if item.root.ID == target.root.ID && item.workspace.Path == target.workspace.Path {
			m.setNavigation(index)
			return item, true
		}
	}
	return navItem{}, false
}

func (m dashboard) startWorkspaceGitAction(action workspaceAction, target navItem) (tea.Model, tea.Cmd) {
	gitActionByWorkspaceAction := map[workspaceAction]gitAction{
		workspaceGitStatusAction: gitStatusAction,
		workspaceGitFetchAction:  gitFetchAction,
		workspaceGitPullAction:   gitPullAction,
		workspaceGitPushAction:   gitPushAction,
	}
	gitAction, ok := gitActionByWorkspaceAction[action]
	if !ok || !target.hasGit {
		m.modal = noModal
		m.setError(gitError, "selected workspace is not a Git repository")
		return m, nil
	}
	m.modal = gitActionsModal
	m.gitActionTarget = target.workspace
	m.gitActionIndex = int(gitAction)
	m.gitAction = gitAction
	m.gitActionPending = false
	m.gitActionComplete = false
	m.gitActionOutput = ""
	m.gitActionError = ""
	m.gitActionOffset = 0
	m.gitActionReturn = workspaceActionsModal
	m.clearAnyError()
	return m.startGitAction()
}

func (m dashboard) workspaceActionPopup(width, height int) (lines []string, x, y int) {
	choices := m.workspaceActionChoices()
	rows := m.workspaceActionRows()
	offset, end := m.workspaceActionWindow(height)
	labelWidth := 6
	for _, row := range rows[offset:end] {
		if !row.divider {
			labelWidth = max(labelWidth, lipgloss.Width(choices[row.choiceIndex].label))
		}
	}
	popupWidth := min(max(labelWidth+3, 12), max(width, 1))
	innerWidth := max(popupWidth-2, 0)
	lines = make([]string, 0, end-offset+2)
	lines = append(lines, m.styles.modalBorder.Render("╭"+strings.Repeat("─", innerWidth)+"╮"))
	for _, row := range rows[offset:end] {
		if row.divider {
			lines = append(lines, m.styles.modalBorder.Render("│")+
				m.styles.divider.Render(strings.Repeat("─", innerWidth))+
				m.styles.modalBorder.Render("│"))
			continue
		}
		choice := choices[row.choiceIndex]
		style := m.styles.modalBody
		if choice.destructive {
			style = m.styles.errorText
		}
		if row.choiceIndex == m.workspaceActionIndex {
			style = m.styles.navigationSelected
		} else if m.hover.kind == hoverWorkspaceAction && m.hover.index == row.choiceIndex {
			style = m.styles.hovered(style)
		}
		label := " " + choice.label
		lines = append(lines, m.styles.modalBorder.Render("│")+
			style.Render(pad(truncate(label, innerWidth), innerWidth))+
			m.styles.modalBorder.Render("│"))
	}
	lines = append(lines, m.styles.modalBorder.Render("╰"+strings.Repeat("─", innerWidth)+"╯"))
	x = min(max(m.workspaceActionAnchorX, 0), max(width-popupWidth, 0))
	y = min(max(m.workspaceActionAnchorY, 0), max(height-len(lines), 0))
	return lines, x, y
}

func (m dashboard) overlayWorkspaceActions(base []string, width, height int) []string {
	popup, x, y := m.workspaceActionPopup(width, height)
	result := append([]string(nil), base...)
	for index, popupLine := range popup {
		row := y + index
		if row < 0 || row >= len(result) || row >= height {
			continue
		}
		line := pad(truncate(result[row], width), width)
		popupWidth := lipgloss.Width(popupLine)
		result[row] = ansi.Cut(line, 0, x) + popupLine + ansi.Cut(line, x+popupWidth, width)
	}
	return result
}

func (m dashboard) handleWorkspaceActionsMouse(message tea.MouseMsg) (tea.Model, tea.Cmd, bool) {
	mouse := message.Mouse()
	width, height := max(m.width, 40), m.dimensions().bodyHeight
	if wheel, ok := message.(tea.MouseWheelMsg); ok {
		popup, x, y := m.workspaceActionPopup(width, height)
		popupWidth := 0
		if len(popup) > 0 {
			popupWidth = lipgloss.Width(popup[0])
		}
		if mouse.X < x || mouse.X >= x+popupWidth || mouse.Y < y || mouse.Y >= y+len(popup) {
			return m, nil, true
		}
		switch wheel.Button {
		case tea.MouseWheelUp:
			m.moveWorkspaceAction(-3)
		case tea.MouseWheelDown:
			m.moveWorkspaceAction(3)
		default:
			return m, nil, false
		}
		m.hover = m.hoverTargetAt(mouse)
		return m, nil, true
	}
	click, ok := message.(tea.MouseClickMsg)
	if !ok {
		return m, nil, false
	}
	index, hit := m.workspaceActionIndexAtMouse(mouse, width, height)
	if click.Button != tea.MouseLeft || !hit {
		m.modal = noModal
		return m, nil, true
	}
	m.workspaceActionIndex = index
	choices := m.workspaceActionChoices()
	updated, command := m.runWorkspaceAction(choices[index].action)
	return updated, command, true
}

func (m dashboard) workspaceActionIndexAtMouse(mouse tea.Mouse, width, height int) (int, bool) {
	popup, x, y := m.workspaceActionPopup(width, height)
	if len(popup) < 2 {
		return 0, false
	}
	popupWidth := lipgloss.Width(popup[0])
	if mouse.X <= x || mouse.X >= x+popupWidth-1 || mouse.Y <= y || mouse.Y >= y+len(popup)-1 {
		return 0, false
	}
	offset, end := m.workspaceActionWindow(height)
	rowIndex := offset + mouse.Y - y - 1
	if rowIndex < offset || rowIndex >= end {
		return 0, false
	}
	row := m.workspaceActionRows()[rowIndex]
	if row.divider {
		return 0, false
	}
	return row.choiceIndex, true
}
