package daemon

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/opspresso/romty/internal/testutil"
)

func TestBroadcastDetachesSlowClientWhileInputIsBlocked(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	connection, peer := net.Pipe()
	defer peer.Close()
	value := newSessionForTest(writer)
	value.clients[connection] = &attachment{live: true, output: make(chan []byte), done: make(chan struct{})}
	written := make(chan error, 1)
	go func() { written <- value.write(bytes.Repeat([]byte("x"), 1<<20)) }()
	t.Cleanup(func() { reader.Close(); writer.Close(); <-written })
	deadline := time.Now().Add(time.Second)
	for value.writeMu.TryLock() {
		value.writeMu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("input writer never started")
		}
		time.Sleep(time.Millisecond)
	}
	finished := make(chan struct{})
	go func() { value.broadcast([]byte("output")); close(finished) }()
	select {
	case <-finished:
	case <-time.After(200 * time.Millisecond):
		reader.Close()
		<-finished
		t.Fatal("output drain waited for blocked terminal input")
	}
	if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("slow client was not detached: %v", err)
	}
}

func TestSessionBoundsInputToAnUnresponsivePTY(t *testing.T) {
	value, err := startSession("blocked-input", t.TempDir(), "/bin/sh", os.Environ(), 80, 24, func() {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(value.close)
	// A PTY must support interruption before any potentially blocking input
	// is sent. Check again after resizing and foreground detection.
	if err := value.pty.SetWriteDeadline(time.Time{}); err != nil {
		t.Fatalf("PTY does not support write interruption: %v", err)
	}
	if err := value.resizeFor("", 90, 30); err != nil {
		t.Fatal(err)
	}
	if _, err := value.foregroundProcessGroup(); err != nil {
		t.Fatal(err)
	}
	if err := value.write([]byte("stty raw -echo; printf 'no-%s' input; exec sleep 30\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		output, _, _ := value.recentOutput(4096)
		if bytes.Contains(output, []byte("no-input")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shell did not stop reading input")
		}
		time.Sleep(time.Millisecond)
	}
	testutil.Set(t, &terminalInputTimeout, 100*time.Millisecond)
	if err := value.write(bytes.Repeat([]byte("x"), 1<<20)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("write to unresponsive PTY = %v, want timeout", err)
	}
	// Closing the PTY must interrupt a pending write directly, without waiting
	// for its deadline. Otherwise shutdown can still wait forever for reaping.
	terminalInputTimeout = time.Minute
	written := make(chan error, 1)
	go func() { written <- value.write(bytes.Repeat([]byte("x"), 1<<20)) }()
	deadline = time.Now().Add(time.Second)
	for value.writeMu.TryLock() {
		value.writeMu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("input writer never started")
		}
		time.Sleep(time.Millisecond)
	}
	closed := make(chan struct{})
	go func() { value.close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("session close waited for blocked input")
	}
	if err := <-written; !errors.Is(err, os.ErrClosed) {
		t.Fatalf("interrupted input = %v, want closed PTY", err)
	}
}

func TestSessionTracksOnlyLiveOutputAsAgentActivity(t *testing.T) {
	value := newSessionForTest(nil)
	value.restore([]byte("saved output\r\n"), resumeMarker, "")
	_, _, lastOutput := value.recentOutput(phaseHintBytes)
	if !lastOutput.IsZero() {
		t.Fatal("restoring recorded output renewed activity")
	}

	before := time.Now()
	value.broadcast([]byte("\x1b[8;4H•"))
	output, _, lastOutput := value.recentOutput(phaseHintBytes)
	if lastOutput.Before(before) || lastOutput.After(time.Now()) || !bytes.HasSuffix(output, []byte("\x1b[8;4H•")) {
		t.Fatalf("live output did not renew activity: last output = %v", lastOutput)
	}
	value.broadcast(nil)
	_, _, afterEmpty := value.recentOutput(phaseHintBytes)
	if afterEmpty != lastOutput {
		t.Fatal("empty output renewed activity")
	}
}

func TestSessionReleasesThePTYWhenTheShellExits(t *testing.T) {
	exited := make(chan struct{})
	value, err := startSession("release", t.TempDir(), "/bin/sh", os.Environ(), 80, 24,
		func() { close(exited) })
	if err != nil {
		t.Fatalf("startSession() error = %v", err)
	}
	if err := value.write([]byte("exit\n")); err != nil {
		t.Fatalf("write() error = %v", err)
	}
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("shell did not exit")
	}

	// The server drops the session once the shell has exited and the PTY is
	// drained, so this is the last moment anything could close the master:
	// whatever holds it here holds it for the daemon's life.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(descriptorState(value.pty), os.ErrClosed) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("PTY master is still open after the shell exited")
}

func TestSessionDrainsPTYBeforeReportingExit(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}
	defer reader.Close()
	defer writer.Close()

	command := exec.Command("/bin/sh", "-c", "exit 0")
	if err := command.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	exited := make(chan struct{})
	value := newSessionForTest(reader)
	value.command = command
	value.readDone = make(chan struct{})
	value.onExit = func() { close(exited) }
	go value.read()
	go value.wait()

	select {
	case <-exited:
		t.Fatal("session reported exit before the PTY was drained")
	case <-time.After(250 * time.Millisecond):
	}
	if _, err := writer.Write([]byte("final output")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() writer error = %v", err)
	}
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("session did not report exit after the PTY was drained")
	}
	if got := string(value.history.bytes()); got != "final output" {
		t.Fatalf("history = %q, want final output", got)
	}
}

func TestSessionReportsExitBeforeClosingClients(t *testing.T) {
	command := exec.Command("/bin/sh", "-c", "exit 0")
	if err := command.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	serverConnection, clientConnection := net.Pipe()
	defer serverConnection.Close()
	defer clientConnection.Close()

	readDone := make(chan struct{})
	close(readDone)
	exitDone := make(chan struct{})
	exitStarted := make(chan struct{})
	releaseExit := make(chan struct{})
	defer func() {
		select {
		case <-releaseExit:
		default:
			close(releaseExit)
		}
	}()
	value := newSessionForTest(nil)
	value.command = command
	value.readDone = readDone
	value.exitDone = exitDone
	value.onExit = func() {
		close(exitStarted)
		<-releaseExit
	}
	value.clients = map[net.Conn]*attachment{serverConnection: {live: true}}
	go value.wait()

	select {
	case <-exitStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("session did not report exit")
	}
	if err := clientConnection.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	if _, err := clientConnection.Read(make([]byte, 1)); err == nil {
		t.Fatal("Read() succeeded while session exit was being persisted")
	} else if errors.Is(err, io.EOF) {
		t.Fatal("client closed before session exit was persisted")
	} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("Read() error = %v, want timeout", err)
	}
	closed := make(chan struct{})
	go func() {
		value.close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("close() returned before session exit cleanup finished")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseExit)
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("close() did not return after session exit cleanup finished")
	}
	if _, err := clientConnection.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("Read() after exit error = %v, want EOF", err)
	}
}

// descriptorState reports os.ErrClosed for a file that has been closed and nil
// for one that is still open. A write of nothing is the question that does not
// change the answer: it reaches the descriptor check and stops there.
func descriptorState(file *os.File) error {
	_, err := file.Write(nil)
	return err
}
