package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/protocol"
)

// clickTabLabel clicks inside a tab's label, clear of its close button.
func clickTabLabel(value *dashboard, index int) tea.Cmd {
	tabs := value.visibleTabs()
	origin := value.dimensions().leftWidth + value.dimensions().separator
	return send(value, tea.MouseClickMsg{
		X: origin + tabCloseLocalX(tabs, index) - 3, Y: 0, Button: tea.MouseLeft,
	})
}

func TestDashboardDoubleClickRenamesTab(t *testing.T) {
	tabs := openTabs(2)
	value, backend := tabCloseDashboard(tabs)
	value.focus = terminalPane

	clickTabLabel(&value, 1)
	if value.modal != noModal {
		t.Fatal("a single click opened the name prompt")
	}
	clickTabLabel(&value, 1)
	if value.modal != inputModal || value.input.kind != tabNameInput || value.input.text != "2" || value.input.tabID != "tab-2" {
		t.Fatalf("double click = (modal %v, kind %v, text %q, tab %q), want the name modal for tab-2",
			value.modal, value.input.kind, value.input.text, value.input.tabID)
	}
	if box := ansi.Strip(strings.Join(value.renderModal(80, 20), "\n")); !strings.Contains(box, "Rename tab") ||
		!strings.Contains(box, "Enter  rename") {
		t.Fatalf("modal = %q, want the Rename tab box", box)
	}

	send(&value, tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	send(&value, tea.KeyPressMsg(tea.Key{Text: "api", Code: 'a'}))
	command := send(&value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command == nil || value.modal != inputModal || !value.input.pending {
		t.Fatalf("enter = (command %v, modal %v, pending %v), want a rename request behind the open modal",
			command, value.modal, value.input.pending)
	}
	send(&value, commandMessage[inputResultMsg](t, command))
	if value.modal != noModal || value.input.pending {
		t.Fatalf("after the rename = (modal %v, pending %v), want the modal closed", value.modal, value.input.pending)
	}
	if backend.renamedTabID != "tab-2" || backend.renamedTabName != "api" {
		t.Fatalf("renamed (%q, %q), want (tab-2, api)", backend.renamedTabID, backend.renamedTabName)
	}
	if name := value.visibleTabs()[1].Name; name != "api" {
		t.Fatalf("tab name after rename = %q, want api", name)
	}
}

func TestDashboardDoubleClickNeedsTheSameTabInQuickSuccession(t *testing.T) {
	tabs := openTabs(2)
	value, _ := tabCloseDashboard(tabs)
	value.focus = terminalPane
	current := time.Now()
	original := now
	now = func() time.Time { return current }
	t.Cleanup(func() { now = original })

	clickTabLabel(&value, 0)
	clickTabLabel(&value, 1)
	if value.modal != noModal {
		t.Fatal("clicks on two different tabs opened the name prompt")
	}
	current = current.Add(doubleClickInterval + time.Millisecond)
	clickTabLabel(&value, 1)
	if value.modal != noModal {
		t.Fatal("two slow clicks opened the name prompt")
	}
}

func TestDashboardTabRenamePromptCancelsAndReportsFailure(t *testing.T) {
	tabs := openTabs(1)
	value, backend := tabCloseDashboard(tabs)
	value.focus = terminalPane
	updated, _ := value.startRenameTab(tabs[0])
	value = updated.(dashboard)
	send(&value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if value.modal != noModal || value.input.tabID != "" {
		t.Fatal("Esc left the name prompt open")
	}

	updated, _ = value.startRenameTab(tabs[0])
	value = updated.(dashboard)
	backend.failures = map[string]error{"RenameTab": &protocol.RefusedError{Message: "tab name must not contain control characters"}}
	command := send(&value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	send(&value, commandMessage[inputResultMsg](t, command))
	if value.modal != inputModal || value.input.text != "1" || value.input.pending {
		t.Fatalf("after a rejected rename = (modal %v, input %q, pending %v), want the modal open with the text kept",
			value.modal, value.input.text, value.input.pending)
	}
	box := ansi.Strip(strings.Join(value.renderModal(80, 20), "\n"))
	if !strings.Contains(box, "tab name must not contain control characters") {
		t.Fatalf("modal = %q, want the rename failure inside it", box)
	}
	if value.errorMessage != "" {
		t.Fatalf("status bar error = %q, want the failure only in the modal", value.errorMessage)
	}
	if strings.Contains(box, "daemon:") {
		t.Fatalf("modal = %q, want the daemon's reason without the transport prefix", box)
	}
	if value.visibleTabs()[0].Name != "1" {
		t.Fatalf("tab name after failed rename = %q, want 1", value.visibleTabs()[0].Name)
	}
	send(&value, tea.KeyPressMsg(tea.Key{Text: "x", Code: 'x'}))
	if value.input.err != "" {
		t.Fatalf("error after typing = %q, want it cleared", value.input.err)
	}
}

// A click elsewhere between two clicks on one tab breaks the pair, however
// quickly the second click follows.
func TestDashboardDoubleClickIgnoresClicksWithAnotherClickBetween(t *testing.T) {
	tabs := openTabs(1)
	value, _ := tabCloseDashboard(tabs)
	value.focus = terminalPane

	clickTabLabel(&value, 0)
	origin := value.dimensions().leftWidth + value.dimensions().separator
	send(&value, tea.MouseClickMsg{X: origin + 2, Y: 5, Button: tea.MouseLeft})
	clickTabLabel(&value, 0)
	if value.modal != noModal {
		t.Fatal("clicks on one tab with another click between opened the name prompt")
	}
}

func TestDashboardTabNamePromptShowsAnEmptyNameError(t *testing.T) {
	tabs := openTabs(1)
	value, backend := tabCloseDashboard(tabs)
	value.focus = terminalPane
	updated, _ := value.startRenameTab(tabs[0])
	value = updated.(dashboard)

	send(&value, tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	if command := send(&value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})); command != nil || value.modal != inputModal {
		t.Fatalf("empty submit = (command %v, modal %v), want the modal kept open", command, value.modal)
	}
	empty := ansi.Strip(strings.Join(value.renderModal(80, 20), "\n"))
	if !strings.Contains(empty, "tab name is required") {
		t.Fatalf("modal = %q, want the empty-name error inside the box", empty)
	}
	send(&value, tea.KeyPressMsg(tea.Key{Text: "x", Code: 'x'}))
	typed := value.renderModal(80, 20)
	if box := ansi.Strip(strings.Join(typed, "\n")); strings.Contains(box, "tab name is required") {
		t.Fatalf("modal = %q, want the error cleared by typing", box)
	}
	if len(typed) != len(strings.Split(empty, "\n")) {
		t.Fatalf("modal height changed from %d to %d when the error cleared", len(strings.Split(empty, "\n")), len(typed))
	}
	if backend.renamedTabID != "" {
		t.Fatalf("empty submit reached the backend for %q", backend.renamedTabID)
	}
}
