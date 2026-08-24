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

	"github.com/nalbam/romty/internal/model"
	"github.com/nalbam/romty/internal/protocol"
)

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
	return err
}

// Unavailable reports whether err means the daemon socket could not be reached,
// which is what a request returns when no daemon is running.
func Unavailable(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

func (c *Client) OpenAttach(tabID string) (net.Conn, *bufio.Reader, error) {
	connection, err := net.DialTimeout("unix", c.socket, 2*time.Second)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to daemon: %w", err)
	}
	if err := protocol.Write(connection, protocol.Request{Action: protocol.ActionAttach, TabID: tabID}); err != nil {
		connection.Close()
		return nil, nil, err
	}

	reader := bufio.NewReader(connection)
	var response protocol.Response
	if err := protocol.Read(reader, &response); err != nil {
		connection.Close()
		return nil, nil, err
	}
	if response.Error != "" {
		connection.Close()
		return nil, nil, fmt.Errorf("daemon: %s", response.Error)
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

// outdatedDaemon explains a mismatch in the terms a user can act on, rather
// than leaving them with a puzzling error from a daemon that predates the
// request they just made.
func outdatedDaemon(daemonVersion int) error {
	return fmt.Errorf("this romty speaks protocol %d but the running daemon speaks %d; run `romty stop` and start romty again",
		protocol.Version, daemonVersion)
}

func (c *Client) call(request protocol.Request) (protocol.Response, error) {
	connection, err := net.DialTimeout("unix", c.socket, 2*time.Second)
	if err != nil {
		return protocol.Response{}, fmt.Errorf("connect to daemon: %w", err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return protocol.Response{}, fmt.Errorf("set daemon deadline: %w", err)
	}
	request.Version = protocol.Version
	if err := protocol.Write(connection, request); err != nil {
		return protocol.Response{}, err
	}

	var response protocol.Response
	if err := protocol.Read(bufio.NewReader(connection), &response); err != nil {
		return protocol.Response{}, err
	}
	if response.Version != protocol.Version {
		return protocol.Response{}, outdatedDaemon(response.Version)
	}
	if response.Error != "" {
		return protocol.Response{}, fmt.Errorf("daemon: %s", response.Error)
	}
	return response, nil
}
