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

var handshakeTimeout = 3 * time.Second

type Client struct {
	socket string
}

type terminalStream struct {
	net.Conn
	reader *bufio.Reader
}

func (s *terminalStream) Read(data []byte) (int, error) {
	return s.reader.Read(data)
}

var _ io.ReadWriteCloser = (*terminalStream)(nil)

func New(socket string) *Client {
	return &Client{socket: socket}
}

func (c *Client) Ping() error {
	_, err := c.call(protocol.Request{Action: protocol.ActionPing})
	return err
}

func (c *Client) Snapshot() (model.Snapshot, error) {
	response, err := c.call(protocol.Request{Action: protocol.ActionSnapshot})
	if err != nil {
		return model.Snapshot{}, err
	}
	if response.Snapshot == nil {
		return model.Snapshot{}, fmt.Errorf("daemon returned no snapshot")
	}
	return *response.Snapshot, nil
}

func (c *Client) Agents() (map[string]model.Agent, error) {
	response, err := c.call(protocol.Request{Action: protocol.ActionAgents})
	if err != nil {
		return nil, err
	}
	return response.Agents, nil
}

func (c *Client) AddRoot(path string) (model.Snapshot, error) {
	normalized, err := normalizePath(path)
	if err != nil {
		return model.Snapshot{}, err
	}
	response, err := c.call(protocol.Request{Action: protocol.ActionAddRoot, Path: normalized})
	if err != nil {
		return model.Snapshot{}, err
	}
	if response.Snapshot == nil {
		return model.Snapshot{}, fmt.Errorf("daemon returned no snapshot")
	}
	return *response.Snapshot, nil
}

func normalizePath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve root path: %w", err)
	}
	return filepath.Clean(absolute), nil
}

func (c *Client) RemoveRoot(rootID string) (model.Snapshot, error) {
	response, err := c.call(protocol.Request{Action: protocol.ActionRemoveRoot, RootID: rootID})
	if err != nil {
		return model.Snapshot{}, err
	}
	if response.Snapshot == nil {
		return model.Snapshot{}, fmt.Errorf("daemon returned no snapshot")
	}
	return *response.Snapshot, nil
}

func (c *Client) RemoveWorkspace(rootID, path string) (model.Snapshot, error) {
	response, err := c.call(protocol.Request{
		Action: protocol.ActionRemoveWorkspace,
		RootID: rootID,
		Path:   path,
	})
	if err != nil {
		return model.Snapshot{}, err
	}
	if response.Snapshot == nil {
		return model.Snapshot{}, fmt.Errorf("daemon returned no snapshot")
	}
	return *response.Snapshot, nil
}

func (c *Client) EnsureWorkspace(rootID, path string) (model.Workspace, error) {
	response, err := c.call(protocol.Request{
		Action: protocol.ActionEnsureWorkspace,
		RootID: rootID,
		Path:   path,
	})
	if err != nil {
		return model.Workspace{}, err
	}
	if response.Workspace == nil {
		return model.Workspace{}, fmt.Errorf("daemon returned no workspace")
	}
	return *response.Workspace, nil
}

func (c *Client) CreateTab(workspaceID string, columns, rows uint16) (model.Tab, error) {
	// The daemon may predate this shell by days, so the environment and the
	// shell to run come from here rather than from whatever the daemon
	// inherited when it started.
	response, err := c.call(protocol.Request{
		Action:      protocol.ActionCreateTab,
		WorkspaceID: workspaceID,
		Columns:     columns,
		Rows:        rows,
		Environment: os.Environ(),
		Shell:       os.Getenv("SHELL"),
	})
	if err != nil {
		return model.Tab{}, err
	}
	if response.Tab == nil {
		return model.Tab{}, fmt.Errorf("daemon returned no tab")
	}
	return *response.Tab, nil
}

func (c *Client) Resize(tabID string, columns, rows uint16) error {
	_, err := c.call(protocol.Request{
		Action:  protocol.ActionResize,
		TabID:   tabID,
		Columns: columns,
		Rows:    rows,
	})
	return err
}

func (c *Client) Shutdown() error {
	_, err := c.call(protocol.Request{Action: protocol.ActionShutdown})
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
	lock, err := os.OpenFile(socket+".lock", os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("open daemon lock after shutdown: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return false, nil
		}
		return false, fmt.Errorf("check daemon lock after shutdown: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		return false, fmt.Errorf("release daemon lock after shutdown check: %w", err)
	}
	return true, nil
}

// Unavailable reports whether err means the daemon socket could not be reached,
// which is what a request returns when no daemon is running.
func Unavailable(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

func (c *Client) OpenAttach(tabID string) (net.Conn, *bufio.Reader, error) {
	if err := validateDaemonSocket(c.socket); err != nil {
		return nil, nil, err
	}
	connection, err := net.DialTimeout("unix", c.socket, dialTimeout)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to daemon: %w", err)
	}
	// Bound the handshake alone. A daemon that accepts the connection and then
	// says nothing — stopped, wedged, or another program holding the socket
	// path — used to leave this read blocked for good, and with it the command
	// goroutine that opens a terminal: the tab simply never appeared, with
	// nothing on the status bar to say why.
	if err := connection.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		connection.Close()
		return nil, nil, fmt.Errorf("set daemon deadline: %w", err)
	}
	if err := sendRequest(connection, protocol.Request{Action: protocol.ActionAttach, TabID: tabID}); err != nil {
		connection.Close()
		return nil, nil, err
	}

	reader := bufio.NewReader(connection)
	var response protocol.Response
	if err := protocol.Read(reader, &response); err != nil {
		connection.Close()
		return nil, nil, err
	}
	// Attach used to skip the version check the short requests run, so a
	// mismatch here arrived as whatever the old daemon streamed next rather
	// than as the one message that says which side to restart.
	if err := checkResponse(protocol.ActionAttach, response); err != nil {
		connection.Close()
		return nil, nil, err
	}
	// The handshake is done; what follows is a terminal that stays open for as
	// long as the user keeps it, and wants no deadline at all.
	if err := connection.SetDeadline(time.Time{}); err != nil {
		connection.Close()
		return nil, nil, fmt.Errorf("clear daemon deadline: %w", err)
	}
	return connection, reader, nil
}

func (c *Client) OpenTerminal(tabID string) (io.ReadWriteCloser, error) {
	connection, reader, err := c.OpenAttach(tabID)
	if err != nil {
		return nil, err
	}
	return &terminalStream{Conn: connection, reader: reader}, nil
}

// sendRequest stamps the protocol version and writes the request. Both paths
// go through it because only one of them used to: OpenAttach built its request
// inline and sent it unversioned, so the daemon had no way to tell an attach
// from a current client apart from one that predates the field.
func sendRequest(w io.Writer, request protocol.Request) error {
	request.Version = protocol.Version
	return protocol.Write(w, request)
}

func (c *Client) call(request protocol.Request) (protocol.Response, error) {
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
	if err := sendRequest(connection, request); err != nil {
		return protocol.Response{}, err
	}

	var response protocol.Response
	if err := protocol.Read(bufio.NewReader(connection), &response); err != nil {
		return protocol.Response{}, err
	}
	if err := checkResponse(request.Action, response); err != nil {
		return protocol.Response{}, err
	}
	return response, nil
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

// checkResponse judges a reply before anything reads its fields. The version
// comes first: a daemon that speaks another protocol may well have an error to
// report, but the mismatch is what the user has to act on.
//
// Except for the actions protocol.VersionExempt names, which the daemon
// carries out whatever version asked. Judging their replies by the version
// undid that on this side: pinging a mismatched daemon reported it as no
// daemon at all, so romty started a second one, watched it lose the lock and
// exit, and gave up with "daemon did not become ready" — and `romty stop`, the
// remedy every mismatch names, stopped the daemon and then reported the
// mismatch as a failure to stop it.
func checkResponse(action string, response protocol.Response) error {
	if response.Version != protocol.Version && !protocol.VersionExempt(action) {
		return protocol.VersionMismatch("romty", protocol.Version, "running daemon", response.Version)
	}
	if response.Error != "" {
		return fmt.Errorf("daemon: %s", response.Error)
	}
	return nil
}
