package screen

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// Tracker follows what a full-screen program has drawn, so the screen can be
// shown to a dashboard that attaches later.
//
// A recording cannot do that once its oldest bytes are gone. A full-screen
// program draws a row once and from then on sends only the cells that change,
// so what it drew an hour ago is on the screen and nowhere in the last eight
// megabytes of output. Replaying those leaves the rows it has not touched
// since empty, and its relative cursor moves land on the wrong ones.
//
// The tracker exists only while the guest is on the alternate screen, which
// is where such programs draw. That screen starts empty, so following it from
// the switch gives its exact contents, and a tab at a shell prompt costs
// nothing: its recording replays correctly without help.
//
// A Tracker is not safe for concurrent use.
type Tracker struct {
	columns, rows int
	// scanner reads the output for the switch to the alternate screen while
	// there is no emulator to see it. entered is set by its handler.
	scanner *ansi.Parser
	entered bool
	// emulator is the alternate screen, nil while the guest is not on it,
	// and release lets go of it.
	emulator *vt.Emulator
	release  func()
	titles   TitleScrubber
}

// alternateScreen is the mode the tracker follows. It is the one the daemon
// restores to a reattaching dashboard, so a tracked screen is always one the
// dashboard is on as well.
const alternateScreen = 1049

// NewTracker returns a tracker for a terminal of the given size.
func NewTracker(columns, rows int) *Tracker {
	tracker := &Tracker{columns: columns, rows: rows}
	tracker.scanner = ansi.NewParser()
	tracker.scanner.SetHandler(ansi.Handler{HandleCsi: func(command ansi.Cmd, params ansi.Params) {
		if command.Prefix() != '?' || command.Intermediate() != 0 || command.Final() != 'h' {
			return
		}
		for _, parameter := range params {
			if parameter.Param(0) == alternateScreen {
				tracker.entered = true
			}
		}
	}})
	return tracker
}

// guarded runs one step on the emulator and turns a panic in it into an
// error, after which the tracker holds no screen. The emulator panics on some
// of what a program can print, and a tracker runs in the daemon, where a
// panic would end every tab.
func (t *Tracker) guarded(step func()) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Reset()
			err = fmt.Errorf("follow terminal output: %v", recovered)
		}
	}()
	step()
	return nil
}

// Write follows a piece of the guest's output. It returns an error when the
// emulator could not take it.
func (t *Tracker) Write(data []byte) error {
	return t.guarded(func() {
		if t.emulator == nil {
			data = t.scan(data)
			if !t.entered {
				return
			}
			t.enter()
		}
		_, _ = t.emulator.Write(t.titles.Scrub(data))
		if !t.emulator.IsAltScreen() {
			t.Reset()
		}
	})
}

// scan reads data for the switch to the alternate screen and returns what
// follows it, which is the first of what the program draws there.
func (t *Tracker) scan(data []byte) []byte {
	for index, value := range data {
		t.scanner.Advance(value)
		if t.entered {
			return data[index+1:]
		}
	}
	return nil
}

func (t *Tracker) enter() {
	t.emulator = vt.NewEmulator(t.columns, t.rows)
	// History belongs to the main screen, and the emulator keeps none of the
	// alternate one's. One line is the least it can be told to hold.
	t.emulator.SetScrollbackSize(1)
	Guard(t.emulator)
	t.release = Unanswered(t.emulator)
	_, _ = t.emulator.Write([]byte(ansi.SetModeAltScreenSaveCursor))
}

// Unanswered reads and drops what the emulator answers a program's questions
// with, for an emulator nothing is typing into: the dashboard that is
// attached answers them for real. Without a reader the emulator would wait on
// its first answer for good.
//
// The function it returns closes the emulator. The emulator is not safe to
// close while it is being read, so the reading is ended first.
func Unanswered(emulator *vt.Emulator) (release func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(io.Discard, emulator)
	}()
	return func() {
		if closer, ok := emulator.InputPipe().(io.Closer); ok {
			_ = closer.Close()
		}
		<-done
		_ = emulator.Close()
	}
}

// Reset forgets the screen. The tracker follows the next switch to the
// alternate screen from a clean start.
func (t *Tracker) Reset() {
	if t.emulator != nil {
		t.release()
		t.emulator, t.release = nil, nil
	}
	t.entered = false
	t.titles = TitleScrubber{}
	t.scanner.Reset()
}

// Resize follows the terminal to a new size. It returns an error when the
// emulator could not.
func (t *Tracker) Resize(columns, rows int) error {
	t.columns, t.rows = columns, rows
	return t.guarded(func() {
		if t.emulator != nil {
			t.emulator.Resize(columns, rows)
		}
	})
}

// Close releases the emulator.
func (t *Tracker) Close() {
	t.Reset()
}

// snapshotPrefix opens the application program command that carries a screen.
// The command is private to romty: an APC is what a terminal that does not
// know it discards whole, which is what a dashboard older than this does.
const snapshotPrefix = "romty:screen;"

// maxSnapshotBytes bounds the cells of one snapshot. A screen of a few
// hundred columns, every cell in a colour of its own, is about a megabyte. A
// program can make it far more, by putting a long hyperlink on every row, and
// then gets no snapshot: the daemon builds one for each attach, and the
// dashboard reads the command that carries it into a buffer of four megabytes.
const maxSnapshotBytes = 2 << 20

// Snapshot is the alternate screen as the sequence that draws it over
// whatever a replay left there. It is nil when the guest is not on that
// screen or the screen is more than a snapshot carries, and comes with an
// error when the emulator could not give it.
//
// The cells travel as data for Apply to place, not as output to print:
// printing them would leave the reader's pen and saved cursor where the
// snapshot put them rather than where the guest did. The cursor follows as
// an ordinary move, which changes nothing else. A move cannot say that the
// cursor is waiting past the last column to wrap, or that the guest counts
// rows from a scroll region (DECOM); a reader is off by that much until the
// program next places the cursor itself.
func (t *Tracker) Snapshot() (snapshot []byte, err error) {
	err = t.guarded(func() {
		if t.emulator == nil {
			return
		}
		cells := t.emulator.Render()
		if len(cells) > maxSnapshotBytes {
			return
		}
		// Which characters take two cells depends on this mode, so the
		// reader has to lay the cells out under the same one.
		if t.emulator.WidthMethod() == ansi.GraphemeWidth {
			cells = ansi.SetModeUnicodeCore + cells
		}
		position := t.emulator.CursorPosition()
		snapshot = fmt.Appendf(nil, "\x1b_%s%d;%d;%s\x1b\\\x1b[%d;%dH",
			snapshotPrefix, t.emulator.Width(), t.emulator.Height(),
			base64.StdEncoding.EncodeToString([]byte(cells)),
			position.Y+1, position.X+1)
	})
	return snapshot, err
}

// Apply takes the payload of an application program command and reports
// whether it is a snapshot. A snapshot is drawn onto the emulator's alternate
// screen when the emulator is on it at the size the snapshot was taken, and
// is otherwise dropped: it was a picture of some other screen.
func Apply(emulator *vt.Emulator, payload string) bool {
	body, ok := strings.CutPrefix(payload, snapshotPrefix)
	if !ok {
		return false
	}
	fields := strings.SplitN(body, ";", 3)
	if len(fields) != 3 {
		return true
	}
	columns, columnsErr := strconv.Atoi(fields[0])
	rows, rowsErr := strconv.Atoi(fields[1])
	cells, cellsErr := base64.StdEncoding.DecodeString(fields[2])
	if columnsErr != nil || rowsErr != nil || cellsErr != nil ||
		!emulator.IsAltScreen() || columns != emulator.Width() || rows != emulator.Height() {
		return true
	}
	draw(emulator, cells)
	return true
}

// draw puts a snapshot's cells on the emulator's screen.
//
// The cells are laid out by an emulator, not placed from the text: how many
// columns a character takes is the emulator's own answer, and one that
// differs by a column moves the rest of the row and leaves a cell wider than
// the screen has room for, which the emulator later indexes past. So the text
// is printed to a second emulator of the same size, and its cells are copied.
// That emulator also takes whatever else the text holds, which matters
// because a program can print a command that looks like a snapshot.
func draw(emulator *vt.Emulator, cells []byte) {
	columns, rows := emulator.Width(), emulator.Height()
	layout := vt.NewEmulator(columns, rows)
	defer Unanswered(layout)()
	// A snapshot the second emulator cannot take is dropped, as one of
	// another screen is: the replay stays what the recording made it.
	defer func() { _ = recover() }()
	Guard(layout)

	// Without autowrap a row that came out too long ends at the margin
	// instead of running into the next one.
	text := []byte(ansi.ResetModeAutoWrap)
	for row, line := range bytes.Split(cells, []byte("\n")) {
		if row == rows {
			break
		}
		text = fmt.Appendf(text, "\x1b[%d;1H\x1b[m", row+1)
		text = append(text, line...)
	}
	var titles TitleScrubber
	_, _ = layout.Write(titles.Scrub(text))

	for y := range rows {
		for x := 0; x < columns; {
			cell := layout.CellAt(x, y)
			emulator.SetCell(x, y, cell)
			// The cells a wide character covers are set with it.
			width := 1
			if cell != nil && cell.Width > 1 {
				width = cell.Width
			}
			x += width
		}
	}
}
