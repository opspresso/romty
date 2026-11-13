package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/opspresso/romty/internal/screen"
)

// A snapshot holds characters whose width depends on who measures it: an
// emoji with a variation selector, a family joined by zero-width joiners. The
// screen it draws must take each the columns the emulator gives it, or the
// rest of the row moves and a cell is left wider than the room it has, which
// the emulator indexes past the next time the pane shrinks.
func TestEmbeddedTerminalTakesASnapshotOfWideCharacters(t *testing.T) {
	const columns, rows = 120, 10
	for _, wide := range []string{"⚠️", "✔️", "👨‍👩‍👧", "한글", "🇰🇷"} {
		t.Run(wide, func(t *testing.T) {
			output := "\x1b[?1049h\x1b[2J\x1b[1;1H" + wide + "|end\x1b[2;74H" + wide + "x"
			tracker := screen.NewTracker(columns, rows)
			defer tracker.Close()
			if err := tracker.Write([]byte(output)); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			snapshot, err := tracker.Snapshot()
			if err != nil || snapshot == nil {
				t.Fatalf("Snapshot() = %q, %v", snapshot, err)
			}

			// What the guest's own output makes of the same rows.
			reference := newEmbeddedTerminal("reference", newMemoryStream(""), columns, rows)
			defer reference.close()
			reference.writeOutput([]byte(output))

			// The replay holds only the switch; the snapshot draws the rest.
			replay := append([]byte("\x1b[?1049h"), snapshot...)
			terminal := newEmbeddedTerminalWithReplay("tab-1", replaySizedMemoryStream{newMemoryStream(""), columns, rows}, replay, 80, 24)
			defer terminal.close()
			for _, row := range []int{0, 1} {
				if got, want := cellWidths(terminal, row), cellWidths(reference, row); got != want {
					t.Fatalf("row %d after the snapshot has cells %s, want the guest's %s", row, got, want)
				}
			}

			terminal.resize(76, rows)
			terminal.writeOutput([]byte("\x1b[2;76Hx"))
		})
	}
}

// cellWidths lists where each character of a row starts and how many columns
// it takes.
func cellWidths(terminal *embeddedTerminal, row int) string {
	var cells strings.Builder
	for x := 0; x < terminal.emulator.Width(); x++ {
		cell := terminal.emulator.CellAt(x, row)
		if cell == nil || cell.Width == 0 || cell.Content == " " || cell.Content == "" {
			continue
		}
		fmt.Fprintf(&cells, "%d:%s/%d ", x, cell.Content, cell.Width)
	}
	return cells.String()
}
