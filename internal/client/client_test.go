package client

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
