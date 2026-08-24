package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nalbam/romty/internal/client"
	"github.com/nalbam/romty/internal/daemon"
	"github.com/nalbam/romty/internal/paths"
	"github.com/nalbam/romty/internal/testutil"
)

func TestRunRejectsNestedRomty(t *testing.T) {
	t.Setenv("ROMTY", "1")

	err := run()
	if err == nil || !strings.Contains(err.Error(), "inside a romty terminal") {
		t.Fatalf("run() error = %v, want nested romty error", err)
	}
}

func TestRunStopsDaemon(t *testing.T) {
	runtime := stopArgs(t)
	server, err := daemon.New(runtime.Socket, runtime.State, "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	testutil.WaitForDaemon(t, client.New(runtime.Socket))

	if err := run(); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not stop")
	}

	// Stopping again must stay a no-op so scripts can chain on `romty stop`.
	if err := run(); err != nil {
		t.Fatalf("second run() error = %v, want nil for an already stopped daemon", err)
	}
}

func TestRunStopsMissingDaemon(t *testing.T) {
	stopArgs(t)

	if err := run(); err != nil {
		t.Fatalf("run() error = %v, want nil when no daemon was ever started", err)
	}
}

// stopArgs points romty at an empty runtime directory and sets `romty stop` as
// the command line for the duration of the test.
func stopArgs(t *testing.T) paths.Paths {
	t.Helper()
	t.Setenv("ROMTY", "")
	t.Setenv("ROMTY_HOME", testutil.ShortTempDir(t))
	runtime, err := paths.Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	originalArgs := os.Args
	os.Args = []string{"romty", "stop"}
	t.Cleanup(func() { os.Args = originalArgs })
	return runtime
}
