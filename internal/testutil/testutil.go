// Package testutil holds helpers shared by the romty package tests.
package testutil

import (
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/opspresso/romty/internal/paths"
)

// QuietLogger keeps the daemon's diagnostics out of test output. They belong
// in daemon.log, where a user can find them, not interleaved with test names.
func QuietLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

// ShortTempDir creates a temporary romty home whose daemon socket path stays
// under paths.SocketPathLimit, which is what paths.Resolve refuses to exceed.
// TMPDIR is used when it fits, which it does not on macOS, where the per-user
// temporary directory alone is nearly 50 bytes.
func ShortTempDir(t *testing.T) string {
	t.Helper()
	base := os.TempDir()
	// The name MkdirTemp settles on is "romty-test-" and ten digits, which is
	// what this stands in for.
	if len(filepath.Join(base, "romty-test-0123456789", "daemon.sock")) >= paths.SocketPathLimit {
		base = "/tmp"
	}
	directory, err := os.MkdirTemp(base, "romty-test-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	return directory
}

// Pinger is all WaitForDaemon needs of a client. Naming it rather than the
// concrete type keeps this package out of the client's import graph, so the
// client's own tests can use the helpers here instead of keeping copies.
type Pinger interface {
	Ping() error
}

// WaitForDaemon blocks until the daemon answers a ping, and fails the test when
// it does not become ready in time.
func WaitForDaemon(t *testing.T, backend Pinger) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := backend.Ping(); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("daemon did not become ready")
}

// Set replaces *target with value for the rest of the test and restores the
// previous value when the test ends.
func Set[T any](t testing.TB, target *T, value T) {
	t.Helper()
	previous := *target
	*target = value
	t.Cleanup(func() { *target = previous })
}

// RunGit runs git with a fixed identity and no user or system configuration,
// so fixtures can commit anywhere. It skips the test when git is missing.
func RunGit(t *testing.T, arguments ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	command := exec.Command("git", arguments...)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=romty", "GIT_AUTHOR_EMAIL=romty@example.com",
		"GIT_COMMITTER_NAME=romty", "GIT_COMMITTER_EMAIL=romty@example.com")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}

// WriteFile writes content to name under directory, creating parent
// directories as needed.
func WriteFile(t *testing.T, directory, name, content string) {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}
