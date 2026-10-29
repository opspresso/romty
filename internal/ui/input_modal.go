// Input modal: the one-line text box romty opens to ask for a root path or a
// tab name, drawn over the panes like every other question romty asks.

package ui

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

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
	// base is the directory a relative root path is resolved against: the
	// one the root picker shows.
	base string
	// returnTo is the modal Esc goes back to: the root picker when the path
	// modal was opened from it.
	returnTo modal
}

// inputModalWidth is the width the input box always takes. A box sized to its
// text would grow and shrink under the cursor with every key.
const inputModalWidth = 56

// startRootInput opens the path modal from the root picker. A relative path
// is resolved against the directory the picker shows, so a path near it is a
// few keys away, and an absolute or ~ path is taken as it is.
func (m dashboard) startRootInput() (tea.Model, tea.Cmd) {
	m.openInput(inputState{kind: rootPathInput, base: m.browse.path, returnTo: browseModal})
	return m, nil
}

// rootPath is the path the modal submits for value.
func (s inputState) rootPath(value string) string {
	if s.base == "" || filepath.IsAbs(value) || value == "~" || strings.HasPrefix(value, "~/") {
		return value
	}
	return filepath.Join(s.base, value)
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
		returnTo := m.input.returnTo
		m.closeInput()
		m.modal = returnTo
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
	kind, tabID, path := m.input.kind, m.input.tabID, m.input.rootPath(value)
	request := m.snapshotRequest(func(backend Backend) (model.Snapshot, error) {
		if kind == tabNameInput {
			return backend.RenameTab(tabID, value)
		}
		return backend.AddRoot(path)
	})
	return func() tea.Msg { return inputResultMsg{request()} }
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
		if refused, ok := errors.AsType[*model.RefusedError](message.err); ok {
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
		for line := range strings.SplitSeq(ansi.Wrap(display.Text(m.input.err), contentWidth, ""), "\n") {
			lines = append(lines, m.styles.errorText.Render(line))
		}
	case m.input.kind == rootPathInput && m.input.base != "":
		lines = append(lines, truncate(m.styles.empty.Render("Relative to "+display.Text(m.input.base)), contentWidth))
	case m.input.kind == tabNameInput:
		// The rule is shown before it is broken, not only in the refusal.
		lines = append(lines, m.styles.empty.Render(fmt.Sprintf("1 to %d characters · %d now",
			model.MaxTabNameLength, utf8.RuneCountInString(strings.TrimSpace(m.input.text)))))
	default:
		lines = append(lines, "")
	}
	return m.withModalActions(modalBox(m.styles, width, m.input.kind.title(), lines...))
}

// renderInputField draws the typed text on a filled field with the cursor at
// its end. Text wider than the field loses its start, not its end, because the
// end is where the user is typing.
func (m dashboard) renderInputField(width int) string {
	return m.renderTextField(m.input.text, width)
}

// renderTextField draws a one-line text field holding value, as the input
// modal and the delete confirmation both show one.
func (m dashboard) renderTextField(value string, width int) string {
	text := display.Text(value)
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
