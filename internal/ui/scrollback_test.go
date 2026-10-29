package ui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/opspresso/romty/internal/model"
)

func TestDashboardIgnoresAnOlderWorkspaceSelection(t *testing.T) {
	workspace := func(id string) model.Workspace {
		return model.Workspace{ID: id, RootID: "root-1", Name: id, Path: "/projects/" + id}
	}
	tree := func(revision uint64, names ...string) model.Snapshot {
		directories := make([]model.WorkspaceView, 0, len(names))
		for _, name := range names {
			value := workspace(name)
			directories = append(directories, model.WorkspaceView{
				Workspace: value,
				Tabs:      []model.Tab{{ID: name + "-tab", WorkspaceID: value.ID, Running: true}},
			})
		}
		return model.Snapshot{Revision: revision, Roots: []model.RootView{{
			Root:        projectsRoot,
			Directories: directories,
		}}}
	}

	value := newDashboard(&fakeBackend{}, tree(1, "alpha", "beta"))
	value.selectionRequest = 2
	send(&value, workspaceMsg{
		value: workspace("beta"), snapshot: tree(3, "alpha", "beta"),
		order: 2, selection: 2, tabID: "beta-tab",
	})
	command := send(&value, workspaceMsg{
		value: workspace("alpha"), snapshot: tree(2, "alpha", "beta"),
		order: 1, selection: 1, tabID: "alpha-tab",
	})

	if command != nil || value.selectedWorkspaceID != "beta" || value.selectedPath != "/projects/beta" {
		t.Fatalf("stale selection result = (command %v, workspace %q, path %q)", command, value.selectedWorkspaceID, value.selectedPath)
	}
}

func TestCreateTabErrorKeepsItsSelection(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.selectionRequest = 1
	command := value.createTab()
	value.selectionRequest = 2

	raw := command()
	message, ok := raw.(tabMsg)
	if !ok {
		t.Fatalf("createTab() message = %T, want tabMsg", raw)
	}
	if message.selection != 1 {
		t.Fatalf("createTab() selection = %d, want 1", message.selection)
	}
}

func TestDashboardCreatesOnlyOneTabWhileASelectionIsPending(t *testing.T) {
	snapshot, workspace := alphaWorkspaceSnapshot(nil)
	backend := &fakeBackend{
		snapshot:   snapshot,
		workspace:  workspace,
		createdTab: model.Tab{ID: "tab-1", WorkspaceID: workspace.ID, Name: "1", Running: true},
	}
	value := newDashboard(backend, snapshot)
	value.setNavigation(1)

	first := value.selectWorkspace()
	second := value.selectWorkspace()
	firstMessage := first()
	secondMessage := second()
	create := send(&value, secondMessage)
	stale := send(&value, firstMessage)
	if stale != nil {
		t.Fatal("older selection created a second tab command")
	}
	if retry := value.selectWorkspace(); retry != nil {
		t.Fatal("selection accepted another Enter while tab creation was pending")
	}
	if create == nil {
		t.Fatal("newest selection did not create a tab command")
	}
	send(&value, commandMessage[tabMsg](t, create))
	if backend.createCount != 1 || value.tabPending {
		t.Fatalf("tab creation = (calls %d, pending %t), want (1, false)", backend.createCount, value.tabPending)
	}
}

func TestDashboardMouseWheelScrollsTheWorkspaceWithoutMovingItsCursor(t *testing.T) {
	directories := make([]model.WorkspaceView, 0, 6)
	for index := range 6 {
		name := fmt.Sprintf("workspace-%02d", index)
		directories = append(directories, model.WorkspaceView{Workspace: model.Workspace{
			ID: name, RootID: "root-1", Name: name, Path: "/projects/" + name,
		}})
	}
	value := newDashboard(&fakeBackend{}, model.Snapshot{Roots: []model.RootView{{
		Root: projectsRoot, Directories: directories,
	}}})
	value.gitStates = repositoryStates(directories)
	value.width, value.height = 120, 10
	value.focus = terminalPane
	before := ansi.Strip(strings.Join(value.renderNavigation(value.dimensions().leftWidth, value.dimensions().bodyHeight), "\n"))

	command := send(&value, tea.MouseWheelMsg{X: 2, Y: 3, Button: tea.MouseWheelDown})
	if value.navIndex != 0 || value.cursorPath != "/projects" || value.focus != terminalPane || command != nil {
		t.Fatalf("workspace wheel = (index %d, path %q, focus %v, command %v)",
			value.navIndex, value.cursorPath, value.focus, command)
	}
	after := ansi.Strip(strings.Join(value.renderNavigation(value.dimensions().leftWidth, value.dimensions().bodyHeight), "\n"))
	if before == after || strings.Contains(after, "workspace-00") || !strings.Contains(after, "workspace-02") {
		t.Fatalf("workspace wheel did not move the viewport:\n%s\nwant later workspaces", after)
	}
	if index, ok := value.navigationIndexAtRow(3, value.dimensions().bodyHeight); !ok || index != 3 {
		t.Fatalf("visible row hit = (%d, %v), want workspace-02", index, ok)
	}
	if value.hover.kind != hoverNavigation || value.hover.index != 3 {
		t.Fatalf("hover after wheel = (%v, %d), want workspace-02", value.hover.kind, value.hover.index)
	}
	offset := value.navOffset
	value.ensureWorkspaceCursor()
	if value.navOffset != offset {
		t.Fatalf("snapshot cursor sync moved the scrolled viewport from %d to %d", offset, value.navOffset)
	}
}

func TestDashboardScrollsTerminalHistory(t *testing.T) {
	value := scrolledDashboard(t, 200)
	history := value.terminal.scrollbackLen()

	// The live screen captures the wheel so it can enter scrollback directly.
	// Button events are enough for that; terminal focus asks for no motion.
	if value.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("mouse mode outside scrollback = %v, want button events", value.View().MouseMode)
	}
	live := plainRows(value.terminal.renderViewport(0))

	command := send(&value, key(tea.KeyF6, ""))
	if !value.scrollback || value.scrollOffset != 0 {
		t.Fatalf("F6 = (scrollback %v, offset %d), want the live view in scrollback mode",
			value.scrollback, value.scrollOffset)
	}
	if sequences := rawSequences(command); !slices.Contains(sequences, ansi.SetMode(altScrollMode)) {
		t.Fatalf("F6 sequences = %q, want alternate scroll asked for", sequences)
	}
	view := value.View()
	if view.MouseMode != tea.MouseModeNone || view.Cursor != nil {
		t.Fatalf("copy mode view = (mouse %v, cursor %v), want the mouse left with the host", view.MouseMode, view.Cursor)
	}

	// The wheel arrives as arrow keys through the alternate scroll copy mode
	// just asked for, which is what keeps the host's drag selection alive.
	send(&value, key(tea.KeyUp, ""))
	if value.scrollOffset != 1 {
		t.Fatalf("wheel up offset = %d, want 1", value.scrollOffset)
	}
	if slices.Equal(plainRows(value.terminal.renderViewport(value.scrollOffset)), live) {
		t.Fatal("scrolling up did not change the rendered viewport")
	}
	send(&value, key(tea.KeyDown, ""))
	if value.scrollOffset != 0 {
		t.Fatalf("wheel down offset = %d, want back at the live screen", value.scrollOffset)
	}

	// Repeated Shift+PgUp reaches the oldest retained line and clamps there.
	for range history/value.scrollbackPage() + 1 {
		send(&value, tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModShift}))
	}
	if value.scrollOffset != history {
		t.Fatalf("Shift+PgUp offset = %d, want the full history %d", value.scrollOffset, history)
	}
	oldest := plainRows(value.terminal.renderViewport(value.scrollOffset))[0]
	retained := plainRows([]string{value.terminal.emulator.Scrollback().Line(0).Render()})[0]
	if oldest != retained {
		t.Fatalf("oldest visible row = %q, want the first retained line %q", oldest, retained)
	}
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "SCROLLBACK") ||
		!strings.Contains(rendered, fmt.Sprintf("oldest · %d lines", history)) ||
		!strings.Contains(rendered, "Esc  exit") {
		t.Fatalf("status bar does not report the scrollback position:\n%s", rendered)
	}
	for range history/value.scrollbackPage() + 1 {
		send(&value, tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown, Mod: tea.ModShift}))
	}
	if value.scrollOffset != 0 {
		t.Fatalf("Shift+PgDown offset = %d, want the live screen", value.scrollOffset)
	}

	command = send(&value, key(tea.KeyEscape, ""))
	if value.scrollback || value.scrollOffset != 0 || value.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("Esc = (scrollback %v, offset %d, mouse %v), want live wheel capture restored",
			value.scrollback, value.scrollOffset, value.View().MouseMode)
	}
	if sequences := rawSequences(command); !slices.Contains(sequences, ansi.ResetMode(altScrollMode)) {
		t.Fatalf("Esc sequences = %q, want alternate scroll given up", sequences)
	}
}

func TestDashboardEntersScrollbackFromLiveMouseWheel(t *testing.T) {
	value := scrolledDashboard(t, 200)
	leftWidth := value.dimensions().leftWidth

	send(&value, tea.MouseWheelMsg{
		X: leftWidth + 3, Y: terminalTop + 2, Button: tea.MouseWheelUp,
	})
	if !value.scrollback || value.scrollOffset != 3 {
		t.Fatalf("wheel up = (scrollback %v, offset %d), want three lines of history",
			value.scrollback, value.scrollOffset)
	}

	send(&value, key('x', "x"))
	if value.scrollback || value.scrollOffset != 0 {
		t.Fatalf("typing = (scrollback %v, offset %d), want the live screen",
			value.scrollback, value.scrollOffset)
	}
	waitForGuest(t, guest(value), "x")
}

// Outside scrollback the host's alternate scroll turns a wheel notch into three
// arrow presses romty cannot tell from typed ones, which walked the workspace
// tree and reached the shell as history keys. romty holds the mode off until
// scrollback, the one state that wants those keys, opens.
func TestDashboardHoldsAlternateScrollForScrollback(t *testing.T) {
	value := scrolledDashboard(t, 200)

	if sequences := rawSequences(value.Init()); !slices.Contains(sequences, ansi.ResetMode(altScrollMode)) {
		t.Fatalf("startup sequences = %q, want alternate scroll turned off", sequences)
	}

	// A key that changes nothing about scrollback repeats no sequence: the mode
	// moves on the transitions, not on every message.
	command := send(&value, key(tea.KeyDown, ""))
	if sequences := rawSequences(command); len(sequences) != 0 {
		t.Fatalf("sequences without a transition = %q, want none", sequences)
	}

	command = send(&value, key(tea.KeyF6, ""))
	if sequences := rawSequences(command); !slices.Contains(sequences, ansi.SetMode(altScrollMode)) {
		t.Fatalf("entering scrollback = %q, want alternate scroll asked for", sequences)
	}
	command = send(&value, key(tea.KeyUp, ""))
	if sequences := rawSequences(command); len(sequences) != 0 {
		t.Fatalf("scrolling inside scrollback = %q, want the mode left alone", sequences)
	}

	// F7 leaves scrollback for the workspace pane, which is one of the seven
	// paths out: the mode follows the state rather than a particular key.
	command = send(&value, key(tea.KeyF7, ""))
	if value.scrollback {
		t.Fatal("F7 left scrollback open")
	}
	if sequences := rawSequences(command); !slices.Contains(sequences, ansi.ResetMode(altScrollMode)) {
		t.Fatalf("leaving scrollback = %q, want alternate scroll given up", sequences)
	}
}

func TestDashboardEntersScrollbackFromEveryBinding(t *testing.T) {
	page := scrolledDashboard(t, 200).scrollbackPage()

	for _, step := range []struct {
		name    string
		enter   []tea.KeyPressMsg
		focus   pane
		offset  int
		wheelOK bool
	}{
		{name: "F6", enter: []tea.KeyPressMsg{key(tea.KeyF6, "")}, focus: terminalPane},
		{
			name:  "Ctrl+Shift+\\",
			enter: []tea.KeyPressMsg{scrollbackToggleKey()},
			focus: terminalPane,
		},
		{
			name:  "Ctrl+Shift+\\ from the workspace pane",
			enter: []tea.KeyPressMsg{scrollbackToggleKey()},
			focus: leftPane,
		},
		{
			name: "Ctrl+Shift+\\ with an alternate layout",
			enter: []tea.KeyPressMsg{tea.KeyPressMsg(tea.Key{
				Code: '₩', ShiftedCode: '|', BaseCode: '\\', Mod: tea.ModCtrl | tea.ModShift,
			})},
			focus: terminalPane,
		},
		{
			name:   "Shift+PgUp",
			enter:  []tea.KeyPressMsg{tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModShift})},
			focus:  terminalPane,
			offset: page,
		},
		{
			name:   "Shift+PgUp from the workspace pane",
			enter:  []tea.KeyPressMsg{tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModShift})},
			focus:  leftPane,
			offset: page,
		},
	} {
		t.Run(step.name, func(t *testing.T) {
			value := scrolledDashboard(t, 200)
			value.focus = step.focus
			for _, message := range step.enter {
				send(&value, message)
			}
			if !value.scrollback || value.scrollOffset != step.offset {
				t.Fatalf("%s = (scrollback %v, offset %d), want scrollback at offset %d",
					step.name, value.scrollback, value.scrollOffset, step.offset)
			}
		})
	}
}

func TestDashboardHoldsScrollbackViewportOnNewOutput(t *testing.T) {
	value := scrolledDashboard(t, 200)
	send(&value, tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModShift}))
	anchored := plainRows(value.terminal.renderViewport(value.scrollOffset))
	before := value.scrollOffset

	send(&value, terminalOutputMsg{terminal: value.terminal, data: []byte("fresh-a\r\nfresh-b\r\n")})
	if value.scrollOffset == before {
		t.Fatalf("offset stayed at %d while new output pushed lines into the scrollback", before)
	}
	if !slices.Equal(plainRows(value.terminal.renderViewport(value.scrollOffset)), anchored) {
		t.Fatalf("viewport drifted when new output arrived while scrolled back:\n%s\nwant\n%s",
			strings.Join(plainRows(value.terminal.renderViewport(value.scrollOffset)), "\n"), strings.Join(anchored, "\n"))
	}

	// Leaving scrollback returns to the live screen, which has the new output.
	send(&value, key(tea.KeyF6, ""))
	if value.scrollback || value.scrollOffset != 0 {
		t.Fatalf("F6 = (scrollback %v, offset %d), want the live screen", value.scrollback, value.scrollOffset)
	}
	if !strings.Contains(ansi.Strip(strings.Join(value.terminal.renderViewport(0), "\n")), "fresh-b") {
		t.Fatal("live screen does not show the output that arrived during scrollback")
	}
}

// Full-screen applications such as vim, less, and Claude Code switch to the
// alternate screen, which has no history of its own. romty must not offer the
// main screen's older output as if it belonged to them.
func TestDashboardRefusesScrollbackOnTheAlternateScreen(t *testing.T) {
	value := scrolledDashboard(t, 200)
	send(&value, key(tea.KeyF6, ""))
	if !value.scrollback {
		t.Fatal("scrollback did not open on the main screen")
	}

	// The application takes over the screen while scrollback is open.
	send(&value, terminalOutputMsg{
		terminal: value.terminal,
		data:     []byte("\x1b[?1049h\x1b[2J\x1b[Happlication frame\r\n"),
	})
	if value.scrollback || value.scrollOffset != 0 {
		t.Fatalf("scrollback stayed open on the alternate screen = (scrollback %v, offset %d)",
			value.scrollback, value.scrollOffset)
	}
	if !strings.Contains(value.errorMessage, "owns the screen") {
		t.Fatalf("error message = %q, want an explanation of the alternate screen", value.errorMessage)
	}

	// Re-entering must not resurrect the history from before the application.
	for _, message := range []tea.KeyPressMsg{
		key(tea.KeyF6, ""),
		tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModShift}),
	} {
		send(&value, message)
		if value.scrollback {
			t.Fatalf("%q entered scrollback while the alternate screen was active", message.String())
		}
	}
	rows := plainRows(value.terminal.renderViewport(500))
	if rows[0] != "application frame" {
		t.Fatalf("viewport row 0 = %q, want the live alternate screen", rows[0])
	}
	if strings.Contains(strings.Join(rows, "\n"), "line-") {
		t.Fatalf("viewport leaked pre-application output:\n%s", strings.Join(rows, "\n"))
	}

	// Leaving the alternate screen restores the history that was there before.
	send(&value, terminalOutputMsg{terminal: value.terminal, data: []byte("\x1b[?1049l")})
	send(&value, key(tea.KeyF6, ""))
	if !value.scrollback {
		t.Fatalf("scrollback did not reopen after the application exited: %q", value.errorMessage)
	}
}

// Scrollback with nothing to show is a state romty explains, not a failure it
// suffered, so the status bar must not raise the red ERROR that a refused
// daemon call or a dead terminal earns.
func TestDashboardReportsEmptyScrollbackAsANotice(t *testing.T) {
	value := terminalDashboard(t)

	send(&value, key(tea.KeyF6, ""))
	if value.scrollback || !strings.Contains(value.errorMessage, "scrolled off") {
		t.Fatalf("F6 with an empty scrollback = (scrollback %v, message %q)",
			value.scrollback, value.errorMessage)
	}
	rendered := ansi.Strip(value.render())
	if !strings.Contains(rendered, "NOTE") || strings.Contains(rendered, "ERROR") {
		t.Fatalf("the status bar does not report the empty scrollback as a notice:\n%s", rendered)
	}

	// A failure after the notice still takes the bar as an error.
	value.setError(terminalError, "resize terminal: refused")
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "ERROR") {
		t.Fatalf("a failure raised after a notice does not show as an error:\n%s", rendered)
	}
}

// Copy mode drops the workspace pane so that a plain drag in the host terminal
// selects terminal output alone. In the split layout every host row also holds
// the workspace tree, which a multi-line selection would copy along with it.
func TestDashboardRendersCopyModeFullWidth(t *testing.T) {
	value := scrolledDashboard(t, 200)
	value.state = model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "nalbam", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: model.Workspace{ID: "w", Name: "SnowClash", Path: "/p/s"}}},
	}}}
	if !strings.Contains(ansi.Strip(value.render()), "SnowClash") {
		t.Fatal("the split layout does not show the workspace tree")
	}

	send(&value, key(tea.KeyF6, ""))
	bodyHeight := value.dimensions().bodyHeight
	body := strings.Split(ansi.Strip(value.render()), "\n")[:bodyHeight]
	joined := strings.Join(body, "\n")
	if strings.Contains(joined, "SnowClash") || strings.Contains(joined, "nalbam") || strings.Contains(joined, "│") {
		t.Fatalf("copy mode still renders the workspace pane and divider:\n%s", joined)
	}
	if !strings.Contains(joined, "line-") {
		t.Fatalf("copy mode does not render terminal output:\n%s", joined)
	}

	send(&value, key(tea.KeyEscape, ""))
	if !strings.Contains(ansi.Strip(value.render()), "SnowClash") {
		t.Fatal("leaving copy mode did not restore the workspace pane")
	}
}

// An application that owns the screen has a history of its own and romty has
// none to offer, so the wheel goes to vim, less, or an agent.
func TestDashboardGivesTheWheelToAGuestThatOwnsTheScreen(t *testing.T) {
	t.Run("a guest that asked for the mouse", func(t *testing.T) {
		value := scrolledDashboard(t, 200)
		leftWidth := value.dimensions().leftWidth
		// The alternate screen and a mouse request, the way Claude Code and
		// htop start.
		value.terminal.writeOutput([]byte("\x1b[?1049h\x1b[?1003h\x1b[?1006h"))

		send(&value, tea.MouseWheelMsg{
			X: leftWidth + separatorWidth + 4, Y: terminalTop + 2, Button: tea.MouseWheelUp,
		})
		if value.scrollback {
			t.Fatal("the wheel opened scrollback on the alternate screen")
		}
		waitForGuest(t, guest(value), "\x1b[<64;5;3")
	})

	t.Run("a guest that did not", func(t *testing.T) {
		value := scrolledDashboard(t, 200)
		leftWidth := value.dimensions().leftWidth
		// less and man own the screen without ever asking for the mouse, and a
		// terminal gives them the cursor keys of alternate scroll instead.
		value.terminal.writeOutput([]byte("\x1b[?1049h"))

		send(&value, tea.MouseWheelMsg{
			X: leftWidth + separatorWidth + 4, Y: terminalTop + 2, Button: tea.MouseWheelUp,
		})
		if value.scrollback {
			t.Fatal("the wheel opened scrollback on the alternate screen")
		}
		waitForGuest(t, guest(value), "\x1b[A\x1b[A\x1b[A")

		value.Update(tea.MouseWheelMsg{
			X: leftWidth + separatorWidth + 4, Y: terminalTop + 2, Button: tea.MouseWheelDown,
		})
		waitForGuest(t, guest(value), "\x1b[B\x1b[B\x1b[B")
	})
}

// Silence is what a wheel that the host never reports looks like, so the one
// state where romty has nothing to scroll has to say so instead.
func TestDashboardSaysWhyTheWheelHasNothingToScroll(t *testing.T) {
	value := terminalDashboard(t)

	send(&value, tea.MouseWheelMsg{
		X: value.dimensions().leftWidth + separatorWidth + 4, Y: terminalTop + 2, Button: tea.MouseWheelUp,
	})
	if value.scrollback {
		t.Fatal("scrollback opened with no history to show")
	}
	if !value.noticeMessage || !strings.Contains(value.errorMessage, "scrolled off") {
		t.Fatalf("status bar = (notice %v, %q), want the reason the wheel did nothing",
			value.noticeMessage, value.errorMessage)
	}
}

// A terminal with no alternate scroll sends nothing once romty hands the mouse
// back, which leaves scrollback unscrollable on a phone and on some desktop
// terminals. Keeping the mouse is the trade the option makes.
func TestDashboardKeepsTheMouseInScrollbackWhenAsked(t *testing.T) {
	value := scrolledDashboard(t, 200)
	value.config.ScrollbackMouse = true

	command := send(&value, key(tea.KeyF6, ""))
	if !value.scrollback {
		t.Fatal("F6 did not open scrollback")
	}
	if view := value.View(); view.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("scrollback mouse mode = %v, want romty to keep the wheel", view.MouseMode)
	}
	if sequences := rawSequences(command); slices.Contains(sequences, ansi.SetMode(altScrollMode)) {
		t.Fatalf("sequences = %q, want no alternate scroll while romty holds the mouse", sequences)
	}

	send(&value, tea.MouseWheelMsg{X: 40, Y: terminalTop + 2, Button: tea.MouseWheelUp})
	if value.scrollOffset != wheelLines {
		t.Fatalf("scroll offset = %d, want one notch of history", value.scrollOffset)
	}

	// Off again, the host gets the mouse back for its own drag selection.
	value.config.ScrollbackMouse = false
	if view := value.View(); view.MouseMode != tea.MouseModeNone {
		t.Fatalf("scrollback mouse mode = %v, want the mouse returned to the host", view.MouseMode)
	}
}

// Scrollback is romty's own view of output the guest has already printed, so
// passthrough must not take the wheel there and scroll the guest instead of the
// history the user opened.
func TestDashboardScrollsHistoryNotTheGuestWithBothMouseOptions(t *testing.T) {
	value := scrolledDashboard(t, 200)
	value.config.ScrollbackMouse = true
	value.config.MousePassthrough = true
	value.terminal.writeOutput([]byte("\x1b[?1003h\x1b[?1006h"))

	send(&value, key(tea.KeyF6, ""))
	if !value.scrollback {
		t.Fatal("F6 did not open scrollback")
	}
	sent := guest(value).String()

	send(&value, tea.MouseWheelMsg{X: 40, Y: terminalTop + 2, Button: tea.MouseWheelUp})
	if value.scrollOffset != wheelLines {
		t.Fatalf("scroll offset = %d, want the wheel to move the history", value.scrollOffset)
	}
	waitForGuestSilence(t, value.terminal, sent)
}

func TestDashboardTogglesScrollbackMouseFromConfig(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	send(&value, key(tea.KeyF3, ""))
	if value.modal != configModal {
		t.Fatalf("F3 modal = %v, want config", value.modal)
	}

	command := send(&value, key('m', "m"))
	if !value.config.ScrollbackMouse || command == nil {
		t.Fatalf("m = (scrollback mouse %v, command %v), want the setting on and saved",
			value.config.ScrollbackMouse, command)
	}
	if !configRowReads(ansi.Strip(value.render()), "Scrollback mouse", "on") {
		t.Fatalf("config modal does not report the setting:\n%s", ansi.Strip(value.render()))
	}

	send(&value, key('m', "m"))
	if value.config.ScrollbackMouse {
		t.Fatal("m did not turn the setting back off")
	}
}

func TestDashboardFocusesTerminalWhenLeavingScrollback(t *testing.T) {
	for _, leave := range []tea.KeyPressMsg{scrollbackToggleKey(), key(tea.KeyEscape, ""), key(tea.KeyF6, "")} {
		value := scrolledDashboard(t, 200)
		value.focus = leftPane
		send(&value, scrollbackToggleKey())
		if !value.scrollback || value.focus != leftPane {
			t.Fatalf("Ctrl+Shift+\\ = (scrollback %v, focus %v), want scrollback without moving focus",
				value.scrollback, value.focus)
		}
		send(&value, leave)
		if value.scrollback || value.focus != terminalPane {
			t.Fatalf("%q = (scrollback %v, focus %v), want the terminal focused",
				leave.String(), value.scrollback, value.focus)
		}
		if value.View().Cursor == nil {
			t.Fatalf("%q left the terminal without a cursor", leave.String())
		}
	}
}

func TestDashboardLeavesScrollbackForTerminalInput(t *testing.T) {
	for _, test := range []struct {
		message tea.KeyPressMsg
		want    string
	}{
		{message: key('x', "x"), want: "x"},
		{message: key('k', "k"), want: "k"},
		{message: key(tea.KeyEnter, ""), want: "\r"},
		{message: key(tea.KeyPgUp, ""), want: "\x1b[5~"},
		{message: controlBackslashKey(), want: "\x1c"},
	} {
		t.Run(test.message.String(), func(t *testing.T) {
			value := scrolledDashboard(t, 200)
			send(&value, key(tea.KeyF6, ""))

			send(&value, test.message)
			if value.scrollback || value.scrollOffset != 0 || value.focus != terminalPane {
				t.Fatalf("%q = (scrollback %v, offset %d, focus %v), want terminal input mode",
					test.message.String(), value.scrollback, value.scrollOffset, value.focus)
			}
			waitForGuest(t, guest(value), test.want)
		})
	}
}

func TestDashboardLeavesScrollbackForPaste(t *testing.T) {
	value := scrolledDashboard(t, 200)
	send(&value, key(tea.KeyF6, ""))

	send(&value, tea.PasteMsg{Content: "pasted"})
	if value.scrollback || value.scrollOffset != 0 || value.focus != terminalPane {
		t.Fatalf("paste = (scrollback %v, offset %d, focus %v), want terminal input mode",
			value.scrollback, value.scrollOffset, value.focus)
	}
	waitForGuest(t, guest(value), "pasted")
}

func TestDashboardRefusesScrollbackWithoutTerminal(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})

	for _, message := range []tea.KeyPressMsg{
		key(tea.KeyF6, ""),
		tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModShift}),
		scrollbackToggleKey(),
	} {
		send(&value, message)
		if value.scrollback {
			t.Fatalf("%q entered scrollback with no terminal open", message.String())
		}
	}
	if value.View().MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("mouse mode = %v, want dashboard clicks enabled", value.View().MouseMode)
	}
}

func TestDashboardCapturesLiveMouseWheelAndUsesKeyboardFocus(t *testing.T) {
	stream := newMemoryStream("")
	terminal := newEmbeddedTerminal("tab-1", stream, 40, 10)
	defer terminal.close()

	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 120
	value.height = 40
	value.terminal = terminal
	value.focus = terminalPane

	if view := value.View(); view.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("initial mouse mode = %v, want button events", view.MouseMode)
	}

	send(&value, controlSlashKey())
	if value.focus != leftPane {
		t.Fatalf("Ctrl+/ focus = %v, want workspace pane", value.focus)
	}
	separator := value.styles.dividerActive.Render("◀") + value.styles.divider.Render("│") + " "
	if rendered := value.render(); !strings.Contains(rendered, separator) {
		t.Fatalf("workspace focus is not visible:\n%s", rendered)
	}
	if view := value.View(); view.MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("mouse mode after Ctrl+/ = %v, want all motion", view.MouseMode)
	}
}

func TestDashboardRewritesAStaleSelectionSave(t *testing.T) {
	tabs := []model.Tab{
		{ID: "tab-1", WorkspaceID: "workspace-1", Running: true},
		{ID: "tab-2", WorkspaceID: "workspace-1", Running: true},
	}
	snapshot, workspace := alphaWorkspaceSnapshot(tabs)
	configPath := filepath.Join(t.TempDir(), "config.json")
	value := newDashboardWithConfig(&fakeBackend{}, snapshot, configPath, Config{})
	value.config.LastWorkspacePath = workspace.Path
	value.config.LastTabID = tabs[1].ID

	rewrite := send(&value, configSavedMsg{
		saved: Config{LastWorkspacePath: workspace.Path, LastTabID: tabs[0].ID},
	})
	if rewrite == nil {
		t.Fatal("an older selection save was accepted as current")
	}
	rewrite()
	loaded, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if loaded.LastWorkspacePath != workspace.Path || loaded.LastTabID != tabs[1].ID {
		t.Fatalf("rewritten selection = (%q, %q), want (%q, %q)",
			loaded.LastWorkspacePath, loaded.LastTabID, workspace.Path, tabs[1].ID)
	}
}

func TestDashboardFindsAndWalksScrollbackMatches(t *testing.T) {
	value := scrolledDashboard(t, 200)
	send(&value, tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModShift}))
	if !value.scrollback {
		t.Fatal("Shift+PgUp did not enter scrollback")
	}

	value = typeSearch(t, value, "line-004")
	if len(value.search.matches) != 1 {
		t.Fatalf("matches = %v, want the one line-004", value.search.matches)
	}
	if !strings.Contains(plainStatus(value), "line-004 1/1") {
		t.Fatalf("status = %q, want the match counted", plainStatus(value))
	}
	// The match is on screen at the offset the search scrolled to.
	if !scrollbackShows(value, "line-004") {
		t.Fatalf("viewport does not show the match:\n%s", strings.Join(plainRows(value.terminal.renderViewport(value.scrollOffset)), "\n"))
	}

	// A query on many lines starts at the newest and walks towards the oldest.
	value = typeSearch(t, value, "line-01")
	if len(value.search.matches) != 10 {
		t.Fatalf("matches = %d, want the ten line-01x rows", len(value.search.matches))
	}
	if !scrollbackShows(value, "line-019") {
		t.Fatal("search did not start at the newest match")
	}
	send(&value, key('n', "n"))
	if !scrollbackShows(value, "line-018") {
		t.Fatal("n did not step towards older output")
	}
	send(&value, tea.KeyPressMsg(tea.Key{Code: 'n', ShiftedCode: 'N', Text: "N", Mod: tea.ModShift}))
	if !scrollbackShows(value, "line-019") {
		t.Fatal("N did not step back towards newer output")
	}
}

func TestDashboardSaysWhenScrollbackHasNoMatch(t *testing.T) {
	value := scrolledDashboard(t, 200)
	send(&value, tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModShift}))

	value = typeSearch(t, value, "nothing-here")
	if len(value.search.matches) != 0 {
		t.Fatalf("matches = %v, want none", value.search.matches)
	}
	if value.errorMessage == "" || !value.noticeMessage {
		t.Fatalf("status = (%q, notice %v), want a note that nothing matched",
			value.errorMessage, value.noticeMessage)
	}
	// Without matches n is not a search key, so it goes back to being what any
	// other key is in scrollback: leave, and send it to the shell.
	send(&value, key('n', "n"))
	if value.scrollback {
		t.Fatal("n with no matches did not leave scrollback")
	}
}

// Leaving scrollback forgets the search, so reopening it does not land on a
// stale match from output that has since scrolled away.
func TestDashboardForgetsTheSearchOnLeavingScrollback(t *testing.T) {
	value := scrolledDashboard(t, 200)
	send(&value, tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModShift}))
	value = typeSearch(t, value, "line-004")

	send(&value, key(tea.KeyEscape, ""))
	if value.scrollback || value.search.typing || value.search.query != "" || len(value.search.matches) != 0 {
		t.Fatalf("after leaving = (scrollback %v, mode %v, query %q, matches %v), want the search forgotten",
			value.scrollback, value.search.typing, value.search.query, value.search.matches)
	}
}

func TestDashboardScrollsHelpModalWithMouseWheel(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 100
	value.height = 20
	send(&value, key('?', "?"))

	if value.View().MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("help mouse mode = %v, want all motion", value.View().MouseMode)
	}
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "↑/↓/Wheel") {
		t.Fatalf("help status does not advertise wheel scrolling:\n%s", rendered)
	}
	send(&value, tea.MouseWheelMsg{X: 50, Y: 10, Button: tea.MouseWheelDown})
	if value.helpOffset != 3 {
		t.Fatalf("wheel down help offset = %d, want 3", value.helpOffset)
	}
	send(&value, tea.MouseWheelMsg{X: 50, Y: 10, Button: tea.MouseWheelUp})
	if value.helpOffset != 0 {
		t.Fatalf("wheel up help offset = %d, want 0", value.helpOffset)
	}

	send(&value, key(tea.KeyEscape, ""))
	if value.View().MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("mouse mode after closing help = %v, want dashboard clicks enabled", value.View().MouseMode)
	}
}
