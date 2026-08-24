package daemon

import (
	"errors"
	"os"
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

	// The server drops the session as soon as the shell exits, so this is the
	// last moment anything could close the master: whatever holds it here holds
	// it for the daemon's life.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(descriptorState(value.pty), os.ErrClosed) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("PTY master is still open after the shell exited")
}

// descriptorState reports os.ErrClosed for a file that has been closed and nil
// for one that is still open. A write of nothing is the question that does not
// change the answer: it reaches the descriptor check and stops there.
func descriptorState(file *os.File) error {
	_, err := file.Write(nil)
	return err
}
