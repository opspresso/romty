package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/opspresso/romty/internal/client"
	"github.com/opspresso/romty/internal/daemon"
	"github.com/opspresso/romty/internal/paths"
	"github.com/opspresso/romty/internal/protocol"
	"github.com/opspresso/romty/internal/testutil"
	"github.com/opspresso/romty/internal/version"
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

	err := runCommand(nil, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "inside a romty terminal") {
		t.Fatalf("run() error = %v, want nested romty error", err)
	}
}

func TestVersionAndHelpDoNotNeedARuntime(t *testing.T) {
	t.Setenv("ROMTY", "1")
	t.Setenv("ROMTY_HOME", strings.Repeat("too-deep/", paths.SocketPathLimit))
	originalVersion := version.Value
	version.Value = "9.8.7"
	t.Cleanup(func() { version.Value = originalVersion })

	var output bytes.Buffer
	if err := runCommand([]string{"version"}, &output); err != nil {
		t.Fatalf("version error = %v", err)
	}
	if got := output.String(); got != "romty v9.8.7\n" {
		t.Fatalf("version output = %q", got)
	}

	output.Reset()
	if err := runCommand([]string{"help"}, &output); err != nil {
		t.Fatalf("help error = %v", err)
	}
	for _, command := range []string{"status", "version", "help", "doctor", "hooks", "list", "stop"} {
		if !strings.Contains(output.String(), "  "+command) {
			t.Fatalf("help does not contain %q:\n%s", command, output.String())
		}
	}
	if strings.Contains(output.String(), "  daemon") {
		t.Fatalf("help exposes the internal daemon command:\n%s", output.String())
	}
}

func TestHooksCommandInstallsDetectedAgentHooksWithoutARuntime(t *testing.T) {
	useReleaseBuild(t)
	bin := t.TempDir()
	for _, name := range []string{"claude", "codex"} {
		path := filepath.Join(bin, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	claudeHome := filepath.Join(t.TempDir(), "claude")
	codexHome := filepath.Join(t.TempDir(), "codex")
	t.Setenv("PATH", bin)
	t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("ROMTY_HOME", strings.Repeat("too-deep/", paths.SocketPathLimit))

	var output bytes.Buffer
	if err := runCommand([]string{"hooks"}, &output); err != nil {
		t.Fatalf("hooks error = %v", err)
	}
	for _, want := range []string{"claude:     installed", "codex:      installed"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("hooks output does not contain %q:\n%s", want, output.String())
		}
	}
	for _, path := range []string{filepath.Join(claudeHome, "settings.json"), filepath.Join(codexHome, "hooks.json")} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read installed hooks: %v", err)
		}
		if !bytes.Contains(data, []byte(" hook ")) {
			t.Fatalf("installed file has no romty hook: %s", data)
		}
	}
}

func TestHooksCommandDoesNotOverwriteInvalidSettings(t *testing.T) {
	useReleaseBuild(t)
	bin := t.TempDir()
	claude := filepath.Join(bin, "claude")
	if err := os.WriteFile(claude, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	settings := filepath.Join(home, "settings.json")
	broken := []byte(`{"hooks":[]}`)
	if err := os.WriteFile(settings, broken, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "codex"))

	var output bytes.Buffer
	err := runCommand([]string{"hooks"}, &output)
	if err == nil || !strings.Contains(err.Error(), "hooks must be an object") {
		t.Fatalf("hooks error = %v, want invalid settings", err)
	}
	if !strings.Contains(output.String(), "claude:     invalid") || !strings.Contains(output.String(), "codex:      not found") {
		t.Fatalf("hooks output = %q", output.String())
	}
	got, readErr := os.ReadFile(settings)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(got, broken) {
		t.Fatal("hooks command changed invalid settings")
	}
}

func TestHooksCommandSkipsDevelopmentBuild(t *testing.T) {
	bin := t.TempDir()
	claude := filepath.Join(bin, "claude")
	if err := os.WriteFile(claude, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	originalVersion := version.Value
	version.Value = ""
	t.Cleanup(func() { version.Value = originalVersion })
	t.Setenv("PATH", bin)
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "codex"))

	var output bytes.Buffer
	if err := runCommand([]string{"hooks"}, &output); err != nil {
		t.Fatalf("hooks error = %v", err)
	}
	if !strings.Contains(output.String(), "claude:     development build; skipped") {
		t.Fatalf("hooks output = %q", output.String())
	}
	if _, err := os.Stat(filepath.Join(home, "settings.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("development hooks created settings: %v", err)
	}
}

func useReleaseBuild(t *testing.T) {
	t.Helper()
	original := version.Value
	version.Value = "0.15.0"
	t.Cleanup(func() { version.Value = original })
}

func TestStatusAndListDoNotStartAMissingDaemon(t *testing.T) {
	home := filepath.Join(testutil.ShortTempDir(t), "romty-home")
	t.Setenv("ROMTY", "1")
	t.Setenv("ROMTY_HOME", home)

	for _, command := range []string{"status", "list"} {
		var output bytes.Buffer
		if err := runCommand([]string{command}, &output); err != nil {
			t.Fatalf("%s error = %v", command, err)
		}
		if got := output.String(); got != "daemon:     stopped\n" {
			t.Fatalf("%s output = %q", command, got)
		}
	}
	var doctorOutput bytes.Buffer
	if err := runCommand([]string{"doctor"}, &doctorOutput); err != nil {
		t.Fatalf("doctor error = %v", err)
	}
	for _, want := range []string{
		"runtime:    not created",
		"state:      not created",
		"config:     not created",
		"daemon:     stopped",
	} {
		if !strings.Contains(doctorOutput.String(), want) {
			t.Fatalf("doctor does not contain %q:\n%s", want, doctorOutput.String())
		}
	}
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only commands created runtime directory: %v", err)
	}
}

func TestStatusAndListDescribeTheRunningDaemon(t *testing.T) {
	runtime := commandRuntime(t)
	rootName := "projects\x1b]0;hostile\a"
	rootPath := filepath.Join(testutil.ShortTempDir(t), rootName)
	workspacePath := filepath.Join(rootPath, "alpha")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	backend := serveCommandDaemon(t, runtime)
	snapshot, err := backend.AddRoot(rootPath)
	if err != nil {
		t.Fatalf("AddRoot() error = %v", err)
	}
	workspace, err := backend.EnsureWorkspace(snapshot.Roots[0].Root.ID, workspacePath)
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	if _, err := backend.CreateTab(workspace.ID, 80, 24); err != nil {
		t.Fatalf("CreateTab() error = %v", err)
	}

	var output bytes.Buffer
	if err := runCommand([]string{"status"}, &output); err != nil {
		t.Fatalf("status error = %v", err)
	}
	for _, want := range []string{
		"daemon:     running",
		fmt.Sprintf("protocol:   %d", protocol.Version),
		"roots:      1",
		"workspaces: 1",
		"sessions:   1 (claude: 0, codex: 0, shell: 1)",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("status does not contain %q:\n%s", want, output.String())
		}
	}

	output.Reset()
	if err := runCommand([]string{"list"}, &output); err != nil {
		t.Fatalf("list error = %v", err)
	}
	for _, want := range []string{
		fmt.Sprintf("root:       %q  %q", rootName, snapshot.Roots[0].Root.Path),
		fmt.Sprintf("  workspace: %q  %q", "alpha", workspace.Path),
		"    tab:    \"1\"  shell",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("list does not contain %q:\n%s", want, output.String())
		}
	}
	if strings.Contains(output.String(), "\x1b") || strings.Contains(output.String(), "\a") {
		t.Fatalf("list emitted terminal control characters: %q", output.String())
	}
}

func TestDoctorReportsInvalidStateWithoutChangingIt(t *testing.T) {
	runtime := commandRuntime(t)
	if err := os.Mkdir(runtime.Directory, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	invalid := []byte("{not json\n")
	if err := os.WriteFile(runtime.State, invalid, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var output bytes.Buffer
	err := runCommand([]string{"doctor"}, &output)
	if err == nil || !strings.Contains(err.Error(), "doctor found 1 problem") {
		t.Fatalf("doctor error = %v", err)
	}
	for _, want := range []string{
		"runtime:    ok",
		"state:      error",
		"config:     not created",
		"daemon:     stopped",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("doctor does not contain %q:\n%s", want, output.String())
		}
	}
	data, readErr := os.ReadFile(runtime.State)
	if readErr != nil || !bytes.Equal(data, invalid) {
		t.Fatalf("doctor changed invalid state: data %q, error %v", data, readErr)
	}
}

func TestCommandThemeUsesColorOnlyForATerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer master.Close()
	defer terminal.Close()
	if !newCommandTheme(terminal).enabled {
		t.Fatal("terminal output did not enable command colors")
	}
	if newCommandTheme(&bytes.Buffer{}).enabled {
		t.Fatal("buffered output enabled command colors")
	}

	t.Setenv("NO_COLOR", "1")
	if newCommandTheme(terminal).enabled {
		t.Fatal("NO_COLOR did not disable command colors")
	}
}

func TestPrintFieldColorsThePaddedLabel(t *testing.T) {
	var output bytes.Buffer
	if err := printField(&output, commandTheme{enabled: true}, "daemon", "running"); err != nil {
		t.Fatalf("printField() error = %v", err)
	}
	if got, want := output.String(), "\x1b[36mdaemon:    \x1b[0m running\n"; got != want {
		t.Fatalf("printField() = %q, want %q", got, want)
	}
}

func TestCommandTextReplacesControlCharacters(t *testing.T) {
	if got, want := commandText("/tmp/\x1b[31mconfig\n"), "/tmp/�[31mconfig�"; got != want {
		t.Fatalf("commandText() = %q, want %q", got, want)
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
	// The dashboard now offers to configure agents found on PATH. This test is
	// about daemon startup and quitting, so give the child no agent binaries.
	t.Setenv("PATH", t.TempDir())
	runtime, err := paths.Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable() error = %v", err)
	}

	command := exec.CommandContext(t.Context(), executable)
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

	stop := exec.CommandContext(t.Context(), executable, "stop")
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

func commandRuntime(t *testing.T) paths.Paths {
	t.Helper()
	t.Setenv("ROMTY", "")
	t.Setenv("ROMTY_HOME", filepath.Join(testutil.ShortTempDir(t), "romty-home"))
	runtime, err := paths.Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	return runtime
}

func serveCommandDaemon(t *testing.T, runtime paths.Paths) *client.Client {
	t.Helper()
	server, err := daemon.New(runtime.Socket, runtime.State, "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.SetLogger(testutil.QuietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	})
	backend := client.New(runtime.Socket)
	testutil.WaitForDaemon(t, backend)
	return backend
}
