package daemon_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/opspresso/romty/internal/client"
	"github.com/opspresso/romty/internal/daemon"
	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/paths"
	"github.com/opspresso/romty/internal/protocol"
	"github.com/opspresso/romty/internal/state"
	"github.com/opspresso/romty/internal/testutil"
	"github.com/opspresso/romty/internal/version"
)

func TestServeRecoversRemovedSocketWithoutLosingSession(t *testing.T) {
	d := startDaemon(t)
	backend := d.connect(t)
	root := t.TempDir()
	_, workspace := openWorkspace(t, backend, root, root)
	tab := createTab(t, backend, workspace)
	connection, reader := attach(t, backend, tab)
	defer connection.Close()
	writeCommand(t, connection, "saved_value=preserved; printf 'before-%s\\n' unlink")
	readUntil(t, connection, reader, "before-unlink")

	if err := os.Remove(d.socket); err != nil {
		t.Fatalf("remove live socket: %v", err)
	}
	// An unlinked listener still owns its lock and existing streams. A new
	// daemon cannot take over; the same daemon must restore its address.
	runtime := paths.Paths{Directory: filepath.Dir(d.socket), Socket: d.socket}
	if err := client.EnsureDaemon(runtime, "/nonexistent/romty"); err != nil {
		t.Fatalf("EnsureDaemon() during socket recovery: %v", err)
	}
	info, err := os.Stat(d.socket)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("restored socket permissions: info=%v, error=%v", info, err)
	}
	writeCommand(t, connection, "printf 'existing-%s\\n' \"$saved_value\"")
	readUntil(t, connection, reader, "existing-preserved")
	reconnected, replay := attach(t, client.New(d.socket), tab)
	defer reconnected.Close()
	readUntil(t, reconnected, replay, "before-unlink")
	writeCommand(t, reconnected, "printf 'reconnected-%s\\n' \"$saved_value\"")
	readUntil(t, reconnected, replay, "reconnected-preserved")
	if err := backend.Shutdown(); err != nil {
		t.Fatalf("Shutdown() after recovery: %v", err)
	}
}

func TestServeRemovesPersistedTerminalTabs(t *testing.T) {
	base := testutil.ShortTempDir(t)
	socket := filepath.Join(base, "daemon.sock")
	statePath := filepath.Join(base, "state.json")
	store := state.New(statePath)
	if err := store.Save(model.State{Tabs: []model.Tab{{ID: "stale-tab", Running: true}}}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	server := newQuietDaemon(t, socket, statePath, "/bin/sh")
	persisted, err := store.Load()
	if err != nil {
		t.Fatalf("Load() before Serve error = %v", err)
	}
	if len(persisted.Tabs) != 1 {
		t.Fatalf("persisted tabs before Serve = %#v, want tabs unchanged", persisted.Tabs)
	}

	serveDaemon(t, server, socket).connect(t)
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
	if err := os.Mkdir(filepath.Join(root, ".hidden"), 0o755); err != nil {
		t.Fatalf("Mkdir() hidden error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("not a workspace"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(secondRoot, "gamma"), 0o755); err != nil {
		t.Fatalf("MkdirAll() second root error = %v", err)
	}

	socket := filepath.Join(base, "daemon.sock")
	statePath := filepath.Join(base, "state.json")
	backend := startDaemonAt(t, socket, statePath, "/bin/sh").connect(t)
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

	firstConnection, firstReader := attach(t, backend, tab)
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
	backend := startDaemonAt(t, socket, statePath, "/bin/sh").connect(t)
	_, workspace := openWorkspace(t, backend, root, root)
	tab := createTab(t, backend, workspace)
	connection, reader := attach(t, backend, tab)

	writeCommand(t, connection, "pwd")
	readUntil(t, connection, reader, root)
	writeCommand(t, connection, "mkdir cloned")
	snapshot := waitForRootDirectoryCount(t, backend, 1)
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
	backend := startDaemonAt(t, socket, filepath.Join(base, "state.json"), "/bin/sh").connect(t)
	rootID, workspace := openWorkspace(t, backend, root, workspacePath)
	tab := createTab(t, backend, workspace)

	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("RemoveAll() error = %v", err)
	}
	snapshot, err := backend.Snapshot()
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
	connection, reader := attach(t, backend, tab)
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

	backend := startDaemonAt(t, filepath.Join(base, "daemon.sock"), filepath.Join(base, "state.json"), "/bin/sh").connect(t)
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
	served := startDaemonAt(t, filepath.Join(base, "daemon.sock"), filepath.Join(base, "state.json"), "/bin/sh")
	backend := served.connect(t)
	_, workspace := openWorkspace(t, backend, root, root)
	connection, reader := attach(t, backend, createTab(t, backend, workspace))

	if err := backend.Shutdown(); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := served.wait(t); err != nil {
		t.Fatalf("Serve() after Shutdown() error = %v", err)
	}
	if err := backend.Ping(); err == nil {
		t.Fatal("Ping() after Shutdown() error = nil")
	}
	expectTerminalClosed(t, connection, reader)
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

// A daemon shares its process with whatever else runs in it: a TUI that starts
// one in development, and the whole of this suite under test. The umask is
// process-wide, so binding the socket must not change it: a directory created
// meanwhile would lose its execute bit and could not be a shell's working
// directory.
func TestServeLeavesTheModesOfConcurrentDirectoriesAlone(t *testing.T) {
	previous := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(previous) })
	base := testutil.ShortTempDir(t)
	served := startDaemonAt(t, filepath.Join(base, "daemon.sock"), filepath.Join(base, "state.json"), "/bin/sh")

	for index := range 200 {
		directory := filepath.Join(base, fmt.Sprintf("workspace-%d", index))
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}
		info, err := os.Stat(directory)
		if err != nil {
			t.Fatalf("Stat() error = %v", err)
		}
		if mode := info.Mode().Perm(); mode != 0o755 {
			t.Fatalf("directory %d mode = %04o, want 0755", index, mode)
		}
	}
	served.connect(t)
	if err := served.stop(t); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
}

func TestCloseTabTerminatesOnlyTheSelectedSession(t *testing.T) {
	socket := startDaemon(t).socket
	root := t.TempDir()
	workspacePath := filepath.Join(root, "alpha")
	if err := os.Mkdir(workspacePath, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	backend := client.New(socket)
	testutil.WaitForDaemon(t, backend)
	_, workspace := openWorkspace(t, backend, root, workspacePath)
	first := createTab(t, backend, workspace)
	second := createTab(t, backend, workspace)

	snapshot, err := backend.CloseTab(first.ID)
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
	backend := startDaemonAt(t, socket, filepath.Join(base, "state.json"), "/bin/sh").connect(t)

	_, workspace := openWorkspace(t, backend, base, base)
	tab := createTab(t, backend, workspace)

	first, reader := attach(t, backend, tab)
	// The shell echoes the command line, so the marker is split in the source
	// and only whole in the output: waiting on the echo would race the history.
	writeCommand(t, first, `printf 'MARK\033[6n\033[c\033]11;?\033\\END'; echo SETT''LED`)
	readUntil(t, first, reader, "SETTLED")
	first.Close()

	second, secondReader := attach(t, backend, tab)
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
// One such root must not fail the snapshot, which every path including startup
// needs.
func TestSnapshotSurvivesAnUnreadableRoot(t *testing.T) {
	base := testutil.ShortTempDir(t)
	socket := filepath.Join(base, "daemon.sock")
	backend := startDaemonAt(t, socket, filepath.Join(base, "state.json"), "/bin/sh").connect(t)

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

	// And it can be forgotten from the TUI.
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
	backend := startDaemonAt(t, socket, filepath.Join(base, "state.json"), "/bin/sh").connect(t)
	rootID, workspace := openWorkspace(t, backend, root, root)
	tab := createTab(t, backend, workspace)
	connection, reader := attach(t, backend, tab)
	secondTab := createTab(t, backend, workspace)
	secondConnection, secondReader := attach(t, backend, secondTab)

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
	backend := startDaemonAt(t, socket, statePath, "/bin/sh").connect(t)
	rootID, workspace := openWorkspace(t, backend, root, workspacePath)
	tab := createTab(t, backend, workspace)
	connection, reader := attach(t, backend, tab)
	siblingWorkspace, err := backend.EnsureWorkspace(rootID, siblingPath)
	if err != nil {
		t.Fatalf("EnsureWorkspace() sibling error = %v", err)
	}
	siblingTab := createTab(t, backend, siblingWorkspace)
	siblingConnection, siblingReader := attach(t, backend, siblingTab)

	// The daemon records canonical paths, and a workspace is a direct child.
	canonicalRoot := filepath.Dir(workspace.Path)
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

	snapshot, err := backend.RemoveWorkspace(rootID, workspace.Path)
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
	startDaemonAt(t, socket, filepath.Join(home, "state.json"), "/bin/sh").connect(t)

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
	backend := startDaemonAt(t, socket, filepath.Join(base, "state.json"), "/usr/bin/true").connect(t)

	_, workspace := openWorkspace(t, backend, base, base)
	for range 5 {
		if _, err := backend.CreateTab(workspace.ID, 80, 24); err != nil {
			t.Fatalf("CreateTab() error = %v", err)
		}
	}

	remaining := -1
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := backend.Snapshot()
		if err != nil {
			t.Fatalf("Snapshot() error = %v", err)
		}
		if remaining = countTabs(snapshot); remaining == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%d tabs outlived their shells", remaining)
}

func countTabs(snapshot model.Snapshot) int {
	total := 0
	for range snapshot.Tabs() {
		total++
	}
	return total
}

// The daemon can be days older than the shell the user is working in, so a tab
// must start with that shell's environment rather than whatever the daemon
// inherited when it was first launched.
func TestCreateTabUsesTheClientEnvironment(t *testing.T) {
	base := testutil.ShortTempDir(t)
	socket := filepath.Join(base, "daemon.sock")
	backend := startDaemonAt(t, socket, filepath.Join(base, "state.json"), "/bin/sh").connect(t)

	_, workspace := openWorkspace(t, backend, base, base)

	// Set after the daemon started, the way a user's PATH changes under a
	// daemon that has been running for days.
	t.Setenv("ROMTY_AUDIT_MARKER", "from-the-client")
	tab := createTab(t, backend, workspace)
	connection, reader := attach(t, backend, tab)
	writeCommand(t, connection, `printf 'marker=%s\n' "$ROMTY_AUDIT_MARKER"`)
	readUntil(t, connection, reader, "marker=from-the-client")
}

func TestServerAcceptsAnOlderSupportedProtocol(t *testing.T) {
	socket := startDaemon(t).socket

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
	socket := startDaemon(t).socket

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
	socket := startDaemon(t).socket

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
	socket := startDaemon(t).socket

	for _, testCase := range []struct {
		action     string
		version    int
		capability string
	}{
		{action: "agents", version: 1, capability: protocol.CapabilityAgents},
		{action: "remove_workspace", version: 3, capability: protocol.CapabilityRemoveWorkspace},
		{action: "agent_statuses", version: 4, capability: protocol.CapabilityAgentStatus},
		{action: "close_tab", version: 5, capability: protocol.CapabilityCloseTab},
		{action: "rename_tab", version: 6, capability: protocol.CapabilityRenameTab},
		{action: "reopen_tab", version: 7, capability: protocol.CapabilityReopenTab},
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
	socket := startDaemon(t).socket

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
	served := startDaemon(t)
	socket := served.socket

	if response := speakRaw(t, socket, map[string]any{"action": "ping"}); response.Error != "" {
		t.Fatalf("unversioned ping error = %q, want it answered", response.Error)
	}
	if response := speakRaw(t, socket, map[string]any{"action": "shutdown"}); response.Error != "" {
		t.Fatalf("unversioned shutdown error = %q, want it answered", response.Error)
	}
	// The shutdown was accepted, so the daemon stops on its own; waiting on
	// Serve is what proves the request reached past the version check.
	if err := served.wait(t); err != nil {
		t.Fatalf("Serve() after an unversioned shutdown error = %v", err)
	}
}

// Attach is sent with the negotiated version and capabilities, so the daemon
// announces the replay boundary and size rather than treating it as a client
// that predates them.
func TestAttachSendsTheProtocolVersion(t *testing.T) {
	socket := startDaemon(t).socket

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
	tab := createTab(t, backend, workspace)
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

// testDaemon is a daemon serving on its own goroutine for one test. The test
// stops it at cleanup unless the test stopped it first.
type testDaemon struct {
	socket string
	cancel context.CancelFunc
	done   chan error
	once   sync.Once
	err    error
}

// newQuietDaemon builds a daemon on socket and statePath that logs nowhere.
// TestMain runs every test tab in /bin/sh. CreateTab sends the caller's
// $SHELL, which outranks the shell a test daemon is given, and a developer's
// interactive shell loads dotfiles: a prompt or a terminal wrapper such as
// Kiro CLI's kiro-cli-term then changes the output the tests read.
func TestMain(m *testing.M) {
	os.Setenv("SHELL", "/bin/sh")
	os.Unsetenv("ENV")
	os.Exit(m.Run())
}

func newQuietDaemon(t *testing.T, socket, statePath, shell string) *daemon.Server {
	t.Helper()
	server, err := daemon.New(socket, statePath, shell)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.SetLogger(testutil.QuietLogger())
	return server
}

// serveDaemon serves server until the test stops it or ends.
func serveDaemon(t *testing.T, server *daemon.Server, socket string) *testDaemon {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	value := &testDaemon{socket: socket, cancel: cancel, done: make(chan error, 1)}
	go func() { value.done <- server.Serve(ctx) }()
	t.Cleanup(func() {
		if err := value.stop(t); err != nil {
			t.Errorf("Serve() error = %v", err)
		}
	})
	return value
}

// startDaemonAt builds and serves a daemon on socket and statePath with shell.
func startDaemonAt(t *testing.T, socket, statePath, shell string) *testDaemon {
	t.Helper()
	return serveDaemon(t, newQuietDaemon(t, socket, statePath, shell), socket)
}

// startDaemon serves a daemon in a new private romty home.
func startDaemon(t *testing.T) *testDaemon {
	t.Helper()
	base := testutil.ShortTempDir(t)
	return startDaemonAt(t, filepath.Join(base, "daemon.sock"), filepath.Join(base, "state.json"), "/bin/sh")
}

// connect returns a client once the daemon answers it.
func (d *testDaemon) connect(t *testing.T) *client.Client {
	t.Helper()
	backend := client.New(d.socket)
	testutil.WaitForDaemon(t, backend)
	return backend
}

// wait returns what Serve returned, or an error when it does not return soon.
func (d *testDaemon) wait(t *testing.T) error {
	t.Helper()
	d.once.Do(func() {
		select {
		case d.err = <-d.done:
		case <-time.After(3 * time.Second):
			d.err = errors.New("Serve() did not stop")
		}
	})
	return d.err
}

// stop cancels Serve and returns what it returned.
func (d *testDaemon) stop(t *testing.T) error {
	t.Helper()
	d.cancel()
	return d.wait(t)
}

// openWorkspace adds root and registers path inside it as a workspace.
func openWorkspace(t *testing.T, backend *client.Client, root, path string) (string, model.Workspace) {
	t.Helper()
	snapshot, err := backend.AddRoot(root)
	if err != nil {
		t.Fatalf("AddRoot() error = %v", err)
	}
	rootID := snapshot.Roots[len(snapshot.Roots)-1].Root.ID
	workspace, err := backend.EnsureWorkspace(rootID, path)
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	return rootID, workspace
}

// createTab starts an 80x24 terminal in workspace.
func createTab(t *testing.T, backend *client.Client, workspace model.Workspace) model.Tab {
	t.Helper()
	tab, err := backend.CreateTab(workspace.ID, 80, 24)
	if err != nil {
		t.Fatalf("CreateTab() error = %v", err)
	}
	return tab
}

// attach opens tab's terminal stream until the test ends.
func attach(t *testing.T, backend *client.Client, tab model.Tab) (net.Conn, *bufio.Reader) {
	t.Helper()
	connection, reader, err := backend.OpenAttach(tab.ID)
	if err != nil {
		t.Fatalf("OpenAttach(%s) error = %v", tab.ID, err)
	}
	t.Cleanup(func() { connection.Close() })
	return connection, reader
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

// A workspace whose directory has already gone must still be removable. The
// removal that deletes the directory rolls its state back when the state
// cannot be written, which leaves romty holding a record for a path that is no
// longer there. A retry has to forget that record rather than fail on the
// missing directory.
func TestRemoveWorkspaceForgetsADirectoryThatIsAlreadyGone(t *testing.T) {
	base := testutil.ShortTempDir(t)
	root := filepath.Join(base, "projects")
	workspacePath := filepath.Join(root, "alpha")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	socket := filepath.Join(base, "daemon.sock")
	backend := startDaemonAt(t, socket, filepath.Join(base, "state.json"), "/bin/sh").connect(t)
	rootID, workspace := openWorkspace(t, backend, root, workspacePath)
	createTab(t, backend, workspace)

	// The directory goes without romty being the one to remove it, which is
	// the state a rolled-back removal leaves behind. The canonical path is
	// what the daemon recorded and what a removal has to name.
	canonical := workspace.Path
	if err := os.RemoveAll(canonical); err != nil {
		t.Fatalf("RemoveAll() error = %v", err)
	}

	snapshot, err := backend.RemoveWorkspace(rootID, canonical)
	if err != nil {
		t.Fatalf("RemoveWorkspace() on a missing directory error = %v", err)
	}
	for _, directory := range snapshot.Roots[0].Directories {
		if directory.Workspace.Path == canonical {
			t.Fatalf("the workspace is still in the tree: %#v", directory)
		}
	}
	if countTabs(snapshot) != 0 {
		t.Fatalf("tabs survived the removal: %#v", snapshot)
	}

	// The boundary still holds: a path outside the root is refused whether or
	// not it exists.
	if _, err := backend.RemoveWorkspace(rootID, filepath.Join(base, "outside")); err == nil {
		t.Fatal("RemoveWorkspace() accepted a path outside the root")
	}
}

// `romty stop` is for upgrades, and what it must not cost is the work in
// flight: the stopping daemon saves each running tab's recording and agent
// identity, and the next daemon hands both back to the first tab created in
// that workspace — the old output replayed behind a marker, and the agent's
// resume command already typed at the prompt.
func TestStopSavesSessionsAndTheNextDaemonRestoresThem(t *testing.T) {
	base := testutil.ShortTempDir(t)
	socket := filepath.Join(base, "daemon.sock")
	statePath := filepath.Join(base, "state.json")
	workspacePath := filepath.Join(base, "alpha")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	first := startDaemonAt(t, socket, statePath, "/bin/sh")
	backend := first.connect(t)
	_, workspace := openWorkspace(t, backend, base, workspacePath)
	tab := createTab(t, backend, workspace)
	const sessionID = "0123abcd-89ef-4567-0123-456789abcdef"
	connection, reader := attach(t, backend, tab)
	writeCommand(t, connection, `echo RESUME''PROOF`)
	readUntil(t, connection, reader, "RESUMEPROOF")
	// Only an agent that still runs is resumed, so one stays in the
	// foreground: a copy of sleep, named the way romty recognises Claude Code.
	writeCommand(t, connection, fakeAgent(t, base, "claude")+" 60")
	waitForAgent(t, backend, tab.ID, model.AgentClaude)
	if err := backend.ReportAgentEvent(tab.ID, protocol.AgentEvent{
		Agent: model.AgentClaude, HookEvent: "SessionStart", SessionID: sessionID,
	}); err != nil {
		t.Fatalf("ReportAgentEvent() error = %v", err)
	}
	connection.Close()
	if err := first.stop(t); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	backend = startDaemonAt(t, socket, statePath, "/bin/sh").connect(t)
	restored := createTab(t, backend, workspace)
	connection, reader, err := backend.OpenAttach(restored.ID)
	if err != nil {
		t.Fatalf("OpenAttach() after restart error = %v", err)
	}
	defer connection.Close()
	// The replay carries the saved output, then the marker, and the fresh
	// shell echoes the pre-typed resume command when it reads its input. One
	// accumulating read, because a single chunk can carry all three: reading
	// until each in turn would swallow the next with the current one.
	if err := connection.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	var restoredOutput strings.Builder
	buffer := make([]byte, 4096)
	for !strings.Contains(restoredOutput.String(), "claude --resume "+sessionID) {
		count, err := reader.Read(buffer)
		if err != nil {
			t.Fatalf("read restored output: %v; output = %q", err, restoredOutput.String())
		}
		restoredOutput.Write(buffer[:count])
	}
	output := restoredOutput.String()
	proof := strings.Index(output, "RESUMEPROOF")
	marker := strings.Index(output, "restored from the previous romty session")
	if proof < 0 || marker < proof {
		t.Fatalf("restored output out of order (proof %d, marker %d):\n%q", proof, marker, output)
	}

	third := createTab(t, backend, workspace)
	thirdConnection, thirdReader := attach(t, backend, third)
	writeCommand(t, thirdConnection, `echo FRESH''TAB`)
	readUntil(t, thirdConnection, thirdReader, "FRESHTAB")
	if _, err := os.Stat(filepath.Join(base, "resume", tab.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("consumed snapshot still on disk: %v", err)
	}
}

// A tab whose agent has exited has no session to resume, even when its hook
// reported one and no SessionEnd followed.
func TestStopDoesNotResumeAnAgentThatExited(t *testing.T) {
	base := testutil.ShortTempDir(t)
	socket := filepath.Join(base, "daemon.sock")
	statePath := filepath.Join(base, "state.json")

	first := startDaemonAt(t, socket, statePath, "/bin/sh")
	backend := first.connect(t)
	_, workspace := openWorkspace(t, backend, base, base)
	tab := createTab(t, backend, workspace)
	connection, reader := attach(t, backend, tab)
	writeCommand(t, connection, fakeAgent(t, base, "claude")+" 1; echo AGENT''GONE")
	waitForAgent(t, backend, tab.ID, model.AgentClaude)
	if err := backend.ReportAgentEvent(tab.ID, protocol.AgentEvent{
		Agent: model.AgentClaude, HookEvent: "Stop", SessionID: "0123abcd-89ef-4567-0123-456789abcdef",
	}); err != nil {
		t.Fatalf("ReportAgentEvent() error = %v", err)
	}
	readUntil(t, connection, reader, "AGENTGONE")
	connection.Close()
	if err := first.stop(t); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	meta, err := os.ReadFile(filepath.Join(base, "resume", tab.ID+".json"))
	if err != nil {
		t.Fatalf("read saved session: %v", err)
	}
	if strings.Contains(string(meta), "claude") {
		t.Fatalf("saved session names the exited agent: %s", meta)
	}
}

// Closing a tab keeps its output and agent session in the closed history,
// newest first and at most three per workspace. Reopening one
// replays the output, types the agent's resume command, takes back the tab
// name, and consumes the entry.
func TestClosedTabCanBeReopenedWithItsOutputAndAgent(t *testing.T) {
	base := testutil.ShortTempDir(t)
	workspacePath := filepath.Join(base, "alpha")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	backend := startDaemonAt(t, filepath.Join(base, "daemon.sock"), filepath.Join(base, "state.json"), "/bin/sh").connect(t)
	_, workspace := openWorkspace(t, backend, base, workspacePath)
	const closedHistoryLimit = 3

	// Three plain tabs closed first, then the agent tab: the oldest plain one
	// falls out of the history.
	for range closedHistoryLimit {
		tab := createTab(t, backend, workspace)
		if _, err := backend.CloseTab(tab.ID); err != nil {
			t.Fatalf("CloseTab() error = %v", err)
		}
	}
	tab := createTab(t, backend, workspace)
	const sessionID = "0123abcd-89ef-4567-0123-456789abcdef"
	connection, reader := attach(t, backend, tab)
	writeCommand(t, connection, `echo CLOSED''PROOF`)
	readUntil(t, connection, reader, "CLOSEDPROOF")
	writeCommand(t, connection, fakeAgent(t, base, "claude")+" 60")
	waitForAgent(t, backend, tab.ID, model.AgentClaude)
	if err := backend.ReportAgentEvent(tab.ID, protocol.AgentEvent{
		Agent: model.AgentClaude, HookEvent: "SessionStart", SessionID: sessionID,
	}); err != nil {
		t.Fatalf("ReportAgentEvent() error = %v", err)
	}
	connection.Close()
	snapshot, err := backend.CloseTab(tab.ID)
	if err != nil {
		t.Fatalf("CloseTab() error = %v", err)
	}
	closed := snapshot.Roots[0].Directories[0].Closed
	if len(closed) != closedHistoryLimit {
		t.Fatalf("closed sessions = %#v, want %d", closed, closedHistoryLimit)
	}
	if closed[0].ID != tab.ID || closed[0].Name != tab.Name || closed[0].Agent != model.AgentClaude {
		t.Fatalf("newest closed session = %#v, want tab %q with claude", closed[0], tab.ID)
	}

	if _, err := backend.ReopenTab(workspace.ID, "../state", 80, 24); err == nil {
		t.Fatal("ReopenTab() accepted an ID that is not a closed session")
	}
	reopened, err := backend.ReopenTab(workspace.ID, tab.ID, 80, 24)
	if err != nil {
		t.Fatalf("ReopenTab() error = %v", err)
	}
	if reopened.Name != tab.Name {
		t.Fatalf("reopened tab name = %q, want %q", reopened.Name, tab.Name)
	}
	connection, reader, err = backend.OpenAttach(reopened.ID)
	if err != nil {
		t.Fatalf("OpenAttach() error = %v", err)
	}
	defer connection.Close()
	if err := connection.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	var output strings.Builder
	buffer := make([]byte, 4096)
	for !strings.Contains(output.String(), "claude --resume "+sessionID) {
		count, err := reader.Read(buffer)
		if err != nil {
			t.Fatalf("read reopened output: %v; output = %q", err, output.String())
		}
		output.Write(buffer[:count])
	}
	proof := strings.Index(output.String(), "CLOSEDPROOF")
	marker := strings.Index(output.String(), "reopened from a closed tab")
	if proof < 0 || marker < proof {
		t.Fatalf("reopened output out of order (proof %d, marker %d): %q", proof, marker, output.String())
	}

	snapshot, err = backend.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	for _, value := range snapshot.Roots[0].Directories[0].Closed {
		if value.ID == tab.ID {
			t.Fatal("a reopened session is still in the closed history")
		}
	}
	if _, err := backend.ReopenTab(workspace.ID, tab.ID, 80, 24); err == nil {
		t.Fatal("ReopenTab() reopened the same session twice")
	}
}

// fakeAgent links sleep into directory under name, so a test can run a
// process that romty recognises as that agent: the process list shows the
// link's name. A link rather than a copy, because macOS kills a copied system
// binary.
func fakeAgent(t *testing.T, directory, name string) string {
	t.Helper()
	source, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatalf("LookPath(sleep) error = %v", err)
	}
	path := filepath.Join(directory, name)
	if err := os.Symlink(source, path); err != nil {
		t.Fatalf("link %s: %v", name, err)
	}
	return path
}

// waitForAgent returns once the daemon sees agent in the tab's foreground.
func waitForAgent(t *testing.T, backend *client.Client, tabID string, agent model.Agent) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		statuses, err := backend.AgentStatuses()
		if err == nil && statuses[tabID].Agent == agent {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s never ran in the foreground of tab %s", agent, tabID)
}

func TestRenameTabChangesOnlyTheName(t *testing.T) {
	socket := startDaemon(t).socket
	root := t.TempDir()
	workspacePath := filepath.Join(root, "alpha")
	if err := os.Mkdir(workspacePath, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	backend := client.New(socket)
	testutil.WaitForDaemon(t, backend)
	_, workspace := openWorkspace(t, backend, root, workspacePath)
	tab := createTab(t, backend, workspace)

	snapshot, err := backend.RenameTab(tab.ID, "  server  ")
	if err != nil {
		t.Fatalf("RenameTab() error = %v", err)
	}
	tabs := snapshot.Roots[0].Directories[0].Tabs
	if len(tabs) != 1 || tabs[0].ID != tab.ID || tabs[0].Name != "server" || !tabs[0].Running {
		t.Fatalf("tabs after rename = %#v, want running %q named server", tabs, tab.ID)
	}
	stream, _, err := backend.OpenTerminal(tab.ID)
	if err != nil {
		t.Fatalf("OpenTerminal() renamed tab error = %v", err)
	}
	stream.Close()

	for _, name := range []string{"", "   ", strings.Repeat("x", 33), "a\tb", "name\n", "\tname"} {
		if _, err := backend.RenameTab(tab.ID, name); err == nil {
			t.Fatalf("RenameTab(%q) succeeded, want an error", name)
		}
	}
	if _, err := backend.RenameTab("missing", "name"); err == nil {
		t.Fatal("RenameTab() of an unknown tab succeeded")
	}
	snapshot, err = backend.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if name := snapshot.Roots[0].Directories[0].Tabs[0].Name; name != "server" {
		t.Fatalf("name after rejected renames = %q, want server", name)
	}
}

// move_tab reorders a workspace's tabs, stops at either end, and keeps the
// order in the state file; ping reports the daemon's build.
func TestMoveTabReordersAWorkspacesTabs(t *testing.T) {
	socket := startDaemon(t).socket
	root := t.TempDir()
	workspacePath := filepath.Join(root, "alpha")
	if err := os.Mkdir(workspacePath, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	backend := client.New(socket)
	testutil.WaitForDaemon(t, backend)
	_, workspace := openWorkspace(t, backend, root, workspacePath)
	first := createTab(t, backend, workspace)
	second := createTab(t, backend, workspace)
	third := createTab(t, backend, workspace)
	order := func(snapshot model.Snapshot) []string {
		var names []string
		for tab := range snapshot.Tabs() {
			names = append(names, tab.ID)
		}
		return names
	}
	snapshot, err := backend.MoveTab(third.ID, -1)
	if err != nil {
		t.Fatalf("MoveTab() error = %v", err)
	}
	if got := order(snapshot); !slices.Equal(got, []string{first.ID, third.ID, second.ID}) {
		t.Fatalf("order after moving the third left = %v", got)
	}
	snapshot, err = backend.MoveTab(first.ID, -5)
	if err != nil || !slices.Equal(order(snapshot), []string{first.ID, third.ID, second.ID}) {
		t.Fatalf("moving the first past the start = (%v, %v), want no change", order(snapshot), err)
	}
	info, err := backend.DaemonInfo()
	if err != nil || info.Version != version.String() {
		t.Fatalf("DaemonInfo() = (%+v, %v), want this build", info, err)
	}
}
