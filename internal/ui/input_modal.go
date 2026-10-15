// Input modal: the one-line text box romty opens to ask for a root path or a
// tab name, drawn over the panes like every other question romty asks.

package ui

import (
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/display"
	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/protocol"
)

// inputKind names what the input modal is asking for.
type inputKind int

const (
	rootPathInput inputKind = iota
	tabNameInput
)

// inputState is what the input modal holds while it is open.
type inputState struct {
	kind inputKind
	text string
	// err is the reason the last submit was refused, shown under the field.
	err string
	// pending is set while a submit waits for the daemon's answer.
	pending bool
	// tabID is the tab a name is for, held so the answer applies to the tab
	// the modal opened for.
	tabID string
}

// inputModalWidth is the width the input box always takes. A box sized to its
// text would grow and shrink under the cursor with every key.
const inputModalWidth = 56

func (m dashboard) startRootInput() (tea.Model, tea.Cmd) {
	m.openInput(inputState{kind: rootPathInput})
	return m, nil
}

// startRenameTab opens the name modal with the tab's current name, so a small
// change does not mean retyping the whole name.
func (m dashboard) startRenameTab(tab model.Tab) (tea.Model, tea.Cmd) {
	if tab.ID == "" {
		return m, nil
	}
	m.openInput(inputState{kind: tabNameInput, text: tab.Name, tabID: tab.ID})
	return m, nil
}

func (m *dashboard) openInput(state inputState) {
	m.modal = inputModal
	m.input = state
	m.clearAnyError()
}

// handleInput takes every key while the input modal is open, F4 included, so
// a stray function key cannot discard what was typed.
func (m dashboard) handleInput(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "esc":
		m.closeInput()
	case "enter":
		value := strings.TrimSpace(m.input.text)
		if value == "" {
			m.input.err = m.input.kind.required()
			return m, nil
		}
		m.input.err = ""
		m.input.pending = true
		return m, m.submitInput(value)
	default:
		m.input.text = editText(m.input.text, message)
		m.input.err = ""
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
	backend, kind, tabID := m.backend, m.input.kind, m.input.tabID
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
	m.input.pending = false
	if message.err != nil {
		m.input.err = message.err.Error()
		// The modal already says what was asked, so a refusal shows the
		// daemon's reason alone.
		var refused *protocol.RefusedError
		if errors.As(message.err, &refused) {
			m.input.err = refused.Message
		}
		return m, nil
	}
	m.closeInput()
	return m.update(message.snapshotMsg)
}

// closeInput closes the input modal and forgets what it held.
func (m *dashboard) closeInput() {
	m.modal = noModal
	m.input = inputState{}
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
	if m.input.pending {
		return nil
	}
	return []modalAction{
		{shortcut: shortcut{key: "Enter", description: m.input.kind.submit()}, code: tea.KeyEnter},
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
	case m.input.pending:
		lines = append(lines, m.styles.empty.Render(m.input.kind.pending()))
	case m.input.err != "":
		for _, line := range strings.Split(ansi.Wrap(display.Text(m.input.err), contentWidth, ""), "\n") {
			lines = append(lines, m.styles.errorText.Render(line))
		}
	default:
		lines = append(lines, "")
	}
	return m.withModalActions(modalBox(m.styles, width, m.input.kind.title(), lines...))
}

// renderInputField draws the typed text on a filled field with the cursor at
// its end. Text wider than the field loses its start, not its end, because the
// end is where the user is typing.
func (m dashboard) renderInputField(width int) string {
	text := display.Text(m.input.text)
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
