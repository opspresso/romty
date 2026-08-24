package ui

import (
	"bytes"
	"errors"
	"fmt"
	"image/color"
	"io"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nalbam/romty/internal/model"
)

type fakeBackend struct {
	snapshot       model.Snapshot
	workspace      model.Workspace
	createdTab     model.Tab
	createdColumns uint16
	createdRows    uint16
	createCount    int
	addedPath      string
	removedRootID  string
	ensuredRootID  string
	ensuredPath    string
	openedTab      string
	snapshotCount  int
	shutdownCount  int
	stream         *memoryStream
	// The real client fails: the daemon can be unreachable, past its
	// deadline, or refuse the request. A fake that only ever succeeds hides
	// every branch that handles those.
	failures map[string]error
}

type memoryStream struct {
	reader  *bytes.Reader
	mu      sync.Mutex
	written bytes.Buffer
}

func newMemoryStream(output string) *memoryStream {
	return &memoryStream{reader: bytes.NewReader([]byte(output))}
}

func (s *memoryStream) Read(data []byte) (int, error) {
	return s.reader.Read(data)
}

func (s *memoryStream) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.written.Write(data)
}

func (s *memoryStream) Close() error {
	return nil
}

func (s *memoryStream) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.written.String()
}

var _ io.ReadWriteCloser = (*memoryStream)(nil)

// fail makes the named method return err until cleared, the way a daemon that
// went away makes every call fail.
func (f *fakeBackend) fail(method string, err error) {
	if f.failures == nil {
		f.failures = make(map[string]error)
	}
	f.failures[method] = err
}

func (f *fakeBackend) failure(method string) error {
	return f.failures[method]
}

func (f *fakeBackend) AddRoot(path string) (model.Snapshot, error) {
	f.addedPath = path
	if err := f.failure("AddRoot"); err != nil {
		return model.Snapshot{}, err
	}
	return f.snapshot, nil
}

func (f *fakeBackend) RemoveRoot(rootID string) (model.Snapshot, error) {
	f.removedRootID = rootID
	if err := f.failure("RemoveRoot"); err != nil {
		return model.Snapshot{}, err
	}
	remaining := make([]model.RootView, 0, len(f.snapshot.Roots))
	for _, root := range f.snapshot.Roots {
		if root.Root.ID != rootID {
			remaining = append(remaining, root)
		}
	}
	f.snapshot.Roots = remaining
	return f.snapshot, nil
}

func (f *fakeBackend) Snapshot() (model.Snapshot, error) {
	f.snapshotCount++
	if err := f.failure("Snapshot"); err != nil {
		return model.Snapshot{}, err
	}
	return f.snapshot, nil
}

func (f *fakeBackend) EnsureWorkspace(rootID, path string) (model.Workspace, error) {
	f.ensuredRootID = rootID
	f.ensuredPath = path
	if err := f.failure("EnsureWorkspace"); err != nil {
		return model.Workspace{}, err
	}
	return f.workspace, nil
}

func (f *fakeBackend) CreateTab(workspaceID string, columns, rows uint16) (model.Tab, error) {
	f.createCount++
	f.createdColumns = columns
	f.createdRows = rows
	if err := f.failure("CreateTab"); err != nil {
		return model.Tab{}, err
	}
	for rootIndex := range f.snapshot.Roots {
		root := &f.snapshot.Roots[rootIndex]
		if f.workspace.ID == workspaceID && f.workspace.Path == root.Root.Path {
			root.Tabs = append(root.Tabs, f.createdTab)
			break
		}
		for directoryIndex := range root.Directories {
			directory := &root.Directories[directoryIndex]
			if directory.Workspace.ID == workspaceID {
				directory.Tabs = append(directory.Tabs, f.createdTab)
				break
			}
		}
	}
	return f.createdTab, nil
}

func (f *fakeBackend) OpenTerminal(tabID string) (io.ReadWriteCloser, error) {
	f.openedTab = tabID
	if err := f.failure("OpenTerminal"); err != nil {
		return nil, err
	}
	f.stream = newMemoryStream("\x1b[2J\x1b[Hembedded terminal")
	return f.stream, nil
}

func (f *fakeBackend) Resize(_ string, columns, rows uint16) error {
	f.createdColumns = columns
	f.createdRows = rows
	return f.failure("Resize")
}

func (f *fakeBackend) Shutdown() error {
	f.shutdownCount++
	if err := f.failure("Shutdown"); err != nil {
		return err
	}
	return nil
}

func TestDashboardSelectsWorkspaceAndCreatesTerminal(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{
			Workspace: workspace,
		}},
	}}}
	backend := &fakeBackend{
		snapshot:   snapshot,
		workspace:  workspace,
		createdTab: model.Tab{ID: "tab-1", WorkspaceID: workspace.ID, Name: "1", Running: true},
	}
	value := newDashboard(backend, snapshot)
	value.width = 120
	value.height = 40

	updated, _ := value.Update(key(tea.KeyDown, ""))
	value = updated.(dashboard)
	updated, command := value.Update(key(tea.KeyEnter, ""))
	value = updated.(dashboard)
	if command == nil {
		t.Fatal("select workspace command = nil")
	}
	updated, command = value.Update(command())
	value = updated.(dashboard)
	if value.selectedWorkspaceID != workspace.ID || value.focus != leftPane {
		t.Fatalf("selected workspace = %q, focus = %v", value.selectedWorkspaceID, value.focus)
	}
	if command == nil {
		t.Fatal("workspace without tabs did not create a terminal")
	}
	updated, openCommand := value.Update(command())
	value = updated.(dashboard)
	if openCommand == nil {
		t.Fatal("created tab did not open an embedded terminal")
	}
	updated, readCommand := value.Update(openCommand())
	value = updated.(dashboard)
	defer value.closeTerminal()
	if value.terminal == nil || value.focus != terminalPane || readCommand == nil {
		t.Fatalf("terminal = %v, focus = %v, read command = %v", value.terminal, value.focus, readCommand)
	}
	if backend.createdColumns != 89 || backend.createdRows != 36 {
		t.Fatalf("terminal size = %dx%d, want right pane size 89x36", backend.createdColumns, backend.createdRows)
	}
	if backend.createCount != 1 {
		t.Fatalf("CreateTab() calls = %d, want 1", backend.createCount)
	}

	updated, _ = value.Update(readCommand())
	value = updated.(dashboard)
	rendered := value.render()
	if !strings.Contains(rendered, "projects") || !strings.Contains(rendered, "embedded terminal") {
		t.Fatalf("rendered dashboard does not contain both panes:\n%s", rendered)
	}
	updated, _ = value.Update(tea.KeyPressMsg(tea.Key{Code: '\\', Mod: tea.ModCtrl}))
	value = updated.(dashboard)
	if value.focus != leftPane || value.terminal == nil {
		t.Fatalf("Ctrl+\\ focus = %v, terminal = %v", value.focus, value.terminal)
	}
}

func TestDashboardSelectsRootAndCreatesTerminal(t *testing.T) {
	root := model.Root{ID: "root-1", Name: "projects", Path: "/projects"}
	rootWorkspace := model.Workspace{ID: "root-workspace", RootID: root.ID, Name: root.Name, Path: root.Path}
	tab := model.Tab{ID: "root-tab", WorkspaceID: rootWorkspace.ID, Name: "1", Running: true}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: root,
		Directories: []model.WorkspaceView{{
			Workspace: model.Workspace{RootID: root.ID, Name: "cloned", Path: "/projects/cloned"},
		}},
	}}}
	backend := &fakeBackend{snapshot: snapshot, workspace: rootWorkspace, createdTab: tab}
	value := newDashboard(backend, snapshot)
	value.width = 120
	value.height = 40
	value.navIndex = 0

	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "▌ ▾ projects") || !strings.Contains(rendered, "  └─ cloned") {
		t.Fatalf("root and indented workspace are not distinguishable:\n%s", rendered)
	}
	updated, selectCommand := value.Update(key(tea.KeyEnter, ""))
	value = updated.(dashboard)
	if selectCommand == nil {
		t.Fatal("root selection command = nil")
	}
	updated, createCommand := value.Update(selectCommand())
	value = updated.(dashboard)
	if createCommand == nil || backend.ensuredRootID != root.ID || backend.ensuredPath != root.Path {
		t.Fatalf("root selection = (command %v, root %q, path %q)", createCommand, backend.ensuredRootID, backend.ensuredPath)
	}
	updated, openCommand := value.Update(createCommand())
	value = updated.(dashboard)
	if openCommand == nil || backend.createCount != 1 {
		t.Fatalf("root tab creation = (command %v, count %d)", openCommand, backend.createCount)
	}
	updated, readCommand := value.Update(openCommand())
	value = updated.(dashboard)
	defer value.closeTerminal()
	if value.terminal == nil || value.terminal.id != tab.ID || readCommand == nil {
		t.Fatalf("root terminal = %v, read command = %v", value.terminal, readCommand)
	}
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "▎ ▾ projects") || !strings.Contains(rendered, "●") {
		t.Fatalf("root tab marker is missing:\n%s", rendered)
	}
}

func TestDashboardReloadsWorkspacesWhenReturningFromTerminal(t *testing.T) {
	root := model.Root{ID: "root-1", Name: "projects", Path: "/projects"}
	rootWorkspace := model.Workspace{ID: "root-workspace", RootID: root.ID, Name: root.Name, Path: root.Path}
	tab := model.Tab{ID: "root-tab", WorkspaceID: rootWorkspace.ID, Name: "1", Running: true}
	initial := model.Snapshot{Roots: []model.RootView{{Root: root, Tabs: []model.Tab{tab}}}}
	refreshed := model.Snapshot{Roots: []model.RootView{{
		Root: root,
		Tabs: []model.Tab{tab},
		Directories: []model.WorkspaceView{{
			Workspace: model.Workspace{RootID: root.ID, Name: "cloned", Path: "/projects/cloned"},
		}},
	}}}
	backend := &fakeBackend{snapshot: refreshed, workspace: rootWorkspace}
	terminal := newEmbeddedTerminal(tab.ID, newMemoryStream(""), 40, 10)
	defer terminal.closeTerminal()
	value := newDashboard(backend, initial)
	value.focus = terminalPane
	value.selectedWorkspaceID = rootWorkspace.ID
	value.selectedPath = root.Path
	value.terminal = terminal

	updated, refreshCommand := value.Update(tea.KeyPressMsg(tea.Key{Code: '\\', Mod: tea.ModCtrl}))
	value = updated.(dashboard)
	if value.focus != leftPane || refreshCommand == nil {
		t.Fatalf("Ctrl+\\ result = (focus %v, command %v)", value.focus, refreshCommand)
	}
	updated, _ = value.Update(refreshCommand())
	value = updated.(dashboard)
	if backend.snapshotCount != 1 || !strings.Contains(ansi.Strip(value.render()), "  └─ cloned") {
		t.Fatalf("reloaded workspace list = (snapshots %d):\n%s", backend.snapshotCount, value.render())
	}
}

func TestDashboardOpensExistingWorkspaceTabWithoutCreatingOne(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	tab := model.Tab{ID: "tab-1", WorkspaceID: workspace.ID, Name: "1", Running: true}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace, Tabs: []model.Tab{tab}}},
	}}}
	backend := &fakeBackend{snapshot: snapshot, workspace: workspace}
	value := newDashboard(backend, snapshot)
	value.navIndex = 1

	command := value.selectWorkspace()
	updated, openCommand := value.Update(command())
	value = updated.(dashboard)
	if openCommand == nil {
		t.Fatal("existing terminal was not opened")
	}
	if backend.createCount != 0 {
		t.Fatalf("CreateTab() calls = %d, want 0", backend.createCount)
	}
}

func TestDashboardAddsRootFromPrompt(t *testing.T) {
	backend := &fakeBackend{}
	value := newDashboard(backend, model.Snapshot{})

	updated, _ := value.Update(key(tea.KeyF2, ""))
	value = updated.(dashboard)
	updated, _ = value.Update(key(tea.KeyExtended, "/projects"))
	value = updated.(dashboard)
	updated, command := value.Update(key(tea.KeyEnter, ""))
	value = updated.(dashboard)
	if command == nil {
		t.Fatal("add root command = nil")
	}
	updated, _ = value.Update(command())
	value = updated.(dashboard)
	if backend.addedPath != "/projects" || value.errorMessage != "" {
		t.Fatalf("added path = %q, error = %q", backend.addedPath, value.errorMessage)
	}
}

func TestDashboardAcceptsPastedRootPath(t *testing.T) {
	backend := &fakeBackend{}
	value := newDashboard(backend, model.Snapshot{})

	updated, _ := value.Update(key(tea.KeyF2, ""))
	value = updated.(dashboard)
	updated, _ = value.Update(tea.PasteMsg{Content: "/projects/with spaces"})
	value = updated.(dashboard)
	updated, command := value.Update(key(tea.KeyEnter, ""))
	value = updated.(dashboard)
	if command == nil {
		t.Fatal("add pasted root command = nil")
	}
	value.Update(command())
	if backend.addedPath != "/projects/with spaces" {
		t.Fatalf("added path = %q, want pasted path", backend.addedPath)
	}
}

func TestDashboardSupportsHiddenWorkspaceShortcuts(t *testing.T) {
	backend := &fakeBackend{}

	value := newDashboard(backend, model.Snapshot{})
	updated, command := value.Update(key('i', "i"))
	value = updated.(dashboard)
	if value.modal != aboutModal || command != nil {
		t.Fatalf("i result = (modal %v, command %v), want about modal", value.modal, command)
	}

	value = newDashboard(backend, model.Snapshot{})
	updated, command = value.Update(key('a', "a"))
	value = updated.(dashboard)
	if !value.inputMode || command != nil {
		t.Fatalf("a result = (input mode %v, command %v), want root input", value.inputMode, command)
	}

	value = newDashboard(backend, model.Snapshot{})
	updated, command = value.Update(key(',', ","))
	value = updated.(dashboard)
	if value.modal != configModal || command != nil {
		t.Fatalf(", result = (modal %v, command %v), want config modal", value.modal, command)
	}

	value = newDashboard(backend, model.Snapshot{})
	updated, command = value.Update(key('q', "q"))
	value = updated.(dashboard)
	if !value.result.Quit || command == nil {
		t.Fatalf("q result = (quit %v, command %v), want quit", value.result.Quit, command)
	}

	value = newDashboard(backend, model.Snapshot{})
	updated, command = value.Update(key('r', "r"))
	if command == nil {
		t.Fatal("r refresh command = nil")
	}
	command()
	if backend.snapshotCount != 1 {
		t.Fatalf("r snapshot calls = %d, want 1", backend.snapshotCount)
	}
}

func TestDashboardIgnoresRemovedPlusShortcut(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})

	updated, command := value.Update(key('+', "+"))
	value = updated.(dashboard)
	if value.inputMode || command != nil {
		t.Fatalf("+ result = (input mode %v, command %v), want no action", value.inputMode, command)
	}
}

func TestDashboardSupportsIMEIndependentShortcuts(t *testing.T) {
	backend := &fakeBackend{}

	value := newDashboard(backend, model.Snapshot{})
	updated, command := value.Update(key(tea.KeyF1, ""))
	value = updated.(dashboard)
	if value.modal != aboutModal || command != nil {
		t.Fatalf("F1 result = (modal %v, command %v), want about modal", value.modal, command)
	}

	value = newDashboard(backend, model.Snapshot{})
	updated, command = value.Update(key(tea.KeyF2, ""))
	value = updated.(dashboard)
	if !value.inputMode || command != nil {
		t.Fatalf("F2 result = (input mode %v, command %v), want root input", value.inputMode, command)
	}

	value = newDashboard(backend, model.Snapshot{})
	updated, command = value.Update(key(tea.KeyF3, ""))
	value = updated.(dashboard)
	if value.modal != configModal || command != nil {
		t.Fatalf("F3 result = (modal %v, command %v), want config modal", value.modal, command)
	}

	value = newDashboard(backend, model.Snapshot{})
	updated, command = value.Update(key(tea.KeyF5, ""))
	value = updated.(dashboard)
	if command == nil {
		t.Fatal("F5 refresh command = nil")
	}
	value.Update(command())
	if backend.snapshotCount != 1 {
		t.Fatalf("F5 snapshot calls = %d, want 1", backend.snapshotCount)
	}

	value = newDashboard(backend, model.Snapshot{})
	updated, command = value.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	value = updated.(dashboard)
	if !value.result.Quit || command == nil {
		t.Fatalf("Ctrl+C result = (quit %v, command %v), want quit", value.result.Quit, command)
	}

	terminal := newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)
	value = newDashboard(backend, model.Snapshot{})
	value.focus = terminalPane
	value.terminal = terminal
	value.width = 120
	if rendered := value.render(); !shortcutOrder(rendered, "F1", "F2", "F3", "F4", "F5", "F6") {
		t.Fatalf("terminal shortcuts are not in F1-F6 order:\n%s", rendered)
	} else if !strings.Contains(rendered, value.styles.shortcutKey.Render(" Ctrl+\\ ")) {
		t.Fatalf("terminal status bar does not contain navigation shortcut:\n%s", rendered)
	}
	updated, command = value.Update(key(tea.KeyF3, ""))
	value = updated.(dashboard)
	if value.modal != configModal || command != nil {
		t.Fatalf("terminal F3 result = (modal %v, command %v), want config modal", value.modal, command)
	}
	updated, command = value.Update(key(tea.KeyF4, ""))
	value = updated.(dashboard)
	if !value.result.Quit || command == nil || value.terminal != nil {
		t.Fatalf("F4 result = (quit %v, command %v, terminal %v), want global quit", value.result.Quit, command, value.terminal)
	}

	value = newDashboard(backend, model.Snapshot{})
	value.width = 120
	rendered := value.render()
	for _, status := range []string{"F1", "F2", "F3", "F4", "F5", "F6"} {
		if !strings.Contains(rendered, value.styles.shortcutKey.Render(" "+status+" ")) {
			t.Fatalf("status bar does not contain %s:\n%s", status, rendered)
		}
	}
	if !shortcutOrder(rendered, "F1", "F2", "F3", "F4", "F5", "F6") {
		t.Fatalf("workspace shortcuts are not in F1-F6 order:\n%s", rendered)
	}
	for _, key := range []string{"↑/↓", "←/→", "Enter", "Tab"} {
		if !strings.Contains(rendered, value.styles.shortcutKey.Render(" "+key+" ")) {
			t.Fatalf("workspace status bar does not contain %q navigation shortcut:\n%s", key, rendered)
		}
	}
	for _, key := range []string{"i", "a", ",", "q", "r", "+", "?"} {
		if strings.Contains(rendered, value.styles.shortcutKey.Render(" "+key+" ")) {
			t.Fatalf("hidden shortcut %q is shown in the status bar:\n%s", key, rendered)
		}
	}
	for _, description := range []string{"add root", "quit"} {
		if !strings.Contains(rendered, value.styles.shortcutDescription.Render(description)) {
			t.Fatalf("status bar does not contain %q description:\n%s", description, rendered)
		}
	}
}

func TestDashboardConfirmsDaemonShutdown(t *testing.T) {
	backend := &fakeBackend{}
	terminal := newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)
	value := newDashboard(backend, model.Snapshot{})
	value.focus = terminalPane
	value.terminal = terminal
	value.width = 100
	value.height = 24

	updated, command := value.Update(key(tea.KeyF6, ""))
	value = updated.(dashboard)
	if value.modal != shutdownModal || command != nil || backend.shutdownCount != 0 {
		t.Fatalf("F6 result = (modal %v, command %v, shutdowns %d), want confirmation", value.modal, command, backend.shutdownCount)
	}
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "Stop daemon") || !strings.Contains(rendered, "all running terminal sessions") {
		t.Fatalf("shutdown warning is missing:\n%s", rendered)
	}

	updated, command = value.Update(key(tea.KeyEscape, ""))
	value = updated.(dashboard)
	if value.modal != noModal || command != nil || backend.shutdownCount != 0 {
		t.Fatalf("Esc result = (modal %v, command %v, shutdowns %d), want cancellation", value.modal, command, backend.shutdownCount)
	}

	updated, _ = value.Update(key(tea.KeyF6, ""))
	value = updated.(dashboard)
	updated, command = value.Update(key(tea.KeyEnter, ""))
	value = updated.(dashboard)
	if command == nil || !value.shutdownPending || backend.shutdownCount != 0 {
		t.Fatalf("Enter result = (command %v, pending %v, shutdowns %d), want deferred shutdown", command, value.shutdownPending, backend.shutdownCount)
	}

	// Esc cannot take back a dispatched shutdown, and Enter must not repeat it.
	for _, message := range []tea.KeyPressMsg{key(tea.KeyEscape, ""), key(tea.KeyEnter, ""), key(tea.KeyF6, "")} {
		updated, extra := value.Update(message)
		value = updated.(dashboard)
		if extra != nil || value.modal != shutdownModal {
			t.Fatalf("%q during shutdown = (command %v, modal %v), want the modal held", message.String(), extra, value.modal)
		}
	}
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "STOPPING") {
		t.Fatalf("status bar does not report the pending shutdown:\n%s", rendered)
	}

	updated, quitCommand := value.Update(command())
	value = updated.(dashboard)
	if backend.shutdownCount != 1 || !value.result.Quit || quitCommand == nil || value.terminal != nil {
		t.Fatalf("shutdown result = (count %d, quit %v, command %v, terminal %v)", backend.shutdownCount, value.result.Quit, quitCommand, value.terminal)
	}
}

func TestDashboardReportsFailedShutdown(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	updated, _ := value.Update(key(tea.KeyF6, ""))
	value = updated.(dashboard)
	updated, _ = value.Update(key(tea.KeyEnter, ""))
	value = updated.(dashboard)

	updated, command := value.Update(daemonStoppedMsg{err: errors.New("dial unix: no such file")})
	value = updated.(dashboard)
	if command != nil || value.shutdownPending || value.modal != noModal || value.result.Quit {
		t.Fatalf("failed shutdown = (command %v, pending %v, modal %v, quit %v), want the dashboard restored",
			command, value.shutdownPending, value.modal, value.result.Quit)
	}
	if !strings.Contains(value.errorMessage, "stop daemon: ") {
		t.Fatalf("error message = %q, want a stop daemon failure", value.errorMessage)
	}

	// Retrying is possible because the pending flag was cleared.
	updated, _ = value.Update(key(tea.KeyF6, ""))
	value = updated.(dashboard)
	if value.modal != shutdownModal || value.errorMessage != "" {
		t.Fatalf("retry = (modal %v, error %q), want a fresh confirmation", value.modal, value.errorMessage)
	}
}

// plainRows drops styling and trailing padding so viewport rows can be compared
// whether they are rendered from the live screen or from the scrollback.
// waitForGuest blocks until the guest's stream holds want, so tests do not
// race the goroutine that drains the emulator's input pipe. A fixed sleep is
// long enough on an idle machine and too short under -race.
func waitForGuest(t *testing.T, stream *memoryStream, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stream.String(), want) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("guest received %q, want it to contain %q", stream.String(), want)
}

// waitForGuestSilence gives the emulator a chance to speak and fails if it
// does, which is the opposite assertion and cannot be polled for.
func waitForGuestSilence(t *testing.T, stream *memoryStream, was string) {
	t.Helper()
	time.Sleep(100 * time.Millisecond)
	if now := stream.String(); now != was {
		t.Fatalf("guest received %q, want nothing beyond %q", now, was)
	}
}

func plainRows(lines []string) []string {
	rows := make([]string, 0, len(lines))
	for _, line := range lines {
		rows = append(rows, strings.TrimRight(ansi.Strip(line), " "))
	}
	return rows
}

// scrolledDashboard returns a dashboard whose terminal holds `lines` rows of
// numbered output, all but the last screen of which sits in the scrollback.
func scrolledDashboard(t *testing.T, lines int) dashboard {
	t.Helper()
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 120
	value.height = 30
	view := value.dimensions()
	rightWidth, terminalHeight := view.rightWidth, view.terminalHeight
	value.terminal = newEmbeddedTerminal("tab-1", newMemoryStream(""), rightWidth, terminalHeight)
	t.Cleanup(value.closeTerminal)
	value.focus = terminalPane

	var output strings.Builder
	for index := range lines {
		fmt.Fprintf(&output, "line-%03d\r\n", index)
	}
	value.terminal.writeOutput([]byte(output.String()))
	if value.terminal.scrollbackLen() == 0 {
		t.Fatalf("terminal produced no scrollback for %d lines at height %d", lines, terminalHeight)
	}
	return value
}

func TestDashboardScrollsTerminalHistory(t *testing.T) {
	value := scrolledDashboard(t, 200)
	history := value.terminal.scrollbackLen()

	// The mouse stays with the host terminal until scrollback mode is entered.
	if value.View().MouseMode != tea.MouseModeNone {
		t.Fatalf("mouse mode outside scrollback = %v, want none", value.View().MouseMode)
	}
	live := plainRows(value.terminal.renderViewport(0))

	updated, command := value.Update(key(tea.KeyF7, ""))
	value = updated.(dashboard)
	if !value.scrollback || value.scrollOffset != 0 || command != nil {
		t.Fatalf("F7 = (scrollback %v, offset %d, command %v), want the live view in scrollback mode",
			value.scrollback, value.scrollOffset, command)
	}
	view := value.View()
	if view.MouseMode != tea.MouseModeNone || view.Cursor != nil {
		t.Fatalf("copy mode view = (mouse %v, cursor %v), want the mouse left with the host", view.MouseMode, view.Cursor)
	}

	// The wheel arrives as arrow keys through the terminal's alternate scroll,
	// which is what keeps the host's drag selection alive in copy mode.
	updated, _ = value.Update(key(tea.KeyUp, ""))
	value = updated.(dashboard)
	if value.scrollOffset != 1 {
		t.Fatalf("wheel up offset = %d, want 1", value.scrollOffset)
	}
	if slices.Equal(plainRows(value.terminal.renderViewport(value.scrollOffset)), live) {
		t.Fatal("scrolling up did not change the rendered viewport")
	}
	updated, _ = value.Update(key(tea.KeyDown, ""))
	value = updated.(dashboard)
	if value.scrollOffset != 0 {
		t.Fatalf("wheel down offset = %d, want back at the live screen", value.scrollOffset)
	}

	// Home reaches the oldest retained line and clamps there.
	updated, _ = value.Update(key(tea.KeyHome, ""))
	value = updated.(dashboard)
	if value.scrollOffset != history {
		t.Fatalf("Home offset = %d, want the full history %d", value.scrollOffset, history)
	}
	oldest := plainRows(value.terminal.renderViewport(value.scrollOffset))[0]
	retained := plainRows([]string{value.terminal.emulator.Scrollback().Line(0).Render()})[0]
	if oldest != retained {
		t.Fatalf("oldest visible row = %q, want the first retained line %q", oldest, retained)
	}
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "SCROLLBACK") ||
		!strings.Contains(rendered, fmt.Sprintf("%d/%d", history, history)) {
		t.Fatalf("status bar does not report the scrollback position:\n%s", rendered)
	}
	updated, _ = value.Update(key(tea.KeyEnd, ""))
	value = updated.(dashboard)
	if value.scrollOffset != 0 {
		t.Fatalf("End offset = %d, want the live screen", value.scrollOffset)
	}

	updated, _ = value.Update(key(tea.KeyEscape, ""))
	value = updated.(dashboard)
	if value.scrollback || value.scrollOffset != 0 || value.View().MouseMode != tea.MouseModeNone {
		t.Fatalf("Esc = (scrollback %v, offset %d, mouse %v), want the mouse returned to the host",
			value.scrollback, value.scrollOffset, value.View().MouseMode)
	}
}

func TestDashboardEntersScrollbackFromEveryBinding(t *testing.T) {
	page := scrolledDashboard(t, 200).scrollbackPage()

	for _, step := range []struct {
		name    string
		enter   []tea.KeyPressMsg
		focus   pane
		offset  int
		wheelOK bool
	}{
		{name: "F7", enter: []tea.KeyPressMsg{key(tea.KeyF7, "")}, focus: terminalPane},
		{
			name:  "Ctrl+\\ twice",
			enter: []tea.KeyPressMsg{tea.KeyPressMsg(tea.Key{Code: '\\', Mod: tea.ModCtrl}), tea.KeyPressMsg(tea.Key{Code: '\\', Mod: tea.ModCtrl})},
			focus: terminalPane,
		},
		{
			name:   "Shift+PgUp",
			enter:  []tea.KeyPressMsg{tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModShift})},
			focus:  terminalPane,
			offset: page,
		},
		{
			name:   "Shift+PgUp from the workspace pane",
			enter:  []tea.KeyPressMsg{tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModShift})},
			focus:  leftPane,
			offset: page,
		},
	} {
		t.Run(step.name, func(t *testing.T) {
			value := scrolledDashboard(t, 200)
			value.focus = step.focus
			for _, message := range step.enter {
				updated, _ := value.Update(message)
				value = updated.(dashboard)
			}
			if !value.scrollback || value.scrollOffset != step.offset {
				t.Fatalf("%s = (scrollback %v, offset %d), want scrollback at offset %d",
					step.name, value.scrollback, value.scrollOffset, step.offset)
			}
		})
	}
}

func TestDashboardHoldsScrollbackViewportOnNewOutput(t *testing.T) {
	value := scrolledDashboard(t, 200)
	updated, _ := value.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModShift}))
	value = updated.(dashboard)
	anchored := plainRows(value.terminal.renderViewport(value.scrollOffset))
	before := value.scrollOffset

	updated, _ = value.Update(terminalOutputMsg{terminal: value.terminal, data: []byte("fresh-a\r\nfresh-b\r\n")})
	value = updated.(dashboard)
	if value.scrollOffset == before {
		t.Fatalf("offset stayed at %d while new output pushed lines into the scrollback", before)
	}
	if !slices.Equal(plainRows(value.terminal.renderViewport(value.scrollOffset)), anchored) {
		t.Fatalf("viewport drifted when new output arrived while scrolled back:\n%s\nwant\n%s",
			strings.Join(plainRows(value.terminal.renderViewport(value.scrollOffset)), "\n"), strings.Join(anchored, "\n"))
	}

	// Leaving scrollback returns to the live screen, which has the new output.
	updated, _ = value.Update(key('q', "q"))
	value = updated.(dashboard)
	if value.scrollback || value.scrollOffset != 0 {
		t.Fatalf("q = (scrollback %v, offset %d), want the live screen", value.scrollback, value.scrollOffset)
	}
	if !strings.Contains(ansi.Strip(strings.Join(value.terminal.renderViewport(0), "\n")), "fresh-b") {
		t.Fatal("live screen does not show the output that arrived during scrollback")
	}
}

// Full-screen applications such as vim, less, and Claude Code switch to the
// alternate screen, which has no history of its own. romty must not offer the
// main screen's older output as if it belonged to them.
func TestDashboardRefusesScrollbackOnTheAlternateScreen(t *testing.T) {
	value := scrolledDashboard(t, 200)
	updated, _ := value.Update(key(tea.KeyF7, ""))
	value = updated.(dashboard)
	if !value.scrollback {
		t.Fatal("scrollback did not open on the main screen")
	}

	// The application takes over the screen while scrollback is open.
	updated, _ = value.Update(terminalOutputMsg{
		terminal: value.terminal,
		data:     []byte("\x1b[?1049h\x1b[2J\x1b[Happlication frame\r\n"),
	})
	value = updated.(dashboard)
	if value.scrollback || value.scrollOffset != 0 {
		t.Fatalf("scrollback stayed open on the alternate screen = (scrollback %v, offset %d)",
			value.scrollback, value.scrollOffset)
	}
	if !strings.Contains(value.errorMessage, "owns the screen") {
		t.Fatalf("error message = %q, want an explanation of the alternate screen", value.errorMessage)
	}

	// Re-entering must not resurrect the history from before the application.
	for _, message := range []tea.KeyPressMsg{
		key(tea.KeyF7, ""),
		tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModShift}),
	} {
		updated, _ = value.Update(message)
		value = updated.(dashboard)
		if value.scrollback {
			t.Fatalf("%q entered scrollback while the alternate screen was active", message.String())
		}
	}
	rows := plainRows(value.terminal.renderViewport(500))
	if rows[0] != "application frame" {
		t.Fatalf("viewport row 0 = %q, want the live alternate screen", rows[0])
	}
	if strings.Contains(strings.Join(rows, "\n"), "line-") {
		t.Fatalf("viewport leaked pre-application output:\n%s", strings.Join(rows, "\n"))
	}

	// Leaving the alternate screen restores the history that was there before.
	updated, _ = value.Update(terminalOutputMsg{terminal: value.terminal, data: []byte("\x1b[?1049l")})
	value = updated.(dashboard)
	updated, _ = value.Update(key(tea.KeyF7, ""))
	value = updated.(dashboard)
	if !value.scrollback {
		t.Fatalf("scrollback did not reopen after the application exited: %q", value.errorMessage)
	}
}

// Copy mode drops the workspace pane so that a plain drag in the host terminal
// selects terminal output alone. In the split layout every host row also holds
// the workspace tree, which a multi-line selection would copy along with it.
func TestDashboardRendersCopyModeFullWidth(t *testing.T) {
	value := scrolledDashboard(t, 200)
	value.state = model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "nalbam", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: model.Workspace{ID: "w", Name: "SnowClash", Path: "/p/s"}}},
	}}}
	if !strings.Contains(ansi.Strip(value.render()), "SnowClash") {
		t.Fatal("the split layout does not show the workspace tree")
	}

	updated, _ := value.Update(key(tea.KeyF7, ""))
	value = updated.(dashboard)
	bodyHeight := value.dimensions().bodyHeight
	body := strings.Split(ansi.Strip(value.render()), "\n")[:bodyHeight]
	joined := strings.Join(body, "\n")
	if strings.Contains(joined, "SnowClash") || strings.Contains(joined, "nalbam") || strings.Contains(joined, "│") {
		t.Fatalf("copy mode still renders the workspace pane and divider:\n%s", joined)
	}
	if !strings.Contains(joined, "line-") {
		t.Fatalf("copy mode does not render terminal output:\n%s", joined)
	}

	updated, _ = value.Update(key(tea.KeyEscape, ""))
	value = updated.(dashboard)
	if !strings.Contains(ansi.Strip(value.render()), "SnowClash") {
		t.Fatal("leaving copy mode did not restore the workspace pane")
	}
}

func TestDashboardKeepsMouseWithTheHostUnlessPassthroughIsOn(t *testing.T) {
	value := scrolledDashboard(t, 200)
	// A guest that wants the mouse, the way Claude Code and htop do.
	value.terminal.writeOutput([]byte("\x1b[?1003h\x1b[?1006h"))

	if value.terminal.guestMouseMode() != tea.MouseModeAllMotion {
		t.Fatalf("guest mouse mode = %v, want all motion", value.terminal.guestMouseMode())
	}
	if value.View().MouseMode != tea.MouseModeNone {
		t.Fatalf("mouse mode = %v, want the host to keep the mouse by default", value.View().MouseMode)
	}
	value.Update(tea.MouseWheelMsg{X: 40, Y: 6, Button: tea.MouseWheelUp})
	waitForGuestSilence(t, value.terminal.stream.(*memoryStream), "")

	value.mousePassthrough = true
	if value.View().MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("mouse mode = %v, want the guest's own mode mirrored", value.View().MouseMode)
	}
	leftWidth := value.dimensions().leftWidth
	value.Update(tea.MouseWheelMsg{X: leftWidth + 3 + 4, Y: terminalTop + 2, Button: tea.MouseWheelUp})
	waitForGuest(t, value.terminal.stream.(*memoryStream), "\x1b[<64;5;3")
	sent := value.terminal.stream.(*memoryStream).String()

	// Events over the workspace pane are not the guest's business.
	value.Update(tea.MouseWheelMsg{X: 1, Y: terminalTop + 2, Button: tea.MouseWheelUp})
	waitForGuestSilence(t, value.terminal.stream.(*memoryStream), sent)

	// Copy mode takes the mouse back so the host can select the scrolled page.
	updated, _ := value.Update(key(tea.KeyF7, ""))
	value = updated.(dashboard)
	if !value.scrollback || value.View().MouseMode != tea.MouseModeNone {
		t.Fatalf("copy mode = (scrollback %v, mouse %v), want the mouse returned to the host",
			value.scrollback, value.View().MouseMode)
	}
}

// A guest that owns the screen pages its own output, so romty must hand the key
// over rather than swallowing it for a scrollback it cannot offer.
func TestDashboardForwardsPagingToTheGuestThatOwnsTheScreen(t *testing.T) {
	value := scrolledDashboard(t, 200)
	value.terminal.writeOutput([]byte("\x1b[?1049h"))

	value.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModShift}))
	// Sent unmodified: plain PgUp is the key such applications bind.
	waitForGuest(t, value.terminal.stream.(*memoryStream), "\x1b[5~")

	// On the main screen it still belongs to romty's own scrollback.
	value.terminal.writeOutput([]byte("\x1b[?1049l"))
	updated, _ := value.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModShift}))
	value = updated.(dashboard)
	if !value.scrollback || value.scrollOffset == 0 {
		t.Fatalf("Shift+PgUp on the main screen = (scrollback %v, offset %d), want a page of history",
			value.scrollback, value.scrollOffset)
	}
}

// Ctrl+\ cycles terminal -> workspace -> scrollback -> terminal, so leaving
// scrollback puts the keyboard back where the full-width view already is.
func TestDashboardFocusesTerminalWhenLeavingScrollback(t *testing.T) {
	control := tea.KeyPressMsg(tea.Key{Code: '\\', Mod: tea.ModCtrl})
	for _, leave := range []tea.KeyPressMsg{control, key(tea.KeyEscape, ""), key('q', "q"), key(tea.KeyF7, "")} {
		value := scrolledDashboard(t, 200)
		updated, _ := value.Update(control)
		value = updated.(dashboard)
		if value.focus != leftPane {
			t.Fatalf("Ctrl+\\ focus = %v, want the workspace pane", value.focus)
		}
		updated, _ = value.Update(control)
		value = updated.(dashboard)
		if !value.scrollback || value.focus != leftPane {
			t.Fatalf("second Ctrl+\\ = (scrollback %v, focus %v), want scrollback without moving focus",
				value.scrollback, value.focus)
		}

		updated, _ = value.Update(leave)
		value = updated.(dashboard)
		if value.scrollback || value.focus != terminalPane {
			t.Fatalf("%q = (scrollback %v, focus %v), want the terminal focused",
				leave.String(), value.scrollback, value.focus)
		}
		if value.View().Cursor == nil {
			t.Fatalf("%q left the terminal without a cursor", leave.String())
		}
	}
}

func TestDashboardKeepsWorkspaceFocusWhenTheTerminalIsGone(t *testing.T) {
	value := scrolledDashboard(t, 200)
	value.focus = leftPane
	updated, _ := value.Update(key(tea.KeyF7, ""))
	value = updated.(dashboard)
	value.terminal.disconnect()

	updated, _ = value.Update(key(tea.KeyEscape, ""))
	value = updated.(dashboard)
	if value.scrollback || value.focus == terminalPane {
		t.Fatalf("leaving scrollback = (scrollback %v, focus %v), want the workspace pane for a dead terminal",
			value.scrollback, value.focus)
	}
}

// exitedDashboard opens a terminal on a workspace holding the given tabs, then
// ends its stream the way a shell exiting does, and returns the dashboard plus
// the refresh command it asked for.
func exitedDashboard(t *testing.T, tabs []model.Tab, open int) (dashboard, *fakeBackend, tea.Cmd) {
	t.Helper()
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace, Tabs: tabs}},
	}}}
	backend := &fakeBackend{snapshot: snapshot, workspace: workspace}
	value := newDashboard(backend, snapshot)
	value.width = 120
	value.height = 40
	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path
	value.navIndex = 1
	value.tabIndex = open
	value.focus = terminalPane
	value.terminal = newEmbeddedTerminal(tabs[open].ID, newMemoryStream(""), 89, 36)
	t.Cleanup(value.closeTerminal)

	updated, refresh := value.Update(terminalOutputMsg{terminal: value.terminal, err: io.EOF})
	value = updated.(dashboard)
	if value.terminal != nil {
		t.Fatal("the exited terminal is still attached")
	}
	if refresh == nil {
		t.Fatal("an exited terminal did not ask for a fresh snapshot")
	}
	return value, backend, refresh
}

func TestDashboardMovesToASiblingTabWhenTheShellExits(t *testing.T) {
	tabs := []model.Tab{
		{ID: "tab-1", WorkspaceID: "workspace-1", Name: "1", Running: true},
		{ID: "tab-2", WorkspaceID: "workspace-1", Name: "2", Running: true},
		{ID: "tab-3", WorkspaceID: "workspace-1", Name: "3", Running: true},
	}
	value, backend, refresh := exitedDashboard(t, tabs, 1)

	// The daemon drops the tab whose shell exited.
	backend.snapshot.Roots[0].Directories[0].Tabs = []model.Tab{tabs[0], tabs[2]}
	updated, open := value.Update(refresh())
	value = updated.(dashboard)
	if open == nil {
		t.Fatal("no sibling tab was opened after the shell exited")
	}
	if value.tabIndex != 1 {
		t.Fatalf("tab index = %d, want the tab that took the exited one's place", value.tabIndex)
	}
	updated, _ = value.Update(open())
	value = updated.(dashboard)
	defer value.closeTerminal()
	if backend.openedTab != "tab-3" || value.focus != terminalPane {
		t.Fatalf("opened %q with focus %v, want tab-3 in the terminal pane", backend.openedTab, value.focus)
	}
}

func TestDashboardMovesToTheLastTabWhenTheLastShellExits(t *testing.T) {
	tabs := []model.Tab{
		{ID: "tab-1", WorkspaceID: "workspace-1", Name: "1", Running: true},
		{ID: "tab-2", WorkspaceID: "workspace-1", Name: "2", Running: true},
	}
	value, backend, refresh := exitedDashboard(t, tabs, 1)

	backend.snapshot.Roots[0].Directories[0].Tabs = tabs[:1]
	updated, open := value.Update(refresh())
	value = updated.(dashboard)
	if open == nil || value.tabIndex != 0 {
		t.Fatalf("trailing exit = (command %v, tab index %d), want the previous tab", open, value.tabIndex)
	}
	updated, _ = value.Update(open())
	value = updated.(dashboard)
	defer value.closeTerminal()
	if backend.openedTab != "tab-1" {
		t.Fatalf("opened %q, want tab-1", backend.openedTab)
	}
}

func TestDashboardReturnsToTheWorkspaceWhenTheLastTabExits(t *testing.T) {
	tabs := []model.Tab{{ID: "tab-1", WorkspaceID: "workspace-1", Name: "1", Running: true}}
	value, backend, refresh := exitedDashboard(t, tabs, 0)

	backend.snapshot.Roots[0].Directories[0].Tabs = nil
	updated, open := value.Update(refresh())
	value = updated.(dashboard)
	if open != nil {
		t.Fatalf("a terminal was opened with no tabs left: %v", open)
	}
	if value.focus != leftPane || value.navIndex != 1 {
		t.Fatalf("focus = %v at nav index %d, want the workspace pane on the same workspace",
			value.focus, value.navIndex)
	}
	rendered := ansi.Strip(value.render())
	if !strings.Contains(rendered, "Select + and press Enter") || value.View().Cursor != nil {
		t.Fatalf("the exited terminal is still on screen:\n%s", rendered)
	}
}

// A stream that drops while the tab keeps running is a lost connection, not an
// exit, and the same walk reattaches to it.
func TestDashboardReattachesWhenTheConnectionDrops(t *testing.T) {
	tabs := []model.Tab{{ID: "tab-1", WorkspaceID: "workspace-1", Name: "1", Running: true}}
	value, backend, refresh := exitedDashboard(t, tabs, 0)

	updated, open := value.Update(refresh())
	value = updated.(dashboard)
	if open == nil {
		t.Fatal("a still running tab was not reattached")
	}
	updated, _ = value.Update(open())
	value = updated.(dashboard)
	defer value.closeTerminal()
	if backend.openedTab != "tab-1" || value.terminal == nil {
		t.Fatalf("reattached to %q with terminal %v, want tab-1 open", backend.openedTab, value.terminal)
	}
}

// A shell exiting in the background must not pull the user out of the tree.
func TestDashboardKeepsWorkspaceFocusWhenABackgroundShellExits(t *testing.T) {
	tabs := []model.Tab{
		{ID: "tab-1", WorkspaceID: "workspace-1", Name: "1", Running: true},
		{ID: "tab-2", WorkspaceID: "workspace-1", Name: "2", Running: true},
	}
	value, backend, _ := exitedDashboard(t, tabs, 0)
	value.focus = leftPane
	value.terminalExited = true

	backend.snapshot.Roots[0].Directories[0].Tabs = tabs[1:]
	updated, open := value.Update(snapshotMsg{value: backend.snapshot})
	value = updated.(dashboard)
	if open != nil || value.focus != leftPane {
		t.Fatalf("background exit = (command %v, focus %v), want the workspace pane untouched", open, value.focus)
	}
}

// The move off an exited terminal is deferred until a snapshot says where to
// go. If that snapshot never arrives the move must be settled, not left armed
// to fire on whatever refresh comes next.
func TestDashboardSettlesAnExitWhenTheSnapshotFails(t *testing.T) {
	tabs := []model.Tab{
		{ID: "tab-1", WorkspaceID: "workspace-1", Name: "1", Running: true},
		{ID: "tab-2", WorkspaceID: "workspace-1", Name: "2", Running: true},
	}
	value, backend, _ := exitedDashboard(t, tabs, 0)

	updated, _ := value.Update(snapshotMsg{err: errors.New("connect to daemon: no such file")})
	value = updated.(dashboard)
	if value.terminalExited || value.focus != leftPane {
		t.Fatalf("failed refresh = (pending %v, focus %v), want the exit settled on the workspace pane",
			value.terminalExited, value.focus)
	}
	if !strings.Contains(value.errorMessage, "connect to daemon") {
		t.Fatalf("error message = %q, want the refresh failure", value.errorMessage)
	}

	// A healthy terminal opened afterwards must survive an ordinary refresh.
	value.terminal = newEmbeddedTerminal("tab-2", newMemoryStream(""), 89, 36)
	t.Cleanup(value.closeTerminal)
	value.focus = terminalPane
	value.tabIndex = 1
	updated, command := value.Update(snapshotMsg{value: backend.snapshot})
	value = updated.(dashboard)
	if command != nil || value.terminal == nil {
		t.Fatalf("later refresh = (command %v, terminal %v), want the open terminal left alone",
			command, value.terminal != nil)
	}
}

// Every user-visible failure the backend can produce has to reach the status
// bar and leave the dashboard usable. None of these branches had a test,
// because the fake could not fail.
func TestDashboardReportsBackendFailures(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace}},
	}}}

	for _, probe := range []struct {
		name   string
		method string
		act    func(dashboard) (tea.Model, tea.Cmd)
	}{
		{
			name:   "refresh",
			method: "Snapshot",
			act:    func(m dashboard) (tea.Model, tea.Cmd) { return m.Update(key(tea.KeyF5, "")) },
		},
		{
			name:   "opening a workspace",
			method: "EnsureWorkspace",
			act:    func(m dashboard) (tea.Model, tea.Cmd) { return m.Update(key(tea.KeyEnter, "")) },
		},
		{
			name:   "adding a root",
			method: "AddRoot",
			act: func(m dashboard) (tea.Model, tea.Cmd) {
				updated, _ := m.Update(key(tea.KeyF2, ""))
				updated, _ = updated.(dashboard).Update(key('/', "/"))
				return updated.(dashboard).Update(key(tea.KeyEnter, ""))
			},
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			backend := &fakeBackend{snapshot: snapshot, workspace: workspace}
			backend.fail(probe.method, errors.New("daemon: "+probe.method+" refused"))
			value := newDashboard(backend, snapshot)
			value.width = 120
			value.height = 40
			value.navIndex = 1

			updated, command := probe.act(value)
			value = updated.(dashboard)
			if command == nil {
				t.Fatalf("%s produced no command to fail", probe.name)
			}
			updated, _ = value.Update(command())
			value = updated.(dashboard)
			if !strings.Contains(value.errorMessage, probe.method+" refused") {
				t.Fatalf("error message = %q, want the %s failure", value.errorMessage, probe.method)
			}
			if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "ERROR") {
				t.Fatalf("the status bar does not show the failure:\n%s", rendered)
			}
			// The tree still renders and the keyboard still works.
			if !strings.Contains(ansi.Strip(value.render()), "alpha") {
				t.Fatalf("the workspace tree is gone after a failure:\n%s", ansi.Strip(value.render()))
			}
		})
	}
}

// A tab whose creation failed must not leave the dashboard pointing at a
// terminal that was never opened.
func TestDashboardReportsAFailedTabCreation(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	backend := &fakeBackend{
		snapshot: model.Snapshot{Roots: []model.RootView{{
			Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
			Directories: []model.WorkspaceView{{Workspace: workspace}},
		}}},
		workspace: workspace,
	}
	backend.fail("CreateTab", errors.New("daemon: start shell PTY: permission denied"))
	value := newDashboard(backend, backend.snapshot)
	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path

	updated, _ := value.Update(tabMsg{err: backend.failure("CreateTab")})
	value = updated.(dashboard)
	if value.terminal != nil || !strings.Contains(value.errorMessage, "permission denied") {
		t.Fatalf("failed creation = (terminal %v, error %q)", value.terminal != nil, value.errorMessage)
	}
}

// Opening a terminal can fail between the snapshot and the attach, because the
// shell may exit in between. The dashboard has to fall back to the tree.
func TestDashboardReturnsToTheTreeWhenOpeningFails(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.focus = terminalPane

	updated, _ := value.Update(terminalOpenedMsg{err: errors.New("daemon: running terminal session not found")})
	value = updated.(dashboard)
	if value.focus != leftPane || value.terminal != nil {
		t.Fatalf("failed open = (focus %v, terminal %v), want the workspace pane", value.focus, value.terminal != nil)
	}
	if !strings.Contains(value.errorMessage, "session not found") {
		t.Fatalf("error message = %q, want the attach failure", value.errorMessage)
	}
}

// A root romty cannot read has to say so, and be removable: it used to make
// every snapshot fail, and there was no way to forget it short of editing the
// state file.
func TestDashboardShowsAndRemovesAnUnreadableRoot(t *testing.T) {
	snapshot := model.Snapshot{Roots: []model.RootView{
		{
			Root:        model.Root{ID: "root-1", Name: "gone", Path: "/volumes/gone"},
			Error:       "open /volumes/gone: no such file or directory",
			Directories: []model.WorkspaceView{},
		},
		{
			Root:        model.Root{ID: "root-2", Name: "projects", Path: "/projects"},
			Directories: []model.WorkspaceView{{Workspace: model.Workspace{ID: "w", Name: "alpha", Path: "/projects/alpha"}}},
		},
	}}
	backend := &fakeBackend{snapshot: snapshot}
	value := newDashboard(backend, snapshot)
	value.width = 120
	value.height = 40

	rendered := ansi.Strip(value.render())
	if !strings.Contains(rendered, "✗ gone") {
		t.Fatalf("the unreadable root is not marked:\n%s", rendered)
	}
	if !strings.Contains(rendered, "▾ projects") || !strings.Contains(rendered, "alpha") {
		t.Fatalf("the healthy root did not survive its neighbour:\n%s", rendered)
	}

	value.navIndex = 0
	updated, command := value.Update(key('d', "d"))
	value = updated.(dashboard)
	if command == nil {
		t.Fatal("d produced no removal command")
	}
	updated, _ = value.Update(command())
	value = updated.(dashboard)
	if backend.removedRootID != "root-1" {
		t.Fatalf("removed %q, want root-1", backend.removedRootID)
	}
	rendered = ansi.Strip(value.render())
	if strings.Contains(rendered, "gone") || !strings.Contains(rendered, "projects") {
		t.Fatalf("removal left the wrong tree:\n%s", rendered)
	}
}

// A connection that drops used to be retried at once: the daemon replayed the
// whole recording, the client fell behind, the daemon cut it off, and round it
// went. Retries are spaced and bounded, and the last word is the user's.
func TestDashboardBacksOffRepeatedReattaches(t *testing.T) {
	tabs := []model.Tab{{ID: "tab-1", WorkspaceID: "workspace-1", Name: "1", Running: true}}
	value, backend, refresh := exitedDashboard(t, tabs, 0)

	// drop reopens the terminal the way a completed attach does, then ends its
	// stream, which is what a connection the daemon cut off looks like.
	drop := func(t *testing.T, value dashboard, open tea.Cmd) dashboard {
		t.Helper()
		updated, _ := value.Update(open())
		value = updated.(dashboard)
		if value.terminal == nil {
			t.Fatal("the reattach did not open a terminal")
		}
		updated, _ = value.Update(terminalOutputMsg{terminal: value.terminal, err: io.EOF})
		return updated.(dashboard)
	}

	// The first drop reconnects at once: a one-off should recover unnoticed.
	updated, open := value.Update(refresh())
	value = updated.(dashboard)
	if open == nil || value.reattachAttempts != 0 {
		t.Fatalf("first reattach = (command %v, retries %d), want an immediate reconnect",
			open, value.reattachAttempts)
	}
	value = drop(t, value, open)

	// Each further drop is a retry: spaced, and counted.
	for attempt := 1; attempt <= maximumReattachAttempts; attempt++ {
		updated, delayed := value.Update(refresh())
		value = updated.(dashboard)
		if delayed == nil {
			t.Fatalf("retry %d produced no command", attempt)
		}
		if value.reattachAttempts != attempt {
			t.Fatalf("retry %d recorded %d attempts", attempt, value.reattachAttempts)
		}
		// The command must be a timer rather than an immediate open, or the
		// loop this exists to damp is still a loop.
		waited := time.Now()
		message := delayed()
		if _, ok := message.(reopenTerminalMsg); !ok {
			t.Fatalf("retry %d returned %T, want a delayed reopen", attempt, message)
		}
		if elapsed := time.Since(waited); elapsed < initialReattachBackoff {
			t.Fatalf("retry %d waited %v, want at least %v", attempt, elapsed, initialReattachBackoff)
		}
		updated, open = value.Update(message)
		value = updated.(dashboard)
		if open == nil {
			t.Fatal("the backoff timer did not lead to a reattach")
		}
		value = drop(t, value, open)
	}

	// Past the ceiling romty stops on its own and says so.
	updated, command := value.Update(refresh())
	value = updated.(dashboard)
	if command != nil {
		t.Fatalf("romty kept retrying past %d attempts", maximumReattachAttempts)
	}
	if value.focus != leftPane || !strings.Contains(value.errorMessage, "keeps disconnecting") {
		t.Fatalf("giving up = (focus %v, error %q)", value.focus, value.errorMessage)
	}
	if backend.openedTab != "tab-1" {
		t.Fatalf("opened %q, want the dropping tab", backend.openedTab)
	}

}

// Opening a workspace is the user asking again, so pending retries start over
// and the next attach is immediate rather than delayed.
func TestDashboardForgetsRetriesWhenTheUserSelectsAgain(t *testing.T) {
	tabs := []model.Tab{{ID: "tab-1", WorkspaceID: "workspace-1", Name: "1", Running: true}}
	value, backend, refresh := exitedDashboard(t, tabs, 0)

	// One immediate reconnect, one drop, one counted retry.
	updated, open := value.Update(refresh())
	value = updated.(dashboard)
	updated, _ = value.Update(open())
	value = updated.(dashboard)
	updated, _ = value.Update(terminalOutputMsg{terminal: value.terminal, err: io.EOF})
	value = updated.(dashboard)
	updated, _ = value.Update(refresh())
	value = updated.(dashboard)
	if value.reattachAttempts == 0 || value.reattachTab == "" {
		t.Fatalf("no retry is pending: %d on %q", value.reattachAttempts, value.reattachTab)
	}

	updated, _ = value.Update(workspaceMsg{
		value:    model.Workspace{ID: "workspace-1", Name: "alpha", Path: "/projects/alpha"},
		snapshot: backend.snapshot,
		tabID:    "tab-1",
	})
	value = updated.(dashboard)
	if value.reattachTab != "" || value.reattachAttempts != 0 {
		t.Fatalf("a user selection left %d retries pending on %q",
			value.reattachAttempts, value.reattachTab)
	}
}

func TestReattachBackoffGrowsAndIsCapped(t *testing.T) {
	previous := time.Duration(0)
	for attempt := 1; attempt <= 8; attempt++ {
		backoff := reattachBackoff(attempt)
		if backoff < previous {
			t.Fatalf("backoff shrank at attempt %d: %v after %v", attempt, backoff, previous)
		}
		if backoff > maximumReattachBackoff {
			t.Fatalf("backoff at attempt %d = %v, want at most %v", attempt, backoff, maximumReattachBackoff)
		}
		previous = backoff
	}
	if reattachBackoff(1) != initialReattachBackoff {
		t.Fatalf("first backoff = %v, want %v", reattachBackoff(1), initialReattachBackoff)
	}
}

// The terminal's origin, the right pane's width and the mouse translation all
// assume the separator and tab bar are exactly as wide and tall as drawn. That
// agreement is not something the compiler can check, so check it here: change
// either and the cursor and the mouse silently point at the wrong cell.
func TestLayoutMatchesWhatIsRendered(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 120
	value.height = 40

	head, body := value.paneSeparators()
	for name, separator := range map[string]string{"first row": head, "other rows": body} {
		if width := lipgloss.Width(ansi.Strip(separator)); width != separatorWidth {
			t.Fatalf("the %s separator is %d cells wide, but the layout assumes %d",
				name, width, separatorWidth)
		}
	}

	bar := renderTabBar(value.styles, nil, 0, 40)
	if len(bar) != terminalTop {
		t.Fatalf("the tab bar is %d rows tall, but terminalTop is %d", len(bar), terminalTop)
	}

	// The origin has to be where the terminal's first cell actually lands.
	view := value.dimensions()
	originX, originY := view.terminalOrigin()
	if originX != view.leftWidth+separatorWidth || originY != terminalTop {
		t.Fatalf("terminal origin = (%d,%d), want (%d,%d)",
			originX, originY, view.leftWidth+separatorWidth, terminalTop)
	}
	if view.leftWidth+separatorWidth+view.rightWidth != value.width {
		t.Fatalf("panes and separator cover %d columns, want the full %d",
			view.leftWidth+separatorWidth+view.rightWidth, value.width)
	}
}

func TestDashboardRefusesScrollbackWithoutTerminal(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})

	for _, message := range []tea.KeyPressMsg{
		key(tea.KeyF7, ""),
		tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModShift}),
		tea.KeyPressMsg(tea.Key{Code: '\\', Mod: tea.ModCtrl}),
	} {
		updated, _ := value.Update(message)
		value = updated.(dashboard)
		if value.scrollback {
			t.Fatalf("%q entered scrollback with no terminal open", message.String())
		}
	}
	if value.View().MouseMode != tea.MouseModeNone {
		t.Fatalf("mouse mode = %v, want none", value.View().MouseMode)
	}
}

func TestDashboardKeepsGlobalKeysAtOnePrecedence(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.errorMessage = "boom"

	// Every function key works with a modal open, and each clears a stale error.
	for _, step := range []struct {
		message tea.KeyPressMsg
		want    modal
	}{
		{message: key(tea.KeyF1, ""), want: aboutModal},
		{message: key(tea.KeyF3, ""), want: configModal},
		{message: key(tea.KeyF6, ""), want: shutdownModal},
		{message: key(tea.KeyF1, ""), want: aboutModal},
	} {
		updated, command := value.Update(step.message)
		value = updated.(dashboard)
		if value.modal != step.want || command != nil || value.errorMessage != "" {
			t.Fatalf("%q with a modal open = (modal %v, command %v, error %q), want modal %v",
				step.message.String(), value.modal, command, value.errorMessage, step.want)
		}
	}
	if rendered := ansi.Strip(value.render()); strings.Contains(rendered, "ERROR") || !strings.Contains(rendered, "Esc") {
		t.Fatalf("status bar keeps the stale error instead of the modal hint:\n%s", rendered)
	}

	updated, _ := value.Update(key(tea.KeyF2, ""))
	value = updated.(dashboard)
	if !value.inputMode || value.modal != noModal {
		t.Fatalf("F2 with a modal open = (input %v, modal %v), want root input", value.inputMode, value.modal)
	}

	// Root input owns every key except its own cancel, so F4 cannot discard it.
	value.input = "/projects"
	updated, command := value.Update(key(tea.KeyF4, ""))
	value = updated.(dashboard)
	if value.result.Quit || command != nil || value.input != "/projects" {
		t.Fatalf("F4 while typing = (quit %v, command %v, input %q), want the input kept",
			value.result.Quit, command, value.input)
	}
	updated, _ = value.Update(key(tea.KeyEscape, ""))
	value = updated.(dashboard)
	if value.inputMode || value.input != "" {
		t.Fatalf("Esc while typing = (input mode %v, input %q), want the prompt cancelled", value.inputMode, value.input)
	}
}

func shortcutOrder(rendered string, values ...string) bool {
	lines := strings.Split(ansi.Strip(rendered), "\n")
	status := lines[len(lines)-1]
	previous := -1
	for _, value := range values {
		index := strings.Index(status, value)
		if index <= previous {
			return false
		}
		previous = index
	}
	return true
}

func TestDashboardKeepsNativeMouseSelectionAndUsesKeyboardFocus(t *testing.T) {
	stream := newMemoryStream("")
	terminal := newEmbeddedTerminal("tab-1", stream, 40, 10)
	defer terminal.closeTerminal()

	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 120
	value.height = 40
	value.terminal = terminal
	value.focus = terminalPane

	if view := value.View(); view.MouseMode != tea.MouseModeNone {
		t.Fatalf("initial mouse mode = %v, want none", view.MouseMode)
	}
	if rendered := value.render(); strings.Contains(rendered, "Ctrl+G") || strings.Contains(rendered, "mouse focus") {
		t.Fatalf("mouse focus mode is still advertised:\n%s", rendered)
	}

	updated, _ := value.Update(tea.KeyPressMsg(tea.Key{Code: '\\', Mod: tea.ModCtrl}))
	value = updated.(dashboard)
	if value.focus != leftPane {
		t.Fatalf("Ctrl+\\ focus = %v, want workspace pane", value.focus)
	}
	separator := value.styles.dividerActive.Render("◀") + value.styles.divider.Render("│") + " "
	if rendered := value.render(); !strings.Contains(rendered, separator) {
		t.Fatalf("workspace focus is not visible:\n%s", rendered)
	}
	if view := value.View(); view.MouseMode != tea.MouseModeNone {
		t.Fatalf("mouse mode after Ctrl+\\ = %v, want none", view.MouseMode)
	}
}

func TestDashboardMovesWorkspaceAndTabCursorBeforeConfirming(t *testing.T) {
	firstWorkspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	secondWorkspace := model.Workspace{ID: "workspace-2", RootID: "root-1", Name: "beta", Path: "/projects/beta"}
	firstTab := model.Tab{ID: "tab-1", WorkspaceID: firstWorkspace.ID, Name: "one", Running: true}
	secondTab := model.Tab{ID: "tab-2", WorkspaceID: secondWorkspace.ID, Name: "two", Running: true}
	thirdTab := model.Tab{ID: "tab-3", WorkspaceID: secondWorkspace.ID, Name: "three", Running: true}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{
			{Workspace: firstWorkspace, Tabs: []model.Tab{firstTab}},
			{Workspace: secondWorkspace, Tabs: []model.Tab{secondTab, thirdTab}},
		},
	}}}
	backend := &fakeBackend{snapshot: snapshot, workspace: secondWorkspace}
	terminal := newEmbeddedTerminal(firstTab.ID, newMemoryStream(""), 40, 10)
	defer terminal.closeTerminal()
	value := newDashboard(backend, snapshot)
	value.selectedWorkspaceID = firstWorkspace.ID
	value.selectedPath = firstWorkspace.Path
	value.terminal = terminal

	updated, command := value.Update(key(tea.KeyDown, ""))
	value = updated.(dashboard)
	if command != nil || value.navIndex != 1 {
		t.Fatalf("first workspace cursor move = (command %v, index %d)", command, value.navIndex)
	}
	updated, command = value.Update(key(tea.KeyDown, ""))
	value = updated.(dashboard)
	if command != nil || value.navIndex != 2 || value.terminal.id != firstTab.ID {
		t.Fatalf("workspace cursor move = (command %v, index %d, terminal %q)", command, value.navIndex, value.terminal.id)
	}
	if rendered := value.render(); !strings.Contains(rendered, value.styles.tabSelected.Render(" two ")) {
		t.Fatalf("candidate workspace tabs are not visible:\n%s", rendered)
	}

	updated, command = value.Update(key(tea.KeyRight, ""))
	value = updated.(dashboard)
	if command != nil || value.tabIndex != 1 || backend.openedTab != "" || value.terminal.id != firstTab.ID {
		t.Fatalf("tab cursor move opened a terminal: command=%v index=%d opened=%q terminal=%q", command, value.tabIndex, backend.openedTab, value.terminal.id)
	}

	updated, selectCommand := value.Update(key(tea.KeyEnter, ""))
	value = updated.(dashboard)
	if selectCommand == nil {
		t.Fatal("Enter did not select the workspace and tab")
	}
	updated, openCommand := value.Update(selectCommand())
	value = updated.(dashboard)
	if openCommand == nil {
		t.Fatal("confirmed tab did not produce an open command")
	}
	openCommand()
	if backend.openedTab != thirdTab.ID {
		t.Fatalf("opened tab = %q, want %q", backend.openedTab, thirdTab.ID)
	}
}

func TestDashboardSelectsPlusAndCreatesTabOnEnter(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	tabs := []model.Tab{
		{ID: "tab-1", WorkspaceID: workspace.ID, Name: "1", Running: true},
		{ID: "tab-2", WorkspaceID: workspace.ID, Name: "2", Running: true},
	}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace, Tabs: tabs}},
	}}}
	backend := &fakeBackend{
		snapshot:   snapshot,
		workspace:  workspace,
		createdTab: model.Tab{ID: "tab-3", WorkspaceID: workspace.ID, Name: "3", Running: true},
	}
	value := newDashboard(backend, snapshot)
	value.navIndex = 1
	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path

	updated, command := value.Update(key(tea.KeyLeft, ""))
	value = updated.(dashboard)
	if command != nil || value.tabIndex != len(tabs) {
		t.Fatalf("left from first tab = (command %v, index %d), want + index %d", command, value.tabIndex, len(tabs))
	}
	if rendered := value.render(); !strings.Contains(rendered, value.styles.tabSelected.Render(" + ")) {
		t.Fatalf("+ cursor is not visible:\n%s", rendered)
	}

	updated, selectCommand := value.Update(key(tea.KeyEnter, ""))
	value = updated.(dashboard)
	if selectCommand == nil {
		t.Fatal("Enter on + did not select the workspace")
	}
	updated, createCommand := value.Update(selectCommand())
	value = updated.(dashboard)
	if createCommand == nil {
		t.Fatal("Enter on + did not produce a create command")
	}
	createdMessage := createCommand()
	if backend.createCount != 1 || backend.openedTab != "" {
		t.Fatalf("Enter on + created %d tabs and opened %q", backend.createCount, backend.openedTab)
	}
	updated, openCommand := value.Update(createdMessage)
	value = updated.(dashboard)
	if openCommand == nil {
		t.Fatal("created tab did not produce an open command")
	}
}

func TestDashboardHighlightsNavigationAndShowsOpenTabs(t *testing.T) {
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root-1", Name: "nalbam", Path: "/projects"},
		Directories: []model.WorkspaceView{
			{
				Workspace: model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "SnowClash", Path: "/projects/SnowClash"},
				Tabs: []model.Tab{
					{ID: "tab-1", Name: "1", Running: true},
					{ID: "tab-2", Name: "2", Running: false},
					{ID: "tab-3", Name: "3", Running: true},
				},
			},
			{Workspace: model.Workspace{ID: "workspace-2", RootID: "root-1", Name: "TankClash", Path: "/projects/TankClash"}},
		},
	}}}
	value := newDashboard(&fakeBackend{}, snapshot)
	value.width = 120
	value.height = 40
	value.navIndex = 1
	value.selectedWorkspaceID = "workspace-1"
	value.selectedPath = "/projects/SnowClash"

	rendered := value.render()
	plain := ansi.Strip(rendered)
	if strings.Contains(rendered, "Terminal") || strings.Contains(rendered, "/projects/SnowClash") {
		t.Fatalf("terminal title or workspace path is still visible:\n%s", rendered)
	}
	if strings.Contains(plain, "> ") {
		t.Fatalf("navigation still contains arrow cursor:\n%s", rendered)
	}
	if !strings.Contains(plain, "▾ nalbam") || !strings.Contains(plain, "▌ ├─ SnowClash") {
		t.Fatalf("navigation tree or selection color is missing:\n%s", rendered)
	}
	view := value.dimensions()
	leftWidth, bodyHeight := view.leftWidth, view.bodyHeight
	navigation := ansi.Strip(strings.Join(value.renderNavigation(leftWidth, bodyHeight), "\n"))
	snowClash := ""
	for _, line := range strings.Split(navigation, "\n") {
		if strings.Contains(line, "SnowClash") {
			snowClash = strings.TrimRight(line, " ")
		}
	}
	if !strings.HasSuffix(snowClash, "●●") || strings.HasSuffix(snowClash, "●●●") {
		t.Fatalf("SnowClash row = %q, want markers for exactly the two running tabs", snowClash)
	}
	if strings.Contains(plain, "2 exited") {
		t.Fatalf("exited tab was included in terminal tabs:\n%s", rendered)
	}
	if !strings.Contains(rendered, value.styles.tabSelected.Render(" 1 ")) || !strings.Contains(rendered, value.styles.tab.Render(" 3 ")) {
		t.Fatalf("terminal tabs are not styled as active and inactive tabs:\n%s", rendered)
	}
	status := value.styles.shortcutKey.Render(" F2 ") + " " + value.styles.shortcutDescription.Render("add root")
	if !strings.Contains(rendered, status) {
		t.Fatalf("status shortcuts and descriptions are not separated:\n%s", rendered)
	}
	if !strings.Contains(plain, "  └─ TankClash") {
		t.Fatalf("workspace without tabs is missing:\n%s", rendered)
	}

	value.tabIndex = 3
	updated, _ := value.Update(snapshotMsg{value: snapshot})
	value = updated.(dashboard)
	if value.tabIndex != 2 {
		t.Fatalf("tab index after exited tab removal = %d, want + index 2", value.tabIndex)
	}
}

func TestDashboardUsesCompactLeftPane(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 120
	value.height = 40

	view := value.dimensions()
	leftWidth, rightWidth := view.leftWidth, view.rightWidth
	if leftWidth != 28 || rightWidth != 89 {
		t.Fatalf("pane widths = %d/%d, want 28/89", leftWidth, rightWidth)
	}

	value = newDashboardWithConfig(&fakeBackend{}, model.Snapshot{}, "", Config{LeftWidth: 24})
	value.width = 120
	view = value.dimensions()
	leftWidth, rightWidth = view.leftWidth, view.rightWidth
	if leftWidth != 24 || rightWidth != 93 {
		t.Fatalf("configured pane widths = %d/%d, want 24/93", leftWidth, rightWidth)
	}
}

func TestDashboardAdaptsChromeToTerminalBackground(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	if value.Init() == nil {
		t.Fatal("Init() did not request the terminal background color")
	}
	darkAccent := value.styles.paneTitleActive.GetForeground()

	updated, command := value.Update(tea.BackgroundColorMsg{Color: color.White})
	value = updated.(dashboard)
	if command != nil {
		t.Fatalf("background color update command = %v, want nil", command)
	}
	if reflect.DeepEqual(darkAccent, value.styles.paneTitleActive.GetForeground()) {
		t.Fatal("light and dark palettes use the same accent color")
	}
	if !strings.Contains(value.render(), value.styles.paneTitleActive.Render(" romty ")) {
		t.Fatal("light palette was not applied to the dashboard")
	}
}

func TestDashboardKeepsWideWorkspaceNamesWithinViewport(t *testing.T) {
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root-1", Name: "플랫폼-프로젝트-모음", Path: "/projects"},
		Directories: []model.WorkspaceView{{
			Workspace: model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "아주-긴-워크스페이스-이름", Path: "/projects/long"},
		}},
	}}}
	value := newDashboard(&fakeBackend{}, snapshot)
	value.width = 40
	value.height = 12
	value.navIndex = 1

	for index, line := range strings.Split(value.render(), "\n") {
		if lineWidth := lipgloss.Width(line); lineWidth > value.width {
			t.Fatalf("rendered line %d width = %d, want at most %d:\n%s", index, lineWidth, value.width, line)
		}
	}
}

func TestDashboardKeepsNavigationCursorVisible(t *testing.T) {
	directories := make([]model.WorkspaceView, 0, 40)
	for index := range 40 {
		name := fmt.Sprintf("workspace-%02d", index)
		directories = append(directories, model.WorkspaceView{
			Workspace: model.Workspace{ID: name, RootID: "root-1", Name: name, Path: "/projects/" + name},
		})
	}
	value := newDashboard(&fakeBackend{}, model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "nalbam", Path: "/projects"},
		Directories: directories,
	}}})
	value.width = 120
	value.height = 30
	view := value.dimensions()
	leftWidth, bodyHeight := view.leftWidth, view.bodyHeight

	for _, navIndex := range []int{0, 20, len(directories)} {
		value.navIndex = navIndex
		lines := value.renderNavigation(leftWidth, bodyHeight)
		if len(lines) > bodyHeight {
			t.Fatalf("navigation at index %d rendered %d lines, want at most %d", navIndex, len(lines), bodyHeight)
		}
		item, ok := value.navigationItem()
		if !ok {
			t.Fatalf("navigation item at index %d is missing", navIndex)
		}
		if !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), item.workspace.Name) {
			t.Fatalf("navigation at index %d does not show the selected %q:\n%s",
				navIndex, item.workspace.Name, ansi.Strip(strings.Join(lines, "\n")))
		}
	}
}

func TestDashboardShowsAboutModalWithoutReplacingDashboard(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 100
	value.height = 30

	updated, _ := value.Update(key(tea.KeyF1, ""))
	value = updated.(dashboard)
	if value.modal != aboutModal {
		t.Fatalf("modal = %v, want about", value.modal)
	}
	rendered := value.render()
	// The pane title only ever comes from the dashboard behind the modal; the
	// modal body renders "romty" too, so a bare substring proves nothing.
	if !strings.Contains(rendered, "About") || !strings.Contains(rendered, "Persistent terminal workspace manager") ||
		!strings.Contains(rendered, value.styles.paneTitleActive.Render(" romty ")) {
		t.Fatalf("about modal or dashboard background is missing:\n%s", rendered)
	}

	updated, _ = value.Update(key(tea.KeyEscape, ""))
	value = updated.(dashboard)
	if value.modal != noModal || strings.Contains(value.render(), "Persistent terminal workspace manager") {
		t.Fatalf("about modal did not close:\n%s", value.render())
	}
}

func TestDashboardShowsAllShortcutsInHelpModal(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 100
	value.height = 40
	lines := strings.Split(value.render(), "\n")
	status := lines[len(lines)-1]
	if strings.Contains(status, value.styles.shortcutKey.Render(" + ")) || strings.Contains(status, value.styles.shortcutKey.Render(" , ")) {
		t.Fatalf("hidden workspace shortcuts are shown in the status bar:\n%s", status)
	}

	updated, command := value.Update(key('?', "?"))
	value = updated.(dashboard)
	if value.modal != helpModal || command != nil {
		t.Fatalf("? result = (modal %v, command %v), want help modal", value.modal, command)
	}
	bodyHeight := value.dimensions().bodyHeight
	modalLines := value.renderModal(value.width, bodyHeight)
	plainLines := strings.Split(ansi.Strip(strings.Join(modalLines, "\n")), "\n")
	plain := strings.Join(plainLines, "\n")
	for _, section := range []string{"COMMANDS", "NAVIGATION", "TERMINAL", "OTHER"} {
		if !strings.Contains(plain, section) {
			t.Fatalf("help modal does not contain %q section:\n%s", section, plain)
		}
	}
	shortcuts := []struct {
		keys        []string
		description string
	}{
		{keys: []string{"F1", "i"}, description: "About"},
		{keys: []string{"F2", "a"}, description: "Add root"},
		{keys: []string{",", "F3"}, description: "Config"},
		{keys: []string{"q", "F4"}, description: "Quit"},
		{keys: []string{"r", "F5"}, description: "Refresh"},
		{keys: []string{"F6"}, description: "Stop daemon"},
		{keys: []string{"F7"}, description: "Scrollback"},
		{keys: []string{"?"}, description: "Help"},
		{keys: []string{"↑/↓", "j/k"}, description: "Select workspace"},
		{keys: []string{"←/→", "h/l"}, description: "Select tab / +"},
		{keys: []string{"Enter"}, description: "Open / confirm"},
		{keys: []string{"Tab"}, description: "Focus terminal"},
		{keys: []string{"Ctrl+\\"}, description: "Focus workspace"},
		{keys: []string{"F7", "Ctrl+\\"}, description: "Enter / leave"},
		{keys: []string{"PgUp/PgDn"}, description: "Scroll a page"},
		{keys: []string{"Shift+PgUp"}, description: "Enter at a page back"},
		{keys: []string{"Wheel"}, description: "Scroll with the mouse"},
		{keys: []string{"Ctrl+C"}, description: "Quit"},
		{keys: []string{"←/→", "[/]"}, description: "Resize workspace pane"},
		{keys: []string{"Esc"}, description: "Close / cancel"},
	}
	for _, shortcut := range shortcuts {
		if !helpContainsShortcut(plainLines, shortcut.description, shortcut.keys...) {
			t.Fatalf("help modal does not contain %v %q shortcut:\n%s", shortcut.keys, shortcut.description, plain)
		}
	}
	// The function key is the one that works everywhere, so it leads the row
	// and the pane-only alias follows it.
	for _, row := range []struct {
		description string
		first       string
		second      string
	}{
		{description: "About", first: "F1", second: "i"},
		{description: "Add root", first: "F2", second: "a"},
		{description: "Config", first: "F3", second: ","},
		{description: "Quit", first: "F4", second: "q"},
		{description: "Refresh", first: "F5", second: "r"},
	} {
		line := helpLine(plainLines, row.description)
		if first, second := strings.Index(line, row.first), strings.Index(line, row.second); first < 0 || first > second {
			t.Fatalf("%q row = %q, want %s before %s", row.description, line, row.first, row.second)
		}
	}
	if len(modalLines) > value.height-2 {
		t.Fatalf("help modal height = %d, want at most %d", len(modalLines), value.height-2)
	}
	for index, line := range modalLines {
		if lineWidth := lipgloss.Width(line); lineWidth > 64 {
			t.Fatalf("help modal line %d width = %d, want at most 64", index, lineWidth)
		}
	}
}

func TestDashboardScrollsHelpModalOnShortTerminals(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 100
	value.height = 20
	updated, _ := value.Update(key('?', "?"))
	value = updated.(dashboard)

	bodyHeight := value.dimensions().bodyHeight
	lines := strings.Split(value.render(), "\n")
	modalLines := value.renderModal(value.width, bodyHeight)
	if len(modalLines) > bodyHeight {
		t.Fatalf("help modal height = %d, want at most %d", len(modalLines), bodyHeight)
	}
	if !strings.HasPrefix(ansi.Strip(modalLines[0]), "╭─ Help 1-") ||
		!strings.HasPrefix(ansi.Strip(modalLines[len(modalLines)-1]), "╰─") {
		t.Fatalf("help modal is not a terminated box with a range title:\n%s", ansi.Strip(strings.Join(modalLines, "\n")))
	}
	if !strings.Contains(ansi.Strip(lines[len(lines)-1]), "scroll") {
		t.Fatalf("status bar does not offer the scroll shortcut:\n%s", ansi.Strip(lines[len(lines)-1]))
	}

	// The last entry is off screen at this height and must be reachable.
	for range value.helpEntries() {
		updated, _ = value.Update(key(tea.KeyDown, ""))
		value = updated.(dashboard)
	}
	plain := ansi.Strip(strings.Join(value.renderModal(value.width, bodyHeight), "\n"))
	if !strings.Contains(plain, "Close / cancel") {
		t.Fatalf("help modal did not scroll to the last shortcut:\n%s", plain)
	}
	if strings.Contains(plain, "About") {
		t.Fatalf("help modal kept the first shortcut after scrolling to the end:\n%s", plain)
	}
}

func helpLine(lines []string, description string) string {
	for _, line := range lines {
		if strings.Contains(line, description) {
			return line
		}
	}
	return ""
}

func helpContainsShortcut(lines []string, description string, keys ...string) bool {
	for _, line := range lines {
		if !strings.Contains(line, description) {
			continue
		}
		matches := true
		for _, key := range keys {
			matches = matches && strings.Contains(line, key)
		}
		if matches {
			return true
		}
	}
	return false
}

func TestDashboardDoesNotCaptureWorkspaceShortcutsInTerminal(t *testing.T) {
	terminal := newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)
	defer terminal.closeTerminal()
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.focus = terminalPane
	value.terminal = terminal

	for _, message := range []tea.KeyPressMsg{
		key('i', "i"), key('a', "a"), key(',', ","), key('q', "q"), key('r', "r"), key('+', "+"), key('?', "?"),
	} {
		updated, command := value.Update(message)
		value = updated.(dashboard)
		if command != nil || value.inputMode || value.modal != noModal {
			t.Fatalf("terminal key %q was captured: command=%v input=%v modal=%v", message.String(), command, value.inputMode, value.modal)
		}
	}
}

func TestDashboardAdjustsAndPersistsLeftPaneWidth(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 120
	value.height = 40
	value.configPath = configPath

	updated, _ := value.Update(key(',', ","))
	value = updated.(dashboard)
	if value.modal != configModal {
		t.Fatalf("modal = %v, want config", value.modal)
	}
	updated, saveCommand := value.Update(key(tea.KeyRight, ""))
	value = updated.(dashboard)
	leftWidth := value.dimensions().leftWidth
	if saveCommand == nil || leftWidth != 29 {
		t.Fatalf("right adjustment = (command %v, width %d), want saved width 29", saveCommand, leftWidth)
	}
	updated, _ = value.Update(saveCommand())
	value = updated.(dashboard)
	loaded, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if loaded.LeftWidth != 29 || !strings.Contains(value.render(), "Left pane width: 29") {
		t.Fatalf("persisted config = %#v\n%s", loaded, value.render())
	}

	updated, _ = value.Update(key(tea.KeyEscape, ""))
	value = updated.(dashboard)
	if value.modal != noModal {
		t.Fatalf("modal after Esc = %v, want none", value.modal)
	}
}

func key(code rune, text string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Text: text})
}
