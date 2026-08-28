// Pointer handling: what the dashboard chrome does with a click, a drag and a
// wheel, which target the pointer is over, and how much of the mouse is left
// for the guest application.
package ui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/opspresso/romty/internal/model"
)

type hoverKind int

type hoverTarget struct {
	kind  hoverKind
	index int
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
		if !m.terminal.guestWantsMotion(mouse.Button != uv.MouseNone) {
			return m, nil
		}
		m.terminal.sendMouse(uv.MouseMotionEvent(mouse))
	}
	return m, nil
}

func (m dashboard) handleTerminalWheel(wheel tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	if m.terminal == nil {
		return m, nil
	}
	mouse, inside := m.translateMouse(wheel.Mouse())
	if !inside && !m.scrollback {
		return m, nil
	}
	if !m.scrollback && m.terminal.altScreen() {
		return m.scrollGuest(mouse, wheel.Button)
	}
	switch wheel.Button {
	case tea.MouseWheelUp:
		if m.scrollback || m.startScrollback() {
			m.scrollTerminal(wheelLines)
			return m, nil
		}
		// Saying nothing is what a dead wheel looks like. Scrollback that has
		// nothing to show is not a fault of the terminal or of the user, and
		// the same sentence answers Shift+PgUp.
		m.setNotice(terminalError, m.scrollbackUnavailable())
	case tea.MouseWheelDown:
		if m.scrollback {
			m.scrollTerminal(-wheelLines)
		}
	}
	return m, nil
}

// scrollGuest hands the wheel to an application that owns the screen. Its
// history is its own — romty has none to offer — and this is what a terminal
// does with the wheel in the alternate screen: mouse reports to an application
// that asked for the mouse, and the cursor keys of alternate scroll to one that
// did not. Without it the wheel did nothing at all inside vim, less, or an
// agent, which is every full-screen program romty exists to keep running.
func (m dashboard) scrollGuest(mouse uv.Mouse, button tea.MouseButton) (tea.Model, tea.Cmd) {
	if m.terminal.guestMouseMode() != tea.MouseModeNone {
		m.terminal.sendMouse(uv.MouseWheelEvent(mouse))
		return m, nil
	}
	var code rune
	switch button {
	case tea.MouseWheelUp:
		code = tea.KeyUp
	case tea.MouseWheelDown:
		code = tea.KeyDown
	default:
		return m, nil
	}
	for range wheelLines {
		m.terminal.sendKey(tea.KeyPressMsg(tea.Key{Code: code}))
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

func (m dashboard) handleModalMouse(message tea.MouseMsg) (tea.Model, tea.Cmd, bool) {
	switch m.modal {
	case browseModal:
		if updated, command, handled := m.handleBrowseMouse(message); handled {
			return updated, command, true
		}
	case workspaceActionsModal:
		if updated, command, handled := m.handleWorkspaceActionsMouse(message); handled {
			return updated, command, true
		}
	case gitActionsModal:
		if updated, command, handled := m.handleGitActionsMouse(message); handled {
			return updated, command, true
		}
	case configModal:
		if updated, command, handled := m.handleConfigMouse(message); handled {
			return updated, command, true
		}
	}
	click, ok := message.(tea.MouseClickMsg)
	if !ok || click.Button != tea.MouseLeft {
		return m, nil, false
	}
	mouse := click.Mouse()
	for _, hit := range m.modalActionHits(max(m.width, 40), m.dimensions().bodyHeight) {
		if mouse.Y == hit.row && mouse.X >= hit.left && mouse.X < hit.right {
			updated, command := m.handleKey(tea.KeyPressMsg(tea.Key{Code: hit.action.code}))
			return updated, command, true
		}
	}
	return m, nil, false
}

func (m dashboard) hoverTargetAt(mouse tea.Mouse) hoverTarget {
	width := max(m.width, 40)
	height := m.dimensions().bodyHeight
	if m.modal == workspaceActionsModal {
		if index, ok := m.workspaceActionIndexAtMouse(mouse, width, height); ok {
			return hoverTarget{kind: hoverWorkspaceAction, index: index}
		}
		return hoverTarget{}
	}
	if m.modal != noModal {
		for index, hit := range m.modalActionHits(width, height) {
			if mouse.Y == hit.row && mouse.X >= hit.left && mouse.X < hit.right {
				return hoverTarget{kind: hoverModalAction, index: index}
			}
		}
		row, inside := m.modalContentRow(mouse, width, height)
		if !inside {
			return hoverTarget{}
		}
		switch m.modal {
		case browseModal:
			if index, ok := m.browseIndexAtContentRow(row); ok {
				return hoverTarget{kind: hoverBrowseRow, index: index}
			}
		case gitActionsModal:
			if m.gitActionComplete && row >= gitActionHeaderRows {
				return hoverTarget{kind: hoverGitResult, index: row}
			}
			if !m.gitActionPending && row >= gitActionHeaderRows &&
				row < gitActionHeaderRows+len(gitActionChoices) {
				return hoverTarget{kind: hoverGitAction, index: row - gitActionHeaderRows}
			}
		case configModal:
			if row >= 0 && row < len(configRows()) {
				return hoverTarget{kind: hoverConfigRow, index: row}
			}
		}
		return hoverTarget{}
	}

	view := m.dimensions()
	if view.separator > 0 && mouse.Y < view.bodyHeight &&
		mouse.X >= view.leftWidth && mouse.X < view.leftWidth+view.separator {
		return hoverTarget{kind: hoverDivider}
	}
	if mouse.X < view.leftWidth && mouse.Y < view.bodyHeight {
		if index, ok := m.navigationIndexAtRow(mouse.Y, view.bodyHeight); ok {
			return hoverTarget{kind: hoverNavigation, index: index}
		}
		return hoverTarget{}
	}
	if mouse.Y == 0 && mouse.X >= view.leftWidth+view.separator {
		localX := mouse.X - view.leftWidth - view.separator
		if hit, ok := tabHitAtX(m.visibleTabs(), localX); ok {
			kind := hoverTab
			if hit.close {
				kind = hoverTabClose
			}
			return hoverTarget{kind: kind, index: hit.index}
		}
	}
	return hoverTarget{}
}

func (m dashboard) handleConfigMouse(message tea.MouseMsg) (tea.Model, tea.Cmd, bool) {
	row, inside := m.modalContentRow(message.Mouse(), max(m.width, 40), m.dimensions().bodyHeight)
	if !inside {
		return m, nil, false
	}
	if wheel, ok := message.(tea.MouseWheelMsg); ok && row == 0 {
		switch wheel.Button {
		case tea.MouseWheelUp:
			updated, command := m.adjustLeftWidth(1)
			return updated, command, true
		case tea.MouseWheelDown:
			updated, command := m.adjustLeftWidth(-1)
			return updated, command, true
		}
	}
	click, ok := message.(tea.MouseClickMsg)
	if !ok || click.Button != tea.MouseLeft {
		return m, nil, false
	}
	if updated, command, ok := m.runConfigRow(row); ok {
		return updated, command, true
	}
	// A click inside the box that lands on no setting is still the modal's;
	// letting it fall through would send it to whatever is drawn behind.
	return m, nil, true
}

func (m dashboard) modalContentOrigin(width, height int) (int, int) {
	lines := m.renderModal(width, height)
	if len(lines) == 0 {
		return 0, 0
	}
	modalWidth := lipgloss.Width(lines[0])
	return max((width-modalWidth)/2, 0) + 3, max((height-len(lines))/2, 0) + 1
}

func (m dashboard) modalContentRow(mouse tea.Mouse, width, height int) (int, bool) {
	lines := m.renderModal(width, height)
	if len(lines) < 2 {
		return 0, false
	}
	modalWidth := lipgloss.Width(lines[0])
	modalLeft := max((width-modalWidth)/2, 0)
	_, contentTop := m.modalContentOrigin(width, height)
	row := mouse.Y - contentTop
	inside := mouse.X >= modalLeft+1 && mouse.X < modalLeft+modalWidth-1 &&
		row >= 0 && row < len(lines)-2
	return row, inside
}

func (m dashboard) handleDashboardMouse(message tea.MouseMsg) (tea.Model, tea.Cmd, bool) {
	mouse := message.Mouse()
	view := m.dimensions()
	if m.navigationResize {
		switch message.(type) {
		case tea.MouseMotionMsg:
			m.setLeftWidth(mouse.X - 1)
			return m, m.resizeTerminal(), true
		case tea.MouseReleaseMsg:
			m.navigationResize = false
			return m, tea.Batch(m.resizeTerminal(), m.saveConfig()), true
		}
	}

	if wheel, ok := message.(tea.MouseWheelMsg); ok && mouse.X < view.leftWidth && mouse.Y < view.bodyHeight {
		switch wheel.Button {
		case tea.MouseWheelUp:
			m.scrollNavigation(-3)
		case tea.MouseWheelDown:
			m.scrollNavigation(3)
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
	if click.Button == tea.MouseRight && mouse.X < view.leftWidth && mouse.Y < view.bodyHeight {
		item, ok := m.focusNavigationRow(mouse.Y, view.bodyHeight)
		if !ok {
			return m, nil, true
		}
		updated, command := m.openWorkspaceActionsAt(item, mouse.X, mouse.Y)
		return updated, command, true
	}
	if click.Button != tea.MouseLeft {
		return m, nil, false
	}
	if view.separator > 0 && mouse.Y < view.bodyHeight &&
		mouse.X >= view.leftWidth && mouse.X < view.leftWidth+view.separator {
		m.navigationResize = true
		return m, nil, true
	}
	if mouse.X < view.leftWidth && mouse.Y < view.bodyHeight {
		if _, ok := m.focusNavigationRow(mouse.Y, view.bodyHeight); !ok {
			return m, nil, true
		}
		return m, m.selectWorkspace(), true
	}
	if mouse.Y == 0 && mouse.X >= view.leftWidth+view.separator {
		localX := mouse.X - view.leftWidth - view.separator
		tabs := m.visibleTabs()
		hit, ok := tabHitAtX(tabs, localX)
		if !ok {
			return m, nil, true
		}
		if hit.close {
			updated, command := m.confirmCloseTab(tabs[hit.index], hit.index)
			return updated, command, true
		}
		m.tabIndex = hit.index
		if m.focus == leftPane {
			return m, m.selectWorkspace(), true
		}
		if hit.index == len(tabs) {
			updated, command := m.newTab()
			return updated, command, true
		}
		return m, m.openSelectedTerminal(), true
	}
	return m, nil, false
}

// navigationItemAtRow is the tree item drawn on a body row, and where it sits.
// Both are wanted together: the click handlers used to ask for the index, then
// rebuild the whole tree once more per field they read off the item.
func (m dashboard) navigationItemAtRow(row, height int) (navItem, int, bool) {
	items := m.navigationItems()
	available := max(height-2, 0)
	start, end := navigationWindow(items, m.navOffset, available)
	currentRow := 2
	for index := start; index < end; index++ {
		nextRow := currentRow + navigationRows(items[index])
		if row >= currentRow && row < nextRow {
			return items[index], index, true
		}
		currentRow = nextRow
	}
	return navItem{}, 0, false
}

func (m dashboard) navigationIndexAtRow(row, height int) (int, bool) {
	_, index, ok := m.navigationItemAtRow(row, height)
	return index, ok
}

// focusNavigationRow puts the keyboard and the cursor on the tree row under a
// click and reports what it landed on. Opening a workspace and opening its
// action palette both start with exactly this.
func (m *dashboard) focusNavigationRow(row, height int) (navItem, bool) {
	item, index, ok := m.navigationItemAtRow(row, height)
	if !ok {
		return navItem{}, false
	}
	m.focus = leftPane
	m.setNavigation(index)
	m.syncTabCursor(runningTabs(item.tabs))
	return item, true
}

type tabHit struct {
	index int
	close bool
}

func tabHitAtX(tabs []model.Tab, x int) (tabHit, bool) {
	position := 0
	for index := 0; index <= len(tabs); index++ {
		label := "  +  "
		if index < len(tabs) {
			label = "  " + displayText(tabs[index].Name) + "  × "
		}
		end := position + lipgloss.Width(label)
		if x >= position && x < end {
			close := index < len(tabs) && x >= end-2
			return tabHit{index: index, close: close}, true
		}
		position = end + 1
	}
	return tabHit{}, false
}

// mouseMode asks for pointer motion wherever romty draws hoverable controls.
// Copy mode still gives the mouse back to the host for native selection. A
// guest application that asked for the mouse receives events only when the
// user opted into passthrough, while romty keeps motion for its surrounding
// dashboard chrome.
func (m dashboard) mouseMode() tea.MouseMode {
	if m.modal != noModal {
		return tea.MouseModeAllMotion
	}
	if m.gitDiff.active {
		return tea.MouseModeCellMotion
	}
	if m.scrollback {
		// Keeping the mouse is the only way to hear the wheel on a terminal
		// with no alternate scroll, which is what a phone SSH client and some
		// desktop terminals are. It costs the drag selection, so it is asked
		// for rather than assumed.
		if m.scrollbackMouse {
			return tea.MouseModeCellMotion
		}
		return tea.MouseModeNone
	}
	// Motion is what the hover highlights are drawn from, so romty asks for it
	// even while passthrough hands the buttons to the guest. forwardMouse then
	// keeps the motion a guest never asked for to itself.
	return tea.MouseModeAllMotion
}

// guestOwnsMouse reports whether passthrough has handed the mouse to the guest.
// Scrollback is romty's own view of output the guest has already printed, so
// the guest does not own the mouse there however the passthrough is set.
func (m dashboard) guestOwnsMouse() bool {
	return m.mousePassthrough && !m.scrollback &&
		m.terminal != nil && m.terminal.guestMouseMode() != tea.MouseModeNone
}
