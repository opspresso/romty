package ui

import (
	"image/color"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Codex and Claude Code ask for focus events and stay quiet while someone is
// looking. Told nothing, they take the tab for watched however long the user
// has been somewhere else.
func TestDashboardTellsTheGuestWhenKeysStopReachingIt(t *testing.T) {
	// With history, so scrollback has something to open on.
	value := scrolledDashboard(t, 200)
	value.terminal.writeOutput([]byte("\x1b[?1004h"))

	// Any message settles the focus the dashboard starts with.
	send(&value, tea.FocusMsg{})
	sent := waitForGuestSilence(t, value.terminal, "\x1b[I")

	for _, step := range []struct {
		name    string
		message tea.Msg
		report  string
	}{
		{name: "the host window loses the focus", message: tea.BlurMsg{}, report: "\x1b[O"},
		{name: "the host window gets it back", message: tea.FocusMsg{}, report: "\x1b[I"},
		{name: "a second report of the same focus", message: tea.FocusMsg{}, report: ""},
		{name: "Help opens over the terminal", message: key(tea.KeyF1, ""), report: "\x1b[O"},
		{name: "Help closes", message: key(tea.KeyEscape, ""), report: "\x1b[I"},
		{name: "the workspace pane takes the keyboard", message: key(tea.KeyF7, ""), report: "\x1b[O"},
		{name: "the terminal pane takes it back", message: key(tea.KeyF7, ""), report: "\x1b[I"},
		{name: "scrollback opens", message: key(tea.KeyF6, ""), report: "\x1b[O"},
	} {
		send(&value, step.message)
		sent = waitForGuestSilence(t, value.terminal, sent+step.report)
	}
}

// A guest that never asked for focus events is sent none.
func TestDashboardSendsNoFocusReportsUnasked(t *testing.T) {
	value := terminalDashboard(t)
	send(&value, tea.BlurMsg{})
	send(&value, tea.FocusMsg{})
	waitForGuestSilence(t, value.terminal, "")
}

// Leaving a tab is the guest losing the keyboard. The report is written
// before the stream closes, not raced against it.
func TestClosingATerminalTellsTheGuestItLostTheFocus(t *testing.T) {
	stream := newMemoryStream("")
	terminal := newEmbeddedTerminal("tab-1", stream, 40, 10)
	terminal.writeOutput([]byte("\x1b[?1004h"))
	terminal.setFocus(true)
	terminal.sendKey(key('x', "x"))

	terminal.close()
	if got := stream.String(); got != "\x1b[Ix\x1b[O" {
		t.Fatalf("guest received %q, want the focus, the key and the blur in order", got)
	}
	if !stream.isClosed() {
		t.Fatal("the stream was left open")
	}
}

// A program picks its palette from the answer to a colour query. The answer
// is the terminal the user is looking at, not the emulator's white on black.
func TestGuestColourQueriesGetTheHostsColours(t *testing.T) {
	value := terminalDashboard(t)
	send(&value, tea.BackgroundColorMsg{Color: color.RGBA{R: 0xfa, G: 0xf4, B: 0xed, A: 0xff}})
	send(&value, tea.ForegroundColorMsg{Color: color.RGBA{R: 0x57, G: 0x52, B: 0x79, A: 0xff}})

	value.terminal.writeOutput([]byte("\x1b]11;?\x07\x1b]10;?\x07"))
	waitForGuestSilence(t, value.terminal, "\x1b]11;rgb:fafa/f4f4/eded\x07\x1b]10;rgb:5757/5252/7979\x07")
}

// The colours were reported before this terminal existed.
func TestATerminalOpenedLaterKnowsTheHostsColours(t *testing.T) {
	tabs := openTabs(1)
	snapshot, workspace := alphaWorkspaceSnapshot(tabs)
	value := newDashboard(&fakeBackend{}, snapshot)
	value.width, value.height = 120, 30
	value.selectedWorkspaceID, value.selectedPath = workspace.ID, workspace.Path
	send(&value, tea.BackgroundColorMsg{Color: color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}})

	send(&value, terminalOpenedMsg{tabID: tabs[0].ID, stream: newMemoryStream("")})
	t.Cleanup(value.closeTerminal)
	if value.terminal == nil {
		t.Fatal("the terminal did not open")
	}

	value.terminal.writeOutput([]byte("\x1b]11;?\x07"))
	waitForGuestSilence(t, value.terminal, "\x1b]11;rgb:ffff/ffff/ffff\x07")
}
