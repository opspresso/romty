package screen

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// reader is an emulator the way a dashboard holds one: guarded, with its
// answers read, and taking a snapshot from the command that carries it.
func reader(t *testing.T, columns, rows int) *vt.Emulator {
	t.Helper()
	emulator := vt.NewEmulator(columns, rows)
	Guard(emulator)
	emulator.RegisterApcHandler(func(data []byte) bool { return Apply(emulator, string(data)) })
	t.Cleanup(Unanswered(emulator))
	return emulator
}

func mustSnapshot(t *testing.T, tracker *Tracker) []byte {
	t.Helper()
	snapshot, err := tracker.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	return snapshot
}

func text(emulator *vt.Emulator) string {
	lines := strings.Split(ansi.Strip(emulator.Render()), "\n")
	for index := range lines {
		lines[index] = strings.TrimRight(lines[index], " ")
	}
	return strings.Join(lines, "\n")
}

func TestTrackerHoldsNoScreenAtAShellPrompt(t *testing.T) {
	tracker := NewTracker(20, 4)
	defer tracker.Close()
	if err := tracker.Write([]byte("$ ls\r\nREADME.md\r\n$ \x1b[?2004h\x1b[?25h")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if snapshot := mustSnapshot(t, tracker); snapshot != nil {
		t.Fatalf("Snapshot() = %q on the main screen, want none", snapshot)
	}
}

// The case the tracker is for. A full-screen program draws its header once
// and then only what changes, with moves relative to where it last stopped.
// A dashboard that replays only the end of that sees the changes without the
// screen they were made to.
func TestSnapshotGivesAReaderTheScreenItsReplayLost(t *testing.T) {
	const columns, rows = 30, 6
	drawn := []string{
		"\x1b[?1049h\x1b[2J\x1b[H",
		"\x1b[1mAgent v1\x1b[m\r\n~/work/romty\r\n\r\n",
		"\x1b[38;2;200;100;50m> first prompt\x1b[m",
		// Later frames: back to the prompt row, change it, add a status row.
		"\x1b[G\x1b[2K> second\x1b[B\x1b[Gstatus: working",
		"\x1b[A\x1b[G\x1b[2K> third \x1b]8;;https://romty.dev\x1b\\link\x1b]8;;\x1b\\",
	}
	tracker := NewTracker(columns, rows)
	defer tracker.Close()
	whole := reader(t, columns, rows)
	// A dashboard scrubs what it hands its emulator, which is also what puts
	// a hyperlink's fields in the order the emulator reads them.
	var scrubber TitleScrubber
	for _, chunk := range drawn {
		if err := tracker.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
		_, _ = whole.Write(scrubber.Scrub([]byte(chunk)))
	}
	want := text(whole)
	if !strings.Contains(want, "Agent v1") || !strings.Contains(want, "> third link") ||
		!strings.Contains(want, "status: working") {
		t.Fatalf("the uncut screen is not what the test means to draw:\n%s", want)
	}

	// What a client is sent: the mode, the end of the recording, the snapshot.
	late := reader(t, columns, rows)
	_, _ = late.Write([]byte("\x1b[?1049h" + drawn[len(drawn)-1]))
	if text(late) == want {
		t.Fatal("the cut replay already matches; the test proves nothing")
	}
	_, _ = late.Write(mustSnapshot(t, tracker))

	if got := text(late); got != want {
		t.Fatalf("screen after the snapshot:\n%s\nwant:\n%s", got, want)
	}
	if got, want := late.CursorPosition(), whole.CursorPosition(); got != want {
		t.Fatalf("cursor after the snapshot = %v, want %v", got, want)
	}
	// Colour and links are part of the screen.
	if got, want := late.Render(), whole.Render(); got != want {
		t.Fatalf("styled screen after the snapshot:\n%q\nwant:\n%q", got, want)
	}
}

// What the guest writes after the snapshot must land as it would have: the
// snapshot places cells and moves the cursor, and leaves the pen alone.
func TestSnapshotLeavesTheReadersPenAlone(t *testing.T) {
	const columns, rows = 20, 3
	tracker := NewTracker(columns, rows)
	defer tracker.Close()
	output := "\x1b[?1049h\x1b[Hplain \x1b[1;31m"
	if err := tracker.Write([]byte(output)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	late := reader(t, columns, rows)
	_, _ = late.Write([]byte(output))
	_, _ = late.Write(mustSnapshot(t, tracker))
	_, _ = late.Write([]byte("red"))

	whole := reader(t, columns, rows)
	_, _ = whole.Write([]byte(output + "red"))
	if got, want := late.Render(), whole.Render(); got != want {
		t.Fatalf("screen = %q, want the text after the snapshot in the guest's pen: %q", got, want)
	}
}

func TestTrackerFindsTheSwitchHoweverItArrives(t *testing.T) {
	for _, probe := range []struct {
		name   string
		chunks []string
	}{
		{name: "alone", chunks: []string{"\x1b[?1049h", "screen"}},
		{name: "after shell output", chunks: []string{"$ vim\r\n\x1b[?1049hscreen"}},
		{name: "among other modes", chunks: []string{"\x1b[?1006;1049;2004hscreen"}},
		{name: "cut by the chunk boundary", chunks: []string{"\x1b[?10", "49", "hscreen"}},
		{name: "after a title with C1 bytes inside its characters", chunks: []string{"\x1b]0;✳ 문서\a\x1b[?1049hscreen"}},
	} {
		t.Run(probe.name, func(t *testing.T) {
			tracker := NewTracker(20, 3)
			defer tracker.Close()
			for _, chunk := range probe.chunks {
				if err := tracker.Write([]byte(chunk)); err != nil {
					t.Fatalf("Write() error = %v", err)
				}
			}
			late := reader(t, 20, 3)
			_, _ = late.Write([]byte("\x1b[?1049h"))
			_, _ = late.Write(mustSnapshot(t, tracker))
			if got := text(late); !strings.HasPrefix(got, "screen") {
				t.Fatalf("snapshot drew %q, want what followed the switch", got)
			}
		})
	}
}

func TestTrackerLetsGoOfTheScreenWhenTheProgramLeaves(t *testing.T) {
	tracker := NewTracker(20, 3)
	defer tracker.Close()
	for _, chunk := range []string{"\x1b[?1049hfirst", "\x1b[?1049l$ "} {
		if err := tracker.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	if mustSnapshot(t, tracker) != nil {
		t.Fatal("the tracker still holds a screen the program left")
	}

	// The next program starts on an empty screen, not on the last one's.
	if err := tracker.Write([]byte("\x1b[?1049h\x1b[2;1Hsecond")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	late := reader(t, 20, 3)
	_, _ = late.Write([]byte("\x1b[?1049h"))
	_, _ = late.Write(mustSnapshot(t, tracker))
	if got := text(late); got != "\nsecond\n" {
		t.Fatalf("snapshot drew %q, want only the second program's screen", got)
	}
}

// A window title must not be drawn into the picture: the emulator ends a
// title at the 0x9C inside "서" and prints the rest as text.
func TestTrackerKeepsTitlesOffTheScreen(t *testing.T) {
	tracker := NewTracker(30, 3)
	defer tracker.Close()
	if err := tracker.Write([]byte("\x1b[?1049h\x1b]0;✳ 문서 개선\ascreen")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	late := reader(t, 30, 3)
	_, _ = late.Write([]byte("\x1b[?1049h"))
	_, _ = late.Write(mustSnapshot(t, tracker))
	if got := text(late); strings.Contains(got, "개선") || !strings.HasPrefix(got, "screen") {
		t.Fatalf("snapshot drew %q, want the title left out", got)
	}
}

func TestTrackerFollowsAResize(t *testing.T) {
	tracker := NewTracker(20, 3)
	defer tracker.Close()
	_ = tracker.Write([]byte("\x1b[?1049h"))
	if err := tracker.Resize(40, 5); err != nil {
		t.Fatalf("Resize() error = %v", err)
	}
	_ = tracker.Write([]byte("\x1b[5;30Hcorner"))

	late := reader(t, 40, 5)
	_, _ = late.Write([]byte("\x1b[?1049h"))
	_, _ = late.Write(mustSnapshot(t, tracker))
	if got := text(late); !strings.HasSuffix(got, strings.Repeat(" ", 29)+"corner") {
		t.Fatalf("snapshot drew %q, want the new size", got)
	}
}

// A snapshot is a picture of one screen. A reader somewhere else — on its main
// screen, or at another size — must not have it drawn over what it shows.
func TestApplyDropsASnapshotOfAnotherScreen(t *testing.T) {
	tracker := NewTracker(20, 3)
	defer tracker.Close()
	_ = tracker.Write([]byte("\x1b[?1049hpicture"))
	snapshot := mustSnapshot(t, tracker)

	onMain := reader(t, 20, 3)
	_, _ = onMain.Write([]byte("shell"))
	_, _ = onMain.Write(snapshot[:strings.Index(string(snapshot), "\x1b\\")+2])
	if got := text(onMain); !strings.HasPrefix(got, "shell") || strings.Contains(got, "picture") {
		t.Fatalf("main screen = %q, want it left alone", got)
	}

	otherSize := reader(t, 30, 4)
	_, _ = otherSize.Write([]byte("\x1b[?1049hmine"))
	_, _ = otherSize.Write(snapshot)
	if got := text(otherSize); strings.Contains(got, "picture") {
		t.Fatalf("screen of another size = %q, want the snapshot dropped", got)
	}
}

// A dashboard older than the snapshot has no handler for it. It must discard
// the command whole, so the replay it shows is the one it always showed, with
// the cursor where the guest has it.
func TestSnapshotIsNothingToAReaderThatDoesNotKnowIt(t *testing.T) {
	tracker := NewTracker(20, 3)
	defer tracker.Close()
	_ = tracker.Write([]byte("\x1b[?1049hpicture\x1b[2;5H"))

	older := vt.NewEmulator(20, 3)
	t.Cleanup(Unanswered(older))
	_, _ = older.Write([]byte("\x1b[?1049hreplayed"))
	_, _ = older.Write(mustSnapshot(t, tracker))
	if got := text(older); got != "replayed\n\n" {
		t.Fatalf("screen = %q, want the replay alone", got)
	}
	if position := older.CursorPosition(); position.X != 4 || position.Y != 1 {
		t.Fatalf("cursor = %v, want the guest's (4,1)", position)
	}
}

func TestApplyTakesOnlySnapshots(t *testing.T) {
	emulator := reader(t, 20, 3)
	if Apply(emulator, "romty:keyboard;;;") || Apply(emulator, "Gf=100") {
		t.Fatal("Apply took a command that is not a snapshot")
	}
	for _, malformed := range []string{"romty:screen;", "romty:screen;20;3", "romty:screen;x;3;", "romty:screen;20;3;***"} {
		if !Apply(emulator, malformed) {
			t.Fatalf("Apply(%q) left a malformed snapshot for another handler", malformed)
		}
	}
}

// One short sequence asks for the last character a billion times. The
// emulator prints each one, which is minutes of reading nothing else.
func TestGuardBoundsRepeatCounts(t *testing.T) {
	emulator := reader(t, 20, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, sequence := range []string{"a\x1b[999999999b", "\x1b[999999999I", "\x1b[999999999X", "\x1b[999999999Z"} {
			_, _ = emulator.Write([]byte(sequence))
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the emulator is still counting")
	}
	if got := text(emulator); !strings.HasPrefix(got, strings.Repeat("a", 20)) {
		t.Fatalf("screen = %q, want the repeat to fill what can be seen", got)
	}
}

// A program can make its screen as large as it likes in bytes, by putting a
// long hyperlink on every row. Such a screen is left to the recording.
func TestSnapshotOfAScreenTooLargeToCarryIsNone(t *testing.T) {
	tracker := NewTracker(200, 50)
	defer tracker.Close()
	var output strings.Builder
	output.WriteString("\x1b[?1049h\x1b]8;;https://example.com/" + strings.Repeat("a", 100_000) + "\x1b\\")
	for row := 1; row <= 50; row++ {
		output.WriteString("\x1b[" + strconv.Itoa(row) + ";1H" + strings.Repeat("x", 200))
	}
	if err := tracker.Write([]byte(output.String())); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if snapshot := mustSnapshot(t, tracker); snapshot != nil {
		t.Fatalf("snapshot of %d bytes, want none", len(snapshot))
	}
}

// What a snapshot carries is drawn, not obeyed: a program that printed a
// command looking like a snapshot gets it shown as nothing at all.
func TestSnapshotDrawsNoSnapshotItHolds(t *testing.T) {
	tracker := NewTracker(20, 3)
	defer tracker.Close()
	forged := "\x1b_romty:screen;20;3;" + base64.StdEncoding.EncodeToString([]byte("forged")) + "\x1b\\"
	_ = tracker.Write([]byte("\x1b[?1049hmine" + forged))

	emulator := reader(t, 20, 3)
	_, _ = emulator.Write([]byte("\x1b[?1049h"))
	_, _ = emulator.Write(mustSnapshot(t, tracker))
	if got := text(emulator); got != "mine\n\n" {
		t.Fatalf("screen = %q, want the program's own", got)
	}
}
