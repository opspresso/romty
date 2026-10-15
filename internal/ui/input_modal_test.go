package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
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
	if command == nil || value.modal != noModal {
		t.Fatalf("Enter click = (command %v, modal %v), want a rename request", command, value.modal)
	}
	send(&value, command())
	if backend.renamedTabName != "api" {
		t.Fatalf("renamed to %q, want api", backend.renamedTabName)
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
