// Workspace actions: the context palette for the highlighted root or
// directory, including its direct terminal, file, Git, and removal operations.

package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/opspresso/romty/internal/display"
	"github.com/opspresso/romty/internal/model"
)

type workspaceAction int

const (
	workspaceOpenTerminalAction workspaceAction = iota
	workspaceNewTabAction
	workspaceCloseTabAction
	workspaceReopenAction
	workspaceAllFilesAction
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
	workspaceClosedGroup
	workspaceFileGitGroup
	workspaceDestructiveGroup
)

type workspaceActionChoice struct {
	action      workspaceAction
	label       string
	group       workspaceActionGroup
	destructive bool
	// closedID names the closed session a reopen action restores.
	closedID string
}

// workspacePalette is the open workspace action palette.
type workspacePalette struct {
	// target is captured when the palette opens, so a refresh cannot
	// retarget a destructive or remote action behind it.
	target navItem
	// sessionChoice limits the palette to a new session and the target's
	// closed sessions, drawn in the middle of the terminal pane instead of
	// on the row. Selecting a workspace without a running terminal opens it
	// this way instead of starting a shell at once.
	sessionChoice bool
	index         int
	offset        int
	// anchorX and anchorY are the screen cell the palette opened at.
	anchorX int
	anchorY int
}

type workspaceActionRow struct {
	choiceIndex int
	divider     bool
}

func (m dashboard) openWorkspaceActions() (tea.Model, tea.Cmd) {
	if m.modal != noModal || m.gitRun.pending {
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
	m.palette = workspacePalette{target: target, anchorX: x, anchorY: y}
	m.clearAnyError()
	return m, nil
}

// needsSessionChoice reports whether selecting item asks which session to
// open: a readable workspace with no running terminal.
// needsSessionChoice reports a workspace whose Enter has a real choice to
// make: it has no running terminal and has closed sessions to reopen. With
// nothing to reopen the only answer is a new session, so Enter starts one.
func (m dashboard) needsSessionChoice(item navItem) bool {
	return item.failure == "" && len(runningTabs(item.tabs)) == 0 && len(item.closed) > 0
}

// activateWorkspace is Enter on the workspace tree: it opens the selected
// terminal, or asks which session to open when none is running.
func (m dashboard) activateWorkspace() (tea.Model, tea.Cmd) {
	item, ok := m.navigationItem()
	if !ok || !m.needsSessionChoice(item) || m.tabPending {
		return m, m.selectWorkspace()
	}
	m.ensureNavigationVisible()
	return m.openSessionChoice(item)
}

func (m dashboard) openSessionChoice(target navItem) (tea.Model, tea.Cmd) {
	if m.modal != noModal || m.tabPending {
		return m, nil
	}
	m.modal = workspaceActionsModal
	m.palette = workspacePalette{target: target, sessionChoice: true}
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
	target := m.palette.target
	if m.palette.sessionChoice {
		return sessionChoices(target, time.Now())
	}
	choices := make([]workspaceActionChoice, 0, 10)
	if target.failure == "" {
		if len(runningTabs(target.tabs)) > 0 {
			choices = append(choices, workspaceActionChoice{
				action: workspaceOpenTerminalAction, label: "Open terminal",
			})
		}
		choices = append(choices, workspaceActionChoice{
			action: workspaceNewTabAction, label: "New tab",
		})
		if m.tabIndex < len(runningTabs(target.tabs)) {
			choices = append(choices, workspaceActionChoice{
				action: workspaceCloseTabAction, label: "Close tab",
			})
		}
		if !target.isRoot {
			choices = append(choices, workspaceActionChoice{
				action: workspaceAllFilesAction, label: "All files", group: workspaceFileGitGroup,
			})
		}
		if target.hasGit {
			choices = append(choices,
				workspaceActionChoice{action: workspaceFileChangesAction, label: "File changes", group: workspaceFileGitGroup},
				workspaceActionChoice{action: workspaceGitStatusAction, label: "Git status", group: workspaceFileGitGroup},
				workspaceActionChoice{action: workspaceGitFetchAction, label: "Git fetch", group: workspaceFileGitGroup},
				workspaceActionChoice{action: workspaceGitPullAction, label: "Git pull", group: workspaceFileGitGroup},
				workspaceActionChoice{action: workspaceGitPushAction, label: "Git push", group: workspaceFileGitGroup},
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

// sessionChoices offers a new session first, then the closed sessions newest
// first.
func sessionChoices(target navItem, now time.Time) []workspaceActionChoice {
	choices := []workspaceActionChoice{{action: workspaceNewTabAction, label: "New session"}}
	for _, closed := range target.closed {
		choices = append(choices, workspaceActionChoice{
			action: workspaceReopenAction, label: closedSessionLabel(closed, now),
			group: workspaceClosedGroup, closedID: closed.ID,
		})
	}
	return choices
}

// closedSessionLabel names a closed session by its tab, its agent, and when
// it closed: the time alone today, the date as well before that.
func closedSessionLabel(closed model.ClosedSession, now time.Time) string {
	parts := []string{"Reopen " + closed.Name}
	if closed.Agent != "" {
		parts = append(parts, string(closed.Agent))
	}
	at := closed.ClosedAt.Local()
	layout := "01-02 15:04"
	if year, month, day := at.Date(); now.Local().Year() == year && now.Local().Month() == month && now.Local().Day() == day {
		layout = "15:04"
	}
	parts = append(parts, at.Format(layout))
	return strings.Join(parts, " · ")
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
	offset := min(max(m.palette.offset, 0), max(count-capacity, 0))
	return offset, min(offset+capacity, count)
}

func (m *dashboard) moveWorkspaceAction(delta int) {
	choices := m.workspaceActionChoices()
	if len(choices) == 0 {
		m.palette.index, m.palette.offset = 0, 0
		return
	}
	m.palette.index = ((m.palette.index+delta)%len(choices) + len(choices)) % len(choices)
	m.ensureWorkspaceActionVisible()
}

func (m *dashboard) ensureWorkspaceActionVisible() {
	count := len(m.workspaceActionChoices())
	if count == 0 {
		m.palette.offset = 0
		return
	}
	capacity := m.workspaceActionCapacity(m.dimensions().bodyHeight)
	m.palette.index = min(max(m.palette.index, 0), count-1)
	rows := m.workspaceActionRows()
	selectedRow := 0
	for index, row := range rows {
		if !row.divider && row.choiceIndex == m.palette.index {
			selectedRow = index
			break
		}
	}
	maximum := max(len(rows)-capacity, 0)
	m.palette.offset = min(selectedRow, min(max(m.palette.offset, 0), maximum))
	if selectedRow >= m.palette.offset+capacity {
		m.palette.offset = selectedRow - capacity + 1
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
		m.palette.index = 0
		m.ensureWorkspaceActionVisible()
	case "end", "G":
		m.palette.index = len(choices) - 1
		m.ensureWorkspaceActionVisible()
	case "enter":
		return m.runWorkspaceAction(choices[m.palette.index])
	}
	return m, nil
}

func (m dashboard) runWorkspaceAction(choice workspaceActionChoice) (tea.Model, tea.Cmd) {
	action := choice.action
	target, ok := m.currentWorkspaceActionTarget()
	if !ok {
		m.modal = noModal
		m.setNotice(treeError, "selected workspace no longer exists")
		return m, nil
	}
	m.palette.target = target
	switch action {
	case workspaceOpenTerminalAction:
		tabs := runningTabs(target.tabs)
		if len(tabs) == 0 {
			m.modal = noModal
			m.setNotice(terminalError, noRunningTerminal)
			return m, nil
		}
		m.tabIndex = min(m.tabIndex, len(tabs)-1)
		m.modal = noModal
		return m, m.selectWorkspace()
	case workspaceNewTabAction:
		m.tabIndex = len(runningTabs(target.tabs))
		m.modal = noModal
		return m, m.selectWorkspace()
	case workspaceReopenAction:
		m.tabIndex = len(runningTabs(target.tabs))
		m.modal = noModal
		return m, m.selectWorkspaceFor(choice.closedID)
	case workspaceCloseTabAction:
		tabs := runningTabs(target.tabs)
		if m.tabIndex >= len(tabs) {
			m.modal = noModal
			m.setNotice(terminalError, noRunningTerminal)
			return m, nil
		}
		return m.confirmCloseTab(tabs[m.tabIndex], m.tabIndex)
	case workspaceFileChangesAction:
		m.modal = noModal
		return m.openChangedFilesView(target)
	case workspaceAllFilesAction:
		m.modal = noModal
		return m.openAllFilesView(target)
	case workspaceGitStatusAction, workspaceGitFetchAction, workspaceGitPullAction, workspaceGitPushAction:
		return m.startWorkspaceGitAction(action, target)
	case workspaceRemoveAction:
		return m.confirmRemoveSelection(target)
	default:
		m.modal = noModal
		m.setError(treeError, fmt.Sprintf("unknown workspace action %d", action))
		return m, nil
	}
}

func (m *dashboard) currentWorkspaceActionTarget() (navItem, bool) {
	target := m.palette.target
	for index, item := range m.navigationItems() {
		if item.root.ID == target.root.ID && item.workspace.Path == target.workspace.Path {
			m.setNavigation(index)
			return item, true
		}
	}
	return navItem{}, false
}

// gitActionByWorkspaceAction names which Git operation each palette entry
// starts, so the palette adds an entry without repeating the Git actions.
var gitActionByWorkspaceAction = map[workspaceAction]gitAction{
	workspaceGitStatusAction: gitStatusAction,
	workspaceGitFetchAction:  gitFetchAction,
	workspaceGitPullAction:   gitPullAction,
	workspaceGitPushAction:   gitPushAction,
}

func (m dashboard) startWorkspaceGitAction(action workspaceAction, target navItem) (tea.Model, tea.Cmd) {
	gitAction, ok := gitActionByWorkspaceAction[action]
	if !ok || !target.hasGit {
		m.modal = noModal
		m.setError(gitError, notAGitRepository)
		return m, nil
	}
	m.modal = gitActionsModal
	m.gitRun = gitActionRun{
		target: target.workspace, index: int(gitAction), action: gitAction, returnTo: workspaceActionsModal,
	}
	m.clearAnyError()
	return m.startGitAction()
}

func (m dashboard) workspaceActionPopup(width, height int) (lines []string, x, y int) {
	if m.palette.sessionChoice {
		return m.sessionChoicePanel(height)
	}
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
		if row.choiceIndex == m.palette.index {
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
	x = min(max(m.palette.anchorX, 0), max(width-popupWidth, 0))
	y = min(max(m.palette.anchorY, 0), max(height-len(lines), 0))
	return lines, x, y
}

// sessionChoicePanel draws the session choice in the middle of the terminal
// pane, which shows nothing else while it is open. It has no border: a title
// row above the choices and a blank row below them take the border's place,
// so pointer hit-testing is the same as for the palette.
func (m dashboard) sessionChoicePanel(height int) (lines []string, x, y int) {
	view := m.dimensions()
	choices := m.workspaceActionChoices()
	rows := m.workspaceActionRows()
	offset, end := m.workspaceActionWindow(height)
	title := " Open a session in " + display.Text(m.palette.target.workspace.Name)
	labelWidth := lipgloss.Width(title)
	for _, row := range rows[offset:end] {
		if !row.divider {
			labelWidth = max(labelWidth, lipgloss.Width(choices[row.choiceIndex].label)+1)
		}
	}
	panelWidth := min(labelWidth+4, max(view.rightWidth, 1))
	innerWidth := max(panelWidth-2, 0)
	lines = make([]string, 0, end-offset+2)
	lines = append(lines, " "+m.styles.navigationRoot.Render(pad(truncate(title, innerWidth), innerWidth))+" ")
	for _, row := range rows[offset:end] {
		if row.divider {
			lines = append(lines, " "+m.styles.divider.Render(strings.Repeat("─", innerWidth))+" ")
			continue
		}
		style := m.styles.modalBody
		if row.choiceIndex == m.palette.index {
			style = m.styles.navigationSelected
		} else if m.hover.kind == hoverWorkspaceAction && m.hover.index == row.choiceIndex {
			style = m.styles.hovered(style)
		}
		label := " " + choices[row.choiceIndex].label
		lines = append(lines, " "+style.Render(pad(truncate(label, innerWidth), innerWidth))+" ")
	}
	lines = append(lines, strings.Repeat(" ", panelWidth))
	originX, originY := view.terminalOrigin()
	x = originX + max(view.rightWidth-panelWidth, 0)/2
	y = originY + max(height-originY-len(lines), 0)/2
	return lines, x, y
}

func (m dashboard) overlayWorkspaceActions(base []string, width, height int) []string {
	popup, x, y := m.workspaceActionPopup(width, height)
	return overlayBox(base, popup, x, y, width, height)
}

func (m dashboard) handleWorkspaceActionsMouse(message tea.MouseMsg) (tea.Model, tea.Cmd, bool) {
	mouse := message.Mouse()
	width, height := m.bodySize()
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
			m.moveWorkspaceAction(-wheelLines)
		case tea.MouseWheelDown:
			m.moveWorkspaceAction(wheelLines)
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
	m.palette.index = index
	choices := m.workspaceActionChoices()
	updated, command := m.runWorkspaceAction(choices[index])
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
