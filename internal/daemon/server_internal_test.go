package daemon

import (
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
