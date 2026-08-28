package daemon_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opspresso/romty/internal/client"
	"github.com/opspresso/romty/internal/daemon"
	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/protocol"
	"github.com/opspresso/romty/internal/state"
	"github.com/opspresso/romty/internal/testutil"
)

func TestServeRemovesPersistedTerminalTabs(t *testing.T) {
	base := testutil.ShortTempDir(t)
	socket := filepath.Join(base, "daemon.sock")
	statePath := filepath.Join(base, "state.json")
	store := state.New(statePath)
	if err := store.Save(model.State{Tabs: []model.Tab{{ID: "stale-tab", Running: true}}}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	server, err := daemon.New(socket, statePath, "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.SetLogger(testutil.QuietLogger())
	persisted, err := store.Load()
	if err != nil {
		t.Fatalf("Load() before Serve error = %v", err)
	}
	if len(persisted.Tabs) != 1 {
		t.Fatalf("persisted tabs before Serve = %#v, want tabs unchanged", persisted.Tabs)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() {
		cancel()
		<-done
	}()
	testutil.WaitForDaemon(t, client.New(socket))
	persisted, err = store.Load()
	if err != nil {
		t.Fatalf("Load() after Serve error = %v", err)
	}
	if len(persisted.Tabs) != 0 {
		t.Fatalf("persisted tabs = %#v, want stale tabs removed", persisted.Tabs)
	}
}

func TestServerDiscoversWorkspacesAndReattachesSession(t *testing.T) {
	base := testutil.ShortTempDir(t)
	root := filepath.Join(base, "projects")
	secondRoot := filepath.Join(base, "work")
	workspacePath := filepath.Join(root, "alpha")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "beta"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("not a workspace"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(secondRoot, "gamma"), 0o755); err != nil {
		t.Fatalf("MkdirAll() second root error = %v", err)
	}

	socket := filepath.Join(base, "daemon.sock")
	statePath := filepath.Join(base, "state.json")
	server, err := daemon.New(socket, statePath, "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.SetLogger(testutil.QuietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve() error = %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Serve() did not stop")
		}
	})

	backend := client.New(socket)
	testutil.WaitForDaemon(t, backend)
	snapshot, err := backend.AddRoot(root)
	if err != nil {
		t.Fatalf("AddRoot() error = %v", err)
	}
	if len(snapshot.Roots) != 1 {
		t.Fatalf("len(Roots) = %d, want 1", len(snapshot.Roots))
	}
	directories := snapshot.Roots[0].Directories
	if len(directories) != 2 || directories[0].Workspace.Name != "alpha" || directories[1].Workspace.Name != "beta" {
		t.Fatalf("Directories = %#v, want alpha and beta", directories)
	}
	snapshot, err = backend.AddRoot(secondRoot)
	if err != nil {
		t.Fatalf("AddRoot() second root error = %v", err)
	}
	if len(snapshot.Roots) != 2 || len(snapshot.Roots[1].Directories) != 1 {
		t.Fatalf("Roots = %#v, want two roots with discovered directories", snapshot.Roots)
	}

	workspace, err := backend.EnsureWorkspace(snapshot.Roots[0].Root.ID, workspacePath)
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	tab, err := backend.CreateTab(workspace.ID, 100, 30)
	if err != nil {
		t.Fatalf("CreateTab() error = %v", err)
	}

	firstConnection, firstReader, err := backend.OpenAttach(tab.ID)
	if err != nil {
		t.Fatalf("OpenAttach() first error = %v", err)
	}
	writeCommand(t, firstConnection, "pwd")
	readUntil(t, firstConnection, firstReader, workspace.Path)
	if err := backend.Resize(tab.ID, 120, 35); err != nil {
		t.Fatalf("Resize() error = %v", err)
	}
	writeCommand(t, firstConnection, "stty size")
	readUntil(t, firstConnection, firstReader, "35 120")
	// The marker is assembled by printf so the shell's echo of the command
	// never contains it whole: matching the echo would let this test pass
	// while the replay had lost the command's actual output.
	writeCommand(t, firstConnection, "printf 'romty-first-%s\\n' marker")
	readUntil(t, firstConnection, firstReader, "romty-first-marker")
	if err := firstConnection.Close(); err != nil {
		t.Fatalf("Close() first connection error = %v", err)
	}

	restartedClient := client.New(socket)
	secondConnection, secondReader, err := restartedClient.OpenAttach(tab.ID)
	if err != nil {
		t.Fatalf("OpenAttach() second error = %v", err)
	}
	defer secondConnection.Close()
	readUntil(t, secondConnection, secondReader, "romty-first-marker")
	writeCommand(t, secondConnection, "printf 'romty-second-%s\\n' marker")
	readUntil(t, secondConnection, secondReader, "romty-second-marker")
	secondTab, err := restartedClient.CreateTab(workspace.ID, 90, 25)
	if err != nil {
		t.Fatalf("CreateTab() second error = %v", err)
	}
	thirdConnection, thirdReader, err := restartedClient.OpenAttach(secondTab.ID)
	if err != nil {
		t.Fatalf("OpenAttach() third error = %v", err)
	}
	defer thirdConnection.Close()
	writeCommand(t, thirdConnection, "printf 'romty-third-%s\\n' marker")
	readUntil(t, thirdConnection, thirdReader, "romty-third-marker")

	restored, err := restartedClient.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	tabs := restored.Roots[0].Directories[0].Tabs
	if len(tabs) != 2 || tabs[0].ID != tab.ID || tabs[1].ID != secondTab.ID || !tabs[0].Running || !tabs[1].Running {
		t.Fatalf("Tabs = %#v, want two running tabs", tabs)
	}

	persisted, err := state.New(statePath).Load()
	if err != nil {
		t.Fatalf("Load() persisted state error = %v", err)
	}
	if len(persisted.Roots) != 2 || len(persisted.Workspaces) != 1 || len(persisted.Tabs) != 2 {
		t.Fatalf("persisted state = %#v, want two roots, one workspace, and two tabs", persisted)
	}

	writeCommand(t, secondConnection, "exit")
	waitForTabCount(t, restartedClient, workspace.ID, 1)
	replacementTab, err := restartedClient.CreateTab(workspace.ID, 90, 25)
	if err != nil {
		t.Fatalf("CreateTab() replacement error = %v", err)
	}
	if replacementTab.Name == secondTab.Name {
		t.Fatalf("replacement tab name = %q, want a unique name", replacementTab.Name)
	}
	replacementConnection, _, err := restartedClient.OpenAttach(replacementTab.ID)
	if err != nil {
		t.Fatalf("OpenAttach() replacement error = %v", err)
	}
	defer replacementConnection.Close()
	writeCommand(t, thirdConnection, "exit")
	waitForTabCount(t, restartedClient, workspace.ID, 1)
	writeCommand(t, replacementConnection, "exit")
	waitForTabCount(t, restartedClient, workspace.ID, 0)

	persisted, err = state.New(statePath).Load()
	if err != nil {
		t.Fatalf("Load() state after terminal exits error = %v", err)
	}
	if len(persisted.Tabs) != 0 {
		t.Fatalf("persisted tabs = %#v, want exited tabs removed", persisted.Tabs)
	}
}

func TestServerOpensRootTerminalAndReloadsDirectories(t *testing.T) {
	base := testutil.ShortTempDir(t)
	root := filepath.Join(base, "projects")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	socket := filepath.Join(base, "daemon.sock")
	statePath := filepath.Join(base, "state.json")
	server, err := daemon.New(socket, statePath, "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.SetLogger(testutil.QuietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	backend := client.New(socket)
	testutil.WaitForDaemon(t, backend)
	snapshot, err := backend.AddRoot(root)
	if err != nil {
		t.Fatalf("AddRoot() error = %v", err)
	}
	rootID := snapshot.Roots[0].Root.ID
	workspace, err := backend.EnsureWorkspace(rootID, root)
	if err != nil {
		t.Fatalf("EnsureWorkspace() root error = %v", err)
	}
	tab, err := backend.CreateTab(workspace.ID, 80, 24)
	if err != nil {
		t.Fatalf("CreateTab() root error = %v", err)
	}
	connection, reader, err := backend.OpenAttach(tab.ID)
	if err != nil {
		t.Fatalf("OpenAttach() root error = %v", err)
	}
	defer connection.Close()

	writeCommand(t, connection, "pwd")
	readUntil(t, connection, reader, root)
	writeCommand(t, connection, "mkdir cloned")
	snapshot = waitForRootDirectoryCount(t, backend, 1)
	if len(snapshot.Roots[0].Tabs) != 1 || len(snapshot.Roots[0].Directories) != 1 || snapshot.Roots[0].Directories[0].Workspace.Name != "cloned" {
		t.Fatalf("root snapshot after create = %#v", snapshot.Roots[0])
	}

	writeCommand(t, connection, "rmdir cloned")
	snapshot = waitForRootDirectoryCount(t, backend, 0)
	if len(snapshot.Roots[0].Tabs) != 1 || len(snapshot.Roots[0].Directories) != 0 {
		t.Fatalf("root snapshot after delete = %#v", snapshot.Roots[0])
	}
}

func TestSnapshotKeepsRunningWorkspaceWhenItsDirectoryDisappears(t *testing.T) {
	base := testutil.ShortTempDir(t)
	root := filepath.Join(base, "projects")
	workspacePath := filepath.Join(root, "alpha")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	socket := filepath.Join(base, "daemon.sock")
	server, err := daemon.New(socket, filepath.Join(base, "state.json"), "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.SetLogger(testutil.QuietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	backend := client.New(socket)
	testutil.WaitForDaemon(t, backend)
	snapshot, err := backend.AddRoot(root)
	if err != nil {
		t.Fatalf("AddRoot() error = %v", err)
	}
	rootID := snapshot.Roots[0].Root.ID
	workspace, err := backend.EnsureWorkspace(rootID, workspacePath)
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	tab, err := backend.CreateTab(workspace.ID, 80, 24)
	if err != nil {
		t.Fatalf("CreateTab() error = %v", err)
	}

	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("RemoveAll() error = %v", err)
	}
	snapshot, err = backend.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snapshot.Roots) != 1 || snapshot.Roots[0].Error == "" || len(snapshot.Roots[0].Directories) != 1 {
		t.Fatalf("snapshot after directory removal = %#v", snapshot.Roots)
	}
	missing := snapshot.Roots[0].Directories[0]
	if missing.Workspace.ID != workspace.ID || len(missing.Tabs) != 1 || missing.Tabs[0].ID != tab.ID {
		t.Fatalf("missing workspace = %#v, want running tab %q", missing, tab.ID)
	}
	if _, err := backend.EnsureWorkspace(rootID, workspace.Path); err != nil {
		t.Fatalf("EnsureWorkspace() for running missing workspace error = %v", err)
	}
	connection, reader, err := backend.OpenAttach(tab.ID)
	if err != nil {
		t.Fatalf("OpenAttach() after directory removal error = %v", err)
	}
	defer connection.Close()
	writeCommand(t, connection, "printf workspace-survived")
	readUntil(t, connection, reader, "workspace-survived")
}

func TestEnsureWorkspaceRejectsNestedDirectory(t *testing.T) {
	base := testutil.ShortTempDir(t)
	root := filepath.Join(base, "projects")
	nested := filepath.Join(root, "alpha", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	server, err := daemon.New(filepath.Join(base, "daemon.sock"), filepath.Join(base, "state.json"), "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.SetLogger(testutil.QuietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	backend := client.New(filepath.Join(base, "daemon.sock"))
	testutil.WaitForDaemon(t, backend)
	snapshot, err := backend.AddRoot(root)
	if err != nil {
		t.Fatalf("AddRoot() error = %v", err)
	}
	_, err = backend.EnsureWorkspace(snapshot.Roots[0].Root.ID, nested)
	if err == nil || !strings.Contains(err.Error(), "direct child") {
		t.Fatalf("EnsureWorkspace() error = %v, want direct child error", err)
	}
}

func TestServerStopsAfterAcknowledgingShutdown(t *testing.T) {
	base := testutil.ShortTempDir(t)
	root := filepath.Join(base, "projects")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	socket := filepath.Join(base, "daemon.sock")
	server, err := daemon.New(socket, filepath.Join(base, "state.json"), "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.SetLogger(testutil.QuietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()

	backend := client.New(socket)
	testutil.WaitForDaemon(t, backend)
	snapshot, err := backend.AddRoot(root)
	if err != nil {
		t.Fatalf("AddRoot() error = %v", err)
	}
	workspace, err := backend.EnsureWorkspace(snapshot.Roots[0].Root.ID, root)
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	tab, err := backend.CreateTab(workspace.ID, 80, 24)
	if err != nil {
		t.Fatalf("CreateTab() error = %v", err)
	}
	connection, reader, err := backend.OpenAttach(tab.ID)
	if err != nil {
		t.Fatalf("OpenAttach() error = %v", err)
	}
	defer connection.Close()

	if err := backend.Shutdown(); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve() did not stop after Shutdown()")
	}
	if err := backend.Ping(); err == nil {
		t.Fatal("Ping() after Shutdown() error = nil")
	}
	if err := connection.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	buffer := make([]byte, 4096)
	for {
		if _, err := reader.Read(buffer); err != nil {
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				t.Fatal("terminal connection remained open after Shutdown()")
			}
			break
		}
	}
}

func waitForTabCount(t *testing.T, backend *client.Client, workspaceID string, count int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := backend.Snapshot()
		if err == nil {
			for _, root := range snapshot.Roots {
				for _, directory := range root.Directories {
					if directory.Workspace.ID == workspaceID && len(directory.Tabs) == count {
						return
					}
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("workspace %q tab count did not become %d", workspaceID, count)
}

func TestCloseTabTerminatesOnlyTheSelectedSession(t *testing.T) {
	socket, cancel, done := serveForTest(t)
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve() error = %v", err)
		}
	})
	root := t.TempDir()
	workspacePath := filepath.Join(root, "alpha")
	if err := os.Mkdir(workspacePath, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	backend := client.New(socket)
	testutil.WaitForDaemon(t, backend)
	snapshot, err := backend.AddRoot(root)
	if err != nil {
		t.Fatalf("AddRoot() error = %v", err)
	}
	workspace, err := backend.EnsureWorkspace(snapshot.Roots[0].Root.ID, workspacePath)
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	first, err := backend.CreateTab(workspace.ID, 80, 24)
	if err != nil {
		t.Fatalf("CreateTab() first error = %v\n%s", err, diagnoseShellStart(workspacePath))
	}
	second, err := backend.CreateTab(workspace.ID, 80, 24)
	if err != nil {
		t.Fatalf("CreateTab() second error = %v", err)
	}

	snapshot, err = backend.CloseTab(first.ID)
	if err != nil {
		t.Fatalf("CloseTab() error = %v", err)
	}
	if len(snapshot.Roots[0].Directories) != 1 || len(snapshot.Roots[0].Directories[0].Tabs) != 1 ||
		snapshot.Roots[0].Directories[0].Tabs[0].ID != second.ID {
		t.Fatalf("tabs after close = %#v, want only %q", snapshot.Roots[0].Directories, second.ID)
	}
	if _, _, err := backend.OpenTerminal(first.ID); err == nil {
		t.Fatal("closed tab still accepts terminal attachments")
	}
	stream, _, err := backend.OpenTerminal(second.ID)
	if err != nil {
		t.Fatalf("OpenTerminal() remaining tab error = %v", err)
	}
	stream.Close()
}

func waitForRootDirectoryCount(t *testing.T, backend *client.Client, count int) model.Snapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := backend.Snapshot()
		if err == nil && len(snapshot.Roots) == 1 && len(snapshot.Roots[0].Directories) == count {
			return snapshot
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("root directory count did not become %d", count)
	return model.Snapshot{}
}

func writeCommand(t *testing.T, connection net.Conn, command string) {
	t.Helper()
	if _, err := fmt.Fprintf(connection, "%s\n", command); err != nil {
		t.Fatalf("write terminal command: %v", err)
	}
}

func readUntil(t *testing.T, connection net.Conn, reader *bufio.Reader, marker string) {
	t.Helper()
	if err := connection.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	defer connection.SetReadDeadline(time.Time{})

	var output strings.Builder
	buffer := make([]byte, 4096)
	for !strings.Contains(output.String(), marker) {
		count, err := reader.Read(buffer)
		if err != nil {
			t.Fatalf("read terminal output before %q: %v; output = %q", marker, err, output.String())
		}
		output.Write(buffer[:count])
	}
}

// A terminal query the guest emitted is recorded in the history like any other
// output. Replaying it makes the reattaching client's emulator answer a
// question that was asked and answered long ago, and the answer lands on the
// shell's command line as if typed.
func TestAttachDoesNotReplayTerminalQueries(t *testing.T) {
	base := testutil.ShortTempDir(t)
	socket := filepath.Join(base, "daemon.sock")
	server, err := daemon.New(socket, filepath.Join(base, "state.json"), "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.SetLogger(testutil.QuietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve() error = %v", err)
		}
	}()
	backend := client.New(socket)
	testutil.WaitForDaemon(t, backend)

	snapshot, err := backend.AddRoot(base)
	if err != nil {
		t.Fatalf("AddRoot() error = %v", err)
	}
	workspace, err := backend.EnsureWorkspace(snapshot.Roots[0].Root.ID, base)
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	tab, err := backend.CreateTab(workspace.ID, 80, 24)
	if err != nil {
		t.Fatalf("CreateTab() error = %v", err)
	}

	first, reader, err := backend.OpenAttach(tab.ID)
	if err != nil {
		t.Fatalf("OpenAttach() error = %v", err)
	}
	// The shell echoes the command line, so the marker is split in the source
	// and only whole in the output: waiting on the echo would race the history.
	writeCommand(t, first, `printf 'MARK\033[6n\033[c\033]11;?\033\\END'; echo SETT''LED`)
	readUntil(t, first, reader, "SETTLED")
	first.Close()

	second, secondReader, err := backend.OpenAttach(tab.ID)
	if err != nil {
		t.Fatalf("second OpenAttach() error = %v", err)
	}
	defer second.Close()
	if err := second.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	var replay strings.Builder
	buffer := make([]byte, 4096)
	for {
		count, err := secondReader.Read(buffer)
		replay.Write(buffer[:count])
		if err != nil {
			break
		}
	}
	for _, query := range []string{"\x1b[6n", "\x1b[c", "\x1b]11;?"} {
		if strings.Contains(replay.String(), query) {
			t.Fatalf("replayed history still contains the query %q:\n%q", query, replay.String())
		}
	}
	if !strings.Contains(replay.String(), "MARKEND") {
		t.Fatalf("replayed history lost the output around the queries:\n%q", replay.String())
	}
}

// A root can be unmounted, deleted, or made unreadable while romty is running.
// One such root used to fail every snapshot, and with it every path that needs
// one — including startup, which left romty unable to open at all.
func TestSnapshotSurvivesAnUnreadableRoot(t *testing.T) {
	base := testutil.ShortTempDir(t)
	socket := filepath.Join(base, "daemon.sock")
	server, err := daemon.New(socket, filepath.Join(base, "state.json"), "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.SetLogger(testutil.QuietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() { cancel(); <-done }()
	backend := client.New(socket)
	testutil.WaitForDaemon(t, backend)

	healthy := filepath.Join(base, "healthy")
	doomed := filepath.Join(base, "doomed")
	for _, path := range []string{healthy, doomed, filepath.Join(healthy, "alpha")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}
	if _, err := backend.AddRoot(healthy); err != nil {
		t.Fatalf("AddRoot(healthy) error = %v", err)
	}
	snapshot, err := backend.AddRoot(doomed)
	if err != nil {
		t.Fatalf("AddRoot(doomed) error = %v", err)
	}
	// Compared by name: the daemon stores the canonical path, which on macOS
	// gains a /private prefix that the caller's path does not have.
	var doomedID string
	for _, root := range snapshot.Roots {
		if root.Root.Name == "doomed" {
			doomedID = root.Root.ID
		}
	}
	if doomedID == "" {
		t.Fatalf("the second root is missing from %#v", snapshot.Roots)
	}

	if err := os.RemoveAll(doomed); err != nil {
		t.Fatalf("RemoveAll() error = %v", err)
	}
	snapshot, err = backend.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v, want the healthy root still reported", err)
	}
	if len(snapshot.Roots) != 2 {
		t.Fatalf("roots = %d, want both still listed", len(snapshot.Roots))
	}
	for _, root := range snapshot.Roots {
		switch root.Root.Name {
		case "healthy":
			if root.Error != "" || len(root.Directories) != 1 {
				t.Fatalf("healthy root = (error %q, directories %d), want it unaffected",
					root.Error, len(root.Directories))
			}
		case "doomed":
			if root.Error == "" {
				t.Fatal("the unreadable root reports no error")
			}
		}
	}

	// And it can be forgotten, which was impossible without editing state.json.
	snapshot, err = backend.RemoveRoot(doomedID)
	if err != nil {
		t.Fatalf("RemoveRoot() error = %v", err)
	}
	if len(snapshot.Roots) != 1 || snapshot.Roots[0].Root.Name != "healthy" {
		t.Fatalf("roots after removal = %#v, want only the healthy one", snapshot.Roots)
	}
	if _, err := backend.RemoveRoot(doomedID); err == nil {
		t.Fatal("removing an unknown root reported success")
	}
}

func TestRemoveRootTerminatesItsTerminalSessions(t *testing.T) {
	base := testutil.ShortTempDir(t)
	root := filepath.Join(base, "projects")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	socket := filepath.Join(base, "daemon.sock")
	server, err := daemon.New(socket, filepath.Join(base, "state.json"), "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.SetLogger(testutil.QuietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() { cancel(); <-done }()

	backend := client.New(socket)
	testutil.WaitForDaemon(t, backend)
	snapshot, err := backend.AddRoot(root)
	if err != nil {
		t.Fatalf("AddRoot() error = %v", err)
	}
	rootID := snapshot.Roots[0].Root.ID
	workspace, err := backend.EnsureWorkspace(rootID, root)
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	tab, err := backend.CreateTab(workspace.ID, 80, 24)
	if err != nil {
		t.Fatalf("CreateTab() error = %v", err)
	}
	connection, reader, err := backend.OpenAttach(tab.ID)
	if err != nil {
		t.Fatalf("OpenAttach() error = %v", err)
	}
	defer connection.Close()
	secondTab, err := backend.CreateTab(workspace.ID, 80, 24)
	if err != nil {
		t.Fatalf("CreateTab() second error = %v", err)
	}
	secondConnection, secondReader, err := backend.OpenAttach(secondTab.ID)
	if err != nil {
		t.Fatalf("OpenAttach() second error = %v", err)
	}
	defer secondConnection.Close()

	if _, err := backend.RemoveRoot(rootID); err != nil {
		t.Fatalf("RemoveRoot() error = %v", err)
	}
	expectTerminalClosed(t, connection, reader)
	expectTerminalClosed(t, secondConnection, secondReader)
}

func TestRemoveWorkspaceDeletesOnlyTheDirectChildAndTerminatesItsSessions(t *testing.T) {
	base := testutil.ShortTempDir(t)
	root := filepath.Join(base, "projects")
	workspacePath := filepath.Join(root, "alpha")
	siblingPath := filepath.Join(root, "beta")
	nestedPath := filepath.Join(workspacePath, "nested")
	if err := os.MkdirAll(nestedPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() workspace error = %v", err)
	}
	if err := os.Mkdir(siblingPath, 0o755); err != nil {
		t.Fatalf("Mkdir() sibling error = %v", err)
	}

	socket := filepath.Join(base, "daemon.sock")
	statePath := filepath.Join(base, "state.json")
	server, err := daemon.New(socket, statePath, "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.SetLogger(testutil.QuietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() { cancel(); <-done }()

	backend := client.New(socket)
	testutil.WaitForDaemon(t, backend)
	snapshot, err := backend.AddRoot(root)
	if err != nil {
		t.Fatalf("AddRoot() error = %v", err)
	}
	rootID := snapshot.Roots[0].Root.ID
	workspace, err := backend.EnsureWorkspace(rootID, workspacePath)
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	tab, err := backend.CreateTab(workspace.ID, 80, 24)
	if err != nil {
		t.Fatalf("CreateTab() error = %v", err)
	}
	connection, reader, err := backend.OpenAttach(tab.ID)
	if err != nil {
		t.Fatalf("OpenAttach() error = %v", err)
	}
	defer connection.Close()
	siblingWorkspace, err := backend.EnsureWorkspace(rootID, siblingPath)
	if err != nil {
		t.Fatalf("EnsureWorkspace() sibling error = %v", err)
	}
	siblingTab, err := backend.CreateTab(siblingWorkspace.ID, 80, 24)
	if err != nil {
		t.Fatalf("CreateTab() sibling error = %v", err)
	}
	siblingConnection, siblingReader, err := backend.OpenAttach(siblingTab.ID)
	if err != nil {
		t.Fatalf("OpenAttach() sibling error = %v", err)
	}
	defer siblingConnection.Close()

	canonicalRoot := snapshot.Roots[0].Root.Path
	canonicalNested := filepath.Join(workspace.Path, "nested")
	for _, protected := range []string{canonicalRoot, canonicalNested, filepath.Join(base, "outside")} {
		if _, err := backend.RemoveWorkspace(rootID, protected); err == nil {
			t.Fatalf("RemoveWorkspace(%q) succeeded outside the direct-child boundary", protected)
		}
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("protected root changed: %v", err)
	}
	if _, err := os.Stat(nestedPath); err != nil {
		t.Fatalf("protected nested directory changed: %v", err)
	}

	snapshot, err = backend.RemoveWorkspace(rootID, workspace.Path)
	if err != nil {
		t.Fatalf("RemoveWorkspace() error = %v", err)
	}
	if _, err := os.Stat(workspacePath); !os.IsNotExist(err) {
		t.Fatalf("workspace still exists: %v", err)
	}
	if _, err := os.Stat(siblingPath); err != nil {
		t.Fatalf("sibling changed: %v", err)
	}
	if len(snapshot.Roots) != 1 || len(snapshot.Roots[0].Directories) != 1 ||
		snapshot.Roots[0].Directories[0].Workspace.Name != "beta" {
		t.Fatalf("snapshot after removal = %#v, want only beta", snapshot)
	}
	persisted, err := state.New(statePath).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(persisted.Workspaces) != 1 || persisted.Workspaces[0].ID != siblingWorkspace.ID ||
		len(persisted.Tabs) != 1 || persisted.Tabs[0].ID != siblingTab.ID {
		t.Fatalf("persisted state after removal = %#v, want only the sibling session", persisted)
	}

	expectTerminalClosed(t, connection, reader)
	writeCommand(t, siblingConnection, "printf sibling-survived")
	readUntil(t, siblingConnection, siblingReader, "sibling-survived")
}

func expectTerminalClosed(t *testing.T, connection net.Conn, reader *bufio.Reader) {
	t.Helper()
	if err := connection.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	buffer := make([]byte, 4096)
	for {
		if _, err := reader.Read(buffer); err != nil {
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				t.Fatal("terminal connection remained open after removal")
			}
			return
		}
	}
}

// The socket is the only thing between another local process and every shell
// romty owns, so it must never be reachable by anyone else — not even during
// the instant between bind and chmod.
func TestServeCreatesAPrivateSocketAndDirectory(t *testing.T) {
	base := testutil.ShortTempDir(t)
	home := filepath.Join(base, "home")
	// A romty home that already exists with a permissive mode, which MkdirAll
	// would leave exactly as it found it.
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	socket := filepath.Join(home, "daemon.sock")
	server, err := daemon.New(socket, filepath.Join(home, "state.json"), "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.SetLogger(testutil.QuietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() { cancel(); <-done }()
	testutil.WaitForDaemon(t, client.New(socket))

	info, err := os.Stat(socket)
	if err != nil {
		t.Fatalf("Stat(socket) error = %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("socket mode = %04o, want 0600", mode)
	}
	directory, err := os.Stat(home)
	if err != nil {
		t.Fatalf("Stat(home) error = %v", err)
	}
	if mode := directory.Mode().Perm(); mode != 0o700 {
		t.Fatalf("romty home mode = %04o, want 0700 even though it existed as 0755", mode)
	}
}

// A shell can exit before its tab has finished being registered — a command
// that fails immediately, a login shell that refuses the directory. The tab
// must not survive its shell.
func TestCreateTabLeavesNoTabWhenTheShellExitsAtOnce(t *testing.T) {
	base := testutil.ShortTempDir(t)
	socket := filepath.Join(base, "daemon.sock")
	// A "shell" that exits the moment it starts. The client sends its own
	// SHELL with each tab, so that is where this has to be set.
	t.Setenv("SHELL", "/usr/bin/true")
	server, err := daemon.New(socket, filepath.Join(base, "state.json"), "/usr/bin/true")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.SetLogger(testutil.QuietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() { cancel(); <-done }()
	backend := client.New(socket)
	testutil.WaitForDaemon(t, backend)

	snapshot, err := backend.AddRoot(base)
	if err != nil {
		t.Fatalf("AddRoot() error = %v", err)
	}
	workspace, err := backend.EnsureWorkspace(snapshot.Roots[0].Root.ID, base)
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	for range 5 {
		if _, err := backend.CreateTab(workspace.ID, 80, 24); err != nil {
			t.Fatalf("CreateTab() error = %v", err)
		}
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err = backend.Snapshot()
		if err != nil {
			t.Fatalf("Snapshot() error = %v", err)
		}
		if countTabs(snapshot) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%d tabs outlived their shells", countTabs(snapshot))
}

func countTabs(snapshot model.Snapshot) int {
	total := 0
	for _, root := range snapshot.Roots {
		total += len(root.Tabs)
		for _, directory := range root.Directories {
			total += len(directory.Tabs)
		}
	}
	return total
}

// The daemon can be days older than the shell the user is working in, so a tab
// must start with that shell's environment rather than whatever the daemon
// inherited when it was first launched.
func TestCreateTabUsesTheClientEnvironment(t *testing.T) {
	base := testutil.ShortTempDir(t)
	socket := filepath.Join(base, "daemon.sock")
	server, err := daemon.New(socket, filepath.Join(base, "state.json"), "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.SetLogger(testutil.QuietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() { cancel(); <-done }()
	backend := client.New(socket)
	testutil.WaitForDaemon(t, backend)

	snapshot, err := backend.AddRoot(base)
	if err != nil {
		t.Fatalf("AddRoot() error = %v", err)
	}
	workspace, err := backend.EnsureWorkspace(snapshot.Roots[0].Root.ID, base)
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}

	// Set after the daemon started, the way a user's PATH changes under a
	// daemon that has been running for days.
	t.Setenv("ROMTY_AUDIT_MARKER", "from-the-client")
	tab, err := backend.CreateTab(workspace.ID, 80, 24)
	if err != nil {
		t.Fatalf("CreateTab() error = %v", err)
	}
	connection, reader, err := backend.OpenAttach(tab.ID)
	if err != nil {
		t.Fatalf("OpenAttach() error = %v", err)
	}
	defer connection.Close()
	writeCommand(t, connection, `printf 'marker=%s\n' "$ROMTY_AUDIT_MARKER"`)
	readUntil(t, connection, reader, "marker=from-the-client")
}

func TestServerAcceptsAnOlderSupportedProtocol(t *testing.T) {
	socket, cancel, done := serveForTest(t)
	defer func() { cancel(); <-done }()

	response := speakRaw(t, socket, map[string]any{
		"action":  "snapshot",
		"version": protocol.MinimumVersion,
	})
	if response.Error != "" || response.Snapshot == nil {
		t.Fatalf("supported protocol response = %#v, want a snapshot", response)
	}
	if response.Version != protocol.MinimumVersion {
		t.Fatalf("response version = %d, want selected version %d", response.Version, protocol.MinimumVersion)
	}
}

// A version outside the supported range is refused before dispatch, so a
// future client's mutation cannot happen under semantics this daemon does not
// understand.
func TestServerRefusesAClientOutsideItsProtocolRange(t *testing.T) {
	socket, cancel, done := serveForTest(t)
	defer func() { cancel(); <-done }()

	response := speakRaw(t, socket, map[string]any{"action": "snapshot", "version": protocol.Version + 1})
	if response.Error == "" {
		t.Fatal("snapshot from a mismatched client was accepted")
	}
	for _, want := range []string{"protocol", fmt.Sprintf("selected %d", protocol.Version+1), protocol.Remedy} {
		if !strings.Contains(response.Error, want) {
			t.Fatalf("error = %q, want it to mention %q", response.Error, want)
		}
	}
	if response.Snapshot != nil {
		t.Fatal("a refused request still returned a snapshot")
	}
	// Stamped with what the client selected, so the client's own version check
	// does not refuse the reply that is reporting the mismatch.
	if response.Version != protocol.Version+1 {
		t.Fatalf("refusal version = %d, want the request version %d", response.Version, protocol.Version+1)
	}
}

// The capability gate is the only thing this protocol adds that can newly
// refuse a request an older client used to make, so both of its answers are
// checked against the shape a released client actually sends: a stamped
// version, no range and no capability list.
func TestServerInfersCapabilitiesForAReleasedClient(t *testing.T) {
	socket, cancel, done := serveForTest(t)
	defer func() { cancel(); <-done }()

	removal := speakRaw(t, socket, map[string]any{
		"action":  "remove_workspace",
		"version": 4,
		"root_id": "root-1",
		"path":    "workspace",
	})
	if strings.Contains(removal.Error, "requires capability") {
		t.Fatalf("remove_workspace from a protocol 4 client = %q, want its capability inferred", removal.Error)
	}
	if removal.Version != 4 {
		t.Fatalf("response version = %d, want the request version 4", removal.Version)
	}

	agents := speakRaw(t, socket, map[string]any{"action": "agents", "version": 2})
	if agents.Error != "" {
		t.Fatalf("agents from a protocol 2 client = %q, want it carried out", agents.Error)
	}
}

// A client that negotiated a revision from before a feature says so in its
// capability list, and the daemon refuses rather than answering with a field
// that revision has no meaning for.
func TestServerRefusesAnActionTheSelectedProtocolPredates(t *testing.T) {
	socket, cancel, done := serveForTest(t)
	defer func() { cancel(); <-done }()

	for _, testCase := range []struct {
		action     string
		version    int
		capability string
	}{
		{action: "agents", version: 1, capability: protocol.CapabilityAgents},
		{action: "remove_workspace", version: 3, capability: protocol.CapabilityRemoveWorkspace},
		{action: "agent_statuses", version: 4, capability: protocol.CapabilityAgentStatus},
		{action: "close_tab", version: 5, capability: protocol.CapabilityCloseTab},
	} {
		response := speakRaw(t, socket, map[string]any{
			"action":       testCase.action,
			"version":      testCase.version,
			"min_version":  protocol.MinimumVersion,
			"capabilities": protocol.CapabilitiesForVersion(testCase.version),
		})
		if !strings.Contains(response.Error, testCase.capability) ||
			!strings.Contains(response.Error, protocol.Remedy) {
			t.Fatalf("%s at protocol %d error = %q, want it to name %q and the remedy",
				testCase.action, testCase.version, response.Error, testCase.capability)
		}
		if response.Version != testCase.version {
			t.Fatalf("refusal version = %d, want the request version %d", response.Version, testCase.version)
		}
	}
}

func TestServerPingAdvertisesItsProtocolRangeAndCapabilities(t *testing.T) {
	socket, cancel, done := serveForTest(t)
	defer func() { cancel(); <-done }()

	response := speakRaw(t, socket, map[string]any{"action": "ping"})
	if response.Version != 0 || response.MinVersion != protocol.MinimumVersion || response.MaxVersion != protocol.Version {
		t.Fatalf("protocol range = %d..%d, want %d..%d",
			response.MinVersion, response.MaxVersion, protocol.MinimumVersion, protocol.Version)
	}
	for _, capability := range protocol.CapabilitiesForVersion(protocol.Version) {
		if !protocol.HasCapability(response.Capabilities, capability) {
			t.Fatalf("ping capabilities = %q, want %q", response.Capabilities, capability)
		}
	}
	legacy := speakRaw(t, socket, map[string]any{
		"action":  "ping",
		"version": protocol.MinimumVersion,
	})
	if legacy.Version != protocol.MinimumVersion {
		t.Fatalf("legacy ping response version = %d, want request version %d",
			legacy.Version, protocol.MinimumVersion)
	}
}

// Ping and shutdown are the two the check must let through: ping is how the
// client decides whether a daemon is running at all, and shutdown is the
// remedy the mismatch message names.
func TestServerAnswersPingAndShutdownRegardlessOfVersion(t *testing.T) {
	socket, cancel, done := serveForTest(t)
	defer cancel()

	if response := speakRaw(t, socket, map[string]any{"action": "ping"}); response.Error != "" {
		t.Fatalf("unversioned ping error = %q, want it answered", response.Error)
	}
	if response := speakRaw(t, socket, map[string]any{"action": "shutdown"}); response.Error != "" {
		t.Fatalf("unversioned shutdown error = %q, want it answered", response.Error)
	}
	// The shutdown was accepted, so the daemon stops on its own; waiting on
	// Serve is what proves the request reached past the version check.
	if err := <-done; err != nil {
		t.Fatalf("Serve() after an unversioned shutdown error = %v", err)
	}
}

// Attach built its request inline and sent it unversioned, so the daemon saw
// every attach as coming from a client that predates the field.
func TestAttachSendsTheProtocolVersion(t *testing.T) {
	socket, cancel, done := serveForTest(t)
	defer func() { cancel(); <-done }()

	backend := client.New(socket)
	testutil.WaitForDaemon(t, backend)
	root := t.TempDir()
	if _, err := backend.AddRoot(root); err != nil {
		t.Fatalf("AddRoot() error = %v", err)
	}
	snapshot, err := backend.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	workspace, err := backend.EnsureWorkspace(snapshot.Roots[0].Root.ID, root)
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	tab, err := backend.CreateTab(workspace.ID, 80, 24)
	if err != nil {
		t.Fatalf("CreateTab() error = %v", err)
	}
	stream, _, err := backend.OpenTerminal(tab.ID)
	if err != nil {
		t.Fatalf("OpenTerminal() error = %v", err)
	}
	if sized, ok := stream.(interface{ ReplaySize() (uint16, uint16) }); !ok {
		t.Fatal("terminal stream has no replay size")
	} else if columns, rows := sized.ReplaySize(); columns != 80 || rows != 24 {
		t.Fatalf("replay size = %dx%d, want 80x24", columns, rows)
	}
	stream.Close()
}

// serveForTest starts a daemon on a private socket and returns the socket, the
// cancel that stops it, and the channel carrying Serve's result.
func serveForTest(t *testing.T) (string, context.CancelFunc, chan error) {
	t.Helper()
	base := testutil.ShortTempDir(t)
	socket := filepath.Join(base, "daemon.sock")
	server, err := daemon.New(socket, filepath.Join(base, "state.json"), "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.SetLogger(testutil.QuietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	return socket, cancel, done
}

// speakRaw sends one hand-built request, so a test can say a version the
// client package would never send.
func speakRaw(t *testing.T, socket string, request map[string]any) protocol.Response {
	t.Helper()
	testutil.WaitForDaemon(t, client.New(socket))
	connection, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer connection.Close()
	if err := protocol.Write(connection, request); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	var response protocol.Response
	if err := protocol.Read(bufio.NewReader(connection), &response); err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	return response
}

// diagnoseShellStart reports what a failed shell start had to work with. It is
// temporary: it exists to name the cause of an exec failure that only appears
// on the Linux CI runner.
func diagnoseShellStart(directory string) string {
	var report strings.Builder
	fmt.Fprintf(&report, "  uid=%d gid=%d SHELL=%q TMPDIR=%q\n",
		os.Getuid(), os.Getgid(), os.Getenv("SHELL"), os.Getenv("TMPDIR"))
	for _, candidate := range []string{"/bin/bash", "/usr/bin/bash", "/bin/sh"} {
		info, err := os.Stat(candidate)
		if err != nil {
			fmt.Fprintf(&report, "  %s: %v\n", candidate, err)
			continue
		}
		fmt.Fprintf(&report, "  %s: mode=%v size=%d\n", candidate, info.Mode(), info.Size())
	}
	for path := directory; ; path = filepath.Dir(path) {
		info, err := os.Stat(path)
		if err != nil {
			fmt.Fprintf(&report, "  %s: %v\n", path, err)
		} else {
			fmt.Fprintf(&report, "  %s: mode=%v\n", path, info.Mode())
		}
		if path == "/" || filepath.Dir(path) == path {
			break
		}
	}
	command := exec.Command("/bin/bash", "-c", "exit 0")
	command.Dir = directory
	fmt.Fprintf(&report, "  retry /bin/bash in place: %v\n", command.Run())
	return report.String()
}
