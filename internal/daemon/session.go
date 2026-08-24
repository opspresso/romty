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

// maxHistoryBytes is a variable so tests can reach the trimming path without
// building an eight megabyte buffer.
var maxHistoryBytes = 8 << 20

// resetScreen puts the client's emulator at a known state before the
// recording is replayed onto it.
const resetScreen = "\x1b[2J\x1b[H"

// replayTimeout bounds how long a client being sent the recording may go
// without reading. It is a variable so tests need not wait it out, the way the
// daemon's request timeout is one.
var replayTimeout = 2 * time.Second

// replayChunkBytes is how much of the recording goes out between deadline
// resets: small enough that a client reading steadily keeps resetting the
// clock, large enough not to turn a replay into thousands of writes.
const replayChunkBytes = 64 << 10

type session struct {
	id      string
	pty     *os.File
	command *exec.Cmd
	onExit  func()

	mu      sync.Mutex
	writeMu sync.Mutex
	history recording
	// modes survives the recording being trimmed, so a mode the guest set
	// long ago is still restored to a reattaching client.
	modes   *modeTracker
	clients map[net.Conn]*attachment
	closed  bool
}

// attachment tracks one attached client. A client is not live until it has
// been sent the recorded history; output that arrives before then is queued so
// it reaches the client after the recording rather than interleaved with it.
type attachment struct {
	pending []byte
	live    bool
}

func startSession(id, directory, shell string, environment []string, columns, rows uint16, onExit func()) (*session, error) {
	if columns == 0 {
		columns = 80
	}
	if rows == 0 {
		rows = 24
	}

	command := exec.Command(shell)
	command.Dir = directory
	// The client's environment, not the daemon's. A daemon started days ago
	// from another login session would otherwise hand every new shell that
	// session's PATH and variables.
	if environment == nil {
		environment = os.Environ()
	}
	command.Env = append(environment, "TERM=xterm-256color", "ROMTY=1")
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Cols: columns, Rows: rows})
	if err != nil {
		return nil, fmt.Errorf("start shell PTY: %w", err)
	}

	value := &session{
		id:      id,
		pty:     terminal,
		command: command,
		onExit:  onExit,
		modes:   newModeTracker(),
		clients: make(map[net.Conn]*attachment),
	}
	go value.read()
	go value.wait()
	return value, nil
}

// read drains the PTY master until it is exhausted, and owns closing it. The
// master outlives the shell: it stops answering once the slave is gone, but the
// descriptor stays open, and by then wait has already told the server to drop
// the session, so nothing holds the only reference that could close it. A
// descriptor is not memory, so the finalizer that would eventually release it
// is not pressured by running out of them — a daemon left running for days
// leaked one per terminal that exited. Closing here rather than after Wait is
// what keeps the shell's last words: the read loop ends when the master has
// no more to give, not when the process does.
func (s *session) read() {
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		_ = s.pty.Close()
	}()
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
	s.clients = make(map[net.Conn]*attachment)
	s.mu.Unlock()
	for _, connection := range clients {
		connection.Close()
	}
	s.onExit()
}

func (s *session) broadcast(data []byte) {
	s.mu.Lock()
	s.modes.observe(data)
	s.history.append(data)
	live := make([]net.Conn, 0, len(s.clients))
	stalled := make([]net.Conn, 0)
	for connection, attached := range s.clients {
		if attached.live {
			live = append(live, connection)
			continue
		}
		// Still being sent the recording. Queue rather than interleave, and
		// give up on a client that cannot even keep up with the queue.
		attached.pending = append(attached.pending, data...)
		if len(attached.pending) > maxHistoryBytes {
			stalled = append(stalled, connection)
		}
	}
	s.mu.Unlock()

	for _, connection := range live {
		_ = connection.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
		if _, err := connection.Write(data); err != nil {
			s.detach(connection)
		}
		_ = connection.SetWriteDeadline(time.Time{})
	}
	for _, connection := range stalled {
		s.detach(connection)
	}
}

func (s *session) attach(connection net.Conn) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("terminal session has exited")
	}
	// Take the recording and join the client list in one step, so nothing the
	// shell prints from here on is lost. Everything expensive — filtering up
	// to maxHistoryBytes and pushing it down a socket that may be slow — then
	// happens with the lock released, because the PTY read loop needs it to
	// keep the shell running for everyone else.
	// Copied, not aliased: the recording is a ring written in place, so a
	// slice into it would be rewritten under the replay's feet.
	recorded := s.history.bytes()
	modes := s.modes.restore()
	attached := &attachment{}
	s.clients[connection] = attached
	s.mu.Unlock()

	if err := s.replay(connection, modes, recorded); err != nil {
		s.detach(connection)
		return err
	}

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

// replay sends the recorded screen and then whatever arrived while it was
// being sent, until the client has caught up and can be marked live.
func (s *session) replay(connection net.Conn, modes, recording []byte) error {
	defer connection.SetWriteDeadline(time.Time{})

	// The modes go first so a mode set before the recording's window is
	// restored; any change still inside the recording replays on top of them.
	if err := writeReplay(connection, append([]byte(resetScreen), modes...)); err != nil {
		return fmt.Errorf("initialize attached terminal: %w", err)
	}
	if err := writeReplay(connection, stripQueries(recording)); err != nil {
		return fmt.Errorf("restore terminal history: %w", err)
	}
	for {
		s.mu.Lock()
		attached, ok := s.clients[connection]
		if !ok {
			s.mu.Unlock()
			return fmt.Errorf("terminal session has exited")
		}
		if len(attached.pending) == 0 {
			attached.live = true
			s.mu.Unlock()
			return nil
		}
		queued := attached.pending
		attached.pending = nil
		s.mu.Unlock()

		if err := writeReplay(connection, queued); err != nil {
			return fmt.Errorf("restore terminal history: %w", err)
		}
	}
}

// writeReplay pushes the recording out a piece at a time, refreshing the
// deadline before each one, so the timeout means "this client has stopped
// reading" and not "this client is taking a while".
//
// One deadline over the whole replay meant the second. The TUI takes 32 KiB
// per turn of its event loop, so a full recording is hundreds of turns with a
// render between them, and an attach that was making steady progress was cut
// off for being long. romty reattached, the daemon replayed the same
// recording, and it was cut off again — the loop the reattach backoff damps.
func writeReplay(connection net.Conn, data []byte) error {
	for len(data) > 0 {
		if err := connection.SetWriteDeadline(time.Now().Add(replayTimeout)); err != nil {
			return err
		}
		piece := min(len(data), replayChunkBytes)
		if _, err := connection.Write(data[:piece]); err != nil {
			return err
		}
		data = data[piece:]
	}
	return nil
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("terminal session has exited")
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
	_ = s.pty.Close()
	s.mu.Unlock()
	if s.command.Process != nil {
		_ = s.command.Process.Kill()
	}
}
