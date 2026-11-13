package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/testutil"
)

func screenText(terminal *embeddedTerminal) string {
	return ansi.Strip(strings.Join(terminal.render(), "\n"))
}

// A program that draws its own cursor, or is redrawing, hides the terminal's.
// Showing it anyway puts a block wherever the last write happened to stop.
func TestDashboardShowsTheCursorTheGuestSet(t *testing.T) {
	value := terminalDashboard(t)
	if cursor := value.View().Cursor; cursor == nil || cursor.Shape != tea.CursorBlock || !cursor.Blink {
		t.Fatalf("cursor = %#v, want the blinking block a terminal starts with", cursor)
	}

	value.terminal.writeOutput([]byte("\x1b[?25l"))
	if cursor := value.View().Cursor; cursor != nil {
		t.Fatalf("cursor = %#v after the guest hid it, want none", cursor)
	}

	value.terminal.writeOutput([]byte("\x1b[?25h\x1b[6 q\x1b[3;5H"))
	cursor := value.View().Cursor
	if cursor == nil || cursor.Shape != tea.CursorBar || cursor.Blink {
		t.Fatalf("cursor = %#v, want the steady bar the guest asked for", cursor)
	}
	value.terminal.writeOutput([]byte("\x1b[3 q"))
	if blinking := value.View().Cursor; blinking == nil || blinking.Shape != tea.CursorUnderline || !blinking.Blink {
		t.Fatalf("cursor = %#v, want the blinking underline the guest asked for", blinking)
	}
	originX, originY := value.dimensions().terminalOrigin()
	if cursor.X != originX+4 || cursor.Y != originY+2 {
		t.Fatalf("cursor at (%d,%d), want the guest's position inside the pane", cursor.X, cursor.Y)
	}
}

// Codex wraps every frame in a synchronized update and the bytes of one frame
// arrive over several reads. Painting between two of them shows half of each.
func TestEmbeddedTerminalHoldsTheFrameDuringASynchronizedUpdate(t *testing.T) {
	terminal := newEmbeddedTerminal("tab-1", newMemoryStream(""), 20, 4)
	defer terminal.close()

	terminal.writeOutput([]byte("\x1b[?25l\x1b[2J\x1b[Hfirst frame"))
	terminal.writeOutput([]byte("\x1b[?2026h\x1b[2J\x1b[Hsecond"))
	if got := screenText(terminal); !strings.Contains(got, "first frame") || strings.Contains(got, "second") {
		t.Fatalf("screen during the update = %q, want the frame before it", got)
	}
	if terminal.cursor() != nil {
		t.Fatal("the cursor hidden before the update is shown during it")
	}

	// The cursor the guest shows inside the update appears with the frame.
	terminal.writeOutput([]byte(" frame\x1b[?25h"))
	if terminal.cursor() != nil {
		t.Fatal("the cursor moved before the update ended")
	}
	terminal.writeOutput([]byte("\x1b[?2026l"))
	if got := screenText(terminal); !strings.Contains(got, "second frame") {
		t.Fatalf("screen after the update = %q, want the new frame", got)
	}
	if cursor := terminal.cursor(); cursor == nil || cursor.X != len("second frame") {
		t.Fatalf("cursor after the update = %#v, want it where the new frame left it", cursor)
	}
}

// A program that dies between the two halves of the mode must not freeze its
// tab on the frame before.
func TestEmbeddedTerminalReleasesAnUpdateThatNeverEnds(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	testutil.Set(t, &now, func() time.Time { return current })
	terminal := newEmbeddedTerminal("tab-1", newMemoryStream(""), 20, 4)
	defer terminal.close()

	terminal.writeOutput([]byte("before\x1b[?2026h\x1b[2J\x1b[Hafter"))
	if got := screenText(terminal); !strings.Contains(got, "before") {
		t.Fatalf("screen = %q, want the update held", got)
	}
	current = current.Add(frameHoldLimit)
	if got := screenText(terminal); !strings.Contains(got, "after") || strings.Contains(got, "before") {
		t.Fatalf("screen = %q once the hold ran out, want what the guest drew", got)
	}
}

// The dashboard only draws after a message, so a hold that outlives the
// guest's output needs a timer to end it.
func TestDashboardSetsOneTimerToEndAHeldFrame(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	testutil.Set(t, &now, func() time.Time { return current })
	value := terminalDashboard(t)
	terminal := value.terminal

	terminal.writeOutput([]byte("\x1b[?2026h"))
	if value.frameRelease() == nil {
		t.Fatal("no timer was set for a held frame")
	}
	if value.frameRelease() != nil {
		t.Fatal("a second timer was set for the same hold")
	}

	// The guest began another update since: the timer is set again.
	current = current.Add(frameHoldLimit / 2)
	updated, command := value.Update(frameReleaseMsg{terminal: terminal})
	if command == nil || terminal.held == nil {
		t.Fatalf("early timer = (command %v, held %v), want the hold kept and timed again", command, terminal.held)
	}

	current = current.Add(frameHoldLimit)
	if _, command = updated.Update(frameReleaseMsg{terminal: terminal}); command != nil {
		t.Fatal("the timer was set again after the hold ran out")
	}
	if terminal.held != nil {
		t.Fatal("the hold outlived its limit")
	}
}

// A program asks whether the terminal knows the mode before it relies on it.
func TestEmbeddedTerminalSaysItSupportsSynchronizedOutput(t *testing.T) {
	terminal := newEmbeddedTerminal("tab-1", newMemoryStream(""), 20, 4)
	defer terminal.close()

	terminal.writeOutput([]byte("\x1b[?2026$p"))
	waitForGuestSilence(t, terminal, "\x1b[?2026;2$y")
}

// A replay is drawn off screen in one piece, and the recording may end inside
// an update whose end is still on its way.
func TestEmbeddedTerminalDoesNotHoldAReplay(t *testing.T) {
	replay := []byte("old\x1b[?2026h\x1b[2J\x1b[Hnew")
	terminal := newEmbeddedTerminalWithReplay("tab-1", newMemoryStream(""), replay, 20, 4)
	defer terminal.close()

	if got := screenText(terminal); !strings.Contains(got, "new") {
		t.Fatalf("screen after the replay = %q, want what the recording drew", got)
	}
}

// A held frame was drawn for the size the pane no longer has.
func TestEmbeddedTerminalDropsAHeldFrameOnResize(t *testing.T) {
	terminal := newEmbeddedTerminal("tab-1", newMemoryStream(""), 20, 4)
	defer terminal.close()

	terminal.writeOutput([]byte("before\x1b[?2026h\x1b[2J\x1b[Hafter"))
	terminal.resize(30, 6)
	if lines := terminal.render(); len(lines) != 6 || !strings.Contains(ansi.Strip(lines[0]), "after") {
		t.Fatalf("screen after resize = %q, want the live screen at the new size", lines)
	}
}
