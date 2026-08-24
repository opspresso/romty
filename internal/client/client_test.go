package client

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizePathExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := normalizePath("~/projects")
	if err != nil {
		t.Fatalf("normalizePath() error = %v", err)
	}
	want := filepath.Join(home, "projects")
	if got != want {
		t.Fatalf("normalizePath() = %q, want %q", got, want)
	}
}

// A daemon from before the version field must be named as the problem, in
// terms the user can act on, rather than surfacing as a puzzling missing field
// or an unknown action.
func TestCallReportsAnOutdatedDaemon(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "romty-version-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	defer os.RemoveAll(directory)
	socket := filepath.Join(directory, "daemon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()

	// A daemon that answers without a version, the way an older one would.
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		bufio.NewReader(connection).ReadBytes('\n')
		connection.Write([]byte("{}\n"))
	}()

	err = New(socket).Ping()
	if err == nil {
		t.Fatal("Ping() accepted a daemon that speaks a different protocol")
	}
	for _, want := range []string{"protocol", "romty stop"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to mention %q", err, want)
		}
	}
}

// A daemon can accept a connection and then say nothing — stopped, wedged, or
// another program holding the socket path. Attach has to give up on that: it
// runs on the goroutine that opens a terminal, so a read with no deadline
// leaves the tab never appearing and nothing to say why.
func TestOpenAttachGivesUpOnADaemonThatNeverAnswers(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "daemon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		// Accepted and then ignored, which is the whole of this daemon.
		accepted <- connection
	}()

	previous := handshakeTimeout
	handshakeTimeout = 200 * time.Millisecond
	defer func() { handshakeTimeout = previous }()

	done := make(chan error, 1)
	go func() {
		_, _, err := New(socket).OpenAttach("tab-1")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("OpenAttach() succeeded against a daemon that never answered")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OpenAttach() is still waiting on a daemon that never answers")
	}
	if connection := <-accepted; connection != nil {
		connection.Close()
	}
}

// shortTempDir keeps the socket path within the portable 104-byte limit, which
// the per-user temporary directory on macOS does not leave room for.
func shortTempDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "romty-client-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	return directory
}
