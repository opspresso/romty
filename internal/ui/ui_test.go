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
	ensuredRootID  string
	ensuredPath    string
	openedTab      string
	snapshotCount  int
	shutdownCount  int
	stream         *memoryStream
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

func (f *fakeBackend) AddRoot(path string) (model.Snapshot, error) {
	f.addedPath = path
	return f.snapshot, nil
}

func (f *fakeBackend) Snapshot() (model.Snapshot, error) {
	f.snapshotCount++
	return f.snapshot, nil
}

func (f *fakeBackend) EnsureWorkspace(rootID, path string) (model.Workspace, error) {
	f.ensuredRootID = rootID
	f.ensuredPath = path
	return f.workspace, nil
}

func (f *fakeBackend) CreateTab(workspaceID string, columns, rows uint16) (model.Tab, error) {
	f.createCount++
	f.createdColumns = columns
	f.createdRows = rows
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
	f.stream = newMemoryStream("\x1b[2J\x1b[Hembedded terminal")
	return f.stream, nil
}

func (f *fakeBackend) Resize(_ string, columns, rows uint16) error {
	f.createdColumns = columns
	f.createdRows = rows
	return nil
}

func (f *fakeBackend) Shutdown() error {
	f.shutdownCount++
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
	_, rightWidth, _, terminalHeight := value.dimensions()
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
	if view.MouseMode != tea.MouseModeCellMotion || view.Cursor != nil {
		t.Fatalf("scrollback view = (mouse %v, cursor %v), want mouse on and no cursor", view.MouseMode, view.Cursor)
	}

	updated, _ = value.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	value = updated.(dashboard)
	if value.scrollOffset != wheelScrollLines {
		t.Fatalf("wheel up offset = %d, want %d", value.scrollOffset, wheelScrollLines)
	}
	if slices.Equal(plainRows(value.terminal.renderViewport(value.scrollOffset)), live) {
		t.Fatal("scrolling up did not change the rendered viewport")
	}
	updated, _ = value.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
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
	leftWidth, _, bodyHeight, _ := value.dimensions()
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

	leftWidth, rightWidth, _, _ := value.dimensions()
	if leftWidth != 28 || rightWidth != 89 {
		t.Fatalf("pane widths = %d/%d, want 28/89", leftWidth, rightWidth)
	}

	value = newDashboardWithConfig(&fakeBackend{}, model.Snapshot{}, "", Config{LeftWidth: 24})
	value.width = 120
	leftWidth, rightWidth, _, _ = value.dimensions()
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
	leftWidth, _, bodyHeight, _ := value.dimensions()

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
	_, _, bodyHeight, _ := value.dimensions()
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
		{keys: []string{"i", "F1"}, description: "About"},
		{keys: []string{"a", "F2"}, description: "Add root"},
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

	_, _, bodyHeight, _ := value.dimensions()
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
	leftWidth, _, _, _ := value.dimensions()
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
