package daemon_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nalbam/romty/internal/client"
	"github.com/nalbam/romty/internal/daemon"
	"github.com/nalbam/romty/internal/model"
	"github.com/nalbam/romty/internal/state"
)

func TestServeRemovesPersistedTerminalTabs(t *testing.T) {
	base := shortTempDir(t)
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
	waitForDaemon(t, client.New(socket))
	persisted, err = store.Load()
	if err != nil {
		t.Fatalf("Load() after Serve error = %v", err)
	}
	if len(persisted.Tabs) != 0 {
		t.Fatalf("persisted tabs = %#v, want stale tabs removed", persisted.Tabs)
	}
}

func TestServerDiscoversWorkspacesAndReattachesSession(t *testing.T) {
	base := shortTempDir(t)
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
	waitForDaemon(t, backend)
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
	writeCommand(t, firstConnection, "printf 'romty-first-marker\\n'")
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
	writeCommand(t, secondConnection, "printf 'romty-second-marker\\n'")
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
	writeCommand(t, thirdConnection, "printf 'romty-third-marker\\n'")
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
	base := shortTempDir(t)
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
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	backend := client.New(socket)
	waitForDaemon(t, backend)
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

func TestEnsureWorkspaceRejectsNestedDirectory(t *testing.T) {
	base := shortTempDir(t)
	root := filepath.Join(base, "projects")
	nested := filepath.Join(root, "alpha", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	server, err := daemon.New(filepath.Join(base, "daemon.sock"), filepath.Join(base, "state.json"), "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	backend := client.New(filepath.Join(base, "daemon.sock"))
	waitForDaemon(t, backend)
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
	base := shortTempDir(t)
	root := filepath.Join(base, "projects")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	socket := filepath.Join(base, "daemon.sock")
	server, err := daemon.New(socket, filepath.Join(base, "state.json"), "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()

	backend := client.New(socket)
	waitForDaemon(t, backend)
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

func waitForDaemon(t *testing.T, backend *client.Client) {
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

func shortTempDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "romty-test-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	return directory
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
