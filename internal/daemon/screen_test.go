package daemon

import (
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"github.com/opspresso/romty/internal/screen"
	"github.com/opspresso/romty/internal/testutil"
)

const followedColumns, followedRows = 30, 5

// followedSession is a session whose screen is followed, the way a started
// one is, without a shell behind it.
func followedSession(t *testing.T) *session {
	t.Helper()
	value := newSessionForTest(nil)
	value.columns, value.rows = followedColumns, followedRows
	value.readDone = make(chan struct{})
	value.screen.wake = make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		value.followScreen(followedColumns, followedRows)
	}()
	t.Cleanup(func() { close(value.readDone); <-done })
	return value
}

// printOutput hands the session output the way the PTY does, a read at a time, and
// lets the follower take each one. A follower keeps pace with a real program
// because the recording holds megabytes; the tests shrink it to bytes.
func printOutput(t *testing.T, value *session, output string) {
	t.Helper()
	for len(output) > 0 {
		piece := min(len(output), 64)
		value.broadcast([]byte(output[:piece]))
		output = output[piece:]
		deadline := time.Now().Add(3 * time.Second)
		for {
			value.mu.Lock()
			read := value.screen.position == value.history.total
			value.mu.Unlock()
			if read {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the follower stopped reading")
			}
			time.Sleep(time.Millisecond)
		}
	}
}

// replayedScreen attaches a client and returns its screen once it has taken
// the replay, as the text of each row.
func replayedScreen(t *testing.T, value *session) string {
	t.Helper()
	client, daemonSide := net.Pipe()
	defer daemonSide.Close()

	announced := make(chan int, 1)
	attached := make(chan error, 1)
	go func() {
		attached <- value.attachReady(client, func(replayBytes int) error {
			announced <- replayBytes
			return nil
		})
	}()
	replay := make([]byte, <-announced)
	daemonSide.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(daemonSide, replay); err != nil {
		t.Fatalf("read replay: %v", err)
	}
	daemonSide.Close()
	<-attached

	emulator := vt.NewEmulator(followedColumns, followedRows)
	screen.Guard(emulator)
	emulator.RegisterApcHandler(func(data []byte) bool { return screen.Apply(emulator, string(data)) })
	defer screen.Unanswered(emulator)()
	_, _ = emulator.Write(replay)
	lines := strings.Split(ansi.Strip(emulator.Render()), "\n")
	for index := range lines {
		lines[index] = strings.TrimRight(lines[index], " ")
	}
	return strings.Join(lines, "\n")
}

// frames is what a full-screen program prints after its first screen: the
// same row redrawn over and over, far more of it than the recording keeps.
func frames(count int) string {
	var output strings.Builder
	for index := range count {
		output.WriteString("\x1b[3;1H\x1b[2K")
		output.WriteString("working " + strings.Repeat(".", index%8))
	}
	output.WriteString("\x1b[3;1H\x1b[2Kworking done")
	return output.String()
}

// The header was drawn once, long before the recording's window, and the
// program has only redrawn one row since. The client must still see it.
func TestReattachShowsWhatAFullScreenProgramDrewBeforeTheRecording(t *testing.T) {
	testutil.Set(t, &maxHistoryBytes, 512)
	value := followedSession(t)
	printOutput(t, value, "$ agent\r\n\x1b[?1049h\x1b[2J\x1b[HAgent v1\r\n~/work/romty")
	printOutput(t, value, frames(200))

	if strings.Contains(string(value.snapshotRecording()), "Agent v1") {
		t.Fatal("the recording still holds the header; the test proves nothing")
	}
	want := "Agent v1\n~/work/romty\nworking done\n\n"
	if got := replayedScreen(t, value); got != want {
		t.Fatalf("screen after reattach:\n%q\nwant:\n%q", got, want)
	}
}

// Without the follower the same recording leaves the rows it no longer holds
// empty, which is what a reattach looked like.
func TestReattachWithoutTheFollowerLosesThoseRows(t *testing.T) {
	testutil.Set(t, &maxHistoryBytes, 512)
	value := newSessionForTest(nil)
	value.columns, value.rows = followedColumns, followedRows
	for _, output := range []string{"\x1b[?1049h\x1b[2J\x1b[HAgent v1\r\n~/work/romty", frames(200)} {
		value.guest.observe([]byte(output))
		value.history.append([]byte(output))
	}
	if got := replayedScreen(t, value); strings.Contains(got, "Agent v1") {
		t.Fatalf("screen = %q; the recording alone was enough and the follower is not needed", got)
	}
}

// A shell's recording replays correctly by itself, and its replay is the
// bytes it always was.
func TestReattachToAShellSendsNoScreen(t *testing.T) {
	value := followedSession(t)
	value.broadcast([]byte("$ ls\r\nREADME.md\r\n$ "))

	client, daemonSide := net.Pipe()
	defer daemonSide.Close()
	attached := make(chan error, 1)
	go func() { attached <- value.attach(client) }()
	daemonSide.SetReadDeadline(time.Now().Add(3 * time.Second))
	readReplay(t, daemonSide, []byte(resetScreen+"$ ls\r\nREADME.md\r\n$ "+noKeyboardRequests))
	daemonSide.Close()
	<-attached
}

// A program that left the alternate screen has no screen to restore: what
// the client needs is the shell's, which the recording has.
func TestReattachAfterTheProgramLeftSendsNoScreen(t *testing.T) {
	value := followedSession(t)
	value.broadcast([]byte("\x1b[?1049hfull screen\x1b[?1049l$ "))
	if got := replayedScreen(t, value); strings.Contains(got, "full screen") || !strings.HasPrefix(got, "$") {
		t.Fatalf("screen = %q, want the shell's", got)
	}
}

// The picture is taken at the point the recording was copied. Output that
// arrives while the client is still being served follows as live output and
// must not already be in the picture, or it would be drawn twice.
func TestScreenIsTakenWhereTheRecordingEnds(t *testing.T) {
	value := followedSession(t)
	value.broadcast([]byte("\x1b[?1049h\x1b[Hbefore"))

	value.mu.Lock()
	request := value.requestScreenLocked()
	value.mu.Unlock()
	value.broadcast([]byte("\x1b[2;1Hafter"))

	select {
	case picture := <-request:
		reader := vt.NewEmulator(followedColumns, followedRows)
		defer screen.Unanswered(reader)()
		reader.RegisterApcHandler(func(data []byte) bool { return screen.Apply(reader, string(data)) })
		_, _ = reader.Write([]byte("\x1b[?1049h"))
		_, _ = reader.Write(picture)
		if got := ansi.Strip(reader.Render()); !strings.Contains(got, "before") || strings.Contains(got, "after") {
			t.Fatalf("picture = %q, want the screen as it was when asked for", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the request was never answered")
	}
}

// The follower takes a new size when it reaches the output printed after it,
// not before: what was drawn for the old size is laid out by the old size.
func TestFollowerResizesInStepWithTheOutput(t *testing.T) {
	value := followedSession(t)
	value.broadcast([]byte("\x1b[?1049h\x1b[H"))

	value.mu.Lock()
	value.columns, value.rows = 60, 8
	value.resizeScreenLocked(60, 8)
	value.mu.Unlock()
	value.broadcast([]byte("\x1b[8;50Hcorner"))

	value.mu.Lock()
	request := value.requestScreenLocked()
	value.mu.Unlock()
	value.wakeScreen()
	select {
	case picture := <-request:
		if !strings.HasPrefix(string(picture), "\x1b_romty:screen;60;8;") || !strings.HasSuffix(string(picture), "\x1b[8;56H") {
			t.Fatalf("picture = %.40q…%q, want the new size and the cursor after the text", picture, picture[max(len(picture)-10, 0):])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the request was never answered")
	}
}

// Output the recording dropped before the follower read it leaves a hole in
// the picture. Nobody is given a picture with a hole in it; the follower
// starts again at the next switch to the alternate screen.
func TestFollowerThatFellBehindSendsNoScreenUntilItCanTrustOne(t *testing.T) {
	testutil.Set(t, &maxHistoryBytes, 256)
	value := newSessionForTest(nil)
	value.columns, value.rows = followedColumns, followedRows
	value.readDone = make(chan struct{})
	value.screen.wake = make(chan struct{}, 1)

	// The follower is not running yet, so all of this passes it by.
	value.broadcast([]byte("\x1b[?1049h\x1b[HAgent v1"))
	value.broadcast([]byte(frames(100)))
	value.mu.Lock()
	request := value.requestScreenLocked()
	value.mu.Unlock()

	done := make(chan struct{})
	go func() { defer close(done); value.followScreen(followedColumns, followedRows) }()
	t.Cleanup(func() { close(value.readDone); <-done })
	value.wakeScreen()
	select {
	case picture := <-request:
		if picture != nil {
			t.Fatalf("picture = %.60q, want none from a follower that missed the screen being drawn", picture)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the request was never answered")
	}

	printOutput(t, value, "\x1b[?1049l$ vim\r\n\x1b[?1049h\x1b[Hsecond program")
	if got := replayedScreen(t, value); !strings.HasPrefix(got, "second program") {
		t.Fatalf("screen = %q, want the program the follower saw start", got)
	}
}

// A session restored after a stop begins with what a dead program printed.
// The switch to the alternate screen in it is not the new shell's.
func TestFollowerSkipsARestoredRecording(t *testing.T) {
	value := followedSession(t)
	value.restore([]byte("\x1b[?1049hthe agent that was stopped"), "\r\n[restored]\r\n", "")
	value.broadcast([]byte("$ "))

	value.mu.Lock()
	position, total := value.screen.position, value.history.total
	value.mu.Unlock()
	if position > total {
		t.Fatalf("follower position %d is past the recording's %d", position, total)
	}
	// The guest is at a shell, so there is no request to make; a tracker
	// that had followed the restored bytes would be holding a screen.
	value.mu.Lock()
	value.guest.set[1049] = true
	request := value.requestScreenLocked()
	value.mu.Unlock()
	value.wakeScreen()
	select {
	case picture := <-request:
		if picture != nil {
			t.Fatalf("picture = %.60q, want none: the dead program's screen is not this shell's", picture)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the request was never answered")
	}
}

// An attach must not hang on a follower that has stopped answering.
func TestAttachDoesNotWaitForAFollowerThatIsStuck(t *testing.T) {
	testutil.Set(t, &screenWait, 20*time.Millisecond)
	value := newSessionForTest(nil)
	value.columns, value.rows = followedColumns, followedRows
	// Followed in name only: nothing reads the wake channel.
	value.screen.wake = make(chan struct{}, 1)
	for _, output := range []string{"\x1b[?1049h\x1b[Hscreen"} {
		value.guest.observe([]byte(output))
		value.history.append([]byte(output))
	}
	if got := replayedScreen(t, value); !strings.HasPrefix(got, "screen") {
		t.Fatalf("screen = %q, want the recording replayed without the picture", got)
	}
}

// A started session is followed without anyone asking: the shell's own
// output, read from its PTY, is what a reattaching client's screen is made of.
func TestStartedSessionIsFollowed(t *testing.T) {
	testutil.Set(t, &maxHistoryBytes, 32<<10)
	value, err := startSession("tab-1", t.TempDir(), "/bin/sh", os.Environ(), followedColumns, followedRows, func() {})
	if err != nil {
		t.Fatalf("startSession() error = %v", err)
	}
	t.Cleanup(value.close)

	// The header is drawn once, then one row is redrawn until the recording
	// has long forgotten the header. The words the test looks for are put
	// together by the shell, so the echo of the command does not hold them.
	// The pauses are a program's own pace: output that arrived faster than
	// the follower reads a recording this small would be dropped unread.
	script := `printf '\033[?1049h\033[2J\033[HAgent v%s' 1; n=0; while [ $n -lt 40 ]; do i=0; ` +
		`while [ $i -lt 80 ]; do printf '\033[3;1H\033[2Kworking %s' $i; i=$((i+1)); done; ` +
		`sleep 0.02; n=$((n+1)); done; printf '\033[3;1H\033[2Kworking %s' done` + "\n"
	if err := value.write([]byte(script)); err != nil {
		t.Fatalf("write() error = %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(string(value.snapshotRecording()), "working done") {
		if time.Now().After(deadline) {
			t.Fatal("the shell never finished drawing")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Contains(string(value.snapshotRecording()), "Agent v1") {
		t.Fatal("the recording still holds the header; the test proves nothing")
	}
	if got := replayedScreen(t, value); !strings.HasPrefix(got, "Agent v1\n\nworking done") {
		t.Fatalf("screen after reattach = %q, want the header the recording lost", got)
	}
}

// Once the session's output has ended nothing answers a request, so none is
// made: an attach in the moment before the session closes does not wait.
func TestFollowerStopsWithTheOutput(t *testing.T) {
	value := newSessionForTest(nil)
	value.columns, value.rows = followedColumns, followedRows
	value.readDone = make(chan struct{})
	value.screen.wake = make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		value.followScreen(followedColumns, followedRows)
	}()
	value.broadcast([]byte("\x1b[?1049h\x1b[Hscreen"))
	close(value.readDone)
	<-done

	value.mu.Lock()
	request := value.requestScreenLocked()
	value.mu.Unlock()
	if request != nil {
		t.Fatal("a request was made of a follower that has stopped")
	}
	if fault := value.screenFault(); fault != nil {
		t.Fatalf("screenFault() = %v, want none: the output ending is not a failure", fault)
	}
}
