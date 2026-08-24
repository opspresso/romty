package ui

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

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

	if rendered := value.render(); !strings.Contains(rendered, selectStyle+"▾ projects") || !strings.Contains(rendered, "  cloned") {
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
	if rendered := value.render(); !strings.Contains(rendered, "▾ projects  ●") {
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
	if backend.snapshotCount != 1 || !strings.Contains(value.render(), "  cloned") {
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

	updated, _ := value.Update(key('a', "a"))
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

	updated, _ := value.Update(key('a', "a"))
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

	value = newDashboard(backend, model.Snapshot{})
	value.width = 120
	rendered := value.render()
	for _, status := range []string{"[F1]", "[F2]", "[F5]", "[Ctrl+C]"} {
		if !strings.Contains(rendered, focusStyle+status+resetStyle) {
			t.Fatalf("status bar does not contain %s:\n%s", status, rendered)
		}
	}
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
	if rendered := value.render(); !strings.Contains(rendered, "\x1b[1;96m◀\x1b[0m│ ") {
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
	if rendered := value.render(); !strings.Contains(rendered, activeTabStyle+" two "+resetStyle) {
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
	if rendered := value.render(); !strings.Contains(rendered, activeTabStyle+" + "+resetStyle) {
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
	if strings.Contains(rendered, "Terminal") || strings.Contains(rendered, "/projects/SnowClash") {
		t.Fatalf("terminal title or workspace path is still visible:\n%s", rendered)
	}
	if strings.Contains(rendered, "> ") {
		t.Fatalf("navigation still contains arrow cursor:\n%s", rendered)
	}
	if !strings.Contains(rendered, "▾ nalbam") || !strings.Contains(rendered, "\x1b[1;92m  SnowClash  ●●") {
		t.Fatalf("navigation tree or selection color is missing:\n%s", rendered)
	}
	if strings.Contains(rendered, "SnowClash  ●●●") {
		t.Fatalf("exited tab was included in workspace markers:\n%s", rendered)
	}
	if strings.Contains(rendered, "2 exited") {
		t.Fatalf("exited tab was included in terminal tabs:\n%s", rendered)
	}
	if !strings.Contains(rendered, "\x1b[1;30;106m 1 \x1b[0m") || !strings.Contains(rendered, "\x1b[36m 3 \x1b[0m") {
		t.Fatalf("terminal tabs are not styled as active and inactive tabs:\n%s", rendered)
	}
	if !strings.Contains(rendered, "\x1b[1;96m[F2]\x1b[0m add") {
		t.Fatalf("status shortcuts and descriptions are not separated:\n%s", rendered)
	}
	if !strings.Contains(rendered, "  TankClash") {
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

func TestDashboardShowsAboutModalWithoutReplacingDashboard(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 100
	value.height = 30

	updated, _ := value.Update(key('?', "?"))
	value = updated.(dashboard)
	if value.modal != aboutModal {
		t.Fatalf("modal = %v, want about", value.modal)
	}
	rendered := value.render()
	if !strings.Contains(rendered, "About") || !strings.Contains(rendered, "Persistent terminal workspace manager") || !strings.Contains(rendered, "Workspaces") {
		t.Fatalf("about modal or dashboard background is missing:\n%s", rendered)
	}

	updated, _ = value.Update(key(tea.KeyEscape, ""))
	value = updated.(dashboard)
	if value.modal != noModal || strings.Contains(value.render(), "Persistent terminal workspace manager") {
		t.Fatalf("about modal did not close:\n%s", value.render())
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
