package daemon

import (
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

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
	value := &session{
		pty:      reader,
		command:  command,
		readDone: make(chan struct{}),
		onExit: func() {
			close(exited)
		},
		modes:   newModeTracker(),
		clients: make(map[net.Conn]*attachment),
	}
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
	value := &session{
		command:  command,
		readDone: readDone,
		exitDone: exitDone,
		onExit: func() {
			close(exitStarted)
			<-releaseExit
		},
		clients: map[net.Conn]*attachment{serverConnection: {live: true}},
	}
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
