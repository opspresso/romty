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
// a stray function key cannot discard what was typed. While a submit is out,
// only F4 applies, as in every other pending state: the request cannot be
// taken back, and its answer belongs to the text that was sent.
func (m dashboard) handleInput(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.inputPending {
		if message.String() == "f4" {
			return m.quit()
		}
		return m, nil
	}
	switch message.String() {
	case "esc":
		m.closeInput()
	case "enter":
		value := strings.TrimSpace(m.input)
		if value == "" {
			m.inputError = m.inputKind.required()
			return m, nil
		}
		m.inputError = ""
		m.inputPending = true
		return m, m.submitInput(value)
	default:
		m.input = editText(m.input, message)
		m.inputError = ""
	}
	return m, nil
}

// inputResultMsg is the daemon's answer to a submitted input modal. It is kept
// apart from snapshotMsg so a rejection is shown in the modal that asked, next
// to the text the user can correct, and not in the status bar behind it.
type inputResultMsg struct {
	snapshotMsg
}

func (m *dashboard) submitInput(value string) tea.Cmd {
	order := m.nextSnapshotOrder()
	backend, kind, tabID := m.backend, m.inputKind, m.renameTabID
	return func() tea.Msg {
		var snapshot model.Snapshot
		var err error
		if kind == tabNameInput {
			snapshot, err = backend.RenameTab(tabID, value)
		} else {
			snapshot, err = backend.AddRoot(value)
		}
		return inputResultMsg{snapshotMsg{value: snapshot, order: order, err: err}}
	}
}

// handleInputResult closes the modal only when the daemon accepted the value.
// A rejection keeps the modal open with the text as typed and the reason under
// it.
func (m dashboard) handleInputResult(message inputResultMsg) (tea.Model, tea.Cmd) {
	m.inputPending = false
	if message.err != nil {
		m.inputError = message.err.Error()
		return m, nil
	}
	m.closeInput()
	return m.update(message.snapshotMsg)
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

func (k inputKind) pending() string {
	if k == tabNameInput {
		return "Renaming…"
	}
	return "Adding root…"
}

func (k inputKind) required() string {
	if k == tabNameInput {
		return "tab name is required"
	}
	return "root path is required"
}

func (m dashboard) inputActions() []modalAction {
	if m.inputPending {
		return nil
	}
	return []modalAction{
		{shortcut: shortcut{key: "Enter", description: m.inputKind.submit()}, code: tea.KeyEnter},
		{shortcut: shortcut{key: "Esc", description: "cancel"}, code: tea.KeyEscape},
	}
}

// renderInputModal draws the text field and, under it, the progress of a
// submit or the reason it was rejected. The status row is kept blank rather
// than removed, so the box does not change height while the user types. A
// rejection from the daemon can be longer than the field, so it wraps instead
// of losing its end.
func (m dashboard) renderInputModal(maximum int) []string {
	width := min(inputModalWidth, maximum)
	contentWidth := max(width-6, 1)
	lines := []string{m.renderInputField(contentWidth)}
	switch {
	case m.inputPending:
		lines = append(lines, m.styles.empty.Render(m.inputKind.pending()))
	case m.inputError != "":
		for _, line := range strings.Split(ansi.Wrap(display.Text(m.inputError), contentWidth, ""), "\n") {
			lines = append(lines, m.styles.errorText.Render(line))
		}
	default:
		lines = append(lines, "")
	}
	return m.withModalActions(modalBox(m.styles, width, m.inputKind.title(), lines...))
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
