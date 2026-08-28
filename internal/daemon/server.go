package daemon

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/protocol"
	"github.com/opspresso/romty/internal/state"
)

var ErrAlreadyRunning = errors.New("romty daemon is already running")

// requestTimeout bounds both halves of the handshake — reading the request and
// writing the reply — but not the session that may follow it. A local socket
// handshake takes microseconds; this only has to be long enough that no honest
// client ever meets it. It is a variable so tests need not wait.
var requestTimeout = 10 * time.Second

// These are variables so saturation tests can reach the limits without opening
// enough local sockets to make the test host itself the limiting factor.
var maxActiveConnections = 128
var maxTerminalAttachments = 64

var resolveDirectory = canonicalDirectory
var readDirectory = os.ReadDir

type Server struct {
	socket string
	store  *state.Store
	shell  string

	// logger writes to stderr, which EnsureDaemon points at daemon.log. The
	// daemon outlives every client and fails where nobody is watching, so an
	// error that is not written there leaves no trace at all.
	logger *log.Logger

	mu            sync.Mutex
	value         model.State
	revision      uint64
	sessions      map[string]*session
	agentStatuses map[string]agentRuntime
	stop          chan struct{}
	stopOnce      sync.Once
	// stopping is set once shutdown has begun, so a shell exiting because the
	// daemon killed it is not persisted one tab at a time.
	stopping bool

	requestMu sync.Mutex
	accepting bool
	mutations sync.WaitGroup

	connections chan struct{}
	attachments chan struct{}
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
		socket:        socket,
		store:         store,
		shell:         shell,
		logger:        log.New(os.Stderr, "romty: ", log.LstdFlags),
		value:         value,
		sessions:      make(map[string]*session),
		agentStatuses: make(map[string]agentRuntime),
		stop:          make(chan struct{}),
		accepting:     true,
		connections:   make(chan struct{}, maxActiveConnections),
		attachments:   make(chan struct{}, maxTerminalAttachments),
	}, nil
}

// SetLogger redirects the daemon's diagnostics. Tests use it to stay quiet.
func (s *Server) SetLogger(logger *log.Logger) {
	s.logger = logger
}

func (s *Server) Serve(ctx context.Context) error {
	directory := filepath.Dir(s.socket)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create socket directory: %w", err)
	}
	// MkdirAll leaves an existing directory's mode alone, so a romty home that
	// was created group- or world-readable would stay that way. The socket is
	// the only thing standing between another local process and every shell
	// romty owns, so narrow the directory too.
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("set socket directory permissions: %w", err)
	}
	// One daemon at a time, decided before anything touches the socket.
	// prepareSocket unlinks a socket nothing answers on, which is right for
	// one a crash left behind and wrong for one another daemon bound a moment
	// ago: two daemons starting together could both find nothing to dial, and
	// the second would unlink the first's socket and bind its own. The first
	// kept running, listening on a name no client could reach any more, and
	// kept writing the state file the second now owned.
	lock, err := lockDaemon(s.socket + lockSuffix)
	if err != nil {
		return err
	}
	// Released after shutdown removes the socket, so the next daemon to take
	// the lock never finds this one's socket standing.
	defer lock.Close()
	// Before the socket exists, not after. The tabs in the state file name
	// shells that died with the last daemon, and clearing them once the socket
	// was up raced the client that is already polling for it: EnsureDaemon
	// pings every 25ms and asks for a snapshot the moment one answers, so the
	// first tree a user saw could list terminals that were never there.
	if err := s.removeStaleTabs(); err != nil {
		return err
	}
	if err := prepareSocket(s.socket); err != nil {
		return err
	}
	// The socket is created with the process umask applied to 0777, so it is
	// briefly connectable by anyone. Clamp the umask across the bind rather
	// than widening and narrowing again.
	listener, err := listenPrivately(s.socket)
	if err != nil {
		return err
	}
	defer s.shutdown()
	s.logger.Printf("listening on %s", s.socket)
	defer s.logger.Printf("stopped")

	go func() {
		select {
		case <-ctx.Done():
		case <-s.stop:
		}
		s.beginShutdown()
		listener.Close()
	}()

	for {
		connection, err := listener.Accept()
		if err != nil {
			expected := ctx.Err() != nil || s.stopped()
			s.beginShutdown()
			s.mutations.Wait()
			if expected {
				return nil
			}
			return fmt.Errorf("accept daemon connection: %w", err)
		}
		select {
		case s.connections <- struct{}{}:
			go func() {
				defer func() { <-s.connections }()
				s.handle(connection)
			}()
		default:
			connection.Close()
		}
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
	s.revision++
	return nil
}

// lockSuffix names the lock beside the socket it guards. It is derived rather
// than configured because the two only mean anything together: a lock pointed
// at another path guards nothing.
const lockSuffix = ".lock"

// lockDaemon takes the exclusive lock that says which daemon owns the socket.
// The kernel releases it when the process ends however it ends, so a daemon
// that was killed outright leaves nothing to clean up — which a PID file, the
// other way to answer this, would.
func lockDaemon(path string) (*os.File, error) {
	fd, err := syscall.Open(path,
		syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open daemon lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("inspect daemon lock: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		file.Close()
		return nil, fmt.Errorf("daemon lock must be a regular file owned only by the current user")
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, fmt.Errorf("set daemon lock permissions: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("lock daemon: %w", err)
	}
	return file, nil
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

// listenPrivately binds the unix socket so that it is never, even for an
// instant, reachable by another user.
func listenPrivately(path string) (net.Listener, error) {
	previous := syscall.Umask(0o177)
	listener, err := net.Listen("unix", path)
	syscall.Umask(previous)
	if err != nil {
		// The bind is what decides who owns the socket, not the probe in
		// prepareSocket: two daemons starting at once can both find nothing to
		// dial and race to create it. The one that loses is not broken, and
		// reporting that as a bind failure sent it to daemon.log as an error
		// and exited non-zero for what is the ordinary outcome of a race.
		if errors.Is(err, syscall.EADDRINUSE) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("listen on unix socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		return nil, fmt.Errorf("set socket permissions: %w", err)
	}
	return listener, nil
}

func (s *Server) shutdown() {
	s.mu.Lock()
	s.stopping = true
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
	// A client that connects and never finishes a request would otherwise hold
	// a goroutine and a file descriptor for the daemon's whole life, and could
	// grow the read buffer without bound.
	if err := connection.SetReadDeadline(time.Now().Add(requestTimeout)); err != nil {
		return
	}
	reader := bufio.NewReader(connection)
	var request protocol.Request
	if err := protocol.Read(reader, &request); err != nil {
		_ = reply(connection, protocol.Response{Error: err.Error()})
		return
	}

	// Stamped with the version the client selected, not the daemon's own: a
	// client checks the version before it reads the error, so a reply carrying
	// the daemon's version would be refused for the mismatch it is reporting
	// and the sentence naming the remedy would never reach the user.
	if err := checkClientVersion(request); err != nil {
		_ = replyFor(connection, request, protocol.Response{Error: err.Error()})
		return
	}
	if err := checkClientCapability(request); err != nil {
		_ = replyFor(connection, request, protocol.Response{Error: err.Error()})
		return
	}

	// The request is in; what follows is either a long-lived attach or a short
	// reply, and neither wants the read deadline that bounded the handshake.
	if err := connection.SetReadDeadline(time.Time{}); err != nil {
		return
	}
	if request.Action == protocol.ActionAttach {
		select {
		case s.attachments <- struct{}{}:
			defer func() { <-s.attachments }()
		default:
			_ = replyFor(connection, request, protocol.Response{Error: "too many terminal attachments"})
			return
		}
		finish, ok := s.beginRequest(request.Action)
		if !ok {
			_ = replyFor(connection, request, protocol.Response{Error: "daemon is shutting down"})
			return
		}
		finish()
		s.handleAttach(connection, request)
		return
	}
	if request.Action == protocol.ActionShutdown {
		// Stop even when the acknowledgement cannot be delivered, so a client
		// that already timed out never leaves the daemon running unnoticed.
		s.beginShutdown()
		_ = reply(connection, protocol.Response{})
		return
	}
	_ = replyFor(connection, request, s.dispatch(request))
}

// checkClientVersion accepts a selected revision inside the supported range.
// Ping and shutdown stay version-exempt: one negotiates that range and the
// other remains the remedy even when no overlap exists.
func checkClientVersion(request protocol.Request) error {
	if protocol.VersionExempt(request.Action) ||
		request.Version >= protocol.MinimumVersion && request.Version <= protocol.Version {
		return nil
	}
	return fmt.Errorf("daemon supports protocol %d..%d but the client selected %d; %s",
		protocol.MinimumVersion, protocol.Version, request.Version, protocol.Remedy)
}

func checkClientCapability(request protocol.Request) error {
	var required string
	switch request.Action {
	case protocol.ActionAgents:
		required = protocol.CapabilityAgents
	case protocol.ActionAgentStatuses, protocol.ActionAgentEvent:
		required = protocol.CapabilityAgentStatus
	case protocol.ActionRemoveWorkspace:
		required = protocol.CapabilityRemoveWorkspace
	}
	if required == "" || protocol.HasCapability(requestCapabilities(request), required) {
		return nil
	}
	return fmt.Errorf("action %q requires capability %q; %s", request.Action, required, protocol.Remedy)
}

func requestCapabilities(request protocol.Request) []string {
	supported := protocol.CapabilitiesForVersion(request.Version)
	if request.MinVersion == 0 && len(request.Capabilities) == 0 {
		return supported
	}
	result := make([]string, 0, len(supported))
	for _, capability := range supported {
		if protocol.HasCapability(request.Capabilities, capability) {
			result = append(result, capability)
		}
	}
	return result
}

// reply is for requests without a selected revision, while replyFor echoes the
// negotiated one. Both bound the write so a client that stops reading cannot
// hold a daemon goroutine and file descriptor indefinitely.
func reply(connection net.Conn, response protocol.Response) error {
	response.Version = protocol.Version
	return writeResponse(connection, response)
}

func replyFor(connection net.Conn, request protocol.Request, response protocol.Response) error {
	response.Version = request.Version
	return writeResponse(connection, response)
}

func writeResponse(connection net.Conn, response protocol.Response) error {
	if err := connection.SetWriteDeadline(time.Now().Add(requestTimeout)); err != nil {
		return err
	}
	defer connection.SetWriteDeadline(time.Time{})
	return protocol.Write(connection, response)
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
	finish, ok := s.beginRequest(request.Action)
	if !ok {
		return protocol.Response{Error: "daemon is shutting down"}
	}
	defer finish()

	switch request.Action {
	case protocol.ActionPing:
		return protocol.Response{
			MinVersion:   protocol.MinimumVersion,
			MaxVersion:   protocol.Version,
			Capabilities: protocol.CapabilitiesForVersion(protocol.Version),
		}
	case protocol.ActionSnapshot:
		return s.snapshotResponse()
	case protocol.ActionAgents:
		return protocol.Response{Agents: s.agents()}
	case protocol.ActionAgentStatuses:
		return protocol.Response{AgentStatuses: s.agentStatusesSnapshot()}
	case protocol.ActionAgentEvent:
		return s.recordAgentEvent(request.TabID, request.AgentEvent)
	case protocol.ActionAddRoot:
		return s.addRoot(request.Path)
	case protocol.ActionRemoveRoot:
		return s.removeRoot(request.RootID)
	case protocol.ActionRemoveWorkspace:
		return s.removeWorkspace(request.RootID, request.Path)
	case protocol.ActionEnsureWorkspace:
		return s.ensureWorkspace(request.RootID, request.Path)
	case protocol.ActionCreateTab:
		return s.createTab(request)
	case protocol.ActionResize:
		return s.resize(request.TabID, request.ClientID, request.Columns, request.Rows)
	default:
		return protocol.Response{Error: fmt.Sprintf("unknown action %q", request.Action)}
	}
}

func (s *Server) beginRequest(action string) (func(), bool) {
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	if !s.accepting {
		return nil, false
	}
	switch action {
	case protocol.ActionCreateTab, protocol.ActionResize:
		s.mutations.Add(1)
		return s.mutations.Done, true
	default:
		return func() {}, true
	}
}

func (s *Server) beginMutation() (func(), bool) {
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	if !s.accepting {
		return nil, false
	}
	s.mutations.Add(1)
	return s.mutations.Done, true
}

func (s *Server) beginShutdown() {
	s.requestMu.Lock()
	if s.accepting {
		s.accepting = false
		s.mu.Lock()
		s.stopping = true
		s.mu.Unlock()
	}
	s.requestMu.Unlock()
	s.stopOnce.Do(func() { close(s.stop) })
}

func (s *Server) snapshotResponse() protocol.Response {
	snapshot := s.snapshot()
	return protocol.Response{Snapshot: &snapshot}
}

// removeRoot forgets a root and everything under it. Without it a root that
// became unreadable could only be dropped by editing the state file by hand.
// Its directory stays on disk, but its terminal sessions are terminated so
// they do not survive as processes the workspace tree can no longer reach.
func (s *Server) removeRoot(rootID string) protocol.Response {
	finish, ok := s.beginMutation()
	if !ok {
		return protocol.Response{Error: "daemon is shutting down"}
	}

	s.mu.Lock()
	index := -1
	for position, root := range s.value.Roots {
		if root.ID == rootID {
			index = position
			break
		}
	}
	if index < 0 {
		s.mu.Unlock()
		finish()
		return protocol.Response{Error: "root not found"}
	}

	previous := cloneState(s.value)
	s.value.Roots = append(s.value.Roots[:index], s.value.Roots[index+1:]...)
	workspaces := make([]model.Workspace, 0, len(s.value.Workspaces))
	orphaned := make(map[string]struct{})
	for _, workspace := range s.value.Workspaces {
		if workspace.RootID == rootID {
			orphaned[workspace.ID] = struct{}{}
			continue
		}
		workspaces = append(workspaces, workspace)
	}
	s.value.Workspaces = workspaces
	sessions := make([]*session, 0, len(orphaned))
	tabs := make([]model.Tab, 0, len(s.value.Tabs))
	for _, tab := range s.value.Tabs {
		if _, removed := orphaned[tab.WorkspaceID]; removed {
			if value, ok := s.sessions[tab.ID]; ok {
				sessions = append(sessions, value)
			}
			continue
		}
		tabs = append(tabs, tab)
	}
	s.value.Tabs = tabs
	if err := s.store.Save(s.value); err != nil {
		s.value = previous
		s.mu.Unlock()
		finish()
		return protocol.Response{Error: err.Error()}
	}
	s.revision++
	s.mu.Unlock()
	for _, value := range sessions {
		value.close()
	}
	finish()
	return s.snapshotResponse()
}

func (s *Server) removeWorkspace(rootID, path string) protocol.Response {
	finish, ok := s.beginMutation()
	if !ok {
		return protocol.Response{Error: "daemon is shutting down"}
	}
	defer finish()

	s.mu.Lock()
	root, ok := findRoot(s.value.Roots, rootID)
	s.mu.Unlock()
	if !ok {
		return protocol.Response{Error: "root not found"}
	}
	workspacePath, err := removableWorkspacePath(root, path)
	if err != nil {
		return protocol.Response{Error: err.Error()}
	}
	if err := os.RemoveAll(workspacePath); err != nil {
		return protocol.Response{Error: fmt.Sprintf("delete workspace: %v", err)}
	}

	s.mu.Lock()
	previous := cloneState(s.value)
	removedWorkspaceIDs := make(map[string]struct{})
	workspaces := make([]model.Workspace, 0, len(s.value.Workspaces))
	for _, workspace := range s.value.Workspaces {
		if workspace.RootID == rootID && workspace.Path == workspacePath {
			removedWorkspaceIDs[workspace.ID] = struct{}{}
			continue
		}
		workspaces = append(workspaces, workspace)
	}
	s.value.Workspaces = workspaces
	sessions := make([]*session, 0, len(removedWorkspaceIDs))
	tabs := make([]model.Tab, 0, len(s.value.Tabs))
	for _, tab := range s.value.Tabs {
		if _, removed := removedWorkspaceIDs[tab.WorkspaceID]; removed {
			if value, ok := s.sessions[tab.ID]; ok {
				sessions = append(sessions, value)
			}
			continue
		}
		tabs = append(tabs, tab)
	}
	s.value.Tabs = tabs
	if len(removedWorkspaceIDs) > 0 {
		if err := s.store.Save(s.value); err != nil {
			s.value = previous
			s.mu.Unlock()
			return protocol.Response{Error: fmt.Sprintf("workspace directory deleted but persist state: %v", err)}
		}
	}
	s.revision++
	s.mu.Unlock()
	for _, value := range sessions {
		value.close()
	}
	return s.snapshotResponse()
}

func removableWorkspacePath(root model.Root, path string) (string, error) {
	workspacePath := filepath.Clean(path)
	if path == "" || workspacePath != path || workspacePath == root.Path || filepath.Dir(workspacePath) != root.Path {
		return "", fmt.Errorf("workspace must be a direct child of its root")
	}
	canonicalRoot, err := canonicalDirectory(root.Path)
	if err != nil {
		return "", fmt.Errorf("inspect workspace root: %w", err)
	}
	if canonicalRoot != root.Path {
		return "", fmt.Errorf("workspace root changed since it was registered")
	}
	info, err := os.Lstat(workspacePath)
	if err != nil {
		return "", fmt.Errorf("inspect workspace: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace path is not a directory")
	}
	return workspacePath, nil
}

func (s *Server) addRoot(path string) protocol.Response {
	canonical, err := resolveDirectory(path)
	if err != nil {
		return protocol.Response{Error: err.Error()}
	}
	finish, ok := s.beginMutation()
	if !ok {
		return protocol.Response{Error: "daemon is shutting down"}
	}

	s.mu.Lock()
	for _, root := range s.value.Roots {
		if root.Path == canonical {
			s.mu.Unlock()
			finish()
			return s.snapshotResponse()
		}
	}
	root := model.Root{ID: newID(), Name: filepath.Base(canonical), Path: canonical}
	s.value.Roots = append(s.value.Roots, root)
	if err := s.store.Save(s.value); err != nil {
		s.value.Roots = s.value.Roots[:len(s.value.Roots)-1]
		s.mu.Unlock()
		finish()
		return protocol.Response{Error: err.Error()}
	}
	s.revision++
	s.mu.Unlock()
	finish()
	return s.snapshotResponse()
}

func (s *Server) ensureWorkspace(rootID, path string) protocol.Response {
	s.mu.Lock()
	if _, ok := findRoot(s.value.Roots, rootID); !ok {
		s.mu.Unlock()
		return protocol.Response{Error: "root not found"}
	}
	for _, workspace := range s.value.Workspaces {
		if workspace.RootID == rootID && workspace.Path == path && workspaceHasTabs(s.value.Tabs, workspace.ID) {
			copy := workspace
			s.mu.Unlock()
			return protocol.Response{Workspace: &copy}
		}
	}
	s.mu.Unlock()

	canonical, err := resolveDirectory(path)
	if err != nil {
		return protocol.Response{Error: err.Error()}
	}
	finish, ok := s.beginMutation()
	if !ok {
		return protocol.Response{Error: "daemon is shutting down"}
	}
	defer finish()

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
	s.revision++
	return protocol.Response{Workspace: &workspace}
}

func (s *Server) createTab(request protocol.Request) protocol.Response {
	workspaceID := request.WorkspaceID
	shell := s.shell
	if request.Shell != "" {
		shell = request.Shell
	}

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

	// The shell is started, registered and persisted without releasing the
	// lock. startSession begins waiting on the process immediately, so a shell
	// that exits at once calls sessionExited straight away; holding the lock
	// makes that wait until the tab exists, rather than removing a tab that is
	// not there yet and leaving a dead one behind afterwards.
	value, err := startSession(tab.ID, workspace.Path, shell, request.Environment,
		request.Columns, request.Rows, func() {
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
	s.revision++
	s.mu.Unlock()
	return protocol.Response{Tab: &tab}
}

func (s *Server) resize(tabID, clientID string, columns, rows uint16) protocol.Response {
	s.mu.Lock()
	value, ok := s.sessions[tabID]
	s.mu.Unlock()
	if !ok {
		return protocol.Response{Error: "running terminal session not found"}
	}
	if err := value.resizeFor(clientID, columns, rows); err != nil {
		return protocol.Response{Error: err.Error()}
	}
	return protocol.Response{}
}

func (s *Server) handleAttach(connection net.Conn, request protocol.Request) {
	s.mu.Lock()
	value, ok := s.sessions[request.TabID]
	s.mu.Unlock()
	if !ok {
		_ = replyFor(connection, request, protocol.Response{Error: "running terminal session not found"})
		return
	}
	if !protocol.HasCapability(requestCapabilities(request), protocol.CapabilityReplayBoundary) {
		if err := replyFor(connection, request, protocol.Response{}); err != nil {
			return
		}
		if err := value.attachClientReady(connection, request.ClientID, func(int, uint16, uint16) error { return nil }); err != nil {
			s.logger.Printf("attach to tab %s ended: %v", request.TabID, err)
		}
		return
	}
	if err := value.attachClientReady(connection, request.ClientID, func(replayBytes int, columns, rows uint16) error {
		return replyFor(connection, request, protocol.Response{
			ReplayBytes: replayBytes, ReplayColumns: columns, ReplayRows: rows,
		})
	}); err != nil {
		s.logger.Printf("attach to tab %s ended: %v", request.TabID, err)
	}
}

func (s *Server) sessionExited(tabID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger.Printf("shell for tab %s exited", tabID)
	delete(s.sessions, tabID)
	delete(s.agentStatuses, tabID)
	removed := false
	for index := range s.value.Tabs {
		if s.value.Tabs[index].ID == tabID {
			s.value.Tabs = append(s.value.Tabs[:index], s.value.Tabs[index+1:]...)
			removed = true
			break
		}
	}
	if removed {
		s.revision++
	}
	if s.stopping {
		// These shells exited because the daemon killed them, and it is
		// leaving. Saving once per tab here writes a file the process may not
		// live long enough to finish — a temporary left in the user's romty
		// home for every shutdown — to record something the next daemon
		// discards before it listens, because every tab in the state file
		// names a shell that died with the last one.
		return
	}
	if err := s.store.Save(s.value); err != nil {
		// The tab is gone from memory either way; the state file will
		// disagree until the next successful save. Every other Save here
		// rolls back, but there is nothing to roll back to: the shell has
		// already exited.
		s.logger.Printf("persist state after tab %s exited: %v", tabID, err)
	}
}

// snapshot never fails: a root romty cannot read is reported as one unreadable
// root rather than taken out of the tree, because one unmounted volume used to
// fail every refresh, and with it every path that needs a snapshot.
func (s *Server) snapshot() model.Snapshot {
	s.mu.Lock()
	value := cloneState(s.value)
	revision := s.revision
	s.mu.Unlock()
	statuses := s.agentStatusesSnapshot()
	for index := range value.Tabs {
		status := statuses[value.Tabs[index].ID]
		value.Tabs[index].Agent = status.Agent
		value.Tabs[index].AgentPhase = status.Phase
	}

	result := model.Snapshot{Revision: revision, Roots: make([]model.RootView, 0, len(value.Roots))}
	for _, root := range value.Roots {
		rootWorkspace, _ := workspaceAt(value.Workspaces, root.ID, root.Path)
		entries, err := readDirectory(root.Path)
		if err != nil {
			directories := appendMissingRunningWorkspaces(nil, value, root)
			result.Roots = append(result.Roots, model.RootView{
				Root:        root,
				Tabs:        tabsFor(value.Tabs, rootWorkspace.ID),
				Error:       err.Error(),
				Directories: directories,
			})
			continue
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
		directories = appendMissingRunningWorkspaces(directories, value, root)
		sort.Slice(directories, func(i, j int) bool {
			return directories[i].Workspace.Name < directories[j].Workspace.Name
		})
		result.Roots = append(result.Roots, model.RootView{
			Root:        root,
			Tabs:        tabsFor(value.Tabs, rootWorkspace.ID),
			Directories: directories,
		})
	}
	return result
}

func appendMissingRunningWorkspaces(directories []model.WorkspaceView, value model.State, root model.Root) []model.WorkspaceView {
	seen := make(map[string]struct{}, len(directories))
	for _, directory := range directories {
		seen[directory.Workspace.Path] = struct{}{}
	}
	for _, workspace := range value.Workspaces {
		if workspace.RootID != root.ID || workspace.Path == root.Path {
			continue
		}
		if _, ok := seen[workspace.Path]; ok {
			continue
		}
		tabs := tabsFor(value.Tabs, workspace.ID)
		if len(tabs) == 0 {
			continue
		}
		directories = append(directories, model.WorkspaceView{Workspace: workspace, Tabs: tabs})
		seen[workspace.Path] = struct{}{}
	}
	return directories
}

func (s *Server) agents() map[string]model.Agent {
	statuses := s.agentStatusesSnapshot()
	result := make(map[string]model.Agent, len(statuses))
	for tabID, status := range statuses {
		result[tabID] = status.Agent
	}
	return result
}

func (s *Server) agentStatusesSnapshot() map[string]model.AgentStatus {
	s.mu.Lock()
	sessions := make(map[string]*session, len(s.sessions))
	for tabID, session := range s.sessions {
		sessions[tabID] = session
	}
	reported := make(map[string]model.Agent, len(s.agentStatuses))
	for tabID, runtime := range s.agentStatuses {
		reported[tabID] = runtime.Agent
	}
	s.mu.Unlock()

	agents := sessionAgents(sessions)
	inferred := inferPhases(sessions, agents, reported)

	result := make(map[string]model.AgentStatus, len(agents))
	s.mu.Lock()
	defer s.mu.Unlock()
	for tabID, agent := range agents {
		if _, ok := s.sessions[tabID]; !ok {
			continue
		}
		status := model.AgentStatus{Agent: agent, Phase: model.AgentPhaseUnknown}
		switch runtime, ok := s.agentStatuses[tabID]; {
		case ok && runtime.Agent == agent:
			status.Phase = runtime.Phase
		default:
			if phase, ok := inferred[tabID]; ok {
				status.Phase = phase
			}
		}
		result[tabID] = status
	}
	return result
}

// inferPhases reads a phase back from what each agent drew, for the tabs no
// hook has reported. A hook is the agent's own account of itself and always
// wins, so the guess is neither made nor paid for where one has spoken.
func inferPhases(
	sessions map[string]*session,
	agents map[string]model.Agent,
	reported map[string]model.Agent,
) map[string]model.AgentPhase {
	phases := make(map[string]model.AgentPhase)
	for tabID, agent := range agents {
		if reported[tabID] == agent {
			continue
		}
		value, ok := sessions[tabID]
		if !ok {
			continue
		}
		output, title := value.recentOutput(phaseHintBytes)
		if phase, ok := inferAgentPhase(output, title); ok {
			phases[tabID] = phase
		}
	}
	return phases
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

func workspaceHasTabs(values []model.Tab, workspaceID string) bool {
	for _, value := range values {
		if value.WorkspaceID == workspaceID {
			return true
		}
	}
	return false
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
