package ui

import (
	"bytes"
	"io"
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
	addedPath      string
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
	return f.snapshot, nil
}

func (f *fakeBackend) EnsureWorkspace(_, _ string) (model.Workspace, error) {
	return f.workspace, nil
}

func (f *fakeBackend) CreateTab(_ string, columns, rows uint16) (model.Tab, error) {
	f.createdColumns = columns
	f.createdRows = rows
	f.snapshot.Roots[0].Directories[0].Tabs = append(f.snapshot.Roots[0].Directories[0].Tabs, f.createdTab)
	return f.createdTab, nil
}

func (f *fakeBackend) OpenTerminal(_ string) (io.ReadWriteCloser, error) {
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
	if command != nil {
		t.Fatal("workspace without tabs unexpectedly opened a terminal")
	}

	updated, command = value.Update(key('+', "+"))
	value = updated.(dashboard)
	if command == nil {
		t.Fatal("create tab command = nil")
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
	if backend.createdColumns != 77 || backend.createdRows != 34 {
		t.Fatalf("terminal size = %dx%d, want right pane size 77x34", backend.createdColumns, backend.createdRows)
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

func key(code rune, text string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Text: text})
}
