package client

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opspresso/romty/internal/paths"
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
	socket := serveUnversioned(t)

	_, err := New(socket).Snapshot()
	if err == nil {
		t.Fatal("Snapshot() accepted a daemon that speaks a different protocol")
	}
	for _, want := range []string{"protocol", "romty stop"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to mention %q", err, want)
		}
	}
}

// The daemon answers ping and shutdown whatever version asked, and this side
// has to accept those answers. It did not, and the two calls that carry the
// remedy were exactly the two that could not reach it: ping reported a
// mismatched daemon as no daemon at all, so romty started a second one and
// gave up with "daemon did not become ready", and `romty stop` stopped the
// daemon and then called that a failure to stop it.
func TestExemptCallsSurviveAnOutdatedDaemon(t *testing.T) {
	socket := serveUnversioned(t)
	backend := New(socket)

	if err := backend.Ping(); err != nil {
		t.Fatalf("Ping() error = %v, want an outdated daemon to answer it", err)
	}
	if err := backend.Shutdown(); err != nil {
		t.Fatalf("Shutdown() error = %v, want an outdated daemon to answer it", err)
	}
}

// serveUnversioned stands in for a daemon that answers without a version, the
// way one from before the field would.
func serveUnversioned(t *testing.T) string {
	t.Helper()
	socket := filepath.Join(shortTempDir(t), "daemon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	t.Cleanup(func() { listener.Close() })

	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				if _, err := bufio.NewReader(connection).ReadBytes('\n'); err != nil {
					return
				}
				connection.Write([]byte("{}\n"))
			}()
		}
	}()
	return socket
}

// Starting a second daemon is the answer to nothing listening, and to nothing
// else. An outdated daemon is listening, so EnsureDaemon must let the caller
// reach it and be told which side to restart, rather than starting a rival
// that loses the lock and exits — which is what turned a mismatch into
// "daemon did not become ready".
func TestEnsureDaemonAcceptsAnOutdatedDaemon(t *testing.T) {
	socket := serveUnversioned(t)

	// A path with nothing on it, so an attempt to start a daemon is a failure
	// naming itself rather than a silent second process.
	if err := EnsureDaemon(runtimeFor(socket), "/nonexistent/romty"); err != nil {
		t.Fatalf("EnsureDaemon() error = %v, want an outdated daemon to count as running", err)
	}
}

// A socket that answers but cannot be understood is not an absent daemon
// either. Reporting what the ping said beats spending three seconds waiting
// for a daemon that was never going to start.
func TestEnsureDaemonReportsASocketThatAnswersNothing(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "daemon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			// Accepted and then ignored, which is the whole of this daemon.
			t.Cleanup(func() { connection.Close() })
		}
	}()

	previous := handshakeTimeout
	handshakeTimeout = 200 * time.Millisecond
	defer func() { handshakeTimeout = previous }()

	err = EnsureDaemon(runtimeFor(socket), "/nonexistent/romty")
	if err == nil {
		t.Fatal("EnsureDaemon() succeeded against a socket that answers nothing")
	}
	if strings.Contains(err.Error(), "did not become ready") {
		t.Fatalf("error = %q, want what the ping said rather than a startup timeout", err)
	}
}

// runtimeFor points a romty home at an existing socket.
func runtimeFor(socket string) paths.Paths {
	directory := filepath.Dir(socket)
	return paths.Paths{
		Directory: directory,
		Socket:    socket,
		State:     filepath.Join(directory, "state.json"),
		Config:    filepath.Join(directory, "config.json"),
		Log:       filepath.Join(directory, "daemon.log"),
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
