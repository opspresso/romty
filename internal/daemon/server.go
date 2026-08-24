package daemon

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/nalbam/romty/internal/model"
	"github.com/nalbam/romty/internal/protocol"
	"github.com/nalbam/romty/internal/state"
)

var ErrAlreadyRunning = errors.New("romty daemon is already running")

type Server struct {
	socket string
	store  *state.Store
	shell  string

	mu       sync.Mutex
	value    model.State
	sessions map[string]*session
	listener net.Listener
	stop     chan struct{}
	stopOnce sync.Once
}

func New(socket, statePath, shell string) (*Server, error) {
	store := state.New(statePath)
	value, err := store.Load()
	if err != nil {
		return nil, err
	}
	if shell == "" {
		shell = os.Getenv("SHELL")
	}
	if shell == "" {
		shell = "/bin/sh"
	}
	return &Server{
		socket:   socket,
		store:    store,
		shell:    shell,
		value:    value,
		sessions: make(map[string]*session),
		stop:     make(chan struct{}),
	}, nil
}

func (s *Server) Serve(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.socket), 0o700); err != nil {
		return fmt.Errorf("create socket directory: %w", err)
	}
	if err := prepareSocket(s.socket); err != nil {
		return err
	}
	listener, err := net.Listen("unix", s.socket)
	if err != nil {
		return fmt.Errorf("listen on unix socket: %w", err)
	}
	s.listener = listener
	if err := os.Chmod(s.socket, 0o600); err != nil {
		listener.Close()
		return fmt.Errorf("set socket permissions: %w", err)
	}
	defer s.shutdown()
	if err := s.removeStaleTabs(); err != nil {
		return err
	}

	go func() {
		select {
		case <-ctx.Done():
		case <-s.stop:
		}
		listener.Close()
	}()

	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || s.stopped() {
				return nil
			}
			return fmt.Errorf("accept daemon connection: %w", err)
		}
		go s.handle(connection)
	}
}

func (s *Server) removeStaleTabs() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.value.Tabs) == 0 {
		return nil
	}
	tabs := s.value.Tabs
	s.value.Tabs = []model.Tab{}
	if err := s.store.Save(s.value); err != nil {
		s.value.Tabs = tabs
		return fmt.Errorf("remove stale terminal tabs: %w", err)
	}
	return nil
}

func prepareSocket(path string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect unix socket: %w", err)
	}

	connection, err := net.DialTimeout("unix", path, 200*time.Millisecond)
	if err == nil {
		connection.Close()
		return ErrAlreadyRunning
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale unix socket: %w", err)
	}
	return nil
}

func (s *Server) shutdown() {
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for _, value := range s.sessions {
		sessions = append(sessions, value)
	}
	s.mu.Unlock()
	for _, value := range sessions {
		value.close()
	}
	_ = os.Remove(s.socket)
}

func (s *Server) handle(connection net.Conn) {
	defer connection.Close()
	reader := bufio.NewReader(connection)
	var request protocol.Request
	if err := protocol.Read(reader, &request); err != nil {
		_ = protocol.Write(connection, protocol.Response{Error: err.Error()})
		return
	}

	if request.Action == protocol.ActionAttach {
		s.handleAttach(connection, request.TabID)
		return
	}
	if request.Action == protocol.ActionShutdown {
		if err := protocol.Write(connection, protocol.Response{}); err == nil {
			s.stopOnce.Do(func() { close(s.stop) })
		}
		return
	}
	response := s.dispatch(request)
	_ = protocol.Write(connection, response)
}

func (s *Server) stopped() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

func (s *Server) dispatch(request protocol.Request) protocol.Response {
	switch request.Action {
	case protocol.ActionPing:
		return protocol.Response{}
	case protocol.ActionSnapshot:
		return s.snapshotResponse()
	case protocol.ActionAddRoot:
		return s.addRoot(request.Path)
	case protocol.ActionEnsureWorkspace:
		return s.ensureWorkspace(request.RootID, request.Path)
	case protocol.ActionCreateTab:
		return s.createTab(request.WorkspaceID, request.Columns, request.Rows)
	case protocol.ActionResize:
		return s.resize(request.TabID, request.Columns, request.Rows)
	default:
		return protocol.Response{Error: fmt.Sprintf("unknown action %q", request.Action)}
	}
}

func (s *Server) snapshotResponse() protocol.Response {
	snapshot, err := s.snapshot()
	if err != nil {
		return protocol.Response{Error: err.Error()}
	}
	return protocol.Response{Snapshot: &snapshot}
}

func (s *Server) addRoot(path string) protocol.Response {
	canonical, err := canonicalDirectory(path)
	if err != nil {
		return protocol.Response{Error: err.Error()}
	}

	s.mu.Lock()
	for _, root := range s.value.Roots {
		if root.Path == canonical {
			s.mu.Unlock()
			return s.snapshotResponse()
		}
	}
	root := model.Root{ID: newID(), Name: filepath.Base(canonical), Path: canonical}
	s.value.Roots = append(s.value.Roots, root)
	if err := s.store.Save(s.value); err != nil {
		s.value.Roots = s.value.Roots[:len(s.value.Roots)-1]
		s.mu.Unlock()
		return protocol.Response{Error: err.Error()}
	}
	s.mu.Unlock()
	return s.snapshotResponse()
}

func (s *Server) ensureWorkspace(rootID, path string) protocol.Response {
	canonical, err := canonicalDirectory(path)
	if err != nil {
		return protocol.Response{Error: err.Error()}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	root, ok := findRoot(s.value.Roots, rootID)
	if !ok {
		return protocol.Response{Error: "root not found"}
	}
	if canonical != root.Path && filepath.Dir(canonical) != root.Path {
		return protocol.Response{Error: "workspace must be its root or a direct child"}
	}
	for _, workspace := range s.value.Workspaces {
		if workspace.RootID == rootID && workspace.Path == canonical {
			copy := workspace
			return protocol.Response{Workspace: &copy}
		}
	}

	workspace := model.Workspace{
		ID:     newID(),
		RootID: rootID,
		Name:   filepath.Base(canonical),
		Path:   canonical,
	}
	s.value.Workspaces = append(s.value.Workspaces, workspace)
	if err := s.store.Save(s.value); err != nil {
		s.value.Workspaces = s.value.Workspaces[:len(s.value.Workspaces)-1]
		return protocol.Response{Error: err.Error()}
	}
	return protocol.Response{Workspace: &workspace}
}

func (s *Server) createTab(workspaceID string, columns, rows uint16) protocol.Response {
	s.mu.Lock()
	workspace, ok := findWorkspace(s.value.Workspaces, workspaceID)
	if !ok {
		s.mu.Unlock()
		return protocol.Response{Error: "workspace not found"}
	}
	tab := model.Tab{
		ID:          newID(),
		WorkspaceID: workspaceID,
		Name:        nextTabName(s.value.Tabs, workspaceID),
		Running:     true,
	}

	value, err := startSession(tab.ID, workspace.Path, s.shell, columns, rows, func() {
		s.sessionExited(tab.ID)
	})
	if err != nil {
		s.mu.Unlock()
		return protocol.Response{Error: err.Error()}
	}
	s.value.Tabs = append(s.value.Tabs, tab)
	s.sessions[tab.ID] = value
	if err := s.store.Save(s.value); err != nil {
		s.value.Tabs = s.value.Tabs[:len(s.value.Tabs)-1]
		delete(s.sessions, tab.ID)
		s.mu.Unlock()
		value.close()
		return protocol.Response{Error: err.Error()}
	}
	s.mu.Unlock()
	return protocol.Response{Tab: &tab}
}

func (s *Server) resize(tabID string, columns, rows uint16) protocol.Response {
	s.mu.Lock()
	value, ok := s.sessions[tabID]
	s.mu.Unlock()
	if !ok {
		return protocol.Response{Error: "running terminal session not found"}
	}
	if err := value.resize(columns, rows); err != nil {
		return protocol.Response{Error: err.Error()}
	}
	return protocol.Response{}
}

func (s *Server) handleAttach(connection net.Conn, tabID string) {
	s.mu.Lock()
	value, ok := s.sessions[tabID]
	s.mu.Unlock()
	if !ok {
		_ = protocol.Write(connection, protocol.Response{Error: "running terminal session not found"})
		return
	}
	if err := protocol.Write(connection, protocol.Response{}); err != nil {
		return
	}
	_ = value.attach(connection)
}

func (s *Server) sessionExited(tabID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, tabID)
	for index := range s.value.Tabs {
		if s.value.Tabs[index].ID == tabID {
			s.value.Tabs = append(s.value.Tabs[:index], s.value.Tabs[index+1:]...)
			break
		}
	}
	_ = s.store.Save(s.value)
}

func (s *Server) snapshot() (model.Snapshot, error) {
	s.mu.Lock()
	value := cloneState(s.value)
	s.mu.Unlock()

	result := model.Snapshot{Roots: make([]model.RootView, 0, len(value.Roots))}
	for _, root := range value.Roots {
		entries, err := os.ReadDir(root.Path)
		if err != nil {
			return model.Snapshot{}, fmt.Errorf("read root %q: %w", root.Path, err)
		}
		directories := make([]model.WorkspaceView, 0)
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(root.Path, entry.Name())
			workspace, ok := workspaceAt(value.Workspaces, root.ID, path)
			if !ok {
				workspace = model.Workspace{RootID: root.ID, Name: entry.Name(), Path: path}
			}
			directories = append(directories, model.WorkspaceView{
				Workspace: workspace,
				Tabs:      tabsFor(value.Tabs, workspace.ID),
			})
		}
		sort.Slice(directories, func(i, j int) bool {
			return directories[i].Workspace.Name < directories[j].Workspace.Name
		})
		rootWorkspace, _ := workspaceAt(value.Workspaces, root.ID, root.Path)
		result.Roots = append(result.Roots, model.RootView{
			Root:        root,
			Tabs:        tabsFor(value.Tabs, rootWorkspace.ID),
			Directories: directories,
		})
	}
	return result, nil
}

func canonicalDirectory(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("directory path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve directory: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve directory symlinks: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("inspect directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path is not a directory")
	}
	return filepath.Clean(canonical), nil
}

func newID() string {
	data := make([]byte, 12)
	if _, err := rand.Read(data); err != nil {
		panic(fmt.Sprintf("generate identifier: %v", err))
	}
	return hex.EncodeToString(data)
}

func findRoot(values []model.Root, id string) (model.Root, bool) {
	for _, value := range values {
		if value.ID == id {
			return value, true
		}
	}
	return model.Root{}, false
}

func findWorkspace(values []model.Workspace, id string) (model.Workspace, bool) {
	for _, value := range values {
		if value.ID == id {
			return value, true
		}
	}
	return model.Workspace{}, false
}

func workspaceAt(values []model.Workspace, rootID, path string) (model.Workspace, bool) {
	for _, value := range values {
		if value.RootID == rootID && value.Path == path {
			return value, true
		}
	}
	return model.Workspace{}, false
}

func tabsFor(values []model.Tab, workspaceID string) []model.Tab {
	result := make([]model.Tab, 0)
	for _, value := range values {
		if value.WorkspaceID == workspaceID {
			result = append(result, value)
		}
	}
	return result
}

func nextTabName(values []model.Tab, workspaceID string) string {
	names := make(map[string]struct{})
	for _, value := range values {
		if value.WorkspaceID == workspaceID {
			names[value.Name] = struct{}{}
		}
	}
	for number := 1; ; number++ {
		name := strconv.Itoa(number)
		if _, exists := names[name]; !exists {
			return name
		}
	}
}

func cloneState(value model.State) model.State {
	return model.State{
		Roots:      append([]model.Root(nil), value.Roots...),
		Workspaces: append([]model.Workspace(nil), value.Workspaces...),
		Tabs:       append([]model.Tab(nil), value.Tabs...),
	}
}
