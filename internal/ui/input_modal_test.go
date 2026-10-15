package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/model"
)

func renameModal(t *testing.T) (dashboard, *fakeBackend) {
	t.Helper()
	tabs := openTabs(1)
	value, backend := tabCloseDashboard(tabs)
	updated, _ := value.startRenameTab(tabs[0])
	return updated.(dashboard), backend
}

func clickInputAction(value *dashboard, index int) tea.Cmd {
	hit := value.modalActionHits(value.modalGeometry(value.bodySize()))[index]
	return send(value, tea.MouseClickMsg{X: hit.left, Y: hit.row, Button: tea.MouseLeft})
}

func TestInputModalActionsTakeClicks(t *testing.T) {
	value, backend := renameModal(t)
	value.input = "api"
	command := clickInputAction(&value, 0)
	if command == nil || !value.inputPending {
		t.Fatalf("Enter click = (command %v, pending %v), want a rename request", command, value.inputPending)
	}
	send(&value, commandMessage[inputResultMsg](t, command))
	if backend.renamedTabName != "api" || value.modal != noModal {
		t.Fatalf("after the click = (renamed %q, modal %v), want api and the modal closed", backend.renamedTabName, value.modal)
	}

	value, backend = renameModal(t)
	if command := clickInputAction(&value, 1); command != nil || value.modal != noModal || value.renameTabID != "" {
		t.Fatalf("Esc click = (command %v, modal %v, tab %q), want the modal cancelled",
			command, value.modal, value.renameTabID)
	}
	if backend.renamedTabID != "" {
		t.Fatalf("cancel reached the backend for %q", backend.renamedTabID)
	}
}

func TestInputModalTitleCloseCancels(t *testing.T) {
	value, backend := renameModal(t)
	geometry := value.modalGeometry(value.bodySize())
	send(&value, tea.MouseClickMsg{X: geometry.left + geometry.width - 3, Y: geometry.top, Button: tea.MouseLeft})
	if value.modal != noModal || backend.renamedTabID != "" {
		t.Fatalf("× click = (modal %v, renamed %q), want the modal cancelled", value.modal, backend.renamedTabID)
	}
}

// The end of the text is where the cursor is, so a value wider than the field
// keeps its end on screen and the box keeps its width.
func TestInputModalKeepsTheEndOfLongText(t *testing.T) {
	value, _ := renameModal(t)
	short := value.renderModal(100, 18)
	value.input = strings.Repeat("a", 80) + "END"
	long := value.renderModal(100, 18)
	if field := ansi.Strip(long[1]); !strings.Contains(field, "…") || !strings.Contains(field, "END") {
		t.Fatalf("field = %q, want the end of the text after an ellipsis", field)
	}
	if ansi.StringWidth(short[0]) != ansi.StringWidth(long[0]) {
		t.Fatalf("box width changed from %d to %d with the text", ansi.StringWidth(short[0]), ansi.StringWidth(long[0]))
	}
}

func TestInputModalPasteClearsTheError(t *testing.T) {
	value, _ := renameModal(t)
	value.input = ""
	send(&value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if value.inputError == "" {
		t.Fatal("empty submit set no error")
	}
	send(&value, tea.PasteMsg{Content: "api"})
	if value.input != "api" || value.inputError != "" {
		t.Fatalf("paste = (input %q, error %q), want the text appended and the error cleared", value.input, value.inputError)
	}
}

// A submit cannot be taken back, so nothing may change the text or close the
// modal before the daemon answers.
func TestInputModalHoldsWhileTheSubmitIsOut(t *testing.T) {
	value, _ := renameModal(t)
	command := send(&value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command == nil || !value.inputPending {
		t.Fatalf("enter = (command %v, pending %v), want a pending submit", command, value.inputPending)
	}
	send(&value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	send(&value, tea.KeyPressMsg(tea.Key{Text: "x", Code: 'x'}))
	send(&value, tea.PasteMsg{Content: "y"})
	geometry := value.modalGeometry(value.bodySize())
	send(&value, tea.MouseClickMsg{X: geometry.left + geometry.width - 3, Y: geometry.top, Button: tea.MouseLeft})
	if value.modal != inputModal || value.input != "1" {
		t.Fatalf("while pending = (modal %v, input %q), want the modal and text unchanged", value.modal, value.input)
	}
	box := ansi.Strip(strings.Join(value.renderModal(80, 20), "\n"))
	if !strings.Contains(box, "Renaming…") || strings.Contains(box, "Esc") || strings.Contains(box, "×") {
		t.Fatalf("modal while pending = %q, want progress and no way to cancel", box)
	}
	if status := ansi.Strip(value.renderStatus(80, 20)[1]); !strings.Contains(status, "SAVING") {
		t.Fatalf("status while pending = %q, want the SAVING activity", status)
	}
	send(&value, tea.KeyPressMsg(tea.Key{Code: tea.KeyF4}))
	if !value.result.Quit {
		t.Fatal("F4 while pending did not quit")
	}
}

func TestInputModalShowsARejectedRootPath(t *testing.T) {
	backend := &fakeBackend{}
	backend.fail("AddRoot", errors.New("root path must be a directory: "+strings.Repeat("x", 80)))
	value := newDashboard(backend, model.Snapshot{})
	value.width, value.height = 100, 24
	updated, _ := value.startRootInput()
	value = updated.(dashboard)
	value.input = "/missing"
	command := send(&value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	send(&value, commandMessage[inputResultMsg](t, command))
	if value.modal != inputModal || value.input != "/missing" || value.errorMessage != "" {
		t.Fatalf("after a rejected path = (modal %v, input %q, status error %q), want the modal open with the text kept",
			value.modal, value.input, value.errorMessage)
	}
	box := value.renderModal(100, 22)
	plain := ansi.Strip(strings.Join(box, "\n"))
	if !strings.Contains(plain, "root path must be a directory") || !strings.Contains(plain, "xxx") {
		t.Fatalf("modal = %q, want the whole rejection inside it", plain)
	}
	if ansi.StringWidth(box[0]) != inputModalWidth {
		t.Fatalf("box width = %d, want %d with a long error wrapped", ansi.StringWidth(box[0]), inputModalWidth)
	}
	if !strings.Contains(strings.ReplaceAll(plain, "│", ""), strings.Repeat("x", 40)) {
		t.Fatalf("modal = %q, want the long error wrapped rather than cut", plain)
	}
}
