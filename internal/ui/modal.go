// Modals: the boxes romty draws over the panes, the keys and clicks they take,
// and the frame every one of them is drawn in.
package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/opspresso/romty/internal/agenthooks"
	"github.com/opspresso/romty/internal/sound"
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
	case gitActionsModal:
		if m.gitActionComplete {
			page := max(modalCapacity(m.dimensions().bodyHeight)-3, 1)
			switch message.String() {
			case "enter":
				return m.resetGitActionResult(), nil
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
		case "m":
			return m.toggleScrollbackMouse()
		case "d":
			return m.toggleSoundOnDone()
		case "b":
			return m.toggleSoundOnWaiting()
		case "s":
			return m, soundAlert(sound.Done)
		}
	}
	return m, nil
}

// overlayModal replaces the body with a blank backdrop before centering the
// modal, so workspace and terminal content cannot show around it.
func (m dashboard) overlayModal(width, height int) []string {
	modalLines := m.renderModal(width, height)
	lines := make([]string, height)
	for row := range lines {
		lines[row] = strings.Repeat(" ", width)
	}
	top := max((height-len(modalLines))/2, 0)
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
	modalWidth := min(max(width-4, 32), 72)
	if m.modal == helpModal {
		modalWidth = min(max(width-4, 32), 80)
		return m.renderHelpModal(modalWidth, height)
	}
	if m.modal == browseModal {
		// Paths are long, so the picker gets the wider box help uses.
		return m.renderBrowseModal(min(max(width-4, 32), 80), height)
	}
	if m.modal == gitActionsModal {
		return m.renderGitActionsModal(modalWidth, height)
	}
	if m.modal == removeSelectionModal {
		if m.removeTarget.isRoot {
			return m.withModalActions(modalBox(m.styles, modalWidth, "Forget root",
				m.styles.modalStrong.Render("Forget "+displayText(m.removeTarget.root.Name)+"?"),
				m.styles.empty.Render(displayText(m.removeTarget.root.Path)),
				"",
				m.styles.modalBody.Render("The directory stays on disk."),
				m.styles.errorText.Render("Its running shells will be terminated."),
			))
		}
		// What is about to be deleted comes first, its path under it as the
		// context it is, and the consequences below the break. The path used to
		// trail the two red lines in the body colour, where it read as a third
		// consequence rather than as the thing being named.
		return m.withModalActions(modalBox(m.styles, modalWidth, "Delete workspace",
			m.styles.modalStrong.Render("Delete "+displayText(m.removeTarget.workspace.Name)+"?"),
			m.styles.empty.Render(displayText(m.removeTarget.workspace.Path)),
			"",
			m.styles.errorText.Render("This permanently deletes all contents."),
			m.styles.errorText.Render("Its running shells will be terminated."),
		))
	}
	if m.modal == shutdownModal {
		return m.withModalActions(modalBox(m.styles, modalWidth, "Stop daemon",
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
		return m.withModalActions(modalBox(m.styles, modalWidth, "Agent hooks", lines...))
	}
	if m.modal == configModal {
		return modalBox(m.styles, modalWidth, "Config",
			m.renderConfigRow(modalWidth, 0, fmt.Sprintf("Left pane: %d", m.paneWidth()), "←/→"),
			m.renderConfigRow(modalWidth, 1, "Scrollback mouse: "+onOff(m.scrollbackMouse), "m"),
			m.renderConfigRow(modalWidth, 2, "Sound on done: "+onOff(m.soundOnDone), "d"),
			m.renderConfigRow(modalWidth, 3, "Sound on waiting: "+onOff(m.soundOnWaiting), "b"),
			m.renderConfigRow(modalWidth, 4, "Test sound", "s"),
		)
	}
	return modalBox(m.styles, modalWidth, "About",
		m.styles.modalStrong.Render("romty")+"  "+m.styles.empty.Render(version.String()),
		m.styles.modalBody.Render(tagline),
	)
}

func (m dashboard) renderConfigRow(width, index int, label, key string) string {
	if m.hover.kind == hoverConfigRow && m.hover.index == index {
		return m.styles.interactiveHover.Render(pad(label+"  "+key, max(width-6, 0)))
	}
	return m.styles.modalStrong.Render(label) + "  " + m.styles.empty.Render(key)
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

func (m dashboard) modalActionHits(width, height int) []modalActionHit {
	actions := m.modalActions()
	if len(actions) == 0 {
		return nil
	}
	lines := m.renderModal(width, height)
	modalWidth := lipgloss.Width(lines[0])
	left := max((width-modalWidth)/2, 0) + 3
	row := max((height-len(lines))/2, 0) + len(lines) - 2
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
