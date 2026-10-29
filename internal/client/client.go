// Package client speaks the daemon's protocol over its unix socket. It
// negotiates a revision once per client, refuses a socket that is not the
// daemon's, and starts a daemon when nothing is listening on one.
package client

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/protocol"
)

// dialTimeout bounds reaching the socket, and handshakeTimeout an ordinary
// request and its reply. Neither bounds the attach that may follow, which is a
// terminal a user leaves open. Removing a large workspace gets its own bound.
// handshakeTimeout is a variable so tests need not wait it out, the way the
// daemon's request timeout is one.
const (
	dialTimeout             = 2 * time.Second
	shutdownTimeout         = 10 * time.Second
	workspaceRemovalTimeout = 10 * time.Minute
)

var (
	handshakeTimeout  = 3 * time.Second
	replayReadTimeout = 2 * time.Second
)

type Client struct {
	socket     string
	id         string
	protocolMu sync.Mutex
	protocol   *negotiatedProtocol
}

var nextClientID atomic.Uint64

type negotiatedProtocol struct {
	selectedVersion int
	capabilities    []string
}

type terminalStream struct {
	net.Conn
	reader        *bufio.Reader
	replayColumns uint16
	replayRows    uint16
}

func (s *terminalStream) Read(data []byte) (int, error) {
	return s.reader.Read(data)
}

func (s *terminalStream) ReplaySize() (uint16, uint16) {
	return s.replayColumns, s.replayRows
}

var _ io.ReadWriteCloser = (*terminalStream)(nil)

// New returns a client for the daemon listening on socket. It does not
// connect until the first call.
func New(socket string) *Client {
	return &Client{socket: socket, id: fmt.Sprintf("%d-%d", os.Getpid(), nextClientID.Add(1))}
}

// callFor sends a request and returns the one field of the reply the action
// asked for. A reply without that field is an error that names the daemon,
// not a nil value for the caller to dereference.
func callFor[T any](c *Client, request protocol.Request, field func(protocol.Response) *T, noun string) (T, error) {
	var value T
	response, err := c.call(request)
	if err != nil {
		return value, err
	}
	carried := field(response)
	if carried == nil {
		return value, fmt.Errorf("daemon returned no %s", noun)
	}
	return *carried, nil
}

func snapshotOf(response protocol.Response) *model.Snapshot { return response.Snapshot }

// snapshotCall sends a request whose reply is the workspace tree.
func (c *Client) snapshotCall(request protocol.Request) (model.Snapshot, error) {
	return callFor(c, request, snapshotOf, "snapshot")
}

func workspaceOf(response protocol.Response) *model.Workspace { return response.Workspace }

func tabOf(response protocol.Response) *model.Tab { return response.Tab }

// Ping reports whether a daemon answers on the socket.
func (c *Client) Ping() error {
	_, err := c.probeProtocol()
	return err
}

// ProtocolVersion reports the newest protocol the running daemon supports.
func (c *Client) ProtocolVersion() (int, error) {
	response, err := c.probeProtocol()
	if err != nil {
		return 0, err
	}
	return advertisedMaximum(response), nil
}

// Snapshot returns the workspace tree with every tab and its agent status.
func (c *Client) Snapshot() (model.Snapshot, error) {
	return c.snapshotCall(protocol.Request{Action: protocol.ActionSnapshot})
}

// Agents returns the agent detected in each tab, keyed by tab ID.
func (c *Client) Agents() (map[string]model.Agent, error) {
	supported, err := c.supports(protocol.CapabilityAgents)
	if err != nil {
		return nil, err
	}
	if !supported {
		return map[string]model.Agent{}, nil
	}
	response, err := c.call(protocol.Request{Action: protocol.ActionAgents})
	if err != nil {
		return nil, err
	}
	return response.Agents, nil
}

// AgentStatuses returns each tab's agent and phase, keyed by tab ID. A daemon
// without phase reporting answers with the agent and an unknown phase.
func (c *Client) AgentStatuses() (map[string]model.AgentStatus, error) {
	supported, err := c.supports(protocol.CapabilityAgentStatus)
	if err != nil {
		return nil, err
	}
	if !supported {
		agents, err := c.Agents()
		if err != nil {
			return nil, err
		}
		statuses := make(map[string]model.AgentStatus, len(agents))
		for tabID, agent := range agents {
			statuses[tabID] = model.AgentStatus{Agent: agent, Phase: model.AgentPhaseUnknown}
		}
		return statuses, nil
	}
	response, err := c.call(protocol.Request{Action: protocol.ActionAgentStatuses})
	if err != nil {
		return nil, err
	}
	return response.AgentStatuses, nil
}

// ReportAgentEvent sends one hook event. A daemon without phase reporting
// ignores it.
func (c *Client) ReportAgentEvent(tabID string, event protocol.AgentEvent) error {
	supported, err := c.supports(protocol.CapabilityAgentStatus)
	if err != nil || !supported {
		return err
	}
	_, err = c.call(protocol.Request{
		Action:     protocol.ActionAgentEvent,
		TabID:      tabID,
		AgentEvent: &event,
	})
	return err
}

// AddRoot adds the directory at path as a root. A relative path is resolved
// against the working directory, and ~ against the home directory.
func (c *Client) AddRoot(path string) (model.Snapshot, error) {
	normalized, err := normalizePath(path)
	if err != nil {
		return model.Snapshot{}, err
	}
	return c.snapshotCall(protocol.Request{Action: protocol.ActionAddRoot, Path: normalized})
}

// normalizePath makes a root path absolute, expanding a leading ~ to the
// user's home directory.
func normalizePath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path[1:], "/"))
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve root path: %w", err)
	}
	return filepath.Clean(absolute), nil
}

// RemoveRoot forgets a root and closes its terminals. Its directory stays.
func (c *Client) RemoveRoot(rootID string) (model.Snapshot, error) {
	return c.snapshotCall(protocol.Request{Action: protocol.ActionRemoveRoot, RootID: rootID})
}

// RemoveWorkspace deletes a workspace directory and closes its terminals.
func (c *Client) RemoveWorkspace(rootID, path string) (model.Snapshot, error) {
	if err := c.require(protocol.CapabilityRemoveWorkspace, "removing workspaces"); err != nil {
		return model.Snapshot{}, err
	}
	return c.snapshotCall(protocol.Request{
		Action: protocol.ActionRemoveWorkspace,
		RootID: rootID,
		Path:   path,
	})
}

// EnsureWorkspace returns the workspace at path under a root, creating its
// record on first use.
func (c *Client) EnsureWorkspace(rootID, path string) (model.Workspace, error) {
	return callFor(c, protocol.Request{
		Action: protocol.ActionEnsureWorkspace,
		RootID: rootID,
		Path:   path,
	}, workspaceOf, "workspace")
}

// CreateTab starts a shell in a workspace at the given size.
func (c *Client) CreateTab(workspaceID string, columns, rows uint16) (model.Tab, error) {
	// The daemon may predate this shell by days, so the environment and the
	// shell to run come from here rather than from whatever the daemon
	// inherited when it started.
	return callFor(c, protocol.Request{
		Action:      protocol.ActionCreateTab,
		WorkspaceID: workspaceID,
		Columns:     columns,
		Rows:        rows,
		Environment: c.environment(),
		Shell:       os.Getenv("SHELL"),
	}, tabOf, "tab")
}

// ReopenTab starts a tab from one of the workspace's closed sessions.
func (c *Client) ReopenTab(workspaceID, closedID string, columns, rows uint16) (model.Tab, error) {
	if err := c.require(protocol.CapabilityReopenTab, "reopening closed sessions"); err != nil {
		return model.Tab{}, err
	}
	return callFor(c, protocol.Request{
		Action:      protocol.ActionReopenTab,
		WorkspaceID: workspaceID,
		TabID:       closedID,
		Columns:     columns,
		Rows:        rows,
		Environment: c.environment(),
		Shell:       os.Getenv("SHELL"),
	}, tabOf, "tab")
}

// CloseTab terminates a tab's shell and removes the tab.
func (c *Client) CloseTab(tabID string) (model.Snapshot, error) {
	if err := c.require(protocol.CapabilityCloseTab, "closing tabs"); err != nil {
		return model.Snapshot{}, err
	}
	return c.snapshotCall(protocol.Request{Action: protocol.ActionCloseTab, TabID: tabID})
}

// RenameTab changes a tab's display name.
func (c *Client) RenameTab(tabID, name string) (model.Snapshot, error) {
	if err := c.require(protocol.CapabilityRenameTab, "renaming tabs"); err != nil {
		return model.Snapshot{}, err
	}
	return c.snapshotCall(protocol.Request{Action: protocol.ActionRenameTab, TabID: tabID, Name: name})
}

// MoveTab moves a tab offset places among its workspace's tabs.
func (c *Client) MoveTab(tabID string, offset int) (model.Snapshot, error) {
	if err := c.require(protocol.CapabilityMoveTab, "moving tabs"); err != nil {
		return model.Snapshot{}, err
	}
	return c.snapshotCall(protocol.Request{Action: protocol.ActionMoveTab, TabID: tabID, Offset: offset})
}

// DaemonInfo is what the running daemon says about itself.
type DaemonInfo struct {
	// Version is the daemon's build, or empty for a daemon that predates
	// reporting it, which is older than every client that asks.
	Version        string
	RecordingBytes int64
}

// DaemonInfo asks the running daemon for its build and memory use.
func (c *Client) DaemonInfo() (DaemonInfo, error) {
	response, err := c.probeProtocol()
	if err != nil {
		return DaemonInfo{}, err
	}
	return DaemonInfo{Version: response.DaemonVersion, RecordingBytes: response.RecordingBytes}, nil
}

// Resize reports this client's terminal size for a tab. The daemon applies
// it when this client is the one the user last typed into.
func (c *Client) Resize(tabID string, columns, rows uint16) error {
	_, err := c.call(protocol.Request{
		Action:   protocol.ActionResize,
		TabID:    tabID,
		ClientID: c.id,
		Columns:  columns,
		Rows:     rows,
	})
	return err
}

// Shutdown stops the daemon and waits until it has released its socket and
// lock.
func (c *Client) Shutdown() error {
	response, probeErr := c.probeProtocol()
	version := protocol.Version
	if probeErr == nil {
		version = advertisedMaximum(response)
	}
	_, err := c.callWithProtocol(protocol.Request{Action: protocol.ActionShutdown}, version, nil)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(shutdownTimeout)
	for time.Now().Before(deadline) {
		stopped, err := daemonStopped(c.socket)
		if err != nil {
			return err
		}
		if stopped {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not finish stopping")
}

func daemonStopped(socket string) (bool, error) {
	if _, err := os.Lstat(socket); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("inspect daemon socket after shutdown: %w", err)
	}
	held, err := daemonLockHeld(socket)
	return !held, err
}

// daemonLockHeld distinguishes an absent daemon from one whose socket was
// unlinked. Never create or replace the lock: a second inode would bypass the
// running daemon's ownership of its sessions and state.
func daemonLockHeld(socket string) (bool, error) {
	fd, err := syscall.Open(socket+".lock", syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open daemon lock: %w", err)
	}
	lock := os.NewFile(uintptr(fd), socket+".lock")
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil {
		return false, fmt.Errorf("inspect daemon lock: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return false, fmt.Errorf("daemon lock must be a regular file owned only by the current user")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return true, nil
		}
		return false, fmt.Errorf("check daemon lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		return false, fmt.Errorf("release daemon lock after check: %w", err)
	}
	return false, nil
}

// Unavailable reports whether err means the daemon socket could not be reached,
// which is what a request returns when no daemon is running.
func Unavailable(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

// OpenAttach hands back the attach stream without interpreting the replay
// boundary, so the recorded history and the live output arrive as one stream.
// Tests that read the replay as it comes off the socket use it. The TUI uses
// OpenTerminal, because a terminal has to restore its history before it is
// put on screen.
func (c *Client) OpenAttach(tabID string) (net.Conn, *bufio.Reader, error) {
	connection, reader, _, err := c.openAttach(tabID)
	return connection, reader, err
}

func (c *Client) openAttach(tabID string) (net.Conn, *bufio.Reader, protocol.Response, error) {
	negotiated, err := c.negotiateProtocol()
	if err != nil {
		return nil, nil, protocol.Response{}, err
	}
	if err := validateDaemonSocket(c.socket); err != nil {
		return nil, nil, protocol.Response{}, err
	}
	connection, err := net.DialTimeout("unix", c.socket, dialTimeout)
	if err != nil {
		return nil, nil, protocol.Response{}, fmt.Errorf("connect to daemon: %w", err)
	}
	// Bound the handshake alone. A daemon that accepts the connection and then
	// says nothing — stopped, wedged, or another program holding the socket
	// path — must fail the attach with an error, not block it forever.
	if err := connection.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		connection.Close()
		return nil, nil, protocol.Response{}, fmt.Errorf("set daemon deadline: %w", err)
	}
	if err := sendRequest(connection, protocol.Request{
		Action: protocol.ActionAttach, TabID: tabID, ClientID: c.id,
	},
		negotiated.selectedVersion, negotiated.capabilities); err != nil {
		connection.Close()
		return nil, nil, protocol.Response{}, err
	}

	reader := bufio.NewReader(connection)
	var response protocol.Response
	if err := protocol.Read(reader, &response); err != nil {
		connection.Close()
		return nil, nil, protocol.Response{}, err
	}
	// The response must stay on the revision chosen before raw terminal bytes
	// begin; once the stream starts there is no framed reply left to correct it.
	if err := checkResponse(protocol.ActionAttach, response, negotiated.selectedVersion); err != nil {
		connection.Close()
		return nil, nil, protocol.Response{}, err
	}
	// The handshake is done; what follows is a terminal that stays open for as
	// long as the user keeps it, and wants no deadline at all.
	if err := connection.SetDeadline(time.Time{}); err != nil {
		connection.Close()
		return nil, nil, protocol.Response{}, fmt.Errorf("clear daemon deadline: %w", err)
	}
	return connection, reader, response, nil
}

// OpenTerminal attaches to a tab and returns the live stream and the recorded
// history to restore before it.
func (c *Client) OpenTerminal(tabID string) (io.ReadWriteCloser, []byte, error) {
	connection, reader, response, err := c.openAttach(tabID)
	if err != nil {
		return nil, nil, err
	}
	replayBytes := response.ReplayBytes
	if replayBytes < 0 || replayBytes > protocol.MaxReplayBytes {
		connection.Close()
		return nil, nil, fmt.Errorf("terminal replay size %d is outside 0..%d", replayBytes, protocol.MaxReplayBytes)
	}
	replay := make([]byte, replayBytes)
	if err := readReplay(connection, reader, replay); err != nil {
		connection.Close()
		return nil, nil, fmt.Errorf("restore terminal history: %w", err)
	}
	return &terminalStream{
		Conn: connection, reader: reader,
		replayColumns: response.ReplayColumns, replayRows: response.ReplayRows,
	}, replay, nil
}

func readReplay(connection net.Conn, reader io.Reader, replay []byte) error {
	for len(replay) > 0 {
		if err := connection.SetReadDeadline(time.Now().Add(replayReadTimeout)); err != nil {
			return err
		}
		count, err := reader.Read(replay)
		replay = replay[count:]
		// A reader may hand over the last of the replay together with the
		// error that ends the stream. The history is complete either way, and
		// refusing it here would turn a restored terminal into an attach
		// failure and the reattach backoff that follows one.
		if len(replay) == 0 {
			break
		}
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrNoProgress
		}
	}
	return connection.SetReadDeadline(time.Time{})
}

func sendRequest(w io.Writer, request protocol.Request, version int, capabilities []string) error {
	request.Version = version
	request.MinVersion = protocol.MinimumVersion
	request.Capabilities = capabilities
	return protocol.Write(w, request)
}

func (c *Client) call(request protocol.Request) (protocol.Response, error) {
	negotiated, err := c.negotiateProtocol()
	if err != nil {
		return protocol.Response{}, err
	}
	return c.callWithProtocol(request, negotiated.selectedVersion, negotiated.capabilities)
}

func (c *Client) callWithProtocol(request protocol.Request, version int, capabilities []string) (protocol.Response, error) {
	response, err := c.callRaw(request, version, capabilities)
	if err != nil {
		return protocol.Response{}, err
	}
	if err := checkResponse(request.Action, response, version); err != nil {
		return protocol.Response{}, err
	}
	return response, nil
}

func (c *Client) callRaw(request protocol.Request, version int, capabilities []string) (protocol.Response, error) {
	if err := validateDaemonSocket(c.socket); err != nil {
		return protocol.Response{}, err
	}
	connection, err := net.DialTimeout("unix", c.socket, dialTimeout)
	if err != nil {
		return protocol.Response{}, fmt.Errorf("connect to daemon: %w", err)
	}
	defer connection.Close()
	timeout := handshakeTimeout
	if request.Action == protocol.ActionRemoveWorkspace {
		timeout = workspaceRemovalTimeout
	}
	if err := connection.SetDeadline(time.Now().Add(timeout)); err != nil {
		return protocol.Response{}, fmt.Errorf("set daemon deadline: %w", err)
	}
	if err := sendRequest(connection, request, version, capabilities); err != nil {
		return protocol.Response{}, err
	}

	var response protocol.Response
	if err := protocol.Read(bufio.NewReader(connection), &response); err != nil {
		return protocol.Response{}, err
	}
	return response, nil
}

func (c *Client) negotiateProtocol() (*negotiatedProtocol, error) {
	c.protocolMu.Lock()
	defer c.protocolMu.Unlock()
	if c.protocol != nil {
		return c.protocol, nil
	}
	response, err := c.probeProtocol()
	if err != nil {
		return nil, err
	}
	daemonMax := advertisedMaximum(response)
	daemonMin := response.MinVersion
	if daemonMin == 0 {
		daemonMin = daemonMax
	}
	selected, ok := protocol.SelectVersion(
		protocol.MinimumVersion, protocol.Version, daemonMin, daemonMax,
	)
	if !ok {
		return nil, fmt.Errorf("romty supports protocol %d..%d but the running daemon supports %d..%d; %s",
			protocol.MinimumVersion, protocol.Version, daemonMin, daemonMax, protocol.Remedy)
	}
	advertised := response.Capabilities
	if response.MinVersion == 0 && len(advertised) == 0 {
		advertised = protocol.CapabilitiesForVersion(daemonMax)
	}
	capabilities := protocol.IntersectCapabilities(protocol.CapabilitiesForVersion(selected), advertised)
	c.protocol = &negotiatedProtocol{
		selectedVersion: selected,
		capabilities:    capabilities,
	}
	return c.protocol, nil
}

func advertisedMaximum(response protocol.Response) int {
	if response.MaxVersion != 0 {
		return response.MaxVersion
	}
	return response.Version
}

func (c *Client) probeProtocol() (protocol.Response, error) {
	request := protocol.Request{
		Action:       protocol.ActionPing,
		MinVersion:   protocol.MinimumVersion,
		Capabilities: protocol.CapabilitiesForVersion(protocol.Version),
	}
	response, err := c.callRaw(request, protocol.Version, request.Capabilities)
	if err != nil {
		return protocol.Response{}, err
	}
	// A peer that refuses the ping without naming a version of its own is not
	// one this client can negotiate with — a program holding the socket, or a
	// reply too damaged to read — and what it said is the only clue there is.
	// A refusal that does carry a version is an ordinary mismatch, which
	// negotiation reports in the sentence that also names the remedy.
	if response.Error != "" && advertisedMaximum(response) == 0 {
		return protocol.Response{}, &protocol.RefusedError{Message: response.Error}
	}
	return response, nil
}

func (c *Client) supports(capability string) (bool, error) {
	negotiated, err := c.negotiateProtocol()
	if err != nil {
		return false, err
	}
	return protocol.HasCapability(negotiated.capabilities, capability), nil
}

// require refuses an action the running daemon has no capability for. what
// names the action in the message, such as "closing tabs".
func (c *Client) require(capability, what string) error {
	supported, err := c.supports(capability)
	if err != nil {
		return err
	}
	if !supported {
		return fmt.Errorf("%s requires a newer daemon; %s", what, protocol.Remedy)
	}
	return nil
}

func validateDaemonSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect daemon socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("daemon socket path is not a unix socket")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("daemon socket must be owned by the current user")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("daemon socket must not be accessible by group or other users")
	}
	return nil
}

// checkResponse keeps an ordinary response on the revision negotiation chose.
// Ping advertises a range and shutdown must remain reachable without overlap,
// so neither is tied to the request's selected version.
func checkResponse(action string, response protocol.Response, expectedVersion int) error {
	if response.Version != expectedVersion && !protocol.VersionExempt(action) {
		return fmt.Errorf("romty selected protocol %d but the running daemon answered with %d; %s",
			expectedVersion, response.Version, protocol.Remedy)
	}
	if response.Error != "" {
		return &protocol.RefusedError{Message: response.Error}
	}
	return nil
}
