package ui

import (
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/version"
)

func TestDashboardAddsRootFromPrompt(t *testing.T) {
	backend := &fakeBackend{}
	value := newDashboard(backend, model.Snapshot{})

	send(&value, key(tea.KeyF2, ""))
	send(&value, key('/', "/"))
	send(&value, key(tea.KeyExtended, "/projects"))
	command := send(&value, key(tea.KeyEnter, ""))
	if command == nil {
		t.Fatal("add root command = nil")
	}
	send(&value, commandMessage[inputResultMsg](t, command))
	if backend.addedPath != "/projects" || value.errorMessage != "" {
		t.Fatalf("added path = %q, error = %q", backend.addedPath, value.errorMessage)
	}
}

func TestDashboardAcceptsPastedRootPath(t *testing.T) {
	backend := &fakeBackend{}
	value := newDashboard(backend, model.Snapshot{})

	send(&value, key(tea.KeyF2, ""))
	send(&value, key('/', "/"))
	send(&value, tea.PasteMsg{Content: "/projects/with spaces"})
	command := send(&value, key(tea.KeyEnter, ""))
	if command == nil {
		t.Fatal("add pasted root command = nil")
	}
	send(&value, commandMessage[inputResultMsg](t, command))
	if backend.addedPath != "/projects/with spaces" {
		t.Fatalf("added path = %q, want pasted path", backend.addedPath)
	}
}

func TestDashboardIgnoresRemovedPlusShortcut(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})

	command := send(&value, key('+', "+"))
	if value.modal != noModal || command != nil {
		t.Fatalf("+ result = (modal %v, command %v), want no action", value.modal, command)
	}
}

func TestDashboardConfirmsDaemonShutdown(t *testing.T) {
	backend := &fakeBackend{}
	terminal := newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)
	value := newDashboard(backend, model.Snapshot{})
	value.terminal = terminal
	value.width = 100
	value.height = 24

	command := send(&value, key(tea.KeyF9, ""))
	if value.modal != shutdownModal || command != nil || backend.shutdownCount != 0 {
		t.Fatalf("F9 result = (modal %v, command %v, shutdowns %d), want confirmation", value.modal, command, backend.shutdownCount)
	}
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "Stop daemon") || !strings.Contains(rendered, "all running terminal sessions") {
		t.Fatalf("shutdown warning is missing:\n%s", rendered)
	}

	command = send(&value, key(tea.KeyEscape, ""))
	if value.modal != noModal || command != nil || backend.shutdownCount != 0 {
		t.Fatalf("Esc result = (modal %v, command %v, shutdowns %d), want cancellation", value.modal, command, backend.shutdownCount)
	}

	send(&value, key(tea.KeyF9, ""))
	command = send(&value, key(tea.KeyEnter, ""))
	if command == nil || !value.shutdownPending || backend.shutdownCount != 0 {
		t.Fatalf("Enter result = (command %v, pending %v, shutdowns %d), want deferred shutdown", command, value.shutdownPending, backend.shutdownCount)
	}

	// Esc cannot take back a dispatched shutdown, and Enter must not repeat it.
	for _, message := range []tea.KeyPressMsg{key(tea.KeyEscape, ""), key(tea.KeyEnter, ""), key(tea.KeyF9, "")} {
		extra := send(&value, message)
		if extra != nil || value.modal != shutdownModal {
			t.Fatalf("%q during shutdown = (command %v, modal %v), want the modal held", message.String(), extra, value.modal)
		}
	}
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "STOPPING") {
		t.Fatalf("status bar does not report the pending shutdown:\n%s", rendered)
	}

	quitCommand := send(&value, commandMessage[daemonStoppedMsg](t, command))
	if backend.shutdownCount != 1 || !value.result.Quit || quitCommand == nil || value.terminal != nil {
		t.Fatalf("shutdown result = (count %d, quit %v, command %v, terminal %v)", backend.shutdownCount, value.result.Quit, quitCommand, value.terminal)
	}
}

func TestDashboardReportsFailedShutdown(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	send(&value, key(tea.KeyF9, ""))
	send(&value, key(tea.KeyEnter, ""))

	command := send(&value, daemonStoppedMsg{err: errors.New("dial unix: no such file")})
	if command != nil || value.shutdownPending || value.modal != noModal || value.result.Quit {
		t.Fatalf("failed shutdown = (command %v, pending %v, modal %v, quit %v), want the dashboard restored",
			command, value.shutdownPending, value.modal, value.result.Quit)
	}
	if !strings.Contains(value.errorMessage, "stop daemon: ") {
		t.Fatalf("error message = %q, want a stop daemon failure", value.errorMessage)
	}

	// Retrying is possible because the pending flag was cleared.
	send(&value, key(tea.KeyF9, ""))
	if value.modal != shutdownModal || value.errorMessage != "" {
		t.Fatalf("retry = (modal %v, error %q), want a fresh confirmation", value.modal, value.errorMessage)
	}
}

// The title sits on the border. A chip, the way romty draws every other label,
// keeps it from reading as part of one unbroken accent line.
func TestDashboardDrawsModalTitlesAsChips(t *testing.T) {
	for _, dark := range []bool{true, false} {
		styles := newUIStyles(dark)
		if styles.modalTitle.GetBackground() == styles.modalBorder.GetBackground() {
			t.Fatalf("dark=%v: the modal title has only its weight to be seen against the border",
				dark)
		}
		if styles.modalTitle.GetForeground() == styles.modalBorder.GetForeground() {
			t.Fatalf("dark=%v: the modal title is the border's own colour", dark)
		}
	}
}

// What is about to be deleted is named first and its path is the context under
// it, muted, so it does not read as a third consequence after the red warnings.
func TestDashboardNamesTheDeleteTargetBeforeItsConsequences(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 100, 30
	value.modal = removeSelectionModal
	value.removeTarget = navItem{workspace: model.Workspace{Name: "romty", Path: "/w/romty"}}

	lines := plainRows(value.renderModal(72, 28))
	var body []string
	for _, line := range lines[1 : len(lines)-1] {
		body = append(body, strings.TrimSpace(strings.Trim(line, "│")))
	}
	want := []string{
		"Delete romty?",
		"/w/romty",
		"",
		"This permanently deletes all contents.",
		"Its running shells will be terminated.",
	}
	if len(body) < len(want) || !slices.Equal(body[:len(want)], want) {
		t.Fatalf("delete modal body = %q, want the target named before its consequences %q", body, want)
	}
	rendered := strings.Join(value.renderModal(72, 28), "\n")
	if !strings.Contains(rendered, value.styles.empty.Render("/w/romty")) {
		t.Fatalf("the path is not muted against the name above it:\n%s", rendered)
	}
}

// A root romty cannot read is marked in the tree without failing the snapshot
// for its neighbours, and can be forgotten from the workspace actions.
func TestDashboardShowsAndRemovesAnUnreadableRoot(t *testing.T) {
	snapshot := model.Snapshot{Roots: []model.RootView{
		{
			Root:        model.Root{ID: "root-1", Name: "gone", Path: "/volumes/gone"},
			Error:       "open /volumes/gone: no such file or directory",
			Directories: []model.WorkspaceView{},
		},
		{
			Root:        model.Root{ID: "root-2", Name: "projects", Path: "/projects"},
			Directories: []model.WorkspaceView{{Workspace: model.Workspace{ID: "w", Name: "alpha", Path: "/projects/alpha"}}},
		},
	}}
	backend := &fakeBackend{snapshot: snapshot}
	value := newDashboard(backend, snapshot)
	value.width = 120
	value.height = 40

	rendered := ansi.Strip(value.render())
	if !strings.Contains(rendered, "✗ gone") {
		t.Fatalf("the unreadable root is not marked:\n%s", rendered)
	}
	if !strings.Contains(rendered, "▾ projects") || !strings.Contains(rendered, "alpha") {
		t.Fatalf("the healthy root did not survive its neighbour:\n%s", rendered)
	}

	value.setNavigation(0)
	command := send(&value, key(tea.KeyF8, ""))
	if value.modal != workspaceActionsModal || command != nil {
		t.Fatalf("F8 = (modal %v, command %v), want workspace actions", value.modal, command)
	}
	command = send(&value, key(tea.KeyEnter, ""))
	if value.modal != removeSelectionModal || command != nil {
		t.Fatalf("Forget root = (modal %v, command %v), want a confirmation", value.modal, command)
	}
	body := ansi.Strip(strings.Join(value.renderModal(value.width, value.dimensions().bodyHeight), "\n"))
	if !strings.Contains(body, "gone") || !strings.Contains(body, "directory stays on disk") ||
		!strings.Contains(body, "running shells will be terminated") {
		t.Fatalf("the confirmation does not describe the root removal:\n%s", body)
	}
	command = send(&value, key(tea.KeyEnter, ""))
	if command == nil || value.modal != noModal {
		t.Fatalf("Enter = (command %v, modal %v), want the removal", command, value.modal)
	}
	send(&value, command())
	if backend.removedRootID != "root-1" {
		t.Fatalf("removed %q, want root-1", backend.removedRootID)
	}
	rendered = ansi.Strip(value.render())
	if strings.Contains(rendered, "gone") || !strings.Contains(rendered, "projects") {
		t.Fatalf("removal left the wrong tree:\n%s", rendered)
	}
}

// Removal applies to the root the cursor is on, not the first one in the tree.
func TestDashboardRemovesTheSelectedRoot(t *testing.T) {
	snapshot := twoRootSnapshot()
	backend := &fakeBackend{snapshot: snapshot}
	value := newDashboard(backend, snapshot)
	value.setNavigation(1)

	updated, _ := value.confirmRemoveSelection(cursorItem(t, value))
	value = updated.(dashboard)
	command := send(&value, key(tea.KeyEnter, ""))
	if command == nil {
		t.Fatal("confirmation then Enter produced no removal command")
	}
	value.Update(command())
	if backend.removedRootID != "root-2" {
		t.Fatalf("removed %q, want root-2", backend.removedRootID)
	}
}

func TestDashboardDeletesTheSelectedWorkspace(t *testing.T) {
	snapshot, workspace := alphaWorkspaceSnapshot(nil)
	backend := &fakeBackend{snapshot: snapshot}
	value := newDashboard(backend, snapshot)
	value.width, value.height = 120, 40
	value.setNavigation(1)

	updated, command := value.confirmRemoveSelection(cursorItem(t, value))
	value = updated.(dashboard)
	if command != nil || value.modal != removeSelectionModal {
		t.Fatalf("remove = (command %v, modal %v), want a confirmation", command, value.modal)
	}
	body := ansi.Strip(strings.Join(value.renderModal(value.width, value.dimensions().bodyHeight), "\n"))
	if !strings.Contains(body, "Delete alpha?") || !strings.Contains(body, "permanently deletes all contents") ||
		!strings.Contains(body, "running shells will be terminated") {
		t.Fatalf("workspace confirmation does not describe the deletion:\n%s", body)
	}

	// Enter does nothing until the name is typed, and a wrong name is not it.
	if command := send(&value, key(tea.KeyEnter, "")); command != nil || value.modal != removeSelectionModal {
		t.Fatalf("Enter before the name = (command %v, modal %v), want the dialog kept", command, value.modal)
	}
	typeText(&value, "alph")
	if command := send(&value, key(tea.KeyEnter, "")); command != nil {
		t.Fatal("Enter with a partial name deleted the workspace")
	}
	typeText(&value, "a")

	command = send(&value, key(tea.KeyEnter, ""))
	if command == nil || value.modal != noModal {
		t.Fatalf("Enter = (command %v, modal %v), want the deletion", command, value.modal)
	}
	send(&value, command())
	if backend.removedWorkspaceRootID != "root-1" || backend.removedWorkspacePath != workspace.Path {
		t.Fatalf("removed workspace = (%q, %q), want (%q, %q)",
			backend.removedWorkspaceRootID, backend.removedWorkspacePath, "root-1", workspace.Path)
	}
	if backend.removedRootID != "" {
		t.Fatalf("workspace deletion removed root %q", backend.removedRootID)
	}
	if len(value.state.Roots) != 1 || len(value.state.Roots[0].Directories) != 0 {
		t.Fatalf("snapshot after deletion = %#v, want the root without alpha", value.state)
	}
}

// A refresh landing while the confirmation is open can move the cursor: the
// row it was on is gone, so the tree falls back to the position, which is now
// a different root's row. The answer has to apply to the workspace the
// question named, not to whatever the cursor drifted onto in between.
func TestDashboardDeletesTheWorkspaceTheConfirmationNamed(t *testing.T) {
	snapshot := model.Snapshot{Roots: []model.RootView{
		{
			Root:        model.Root{ID: "root-1", Name: "first", Path: "/first"},
			Directories: []model.WorkspaceView{{Workspace: model.Workspace{ID: "w", Name: "alpha", Path: "/first/alpha"}}},
		},
		{Root: model.Root{ID: "root-2", Name: "second", Path: "/second"}, Directories: []model.WorkspaceView{}},
	}}
	backend := &fakeBackend{snapshot: snapshot}
	value := newDashboard(backend, snapshot)

	// The cursor sits on first's alpha directory, whose root is first.
	value.setNavigation(1)
	if item, _ := value.navigationItem(); item.root.ID != "root-1" {
		t.Fatalf("the cursor starts on root %q, want root-1", item.root.ID)
	}

	updated, _ := value.confirmRemoveSelection(cursorItem(t, value))
	value = updated.(dashboard)

	// alpha is deleted on disk, so the row the cursor remembered is gone and
	// index 1 is now second's own row.
	pruned := model.Snapshot{Roots: []model.RootView{
		{Root: snapshot.Roots[0].Root, Directories: []model.WorkspaceView{}},
		snapshot.Roots[1],
	}}
	send(&value, snapshotMsg{value: pruned})
	if item, _ := value.navigationItem(); item.root.ID != "root-2" {
		t.Fatalf("the cursor drifted to root %q; the test needs it on root-2 to prove anything", item.root.ID)
	}

	typeText(&value, "alpha")
	command := send(&value, key(tea.KeyEnter, ""))
	if command == nil {
		t.Fatal("Enter produced no removal command")
	}
	value.Update(command())
	if backend.removedWorkspaceRootID != "root-1" || backend.removedWorkspacePath != "/first/alpha" {
		t.Fatalf("removed workspace = (%q, %q), want the workspace the confirmation named",
			backend.removedWorkspaceRootID, backend.removedWorkspacePath)
	}
}

func TestDashboardForgetsTheRootTheConfirmationNamed(t *testing.T) {
	snapshot := twoRootSnapshot()
	backend := &fakeBackend{snapshot: snapshot}
	value := newDashboard(backend, snapshot)

	updated, _ := value.confirmRemoveSelection(cursorItem(t, value))
	value = updated.(dashboard)
	send(&value, snapshotMsg{value: model.Snapshot{Roots: snapshot.Roots[1:]}})
	if item, _ := value.navigationItem(); item.root.ID != "root-2" {
		t.Fatalf("the cursor drifted to root %q; want root-2", item.root.ID)
	}

	command := send(&value, key(tea.KeyEnter, ""))
	if command == nil {
		t.Fatal("Enter produced no removal command")
	}
	value.Update(command())
	if backend.removedRootID != "root-1" {
		t.Fatalf("removed %q, want root-1 — the root the confirmation named", backend.removedRootID)
	}
}

// Opening a workspace is the user asking again, so pending retries start over
// and the next attach is immediate rather than delayed.
func TestDashboardForgetsRetriesWhenTheUserSelectsAgain(t *testing.T) {
	tabs := openTabs(1)
	value, backend, refresh := exitedDashboard(t, tabs, 0)

	// One immediate reconnect, one drop, one counted retry.
	open := send(&value, refresh())
	send(&value, open())
	send(&value, terminalOutputMsg{terminal: value.terminal, err: io.EOF})
	send(&value, refresh())
	if value.reattachAttempts == 0 || value.reattachTab == "" {
		t.Fatalf("no retry is pending: %d on %q", value.reattachAttempts, value.reattachTab)
	}

	send(&value, workspaceMsg{
		value:    model.Workspace{ID: "workspace-1", Name: "alpha", Path: "/projects/alpha"},
		snapshot: backend.snapshot,
		tabID:    "tab-1",
	})
	if value.reattachTab != "" || value.reattachAttempts != 0 {
		t.Fatalf("a user selection left %d retries pending on %q",
			value.reattachAttempts, value.reattachTab)
	}
}

// A modal is a box on top of romty, not a screen romty replaces itself with:
// the workspace tree and terminal stay visible around it, and the box is only
// as wide as its content needs.
func TestDashboardCentersModalOverTheDashboardItLeavesShowing(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 100
	value.height = 30

	send(&value, key('i', "i"))
	if value.modal != aboutModal {
		t.Fatalf("modal = %v, want about", value.modal)
	}
	rendered := value.render()
	if !strings.Contains(rendered, "About") || !strings.Contains(rendered, tagline) {
		t.Fatalf("about modal is missing:\n%s", rendered)
	}
	// The dashboard is still there to be read, in the receding tone a backdrop
	// is drawn in rather than in its own colours.
	if !strings.Contains(ansi.Strip(rendered), " romty ") {
		t.Fatalf("the dashboard behind the modal was blanked:\n%s", ansi.Strip(rendered))
	}
	if strings.Contains(rendered, value.styles.paneTitleActive.Render(" romty ")) {
		t.Fatalf("the dashboard behind the modal kept its own colours:\n%s", rendered)
	}

	bodyHeight := value.dimensions().bodyHeight
	modalLines := value.renderModal(value.width, bodyHeight)
	boxWidth := lipgloss.Width(modalLines[0])
	if boxWidth >= value.width {
		t.Fatalf("about modal width = %d, want less than the %d column screen", boxWidth, value.width)
	}
	if boxWidth < lipgloss.Width(tagline)+6 {
		t.Fatalf("about modal width = %d, too narrow for its longest line", boxWidth)
	}

	body := strings.Split(ansi.Strip(rendered), "\n")[:bodyHeight]
	top := (bodyHeight - len(modalLines)) / 2
	wantLeft := (value.width - boxWidth) / 2
	// By column, not by byte: the workspace pane under the box draws
	// box-drawing runes of its own.
	at := strings.Index(body[top], "╭")
	if at < 0 {
		t.Fatalf("about modal has no top-left corner on its first row:\n%s", ansi.Strip(rendered))
	}
	if left := lipgloss.Width(body[top][:at]); left != wantLeft {
		t.Fatalf("about modal left edge = %d, want %d:\n%s", left, wantLeft, ansi.Strip(rendered))
	}
	// The rows the box does not reach still carry the pane divider, which is
	// what says the dashboard is behind it rather than gone.
	divider := 0
	for row := range body {
		if row < top || row >= top+len(modalLines) {
			if strings.Contains(body[row], "│") {
				divider++
			}
		}
	}
	if divider == 0 {
		t.Fatalf("no dashboard row survived beside the modal:\n%s", ansi.Strip(rendered))
	}
	// About is where a user reads which romty they are running, so it names
	// the build rather than leaving a bug report to guess at it.
	if !strings.Contains(ansi.Strip(rendered), version.String()) {
		t.Fatalf("about modal does not show version %q:\n%s", version.String(), ansi.Strip(rendered))
	}

	send(&value, key(tea.KeyEscape, ""))
	if value.modal != noModal || strings.Contains(value.render(), tagline) {
		t.Fatalf("about modal did not close:\n%s", value.render())
	}
}

// The terminal pane forwards `?` to the shell, so F1 is how that pane opens
// Help.
func TestDashboardOpensHelpFromTheTerminalPane(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.focus = terminalPane
	value.terminal = newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)

	command := send(&value, key('?', "?"))
	if value.modal != noModal || command != nil {
		t.Fatalf("? in the terminal = (modal %v, command %v), want it forwarded to the shell", value.modal, command)
	}

	command = send(&value, key(tea.KeyF1, ""))
	if value.modal != helpModal || command != nil {
		t.Fatalf("F1 in the terminal = (modal %v, command %v), want the help modal", value.modal, command)
	}
}

// The help modal is the only place a user can look a shortcut up, so every key
// romty takes in both panes has to appear there. The reference is compared with
// the table that routes those keys rather than with a hand-copied list.
func TestHelpReferenceDocumentsEveryGlobalKey(t *testing.T) {
	documented := make(map[string]bool)
	for _, entry := range helpReference() {
		for _, keycap := range entry.keys {
			for _, name := range helpKeyNames(keycap) {
				documented[name] = true
			}
		}
	}
	for name := range globalKeys {
		if !documented[name] {
			t.Errorf("global key %q appears in no help entry", name)
		}
	}
}

func TestHelpReferenceEntriesAreComplete(t *testing.T) {
	for index, entry := range helpReference() {
		switch {
		case entry.isSection():
			if entry.note == "" || entry.description != "" || len(entry.keys) > 0 {
				t.Errorf("entry %d = %+v, want a section heading with only a note", index, entry)
			}
		case entry.description == "" || len(entry.keys) == 0:
			t.Errorf("entry %d = %+v, want a shortcut with a description and keys", index, entry)
		}
	}
	if first := helpReference()[0]; !first.isSection() {
		t.Errorf("first entry = %+v, want a section heading", first)
	}
}

func TestDashboardShowsCompleteShortcutReferenceInHelpModal(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 100
	value.height = 80
	// One line per reference entry, plus the name-and-version banner above them.
	if entries := value.helpEntries(); len(entries) != len(helpReference())+1 {
		t.Fatalf("help entries = %d, want %d", len(entries), len(helpReference())+1)
	}

	command := send(&value, key('?', "?"))
	if value.modal != helpModal || command != nil {
		t.Fatalf("? result = (modal %v, command %v), want help modal", value.modal, command)
	}
	bodyHeight := value.dimensions().bodyHeight
	modalLines := value.renderModal(value.width, bodyHeight)
	if width := lipgloss.Width(modalLines[0]); width != 80 {
		t.Fatalf("help modal width = %d, want 80", width)
	}
	plainLines := strings.Split(ansi.Strip(strings.Join(modalLines, "\n")), "\n")
	plain := strings.Join(plainLines, "\n")

	// Every entry the reference declares reaches the screen.
	for _, entry := range helpReference() {
		if entry.isSection() {
			if !strings.Contains(plain, entry.section) {
				t.Fatalf("help modal does not contain %q section:\n%s", entry.section, plain)
			}
			continue
		}
		if !helpContainsShortcut(plainLines, entry.description, entry.keys...) {
			t.Fatalf("help modal does not contain %v %q shortcut:\n%s", entry.keys, entry.description, plain)
		}
	}

	// Each function key is documented once, so no shortcut is listed twice
	// under different words. F6 is the exception: it toggles scrollback, and
	// the file view gives it a second meaning of its own.
	for _, name := range []string{"F1", "F2", "F3", "F4", "F5", "F6", "F7", "F8", "F9"} {
		want := 1
		if name == "F6" {
			want = 2
		}
		if count := strings.Count(plain, name); count != want {
			t.Fatalf("help modal contains %s %d times, want %d:\n%s", name, count, want, plain)
		}
	}
	if !strings.Contains(plain, version.String()) {
		t.Fatalf("help modal does not show version %q:\n%s", version.String(), plain)
	}
	if len(modalLines) > value.height-2 {
		t.Fatalf("help modal height = %d, want at most %d", len(modalLines), value.height-2)
	}
	for index, line := range modalLines {
		if lineWidth := lipgloss.Width(line); lineWidth > 80 {
			t.Fatalf("help modal line %d width = %d, want at most 80", index, lineWidth)
		}
	}
}

func TestDashboardScrollsHelpModalOnShortTerminals(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 100
	value.height = 20
	send(&value, key('?', "?"))

	bodyHeight := value.dimensions().bodyHeight
	lines := strings.Split(value.render(), "\n")
	modalLines := value.renderModal(value.width, bodyHeight)
	if len(modalLines) > bodyHeight {
		t.Fatalf("help modal height = %d, want at most %d", len(modalLines), bodyHeight)
	}
	if !strings.HasPrefix(ansi.Strip(modalLines[0]), "╭─ Help 1-") ||
		!strings.HasPrefix(ansi.Strip(modalLines[len(modalLines)-1]), "╰─") {
		t.Fatalf("help modal is not a terminated box with a range title:\n%s", ansi.Strip(strings.Join(modalLines, "\n")))
	}
	if !strings.Contains(ansi.Strip(lines[len(lines)-1]), "scroll") {
		t.Fatalf("status bar does not offer the scroll shortcut:\n%s", ansi.Strip(lines[len(lines)-1]))
	}

	// The last entry is off screen at this height and must be reachable.
	for range value.helpEntries() {
		send(&value, key(tea.KeyDown, ""))
	}
	plain := ansi.Strip(strings.Join(value.renderModal(value.width, bodyHeight), "\n"))
	if !strings.Contains(plain, "Adjust pane width") {
		t.Fatalf("help modal did not scroll to the last shortcut:\n%s", plain)
	}
	if strings.Contains(plain, "About") {
		t.Fatalf("help modal kept the first shortcut after scrolling to the end:\n%s", plain)
	}
}

// The backoff damps a terminal that is dropping now, not one that has dropped
// before. A tab that stays attached past the healthy interval starts its retry
// count over, so old drops never count towards the ceiling.
func TestDashboardForgetsRetriesAfterAHealthyAttach(t *testing.T) {
	tabs := openTabs(1)
	value, _, refresh := exitedDashboard(t, tabs, 0)

	// One immediate reconnect, one drop, one counted retry — the same walk the
	// backoff test makes, so the two disagree only about what time it is.
	open := send(&value, refresh())
	send(&value, open())
	send(&value, terminalOutputMsg{terminal: value.terminal, err: io.EOF})
	send(&value, refresh())
	if value.reattachAttempts != 1 {
		t.Fatalf("retries after one drop = %d, want 1", value.reattachAttempts)
	}

	// Reopen, then let the terminal run past the healthy interval before it
	// drops. That drop starts over: an immediate reconnect, not a delayed one.
	open = send(&value, reopenTerminalMsg{tabID: "tab-1"})
	send(&value, open())
	value.terminalOpenedAt = value.terminalOpenedAt.Add(-2 * healthyAttachInterval)

	send(&value, terminalOutputMsg{terminal: value.terminal, err: io.EOF})
	command := send(&value, refresh())
	if value.reattachAttempts != 0 {
		t.Fatalf("retries after a healthy attach = %d, want them forgotten", value.reattachAttempts)
	}
	if command == nil {
		t.Fatal("no reconnect after a healthy attach dropped")
	}
	if _, delayed := command().(reopenTerminalMsg); delayed {
		t.Fatal("a drop after a healthy attach was delayed, want an immediate reconnect")
	}
}
