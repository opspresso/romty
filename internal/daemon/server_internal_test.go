package daemon

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

	value := &session{clients: make(map[net.Conn]*attachment)}
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

	value := &session{clients: make(map[net.Conn]*attachment)}
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

	value := &session{clients: make(map[net.Conn]*attachment)}
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
