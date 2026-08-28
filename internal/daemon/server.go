// The daemon itself: its lifetime, the sessions it holds, and the agent status
// it reports for them.

// Package daemon keeps romty's shell sessions alive between TUIs. One
// daemon owns a private unix socket, the workspace tree behind it, and a
// PTY per terminal tab, and it outlives every client that attaches to it.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/protocol"
	"github.com/opspresso/romty/internal/state"
	"github.com/opspresso/romty/internal/usage"
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

	usage *usage.Reader
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
		usage:         usage.NewReader(),
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

func (s *Server) shutdown() {
	s.mu.Lock()
	s.stopping = true
	sessions := make([]*session, 0, len(s.sessions))
	for _, value := range s.sessions {
		sessions = append(sessions, value)
	}
	s.mu.Unlock()
	closeSessions(sessions)
	_ = os.Remove(s.socket)
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

func (s *Server) closeTab(tabID string) protocol.Response {
	if tabID == "" {
		return protocol.Response{Error: "tab is required"}
	}
	s.mu.Lock()
	value, ok := s.sessions[tabID]
	s.mu.Unlock()
	if !ok {
		return protocol.Response{Error: "running terminal session not found"}
	}
	value.close()
	return s.snapshotResponse()
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
	reported := make(map[string]agentRuntime, len(s.agentStatuses))
	for tabID, runtime := range s.agentStatuses {
		reported[tabID] = runtime
	}
	workspaces := s.tabWorkspaces()
	s.mu.Unlock()

	agents := sessionAgents(sessions)
	inferred := inferPhases(sessions, agents, reported)
	ledgers := s.sessionUsage(reported, workspaces)

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
			if ledger, ok := ledgers[tabID]; ok {
				status.ContextTokens, status.CostUSD = ledger.ContextTokens, ledger.CostUSD
			}
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
	reported map[string]agentRuntime,
) map[string]model.AgentPhase {
	phases := make(map[string]model.AgentPhase)
	for tabID, agent := range agents {
		if reported[tabID].Agent == agent {
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
