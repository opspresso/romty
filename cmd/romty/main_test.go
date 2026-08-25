package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/opspresso/romty/internal/client"
	"github.com/opspresso/romty/internal/daemon"
	"github.com/opspresso/romty/internal/paths"
	"github.com/opspresso/romty/internal/testutil"
)

func TestMain(m *testing.M) {
	if os.Getenv("ROMTY_TEST_PROCESS") == "1" {
		if err := run(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

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
	server.SetLogger(testutil.QuietLogger())
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

func TestBinaryStartsDaemonAndEntersTheDashboard(t *testing.T) {
	home := testutil.ShortTempDir(t)
	t.Setenv("ROMTY", "")
	t.Setenv("ROMTY_HOME", home)
	t.Setenv("ROMTY_TEST_PROCESS", "1")
	runtime, err := paths.Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable() error = %v", err)
	}

	command := exec.Command(executable)
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatalf("start romty in PTY: %v", err)
	}
	defer terminal.Close()
	exited := false
	defer func() {
		if !exited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()

	backend := client.New(runtime.Socket)
	testutil.WaitForDaemon(t, backend)
	chunks := make(chan []byte, 16)
	readErrors := make(chan error, 1)
	go func() {
		defer close(chunks)
		buffer := make([]byte, 4096)
		for {
			count, err := terminal.Read(buffer)
			if count > 0 {
				chunks <- append([]byte(nil), buffer[:count]...)
			}
			if err != nil {
				readErrors <- err
				return
			}
		}
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	output := make([]byte, 0, 4096)
	for !strings.Contains(string(output), "No roots") {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				t.Fatalf("dashboard closed before rendering:\n%s", output)
			}
			output = append(output, chunk...)
		case err := <-readErrors:
			t.Fatalf("read dashboard: %v\noutput:\n%s", err, output)
		case <-timer.C:
			t.Fatalf("dashboard did not render its initial state:\n%s", output)
		}
	}
	go func() {
		for range chunks {
		}
	}()
	if _, err := terminal.Write([]byte("q")); err != nil {
		t.Fatalf("quit dashboard: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("romty exited with error: %v", err)
		}
		exited = true
	case <-time.After(5 * time.Second):
		t.Fatal("romty did not quit from the dashboard")
	}

	stop := exec.Command(executable, "stop")
	if output, err := stop.CombinedOutput(); err != nil {
		t.Fatalf("romty stop error = %v\noutput:\n%s", err, output)
	}
	if err := backend.Ping(); err == nil || !client.Unavailable(err) {
		t.Fatalf("daemon still answers after stop: %v", err)
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
