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

func TestDashboardFocusesPanesWithOneShotMouseMode(t *testing.T) {
	stream := newMemoryStream("")
	terminal := newEmbeddedTerminal("tab-1", stream, 40, 10)
	defer terminal.closeTerminal()

	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 120
	value.height = 40
	value.terminal = terminal

	if view := value.View(); view.MouseMode != tea.MouseModeNone {
		t.Fatalf("initial mouse mode = %v, want none", view.MouseMode)
	}
	updated, _ := value.Update(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	value = updated.(dashboard)
	if view := value.View(); view.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("armed mouse mode = %v, want cell motion", view.MouseMode)
	}

	updated, _ = value.Update(tea.MouseClickMsg{X: 60, Y: 5, Button: tea.MouseLeft})
	value = updated.(dashboard)
	if value.focus != terminalPane || value.mouseFocusMode {
		t.Fatalf("right click focus = %v, mouse focus mode = %v", value.focus, value.mouseFocusMode)
	}
	if view := value.View(); view.MouseMode != tea.MouseModeNone {
		t.Fatalf("mouse mode after right click = %v, want none", view.MouseMode)
	}
	if rendered := value.render(); strings.Contains(rendered, "[FOCUS]") || !strings.Contains(rendered, "\x1b[1;96mTerminal") {
		t.Fatalf("terminal focus is not visible:\n%s", rendered)
	}

	updated, _ = value.Update(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	value = updated.(dashboard)
	updated, _ = value.Update(tea.MouseClickMsg{X: 10, Y: 5, Button: tea.MouseLeft})
	value = updated.(dashboard)
	if value.focus != leftPane || value.mouseFocusMode {
		t.Fatalf("left click focus = %v, mouse focus mode = %v", value.focus, value.mouseFocusMode)
	}
	if rendered := value.render(); strings.Contains(rendered, "[FOCUS]") || !strings.Contains(rendered, "\x1b[1;96mWorkspaces") {
		t.Fatalf("workspace focus is not visible:\n%s", rendered)
	}

	updated, _ = value.Update(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	value = updated.(dashboard)
	updated, _ = value.Update(key(tea.KeyEscape, ""))
	value = updated.(dashboard)
	if view := value.View(); view.MouseMode != tea.MouseModeNone {
		t.Fatalf("mouse mode after escape = %v, want none", view.MouseMode)
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
	if !strings.Contains(rendered, "  TankClash") {
		t.Fatalf("workspace without tabs is missing:\n%s", rendered)
	}

	value.tabIndex = 2
	updated, _ := value.Update(snapshotMsg{value: snapshot})
	value = updated.(dashboard)
	if value.tabIndex != 1 {
		t.Fatalf("tab index after exited tab removal = %d, want 1", value.tabIndex)
	}
}

func key(code rune, text string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Text: text})
}
