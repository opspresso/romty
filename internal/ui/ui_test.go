package ui

import (
	"bytes"
	"errors"
	"fmt"
	"image/color"
	"io"
	"os"
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

	"github.com/opspresso/romty/internal/agenthooks"
	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/sound"
	"github.com/opspresso/romty/internal/version"
)

func TestMain(m *testing.M) {
	agentAnimationInterval = 0
	agentRefreshInterval = 0
	playSound = func(sound.Kind) error { return nil }
	os.Exit(m.Run())
}

type fakeBackend struct {
	snapshot               model.Snapshot
	agentStatuses          map[string]model.AgentStatus
	workspace              model.Workspace
	createdTab             model.Tab
	createdColumns         uint16
	createdRows            uint16
	createCount            int
	addedPath              string
	removedRootID          string
	removedWorkspaceRootID string
	removedWorkspacePath   string
	ensuredRootID          string
	ensuredPath            string
	openedTab              string
	snapshotCount          int
	shutdownCount          int
	stream                 *memoryStream
	// The real client fails: the daemon can be unreachable, past its
	// deadline, or refuse the request. A fake that only ever succeeds hides
	// every branch that handles those.
	failures map[string]error
}

type memoryStream struct {
	reader   *bytes.Reader
	mu       sync.Mutex
	written  bytes.Buffer
	writeErr error
	closed   bool
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
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return s.written.Write(data)
}

func (s *memoryStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *memoryStream) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
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

func (f *fakeBackend) RemoveWorkspace(rootID, path string) (model.Snapshot, error) {
	f.removedWorkspaceRootID = rootID
	f.removedWorkspacePath = path
	if err := f.failure("RemoveWorkspace"); err != nil {
		return model.Snapshot{}, err
	}
	for rootIndex := range f.snapshot.Roots {
		root := &f.snapshot.Roots[rootIndex]
		if root.Root.ID != rootID {
			continue
		}
		remaining := make([]model.WorkspaceView, 0, len(root.Directories))
		for _, directory := range root.Directories {
			if directory.Workspace.Path != path {
				remaining = append(remaining, directory)
			}
		}
		root.Directories = remaining
	}
	return f.snapshot, nil
}

func (f *fakeBackend) Snapshot() (model.Snapshot, error) {
	f.snapshotCount++
	if err := f.failure("Snapshot"); err != nil {
		return model.Snapshot{}, err
	}
	return f.snapshot, nil
}

func (f *fakeBackend) AgentStatuses() (map[string]model.AgentStatus, error) {
	if err := f.failure("AgentStatuses"); err != nil {
		return nil, err
	}
	return f.agentStatuses, nil
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

func (f *fakeBackend) OpenTerminal(tabID string) (io.ReadWriteCloser, []byte, error) {
	f.openedTab = tabID
	if err := f.failure("OpenTerminal"); err != nil {
		return nil, nil, err
	}
	f.stream = newMemoryStream("live output")
	return f.stream, []byte("\x1b[2J\x1b[Hembedded terminal"), nil
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
	updated, command = value.Update(commandMessage[workspaceMsg](t, command))
	value = updated.(dashboard)
	if value.selectedWorkspaceID != workspace.ID || value.focus != leftPane {
		t.Fatalf("selected workspace = %q, focus = %v", value.selectedWorkspaceID, value.focus)
	}
	if command == nil {
		t.Fatal("workspace without tabs did not create a terminal")
	}
	updated, openCommand := value.Update(commandMessage[tabMsg](t, command))
	value = updated.(dashboard)
	if openCommand == nil {
		t.Fatal("created tab did not open an embedded terminal")
	}
	updated, readCommand := value.Update(commandMessage[terminalOpenedMsg](t, openCommand))
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

	followup := readCommand()
	commands, ok := followup.(tea.BatchMsg)
	if !ok {
		t.Fatalf("terminal followup = %T, want read and resize commands", followup)
	}
	for _, command := range commands {
		if message := command(); message != nil {
			updated, _ = value.Update(message)
			value = updated.(dashboard)
		}
	}
	rendered := value.render()
	if !strings.Contains(rendered, "projects") || !strings.Contains(rendered, "embedded terminal") {
		t.Fatalf("rendered dashboard does not contain both panes:\n%s", rendered)
	}
	updated, _ = value.Update(controlSlashKey())
	value = updated.(dashboard)
	if value.focus != leftPane || value.terminal == nil {
		t.Fatalf("Ctrl+/ focus = %v, terminal = %v", value.focus, value.terminal)
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
	value.setNavigation(0)

	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "▌▾ projects") || !strings.Contains(rendered, "  - cloned") {
		t.Fatalf("root and indented workspace are not distinguishable:\n%s", rendered)
	}
	updated, selectCommand := value.Update(key(tea.KeyEnter, ""))
	value = updated.(dashboard)
	if selectCommand == nil {
		t.Fatal("root selection command = nil")
	}
	updated, createCommand := value.Update(commandMessage[workspaceMsg](t, selectCommand))
	value = updated.(dashboard)
	if createCommand == nil || backend.ensuredRootID != root.ID || backend.ensuredPath != root.Path {
		t.Fatalf("root selection = (command %v, root %q, path %q)", createCommand, backend.ensuredRootID, backend.ensuredPath)
	}
	updated, openCommand := value.Update(commandMessage[tabMsg](t, createCommand))
	value = updated.(dashboard)
	if openCommand == nil || backend.createCount != 1 {
		t.Fatalf("root tab creation = (command %v, count %d)", openCommand, backend.createCount)
	}
	updated, readCommand := value.Update(commandMessage[terminalOpenedMsg](t, openCommand))
	value = updated.(dashboard)
	defer value.closeTerminal()
	if value.terminal == nil || value.terminal.id != tab.ID || readCommand == nil {
		t.Fatalf("root terminal = %v, read command = %v", value.terminal, readCommand)
	}
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "▎▾ projects") || !strings.Contains(rendered, "●") {
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
	defer terminal.close()
	value := newDashboard(backend, initial)
	value.focus = terminalPane
	value.selectedWorkspaceID = rootWorkspace.ID
	value.selectedPath = root.Path
	value.terminal = terminal

	updated, refreshCommand := value.Update(controlSlashKey())
	value = updated.(dashboard)
	if value.focus != leftPane || refreshCommand == nil {
		t.Fatalf("Ctrl+/ result = (focus %v, command %v)", value.focus, refreshCommand)
	}
	updated, _ = value.Update(refreshCommand())
	value = updated.(dashboard)
	if backend.snapshotCount != 1 || !strings.Contains(ansi.Strip(value.render()), "  - cloned") {
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
	value.setNavigation(1)

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

func TestDashboardRejectsSnapshotsThatFinishOutOfOrder(t *testing.T) {
	tree := func(revision uint64, name string) model.Snapshot {
		return model.Snapshot{Revision: revision, Roots: []model.RootView{{
			Root: model.Root{ID: "root-1", Name: name, Path: "/projects"},
		}}}
	}

	value := newDashboard(&fakeBackend{}, tree(1, "initial"))
	updated, _ := value.Update(snapshotMsg{value: tree(2, "newest"), order: 2})
	value = updated.(dashboard)
	updated, _ = value.Update(snapshotMsg{value: tree(1, "older revision"), order: 3})
	value = updated.(dashboard)
	updated, _ = value.Update(snapshotMsg{value: tree(2, "older request"), order: 1})
	value = updated.(dashboard)

	if got := value.state.Roots[0].Root.Name; got != "newest" {
		t.Fatalf("root after out-of-order snapshots = %q, want newest", got)
	}
}

func TestDashboardIgnoresAnOlderWorkspaceSelection(t *testing.T) {
	workspace := func(id string) model.Workspace {
		return model.Workspace{ID: id, RootID: "root-1", Name: id, Path: "/projects/" + id}
	}
	tree := func(revision uint64, names ...string) model.Snapshot {
		directories := make([]model.WorkspaceView, 0, len(names))
		for _, name := range names {
			value := workspace(name)
			directories = append(directories, model.WorkspaceView{
				Workspace: value,
				Tabs:      []model.Tab{{ID: name + "-tab", WorkspaceID: value.ID, Running: true}},
			})
		}
		return model.Snapshot{Revision: revision, Roots: []model.RootView{{
			Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
			Directories: directories,
		}}}
	}

	value := newDashboard(&fakeBackend{}, tree(1, "alpha", "beta"))
	value.selectionRequest = 2
	updated, _ := value.Update(workspaceMsg{
		value: workspace("beta"), snapshot: tree(3, "alpha", "beta"),
		order: 2, selection: 2, tabID: "beta-tab",
	})
	value = updated.(dashboard)
	updated, command := value.Update(workspaceMsg{
		value: workspace("alpha"), snapshot: tree(2, "alpha", "beta"),
		order: 1, selection: 1, tabID: "alpha-tab",
	})
	value = updated.(dashboard)

	if command != nil || value.selectedWorkspaceID != "beta" || value.selectedPath != "/projects/beta" {
		t.Fatalf("stale selection result = (command %v, workspace %q, path %q)", command, value.selectedWorkspaceID, value.selectedPath)
	}
}

func TestCreateTabErrorKeepsItsSelection(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.selectionRequest = 1
	command := value.createTab()
	value.selectionRequest = 2

	raw := command()
	message, ok := raw.(tabMsg)
	if !ok {
		t.Fatalf("createTab() message = %T, want tabMsg", raw)
	}
	if message.selection != 1 {
		t.Fatalf("createTab() selection = %d, want 1", message.selection)
	}
}

func TestDashboardCreatesOnlyOneTabWhileASelectionIsPending(t *testing.T) {
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
	value.setNavigation(1)

	first := value.selectWorkspace()
	second := value.selectWorkspace()
	firstMessage := first()
	secondMessage := second()
	updated, create := value.Update(secondMessage)
	value = updated.(dashboard)
	updated, stale := value.Update(firstMessage)
	value = updated.(dashboard)
	if stale != nil {
		t.Fatal("older selection created a second tab command")
	}
	if retry := value.selectWorkspace(); retry != nil {
		t.Fatal("selection accepted another Enter while tab creation was pending")
	}
	if create == nil {
		t.Fatal("newest selection did not create a tab command")
	}
	updated, _ = value.Update(commandMessage[tabMsg](t, create))
	value = updated.(dashboard)
	if backend.createCount != 1 || value.tabPending {
		t.Fatalf("tab creation = (calls %d, pending %t), want (1, false)", backend.createCount, value.tabPending)
	}
}

func TestDashboardCreatesTabWithCtrlShiftT(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	existing := model.Tab{ID: "tab-1", WorkspaceID: workspace.ID, Name: "1", Running: true}
	created := model.Tab{ID: "tab-2", WorkspaceID: workspace.ID, Name: "2", Running: true}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace, Tabs: []model.Tab{existing}}},
	}}}
	backend := &fakeBackend{snapshot: snapshot, workspace: workspace, createdTab: created}
	value := newDashboard(backend, snapshot)
	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path
	value.terminal = newEmbeddedTerminal(existing.ID, newMemoryStream(""), 40, 10)
	value.focus = terminalPane
	t.Cleanup(value.closeTerminal)

	updated, createCommand := value.Update(newTabKey())
	value = updated.(dashboard)
	if createCommand == nil || !value.tabPending {
		t.Fatalf("Ctrl+Shift+T = (command %v, pending %t), want tab creation", createCommand, value.tabPending)
	}
	updated, openCommand := value.Update(commandMessage[tabMsg](t, createCommand))
	value = updated.(dashboard)
	if openCommand == nil || backend.createCount != 1 || value.tabPending {
		t.Fatalf("created tab = (command %v, calls %d, pending %t), want one tab ready to open",
			openCommand, backend.createCount, value.tabPending)
	}
	updated, readCommand := value.Update(commandMessage[terminalOpenedMsg](t, openCommand))
	value = updated.(dashboard)
	if readCommand == nil || value.terminal == nil || value.terminal.id != created.ID {
		t.Fatalf("opened terminal = (command %v, terminal %v), want %q", readCommand, value.terminal, created.ID)
	}
}

func TestDashboardCreatesTabForWorkspaceCursorWithCtrlShiftT(t *testing.T) {
	root := model.Root{ID: "root-1", Name: "projects", Path: "/projects"}
	alpha := model.Workspace{ID: "workspace-1", RootID: root.ID, Name: "alpha", Path: "/projects/alpha"}
	bravo := model.Workspace{ID: "workspace-2", RootID: root.ID, Name: "bravo", Path: "/projects/bravo"}
	alphaTab := model.Tab{ID: "tab-1", WorkspaceID: alpha.ID, Name: "1", Running: true}
	created := model.Tab{ID: "tab-2", WorkspaceID: bravo.ID, Name: "1", Running: true}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: root,
		Directories: []model.WorkspaceView{
			{Workspace: alpha, Tabs: []model.Tab{alphaTab}},
			{Workspace: bravo},
		},
	}}}
	backend := &fakeBackend{snapshot: snapshot, workspace: bravo, createdTab: created}
	value := newDashboard(backend, snapshot)
	value.selectedWorkspaceID = alpha.ID
	value.selectedPath = alpha.Path
	value.terminal = newEmbeddedTerminal(alphaTab.ID, newMemoryStream(""), 40, 10)
	value.focus = leftPane
	value.setNavigation(2)
	t.Cleanup(value.closeTerminal)

	updated, selectCommand := value.Update(newTabKey())
	value = updated.(dashboard)
	if selectCommand == nil {
		t.Fatal("Ctrl+Shift+T on the workspace cursor produced no selection command")
	}
	updated, createCommand := value.Update(commandMessage[workspaceMsg](t, selectCommand))
	value = updated.(dashboard)
	if createCommand == nil || backend.ensuredPath != bravo.Path {
		t.Fatalf("selected workspace = (command %v, path %q), want %q", createCommand, backend.ensuredPath, bravo.Path)
	}
	updated, openCommand := value.Update(commandMessage[tabMsg](t, createCommand))
	value = updated.(dashboard)
	if openCommand == nil || backend.createCount != 1 || value.selectedWorkspaceID != bravo.ID {
		t.Fatalf("created tab = (command %v, calls %d, workspace %q), want one in %q",
			openCommand, backend.createCount, value.selectedWorkspaceID, bravo.ID)
	}
}

func TestDashboardIgnoresCtrlShiftTWhileTabCreationCannotStart(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace}},
	}}}

	for _, probe := range []struct {
		name    string
		prepare func(*dashboard)
	}{
		{name: "modal open", prepare: func(value *dashboard) { value.modal = helpModal }},
		{name: "tab pending", prepare: func(value *dashboard) { value.tabPending = true }},
	} {
		t.Run(probe.name, func(t *testing.T) {
			value := newDashboard(&fakeBackend{}, snapshot)
			value.setNavigation(1)
			probe.prepare(&value)

			updated, command := value.Update(newTabKey())
			value = updated.(dashboard)
			for _, message := range commandMessages(command) {
				switch message.(type) {
				case workspaceMsg, tabMsg:
					t.Fatalf("Ctrl+Shift+T produced a workspace or tab command")
				}
			}
		})
	}
}

func TestDashboardRecognisesCtrlShiftTAcrossInputLayouts(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace}},
	}}}
	value := newDashboard(&fakeBackend{}, snapshot)
	value.setNavigation(1)
	physicalT := tea.KeyPressMsg(tea.Key{
		Code: 'ㅅ', BaseCode: 't', Mod: tea.ModCtrl | tea.ModShift,
	})

	_, command := value.Update(physicalT)
	if command == nil {
		t.Fatal("Ctrl+Shift+T with a Korean layout produced no tab command")
	}
}

func TestDashboardAddsRootFromPrompt(t *testing.T) {
	backend := &fakeBackend{}
	value := newDashboard(backend, model.Snapshot{})

	updated, _ := value.Update(key(tea.KeyF2, ""))
	value = updated.(dashboard)
	updated, _ = value.Update(key('/', "/"))
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
	updated, _ = value.Update(key('/', "/"))
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

func TestDashboardSupportsRemainingWorkspaceShortcuts(t *testing.T) {
	backend := &fakeBackend{}

	value := newDashboard(backend, model.Snapshot{})
	updated, command := value.Update(key('i', "i"))
	value = updated.(dashboard)
	if value.modal != aboutModal || command != nil {
		t.Fatalf("i result = (modal %v, command %v), want about modal", value.modal, command)
	}

	value = newDashboard(backend, model.Snapshot{})
	updated, command = value.Update(key(',', ","))
	value = updated.(dashboard)
	if value.modal != configModal || command != nil {
		t.Fatalf(", result = (modal %v, command %v), want config modal", value.modal, command)
	}
}

// The cursor stops at the ends of the tree. The wheel reaches romty as arrow
// keys — three per notch, through the terminal's alternate scroll — so a tree
// that wrapped sent one notch at the bottom back to the top.
func TestDashboardNavigationStopsAtBothEnds(t *testing.T) {
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{
			{Workspace: model.Workspace{RootID: "root-1", Name: "api", Path: "/projects/api"}},
			{Workspace: model.Workspace{RootID: "root-1", Name: "web", Path: "/projects/web"}},
		},
	}}}
	value := newDashboard(&fakeBackend{}, snapshot)

	for range 3 {
		updated, _ := value.Update(key(tea.KeyUp, ""))
		value = updated.(dashboard)
	}
	if value.navIndex != 0 {
		t.Fatalf("cursor after ↑ past the top = %d, want 0", value.navIndex)
	}

	last := len(value.navigationItems()) - 1
	for range 5 {
		updated, _ := value.Update(key(tea.KeyDown, ""))
		value = updated.(dashboard)
	}
	if value.navIndex != last {
		t.Fatalf("cursor after ↓ past the end = %d, want %d", value.navIndex, last)
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
	if value.modal != helpModal || command != nil {
		t.Fatalf("F1 result = (modal %v, command %v), want help modal", value.modal, command)
	}

	value = newDashboard(backend, model.Snapshot{})
	updated, command = value.Update(key(tea.KeyF2, ""))
	value = updated.(dashboard)
	if value.modal != browseModal || command == nil {
		t.Fatalf("F2 result = (modal %v, command %v), want the root picker reading", value.modal, command)
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
	var forcedGitFetch bool
	for _, message := range commandMessages(command) {
		if status, ok := message.(gitStatusMsg); ok {
			forcedGitFetch = forcedGitFetch || !status.fetchedAt.IsZero()
			if _, followup := value.Update(message); followup != nil {
				t.Fatal("F5 Git fetch started another refresh loop")
			}
			continue
		}
		value.Update(message)
	}
	if backend.snapshotCount != 1 {
		t.Fatalf("F5 snapshot calls = %d, want 1", backend.snapshotCount)
	}
	if !forcedGitFetch {
		t.Fatal("F5 did not force a Git fetch")
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
	} else if !strings.Contains(rendered, value.styles.shortcutKey.Render(" Ctrl+/ ")) {
		// Assert on a key the fixed status row does not also carry, or the
		// check passes with the contextual rail deleted outright.
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

func TestDashboardMouseSelectsWorkspaceAndTab(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	tabs := []model.Tab{
		{ID: "tab-1", WorkspaceID: workspace.ID, Name: "1", Running: true},
		{ID: "tab-2", WorkspaceID: workspace.ID, Name: "2", Running: true},
	}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace, Tabs: tabs}},
	}}}
	backend := &fakeBackend{snapshot: snapshot, workspace: workspace}
	value := newDashboard(backend, snapshot)
	value.width, value.height = 120, 30

	updated, command := value.Update(tea.MouseClickMsg{X: 2, Y: 3, Button: tea.MouseLeft})
	value = updated.(dashboard)
	if value.navIndex != 1 || value.cursorPath != workspace.Path || command == nil {
		t.Fatalf("workspace click = (index %d, path %q, command %v)", value.navIndex, value.cursorPath, command)
	}

	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path
	value.focus = terminalPane
	rightX := value.dimensions().leftWidth + value.dimensions().separator
	updated, command = value.Update(tea.MouseClickMsg{X: rightX + 5, Y: 0, Button: tea.MouseLeft})
	value = updated.(dashboard)
	if value.tabIndex != 1 || command == nil {
		t.Fatalf("tab click = (index %d, command %v), want second tab opened", value.tabIndex, command)
	}
}

func TestDashboardMouseWheelMovesOnlyTheWorkspaceCursor(t *testing.T) {
	value := newDashboard(&fakeBackend{}, twoRootSnapshot())
	value.width, value.height = 120, 30

	updated, command := value.Update(tea.MouseWheelMsg{X: 2, Y: 3, Button: tea.MouseWheelDown})
	value = updated.(dashboard)
	if value.navIndex != 1 || value.cursorPath != "/second" || command != nil {
		t.Fatalf("workspace wheel = (index %d, path %q, command %v)", value.navIndex, value.cursorPath, command)
	}
}

func TestDashboardDragsAndPersistsTheWorkspaceDivider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	value := newDashboardWithConfig(&fakeBackend{}, model.Snapshot{}, path, Config{LeftWidth: 24})
	value.width, value.height = 120, 30
	seam := value.dimensions().leftWidth + 1

	updated, command := value.Update(tea.MouseClickMsg{X: seam, Y: 5, Button: tea.MouseLeft})
	value = updated.(dashboard)
	if !value.navigationResize || command != nil {
		t.Fatalf("divider press = (dragging %v, command %v)", value.navigationResize, command)
	}

	updated, _ = value.Update(tea.MouseMotionMsg{X: 35, Y: 5, Button: tea.MouseLeft})
	value = updated.(dashboard)
	if value.paneWidth() != 34 {
		t.Fatalf("divider drag width = %d, want 34", value.paneWidth())
	}

	updated, command = value.Update(tea.MouseReleaseMsg{X: 35, Y: 5, Button: tea.MouseLeft})
	value = updated.(dashboard)
	if value.navigationResize || command == nil {
		t.Fatalf("divider release = (dragging %v, command %v)", value.navigationResize, command)
	}
	message := command()
	if saved, ok := message.(configSavedMsg); !ok || saved.err != nil {
		t.Fatalf("save result = %#v", message)
	}
	config, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if config.LeftWidth != 34 {
		t.Fatalf("saved width = %d, want 34", config.LeftWidth)
	}
}

func TestDashboardClaimsTheMouseWithoutAnOpenTerminal(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	if got := value.View().MouseMode; got != tea.MouseModeAllMotion {
		t.Fatalf("mouse mode = %v, want dashboard clicks enabled", got)
	}
}

func TestDashboardHighlightsMouseTargetsOnHover(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	tab := model.Tab{ID: "tab-1", WorkspaceID: workspace.ID, Name: "1", Running: true}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace, Tabs: []model.Tab{tab}}},
	}}}
	value := newDashboard(&fakeBackend{}, snapshot)
	value.width, value.height = 120, 30
	value.setNavigation(1)
	_, defaultDivider := value.paneSeparators()

	before := value.render()
	updated, _ := value.Update(tea.MouseMotionMsg{X: 2, Y: 2})
	value = updated.(dashboard)
	if value.hover.kind != hoverNavigation || value.hover.index != 0 || value.render() == before {
		t.Fatalf("navigation hover = %#v", value.hover)
	}

	seam := value.dimensions().leftWidth + 1
	before = value.render()
	updated, _ = value.Update(tea.MouseMotionMsg{X: seam, Y: 5})
	value = updated.(dashboard)
	if value.hover.kind != hoverDivider || value.render() == before {
		t.Fatalf("divider hover = %#v", value.hover)
	}
	if _, hoveredDivider := value.paneSeparators(); hoveredDivider == defaultDivider {
		t.Fatal("divider hover kept its default color")
	}
	updated, _ = value.Update(tea.MouseMotionMsg{X: seam + 10, Y: 5})
	value = updated.(dashboard)
	if value.hover.kind != hoverNone {
		t.Fatalf("mouse out hover = %#v, want none", value.hover)
	}
	if _, divider := value.paneSeparators(); divider != defaultDivider {
		t.Fatal("divider did not return to its default color after mouse out")
	}

	origin := value.dimensions().leftWidth + value.dimensions().separator
	before = value.render()
	updated, _ = value.Update(tea.MouseMotionMsg{X: origin + 5, Y: 0})
	value = updated.(dashboard)
	if value.hover.kind != hoverTab || value.hover.index != 1 || value.render() == before {
		t.Fatalf("tab hover = %#v", value.hover)
	}
}

func TestDashboardHighlightsDialogMouseTargets(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 100, 24
	updated, _ := value.Update(key(tea.KeyF9, ""))
	value = updated.(dashboard)
	hit := value.modalActionHits(value.width, value.dimensions().bodyHeight)[0]
	before := value.render()
	updated, _ = value.Update(tea.MouseMotionMsg{X: hit.left, Y: hit.row})
	value = updated.(dashboard)
	if value.hover.kind != hoverModalAction || value.hover.index != 0 || value.render() == before {
		t.Fatalf("modal action hover = %#v", value.hover)
	}

	value.modal = configModal
	left, top := value.modalContentOrigin(value.width, value.dimensions().bodyHeight)
	before = value.render()
	updated, _ = value.Update(tea.MouseMotionMsg{X: left + 2, Y: top + 2})
	value = updated.(dashboard)
	if value.hover.kind != hoverConfigRow || value.hover.index != 2 || value.render() == before {
		t.Fatalf("config hover = %#v", value.hover)
	}
}

func TestDashboardConfirmsDaemonShutdown(t *testing.T) {
	backend := &fakeBackend{}
	terminal := newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)
	value := newDashboard(backend, model.Snapshot{})
	value.terminal = terminal
	value.width = 100
	value.height = 24

	updated, command := value.Update(key(tea.KeyF9, ""))
	value = updated.(dashboard)
	if value.modal != shutdownModal || command != nil || backend.shutdownCount != 0 {
		t.Fatalf("F9 result = (modal %v, command %v, shutdowns %d), want confirmation", value.modal, command, backend.shutdownCount)
	}
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "Stop daemon") || !strings.Contains(rendered, "all running terminal sessions") {
		t.Fatalf("shutdown warning is missing:\n%s", rendered)
	}

	updated, command = value.Update(key(tea.KeyEscape, ""))
	value = updated.(dashboard)
	if value.modal != noModal || command != nil || backend.shutdownCount != 0 {
		t.Fatalf("Esc result = (modal %v, command %v, shutdowns %d), want cancellation", value.modal, command, backend.shutdownCount)
	}

	updated, _ = value.Update(key(tea.KeyF9, ""))
	value = updated.(dashboard)
	updated, command = value.Update(key(tea.KeyEnter, ""))
	value = updated.(dashboard)
	if command == nil || !value.shutdownPending || backend.shutdownCount != 0 {
		t.Fatalf("Enter result = (command %v, pending %v, shutdowns %d), want deferred shutdown", command, value.shutdownPending, backend.shutdownCount)
	}

	// Esc cannot take back a dispatched shutdown, and Enter must not repeat it.
	for _, message := range []tea.KeyPressMsg{key(tea.KeyEscape, ""), key(tea.KeyEnter, ""), key(tea.KeyF9, "")} {
		updated, extra := value.Update(message)
		value = updated.(dashboard)
		if extra != nil || value.modal != shutdownModal {
			t.Fatalf("%q during shutdown = (command %v, modal %v), want the modal held", message.String(), extra, value.modal)
		}
	}
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "STOPPING") {
		t.Fatalf("status bar does not report the pending shutdown:\n%s", rendered)
	}

	updated, quitCommand := value.Update(commandMessage[daemonStoppedMsg](t, command))
	value = updated.(dashboard)
	if backend.shutdownCount != 1 || !value.result.Quit || quitCommand == nil || value.terminal != nil {
		t.Fatalf("shutdown result = (count %d, quit %v, command %v, terminal %v)", backend.shutdownCount, value.result.Quit, quitCommand, value.terminal)
	}
}

func TestDashboardConfirmationActionsAreClickable(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 100, 24
	updated, _ := value.Update(key(tea.KeyF9, ""))
	value = updated.(dashboard)

	plain := ansi.Strip(value.render())
	for _, label := range []string{"stop daemon", "cancel"} {
		if !strings.Contains(plain, label) {
			t.Fatalf("confirmation dialog has no clickable %q action:\n%s", label, plain)
		}
	}
	hits := value.modalActionHits(value.width, value.dimensions().bodyHeight)
	if len(hits) != 2 {
		t.Fatalf("modal action hits = %#v, want confirm and cancel", hits)
	}
	confirm := hits[0]
	updated, command := value.Update(tea.MouseClickMsg{
		X: (confirm.left + confirm.right) / 2, Y: confirm.row, Button: tea.MouseLeft,
	})
	value = updated.(dashboard)
	if !value.shutdownPending || command == nil {
		t.Fatalf("confirm click = (pending %v, command %v)", value.shutdownPending, command)
	}

	value = newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 100, 24
	updated, _ = value.Update(key(tea.KeyF9, ""))
	value = updated.(dashboard)
	hits = value.modalActionHits(value.width, value.dimensions().bodyHeight)
	cancel := hits[1]
	updated, command = value.Update(tea.MouseClickMsg{
		X: (cancel.left + cancel.right) / 2, Y: cancel.row, Button: tea.MouseLeft,
	})
	value = updated.(dashboard)
	if value.modal != noModal || command != nil {
		t.Fatalf("cancel click = (modal %v, command %v)", value.modal, command)
	}
}

func TestDashboardAnimatesVisiblePendingWork(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.tabPending = true

	updated, command := value.Update(struct{}{})
	value = updated.(dashboard)
	if !value.agentAnimationPending || command == nil {
		t.Fatalf("pending tab animation = (scheduled %v, command %v)", value.agentAnimationPending, command)
	}
	status := ansi.Strip(value.renderStatus(100, value.dimensions().bodyHeight)[1])
	if !strings.Contains(status, "OPENING") || !strings.Contains(status, agentAnimationFrames[0]) {
		t.Fatalf("pending tab status = %q", status)
	}

	updated, _ = value.Update(agentAnimationMsg{})
	value = updated.(dashboard)
	if value.agentAnimationFrame != 1 || !value.agentAnimationPending {
		t.Fatalf("pending animation = (frame %d, scheduled %v)", value.agentAnimationFrame, value.agentAnimationPending)
	}
}

func TestDashboardAnimatesDirectoryReads(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.modal = browseModal
	value.browse = browser{path: "/projects", loading: true}
	updated, command := value.Update(struct{}{})
	value = updated.(dashboard)
	if command == nil || !value.agentAnimationPending {
		t.Fatal("directory read did not schedule its activity indicator")
	}
	plain := ansi.Strip(strings.Join(value.renderBrowseModal(80, 20), "\n"))
	if !strings.Contains(plain, agentAnimationFrames[0]+" Reading") {
		t.Fatalf("directory activity = %q", plain)
	}
}

func TestDashboardConfirmsAgentHookInstallation(t *testing.T) {
	statuses := []agenthooks.Status{
		{Provider: agenthooks.ProviderClaude, State: agenthooks.StateMissing, Path: "/home/me/.claude/settings.json"},
		{Provider: agenthooks.ProviderCodex, State: agenthooks.StateOutdated, Path: "/home/me/.codex/hooks.json"},
	}
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.offerAgentHooks(statuses)
	if value.modal != hookInstallModal {
		t.Fatalf("hook check modal = %v, want confirmation", value.modal)
	}
	plain := ansi.Strip(strings.Join(value.renderModal(100, 30), "\n"))
	for _, want := range []string{"Claude Code: install", "Codex: update", "Existing settings and other hooks are preserved."} {
		if !strings.Contains(plain, want) {
			t.Fatalf("hook confirmation does not contain %q:\n%s", want, plain)
		}
	}

	previousInstall := installHookProviders
	var installed []agenthooks.Provider
	installHookProviders = func(providers []agenthooks.Provider) ([]agenthooks.Result, error) {
		installed = append(installed, providers...)
		return []agenthooks.Result{
			{Provider: agenthooks.ProviderClaude, Action: agenthooks.ActionInstalled},
			{Provider: agenthooks.ProviderCodex, Action: agenthooks.ActionUpdated},
		}, nil
	}
	t.Cleanup(func() { installHookProviders = previousInstall })

	updated, command := value.Update(key(tea.KeyEnter, ""))
	value = updated.(dashboard)
	if command == nil || !value.hookInstallPending || len(installed) != 0 {
		t.Fatalf("Enter = (command %v, pending %v, installed %v), want deferred install", command, value.hookInstallPending, installed)
	}
	if status := ansi.Strip(value.renderStatus(100, 30)[1]); !strings.Contains(status, "INSTALLING") {
		t.Fatalf("pending status = %q", status)
	}

	updated, extra := value.Update(commandMessage[hooksInstalledMsg](t, command))
	value = updated.(dashboard)
	if extra != nil || value.modal != noModal || value.hookInstallPending {
		t.Fatalf("install result = (extra %v, modal %v, pending %v)", extra, value.modal, value.hookInstallPending)
	}
	if !slices.Equal(installed, []agenthooks.Provider{agenthooks.ProviderClaude, agenthooks.ProviderCodex}) {
		t.Fatalf("installed providers = %v", installed)
	}
	if !value.noticeMessage || !strings.Contains(value.errorMessage, "Claude Code and Codex hooks installed") {
		t.Fatalf("install notice = (%v, %q)", value.noticeMessage, value.errorMessage)
	}
}

func TestDashboardCanSkipAgentHookInstallation(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.offerAgentHooks([]agenthooks.Status{{
		Provider: agenthooks.ProviderClaude,
		State:    agenthooks.StateMissing,
	}})

	updated, command := value.Update(key(tea.KeyEscape, ""))
	value = updated.(dashboard)
	if command != nil || value.modal != noModal || value.hookStatuses != nil {
		t.Fatalf("Esc = (command %v, modal %v, statuses %v), want no changes", command, value.modal, value.hookStatuses)
	}
}

func TestDashboardCanQuitDuringAgentHookInstallation(t *testing.T) {
	for _, pending := range []bool{false, true} {
		value := newDashboard(&fakeBackend{}, model.Snapshot{})
		value.offerAgentHooks([]agenthooks.Status{{
			Provider: agenthooks.ProviderClaude,
			State:    agenthooks.StateMissing,
		}})
		value.hookInstallPending = pending

		updated, command := value.Update(key(tea.KeyF4, ""))
		value = updated.(dashboard)
		if !value.result.Quit || command == nil {
			t.Fatalf("F4 with pending %v = (quit %v, command %v), want quit", pending, value.result.Quit, command)
		}
	}
}

func TestDashboardDoesNotPromptForCurrentAgentHooks(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.offerAgentHooks([]agenthooks.Status{
		{Provider: agenthooks.ProviderClaude, State: agenthooks.StateCurrent},
		{Provider: agenthooks.ProviderCodex, State: agenthooks.StateUnavailable},
	})
	if value.modal != noModal || value.errorMessage != "" {
		t.Fatalf("current hooks = (modal %v, error %q), want no prompt", value.modal, value.errorMessage)
	}
}

func TestDashboardReportsInvalidAgentHookSettings(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.offerAgentHooks([]agenthooks.Status{{
		Provider: agenthooks.ProviderClaude,
		State:    agenthooks.StateInvalid,
		Err:      errors.New("hooks must be an object"),
	}})
	if value.modal != noModal || !strings.Contains(value.errorMessage, "hooks must be an object") || value.noticeMessage {
		t.Fatalf("invalid hooks = (modal %v, error %q, notice %v)", value.modal, value.errorMessage, value.noticeMessage)
	}
}

func TestDashboardReportsFailedShutdown(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	updated, _ := value.Update(key(tea.KeyF9, ""))
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
	updated, _ = value.Update(key(tea.KeyF9, ""))
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

// waitForGuestSilence puts a marker behind the event under test. Seeing that
// marker proves the copy goroutine processed everything before it.
func waitForGuestSilence(t *testing.T, terminal *embeddedTerminal, was string) string {
	t.Helper()
	stream := terminal.stream.(*memoryStream)
	marker := fmt.Sprintf("__romty_test_barrier_%d__", len(was))
	if _, err := io.WriteString(terminal.emulator.InputPipe(), marker); err != nil {
		t.Fatalf("write barrier: %v", err)
	}
	want := was + marker
	waitForGuest(t, stream, want)
	if now := stream.String(); now != want {
		t.Fatalf("guest received %q before barrier, want %q", now, want)
	}
	return want
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

	// The live screen captures the wheel so it can enter scrollback directly.
	if value.View().MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("mouse mode outside scrollback = %v, want all motion", value.View().MouseMode)
	}
	live := plainRows(value.terminal.renderViewport(0))

	updated, command := value.Update(key(tea.KeyF6, ""))
	value = updated.(dashboard)
	if !value.scrollback || value.scrollOffset != 0 {
		t.Fatalf("F6 = (scrollback %v, offset %d), want the live view in scrollback mode",
			value.scrollback, value.scrollOffset)
	}
	if sequences := rawSequences(command); !slices.Contains(sequences, ansi.SetMode(altScrollMode)) {
		t.Fatalf("F6 sequences = %q, want alternate scroll asked for", sequences)
	}
	view := value.View()
	if view.MouseMode != tea.MouseModeNone || view.Cursor != nil {
		t.Fatalf("copy mode view = (mouse %v, cursor %v), want the mouse left with the host", view.MouseMode, view.Cursor)
	}

	// The wheel arrives as arrow keys through the alternate scroll copy mode
	// just asked for, which is what keeps the host's drag selection alive.
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

	// Repeated Shift+PgUp reaches the oldest retained line and clamps there.
	for range history/value.scrollbackPage() + 1 {
		updated, _ = value.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModShift}))
		value = updated.(dashboard)
	}
	if value.scrollOffset != history {
		t.Fatalf("Shift+PgUp offset = %d, want the full history %d", value.scrollOffset, history)
	}
	oldest := plainRows(value.terminal.renderViewport(value.scrollOffset))[0]
	retained := plainRows([]string{value.terminal.emulator.Scrollback().Line(0).Render()})[0]
	if oldest != retained {
		t.Fatalf("oldest visible row = %q, want the first retained line %q", oldest, retained)
	}
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "SCROLLBACK") ||
		!strings.Contains(rendered, fmt.Sprintf("%d/%d", history, history)) ||
		!strings.Contains(rendered, "Ctrl+Shift+\\") {
		t.Fatalf("status bar does not report the scrollback position:\n%s", rendered)
	}
	for range history/value.scrollbackPage() + 1 {
		updated, _ = value.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown, Mod: tea.ModShift}))
		value = updated.(dashboard)
	}
	if value.scrollOffset != 0 {
		t.Fatalf("Shift+PgDown offset = %d, want the live screen", value.scrollOffset)
	}

	updated, command = value.Update(key(tea.KeyEscape, ""))
	value = updated.(dashboard)
	if value.scrollback || value.scrollOffset != 0 || value.View().MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("Esc = (scrollback %v, offset %d, mouse %v), want live wheel capture restored",
			value.scrollback, value.scrollOffset, value.View().MouseMode)
	}
	if sequences := rawSequences(command); !slices.Contains(sequences, ansi.ResetMode(altScrollMode)) {
		t.Fatalf("Esc sequences = %q, want alternate scroll given up", sequences)
	}
}

func TestDashboardEntersScrollbackFromLiveMouseWheel(t *testing.T) {
	value := scrolledDashboard(t, 200)
	leftWidth := value.dimensions().leftWidth

	updated, _ := value.Update(tea.MouseWheelMsg{
		X: leftWidth + 3, Y: terminalTop + 2, Button: tea.MouseWheelUp,
	})
	value = updated.(dashboard)
	if !value.scrollback || value.scrollOffset != 3 {
		t.Fatalf("wheel up = (scrollback %v, offset %d), want three lines of history",
			value.scrollback, value.scrollOffset)
	}

	updated, _ = value.Update(key('x', "x"))
	value = updated.(dashboard)
	if value.scrollback || value.scrollOffset != 0 {
		t.Fatalf("typing = (scrollback %v, offset %d), want the live screen",
			value.scrollback, value.scrollOffset)
	}
	waitForGuest(t, value.terminal.stream.(*memoryStream), "x")
}

// Outside scrollback the host's alternate scroll turns a wheel notch into three
// arrow presses romty cannot tell from typed ones, which walked the workspace
// tree and reached the shell as history keys. romty holds the mode off until
// scrollback, the one state that wants those keys, opens.
func TestDashboardHoldsAlternateScrollForScrollback(t *testing.T) {
	value := scrolledDashboard(t, 200)

	if sequences := rawSequences(value.Init()); !slices.Contains(sequences, ansi.ResetMode(altScrollMode)) {
		t.Fatalf("startup sequences = %q, want alternate scroll turned off", sequences)
	}

	// A key that changes nothing about scrollback repeats no sequence: the mode
	// moves on the transitions, not on every message.
	updated, command := value.Update(key(tea.KeyDown, ""))
	value = updated.(dashboard)
	if sequences := rawSequences(command); len(sequences) != 0 {
		t.Fatalf("sequences without a transition = %q, want none", sequences)
	}

	updated, command = value.Update(key(tea.KeyF6, ""))
	value = updated.(dashboard)
	if sequences := rawSequences(command); !slices.Contains(sequences, ansi.SetMode(altScrollMode)) {
		t.Fatalf("entering scrollback = %q, want alternate scroll asked for", sequences)
	}
	updated, command = value.Update(key(tea.KeyUp, ""))
	value = updated.(dashboard)
	if sequences := rawSequences(command); len(sequences) != 0 {
		t.Fatalf("scrolling inside scrollback = %q, want the mode left alone", sequences)
	}

	// F7 leaves scrollback for the workspace pane, which is one of the seven
	// paths out: the mode follows the state rather than a particular key.
	updated, command = value.Update(key(tea.KeyF7, ""))
	value = updated.(dashboard)
	if value.scrollback {
		t.Fatal("F7 left scrollback open")
	}
	if sequences := rawSequences(command); !slices.Contains(sequences, ansi.ResetMode(altScrollMode)) {
		t.Fatalf("leaving scrollback = %q, want alternate scroll given up", sequences)
	}
}

// rawSequences runs a command the way the runtime does, flattening batches, and
// returns the escape sequences it asked the host terminal for.
func rawSequences(command tea.Cmd) []string {
	if command == nil {
		return nil
	}
	var sequences []string
	switch message := command().(type) {
	case tea.RawMsg:
		sequences = append(sequences, fmt.Sprint(message.Msg))
	case tea.BatchMsg:
		for _, batched := range message {
			sequences = append(sequences, rawSequences(batched)...)
		}
	}
	return sequences
}

func commandMessages(command tea.Cmd) []tea.Msg {
	if command == nil {
		return nil
	}
	message := command()
	batch, ok := message.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{message}
	}
	var messages []tea.Msg
	for _, batched := range batch {
		messages = append(messages, commandMessages(batched)...)
	}
	return messages
}

func commandMessage[T any](t *testing.T, command tea.Cmd) T {
	t.Helper()
	for _, message := range commandMessages(command) {
		if typed, ok := message.(T); ok {
			return typed
		}
	}
	var zero T
	t.Fatalf("command has no %T message", zero)
	return zero
}

func playedSound(command tea.Cmd) (sound.Kind, bool) {
	for _, message := range commandMessages(command) {
		if played, ok := message.(soundPlayedMsg); ok {
			return played.kind, true
		}
	}
	return "", false
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
		{name: "F6", enter: []tea.KeyPressMsg{key(tea.KeyF6, "")}, focus: terminalPane},
		{
			name:  "Ctrl+Shift+\\",
			enter: []tea.KeyPressMsg{scrollbackToggleKey()},
			focus: terminalPane,
		},
		{
			name:  "Ctrl+Shift+\\ from the workspace pane",
			enter: []tea.KeyPressMsg{scrollbackToggleKey()},
			focus: leftPane,
		},
		{
			name: "Ctrl+Shift+\\ with an alternate layout",
			enter: []tea.KeyPressMsg{tea.KeyPressMsg(tea.Key{
				Code: '₩', ShiftedCode: '|', BaseCode: '\\', Mod: tea.ModCtrl | tea.ModShift,
			})},
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
	updated, _ = value.Update(key(tea.KeyF6, ""))
	value = updated.(dashboard)
	if value.scrollback || value.scrollOffset != 0 {
		t.Fatalf("F6 = (scrollback %v, offset %d), want the live screen", value.scrollback, value.scrollOffset)
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
	updated, _ := value.Update(key(tea.KeyF6, ""))
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
		key(tea.KeyF6, ""),
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
	updated, _ = value.Update(key(tea.KeyF6, ""))
	value = updated.(dashboard)
	if !value.scrollback {
		t.Fatalf("scrollback did not reopen after the application exited: %q", value.errorMessage)
	}
}

// Scrollback with nothing to show is a state romty explains, not a failure it
// suffered, so the status bar must not raise the red ERROR that a refused
// daemon call or a dead terminal earns.
func TestDashboardReportsEmptyScrollbackAsANotice(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 120
	value.height = 30
	view := value.dimensions()
	value.terminal = newEmbeddedTerminal("tab-1", newMemoryStream(""), view.rightWidth, view.terminalHeight)
	t.Cleanup(value.closeTerminal)
	value.focus = terminalPane

	updated, _ := value.Update(key(tea.KeyF6, ""))
	value = updated.(dashboard)
	if value.scrollback || !strings.Contains(value.errorMessage, "scrolled off") {
		t.Fatalf("F6 with an empty scrollback = (scrollback %v, message %q)",
			value.scrollback, value.errorMessage)
	}
	rendered := ansi.Strip(value.render())
	if !strings.Contains(rendered, "NOTE") || strings.Contains(rendered, "ERROR") {
		t.Fatalf("the status bar does not report the empty scrollback as a notice:\n%s", rendered)
	}

	// A failure after the notice still takes the bar as an error.
	value.setError(terminalError, "resize terminal: refused")
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "ERROR") {
		t.Fatalf("a failure raised after a notice does not show as an error:\n%s", rendered)
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

	updated, _ := value.Update(key(tea.KeyF6, ""))
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

// An application that owns the screen has a history of its own and romty has
// none to offer, so the wheel is its business. Without this the wheel did
// nothing at all inside vim, less, or an agent, and said nothing either.
func TestDashboardGivesTheWheelToAGuestThatOwnsTheScreen(t *testing.T) {
	t.Run("a guest that asked for the mouse", func(t *testing.T) {
		value := scrolledDashboard(t, 200)
		leftWidth := value.dimensions().leftWidth
		// The alternate screen and a mouse request, the way Claude Code and
		// htop start.
		value.terminal.writeOutput([]byte("\x1b[?1049h\x1b[?1003h\x1b[?1006h"))

		updated, _ := value.Update(tea.MouseWheelMsg{
			X: leftWidth + separatorWidth + 4, Y: terminalTop + 2, Button: tea.MouseWheelUp,
		})
		value = updated.(dashboard)
		if value.scrollback {
			t.Fatal("the wheel opened scrollback on the alternate screen")
		}
		waitForGuest(t, value.terminal.stream.(*memoryStream), "\x1b[<64;5;3")
	})

	t.Run("a guest that did not", func(t *testing.T) {
		value := scrolledDashboard(t, 200)
		leftWidth := value.dimensions().leftWidth
		// less and man own the screen without ever asking for the mouse, and a
		// terminal gives them the cursor keys of alternate scroll instead.
		value.terminal.writeOutput([]byte("\x1b[?1049h"))

		updated, _ := value.Update(tea.MouseWheelMsg{
			X: leftWidth + separatorWidth + 4, Y: terminalTop + 2, Button: tea.MouseWheelUp,
		})
		value = updated.(dashboard)
		if value.scrollback {
			t.Fatal("the wheel opened scrollback on the alternate screen")
		}
		waitForGuest(t, value.terminal.stream.(*memoryStream), "\x1b[A\x1b[A\x1b[A")

		value.Update(tea.MouseWheelMsg{
			X: leftWidth + separatorWidth + 4, Y: terminalTop + 2, Button: tea.MouseWheelDown,
		})
		waitForGuest(t, value.terminal.stream.(*memoryStream), "\x1b[B\x1b[B\x1b[B")
	})
}

// Silence is what a wheel that the host never reports looks like, so the one
// state where romty has nothing to scroll has to say so instead.
func TestDashboardSaysWhyTheWheelHasNothingToScroll(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 120, 30
	view := value.dimensions()
	value.terminal = newEmbeddedTerminal("tab-1", newMemoryStream(""), view.rightWidth, view.terminalHeight)
	t.Cleanup(value.closeTerminal)
	value.focus = terminalPane

	updated, _ := value.Update(tea.MouseWheelMsg{
		X: view.leftWidth + separatorWidth + 4, Y: terminalTop + 2, Button: tea.MouseWheelUp,
	})
	value = updated.(dashboard)
	if value.scrollback {
		t.Fatal("scrollback opened with no history to show")
	}
	if !value.noticeMessage || !strings.Contains(value.errorMessage, "scrolled off") {
		t.Fatalf("status bar = (notice %v, %q), want the reason the wheel did nothing",
			value.noticeMessage, value.errorMessage)
	}
}

// A terminal with no alternate scroll sends nothing once romty hands the mouse
// back, which leaves scrollback unscrollable on a phone and on some desktop
// terminals. Keeping the mouse is the trade the option makes.
func TestDashboardKeepsTheMouseInScrollbackWhenAsked(t *testing.T) {
	value := scrolledDashboard(t, 200)
	value.scrollbackMouse = true

	updated, command := value.Update(key(tea.KeyF6, ""))
	value = updated.(dashboard)
	if !value.scrollback {
		t.Fatal("F6 did not open scrollback")
	}
	if view := value.View(); view.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("scrollback mouse mode = %v, want romty to keep the wheel", view.MouseMode)
	}
	if sequences := rawSequences(command); slices.Contains(sequences, ansi.SetMode(altScrollMode)) {
		t.Fatalf("sequences = %q, want no alternate scroll while romty holds the mouse", sequences)
	}

	updated, _ = value.Update(tea.MouseWheelMsg{X: 40, Y: terminalTop + 2, Button: tea.MouseWheelUp})
	value = updated.(dashboard)
	if value.scrollOffset != wheelLines {
		t.Fatalf("scroll offset = %d, want one notch of history", value.scrollOffset)
	}

	// Off again, the host gets the mouse back for its own drag selection.
	value.scrollbackMouse = false
	if view := value.View(); view.MouseMode != tea.MouseModeNone {
		t.Fatalf("scrollback mouse mode = %v, want the mouse returned to the host", view.MouseMode)
	}
}

// Scrollback is romty's own view of output the guest has already printed, so
// passthrough must not take the wheel there and scroll the guest instead of the
// history the user opened.
func TestDashboardScrollsHistoryNotTheGuestWithBothMouseOptions(t *testing.T) {
	value := scrolledDashboard(t, 200)
	value.scrollbackMouse = true
	value.mousePassthrough = true
	value.terminal.writeOutput([]byte("\x1b[?1003h\x1b[?1006h"))

	updated, _ := value.Update(key(tea.KeyF6, ""))
	value = updated.(dashboard)
	if !value.scrollback {
		t.Fatal("F6 did not open scrollback")
	}
	sent := value.terminal.stream.(*memoryStream).String()

	updated, _ = value.Update(tea.MouseWheelMsg{X: 40, Y: terminalTop + 2, Button: tea.MouseWheelUp})
	value = updated.(dashboard)
	if value.scrollOffset != wheelLines {
		t.Fatalf("scroll offset = %d, want the wheel to move the history", value.scrollOffset)
	}
	waitForGuestSilence(t, value.terminal, sent)
}

// The title sits on the border, so accent bold on accent left the top of every
// box reading as one unbroken line. A chip is how romty draws every other label.
func TestDashboardDrawsModalTitlesAsChips(t *testing.T) {
	for _, dark := range []bool{true, false} {
		styles := newUIStyles(dark)
		if styles.modalTitle.GetBackground() == styles.modalBorder.GetBackground() {
			t.Fatalf("dark=%v: the modal title has only its weight to be seen against the border",
				dark)
		}
		if styles.modalTitle.GetForeground() == styles.modalBorder.GetForeground() {
			t.Fatalf("dark=%v: the modal title is the border's own colour", dark)
		}
	}
}

// What is about to be deleted is named first and its path is the context under
// it. The path used to trail the two red warnings in the body colour, where it
// read as a third consequence rather than as the thing being named.
func TestDashboardNamesTheDeleteTargetBeforeItsConsequences(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 100, 30
	value.modal = removeSelectionModal
	value.removeTarget = navItem{workspace: model.Workspace{Name: "romty", Path: "/w/romty"}}

	lines := plainRows(value.renderModal(72, 28))
	var body []string
	for _, line := range lines[1 : len(lines)-1] {
		body = append(body, strings.TrimSpace(strings.Trim(line, "│")))
	}
	want := []string{
		"Delete romty?",
		"/w/romty",
		"",
		"This permanently deletes all contents.",
		"Its running shells will be terminated.",
	}
	if len(body) < len(want) || !slices.Equal(body[:len(want)], want) {
		t.Fatalf("delete modal body = %q, want the target named before its consequences %q", body, want)
	}
	rendered := strings.Join(value.renderModal(72, 28), "\n")
	if !strings.Contains(rendered, value.styles.empty.Render("/w/romty")) {
		t.Fatalf("the path is not muted against the name above it:\n%s", rendered)
	}
}

func TestDashboardKeepsConfigControlsCompact(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 100, 24
	value.modal = configModal

	lines := plainRows(value.renderModal(72, 22))
	var body []string
	for _, line := range lines[1 : len(lines)-1] {
		body = append(body, strings.TrimSpace(strings.Trim(line, "│")))
	}
	want := []string{
		fmt.Sprintf("Left pane: %d  ←/→", value.paneWidth()),
		"Scrollback mouse: off  m",
		"Sound on done: off  d",
		"Sound on waiting: off  b",
		"Test sound  s",
	}
	if !slices.Equal(body, want) {
		t.Fatalf("config modal body = %q, want each setting grouped with its keys %q", body, want)
	}

	// Keys stay subordinate to the settings while sharing their row, so every
	// control still fits on the shortest supported screen.
	rendered := strings.Join(value.renderModal(72, 22), "\n")
	if !strings.Contains(rendered, value.styles.empty.Render("s")) {
		t.Fatalf("the key hint is not muted:\n%s", rendered)
	}

	// The shortest screen romty lays out for still holds the whole box.
	value.height = 10
	if short := ansi.Strip(value.render()); !strings.Contains(short, "╰") {
		t.Fatalf("config modal lost its bottom edge at height %d:\n%s", value.height, short)
	}
}

func TestDashboardTogglesScrollbackMouseFromConfig(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	updated, _ := value.Update(key(tea.KeyF3, ""))
	value = updated.(dashboard)
	if value.modal != configModal {
		t.Fatalf("F3 modal = %v, want config", value.modal)
	}

	updated, command := value.Update(key('m', "m"))
	value = updated.(dashboard)
	if !value.scrollbackMouse || command == nil {
		t.Fatalf("m = (scrollback mouse %v, command %v), want the setting on and saved",
			value.scrollbackMouse, command)
	}
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "Scrollback mouse: on") {
		t.Fatalf("config modal does not report the setting:\n%s", rendered)
	}

	updated, _ = value.Update(key('m', "m"))
	value = updated.(dashboard)
	if value.scrollbackMouse {
		t.Fatal("m did not turn the setting back off")
	}
}

func TestDashboardConfiguresAndTestsSoundAlerts(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	updated, _ := value.Update(key(tea.KeyF3, ""))
	value = updated.(dashboard)

	updated, saveDone := value.Update(key('d', "d"))
	value = updated.(dashboard)
	updated, saveWaiting := value.Update(key('b', "b"))
	value = updated.(dashboard)
	if !value.soundOnDone || !value.soundOnWaiting || saveDone == nil || saveWaiting == nil {
		t.Fatalf("sound toggles = (done %v, waiting %v, saves %v/%v)",
			value.soundOnDone, value.soundOnWaiting, saveDone, saveWaiting)
	}

	updated, soundCommand := value.Update(key('s', "s"))
	value = updated.(dashboard)
	if played := commandMessage[soundPlayedMsg](t, soundCommand); played.kind != sound.Done {
		t.Fatalf("test sound = %q, want done", played.kind)
	}
}

func TestDashboardClicksConfigControls(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 100, 24
	updated, _ := value.Update(key(tea.KeyF3, ""))
	value = updated.(dashboard)
	left, top := value.modalContentOrigin(value.width, value.dimensions().bodyHeight)

	updated, save := value.Update(tea.MouseClickMsg{X: left + 2, Y: top + 2, Button: tea.MouseLeft})
	value = updated.(dashboard)
	if !value.soundOnDone || save == nil {
		t.Fatalf("done sound click = (enabled %v, save %v)", value.soundOnDone, save)
	}
	updated, soundCommand := value.Update(tea.MouseClickMsg{X: left + 2, Y: top + 4, Button: tea.MouseLeft})
	value = updated.(dashboard)
	if played := commandMessage[soundPlayedMsg](t, soundCommand); played.kind != sound.Done {
		t.Fatalf("test sound click = %q, want done", played.kind)
	}
}

func TestDashboardSoundsOnceForAgentTransitions(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/alpha"}
	tab := model.Tab{
		ID: "tab-1", WorkspaceID: workspace.ID, Name: "1", Running: true,
		Agent: model.AgentCodex, AgentPhase: model.AgentPhaseWorking,
	}
	value := newDashboardWithConfig(&fakeBackend{}, model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "root", Path: "/"},
		Directories: []model.WorkspaceView{{Workspace: workspace, Tabs: []model.Tab{tab}}},
	}}}, "", Config{SoundOnDone: true, SoundOnWaiting: true})
	value.agentSoundReady = true

	idle := map[string]model.AgentStatus{"tab-1": {Agent: model.AgentCodex, Phase: model.AgentPhaseIdle}}
	updated, command := value.Update(agentSnapshotMsg{value: idle})
	value = updated.(dashboard)
	if kind, ok := playedSound(command); !ok || kind != sound.Done {
		t.Fatalf("done transition sound = (%q, %v)", kind, ok)
	}
	updated, command = value.Update(agentSnapshotMsg{value: idle})
	value = updated.(dashboard)
	if kind, ok := playedSound(command); ok {
		t.Fatalf("stable idle transition played %q again", kind)
	}

	waiting := map[string]model.AgentStatus{"tab-1": {Agent: model.AgentCodex, Phase: model.AgentPhaseWaitingApproval}}
	updated, command = value.Update(agentSnapshotMsg{value: waiting})
	value = updated.(dashboard)
	if kind, ok := playedSound(command); !ok || kind != sound.Waiting {
		t.Fatalf("waiting transition sound = (%q, %v)", kind, ok)
	}
}

func TestDashboardDoesNotSoundOnTheFirstAgentSnapshot(t *testing.T) {
	value := newDashboardWithConfig(&fakeBackend{}, model.Snapshot{}, "", Config{SoundOnWaiting: true})
	waiting := map[string]model.AgentStatus{"tab-1": {Agent: model.AgentCodex, Phase: model.AgentPhaseWaitingInput}}
	updated, command := value.Update(agentSnapshotMsg{value: waiting})
	value = updated.(dashboard)
	if !value.agentSoundReady {
		t.Fatal("first successful agent snapshot did not arm later sounds")
	}
	if kind, ok := playedSound(command); ok {
		t.Fatalf("first agent snapshot played %q", kind)
	}
}

// romty asks the host for all motion so it can draw its own hover highlights.
// A guest that asked only for clicks must not be handed those extra reports.
func TestDashboardWithholdsUnrequestedMotionFromTheGuest(t *testing.T) {
	value := scrolledDashboard(t, 200)
	// ?1000h is click tracking: button press and release, no motion.
	value.terminal.writeOutput([]byte("\x1b[?1000h\x1b[?1006h"))
	value.mousePassthrough = true
	if value.terminal.guestMouseMode() != tea.MouseModeCellMotion {
		t.Fatalf("guest mouse mode = %v, want click tracking", value.terminal.guestMouseMode())
	}
	if !value.guestOwnsMouse() {
		t.Fatal("passthrough did not hand the mouse to the guest")
	}

	x, y := value.dimensions().leftWidth+3+4, terminalTop+2
	value.Update(tea.MouseMotionMsg{X: x, Y: y})
	sent := waitForGuestSilence(t, value.terminal, "")

	// The buttons the guest did ask for still reach it.
	value.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	sent += "\x1b[<0;5;3M"
	waitForGuest(t, value.terminal.stream.(*memoryStream), sent)

	// ?1002h is button-event tracking: motion, but only while a button is held.
	value.terminal.writeOutput([]byte("\x1b[?1002h"))
	value.Update(tea.MouseMotionMsg{X: x, Y: y})
	sent = waitForGuestSilence(t, value.terminal, sent)
	value.Update(tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseLeft})
	waitForGuest(t, value.terminal.stream.(*memoryStream), sent+"\x1b[<32;5;3M")
}

func TestDashboardKeepsMouseWithTheHostUnlessPassthroughIsOn(t *testing.T) {
	value := scrolledDashboard(t, 200)
	// A guest that wants the mouse, the way Claude Code and htop do.
	value.terminal.writeOutput([]byte("\x1b[?1003h\x1b[?1006h"))

	if value.terminal.guestMouseMode() != tea.MouseModeAllMotion {
		t.Fatalf("guest mouse mode = %v, want all motion", value.terminal.guestMouseMode())
	}
	if value.View().MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("mouse mode = %v, want romty to capture the live wheel", value.View().MouseMode)
	}
	updated, _ := value.Update(tea.MouseWheelMsg{X: 40, Y: 6, Button: tea.MouseWheelUp})
	value = updated.(dashboard)
	if !value.scrollback {
		t.Fatal("wheel did not open scrollback while passthrough was disabled")
	}
	waitForGuestSilence(t, value.terminal, "")
	updated, _ = value.Update(key(tea.KeyEscape, ""))
	value = updated.(dashboard)

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
	waitForGuestSilence(t, value.terminal, sent)

	// Copy mode takes the mouse back so the host can select the scrolled page.
	updated, _ = value.Update(key(tea.KeyF6, ""))
	value = updated.(dashboard)
	if !value.scrollback || value.View().MouseMode != tea.MouseModeNone {
		t.Fatalf("copy mode = (scrollback %v, mouse %v), want the mouse returned to the host",
			value.scrollback, value.View().MouseMode)
	}
}

func TestDashboardRequestsAlternateKeys(t *testing.T) {
	view := newDashboard(&fakeBackend{}, model.Snapshot{}).View()
	if !view.KeyboardEnhancements.ReportAlternateKeys {
		t.Fatal("dashboard did not request physical base key codes")
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

func TestDashboardFocusesTerminalWhenLeavingScrollback(t *testing.T) {
	for _, leave := range []tea.KeyPressMsg{scrollbackToggleKey(), key(tea.KeyEscape, ""), key(tea.KeyF6, "")} {
		value := scrolledDashboard(t, 200)
		value.focus = leftPane
		updated, _ := value.Update(scrollbackToggleKey())
		value = updated.(dashboard)
		if !value.scrollback || value.focus != leftPane {
			t.Fatalf("Ctrl+Shift+\\ = (scrollback %v, focus %v), want scrollback without moving focus",
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

// F7 and Ctrl+/ both move between panes in one key. Keeping both bindings
// matters on systems where a desktop environment holds one of them globally,
// and Ctrl+/ arrives under two names because only a terminal that speaks the
// Kitty keyboard protocol reports the key that was pressed rather than 0x1F.
func TestDashboardSwitchesPanesWithOneKey(t *testing.T) {
	for _, binding := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{name: "F7", key: key(tea.KeyF7, "")},
		{name: "Ctrl+/", key: controlSlashKey()},
		{name: "Ctrl+/ as 0x1F", key: controlUnderscoreKey()},
		// Ctrl+\ is what romty bound before Ctrl+/ and is kept for the hands
		// that learned it, including on a layout that reports the physical key
		// separately from the character it types.
		{name: "Ctrl+\\", key: controlBackslashKey()},
		{name: "Ctrl+\\ with an alternate layout", key: tea.KeyPressMsg(tea.Key{
			Code: '₩', BaseCode: '\\', Mod: tea.ModCtrl,
		})},
	} {
		t.Run(binding.name, func(t *testing.T) {
			value := scrolledDashboard(t, 200)
			updated, command := value.Update(binding.key)
			value = updated.(dashboard)
			if value.focus != leftPane || command == nil {
				t.Fatalf("terminal -> workspace = (focus %v, command %v), want a refreshed workspace", value.focus, command)
			}

			updated, _ = value.Update(binding.key)
			value = updated.(dashboard)
			if value.focus != terminalPane {
				t.Fatalf("workspace -> terminal = focus %v, want the terminal", value.focus)
			}
		})
	}

	// Scrollback covers the terminal, so switching away from the terminal has
	// to leave it rather than land the keyboard behind the full-width view.
	value := scrolledDashboard(t, 200)
	updated, _ := value.Update(key(tea.KeyF6, ""))
	value = updated.(dashboard)
	if !value.scrollback {
		t.Fatal("F6 did not open scrollback")
	}
	updated, _ = value.Update(key(tea.KeyF7, ""))
	value = updated.(dashboard)
	if value.scrollback || value.focus != leftPane {
		t.Fatalf("F7 in scrollback = (scrollback %v, focus %v), want the workspace pane",
			value.scrollback, value.focus)
	}

	// Ctrl+/ is the one romty teaches. Ctrl+\ answers the same toggle without
	// taking a line of help or a keycap on the status bar for itself.
	value = scrolledDashboard(t, 200)
	keycap := value.styles.shortcutKey.Render(" Ctrl+\\ ")
	if help := strings.Join(value.helpEntries(), "\n"); strings.Contains(help, keycap) {
		t.Fatalf("help advertises the hidden Ctrl+\\ alias:\n%s", ansi.Strip(help))
	}
	if rendered := value.render(); strings.Contains(rendered, keycap) {
		t.Fatalf("status bar advertises the hidden Ctrl+\\ alias:\n%s", ansi.Strip(rendered))
	}
}

// narrowDashboard is a phone-sized dashboard with one workspace in the tree and
// a terminal open on it, so a render can be searched for the tree by name.
func narrowDashboard(t *testing.T, width int) dashboard {
	t.Helper()
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace}},
	}}}
	value := newDashboard(&fakeBackend{snapshot: snapshot, workspace: workspace}, snapshot)
	value.width, value.height = width, 24
	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path
	value.focus = terminalPane
	view := value.dimensions()
	value.terminal = newEmbeddedTerminal("tab-1", newMemoryStream(""), view.rightWidth, view.terminalHeight)
	// The dashboard is settled rather than mid-transition, so the PTY has
	// already been told whichever width this layout gives it.
	value.terminalFullWidth = value.navigationHidden()
	t.Cleanup(value.closeTerminal)
	return value
}

// A phone's SSH client is around 40 to 60 columns, where the workspace pane and
// its separator take half the screen from the terminal the keyboard is in.
func TestDashboardHidesWorkspacePaneOnANarrowScreen(t *testing.T) {
	value := narrowDashboard(t, 60)
	view := value.dimensions()
	if view.leftWidth != 0 || view.separator != 0 || view.rightWidth != 60 {
		t.Fatalf("narrow layout = (left %d, separator %d, right %d), want the terminal alone at 60",
			view.leftWidth, view.separator, view.rightWidth)
	}
	if originX, _ := view.terminalOrigin(); originX != 0 {
		t.Fatalf("terminal origin x = %d, want the first column", originX)
	}
	if rendered := ansi.Strip(value.render()); strings.Contains(rendered, "alpha") {
		t.Fatalf("workspace tree is still drawn:\n%s", rendered)
	}

	updated, _ := value.Update(controlSlashKey())
	value = updated.(dashboard)
	if value.focus != leftPane {
		t.Fatalf("Ctrl+/ focus = %v, want the workspace pane", value.focus)
	}
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "alpha") {
		t.Fatalf("Ctrl+/ did not bring the workspace tree back:\n%s", rendered)
	}
}

// Above the threshold the split is worth its width, so a desktop keeps the
// layout it had and its PTY is never resized by a focus change.
func TestDashboardKeepsWorkspacePaneOnAWideScreen(t *testing.T) {
	value := narrowDashboard(t, 120)
	if value.navigationHidden() {
		t.Fatal("a 120 column screen hid the workspace pane")
	}
	updated, _ := value.Update(key(tea.KeyF7, ""))
	value = updated.(dashboard)
	updated, _ = value.Update(key(tea.KeyF7, ""))
	value = updated.(dashboard)
	if view := value.dimensions(); view.leftWidth == 0 || view.separator != separatorWidth {
		t.Fatalf("wide layout after two toggles = (left %d, separator %d), want the split",
			view.leftWidth, view.separator)
	}
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "alpha") {
		t.Fatalf("workspace tree is missing from a wide screen:\n%s", rendered)
	}
}

// A shell wraps its output to the width it was told, so hiding the pane has to
// reach the PTY the way dragging the window does.
func TestDashboardResizesThePTYWhenTheWorkspacePaneHides(t *testing.T) {
	value := narrowDashboard(t, 60)
	backend := value.backend.(*fakeBackend)

	updated, command := value.Update(controlSlashKey())
	value = updated.(dashboard)
	if command == nil {
		t.Fatal("hiding the workspace pane did not ask the daemon to resize the PTY")
	}
	commandMessages(command)
	wantColumns, wantRows := value.terminalSize()
	if backend.createdColumns != wantColumns || backend.createdRows != wantRows {
		t.Fatalf("PTY size with the pane shown = %dx%d, want %dx%d",
			backend.createdColumns, backend.createdRows, wantColumns, wantRows)
	}

	updated, command = value.Update(controlSlashKey())
	value = updated.(dashboard)
	if command == nil {
		t.Fatal("showing the workspace pane did not ask the daemon to resize the PTY")
	}
	commandMessages(command)
	if wantColumns, _ = value.terminalSize(); backend.createdColumns != wantColumns {
		t.Fatalf("PTY columns with the pane hidden = %d, want the whole screen %d",
			backend.createdColumns, wantColumns)
	}
}

// The file view has two panes of its own, so the narrow layout does not apply
// to it — and the terminal it would widen is not on screen, so opening the view
// must not resize the PTY and make a full-screen guest reflow for a split it
// never appears in.
func TestDashboardKeepsTheFileViewSplitOnANarrowScreen(t *testing.T) {
	value := narrowDashboard(t, 60)
	before, _ := value.terminalSize()

	value.gitDiff = gitDiffView{
		active:    true,
		target:    model.Workspace{Name: "alpha", Path: "/projects/alpha"},
		files:     []gitChangedFile{{Path: "file.go", WorkTreeStatus: 'M'}},
		fileIndex: 0,
	}
	if view := value.gitDiffLayout(); view.leftWidth == 0 || view.separator != separatorWidth {
		t.Fatalf("file view layout = (left %d, separator %d), want the split",
			view.leftWidth, view.separator)
	}
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "file.go") {
		t.Fatalf("file view is not drawn:\n%s", rendered)
	}
	if after, _ := value.terminalSize(); after != before {
		t.Fatalf("PTY width under the file view = %d, want the %d it had at %d columns",
			after, before, value.width)
	}
}

// A terminal whose shell has exited is not somewhere the keyboard can go.
func TestDashboardKeepsWorkspaceFocusWhenSwitchingToADeadTerminal(t *testing.T) {
	value := scrolledDashboard(t, 200)
	value.focus = leftPane
	value.closeTerminal()

	updated, _ := value.Update(key(tea.KeyF7, ""))
	value = updated.(dashboard)
	if value.focus != leftPane {
		t.Fatalf("F7 onto a dead terminal = focus %v, want the workspace pane", value.focus)
	}
}

func TestDashboardKeepsWorkspaceFocusWhenTheTerminalIsGone(t *testing.T) {
	value := scrolledDashboard(t, 200)
	value.focus = leftPane
	updated, _ := value.Update(key(tea.KeyF6, ""))
	value = updated.(dashboard)
	value.closeTerminal()

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
	value.setNavigation(1)
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
				updated, _ = updated.(dashboard).Update(key(tea.KeyExtended, "/projects"))
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
			value.setNavigation(1)

			updated, command := probe.act(value)
			value = updated.(dashboard)
			if command == nil {
				t.Fatalf("%s produced no command to fail", probe.name)
			}
			for _, message := range commandMessages(command) {
				updated, _ = value.Update(message)
				value = updated.(dashboard)
			}
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

// Recorded output is already in the emulator when the terminal becomes part
// of the dashboard. The event loop therefore renders only the final restored
// screen, while keeping the history available to scrollback.
func TestDashboardAdoptsACompletedTerminalReplay(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	tab := model.Tab{ID: "tab-1", WorkspaceID: workspace.ID, Name: "1", Running: true}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{
			Workspace: workspace,
			Tabs:      []model.Tab{tab},
		}},
	}}}
	value := newDashboard(&fakeBackend{}, snapshot)
	value.width = 80
	value.height = 12
	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path
	var replay strings.Builder
	for line := 1; line <= 200; line++ {
		fmt.Fprintf(&replay, "line %03d\r\n", line)
	}
	stream := newMemoryStream("")

	updated, command := value.Update(terminalOpenedMsg{
		tabID:  "tab-1",
		stream: stream,
		replay: []byte(replay.String()),
	})
	value = updated.(dashboard)
	t.Cleanup(value.closeTerminal)

	if value.terminal == nil {
		t.Fatal("the restored terminal was not adopted")
	}
	if value.terminal.scrollbackLen() == 0 {
		t.Fatal("the restored terminal lost its scrollback")
	}
	if rendered := strings.Join(plainRows(value.terminal.render()), "\n"); !strings.Contains(rendered, "line 200") {
		t.Fatalf("restored screen did not land on the final output:\n%s", rendered)
	}
	if command == nil {
		t.Fatal("the restored terminal did not start reading live output")
	}
}

func TestDashboardReconnectsWhenTerminalInputFails(t *testing.T) {
	backend := &fakeBackend{}
	value := newDashboard(backend, model.Snapshot{})
	stream := newMemoryStream("")
	stream.writeErr = errors.New("connection reset")
	value.terminal = newEmbeddedTerminal("tab-1", stream, 40, 10)
	value.focus = terminalPane

	value.terminal.sendKey(key('x', "x"))
	select {
	case <-value.terminal.inputDone:
	case <-time.After(time.Second):
		t.Fatal("terminal input copy did not report the write failure")
	}
	message := value.terminal.read()()
	updated, command := value.Update(message)
	value = updated.(dashboard)

	if value.terminal != nil || !value.terminalExited {
		t.Fatalf("failed input = (terminal %v, exited %v), want reconnect", value.terminal != nil, value.terminalExited)
	}
	if !strings.Contains(value.errorMessage, "write terminal input: connection reset") {
		t.Fatalf("error message = %q, want the input failure", value.errorMessage)
	}
	if command == nil {
		t.Fatal("failed input did not refresh the daemon snapshot")
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

	value.setNavigation(0)
	updated, command := value.Update(key(tea.KeyF8, ""))
	value = updated.(dashboard)
	if value.modal != removeSelectionModal || command != nil {
		t.Fatalf("F8 = (modal %v, command %v), want a confirmation first", value.modal, command)
	}
	body := ansi.Strip(strings.Join(value.renderModal(value.width, value.dimensions().bodyHeight), "\n"))
	if !strings.Contains(body, "gone") || !strings.Contains(body, "directory stays on disk") ||
		!strings.Contains(body, "running shells will be terminated") {
		t.Fatalf("the confirmation does not describe the root removal:\n%s", body)
	}
	updated, command = value.Update(key(tea.KeyEnter, ""))
	value = updated.(dashboard)
	if command == nil || value.modal != noModal {
		t.Fatalf("Enter = (command %v, modal %v), want the removal", command, value.modal)
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

func twoRootSnapshot() model.Snapshot {
	return model.Snapshot{Roots: []model.RootView{
		{Root: model.Root{ID: "root-1", Name: "first", Path: "/first"}, Directories: []model.WorkspaceView{}},
		{Root: model.Root{ID: "root-2", Name: "second", Path: "/second"}, Directories: []model.WorkspaceView{}},
	}}
}

func TestDashboardDoesNotUseRemovedLetterShortcuts(t *testing.T) {
	for _, letter := range []rune{'a', 'q', 'r', 's', 'd', 't'} {
		t.Run(string(letter), func(t *testing.T) {
			value := scrolledDashboard(t, 200)
			value.focus = leftPane

			updated, command := value.Update(key(letter, string(letter)))
			value = updated.(dashboard)
			if command != nil || value.scrollback || value.modal != noModal {
				t.Fatalf("%q = (command %v, scrollback %v, modal %v), want no shortcut action",
					letter, command, value.scrollback, value.modal)
			}
		})
	}

}

func TestDashboardLeavesScrollbackForTerminalInput(t *testing.T) {
	for _, test := range []struct {
		message tea.KeyPressMsg
		want    string
	}{
		{message: key('x', "x"), want: "x"},
		{message: key('k', "k"), want: "k"},
		{message: key(tea.KeyEnter, ""), want: "\r"},
		{message: key(tea.KeyPgUp, ""), want: "\x1b[5~"},
		{message: controlBackslashKey(), want: "\x1c"},
	} {
		t.Run(test.message.String(), func(t *testing.T) {
			value := scrolledDashboard(t, 200)
			updated, _ := value.Update(key(tea.KeyF6, ""))
			value = updated.(dashboard)

			updated, _ = value.Update(test.message)
			value = updated.(dashboard)
			if value.scrollback || value.scrollOffset != 0 || value.focus != terminalPane {
				t.Fatalf("%q = (scrollback %v, offset %d, focus %v), want terminal input mode",
					test.message.String(), value.scrollback, value.scrollOffset, value.focus)
			}
			waitForGuest(t, value.terminal.stream.(*memoryStream), test.want)
		})
	}
}

func TestDashboardLeavesScrollbackForPaste(t *testing.T) {
	value := scrolledDashboard(t, 200)
	updated, _ := value.Update(key(tea.KeyF6, ""))
	value = updated.(dashboard)

	updated, _ = value.Update(tea.PasteMsg{Content: "pasted"})
	value = updated.(dashboard)
	if value.scrollback || value.scrollOffset != 0 || value.focus != terminalPane {
		t.Fatalf("paste = (scrollback %v, offset %d, focus %v), want terminal input mode",
			value.scrollback, value.scrollOffset, value.focus)
	}
	waitForGuest(t, value.terminal.stream.(*memoryStream), "pasted")
}

// Unbound letters always belong to the shell in the terminal pane.
func TestDashboardSendsUnboundLettersToTheShell(t *testing.T) {
	value := scrolledDashboard(t, 200)

	for _, letter := range []rune{'a', 'q', 'r', 's', 'd', 't'} {
		updated, _ := value.Update(key(letter, string(letter)))
		value = updated.(dashboard)
		if value.scrollback || value.modal != noModal {
			t.Fatalf("%q in the terminal = (scrollback %v, modal %v), want it forwarded to the shell",
				letter, value.scrollback, value.modal)
		}
	}
}

// romty takes F1-F7 from the shell and stops there. A full-screen program
// binds the whole function key row: htop puts Kill on F9 and answers it with
// Enter, which is the same Enter that confirms stopping the daemon. Taking F9
// would turn that habit into every session dying at once, and F8 is Delete in
// the file managers this layout comes from.
func TestDashboardLeavesTheHighFunctionKeysToTheShell(t *testing.T) {
	snapshot := twoRootSnapshot()
	value := newDashboard(&fakeBackend{snapshot: snapshot}, snapshot)
	value.width, value.height = 120, 30
	value.terminal = newEmbeddedTerminal("tab-1", newMemoryStream(""), 60, 10)
	t.Cleanup(value.closeTerminal)
	value.focus = terminalPane

	for _, code := range []rune{tea.KeyF8, tea.KeyF9} {
		updated, command := value.Update(key(code, ""))
		value = updated.(dashboard)
		if value.modal != noModal || command != nil {
			t.Fatalf("%v in the terminal = (modal %v, command %v), want it forwarded to the shell",
				code, value.modal, command)
		}
	}

	// The workspace pane still answers them, next to their letters.
	value.focus = leftPane
	updated, _ := value.Update(key(tea.KeyF8, ""))
	value = updated.(dashboard)
	if value.modal != removeSelectionModal {
		t.Fatalf("F8 in the workspace = modal %v, want the removal confirmation", value.modal)
	}
	updated, _ = value.Update(key(tea.KeyEscape, ""))
	value = updated.(dashboard)
	updated, _ = value.Update(key(tea.KeyF9, ""))
	value = updated.(dashboard)
	if value.modal != shutdownModal {
		t.Fatalf("F9 in the workspace = modal %v, want the shutdown confirmation", value.modal)
	}
}

// Esc has to mean no. Removing a root has no undo beyond adding it back and
// rebuilding the tree by hand.
func TestDashboardCancelsRemovingARoot(t *testing.T) {
	snapshot := twoRootSnapshot()
	backend := &fakeBackend{snapshot: snapshot}
	value := newDashboard(backend, snapshot)

	updated, _ := value.Update(key(tea.KeyF8, ""))
	value = updated.(dashboard)
	if value.modal != removeSelectionModal {
		t.Fatalf("F8 modal = %v, want the confirmation", value.modal)
	}
	updated, command := value.Update(key(tea.KeyEscape, ""))
	value = updated.(dashboard)
	if value.modal != noModal || command != nil {
		t.Fatalf("Esc = (modal %v, command %v), want the confirmation closed", value.modal, command)
	}
	if backend.removedRootID != "" {
		t.Fatalf("Esc removed %q", backend.removedRootID)
	}
}

// F8 removes the root the cursor is on, not the first one in the tree.
func TestDashboardRemovesTheSelectedRoot(t *testing.T) {
	snapshot := twoRootSnapshot()
	backend := &fakeBackend{snapshot: snapshot}
	value := newDashboard(backend, snapshot)
	value.setNavigation(1)

	updated, _ := value.Update(key(tea.KeyF8, ""))
	value = updated.(dashboard)
	updated, command := value.Update(key(tea.KeyEnter, ""))
	value = updated.(dashboard)
	if command == nil {
		t.Fatal("F8 then Enter produced no removal command")
	}
	value.Update(command())
	if backend.removedRootID != "root-2" {
		t.Fatalf("removed %q, want root-2", backend.removedRootID)
	}
}

func TestDashboardDeletesTheSelectedWorkspace(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace}},
	}}}
	backend := &fakeBackend{snapshot: snapshot}
	value := newDashboard(backend, snapshot)
	value.width, value.height = 120, 40
	value.setNavigation(1)

	updated, command := value.Update(key(tea.KeyF8, ""))
	value = updated.(dashboard)
	if command != nil || value.modal != removeSelectionModal {
		t.Fatalf("F8 = (command %v, modal %v), want a confirmation", command, value.modal)
	}
	body := ansi.Strip(strings.Join(value.renderModal(value.width, value.dimensions().bodyHeight), "\n"))
	if !strings.Contains(body, "Delete alpha?") || !strings.Contains(body, "permanently deletes all contents") ||
		!strings.Contains(body, "running shells will be terminated") {
		t.Fatalf("workspace confirmation does not describe the deletion:\n%s", body)
	}

	updated, command = value.Update(key(tea.KeyEnter, ""))
	value = updated.(dashboard)
	if command == nil || value.modal != noModal {
		t.Fatalf("Enter = (command %v, modal %v), want the deletion", command, value.modal)
	}
	updated, _ = value.Update(command())
	value = updated.(dashboard)
	if backend.removedWorkspaceRootID != "root-1" || backend.removedWorkspacePath != workspace.Path {
		t.Fatalf("removed workspace = (%q, %q), want (%q, %q)",
			backend.removedWorkspaceRootID, backend.removedWorkspacePath, "root-1", workspace.Path)
	}
	if backend.removedRootID != "" {
		t.Fatalf("workspace deletion removed root %q", backend.removedRootID)
	}
	if len(value.state.Roots) != 1 || len(value.state.Roots[0].Directories) != 0 {
		t.Fatalf("snapshot after deletion = %#v, want the root without alpha", value.state)
	}
}

// A refresh landing while the confirmation is open can move the cursor: the
// row it was on is gone, so the tree falls back to the position, which is now
// a different root's row. The answer has to apply to the workspace the
// question named, not to whatever the cursor drifted onto in between.
func TestDashboardDeletesTheWorkspaceTheConfirmationNamed(t *testing.T) {
	snapshot := model.Snapshot{Roots: []model.RootView{
		{
			Root:        model.Root{ID: "root-1", Name: "first", Path: "/first"},
			Directories: []model.WorkspaceView{{Workspace: model.Workspace{ID: "w", Name: "alpha", Path: "/first/alpha"}}},
		},
		{Root: model.Root{ID: "root-2", Name: "second", Path: "/second"}, Directories: []model.WorkspaceView{}},
	}}
	backend := &fakeBackend{snapshot: snapshot}
	value := newDashboard(backend, snapshot)

	// The cursor sits on first's alpha directory, whose root is first.
	value.setNavigation(1)
	if item, _ := value.navigationItem(); item.root.ID != "root-1" {
		t.Fatalf("the cursor starts on root %q, want root-1", item.root.ID)
	}

	updated, _ := value.Update(key(tea.KeyF8, ""))
	value = updated.(dashboard)

	// alpha is deleted on disk, so the row the cursor remembered is gone and
	// index 1 is now second's own row.
	pruned := model.Snapshot{Roots: []model.RootView{
		{Root: snapshot.Roots[0].Root, Directories: []model.WorkspaceView{}},
		snapshot.Roots[1],
	}}
	updated, _ = value.Update(snapshotMsg{value: pruned})
	value = updated.(dashboard)
	if item, _ := value.navigationItem(); item.root.ID != "root-2" {
		t.Fatalf("the cursor drifted to root %q; the test needs it on root-2 to prove anything", item.root.ID)
	}

	updated, command := value.Update(key(tea.KeyEnter, ""))
	value = updated.(dashboard)
	if command == nil {
		t.Fatal("Enter produced no removal command")
	}
	value.Update(command())
	if backend.removedWorkspaceRootID != "root-1" || backend.removedWorkspacePath != "/first/alpha" {
		t.Fatalf("removed workspace = (%q, %q), want the workspace the confirmation named",
			backend.removedWorkspaceRootID, backend.removedWorkspacePath)
	}
}

func TestDashboardForgetsTheRootTheConfirmationNamed(t *testing.T) {
	snapshot := twoRootSnapshot()
	backend := &fakeBackend{snapshot: snapshot}
	value := newDashboard(backend, snapshot)

	updated, _ := value.Update(key(tea.KeyF8, ""))
	value = updated.(dashboard)
	updated, _ = value.Update(snapshotMsg{value: model.Snapshot{Roots: snapshot.Roots[1:]}})
	value = updated.(dashboard)
	if item, _ := value.navigationItem(); item.root.ID != "root-2" {
		t.Fatalf("the cursor drifted to root %q; want root-2", item.root.ID)
	}

	updated, command := value.Update(key(tea.KeyEnter, ""))
	value = updated.(dashboard)
	if command == nil {
		t.Fatal("Enter produced no removal command")
	}
	value.Update(command())
	if backend.removedRootID != "root-1" {
		t.Fatalf("removed %q, want root-1 — the root the confirmation named", backend.removedRootID)
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

// The status bar has one slot, and a refresh used to empty it whatever was in
// it. So the F5 a user presses right after a failure erased the reason for it,
// and an in-flight refresh could erase one at random.
func TestDashboardKeepsErrorsARefreshDoesNotAnswer(t *testing.T) {
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: model.Workspace{ID: "w", Name: "alpha", Path: "/projects/alpha"}}},
	}}}

	for _, probe := range []struct {
		name     string
		raise    func(dashboard) dashboard
		survives bool
	}{
		{
			name: "a failed resize",
			raise: func(m dashboard) dashboard {
				// A resize failure names its tab, so the terminal it is about
				// has to be the one on screen.
				m.terminal = newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)
				t.Cleanup(m.closeTerminal)
				updated, _ := m.Update(resizeFailedMsg{
					tabID: "tab-1",
					err:   errors.New("terminal session not found"),
				})
				return updated.(dashboard)
			},
			survives: true,
		},
		{
			name: "a daemon that would not stop",
			raise: func(m dashboard) dashboard {
				updated, _ := m.Update(daemonStoppedMsg{err: errors.New("connection reset")})
				return updated.(dashboard)
			},
			survives: true,
		},
		{
			name: "config that could not be written",
			raise: func(m dashboard) dashboard {
				updated, _ := m.Update(configSavedMsg{err: errors.New("read-only file system")})
				return updated.(dashboard)
			},
			survives: true,
		},
		{
			name: "a snapshot that failed",
			raise: func(m dashboard) dashboard {
				updated, _ := m.Update(snapshotMsg{err: errors.New("daemon: read root")})
				return updated.(dashboard)
			},
			survives: false,
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			value := newDashboard(&fakeBackend{snapshot: snapshot}, snapshot)
			value.width = 120
			value.height = 40
			value = probe.raise(value)
			raised := value.errorMessage
			if raised == "" {
				t.Fatal("nothing was raised")
			}

			updated, _ := value.Update(snapshotMsg{value: snapshot})
			value = updated.(dashboard)
			if probe.survives && value.errorMessage != raised {
				t.Fatalf("a refresh erased %q, which it does not answer", raised)
			}
			if !probe.survives && value.errorMessage != "" {
				t.Fatalf("a refresh left %q standing, which it does answer", value.errorMessage)
			}
		})
	}
}

// A resize failure is not a snapshot: borrowing snapshotMsg for it meant the
// handler could not trust message.value, and the failure erased itself.
func TestDashboardReportsAFailedResizeAsItsOwnFailure(t *testing.T) {
	backend := &fakeBackend{}
	backend.fail("Resize", errors.New("terminal session not found"))
	value := newDashboard(backend, model.Snapshot{})
	value.terminal = newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)
	t.Cleanup(value.closeTerminal)

	updated, command := value.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	value = updated.(dashboard)
	if command == nil {
		t.Fatal("a resize produced no command")
	}
	message := command()
	if _, ok := message.(resizeFailedMsg); !ok {
		t.Fatalf("a failed resize produced %T, want resizeFailedMsg", message)
	}
	updated, _ = value.Update(message)
	value = updated.(dashboard)
	if !strings.Contains(value.errorMessage, "resize terminal") {
		t.Fatalf("error message = %q, want the resize failure", value.errorMessage)
	}
	// The emulator has taken the new size even though the daemon refused.
	columns, rows := value.terminalSize()
	if value.terminal.emulator.Width() != int(columns) || value.terminal.emulator.Height() != int(rows) {
		t.Fatalf("emulator = %dx%d, want %dx%d",
			value.terminal.emulator.Width(), value.terminal.emulator.Height(), columns, rows)
	}
}

// Holding an arrow key in the config modal outruns the disk, so an answer can
// arrive under a width that has already moved on. Reading the width before the
// error meant such a save could fail without a word — the one outcome the user
// has to hear about.
func TestDashboardReportsAConfigFailureTheWidthMovedPast(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 120, 40
	value.leftWidth = 24

	updated, command := value.Update(configSavedMsg{
		leftWidth: 22,
		err:       errors.New("read-only file system"),
	})
	value = updated.(dashboard)
	if !strings.Contains(value.errorMessage, "read-only file system") {
		t.Fatalf("error message = %q, want the save failure", value.errorMessage)
	}
	if command == nil {
		t.Fatal("the width that moved on was not saved again")
	}
}

// A resize is a round trip, so dragging a window and then switching tabs
// leaves one in flight for the tab just left. The daemon answers "running
// terminal session not found" for a terminal nobody is looking at, and the
// status bar used to hang that on whichever terminal had taken its place.
func TestDashboardIgnoresAResizeFailureForATerminalItLeft(t *testing.T) {
	backend := &fakeBackend{}
	backend.fail("Resize", errors.New("running terminal session not found"))
	value := newDashboard(backend, model.Snapshot{})
	value.terminal = newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)

	updated, command := value.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	value = updated.(dashboard)
	if command == nil {
		t.Fatal("a resize produced no command")
	}
	message := command()
	failure, ok := message.(resizeFailedMsg)
	if !ok {
		t.Fatalf("a failed resize produced %T, want resizeFailedMsg", message)
	}
	if failure.tabID != "tab-1" {
		t.Fatalf("resizeFailedMsg tabID = %q, want the tab it was sent for", failure.tabID)
	}

	// The user has moved on to another tab before the answer lands.
	value.closeTerminal()
	value.terminal = newEmbeddedTerminal("tab-2", newMemoryStream(""), 40, 10)
	t.Cleanup(value.closeTerminal)

	updated, _ = value.Update(failure)
	value = updated.(dashboard)
	if value.errorMessage != "" {
		t.Fatalf("error message = %q, want nothing about a terminal the user left", value.errorMessage)
	}
}

// The tree is rebuilt from the daemon on every refresh, so a directory
// appearing above the cursor — a `git clone` finishing in another terminal —
// used to slide the highlight onto its neighbour without the user touching a
// key. Enter then opened a workspace they had not chosen.
func TestDashboardKeepsTheCursorOnTheSameWorkspace(t *testing.T) {
	tree := func(names ...string) model.Snapshot {
		directories := make([]model.WorkspaceView, 0, len(names))
		for _, name := range names {
			directories = append(directories, model.WorkspaceView{
				Workspace: model.Workspace{ID: name, RootID: "root-1", Name: name, Path: "/projects/" + name},
			})
		}
		return model.Snapshot{Roots: []model.RootView{{
			Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
			Directories: directories,
		}}}
	}

	value := newDashboard(&fakeBackend{}, tree("beta", "gamma"))
	value.width = 120
	value.height = 40
	// The tree is root, beta, gamma; move onto the last of them.
	value.moveNavigation(1)
	value.moveNavigation(1)
	item, _ := value.navigationItem()
	if item.workspace.Name != "gamma" {
		t.Fatalf("the cursor starts on %q, want gamma", item.workspace.Name)
	}

	// A clone finishes and sorts above the cursor.
	updated, _ := value.Update(snapshotMsg{value: tree("alpha", "beta", "gamma")})
	value = updated.(dashboard)
	item, ok := value.navigationItem()
	if !ok || item.workspace.Name != "gamma" {
		t.Fatalf("after the refresh the cursor is on %q, want gamma", item.workspace.Name)
	}
	if !strings.Contains(ansi.Strip(value.render()), "▌ - gamma") {
		t.Fatalf("the highlight is not on gamma:\n%s", ansi.Strip(value.render()))
	}

	// The workspace the cursor was on disappears: fall back rather than
	// pointing at nothing.
	updated, _ = value.Update(snapshotMsg{value: tree("alpha", "beta")})
	value = updated.(dashboard)
	if _, ok := value.navigationItem(); !ok {
		t.Fatal("the cursor points outside the tree after its workspace was removed")
	}
}

func TestDashboardRefusesScrollbackWithoutTerminal(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})

	for _, message := range []tea.KeyPressMsg{
		key(tea.KeyF6, ""),
		tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModShift}),
		scrollbackToggleKey(),
	} {
		updated, _ := value.Update(message)
		value = updated.(dashboard)
		if value.scrollback {
			t.Fatalf("%q entered scrollback with no terminal open", message.String())
		}
	}
	if value.View().MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("mouse mode = %v, want dashboard clicks enabled", value.View().MouseMode)
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
		{message: key(tea.KeyF1, ""), want: helpModal},
		{message: key(tea.KeyF3, ""), want: configModal},
		{message: key(tea.KeyF1, ""), want: helpModal},
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
	if value.modal != browseModal {
		t.Fatalf("F2 with a modal open = modal %v, want the root picker", value.modal)
	}
	updated, _ = value.Update(key('/', "/"))
	value = updated.(dashboard)
	if !value.inputMode || value.modal != noModal {
		t.Fatalf("/ in the picker = (input %v, modal %v), want root input", value.inputMode, value.modal)
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

func TestDashboardCapturesLiveMouseWheelAndUsesKeyboardFocus(t *testing.T) {
	stream := newMemoryStream("")
	terminal := newEmbeddedTerminal("tab-1", stream, 40, 10)
	defer terminal.close()

	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 120
	value.height = 40
	value.terminal = terminal
	value.focus = terminalPane

	if view := value.View(); view.MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("initial mouse mode = %v, want all motion", view.MouseMode)
	}
	if rendered := value.render(); strings.Contains(rendered, "Ctrl+G") || strings.Contains(rendered, "mouse focus") {
		t.Fatalf("mouse focus mode is still advertised:\n%s", rendered)
	}

	updated, _ := value.Update(controlSlashKey())
	value = updated.(dashboard)
	if value.focus != leftPane {
		t.Fatalf("Ctrl+/ focus = %v, want workspace pane", value.focus)
	}
	separator := value.styles.dividerActive.Render("◀") + value.styles.divider.Render("│") + " "
	if rendered := value.render(); !strings.Contains(rendered, separator) {
		t.Fatalf("workspace focus is not visible:\n%s", rendered)
	}
	if view := value.View(); view.MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("mouse mode after Ctrl+/ = %v, want all motion", view.MouseMode)
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
	defer terminal.close()
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

// Ctrl+Shift+Left/Right is the one tab key that reaches the terminal pane, so
// it has to switch on its own: moving the cursor and waiting for Enter is what
// the plain arrows already do.
func TestDashboardSwitchesTabsWithCtrlShiftArrows(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	tabs := []model.Tab{
		{ID: "tab-1", WorkspaceID: workspace.ID, Name: "1", Running: true},
		{ID: "tab-2", WorkspaceID: workspace.ID, Name: "2", Running: true},
		{ID: "tab-3", WorkspaceID: workspace.ID, Name: "3", Running: true},
	}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace, Tabs: tabs}},
	}}}
	backend := &fakeBackend{snapshot: snapshot, workspace: workspace}
	value := newDashboard(backend, snapshot)
	value.setNavigation(1)
	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path
	value.terminal = newEmbeddedTerminal(tabs[0].ID, newMemoryStream(""), 40, 10)
	value.focus = terminalPane
	defer func() { value.closeTerminal() }()

	// The terminal pane keeps the key rather than sending it to the shell.
	value = pressSwitchTab(t, value, tea.KeyRight)
	if value.tabIndex != 1 || backend.openedTab != tabs[1].ID || value.terminal.id != tabs[1].ID {
		t.Fatalf("Ctrl+Shift+Right = (index %d, opened %q, terminal %q), want the second tab open",
			value.tabIndex, backend.openedTab, value.terminal.id)
	}

	// Left from the first tab wraps to the last rather than stopping there.
	value = pressSwitchTab(t, value, tea.KeyLeft)
	value = pressSwitchTab(t, value, tea.KeyLeft)
	last := len(tabs) - 1
	if value.tabIndex != last || value.terminal.id != tabs[last].ID {
		t.Fatalf("Ctrl+Shift+Left twice = (index %d, terminal %q), want the last tab open",
			value.tabIndex, value.terminal.id)
	}

	// The new-tab slot is a cursor position, not a tab to switch to.
	value.tabIndex = len(tabs)
	value = pressSwitchTab(t, value, tea.KeyRight)
	if value.tabIndex != 0 || value.terminal.id != tabs[0].ID {
		t.Fatalf("Ctrl+Shift+Right from + = (index %d, terminal %q), want the first tab open",
			value.tabIndex, value.terminal.id)
	}

	// The tab that is already open is left alone: reopening it would tear a
	// live terminal down and attach a new one in its place.
	backend.openedTab = ""
	value.tabIndex = 1
	updated, command := value.Update(switchTabKey(tea.KeyLeft))
	value = updated.(dashboard)
	if command != nil || backend.openedTab != "" || value.tabIndex != 1 {
		t.Fatalf("switching to the open tab = (command %v, opened %q, index %d), want nothing done",
			command, backend.openedTab, value.tabIndex)
	}

	// Scrollback belongs to the terminal it was opened on, so a switch made
	// from it lands on the new terminal's live screen.
	value.tabIndex = 0
	value.scrollback = true
	// altScroll comes with scrollback everywhere else, so the shortcut sets it
	// too: a dashboard that disagreed with the host would spend this switch
	// correcting the mode instead of opening the tab.
	value.altScroll = true
	value.scrollOffset = 5
	value = pressSwitchTab(t, value, tea.KeyRight)
	if value.scrollback || value.scrollOffset != 0 || value.terminal.id != tabs[1].ID {
		t.Fatalf("switching from scrollback = (scrollback %v, offset %d, terminal %q), want the live screen",
			value.scrollback, value.scrollOffset, value.terminal.id)
	}

	// A modal is a question waiting for an answer; switching behind it would
	// change the terminal the answer applies to.
	value.modal = shutdownModal
	backend.openedTab = ""
	updated, command = value.Update(switchTabKey(tea.KeyRight))
	value = updated.(dashboard)
	if command != nil || backend.openedTab != "" {
		t.Fatalf("switching with a modal open = (command %v, opened %q), want nothing done",
			command, backend.openedTab)
	}
}

// Ctrl+Shift+Up/Down is the other axis of the same chord: reaching a terminal
// in another workspace used to mean leaving the terminal pane, walking the
// tree and pressing Enter. Only workspaces with a terminal running are stops,
// because a root lists every child directory whether it has been used or not.
func TestDashboardSwitchesWorkspacesWithCtrlShiftArrows(t *testing.T) {
	root := model.Root{ID: "root-1", Name: "projects", Path: "/projects"}
	// alpha and charlie have terminals; bravo is an empty directory between
	// them, and delta an empty one after.
	alpha := model.Workspace{ID: "workspace-1", RootID: root.ID, Name: "alpha", Path: "/projects/alpha"}
	bravo := model.Workspace{RootID: root.ID, Name: "bravo", Path: "/projects/bravo"}
	charlie := model.Workspace{ID: "workspace-3", RootID: root.ID, Name: "charlie", Path: "/projects/charlie"}
	delta := model.Workspace{RootID: root.ID, Name: "delta", Path: "/projects/delta"}
	alphaTab := model.Tab{ID: "tab-1", WorkspaceID: alpha.ID, Name: "1", Running: true}
	charlieTab := model.Tab{ID: "tab-3", WorkspaceID: charlie.ID, Name: "1", Running: true}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: root,
		Directories: []model.WorkspaceView{
			{Workspace: alpha, Tabs: []model.Tab{alphaTab}},
			{Workspace: bravo},
			{Workspace: charlie, Tabs: []model.Tab{charlieTab}},
			{Workspace: delta},
		},
	}}}

	backend := &fakeBackend{snapshot: snapshot, workspace: charlie}
	value := newDashboard(backend, snapshot)
	value.selectedWorkspaceID = alpha.ID
	value.selectedPath = alpha.Path
	value.terminal = newEmbeddedTerminal(alphaTab.ID, newMemoryStream(""), 40, 10)
	value.focus = terminalPane
	t.Cleanup(value.closeTerminal)

	// Down skips bravo, which has nothing running, and opens charlie's.
	value = pressSwitchTab(t, value, tea.KeyDown)
	if backend.openedTab != charlieTab.ID || value.terminal.id != charlieTab.ID {
		t.Fatalf("Ctrl+Shift+Down = (opened %q, terminal %q), want charlie's terminal",
			backend.openedTab, value.terminal.id)
	}
	if value.selectedPath != charlie.Path {
		t.Fatalf("selected path = %q, want %q", value.selectedPath, charlie.Path)
	}
	// The cursor follows, so the tree shows where the keyboard now is.
	if item, ok := value.navigationItem(); !ok || item.workspace.Path != charlie.Path {
		t.Fatalf("cursor = %+v, want it on charlie", item)
	}

	// Down again wraps past delta and the root to alpha rather than stopping.
	backend.workspace = alpha
	value = pressSwitchTab(t, value, tea.KeyDown)
	if backend.openedTab != alphaTab.ID || value.terminal.id != alphaTab.ID {
		t.Fatalf("Ctrl+Shift+Down twice = (opened %q, terminal %q), want it wrapped to alpha",
			backend.openedTab, value.terminal.id)
	}

	// Up from alpha wraps the other way, to the last workspace with a terminal.
	backend.workspace = charlie
	value = pressSwitchTab(t, value, tea.KeyUp)
	if value.terminal.id != charlieTab.ID {
		t.Fatalf("Ctrl+Shift+Up = terminal %q, want it wrapped to charlie", value.terminal.id)
	}
}

// The chord is anchored on the workspace that is open, not on the cursor. A
// cursor anchor makes the key do nothing whenever it points at the workspace
// before the open one, which from the workspace pane is where it often sits.
func TestDashboardSwitchesWorkspacesFromTheOpenOneNotTheCursor(t *testing.T) {
	root := model.Root{ID: "root-1", Name: "projects", Path: "/projects"}
	alpha := model.Workspace{ID: "workspace-1", RootID: root.ID, Name: "alpha", Path: "/projects/alpha"}
	bravo := model.Workspace{ID: "workspace-2", RootID: root.ID, Name: "bravo", Path: "/projects/bravo"}
	alphaTab := model.Tab{ID: "tab-1", WorkspaceID: alpha.ID, Name: "1", Running: true}
	bravoTab := model.Tab{ID: "tab-2", WorkspaceID: bravo.ID, Name: "1", Running: true}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: root,
		Directories: []model.WorkspaceView{
			{Workspace: alpha, Tabs: []model.Tab{alphaTab}},
			{Workspace: bravo, Tabs: []model.Tab{bravoTab}},
		},
	}}}

	backend := &fakeBackend{snapshot: snapshot, workspace: alpha}
	value := newDashboard(backend, snapshot)
	// bravo's terminal is open while the cursor sits on alpha, the workspace
	// just above it.
	value.selectedWorkspaceID = bravo.ID
	value.selectedPath = bravo.Path
	value.terminal = newEmbeddedTerminal(bravoTab.ID, newMemoryStream(""), 40, 10)
	value.focus = leftPane
	value.setNavigation(1)
	t.Cleanup(value.closeTerminal)

	value = pressSwitchTab(t, value, tea.KeyDown)
	if backend.openedTab != alphaTab.ID || value.terminal.id != alphaTab.ID {
		t.Fatalf("Ctrl+Shift+Down = (opened %q, terminal %q), want alpha's terminal rather than nothing",
			backend.openedTab, value.terminal.id)
	}
}

// Nothing running anywhere, or only the workspace already open: there is
// nowhere to go, and reopening the one that is open would tear a live terminal
// down and attach a new one in its place.
func TestDashboardKeepsTheWorkspaceWhenThereIsNowhereToSwitch(t *testing.T) {
	root := model.Root{ID: "root-1", Name: "projects", Path: "/projects"}
	only := model.Workspace{ID: "workspace-1", RootID: root.ID, Name: "alpha", Path: "/projects/alpha"}
	empty := model.Workspace{RootID: root.ID, Name: "bravo", Path: "/projects/bravo"}

	for _, probe := range []struct {
		name string
		tabs []model.Tab
	}{
		{name: "no terminal anywhere"},
		{name: "only the open workspace", tabs: []model.Tab{{ID: "tab-1", WorkspaceID: only.ID, Name: "1", Running: true}}},
	} {
		t.Run(probe.name, func(t *testing.T) {
			snapshot := model.Snapshot{Roots: []model.RootView{{
				Root: root,
				Directories: []model.WorkspaceView{
					{Workspace: only, Tabs: probe.tabs},
					{Workspace: empty},
				},
			}}}
			backend := &fakeBackend{snapshot: snapshot, workspace: only}
			value := newDashboard(backend, snapshot)
			value.selectedWorkspaceID = only.ID
			value.selectedPath = only.Path

			for _, code := range []rune{tea.KeyDown, tea.KeyUp} {
				updated, command := value.Update(switchTabKey(code))
				value = updated.(dashboard)
				if command != nil || backend.openedTab != "" {
					t.Fatalf("Ctrl+Shift+%s = (command %v, opened %q), want nothing done",
						switchTabKey(code).String(), command, backend.openedTab)
				}
			}
		})
	}
}

// A sound and a tab marker say an agent stopped to ask something; without a way
// to reach it the user still has to walk the tree to find which one.
func TestDashboardJumpsToTheNextWaitingAgent(t *testing.T) {
	root := model.Root{ID: "root-1", Name: "projects", Path: "/projects"}
	alpha := model.Workspace{ID: "workspace-1", RootID: root.ID, Name: "alpha", Path: "/projects/alpha"}
	bravo := model.Workspace{ID: "workspace-2", RootID: root.ID, Name: "bravo", Path: "/projects/bravo"}
	charlie := model.Workspace{ID: "workspace-3", RootID: root.ID, Name: "charlie", Path: "/projects/charlie"}
	// alpha's agent is busy; bravo's wants input and charlie's wants approval.
	alphaTab := model.Tab{ID: "tab-1", WorkspaceID: alpha.ID, Name: "1", Running: true,
		Agent: model.AgentClaude, AgentPhase: model.AgentPhaseWorking}
	bravoTab := model.Tab{ID: "tab-2", WorkspaceID: bravo.ID, Name: "1", Running: true,
		Agent: model.AgentClaude, AgentPhase: model.AgentPhaseWaitingInput}
	charlieTab := model.Tab{ID: "tab-3", WorkspaceID: charlie.ID, Name: "1", Running: true,
		Agent: model.AgentCodex, AgentPhase: model.AgentPhaseWaitingApproval}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: root,
		Directories: []model.WorkspaceView{
			{Workspace: alpha, Tabs: []model.Tab{alphaTab}},
			{Workspace: bravo, Tabs: []model.Tab{bravoTab}},
			{Workspace: charlie, Tabs: []model.Tab{charlieTab}},
		},
	}}}

	backend := &fakeBackend{snapshot: snapshot, workspace: bravo}
	value := newDashboard(backend, snapshot)
	value.selectedWorkspaceID = alpha.ID
	value.selectedPath = alpha.Path
	value.terminal = newEmbeddedTerminal(alphaTab.ID, newMemoryStream(""), 40, 10)
	value.focus = terminalPane
	t.Cleanup(func() { value.closeTerminal() })

	// The open terminal is not waiting, so the walk starts at the first stop
	// and skips the busy agent entirely.
	value = pressJumpToWaitingAgent(t, value)
	if backend.openedTab != bravoTab.ID || value.terminal.id != bravoTab.ID {
		t.Fatalf("Ctrl+Shift+A = (opened %q, terminal %q), want bravo's waiting terminal",
			backend.openedTab, value.terminal.id)
	}
	if item, ok := value.navigationItem(); !ok || item.workspace.Path != bravo.Path {
		t.Fatalf("cursor = %+v, want it on bravo", item)
	}

	// Waiting for approval is a stop as much as waiting for input.
	backend.workspace = charlie
	value = pressJumpToWaitingAgent(t, value)
	if value.terminal.id != charlieTab.ID {
		t.Fatalf("second jump = terminal %q, want charlie's", value.terminal.id)
	}

	// The last stop wraps rather than stopping at the end of the tree.
	backend.workspace = bravo
	value = pressJumpToWaitingAgent(t, value)
	if value.terminal.id != bravoTab.ID {
		t.Fatalf("third jump = terminal %q, want it wrapped to bravo", value.terminal.id)
	}
}

// Reopening the terminal that is already attached would tear it down to attach
// a new one in its place, so the only waiting agent gets the keyboard instead.
func TestDashboardJumpToTheOnlyWaitingAgentMovesTheFocus(t *testing.T) {
	root := model.Root{ID: "root-1", Name: "projects", Path: "/projects"}
	alpha := model.Workspace{ID: "workspace-1", RootID: root.ID, Name: "alpha", Path: "/projects/alpha"}
	alphaTab := model.Tab{ID: "tab-1", WorkspaceID: alpha.ID, Name: "1", Running: true,
		Agent: model.AgentClaude, AgentPhase: model.AgentPhaseWaitingInput}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        root,
		Directories: []model.WorkspaceView{{Workspace: alpha, Tabs: []model.Tab{alphaTab}}},
	}}}

	backend := &fakeBackend{snapshot: snapshot, workspace: alpha}
	value := newDashboard(backend, snapshot)
	value.selectedWorkspaceID = alpha.ID
	value.selectedPath = alpha.Path
	value.terminal = newEmbeddedTerminal(alphaTab.ID, newMemoryStream(""), 40, 10)
	value.focus = leftPane
	t.Cleanup(func() { value.closeTerminal() })

	updated, command := value.Update(jumpToWaitingAgentKey())
	value = updated.(dashboard)
	if command != nil || backend.openedTab != "" {
		t.Fatalf("jump to the open terminal = (command %v, opened %q), want no reattach",
			command, backend.openedTab)
	}
	if value.focus != terminalPane {
		t.Fatalf("focus = %v, want the terminal", value.focus)
	}
}

func TestDashboardJumpSaysWhenNoAgentIsWaiting(t *testing.T) {
	root := model.Root{ID: "root-1", Name: "projects", Path: "/projects"}
	alpha := model.Workspace{ID: "workspace-1", RootID: root.ID, Name: "alpha", Path: "/projects/alpha"}
	alphaTab := model.Tab{ID: "tab-1", WorkspaceID: alpha.ID, Name: "1", Running: true,
		Agent: model.AgentClaude, AgentPhase: model.AgentPhaseWorking}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        root,
		Directories: []model.WorkspaceView{{Workspace: alpha, Tabs: []model.Tab{alphaTab}}},
	}}}

	value := newDashboard(&fakeBackend{snapshot: snapshot}, snapshot)
	updated, command := value.Update(jumpToWaitingAgentKey())
	value = updated.(dashboard)
	if command != nil {
		t.Fatalf("jump with nothing waiting produced %v, want no command", command)
	}
	if value.errorMessage != "no agent is waiting" || !value.noticeMessage {
		t.Fatalf("status = (%q, notice %v), want a note that nothing is waiting",
			value.errorMessage, value.noticeMessage)
	}
}

func jumpToWaitingAgentKey() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: 'a', ShiftedCode: 'A', Mod: tea.ModCtrl | tea.ModShift})
}

// pressJumpToWaitingAgent presses Ctrl+Shift+A and settles the commands the
// jump produces, the way pressSwitchTab settles a switch.
func pressJumpToWaitingAgent(t *testing.T, value dashboard) dashboard {
	t.Helper()
	updated, command := value.Update(jumpToWaitingAgentKey())
	value = updated.(dashboard)
	if command == nil {
		t.Fatal("Ctrl+Shift+A produced no command")
	}
	for step := 0; command != nil && step < 4; step++ {
		message := command()
		if _, reading := message.(terminalOutputMsg); reading {
			break
		}
		updated, command = value.Update(message)
		value = updated.(dashboard)
	}
	return value
}

func switchTabKey(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Mod: tea.ModCtrl | tea.ModShift})
}

func newTabKey() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: 't', ShiftedCode: 'T', Mod: tea.ModCtrl | tea.ModShift})
}

// pressSwitchTab presses Ctrl+Shift+code and runs the commands the switch
// produces until it has settled: the workspace pane selects a workspace before
// opening a tab, the terminal pane opens one straight away, and either way the
// terminal that comes back has to be attached. The read command that ends the
// chain is left alone; reading is what the terminal does from then on.
func pressSwitchTab(t *testing.T, value dashboard, code rune) dashboard {
	t.Helper()
	updated, command := value.Update(switchTabKey(code))
	value = updated.(dashboard)
	if command == nil {
		t.Fatalf("Ctrl+Shift+%s produced no command", switchTabKey(code).String())
	}
	for step := 0; command != nil && step < 4; step++ {
		message := command()
		if _, reading := message.(terminalOutputMsg); reading {
			break
		}
		updated, command = value.Update(message)
		value = updated.(dashboard)
	}
	return value
}

// The tab rail in the terminal pane shows the open terminal's tabs, not the
// cursor's: the workspace cursor is free to wander while a terminal is open.
// Switching has to move along the row on screen.
func TestDashboardSwitchesTabsOfTheOpenTerminalNotTheCursor(t *testing.T) {
	open := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	other := model.Workspace{ID: "workspace-2", RootID: "root-1", Name: "beta", Path: "/projects/beta"}
	openTabs := []model.Tab{
		{ID: "tab-1", WorkspaceID: open.ID, Name: "1", Running: true},
		{ID: "tab-2", WorkspaceID: open.ID, Name: "2", Running: true},
	}
	otherTab := model.Tab{ID: "tab-3", WorkspaceID: other.ID, Name: "1", Running: true}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{
			{Workspace: open, Tabs: openTabs},
			{Workspace: other, Tabs: []model.Tab{otherTab}},
		},
	}}}
	backend := &fakeBackend{snapshot: snapshot, workspace: other}
	value := newDashboard(backend, snapshot)
	value.selectedWorkspaceID = open.ID
	value.selectedPath = open.Path
	value.terminal = newEmbeddedTerminal(openTabs[0].ID, newMemoryStream(""), 40, 10)
	value.focus = terminalPane
	// The cursor was left on the other workspace, which is what the workspace
	// pane would switch along.
	value.setNavigation(2)
	defer func() { value.closeTerminal() }()

	value = pressSwitchTab(t, value, tea.KeyRight)
	if value.terminal.id != openTabs[1].ID || backend.ensuredPath != "" {
		t.Fatalf("Ctrl+Shift+Right = (terminal %q, ensured %q), want the open workspace's second tab",
			value.terminal.id, backend.ensuredPath)
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
	value.setNavigation(1)
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
	updated, createCommand := value.Update(commandMessage[workspaceMsg](t, selectCommand))
	value = updated.(dashboard)
	if createCommand == nil {
		t.Fatal("Enter on + did not produce a create command")
	}
	createdMessage := commandMessage[tabMsg](t, createCommand)
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
	value.setNavigation(1)
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
	if !strings.Contains(plain, "▾ nalbam") || !strings.Contains(plain, "▌ - SnowClash") {
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
	if !strings.Contains(plain, "  - TankClash") {
		t.Fatalf("workspace without tabs is missing:\n%s", rendered)
	}

	value.tabIndex = 3
	updated, _ := value.Update(snapshotMsg{value: snapshot})
	value = updated.(dashboard)
	if value.tabIndex != 2 {
		t.Fatalf("tab index after exited tab removal = %d, want + index 2", value.tabIndex)
	}
}

func TestDashboardReplacesControlCharactersInLabels(t *testing.T) {
	hostile := "name\x1b]8;;https://example.com\x07\n"
	safe := "name�]8;;https://example.com��"
	safePrefix := "name�]8;;https://"
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: hostile, Path: "/projects/workspace"}
	value := newDashboard(&fakeBackend{}, model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root-1", Name: hostile, Path: "/projects"},
		Directories: []model.WorkspaceView{{
			Workspace: workspace,
			Tabs:      []model.Tab{{ID: "tab-1", Name: hostile, Running: true}},
		}},
	}}})
	value.width = 120
	value.height = 40
	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path

	rendered := value.render()
	if strings.Contains(rendered, "\x1b]8;;https://example.com") {
		t.Fatalf("dashboard rendered a terminal control sequence:\n%s", rendered)
	}
	if plain := ansi.Strip(rendered); strings.Count(plain, safePrefix) < 2 {
		t.Fatalf("dashboard labels do not contain the safe replacement prefix %q:\n%s", safePrefix, plain)
	}
	tabBar := strings.Join(renderTabBar(value.styles, []model.Tab{{Name: hostile}}, 0, 120), "\n")
	if strings.Contains(tabBar, "\x1b]8;;https://example.com") || !strings.Contains(ansi.Strip(tabBar), safe) {
		t.Fatalf("tab label was not rendered safely: %q", tabBar)
	}

	value.inputMode = true
	value.input = hostile
	if rendered := value.renderStatus(120, 36)[1]; strings.Contains(rendered, "\x1b]8;;https://example.com") ||
		!strings.Contains(ansi.Strip(rendered), safe) {
		t.Fatalf("root input was not rendered safely: %q", rendered)
	}

	value.inputMode = false
	value.setError(treeError, hostile)
	if rendered := value.renderStatus(120, 36)[1]; strings.Contains(rendered, "\x1b]8;;https://example.com") ||
		!strings.Contains(ansi.Strip(rendered), safe) {
		t.Fatalf("error was not rendered safely: %q", rendered)
	}

	value.modal = removeSelectionModal
	value.removeTarget = navItem{root: model.Root{Name: hostile}, isRoot: true}
	if rendered := strings.Join(value.renderModal(120, 36), "\n"); strings.Contains(rendered, "\x1b]8;;https://example.com") ||
		!strings.Contains(ansi.Strip(rendered), safe) {
		t.Fatalf("confirmation was not rendered safely: %q", rendered)
	}
}

func TestOpenTabMarkersUseAgentColorsAndPhaseShapes(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	tabs := []model.Tab{
		{Running: true, Agent: model.AgentClaude, AgentPhase: model.AgentPhaseIdle},
		{Running: true, Agent: model.AgentCodex, AgentPhase: model.AgentPhaseWaitingInput},
		{Running: true, Agent: model.AgentClaude, AgentPhase: model.AgentPhaseWaitingApproval},
		{Running: true, Agent: model.AgentCodex, AgentPhase: model.AgentPhaseError},
		{Running: true, Agent: model.AgentClaude, AgentPhase: model.AgentPhaseWorking},
		{Running: true},
	}

	base := value.styles.navigationSelected
	markers := openTabMarkers(value.styles, base, tabs, 2)
	claude := base.Foreground(value.styles.agentClaude.GetForeground()).Render("○")
	codex := base.Foreground(value.styles.agentCodex.GetForeground()).Render("▲")
	approval := base.Foreground(value.styles.agentClaude.GetForeground()).Render("■")
	failure := base.Foreground(value.styles.agentCodex.GetForeground()).Render("★")
	working := base.Foreground(value.styles.agentClaude.GetForeground()).Render("◑")
	plain := base.Render("●")
	if markers != claude+codex+approval+failure+working+plain {
		t.Fatalf("open tab markers = %q, want agent colors and phase shapes", markers)
	}
}

func TestOpenTabMarkersCycleThroughEveryAnimationFrame(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	base := value.styles.navigationSelected
	style := base.Foreground(value.styles.agentClaude.GetForeground())
	tabs := []model.Tab{{
		Running: true, Agent: model.AgentClaude, AgentPhase: model.AgentPhaseWorking,
	}}

	for index, frame := range agentAnimationFrames {
		marker := openTabMarkers(value.styles, base, tabs, index)
		if marker != style.Render(frame) || lipgloss.Width(marker) != 1 {
			t.Fatalf("animation frame %d = %q with width %d, want %q with width 1", index, marker, lipgloss.Width(marker), frame)
		}
	}
}

func TestAgentAnimationRunsOnlyWhileWorkIsActive(t *testing.T) {
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root-1"},
		Tabs: []model.Tab{{
			ID: "tab-1", Running: true, Agent: model.AgentClaude, AgentPhase: model.AgentPhaseWorking,
		}},
	}}}
	value := newDashboard(&fakeBackend{}, snapshot)
	if !value.agentAnimationActive || !value.agentAnimationPending {
		t.Fatal("working agent did not schedule the initial animation tick")
	}

	updated, command := value.Update(agentAnimationMsg{})
	value = updated.(dashboard)
	if value.agentAnimationFrame != 1 || !value.agentAnimationPending || command == nil {
		t.Fatalf("animation tick = (frame %d, pending %v, command %v), want next frame and tick", value.agentAnimationFrame, value.agentAnimationPending, command)
	}

	value.updateAgents(map[string]model.AgentStatus{
		"tab-1": {Agent: model.AgentClaude, Phase: model.AgentPhaseIdle},
	})
	updated, command = value.Update(agentAnimationMsg{})
	value = updated.(dashboard)
	if value.agentAnimationFrame != 1 || value.agentAnimationActive || value.agentAnimationPending || command != nil {
		t.Fatalf("idle animation = (frame %d, pending %v, command %v), want stopped frame", value.agentAnimationFrame, value.agentAnimationPending, command)
	}

	updated, command = value.Update(agentSnapshotMsg{value: map[string]model.AgentStatus{
		"tab-1": {Agent: model.AgentClaude, Phase: model.AgentPhaseThinking},
	}})
	value = updated.(dashboard)
	if !value.agentAnimationActive || !value.agentAnimationPending || command == nil {
		t.Fatal("new working phase did not restart the animation")
	}
}

func TestNavigationShowsGitStateOnSecondLine(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "SnowClash", Path: "/projects/SnowClash"}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{
			Workspace: workspace,
			Tabs:      []model.Tab{{ID: "tab-1", Running: true}},
		}},
	}}}
	value := newDashboard(&fakeBackend{}, snapshot)
	value.gitStates = map[string]gitState{workspace.Path: {
		Branch: "main",
		Dirty:  true,
		Ahead:  1,
		Behind: 3,
	}}
	value.width = 120
	value.height = 40

	navigation := ansi.Strip(strings.Join(value.renderNavigation(28, 36), "\n"))
	lines := strings.Split(navigation, "\n")
	for index, line := range lines {
		if strings.Contains(line, workspace.Name) {
			if !strings.HasSuffix(strings.TrimRight(line, " "), "●") {
				t.Fatalf("workspace row = %q, want terminal marker", line)
			}
			if index+1 >= len(lines) || strings.TrimSpace(lines[index+1]) != "(main*) ↑1 ↓3" {
				t.Fatalf("Git row after %q = %q, want branch and status", line, lines[index+1])
			}
			return
		}
	}
	t.Fatalf("navigation does not contain workspace %q:\n%s", workspace.Name, navigation)
}

func TestNavigationUsesOneRootLineAndTwoWorkspaceLines(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{
			{Workspace: model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "first", Path: "/projects/first"}},
			{Workspace: model.Workspace{ID: "workspace-2", RootID: "root-1", Name: "last", Path: "/projects/last"}},
		},
	}}})
	value.focus = terminalPane
	value.selectedPath = "/projects/first"
	value.gitStates = map[string]gitState{"/projects/first": {Branch: "main"}}
	lines := value.renderNavigation(28, 20)
	if len(lines) != 7 {
		t.Fatalf("navigation lines = %d, want header, one root line, and two lines per workspace\n%s", len(lines), strings.Join(lines, "\n"))
	}
	plain := make([]string, len(lines))
	for index, line := range lines {
		plain[index] = strings.TrimRight(ansi.Strip(line), " ")
	}
	if plain[2] != " ▾ projects" || plain[3] != "▎ - first" || plain[4] != "▎   (main)" {
		t.Fatalf("root and workspace rows = %#v", plain[2:5])
	}
	if plain[6] != "" {
		t.Fatalf("last workspace continuation = %q, want blank", plain[6])
	}
}

func TestNavigationSeparatesRootsWithOneBlankLine(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{Roots: []model.RootView{
		{Root: model.Root{ID: "root-1", Name: "projects", Path: "/projects"}},
		{Root: model.Root{ID: "root-2", Name: "archive", Path: "/archive"}},
	}})
	lines := value.renderNavigation(28, 20)
	if len(lines) != 5 {
		t.Fatalf("navigation lines = %d, want header, two roots, and one separator\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if strings.TrimSpace(ansi.Strip(lines[3])) != "" || strings.TrimSpace(ansi.Strip(lines[4])) != "▾ archive" {
		t.Fatalf("second root rows = %#v, want a blank line before archive", lines[3:5])
	}
}

func TestGitStatusUpdatesStateAndSchedulesAnotherRefresh(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.gitStates = map[string]gitState{"/old": {Behind: 2}}

	updated, command := value.Update(gitStatusMsg{value: map[string]gitState{"/new": {Behind: 1}}, reschedule: true})
	value = updated.(dashboard)
	if !reflect.DeepEqual(value.gitStates, map[string]gitState{"/new": {Behind: 1}}) {
		t.Fatalf("Git status = %#v, want only the current result", value.gitStates)
	}
	if command == nil {
		t.Fatal("Git status did not schedule another refresh")
	}
}

func TestGitStatusFetchesAtStartupAndAfterInterval(t *testing.T) {
	previousNow := now
	current := time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return current }
	t.Cleanup(func() { now = previousNow })

	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	beforeInterval := value.readGitStatus(false, true)().(gitStatusMsg)
	if !beforeInterval.fetchedAt.IsZero() {
		t.Fatalf("Git fetch before interval = %v, want local status only", beforeInterval.fetchedAt)
	}

	current = current.Add(gitFetchInterval)
	afterInterval := value.readGitStatus(false, true)().(gitStatusMsg)
	if afterInterval.fetchedAt != current {
		t.Fatalf("Git fetch after interval = %v, want %v", afterInterval.fetchedAt, current)
	}
}

func TestInitialGitStatusSeparatesLocalReadFromFetch(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root-1", Path: "/projects"},
		Directories: []model.WorkspaceView{{
			Workspace: model.Workspace{ID: "workspace-1", RootID: "root-1", Path: "/projects/first"},
		}},
	}}})
	if paths := value.workspacePaths(); !reflect.DeepEqual(paths, []string{"/projects/first"}) {
		t.Fatalf("Git paths = %#v, want workspaces without the root", paths)
	}
	var local, remote int
	for _, message := range commandMessages(value.initialGitStatus()) {
		status, ok := message.(gitStatusMsg)
		if !ok {
			continue
		}
		if status.fetchedAt.IsZero() && status.reschedule {
			local++
		}
		if !status.fetchedAt.IsZero() && !status.reschedule {
			remote++
		}
	}
	if local != 1 || remote != 1 {
		t.Fatalf("initial Git reads = (local %d, remote %d), want one of each", local, remote)
	}
}

func TestAgentSnapshotUpdatesStateAndSchedulesAnotherRefresh(t *testing.T) {
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Tabs: []model.Tab{{ID: "tab-1", Running: true}},
	}}}
	value := newDashboard(&fakeBackend{}, snapshot)

	updated, command := value.Update(agentSnapshotMsg{value: map[string]model.AgentStatus{
		"tab-1": {Agent: model.AgentClaude, Phase: model.AgentPhaseWaitingApproval},
	}})
	value = updated.(dashboard)
	if got := value.state.Roots[0].Tabs[0].Agent; got != model.AgentClaude {
		t.Fatalf("agent = %q, want %q", got, model.AgentClaude)
	}
	if got := value.state.Roots[0].Tabs[0].AgentPhase; got != model.AgentPhaseWaitingApproval {
		t.Fatalf("agent phase = %q, want %q", got, model.AgentPhaseWaitingApproval)
	}
	if command == nil {
		t.Fatal("agent snapshot did not schedule another refresh")
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

func TestDashboardRestoresTheLastWorkspaceAndTab(t *testing.T) {
	alpha := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	bravo := model.Workspace{ID: "workspace-2", RootID: "root-1", Name: "bravo", Path: "/projects/bravo"}
	tabs := []model.Tab{
		{ID: "tab-2", WorkspaceID: bravo.ID, Name: "1", Running: true},
		{ID: "tab-3", WorkspaceID: bravo.ID, Name: "2", Running: true},
	}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{
			{Workspace: alpha, Tabs: []model.Tab{{ID: "tab-1", WorkspaceID: alpha.ID, Name: "1", Running: true}}},
			{Workspace: bravo, Tabs: tabs},
		},
	}}}
	backend := &fakeBackend{snapshot: snapshot}
	value := newDashboardWithConfig(backend, snapshot, "", Config{
		LastWorkspacePath: bravo.Path,
		LastTabID:         tabs[1].ID,
	})

	if value.selectedPath != bravo.Path || value.selectedWorkspaceID != bravo.ID ||
		value.navIndex != 2 || value.tabIndex != 1 {
		t.Fatalf("restored selection = (path %q, workspace %q, nav %d, tab %d)",
			value.selectedPath, value.selectedWorkspaceID, value.navIndex, value.tabIndex)
	}

	var opened terminalOpenedMsg
	for _, message := range commandMessages(value.Init()) {
		if candidate, ok := message.(terminalOpenedMsg); ok {
			opened = candidate
		}
	}
	if opened.tabID != tabs[1].ID || backend.openedTab != tabs[1].ID {
		t.Fatalf("startup opened tab = (%q, %q), want %q", opened.tabID, backend.openedTab, tabs[1].ID)
	}
	updated, _ := value.Update(opened)
	value = updated.(dashboard)
	t.Cleanup(value.closeTerminal)
	if value.terminal == nil || value.terminal.id != tabs[1].ID || value.focus != terminalPane {
		t.Fatalf("restored terminal = (%v, focus %v), want %q", value.terminal, value.focus, tabs[1].ID)
	}
}

func TestDashboardDoesNotRestoreAMissingWorkspaceOrTab(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{
			Workspace: workspace,
			Tabs: []model.Tab{
				{ID: "running", WorkspaceID: workspace.ID, Running: true},
				{ID: "stopped", WorkspaceID: workspace.ID},
			},
		}},
	}}}

	for _, probe := range []Config{
		{LastWorkspacePath: "/projects/missing", LastTabID: "running"},
		{LastWorkspacePath: workspace.Path, LastTabID: "missing"},
		{LastWorkspacePath: workspace.Path, LastTabID: "stopped"},
	} {
		value := newDashboardWithConfig(&fakeBackend{}, snapshot, "", probe)
		if value.selectedPath != "" || value.selectedWorkspaceID != "" || value.restorePending {
			t.Fatalf("invalid config %#v restored selection = (path %q, workspace %q, pending %t)",
				probe, value.selectedPath, value.selectedWorkspaceID, value.restorePending)
		}
	}
}

func TestDashboardPersistsTheLastOpenedWorkspaceAndTab(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	tabs := []model.Tab{
		{ID: "tab-1", WorkspaceID: workspace.ID, Name: "1", Running: true},
		{ID: "tab-2", WorkspaceID: workspace.ID, Name: "2", Running: true},
	}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace, Tabs: tabs}},
	}}}
	configPath := filepath.Join(t.TempDir(), "config.json")
	value := newDashboardWithConfig(&fakeBackend{}, snapshot, configPath, Config{})
	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path
	value.tabIndex = 1

	updated, command := value.Update(terminalOpenedMsg{tabID: tabs[1].ID, stream: newMemoryStream("")})
	value = updated.(dashboard)
	t.Cleanup(value.closeTerminal)
	if command == nil {
		t.Fatal("opening a different tab did not save its selection")
	}
	for _, message := range commandMessages(command) {
		if saved, ok := message.(configSavedMsg); ok {
			updated, _ = value.Update(saved)
			value = updated.(dashboard)
		}
	}
	loaded, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if loaded.LastWorkspacePath != workspace.Path || loaded.LastTabID != tabs[1].ID {
		t.Fatalf("saved selection = (%q, %q), want (%q, %q)",
			loaded.LastWorkspacePath, loaded.LastTabID, workspace.Path, tabs[1].ID)
	}
}

func TestDashboardRewritesAStaleSelectionSave(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	tabs := []model.Tab{
		{ID: "tab-1", WorkspaceID: workspace.ID, Running: true},
		{ID: "tab-2", WorkspaceID: workspace.ID, Running: true},
	}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace, Tabs: tabs}},
	}}}
	configPath := filepath.Join(t.TempDir(), "config.json")
	value := newDashboardWithConfig(&fakeBackend{}, snapshot, configPath, Config{})
	value.rememberedWorkspacePath = workspace.Path
	value.rememberedTabID = tabs[1].ID

	updated, rewrite := value.Update(configSavedMsg{
		lastWorkspacePath: workspace.Path,
		lastTabID:         tabs[0].ID,
	})
	value = updated.(dashboard)
	if rewrite == nil {
		t.Fatal("an older selection save was accepted as current")
	}
	rewrite()
	loaded, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if loaded.LastWorkspacePath != workspace.Path || loaded.LastTabID != tabs[1].ID {
		t.Fatalf("rewritten selection = (%q, %q), want (%q, %q)",
			loaded.LastWorkspacePath, loaded.LastTabID, workspace.Path, tabs[1].ID)
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
	value.setNavigation(1)
	value.gitStates = map[string]gitState{"/projects/long": {
		Branch: "feat/a-very-long-workspace-branch-name",
		Dirty:  true,
		Ahead:  123,
		Behind: 456,
	}}

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

func TestDashboardCentersModalOverCoveredDashboard(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 100
	value.height = 30

	updated, _ := value.Update(key('i', "i"))
	value = updated.(dashboard)
	if value.modal != aboutModal {
		t.Fatalf("modal = %v, want about", value.modal)
	}
	rendered := value.render()
	if !strings.Contains(rendered, "About") || !strings.Contains(rendered, "Persistent terminal workspace manager") {
		t.Fatalf("about modal is missing:\n%s", rendered)
	}
	if strings.Contains(rendered, value.styles.paneTitleActive.Render(" romty ")) {
		t.Fatalf("dashboard remains visible behind the modal:\n%s", rendered)
	}
	modalLines := value.renderModal(value.width, value.dimensions().bodyHeight)
	if width := lipgloss.Width(modalLines[0]); width != 72 {
		t.Fatalf("about modal width = %d, want 72", width)
	}
	bodyHeight := value.dimensions().bodyHeight
	body := strings.Split(ansi.Strip(rendered), "\n")[:bodyHeight]
	top := (bodyHeight - len(modalLines)) / 2
	if left := strings.Index(body[top], "╭"); left != 14 {
		t.Fatalf("about modal left edge = %d, want 14:\n%s", left, ansi.Strip(rendered))
	}
	for row := range body {
		if row >= top && row < top+len(modalLines) {
			continue
		}
		if strings.TrimSpace(body[row]) != "" {
			t.Fatalf("modal backdrop row %d is not blank:\n%s", row, ansi.Strip(rendered))
		}
	}
	// About is where a user reads which romty they are running, so it names
	// the build rather than leaving a bug report to guess at it.
	if !strings.Contains(ansi.Strip(rendered), version.String()) {
		t.Fatalf("about modal does not show version %q:\n%s", version.String(), ansi.Strip(rendered))
	}

	updated, _ = value.Update(key(tea.KeyEscape, ""))
	value = updated.(dashboard)
	if value.modal != noModal || strings.Contains(value.render(), "Persistent terminal workspace manager") {
		t.Fatalf("about modal did not close:\n%s", value.render())
	}
}

// Help used to answer to `?` alone, which the terminal pane forwards to the
// shell, so the pane whose shortcuts are hardest to remember was the one with
// no way to look them up.
func TestDashboardOpensHelpFromTheTerminalPane(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.focus = terminalPane
	value.terminal = newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)

	updated, command := value.Update(key('?', "?"))
	value = updated.(dashboard)
	if value.modal != noModal || command != nil {
		t.Fatalf("? in the terminal = (modal %v, command %v), want it forwarded to the shell", value.modal, command)
	}

	updated, command = value.Update(key(tea.KeyF1, ""))
	value = updated.(dashboard)
	if value.modal != helpModal || command != nil {
		t.Fatalf("F1 in the terminal = (modal %v, command %v), want the help modal", value.modal, command)
	}
}

func TestDashboardShowsCompleteShortcutReferenceInHelpModal(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 100
	value.height = 80
	if entries := value.helpEntries(); len(entries) != 42 {
		t.Fatalf("help entries = %d, want 7 sections and 35 shortcuts", len(entries))
	}

	updated, command := value.Update(key('?', "?"))
	value = updated.(dashboard)
	if value.modal != helpModal || command != nil {
		t.Fatalf("? result = (modal %v, command %v), want help modal", value.modal, command)
	}
	bodyHeight := value.dimensions().bodyHeight
	modalLines := value.renderModal(value.width, bodyHeight)
	if width := lipgloss.Width(modalLines[0]); width != 80 {
		t.Fatalf("help modal width = %d, want 80", width)
	}
	plainLines := strings.Split(ansi.Strip(strings.Join(modalLines, "\n")), "\n")
	plain := strings.Join(plainLines, "\n")
	for _, section := range []string{"GLOBAL", "WORKSPACE", "SWITCH", "MOVE", "FILE DIFF", "CONTEXT"} {
		if !strings.Contains(plain, section) {
			t.Fatalf("help modal does not contain %q section:\n%s", section, plain)
		}
	}
	for _, section := range []string{"CORE", "NAVIGATE", "FILES", "SCROLLBACK", "PICKER", "HELP", "CONFIG", "GIT", "PROMPTS"} {
		if strings.Contains(plain, section) {
			t.Fatalf("help modal still contains duplicated %q section:\n%s", section, plain)
		}
	}
	shortcuts := []struct {
		keys        []string
		description string
	}{
		{keys: []string{"F1", "?"}, description: "Help"},
		{keys: []string{"F2"}, description: "Add root"},
		{keys: []string{"F3", ","}, description: "Config"},
		{keys: []string{"F4", "Ctrl+C"}, description: "Quit"},
		{keys: []string{"F5"}, description: "Refresh workspaces/files"},
		{keys: []string{"F6", "Ctrl+Shift+\\"}, description: "Toggle scrollback"},
		{keys: []string{"F7", "Ctrl+/"}, description: "Toggle pane focus"},
		{keys: []string{"F8"}, description: "Remove selection"},
		{keys: []string{"F9"}, description: "Stop daemon"},
		{keys: []string{"i"}, description: "About"},
		{keys: []string{"Tab"}, description: "Focus terminal"},
		{keys: []string{"Ctrl+Shift+T"}, description: "New tab"},
		{keys: []string{"Ctrl+Shift+G"}, description: "Git actions"},
		{keys: []string{"Ctrl+Shift+F"}, description: "Toggle file view"},
		{keys: []string{"Ctrl+Shift+←/→"}, description: "Switch tab"},
		{keys: []string{"Ctrl+Shift+↑/↓"}, description: "Switch workspace"},
		{keys: []string{"↑/↓", "k/j"}, description: "Move one item / line"},
		{keys: []string{"←/→", "h/l"}, description: "Tab; picker child/parent"},
		{keys: []string{"PgUp/PgDn", "Ctrl+B/F"}, description: "Previous / next page"},
		{keys: []string{"Home/End", "g/G"}, description: "First / last item/line"},
		{keys: []string{"Shift+PgUp/PgDn"}, description: "Enter / page scrollback"},
		{keys: []string{"Wheel"}, description: "Scroll Help/history/diff"},
		{keys: []string{"F6"}, description: "Toggle diff layout"},
		{keys: []string{"Ctrl+↑/↓"}, description: "Scroll diff one line"},
		{keys: []string{"Enter"}, description: "Activate / submit"},
		{keys: []string{"Esc"}, description: "Close / cancel / leave"},
		{keys: []string{"/"}, description: "Type a picker path"},
		{keys: []string{"Backspace"}, description: "Erase path character"},
		{keys: []string{"←/→", "[/]"}, description: "Adjust pane width"},
		{keys: []string{"m"}, description: "Toggle scrollback mouse"},
	}
	for _, shortcut := range shortcuts {
		if !helpContainsShortcut(plainLines, shortcut.description, shortcut.keys...) {
			t.Fatalf("help modal does not contain %v %q shortcut:\n%s", shortcut.keys, shortcut.description, plain)
		}
	}
	for _, name := range []string{"F1", "F2", "F3", "F4", "F5", "F6", "F7", "F8", "F9"} {
		want := 1
		if name == "F6" {
			want = 2
		}
		if count := strings.Count(plain, name); count != want {
			t.Fatalf("help modal contains %s %d times, want %d:\n%s", name, count, want, plain)
		}
	}
	help := strings.Join(value.helpEntries(), "\n")
	for _, key := range []string{"a", "q", "r", "s", "d", "t", "v"} {
		if strings.Contains(help, value.styles.shortcutKey.Render(" "+key+" ")) {
			t.Fatalf("help modal still contains removed %q shortcut:\n%s", key, plain)
		}
	}
	if !strings.Contains(plain, version.String()) {
		t.Fatalf("help modal does not show version %q:\n%s", version.String(), plain)
	}
	if len(modalLines) > value.height-2 {
		t.Fatalf("help modal height = %d, want at most %d", len(modalLines), value.height-2)
	}
	for index, line := range modalLines {
		if lineWidth := lipgloss.Width(line); lineWidth > 80 {
			t.Fatalf("help modal line %d width = %d, want at most 80", index, lineWidth)
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
	if !strings.Contains(plain, "Adjust pane width") {
		t.Fatalf("help modal did not scroll to the last shortcut:\n%s", plain)
	}
	if strings.Contains(plain, "About") {
		t.Fatalf("help modal kept the first shortcut after scrolling to the end:\n%s", plain)
	}
}

func TestDashboardScrollsHelpModalWithMouseWheel(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 100
	value.height = 20
	updated, _ := value.Update(key('?', "?"))
	value = updated.(dashboard)

	if value.View().MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("help mouse mode = %v, want all motion", value.View().MouseMode)
	}
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "↑/↓/Wheel") {
		t.Fatalf("help status does not advertise wheel scrolling:\n%s", rendered)
	}
	updated, _ = value.Update(tea.MouseWheelMsg{X: 50, Y: 10, Button: tea.MouseWheelDown})
	value = updated.(dashboard)
	if value.helpOffset != 3 {
		t.Fatalf("wheel down help offset = %d, want 3", value.helpOffset)
	}
	updated, _ = value.Update(tea.MouseWheelMsg{X: 50, Y: 10, Button: tea.MouseWheelUp})
	value = updated.(dashboard)
	if value.helpOffset != 0 {
		t.Fatalf("wheel up help offset = %d, want 0", value.helpOffset)
	}

	updated, _ = value.Update(key(tea.KeyEscape, ""))
	value = updated.(dashboard)
	if value.View().MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("mouse mode after closing help = %v, want dashboard clicks enabled", value.View().MouseMode)
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
	defer terminal.close()
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
	if loaded.LeftWidth != 29 || !strings.Contains(value.render(), "Left pane: 29") {
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

func controlBackslashKey() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: '\\', Mod: tea.ModCtrl})
}

// controlSlashKey is Ctrl+/ as a terminal that speaks the Kitty keyboard
// protocol reports it, and controlUnderscoreKey is the same press from every
// other terminal, which sends 0x1F and is decoded as Ctrl+_.
func controlSlashKey() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: '/', Mod: tea.ModCtrl})
}

func controlUnderscoreKey() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: '_', Mod: tea.ModCtrl})
}

func scrollbackToggleKey() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: '\\', ShiftedCode: '|', Mod: tea.ModCtrl | tea.ModShift})
}

// The backoff damps a terminal that is dropping now, not one that has dropped
// before. Retries used to only ever accumulate, so a tab that reconnected and
// then worked for an afternoon still counted its old drops towards the ceiling
// and was eventually left saying it keeps disconnecting.
func TestDashboardForgetsRetriesAfterAHealthyAttach(t *testing.T) {
	tabs := []model.Tab{{ID: "tab-1", WorkspaceID: "workspace-1", Name: "1", Running: true}}
	value, _, refresh := exitedDashboard(t, tabs, 0)

	// One immediate reconnect, one drop, one counted retry — the same walk the
	// backoff test makes, so the two disagree only about what time it is.
	updated, open := value.Update(refresh())
	value = updated.(dashboard)
	updated, _ = value.Update(open())
	value = updated.(dashboard)
	updated, _ = value.Update(terminalOutputMsg{terminal: value.terminal, err: io.EOF})
	value = updated.(dashboard)
	updated, _ = value.Update(refresh())
	value = updated.(dashboard)
	if value.reattachAttempts != 1 {
		t.Fatalf("retries after one drop = %d, want 1", value.reattachAttempts)
	}

	// Reopen, then let the terminal run past the healthy interval before it
	// drops. That drop starts over: an immediate reconnect, not a delayed one.
	updated, open = value.Update(reopenTerminalMsg{tabID: "tab-1"})
	value = updated.(dashboard)
	updated, _ = value.Update(open())
	value = updated.(dashboard)
	value.terminalOpenedAt = value.terminalOpenedAt.Add(-2 * healthyAttachInterval)

	updated, _ = value.Update(terminalOutputMsg{terminal: value.terminal, err: io.EOF})
	value = updated.(dashboard)
	updated, command := value.Update(refresh())
	value = updated.(dashboard)
	if value.reattachAttempts != 0 {
		t.Fatalf("retries after a healthy attach = %d, want them forgotten", value.reattachAttempts)
	}
	if command == nil {
		t.Fatal("no reconnect after a healthy attach dropped")
	}
	if _, delayed := command().(reopenTerminalMsg); delayed {
		t.Fatal("a drop after a healthy attach was delayed, want an immediate reconnect")
	}
}

// A drop that follows a fresh attach is another turn of the same loop, so the
// healthy-attach reset must not hand it an immediate retry.
func TestDashboardStillBacksOffWhenDropsAreImmediate(t *testing.T) {
	tabs := []model.Tab{{ID: "tab-1", WorkspaceID: "workspace-1", Name: "1", Running: true}}
	value, _, refresh := exitedDashboard(t, tabs, 0)

	updated, open := value.Update(refresh())
	value = updated.(dashboard)
	updated, _ = value.Update(open())
	value = updated.(dashboard)
	if value.terminalOpenedAt.IsZero() {
		t.Fatal("an opened terminal recorded no attach time")
	}
	updated, _ = value.Update(terminalOutputMsg{terminal: value.terminal, err: io.EOF})
	value = updated.(dashboard)
	updated, command := value.Update(refresh())
	value = updated.(dashboard)
	if value.reattachAttempts != 1 || command == nil {
		t.Fatalf("an immediate drop = (retries %d, command %v), want one counted retry",
			value.reattachAttempts, command)
	}
	if message := command(); message != (reopenTerminalMsg{tabID: "tab-1"}) {
		t.Fatalf("an immediate drop returned %T, want a delayed reopen", message)
	}
}

func TestDashboardIgnoresAReattachForTheTabItLeft(t *testing.T) {
	tabs := []model.Tab{
		{ID: "tab-1", WorkspaceID: "workspace-1", Name: "1", Running: true},
		{ID: "tab-2", WorkspaceID: "workspace-1", Name: "2", Running: true},
	}
	value, _, refresh := exitedDashboard(t, tabs, 0)

	updated, open := value.Update(refresh())
	value = updated.(dashboard)
	updated, _ = value.Update(open())
	value = updated.(dashboard)
	updated, refresh = value.Update(terminalOutputMsg{terminal: value.terminal, err: io.EOF})
	value = updated.(dashboard)
	updated, delayed := value.Update(refresh())
	value = updated.(dashboard)
	if delayed == nil {
		t.Fatal("the repeated drop produced no delayed reattach")
	}

	updated, _ = value.Update(switchTabKey(tea.KeyRight))
	value = updated.(dashboard)
	if value.selectedTabID() != "tab-2" {
		t.Fatalf("selected tab = %q, want tab-2", value.selectedTabID())
	}
	updated, stale := value.Update(delayed())
	value = updated.(dashboard)
	if stale != nil {
		t.Fatal("a delayed reattach for tab-1 reopened tab-2")
	}
}

// Opening a terminal is a round trip to the daemon, so two quick switches
// leave two of them in flight and nothing orders the answers. The one for the
// tab the user left must not be adopted: it would show that terminal under the
// next tab's name and take the keyboard with it.
func TestDashboardIgnoresATerminalOpenedForTheTabItLeft(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	tabs := []model.Tab{
		{ID: "tab-1", WorkspaceID: workspace.ID, Name: "1", Running: true},
		{ID: "tab-2", WorkspaceID: workspace.ID, Name: "2", Running: true},
		{ID: "tab-3", WorkspaceID: workspace.ID, Name: "3", Running: true},
	}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace, Tabs: tabs}},
	}}}
	backend := &fakeBackend{snapshot: snapshot, workspace: workspace}
	value := newDashboard(backend, snapshot)
	value.setNavigation(1)
	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path
	value.terminal = newEmbeddedTerminal(tabs[0].ID, newMemoryStream(""), 40, 10)
	value.focus = terminalPane
	t.Cleanup(value.closeTerminal)

	// Two presses before either answer lands.
	updated, toSecond := value.Update(switchTabKey(tea.KeyRight))
	value = updated.(dashboard)
	updated, toThird := value.Update(switchTabKey(tea.KeyRight))
	value = updated.(dashboard)
	if toSecond == nil || toThird == nil {
		t.Fatal("two switches did not both ask the daemon for a terminal")
	}
	second, third := toSecond().(terminalOpenedMsg), toThird().(terminalOpenedMsg)

	// The later answer arrives first, which is what the daemon is free to do.
	updated, _ = value.Update(third)
	value = updated.(dashboard)
	updated, _ = value.Update(second)
	value = updated.(dashboard)

	if value.terminal == nil || value.terminal.id != tabs[2].ID {
		t.Fatalf("terminal = %v, want the tab the cursor is on", value.terminal)
	}
	stale, ok := second.stream.(*memoryStream)
	if !ok {
		t.Fatalf("stale stream = %T, want a memory stream", second.stream)
	}
	if !stale.isClosed() {
		t.Fatal("the terminal romty walked away from is still attached to the daemon")
	}
}

func TestDashboardSizesTerminalWhenAttachCompletes(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	tab := model.Tab{ID: "tab-1", WorkspaceID: workspace.ID, Name: "1", Running: true}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace, Tabs: []model.Tab{tab}}},
	}}}
	backend := &fakeBackend{snapshot: snapshot, workspace: workspace}
	value := newDashboard(backend, snapshot)
	value.width = 80
	value.height = 24
	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path
	value.setNavigation(1)

	open := value.openSelectedTerminal()
	if open == nil {
		t.Fatal("running tab did not produce an attach command")
	}
	updated, resize := value.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	value = updated.(dashboard)
	if resize != nil {
		t.Fatal("window resize without an attached terminal contacted the daemon")
	}

	updated, followup := value.Update(open())
	value = updated.(dashboard)
	defer value.closeTerminal()
	if value.terminal == nil || followup == nil {
		t.Fatalf("attach result = (terminal %v, followup %v), want an open terminal", value.terminal, followup)
	}
	if commands, ok := followup().(tea.BatchMsg); ok {
		for _, command := range commands {
			if command != nil {
				command()
			}
		}
	}
	wantColumns, wantRows := value.terminalSize()
	if backend.createdColumns != wantColumns || backend.createdRows != wantRows {
		t.Fatalf("attached PTY size = %dx%d, want current window size %dx%d",
			backend.createdColumns, backend.createdRows, wantColumns, wantRows)
	}
}

// Dragging a window makes a burst of resizes, each asking the daemon on its
// own goroutine, and nothing orders the round trips. Whichever lands last has
// to leave the PTY at the size on screen, not at one the window had on the way
// past — a shell told the wrong size wraps its output wrongly until something
// resizes again.
func TestDashboardResizesToTheSizeOnScreenWhateverTheOrder(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	backend := value.backend.(*fakeBackend)
	value.terminal = newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)
	t.Cleanup(value.closeTerminal)

	updated, wide := value.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	value = updated.(dashboard)
	updated, narrow := value.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	value = updated.(dashboard)
	if wide == nil || narrow == nil {
		t.Fatal("a window resize did not ask the daemon to resize the PTY")
	}
	wantColumns, wantRows := value.terminalSize()

	// The earlier request lands last, which is what two goroutines are free to
	// do.
	narrow()
	wide()
	if backend.createdColumns != wantColumns || backend.createdRows != wantRows {
		t.Fatalf("resized the PTY to %dx%d, want the %dx%d that is on screen",
			backend.createdColumns, backend.createdRows, wantColumns, wantRows)
	}
}
