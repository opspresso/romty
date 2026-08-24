package daemon

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
)

const maxHistoryBytes = 8 << 20

type session struct {
	id      string
	pty     *os.File
	command *exec.Cmd
	onExit  func()

	mu      sync.Mutex
	writeMu sync.Mutex
	history []byte
	clients map[net.Conn]struct{}
	closed  bool
}

func startSession(id, directory, shell string, columns, rows uint16, onExit func()) (*session, error) {
	if columns == 0 {
		columns = 80
	}
	if rows == 0 {
		rows = 24
	}

	command := exec.Command(shell)
	command.Dir = directory
	command.Env = append(os.Environ(), "TERM=xterm-256color", "ROMTY=1")
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Cols: columns, Rows: rows})
	if err != nil {
		return nil, fmt.Errorf("start shell PTY: %w", err)
	}

	value := &session{
		id:      id,
		pty:     terminal,
		command: command,
		onExit:  onExit,
		clients: make(map[net.Conn]struct{}),
	}
	go value.read()
	go value.wait()
	return value, nil
}

func (s *session) read() {
	buffer := make([]byte, 32*1024)
	for {
		count, err := s.pty.Read(buffer)
		if count > 0 {
			s.broadcast(buffer[:count])
		}
		if err != nil {
			return
		}
	}
}

func (s *session) wait() {
	_ = s.command.Wait()
	s.mu.Lock()
	s.closed = true
	clients := make([]net.Conn, 0, len(s.clients))
	for connection := range s.clients {
		clients = append(clients, connection)
	}
	s.clients = make(map[net.Conn]struct{})
	s.mu.Unlock()
	for _, connection := range clients {
		connection.Close()
	}
	s.onExit()
}

func (s *session) broadcast(data []byte) {
	s.mu.Lock()
	s.appendHistory(data)
	clients := make([]net.Conn, 0, len(s.clients))
	for connection := range s.clients {
		clients = append(clients, connection)
	}
	s.mu.Unlock()

	for _, connection := range clients {
		_ = connection.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
		if _, err := connection.Write(data); err != nil {
			s.detach(connection)
		}
		_ = connection.SetWriteDeadline(time.Time{})
	}
}

func (s *session) appendHistory(data []byte) {
	if len(data) >= maxHistoryBytes {
		s.history = append(s.history[:0], data[len(data)-maxHistoryBytes:]...)
		return
	}
	overflow := len(s.history) + len(data) - maxHistoryBytes
	if overflow > 0 {
		copy(s.history, s.history[overflow:])
		s.history = s.history[:len(s.history)-overflow]
	}
	s.history = append(s.history, data...)
}

func (s *session) attach(connection net.Conn) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("terminal session has exited")
	}
	_ = connection.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := connection.Write([]byte("\x1b[2J\x1b[H")); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("initialize attached terminal: %w", err)
	}
	if _, err := connection.Write(s.history); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("restore terminal history: %w", err)
	}
	s.clients[connection] = struct{}{}
	_ = connection.SetWriteDeadline(time.Time{})
	s.mu.Unlock()

	buffer := make([]byte, 32*1024)
	for {
		count, err := connection.Read(buffer)
		if count > 0 {
			if writeErr := s.write(buffer[:count]); writeErr != nil {
				s.detach(connection)
				return writeErr
			}
		}
		if err != nil {
			s.detach(connection)
			return nil
		}
	}
}

func (s *session) detach(connection net.Conn) {
	s.mu.Lock()
	delete(s.clients, connection)
	s.mu.Unlock()
	connection.Close()
}

func (s *session) write(data []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.pty.Write(data); err != nil {
		return fmt.Errorf("write terminal input: %w", err)
	}
	return nil
}

func (s *session) resize(columns, rows uint16) error {
	if columns == 0 || rows == 0 {
		return fmt.Errorf("terminal size must be greater than zero")
	}
	if err := pty.Setsize(s.pty, &pty.Winsize{Cols: columns, Rows: rows}); err != nil {
		return fmt.Errorf("resize terminal: %w", err)
	}
	return nil
}

func (s *session) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	_ = s.pty.Close()
	if s.command.Process != nil {
		_ = s.command.Process.Kill()
	}
}
