package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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
	if value.inputMode {
		t.Fatal("a single click opened the name prompt")
	}
	clickTabLabel(&value, 1)
	if !value.inputMode || value.inputKind != tabNameInput || value.input != "2" || value.renameTabID != "tab-2" {
		t.Fatalf("double click = (input %v, kind %v, text %q, tab %q), want the prompt for tab-2",
			value.inputMode, value.inputKind, value.input, value.renameTabID)
	}
	if status := strings.Join(value.renderStatus(80, 20), "\n"); !strings.Contains(status, " NAME ") {
		t.Fatalf("status = %q, want the NAME prompt", status)
	}

	send(&value, tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	send(&value, tea.KeyPressMsg(tea.Key{Text: "api", Code: 'a'}))
	command := send(&value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command == nil || value.inputMode {
		t.Fatalf("enter = (command %v, input %v), want a rename request", command, value.inputMode)
	}
	send(&value, command())
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
	if value.inputMode {
		t.Fatal("clicks on two different tabs opened the name prompt")
	}
	current = current.Add(doubleClickInterval + time.Millisecond)
	clickTabLabel(&value, 1)
	if value.inputMode {
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
	if value.inputMode || value.renameTabID != "" {
		t.Fatal("Esc left the name prompt open")
	}

	updated, _ = value.startRenameTab(tabs[0])
	value = updated.(dashboard)
	backend.failures = map[string]error{"RenameTab": errors.New("tab name must not contain control characters")}
	command := send(&value, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	send(&value, command())
	if !strings.Contains(value.errorMessage, "rename tab: tab name must not contain control characters") {
		t.Fatalf("error = %q, want the rename failure", value.errorMessage)
	}
	if value.visibleTabs()[0].Name != "1" {
		t.Fatalf("tab name after failed rename = %q, want 1", value.visibleTabs()[0].Name)
	}
}
