// Input modal: the one-line text box romty opens to ask for a root path or a
// tab name, drawn over the panes like every other question romty asks.

package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/display"
	"github.com/opspresso/romty/internal/model"
)

// inputKind names what the input modal is asking for.
type inputKind int

const (
	rootPathInput inputKind = iota
	tabNameInput
)

// inputModalWidth is the width the input box always takes. A box sized to its
// text would grow and shrink under the cursor with every key.
const inputModalWidth = 56

func (m dashboard) startRootInput() (tea.Model, tea.Cmd) {
	m.openInput(rootPathInput, "")
	return m, nil
}

// startRenameTab opens the name modal with the tab's current name, so a small
// change does not mean retyping the whole name.
func (m dashboard) startRenameTab(tab model.Tab) (tea.Model, tea.Cmd) {
	if tab.ID == "" {
		return m, nil
	}
	m.openInput(tabNameInput, tab.Name)
	m.renameTabID = tab.ID
	return m, nil
}

func (m *dashboard) openInput(kind inputKind, value string) {
	m.modal = inputModal
	m.inputKind = kind
	m.input = value
	m.inputError = ""
	m.renameTabID = ""
	m.clearAnyError()
}

// handleInput takes every key while the input modal is open, F4 included, so
// a stray function key cannot discard what was typed.
func (m dashboard) handleInput(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "esc":
		m.closeInput()
	case "enter":
		value := strings.TrimSpace(m.input)
		if value == "" {
			m.inputError = m.inputKind.required()
			return m, nil
		}
		kind, tabID := m.inputKind, m.renameTabID
		m.closeInput()
		if kind == tabNameInput {
			return m, m.renameTab(tabID, value)
		}
		return m, m.addRoot(value)
	default:
		m.input = editText(m.input, message)
		m.inputError = ""
	}
	return m, nil
}

// closeInput closes the input modal. A validation error belongs to the modal,
// so it leaves with it.
func (m *dashboard) closeInput() {
	m.modal = noModal
	m.input = ""
	m.inputError = ""
	m.renameTabID = ""
}

func (k inputKind) title() string {
	if k == tabNameInput {
		return "Rename tab"
	}
	return "Root path"
}

func (k inputKind) submit() string {
	if k == tabNameInput {
		return "rename"
	}
	return "add root"
}

func (k inputKind) required() string {
	if k == tabNameInput {
		return "tab name is required"
	}
	return "root path is required"
}

func (m dashboard) inputActions() []modalAction {
	return []modalAction{
		{shortcut: shortcut{key: "Enter", description: m.inputKind.submit()}, code: tea.KeyEnter},
		{shortcut: shortcut{key: "Esc", description: "cancel"}, code: tea.KeyEscape},
	}
}

// renderInputModal draws the text field and, under it, the reason the last
// submit was rejected. The error row is kept blank rather than removed, so the
// box does not change height while the user types.
func (m dashboard) renderInputModal(maximum int) []string {
	width := min(inputModalWidth, maximum)
	field := m.renderInputField(max(width-6, 1))
	status := ""
	if m.inputError != "" {
		status = m.styles.errorText.Render(m.inputError)
	}
	return m.withModalActions(modalBox(m.styles, width, m.inputKind.title(), field, status))
}

// renderInputField draws the typed text on a filled field with the cursor at
// its end. Text wider than the field loses its start, not its end, because the
// end is where the user is typing.
func (m dashboard) renderInputField(width int) string {
	text := display.Text(m.input)
	room := max(width-1, 0)
	if overflow := lipgloss.Width(text) - room; overflow > 0 {
		text = ansi.TruncateLeft(text, overflow+1, "…")
	}
	fill := strings.Repeat(" ", max(room-lipgloss.Width(text), 0))
	return m.styles.inputField.Render(text) + m.styles.inputCursor.Render(" ") + m.styles.inputField.Render(fill)
}

// editText applies one key to a line of typed text. The input modal and the
// scrollback search both read their input through it, so a keystroke does the
// same thing in either.
func editText(value string, message tea.KeyPressMsg) string {
	if message.String() == "backspace" {
		runes := []rune(value)
		if len(runes) == 0 {
			return value
		}
		return string(runes[:len(runes)-1])
	}
	if message.Text != "" {
		return value + message.Text
	}
	return value
}
