package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/model"
)

func TestModalFitsNarrowScreen(t *testing.T) {
	for _, width := range []int{12, 24, 32, 40, 80} {
		for _, kind := range []modal{helpModal, browseModal, configModal, aboutModal, gitActionsModal, shutdownModal} {
			value := newDashboard(&fakeBackend{}, model.Snapshot{})
			value.width, value.height, value.modal = width, 24, kind
			for _, line := range value.renderModal(value.bodySize()) {
				if got := lipgloss.Width(line); got > width {
					t.Fatalf("modal %v at width %d draws %d cells: %s", kind, width, got, ansi.Strip(line))
				}
			}
		}
	}
}

func TestWorkspaceActionLargeBackwardStep(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.workspaceActionTarget.failure = "unreadable"
	value.moveWorkspaceAction(-30)
	if value.workspaceActionIndex != 0 {
		t.Fatalf("single action cursor = %d", value.workspaceActionIndex)
	}
	value.workspaceActionTarget.failure = ""
	value.moveWorkspaceAction(-31)
	if value.workspaceActionIndex != 2 {
		t.Fatalf("backward wrap cursor = %d, want 2", value.workspaceActionIndex)
	}
}

func TestRootInputOwnsMouse(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root", Name: "projects", Path: "/projects"},
	}}})
	value.width, value.height = 100, 24
	value.inputMode, value.input = true, "/typed/path"
	for _, message := range []tea.Msg{
		tea.MouseClickMsg{X: 2, Y: 2, Button: tea.MouseLeft},
		tea.MouseClickMsg{X: 2, Y: 2, Button: tea.MouseRight},
	} {
		updated, command := value.Update(message)
		got := updated.(dashboard)
		if command != nil || got.modal != noModal || !got.inputMode || got.input != value.input {
			t.Fatal("mouse activated the dashboard behind the path prompt")
		}
	}
}

func TestModalTitleClose(t *testing.T) {
	for _, kind := range []modal{helpModal, browseModal, configModal, aboutModal, gitActionsModal, shutdownModal, closeTabModal, removeSelectionModal, hookInstallModal} {
		value := newDashboard(&fakeBackend{}, model.Snapshot{})
		value.width, value.height, value.modal = 80, 24, kind
		geometry := value.modalGeometry(value.bodySize())
		title := ansi.Strip(geometry.lines[0])
		at := strings.Index(title, "×")
		if at < 0 {
			t.Fatalf("modal %v has no close button: %s", kind, title)
		}
		x, y := geometry.left+lipgloss.Width(title[:at]), geometry.top
		updated, _ := value.Update(tea.MouseMotionMsg{X: x, Y: y})
		hovered := updated.(dashboard)
		if strings.Join(hovered.renderModal(value.bodySize()), "\n") == strings.Join(geometry.lines, "\n") {
			t.Fatalf("modal %v close button does not highlight", kind)
		}
		updated, command := hovered.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
		if got := updated.(dashboard); got.modal != noModal || command != nil {
			t.Fatalf("modal %v close click did not dismiss", kind)
		}
	}
}

func TestPendingModalCannotCloseWithMouse(t *testing.T) {
	for _, kind := range []modal{gitActionsModal, shutdownModal, hookInstallModal} {
		value := newDashboard(&fakeBackend{}, model.Snapshot{})
		value.width, value.height, value.modal = 80, 24, kind
		value.gitActionPending = kind == gitActionsModal
		value.shutdownPending = kind == shutdownModal
		value.hookInstallPending = kind == hookInstallModal
		value.agentAnimationPending = true
		geometry := value.modalGeometry(value.bodySize())
		if strings.Contains(ansi.Strip(geometry.lines[0]), "×") {
			t.Fatalf("pending modal %v offers Close", kind)
		}
		updated, command := value.Update(tea.MouseClickMsg{
			X: geometry.left + geometry.width - 3, Y: geometry.top, Button: tea.MouseLeft,
		})
		if got := updated.(dashboard); got.modal != kind || command != nil {
			t.Fatalf("pending modal %v dismissed by mouse", kind)
		}
	}
}

func TestModalOwnsPaste(t *testing.T) {
	for _, kind := range []modal{helpModal, configModal, browseModal, gitActionsModal, shutdownModal} {
		stream := newMemoryStream("")
		value := newDashboard(&fakeBackend{}, model.Snapshot{})
		value.terminal = newEmbeddedTerminal("tab", stream, 40, 10)
		t.Cleanup(value.terminal.close)
		value.focus, value.modal = terminalPane, kind
		updated, _ := value.Update(tea.PasteMsg{Content: "hidden shell input"})
		value = updated.(dashboard)
		updated, _ = value.Update(key(tea.KeyEscape, ""))
		value = updated.(dashboard)
		_, _ = value.Update(tea.PasteMsg{Content: "visible shell input"})
		waitForGuest(t, stream, "visible shell input")
		if got := stream.String(); got != "visible shell input" {
			t.Fatalf("modal %v forwarded hidden paste: %q", kind, got)
		}
	}
}

func TestWorkspaceMenuKeepsSelectionVisibleAfterResize(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height, value.modal = 100, 30, workspaceActionsModal
	value.workspaceActionTarget.hasGit = true
	value.workspaceActionIndex = len(value.workspaceActionChoices()) - 1
	updated, _ := value.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	value = updated.(dashboard)
	lines, _, _ := value.workspaceActionPopup(value.bodySize())
	if !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "Delete workspace") {
		t.Fatal("selected menu action disappeared after resize")
	}
}

func TestScrollbackSearchOwnsPaste(t *testing.T) {
	value := scrolledDashboard(t, 200)
	updated, _ := value.Update(key(tea.KeyF6, ""))
	value = updated.(dashboard)
	updated, _ = value.Update(key('/', "/"))
	value = updated.(dashboard)
	updated, _ = value.Update(tea.PasteMsg{Content: "search text"})
	value = updated.(dashboard)
	if !value.scrollback || !value.searchMode || value.searchQuery != "search text" {
		t.Fatal("paste left the search prompt instead of editing its query")
	}
	waitForGuestSilence(t, value.terminal, "")
}

func TestClippedModalActionDoesNotActivateOutsideBox(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height, value.modal = 24, 24, shutdownModal
	geometry := value.modalGeometry(value.bodySize())
	updated, command := value.Update(tea.MouseClickMsg{
		X: geometry.left + geometry.width, Y: geometry.top + len(geometry.lines) - 2, Button: tea.MouseLeft,
	})
	got := updated.(dashboard)
	if got.shutdownPending || command != nil || got.modal != shutdownModal {
		t.Fatal("click outside the visible action started daemon shutdown")
	}
}
