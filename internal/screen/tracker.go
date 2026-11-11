package screen

import (
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
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

// Write follows a piece of the guest's output. It returns an error when the
// emulator could not take it, after which the tracker holds no screen.
func (t *Tracker) Write(data []byte) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Reset()
			err = fmt.Errorf("follow terminal output: %v", recovered)
		}
	}()
	if t.emulator == nil {
		data = t.scan(data)
		if !t.entered {
			return nil
		}
		t.enter()
	}
	_, _ = t.emulator.Write(t.titles.Scrub(data))
	if !t.emulator.IsAltScreen() {
		t.Reset()
	}
	return nil
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

// Resize follows the terminal to a new size.
func (t *Tracker) Resize(columns, rows int) {
	t.columns, t.rows = columns, rows
	if t.emulator != nil {
		t.emulator.Resize(columns, rows)
	}
}

// Close releases the emulator.
func (t *Tracker) Close() {
	t.Reset()
}

// snapshotPrefix opens the application program command that carries a screen.
// The command is private to romty: an APC is what a terminal that does not
// know it discards whole, which is what a dashboard older than this does.
const snapshotPrefix = "romty:screen;"

// Snapshot is the alternate screen as the sequence that draws it over
// whatever a replay left there, or nil when the guest is not on it.
//
// The cells travel as data for Apply to place, not as output to print:
// printing them would leave the reader's pen and saved cursor where the
// snapshot put them rather than where the guest did. The cursor follows as
// an ordinary move, which changes nothing else.
func (t *Tracker) Snapshot() []byte {
	if t.emulator == nil {
		return nil
	}
	position := t.emulator.CursorPosition()
	return fmt.Appendf(nil, "\x1b_%s%d;%d;%s\x1b\\\x1b[%d;%dH",
		snapshotPrefix, t.emulator.Width(), t.emulator.Height(),
		base64.StdEncoding.EncodeToString([]byte(t.emulator.Render())),
		position.Y+1, position.X+1)
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
	uv.NewStyledString(string(cells)).Draw(emulator, uv.Rect(0, 0, columns, rows))
	return true
}
