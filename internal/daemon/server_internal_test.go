package daemon

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newSessionForTest builds the parts of a session that do not need a PTY.
func newSessionForTest() *session {
	return &session{
		modes:   newModeTracker(),
		clients: make(map[net.Conn]*attachment),
	}
}

// A peer that connects and never finishes a request must not hold a goroutine
// and a file descriptor for the daemon's whole life, and must not keep the
// daemon from serving anyone else.
func TestHandshakeTimesOutOnASilentPeer(t *testing.T) {
	previous := requestTimeout
	requestTimeout = 150 * time.Millisecond
	t.Cleanup(func() { requestTimeout = previous })

	base, err := os.MkdirTemp("/tmp", "romty-handshake-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	socket := filepath.Join(base, "daemon.sock")
	server, err := New(socket, filepath.Join(base, "state.json"), "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() { cancel(); <-done }()

	deadline := time.Now().Add(3 * time.Second)
	var silent net.Conn
	for time.Now().Before(deadline) {
		if silent, err = net.Dial("unix", socket); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if silent == nil {
		t.Fatalf("could not reach the daemon: %v", err)
	}
	defer silent.Close()

	if err := silent.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	buffer := make([]byte, 4096)
	for {
		if _, err := silent.Read(buffer); err != nil {
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				t.Fatal("the daemon held the connection open past the handshake timeout")
			}
			break
		}
	}
}

// A client attaching must not stop the shell for everyone else. The recording
// can be megabytes and the socket slow, so none of that may happen while the
// PTY read loop's lock is held. Driven through attach so the locking decision
// itself is what is under test.
func TestAttachDoesNotStallTheSessionForOtherClients(t *testing.T) {
	slow, unread := net.Pipe()
	defer slow.Close()
	defer unread.Close()

	value := newSessionForTest()
	value.history = bytes.Repeat([]byte("history\r\n"), 4096)

	attached := make(chan error, 1)
	go func() { attached <- value.attach(slow) }()

	// The attaching client never reads, so its write blocks once the pipe
	// fills. The PTY read loop must still make progress regardless.
	broadcast := make(chan struct{})
	go func() {
		for range 200 {
			value.broadcast([]byte("live output\r\n"))
		}
		close(broadcast)
	}()
	select {
	case <-broadcast:
	case <-time.After(3 * time.Second):
		t.Fatal("broadcast blocked behind an attaching client")
	}

	unread.Close()
	select {
	case <-attached:
	case <-time.After(3 * time.Second):
		t.Fatal("attach never returned after its client went away")
	}
}

// Output that arrives while the recording is still being written has to reach
// the client after it, not spliced into the middle of it. The pipe is
// unbuffered, so the replay is genuinely mid-write when the broadcast lands.
func TestAttachHandsOffToLiveOutputInOrder(t *testing.T) {
	client, daemonSide := net.Pipe()
	defer daemonSide.Close()

	value := newSessionForTest()
	value.history = bytes.Repeat([]byte("RECORDED"), 8192)

	attached := make(chan error, 1)
	go func() { attached <- value.attach(client) }()

	// Read one chunk, so the replay is blocked partway through its recording.
	buffer := make([]byte, 512)
	daemonSide.SetReadDeadline(time.Now().Add(3 * time.Second))
	first, err := daemonSide.Read(buffer)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	seen := append([]byte(nil), buffer[:first]...)

	value.broadcast([]byte("LIVE"))

	for !bytes.Contains(seen, []byte("LIVE")) {
		daemonSide.SetReadDeadline(time.Now().Add(3 * time.Second))
		count, err := daemonSide.Read(buffer)
		seen = append(seen, buffer[:count]...)
		if err != nil {
			t.Fatalf("the client never received the live output: %v", err)
		}
	}

	live := bytes.Index(seen, []byte("LIVE"))
	recording := bytes.Count(seen[:live], []byte("RECORDED"))
	if recording != 8192 {
		t.Fatalf("live output arrived after only %d of 8192 recorded chunks; it was spliced into the recording", recording)
	}
	client.Close()
	<-attached
}

// The recording handed to the replay must be a copy: appendHistory shifts the
// buffer in place once the history is full, so an aliased slice would be
// rewritten under the replay's feet.
func TestAttachCopiesTheRecordingItReplays(t *testing.T) {
	previous := maxHistoryBytes
	maxHistoryBytes = 4096
	t.Cleanup(func() { maxHistoryBytes = previous })

	client, daemonSide := net.Pipe()
	defer daemonSide.Close()

	value := newSessionForTest()
	value.history = bytes.Repeat([]byte("O"), maxHistoryBytes)

	attached := make(chan error, 1)
	go func() { attached <- value.attach(client) }()

	// Consume the screen reset so what follows is the recording alone, then
	// block the replay partway and overwrite the whole history behind it.
	buffer := make([]byte, len(resetScreen))
	daemonSide.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := daemonSide.Read(buffer); err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	value.broadcast(bytes.Repeat([]byte("N"), maxHistoryBytes))

	buffer = make([]byte, 64)
	var seen []byte
	for len(seen) < maxHistoryBytes {
		daemonSide.SetReadDeadline(time.Now().Add(3 * time.Second))
		count, err := daemonSide.Read(buffer)
		seen = append(seen, buffer[:count]...)
		if err != nil {
			t.Fatalf("the replay stopped early after %d bytes: %v", len(seen), err)
		}
	}
	if replaced := bytes.IndexByte(seen[:maxHistoryBytes], 'N'); replaced >= 0 {
		t.Fatalf("the recording was rewritten %d bytes in, so the replay was reading the live buffer", replaced)
	}
	client.Close()
	<-attached
}

// The daemon outlives every client and fails where nobody is watching, so
// diagnosis depends entirely on what reaches daemon.log. It used to write
// nothing at all: the file existed and was always empty.
func TestDaemonRecordsItsLifecycleAndFailures(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "romty-log-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	socket := filepath.Join(base, "daemon.sock")
	server, err := New(socket, filepath.Join(base, "state.json"), "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	var recorded syncBuffer
	server.SetLogger(log.New(&recorded, "", 0))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(recorded.String(), "listening") {
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(recorded.String(), socket) {
		t.Fatalf("the daemon did not record that it started: %q", recorded.String())
	}

	cancel()
	<-done
	if !strings.Contains(recorded.String(), "stopped") {
		t.Fatalf("the daemon did not record that it stopped: %q", recorded.String())
	}
}

// A shell exiting is the one place the state file can fall behind with nothing
// to roll back to, so it has to say so.
func TestSessionExitIsRecordedWhenStateCannotBeSaved(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "romty-log-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	statePath := filepath.Join(home, "state.json")
	server, err := New(filepath.Join(home, "daemon.sock"), statePath, "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	// Read-only directory, so the temporary file the atomic save needs cannot
	// be created — the shape of a full disk or a permissions change.
	if err := os.Chmod(home, 0o500); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { os.Chmod(home, 0o700) })
	if probe, err := os.CreateTemp(home, "probe-"); err == nil {
		probe.Close()
		t.Skip("the state directory is still writable; running as root?")
	}
	var recorded syncBuffer
	server.SetLogger(log.New(&recorded, "", 0))

	server.sessionExited("tab-7")
	if !strings.Contains(recorded.String(), "tab-7") {
		t.Fatalf("the exit was not recorded: %q", recorded.String())
	}
	if !strings.Contains(recorded.String(), "persist state") {
		t.Fatalf("the failed save was not recorded: %q", recorded.String())
	}
}

// Bracketed paste is set once, early, and never again. Losing it to the
// recording's window means the shell can no longer tell pasted text from
// typed text, so a multi-line paste runs each line — data loss arriving long
// after the mode went missing.
func TestAttachRestoresModesTheRecordingNoLongerHolds(t *testing.T) {
	previous := maxHistoryBytes
	maxHistoryBytes = 2048
	t.Cleanup(func() { maxHistoryBytes = previous })

	value := newSessionForTest()
	value.broadcast([]byte("\x1b[?2004h\x1b[?1h"))
	// Enough output to push those modes out of the recording entirely.
	value.broadcast([]byte(strings.Repeat("x", maxHistoryBytes*2)))
	if strings.Contains(string(value.history), "2004h") {
		t.Fatal("the recording still holds the mode; the test proves nothing")
	}

	client, daemonSide := net.Pipe()
	defer daemonSide.Close()
	attached := make(chan error, 1)
	go func() { attached <- value.attach(client) }()

	buffer := make([]byte, 4096)
	var seen []byte
	for !bytes.Contains(seen, []byte("xxx")) {
		daemonSide.SetReadDeadline(time.Now().Add(3 * time.Second))
		count, err := daemonSide.Read(buffer)
		seen = append(seen, buffer[:count]...)
		if err != nil {
			t.Fatalf("the replay stopped early: %v", err)
		}
	}
	for _, mode := range []string{"\x1b[?1h", "\x1b[?2004h"} {
		position := bytes.Index(seen, []byte(mode))
		if position < 0 {
			t.Fatalf("the replay did not restore %q: %q", mode, seen[:64])
		}
		if position > bytes.Index(seen, []byte("xxx")) {
			t.Fatalf("%q was restored after the recording, so the recording could undo it", mode)
		}
	}
	client.Close()
	<-attached
}

// The environment must come from the request, not from the daemon's own. In
// tests both are the same process, so this passes one the daemon does not have.
func TestStartSessionUsesTheEnvironmentItIsGiven(t *testing.T) {
	if _, present := os.LookupEnv("ROMTY_ONLY_IN_THE_REQUEST"); present {
		t.Skip("the marker is already in the daemon's environment")
	}
	value, err := startSession("tab-1", t.TempDir(), "/bin/sh",
		append(os.Environ(), "ROMTY_ONLY_IN_THE_REQUEST=yes"), 80, 24, func() {})
	if err != nil {
		t.Fatalf("startSession() error = %v", err)
	}
	t.Cleanup(value.close)

	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	go value.attach(local)

	seen := make(chan string, 1)
	go func() {
		buffer := make([]byte, 4096)
		var output []byte
		for {
			remote.SetReadDeadline(time.Now().Add(3 * time.Second))
			count, err := remote.Read(buffer)
			output = append(output, buffer[:count]...)
			if err != nil || bytes.Contains(output, []byte("marker=yes")) {
				seen <- string(output)
				return
			}
		}
	}()
	if err := value.write([]byte("printf 'marker=%s\\n' \"$ROMTY_ONLY_IN_THE_REQUEST\"\n")); err != nil {
		t.Fatalf("write() error = %v", err)
	}
	if output := <-seen; !bytes.Contains([]byte(output), []byte("marker=yes")) {
		t.Fatalf("the shell did not receive the request's environment: %q", output)
	}
}

// The bind, not the probe that precedes it, is what decides who owns the
// socket. Two daemons starting at once can both find nothing to dial and race
// to create one, and the one that loses the bind has lost a race rather than
// failed: reporting it as a bind error sent the ordinary outcome of that race
// to daemon.log as a failure and exited non-zero.
func TestListenPrivatelyReportsAlreadyRunningWhenTheBindIsLost(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "romty-bind-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	socket := filepath.Join(base, "daemon.sock")

	winner, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer winner.Close()

	listener, err := listenPrivately(socket)
	if err == nil {
		listener.Close()
		t.Fatal("listenPrivately() bound a socket another listener already owns")
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("listenPrivately() error = %v, want ErrAlreadyRunning", err)
	}
}
