// Modals: the boxes romty draws over the panes, the keys and clicks they take,
// and the frame every one of them is drawn in.

package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/agenthooks"
	"github.com/opspresso/romty/internal/display"
	"github.com/opspresso/romty/internal/version"
)

type modalAction struct {
	shortcut
	code rune
}

type modalActionHit struct {
	action      modalAction
	left, right int
	row         int
}

func (m dashboard) openModal(value modal) (tea.Model, tea.Cmd) {
	m.modal = value
	m.helpOffset = 0
	m.configIndex = 0
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
	case workspaceActionsModal:
		return m.handleWorkspaceActionKey(message)
	case gitActionsModal:
		if m.gitActionComplete {
			page := max(modalCapacity(m.dimensions().bodyHeight)-3, 1)
			switch message.String() {
			case "enter":
				m = m.resetGitActionResult()
				if m.gitActionReturn == workspaceActionsModal {
					m.modal = workspaceActionsModal
					m.gitActionReturn = noModal
				}
				return m, nil
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
	case closeTabModal:
		if message.String() == "enter" {
			m.modal = noModal
			return m.closeTab(m.closeTabTarget, m.closeTabIndex)
		}
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
		case "up", "k":
			m.moveConfig(-1)
			return m, nil
		case "down", "j":
			m.moveConfig(1)
			return m, nil
		case "home", "g":
			m.moveConfig(-len(configRows()))
			return m, nil
		case "end", "G":
			m.moveConfig(len(configRows()))
			return m, nil
		// Bubble Tea names the space bar "space"; a case of " " is a branch
		// nothing reaches, so Space did nothing at all.
		case "enter", "space":
			if updated, command, ok := m.runConfigRow(m.configIndex); ok {
				return updated, command
			}
			return m, nil
		}
		if updated, command, ok := m.runConfigKey(message.String()); ok {
			return updated, command
		}
	}
	return m, nil
}

// overlayModal draws the modal over what is already on screen. It used to
// blank the whole body first, which meant opening Config took the workspace
// tree and the terminal away to show five settings — and left the user with no
// sight of what they were about to change. The box interior is opaque, so
// laying it on top is enough for it to read as something in front.
func (m dashboard) overlayModal(base []string, width, height int) []string {
	box := m.renderModal(width, height)
	if len(box) == 0 {
		return base
	}
	left := max((width-lipgloss.Width(box[0]))/2, 0)
	top := max((height-len(box))/2, 0)
	return overlayBox(base, box, left, top, width, height)
}

// dimBackdrop takes the colour out of what is behind a modal and redraws it in
// one receding tone, so the box in front is the only thing on screen asking to
// be read.
//
// The colour is stripped rather than adjusted because most of what is behind a
// modal is not romty's to restyle: the workspace tree is, but the terminal is
// whatever the guest printed. Wrapping the row in a faint attribute would not
// survive it either — the first reset inside the content clears it.
func dimBackdrop(styles *uiStyles, rows []string) []string {
	dimmed := make([]string, len(rows))
	for index, row := range rows {
		dimmed[index] = styles.backdrop.Render(ansi.Strip(row))
	}
	return dimmed
}

// overlayBox pastes a box over what is already drawn, leaving the rest of each
// row showing. Both the modals and the workspace action palette want exactly
// this, and each had written its own copy of the cut-and-splice.
func overlayBox(base, box []string, x, y, width, height int) []string {
	result := append([]string(nil), base...)
	for index, boxLine := range box {
		row := y + index
		if row < 0 || row >= len(result) || row >= height {
			continue
		}
		line := pad(truncate(result[row], width), width)
		boxWidth := lipgloss.Width(boxLine)
		result[row] = ansi.Cut(line, 0, x) + boxLine + ansi.Cut(line, x+boxWidth, width)
	}
	return result
}

func (m dashboard) renderModal(width, height int) []string {
	// modalWidth is the cap the box may grow to, not the width it takes.
	modalWidth := min(max(width-4, minimumModalWidth), maximumModalWidth)
	wide := min(max(width-4, minimumModalWidth), maximumWideModalWidth)
	if m.modal == helpModal {
		// Help and the picker are wide by nature — a key column and a
		// description, a path — so they take the wider cap outright.
		return m.renderHelpModal(wide, height)
	}
	if m.modal == browseModal {
		return m.renderBrowseModal(wide, height)
	}
	if m.modal == gitActionsModal {
		// Command output is the one modal content romty does not write, so it
		// gets the wider ceiling to grow into.
		return m.renderGitActionsModal(wide, height)
	}
	if m.modal == removeSelectionModal {
		if m.removeTarget.isRoot {
			return m.withModalActions(modalBoxFit(m.styles, minimumModalWidth, modalWidth, "Forget root",
				m.styles.modalStrong.Render("Forget "+display.Text(m.removeTarget.root.Name)+"?"),
				m.styles.empty.Render(display.Text(m.removeTarget.root.Path)),
				"",
				m.styles.modalBody.Render("The directory stays on disk."),
				m.styles.errorText.Render("Its running shells will be terminated."),
			))
		}
		// What is about to be deleted comes first, its path under it as the
		// context it is, and the consequences below the break. The path used to
		// trail the two red lines in the body colour, where it read as a third
		// consequence rather than as the thing being named.
		return m.withModalActions(modalBoxFit(m.styles, minimumModalWidth, modalWidth, "Delete workspace",
			m.styles.modalStrong.Render("Delete "+display.Text(m.removeTarget.workspace.Name)+"?"),
			m.styles.empty.Render(display.Text(m.removeTarget.workspace.Path)),
			"",
			m.styles.errorText.Render("This permanently deletes all contents."),
			m.styles.errorText.Render("Its running shells will be terminated."),
		))
	}
	if m.modal == closeTabModal {
		return m.withModalActions(modalBoxFit(m.styles, minimumModalWidth, modalWidth, "Close tab",
			m.styles.modalStrong.Render("Close "+display.Text(m.closeTabTarget.Name)+"?"),
			"",
			m.styles.errorText.Render("Its running shell will be terminated."),
		))
	}
	if m.modal == shutdownModal {
		return m.withModalActions(modalBoxFit(m.styles, minimumModalWidth, modalWidth, "Stop daemon",
			m.styles.modalStrong.Render("Stop daemon and all running terminal sessions?"),
			"",
			m.styles.errorText.Render("Running shells will be terminated."),
		))
	}
	if m.modal == hookInstallModal {
		lines := []string{
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
		lines = append(lines, "", m.styles.empty.Render("Existing settings and other hooks are preserved."))
		return m.withModalActions(modalBoxFit(m.styles, minimumModalWidth, modalWidth, "Agent hooks", lines...))
	}
	if m.modal == configModal {
		return m.renderConfigModal(modalWidth, height)
	}
	return modalBoxFit(m.styles, minimumModalWidth, modalWidth, "About",
		m.styles.modalStrong.Render("romty")+"  "+m.styles.empty.Render(version.String()),
		m.styles.modalBody.Render(tagline),
	)
}

// configFloorWidth keeps the settings box from shrinking to the width of five
// short labels. Config is the one modal a user reads rather than answers, so
// it is given room: half the screen, between a legible floor and the cap.
func (m dashboard) configFloorWidth(maximum int) int {
	return min(max(m.screenWidth()/2, 46), maximum)
}

// renderConfigModal lays the settings out in columns — name, value, key — so
// the values line up under one another and a glance down the column says which
// are on. The name holds the left edge and the value and the key are pushed to
// the right, so the row fills whatever width the box takes rather than
// trailing off into a void.
func (m dashboard) renderConfigModal(maximum, height int) []string {
	rows := configRows()
	nameWidth, valueWidth, hintWidth := 0, 0, 0
	for _, row := range rows {
		nameWidth = max(nameWidth, lipgloss.Width(row.name))
		valueWidth = max(valueWidth, lipgloss.Width(row.text(m)))
		hintWidth = max(hintWidth, lipgloss.Width(row.hint))
	}
	natural := configCursorWidth + nameWidth + valueWidth + hintWidth + 2*len(configColumnGap)
	rowWidth := min(max(natural, m.configFloorWidth(maximum)-6), max(maximum-6, 0))

	lines := make([]string, 0, len(rows)+2)
	// A blank row above and below the settings, when the screen has the rows
	// to spare. On the shortest screen romty lays out for it does not, and a
	// box that loses its bottom edge is worse than one drawn tight.
	padded := height >= len(rows)+4
	if padded {
		lines = append(lines, "")
	}
	for index, row := range rows {
		lines = append(lines, m.renderConfigRow(row, index, nameWidth, valueWidth, hintWidth, rowWidth))
	}
	if padded {
		lines = append(lines, "")
	}
	return modalBoxFit(m.styles, m.configFloorWidth(maximum), maximum, "Config", lines...)
}

// configCursorWidth is the marker column every list romty draws carries, so a
// selected setting is marked the way a selected workspace or tab is.
const configCursorWidth = 2

// configColumnGap separates the name, the value and the key on a setting's row.
const configColumnGap = "   "

func (m dashboard) renderConfigRow(row configRow, index, nameWidth, valueWidth, hintWidth, rowWidth int) string {
	cursor := "  "
	if m.configIndex == index {
		cursor = "▌ "
	}
	name, value, hint := row.name, row.text(m), row.hint
	// The name holds the left edge; the value and the key are a block on the
	// right. Whatever width the box takes, the gap opens between them rather
	// than after the key.
	head := cursor + pad(name, nameWidth)
	tail := pad(value, valueWidth) + configColumnGap + pad(hint, hintWidth)
	gap := strings.Repeat(" ", max(rowWidth-lipgloss.Width(head)-lipgloss.Width(tail), len(configColumnGap)))

	// A selected or hovered row is one colour all the way across, the way the
	// picker and the Git action list draw theirs.
	if m.configIndex == index || m.hover.kind == hoverConfigRow && m.hover.index == index {
		if m.configIndex == index {
			return m.styles.navigationSelected.Render(head + gap + tail)
		}
		return m.styles.interactiveHover.Render(head + gap + tail)
	}
	// At rest each column carries its own colour, and the space between them
	// is left unstyled: a run of styled spaces paints a background where there
	// is no content to justify one.
	return m.styles.modalBorder.Render(cursor) +
		m.styles.modalStrong.Render(name) + columnPad(name, nameWidth) + gap +
		m.configValueStyle(row).Render(value) + columnPad(value, valueWidth) + configColumnGap +
		m.styles.empty.Render(hint)
}

func columnPad(value string, width int) string {
	return strings.Repeat(" ", max(width-lipgloss.Width(value), 0))
}

// configValueStyle is what a setting's value is drawn in: on stands out, off
// recedes, and anything that is not a switch reads as plain text.
func (m dashboard) configValueStyle(row configRow) lipgloss.Style {
	switch row.text(m) {
	case "on":
		return m.styles.navigationCurrent
	case "off":
		return m.styles.empty
	default:
		return m.styles.modalBody
	}
}

func (m dashboard) modalActions() []modalAction {
	switch m.modal {
	case removeSelectionModal:
		description := "forget root"
		if !m.removeTarget.isRoot {
			description = "delete workspace"
		}
		return []modalAction{
			{shortcut: shortcut{key: "Enter", description: description}, code: tea.KeyEnter},
			{shortcut: shortcut{key: "Esc", description: "cancel"}, code: tea.KeyEscape},
		}
	case closeTabModal:
		return []modalAction{
			{shortcut: shortcut{key: "Enter", description: "close tab"}, code: tea.KeyEnter},
			{shortcut: shortcut{key: "Esc", description: "cancel"}, code: tea.KeyEscape},
		}
	case shutdownModal:
		if m.shutdownPending {
			return nil
		}
		return []modalAction{
			{shortcut: shortcut{key: "Enter", description: "stop daemon"}, code: tea.KeyEnter},
			{shortcut: shortcut{key: "Esc", description: "cancel"}, code: tea.KeyEscape},
		}
	case hookInstallModal:
		if m.hookInstallPending {
			return nil
		}
		return []modalAction{
			{shortcut: shortcut{key: "Enter", description: "install hooks"}, code: tea.KeyEnter},
			{shortcut: shortcut{key: "Esc", description: "skip"}, code: tea.KeyEscape},
		}
	}
	return nil
}

func (m dashboard) withModalActions(lines []string) []string {
	actions := m.modalActions()
	if len(actions) == 0 || len(lines) < 2 {
		return lines
	}
	width := lipgloss.Width(lines[0])
	segments := make([]string, 0, len(actions))
	for index, action := range actions {
		segments = append(segments, m.renderModalAction(action,
			m.hover.kind == hoverModalAction && m.hover.index == index))
	}
	actionLine := truncate(strings.Join(segments, "  "), max(width-6, 0))
	boxed := make([]string, 0, len(lines)+2)
	boxed = append(boxed, lines[:len(lines)-1]...)
	boxed = append(boxed, modalContentLine(m.styles, width, ""))
	boxed = append(boxed, modalContentLine(m.styles, width, actionLine))
	return append(boxed, lines[len(lines)-1])
}

// modalGeometry is where the modal box lands on screen. Rendering the modal is
// the only way to find out, so every question a pointer raises about one —
// which action is under it, which content row, whether it is inside the box at
// all — used to render the box again. In all-motion mouse mode that came to
// half a dozen renders of the same box per pointer move.
type modalGeometry struct {
	lines []string
	left  int
	top   int
	width int
}

func (m dashboard) modalGeometry(width, height int) modalGeometry {
	lines := m.renderModal(width, height)
	if len(lines) == 0 {
		return modalGeometry{}
	}
	modalWidth := lipgloss.Width(lines[0])
	return modalGeometry{
		lines: lines,
		left:  max((width-modalWidth)/2, 0),
		top:   max((height-len(lines))/2, 0),
		width: modalWidth,
	}
}

// contentOrigin is where the modal's first content cell lands on screen: three
// columns in from the box's left edge, one row under its top border.
func (g modalGeometry) contentOrigin() (int, int) {
	return g.left + 3, g.top + 1
}

// contentRow is the modal's own row number for a screen position, and whether
// the position is inside the box at all. Row zero is the first line under the
// top border.
func (g modalGeometry) contentRow(mouse tea.Mouse) (int, bool) {
	if len(g.lines) < 2 {
		return 0, false
	}
	_, top := g.contentOrigin()
	row := mouse.Y - top
	inside := mouse.X >= g.left+1 && mouse.X < g.left+g.width-1 &&
		row >= 0 && row < len(g.lines)-2
	return row, inside
}

func (m dashboard) modalActionHits(geometry modalGeometry) []modalActionHit {
	actions := m.modalActions()
	if len(actions) == 0 || len(geometry.lines) == 0 {
		return nil
	}
	left := geometry.left + 3
	row := geometry.top + len(geometry.lines) - 2
	hits := make([]modalActionHit, 0, len(actions))
	for _, action := range actions {
		segment := m.renderModalAction(action, false)
		segmentWidth := lipgloss.Width(segment)
		hits = append(hits, modalActionHit{action: action, left: left, right: left + segmentWidth, row: row})
		left += segmentWidth + 2
	}
	return hits
}

func (m dashboard) renderModalAction(action modalAction, hovered bool) string {
	if hovered {
		return m.styles.interactiveHover.Render(" " + action.key + "  " + action.description)
	}
	return renderShortcuts(m.styles, 1<<16, action.shortcut)
}

// modalCapacity is the number of content lines a modal box can hold without
// losing its top and bottom borders.
func modalCapacity(height int) int {
	return max(height-2, 1)
}

// modalBoxFit draws the box at the width its content needs, up to a cap. A
// confirmation of four short lines used to be laid out at the cap itself,
// which covered most of the screen to ask one question — and, now that the
// modal no longer blanks what is behind it, covered it for no reason at all.
func modalBoxFit(styles *uiStyles, minimum, maximum int, title string, values ...string) []string {
	content := 0
	for _, value := range values {
		content = max(content, lipgloss.Width(value))
	}
	// Six columns for the border and the two spaces of padding on each side,
	// and enough for the title to sit on the top border with a corner beside it.
	width := max(content+6, lipgloss.Width(title)+5)
	return modalBox(styles, min(max(width, minimum), maximum), title, values...)
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
	for _, value := range values {
		lines = append(lines, modalContentLine(styles, width, value))
	}
	return append(lines, styles.modalBorder.Render("╰"+strings.Repeat("─", interior)+"╯"))
}

func modalContentLine(styles *uiStyles, width int, value string) string {
	contentWidth := max(width-6, 0)
	content := pad(truncate(value, contentWidth), contentWidth)
	return styles.modalBorder.Render("│") + "  " + content + "  " + styles.modalBorder.Render("│")
}
