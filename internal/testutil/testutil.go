// Package testutil holds helpers shared by the romty package tests.
package testutil

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nalbam/romty/internal/client"
)

// QuietLogger keeps the daemon's diagnostics out of test output. They belong
// in daemon.log, where a user can find them, not interleaved with test names.
func QuietLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

// socketPathLimit is the portable ceiling for a unix socket path. Linux allows
// 108 bytes and macOS 104, so the shorter one bounds both.
const socketPathLimit = 104

// ShortTempDir creates a temporary romty home whose daemon socket path stays
// within socketPathLimit. TMPDIR is used when it fits, which it does not on
// macOS, where the per-user temporary directory alone is nearly 50 bytes.
func ShortTempDir(t *testing.T) string {
	t.Helper()
	base := os.TempDir()
	if len(filepath.Join(base, "romty-test-0123456789", "daemon.sock")) > socketPathLimit {
		base = "/tmp"
	}
	directory, err := os.MkdirTemp(base, "romty-test-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	return directory
}

// WaitForDaemon blocks until the daemon answers a ping, and fails the test when
// it does not become ready in time.
func WaitForDaemon(t *testing.T, backend *client.Client) {
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
