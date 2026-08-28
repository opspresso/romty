package daemon

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/protocol"
)

// newSessionForTest builds the parts of a session that do not need a live PTY.
// Every test session goes through it so none is left half-built: the daemon
// reads a session's guest tracker and recording whether or not the test that
// made it cared about either.
func newSessionForTest(terminal *os.File) *session {
	return &session{
		pty:     terminal,
		guest:   newGuestTracker(),
		clients: make(map[net.Conn]*attachment),
	}
}

func TestShutdownDoesNotWaitForDirectoryPreflight(t *testing.T) {
	for _, action := range []string{protocol.ActionAddRoot, protocol.ActionEnsureWorkspace} {
		t.Run(action, func(t *testing.T) {
			server, err := New(filepath.Join(t.TempDir(), "daemon.sock"), filepath.Join(t.TempDir(), "state.json"), "/bin/sh")
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			request := protocol.Request{Action: action, Path: "/root"}
			if action == protocol.ActionEnsureWorkspace {
				server.value.Roots = []model.Root{{ID: "root-1", Name: "root", Path: "/root"}}
				request.RootID = "root-1"
				request.Path = "/root/child"
			}

			entered := make(chan struct{})
			release := make(chan struct{})
			released := false
			previous := resolveDirectory
			resolveDirectory = func(path string) (string, error) {
				close(entered)
				<-release
				return path, nil
			}
			t.Cleanup(func() {
				resolveDirectory = previous
				if !released {
					close(release)
				}
			})

			response := make(chan protocol.Response, 1)
			go func() { response <- server.dispatch(request) }()
			<-entered
			server.beginShutdown()
			drained := make(chan struct{})
			go func() {
				server.mutations.Wait()
				close(drained)
			}()
			select {
			case <-drained:
			case <-time.After(time.Second):
				t.Fatal("shutdown waited for directory preflight")
			}

			close(release)
			released = true
			result := <-response
			if result.Error != "daemon is shutting down" {
				t.Fatalf("response error = %q, want shutdown rejection", result.Error)
			}
			if len(server.value.Roots) != 0 && action == protocol.ActionAddRoot {
				t.Fatalf("roots = %#v, want no late mutation", server.value.Roots)
			}
			if len(server.value.Workspaces) != 0 {
				t.Fatalf("workspaces = %#v, want no late mutation", server.value.Workspaces)
			}
		})
	}
}

func TestShutdownDoesNotWaitForResponseSnapshot(t *testing.T) {
	for _, action := range []string{protocol.ActionAddRoot, protocol.ActionRemoveRoot} {
		t.Run(action, func(t *testing.T) {
			base := t.TempDir()
			server, err := New(filepath.Join(base, "daemon.sock"), filepath.Join(base, "state.json"), "/bin/sh")
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			request := protocol.Request{Action: action}
			if action == protocol.ActionAddRoot {
				request.Path = t.TempDir()
			} else {
				server.value.Roots = []model.Root{
					{ID: "remove", Name: "remove", Path: t.TempDir()},
					{ID: "keep", Name: "keep", Path: t.TempDir()},
				}
				request.RootID = "remove"
			}

			entered := make(chan struct{})
			release := make(chan struct{})
			released := false
			previous := readDirectory
			readDirectory = func(string) ([]os.DirEntry, error) {
				close(entered)
				<-release
				return nil, nil
			}
			t.Cleanup(func() {
				readDirectory = previous
				if !released {
					close(release)
				}
			})

			response := make(chan protocol.Response, 1)
			go func() { response <- server.dispatch(request) }()
			<-entered
			server.beginShutdown()
			drained := make(chan struct{})
			go func() {
				server.mutations.Wait()
				close(drained)
			}()
			select {
			case <-drained:
			case <-time.After(time.Second):
				t.Fatal("shutdown waited for response snapshot")
			}

			close(release)
			released = true
			result := <-response
			if result.Error != "" {
				t.Fatalf("response error = %q", result.Error)
			}
			if result.Snapshot == nil {
				t.Fatal("response has no snapshot")
			}
		})
	}
}

// A peer that connects and never finishes a request must not hold a goroutine
// and a file descriptor for the daemon's whole life, and must not keep the
// daemon from serving anyone else.
func TestHandshakeTimesOutOnASilentPeer(t *testing.T) {
	previous := requestTimeout
	requestTimeout = 150 * time.Millisecond
	t.Cleanup(func() { requestTimeout = previous })

	base, err := os.MkdirTemp("/tmp", "romty-handshake-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	socket := filepath.Join(base, "daemon.sock")
	server, err := New(socket, filepath.Join(base, "state.json"), "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() { cancel(); <-done }()

	deadline := time.Now().Add(3 * time.Second)
	var silent net.Conn
	for time.Now().Before(deadline) {
		if silent, err = net.Dial("unix", socket); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if silent == nil {
		t.Fatalf("could not reach the daemon: %v", err)
	}
	defer silent.Close()

	if err := silent.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	buffer := make([]byte, 4096)
	for {
		if _, err := silent.Read(buffer); err != nil {
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				t.Fatal("the daemon held the connection open past the handshake timeout")
			}
			break
		}
	}
}

func TestServeLimitsActiveConnectionsAndRecoversCapacity(t *testing.T) {
	previous := maxActiveConnections
	maxActiveConnections = 1
	t.Cleanup(func() { maxActiveConnections = previous })

	base, err := os.MkdirTemp("/tmp", "romty-capacity-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	socket := filepath.Join(base, "daemon.sock")
	server, err := New(socket, filepath.Join(base, "state.json"), "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.SetLogger(log.New(io.Discard, "", 0))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() { cancel(); <-done }()

	// The connection that proves the daemon is listening is the one that holds
	// capacity. A separate probe would share the listen backlog with it, and
	// the accept loop could spend the only slot on that probe — already closed,
	// so it frees the slot again — and reject this one outright.
	deadline := time.Now().Add(3 * time.Second)
	var silent net.Conn
	for {
		silent, err = net.Dial("unix", socket)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("could not reach daemon: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	defer silent.Close()
	waitForConnections(t, server, 1, "silent connection never occupied capacity")

	rejected, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatalf("Dial() rejected error = %v", err)
	}
	rejected.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := rejected.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection above the active limit stayed open")
	}
	rejected.Close()

	silent.Close()
	waitForConnections(t, server, 0, "closed connection did not release capacity")
}

// waitForConnections gives every wait its own budget. One deadline shared by
// the whole test is spent by whichever step the runner happened to be slow at,
// and the step after it fails for a capacity change that had not happened yet.
func waitForConnections(t *testing.T, server *Server, want int, message string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for len(server.connections) != want {
		if time.Now().After(deadline) {
			t.Fatalf("%s: %d active, want %d", message, len(server.connections), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAttachLimitRejectsOnlyExcessTerminalClients(t *testing.T) {
	value := newSessionForTest(nil)
	server := &Server{
		sessions:    map[string]*session{"tab-1": value},
		logger:      log.New(io.Discard, "", 0),
		accepting:   true,
		attachments: make(chan struct{}, 1),
	}

	firstDaemon, firstClient := net.Pipe()
	firstDone := make(chan struct{})
	go func() {
		server.handle(firstDaemon)
		close(firstDone)
	}()
	if err := protocol.Write(firstClient, protocol.Request{
		Action: protocol.ActionAttach, Version: protocol.Version, TabID: "tab-1", ClientID: "first",
	}); err != nil {
		t.Fatalf("Write() first attach error = %v", err)
	}
	firstReader := bufio.NewReader(firstClient)
	var firstResponse protocol.Response
	if err := protocol.Read(firstReader, &firstResponse); err != nil {
		t.Fatalf("Read() first attach error = %v", err)
	}
	if firstResponse.Error != "" {
		t.Fatalf("first attach error = %q", firstResponse.Error)
	}
	if _, err := io.CopyN(io.Discard, firstReader, int64(firstResponse.ReplayBytes)); err != nil {
		t.Fatalf("read first replay error = %v", err)
	}

	secondDaemon, secondClient := net.Pipe()
	secondDone := make(chan struct{})
	go func() {
		server.handle(secondDaemon)
		close(secondDone)
	}()
	if err := protocol.Write(secondClient, protocol.Request{
		Action: protocol.ActionAttach, Version: protocol.Version, TabID: "tab-1", ClientID: "second",
	}); err != nil {
		t.Fatalf("Write() second attach error = %v", err)
	}
	var secondResponse protocol.Response
	if err := protocol.Read(bufio.NewReader(secondClient), &secondResponse); err != nil {
		t.Fatalf("Read() second attach error = %v", err)
	}
	if secondResponse.Error != "too many terminal attachments" {
		t.Fatalf("second attach error = %q", secondResponse.Error)
	}
	secondClient.Close()
	<-secondDone

	firstClient.Close()
	<-firstDone
	if len(server.attachments) != 0 {
		t.Fatal("closed attachment did not release capacity")
	}
}

// A client attaching must not stop the shell for everyone else. The recording
// can be megabytes and the socket slow, so none of that may happen while the
// PTY read loop's lock is held. Driven through attach so the locking decision
// itself is what is under test.
func TestAttachDoesNotStallTheSessionForOtherClients(t *testing.T) {
	slow, unread := net.Pipe()
	defer slow.Close()
	defer unread.Close()

	value := newSessionForTest(nil)
	value.history.append(bytes.Repeat([]byte("history\r\n"), 4096))

	attached := make(chan error, 1)
	go func() { attached <- value.attach(slow) }()

	// The attaching client never reads, so its write blocks once the pipe
	// fills. The PTY read loop must still make progress regardless.
	broadcast := make(chan struct{})
	go func() {
		for range 200 {
			value.broadcast([]byte("live output\r\n"))
		}
		close(broadcast)
	}()
	select {
	case <-broadcast:
	case <-time.After(3 * time.Second):
		t.Fatal("broadcast blocked behind an attaching client")
	}

	unread.Close()
	select {
	case <-attached:
	case <-time.After(3 * time.Second):
		t.Fatal("attach never returned after its client went away")
	}
}

func TestSlowLiveClientDoesNotStallAnotherClient(t *testing.T) {
	previous := maxLiveClientQueueBytes
	maxLiveClientQueueBytes = 64
	t.Cleanup(func() { maxLiveClientQueueBytes = previous })

	value := newSessionForTest(nil)
	slow, unread := net.Pipe()
	healthy, reader := net.Pipe()
	t.Cleanup(func() {
		slow.Close()
		unread.Close()
		healthy.Close()
		reader.Close()
	})
	for _, connection := range []net.Conn{slow, healthy} {
		attached := &attachment{
			output: make(chan []byte, maxLiveClientQueueChunks),
			done:   make(chan struct{}),
			live:   true,
		}
		value.clients[connection] = attached
		go value.writeClient(connection, attached)
	}

	var received []byte
	for range 3 {
		started := time.Now()
		value.broadcast(bytes.Repeat([]byte("x"), 32))
		if time.Since(started) > 100*time.Millisecond {
			t.Fatal("broadcast waited for a live client that stopped reading")
		}
		buffer := make([]byte, 32)
		reader.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := io.ReadFull(reader, buffer); err != nil {
			t.Fatalf("healthy client was stalled behind another live client: %v", err)
		}
		received = append(received, buffer...)
	}
	if !bytes.Equal(received, bytes.Repeat([]byte("x"), 96)) {
		t.Fatalf("healthy client output = %q", received)
	}
}

func TestForegroundClientOwnsTerminalSize(t *testing.T) {
	terminal, peer, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open() error = %v", err)
	}
	defer terminal.Close()
	defer peer.Close()

	first, firstPeer := net.Pipe()
	second, secondPeer := net.Pipe()
	defer firstPeer.Close()
	defer secondPeer.Close()
	value := newSessionForTest(nil)
	value.pty = terminal
	value.clients[first] = &attachment{clientID: "first", columns: 80, rows: 24, activity: 1}
	value.clients[second] = &attachment{clientID: "second", columns: 120, rows: 40, activity: 2}
	value.foreground = first
	value.activity = 2

	if err := value.resizeFor("first", 80, 24); err != nil {
		t.Fatalf("resizeFor() foreground error = %v", err)
	}
	if err := value.resizeFor("second", 120, 40); err != nil {
		t.Fatalf("resizeFor() background error = %v", err)
	}
	assertTerminalSize(t, terminal, 80, 24)

	if err := value.writeFrom(second, []byte("x")); err != nil {
		t.Fatalf("writeFrom() background error = %v", err)
	}
	assertTerminalSize(t, terminal, 120, 40)

	value.detach(second)
	assertTerminalSize(t, terminal, 80, 24)
	first.Close()
}

func assertTerminalSize(t *testing.T, terminal *os.File, columns, rows int) {
	t.Helper()
	gotRows, gotColumns, err := pty.Getsize(terminal)
	if err != nil {
		t.Fatalf("pty.Getsize() error = %v", err)
	}
	if gotColumns != columns || gotRows != rows {
		t.Fatalf("terminal size = %dx%d, want %dx%d", gotColumns, gotRows, columns, rows)
	}
}

func TestAttachAnnouncesTheInitialReplayBoundary(t *testing.T) {
	client, daemonSide := net.Pipe()
	defer daemonSide.Close()

	value := newSessionForTest(nil)
	value.history.append([]byte("before\x1b[6nafter"))

	announced := make(chan int, 1)
	attached := make(chan error, 1)
	go func() {
		attached <- value.attachReady(client, func(replayBytes int) error {
			announced <- replayBytes
			return nil
		})
	}()

	want := []byte(resetScreen + "beforeafter")
	if got := <-announced; got != len(want) {
		t.Fatalf("announced replay bytes = %d, want %d", got, len(want))
	}
	replay := make([]byte, len(want))
	daemonSide.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(daemonSide, replay); err != nil {
		t.Fatalf("ReadFull() replay error = %v", err)
	}
	if !bytes.Equal(replay, want) {
		t.Fatalf("replay = %q, want %q", replay, want)
	}
	daemonSide.Close()
	<-attached
}

func TestAttachKeepsLegacyReplayOnTheTerminalStream(t *testing.T) {
	client, daemonSide := net.Pipe()
	defer daemonSide.Close()

	value := newSessionForTest(nil)
	value.history.append([]byte("legacy history"))
	server := &Server{
		sessions: map[string]*session{"tab-1": value},
		logger:   log.New(io.Discard, "", 0),
	}
	done := make(chan struct{})
	go func() {
		server.handleAttach(client, protocol.Request{
			Action:  protocol.ActionAttach,
			Version: 4,
			TabID:   "tab-1",
		})
		close(done)
	}()

	reader := bufio.NewReader(daemonSide)
	var response protocol.Response
	if err := protocol.Read(reader, &response); err != nil {
		t.Fatalf("Read() attach response error = %v", err)
	}
	if response.Version != 4 || response.ReplayBytes != 0 {
		t.Fatalf("legacy attach response = %#v, want version 4 without a replay boundary", response)
	}
	want := []byte(resetScreen + "legacy history")
	replay := make([]byte, len(want))
	if _, err := io.ReadFull(reader, replay); err != nil {
		t.Fatalf("ReadFull() legacy replay error = %v", err)
	}
	if !bytes.Equal(replay, want) {
		t.Fatalf("legacy replay = %q, want %q", replay, want)
	}
	daemonSide.Close()
	<-done
}

func TestAttachInfersCapabilitiesForAPreNegotiationClient(t *testing.T) {
	client, daemonSide := net.Pipe()
	defer daemonSide.Close()

	value := newSessionForTest(nil)
	value.columns, value.rows = 90, 25
	value.history.append([]byte("version five history"))
	server := &Server{
		sessions: map[string]*session{"tab-1": value},
		logger:   log.New(io.Discard, "", 0),
	}
	done := make(chan struct{})
	go func() {
		server.handleAttach(client, protocol.Request{
			Action:  protocol.ActionAttach,
			Version: 5,
			TabID:   "tab-1",
		})
		close(done)
	}()

	reader := bufio.NewReader(daemonSide)
	var response protocol.Response
	if err := protocol.Read(reader, &response); err != nil {
		t.Fatalf("Read() attach response error = %v", err)
	}
	want := []byte(resetScreen + "version five history")
	if response.Version != 5 || response.ReplayBytes != len(want) ||
		response.ReplayColumns != 90 || response.ReplayRows != 25 {
		t.Fatalf("version 5 attach response = %#v, want replay size %d at 90x25", response, len(want))
	}
	replay := make([]byte, len(want))
	if _, err := io.ReadFull(reader, replay); err != nil {
		t.Fatalf("ReadFull() replay error = %v", err)
	}
	if !bytes.Equal(replay, want) {
		t.Fatalf("replay = %q, want %q", replay, want)
	}
	daemonSide.Close()
	<-done
}

// Output that arrives while the recording is still being written has to reach
// the client after it, not spliced into the middle of it. The pipe is
// unbuffered, so the replay is genuinely mid-write when the broadcast lands.
func TestAttachHandsOffToLiveOutputInOrder(t *testing.T) {
	client, daemonSide := net.Pipe()
	defer daemonSide.Close()

	value := newSessionForTest(nil)
	value.history.append(bytes.Repeat([]byte("RECORDED"), 8192))

	attached := make(chan error, 1)
	go func() { attached <- value.attach(client) }()

	// Read one chunk, so the replay is blocked partway through its recording.
	buffer := make([]byte, 512)
	daemonSide.SetReadDeadline(time.Now().Add(3 * time.Second))
	first, err := daemonSide.Read(buffer)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	seen := append([]byte(nil), buffer[:first]...)

	value.broadcast([]byte("LIVE"))

	for !bytes.Contains(seen, []byte("LIVE")) {
		daemonSide.SetReadDeadline(time.Now().Add(3 * time.Second))
		count, err := daemonSide.Read(buffer)
		seen = append(seen, buffer[:count]...)
		if err != nil {
			t.Fatalf("the client never received the live output: %v", err)
		}
	}

	live := bytes.Index(seen, []byte("LIVE"))
	recording := bytes.Count(seen[:live], []byte("RECORDED"))
	if recording != 8192 {
		t.Fatalf("live output arrived after only %d of 8192 recorded chunks; it was spliced into the recording", recording)
	}
	client.Close()
	<-attached
}

// A daemon killed outright leaves its socket standing with nothing answering
// on it, because only an orderly shutdown removes it. The next daemon has to
// unlink that one and bind its own, or romty never starts again after a crash.
func TestServeReplacesASocketNothingAnswersOn(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "romty-stale-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	socket := filepath.Join(base, "daemon.sock")

	leaveStaleSocket(t, socket)

	server, err := New(socket, filepath.Join(base, "state.json"), "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.SetLogger(log.New(io.Discard, "", 0))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() { cancel(); <-done }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.Dial("unix", socket)
		if err == nil {
			connection.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the daemon never bound over the socket its predecessor left behind")
}

// The other half of the same decision: a socket that answers belongs to a
// running daemon. Unlinking it would leave that daemon listening on a name no
// client can reach while it kept writing the state file.
func TestPrepareSocketRefusesOneThatAnswers(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "romty-live-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	socket := filepath.Join(base, "daemon.sock")

	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			connection.Close()
		}
	}()

	if err := prepareSocket(socket); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("prepareSocket() error = %v, want ErrAlreadyRunning", err)
	}
	if _, err := os.Lstat(socket); err != nil {
		t.Fatalf("the socket a daemon is answering on was removed: %v", err)
	}
}

// leaveStaleSocket puts a socket file in place with nothing listening on it,
// which is what a daemon that was killed rather than stopped leaves behind.
func leaveStaleSocket(t *testing.T, path string) {
	t.Helper()
	address, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		t.Fatalf("ResolveUnixAddr() error = %v", err)
	}
	listener, err := net.ListenUnix("unix", address)
	if err != nil {
		t.Fatalf("ListenUnix() error = %v", err)
	}
	// Closing a unix listener normally unlinks its socket, which is exactly
	// what a killed daemon never gets to do.
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("no socket was left behind: %v", err)
	}
}

// A shell that the daemon killed on its way out must not be persisted one tab
// at a time. The process exits as soon as shutdown returns, so each of those
// saves is a race it can lose halfway through — leaving a temporary file in
// the user's romty home for every shutdown — to record something the next
// daemon throws away before it listens.
func TestShutdownDoesNotPersistTheTabsItIsKilling(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "romty-shutdown-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	statePath := filepath.Join(base, "state.json")

	server, err := New(filepath.Join(base, "daemon.sock"), statePath, "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.SetLogger(log.New(io.Discard, "", 0))
	server.value.Tabs = []model.Tab{{ID: "tab-1", WorkspaceID: "workspace-1", Name: "1", Running: true}}
	if err := server.store.Save(server.value); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	server.shutdown()
	server.sessionExited("tab-1")

	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a shell killed by shutdown was persisted, racing the process exit that follows")
	}
	// The tab is gone from memory either way, which is what the next daemon
	// would have written anyway.
	if len(server.value.Tabs) != 0 {
		t.Fatalf("tabs in memory = %d, want the exited tab dropped", len(server.value.Tabs))
	}
}

// A client that keeps reading must not be cut off for taking a while. One
// deadline over the whole replay bounds the transfer rather than a stall, so a
// large recording sent to a steadily reading client can still be dropped.
func TestAttachSurvivesAClientThatReadsSteadilyButSlowly(t *testing.T) {
	withHistoryLimit(t, 256*1024)
	previous := replayTimeout
	replayTimeout = 150 * time.Millisecond
	t.Cleanup(func() { replayTimeout = previous })

	client, daemonSide := net.Pipe()
	defer daemonSide.Close()

	value := newSessionForTest(nil)
	value.history.append(bytes.Repeat([]byte("R"), maxHistoryBytes))

	attached := make(chan error, 1)
	go func() { attached <- value.attach(client) }()

	// Small reads with a pause between them make the whole transfer take several
	// times replayTimeout while never stalling for one.
	buffer := make([]byte, 8*1024)
	seen := 0
	for seen < maxHistoryBytes {
		time.Sleep(10 * time.Millisecond)
		daemonSide.SetReadDeadline(time.Now().Add(3 * time.Second))
		count, err := daemonSide.Read(buffer)
		if err != nil {
			t.Fatalf("the replay was cut off after %d of %d bytes: %v", seen, maxHistoryBytes, err)
		}
		seen += bytes.Count(buffer[:count], []byte("R"))
	}
	client.Close()
	<-attached
}

// The recording handed to the replay must be a copy: the ring is written in
// place, so an aliased slice would be rewritten under the replay's feet.
func TestAttachCopiesTheRecordingItReplays(t *testing.T) {
	previous := maxHistoryBytes
	maxHistoryBytes = 4096
	t.Cleanup(func() { maxHistoryBytes = previous })

	client, daemonSide := net.Pipe()
	defer daemonSide.Close()

	value := newSessionForTest(nil)
	value.history.append(bytes.Repeat([]byte("O"), maxHistoryBytes))

	attached := make(chan error, 1)
	go func() { attached <- value.attach(client) }()

	// Consume the screen reset so what follows is the recording alone, then
	// block the replay partway and overwrite the whole history behind it.
	buffer := make([]byte, len(resetScreen))
	daemonSide.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := daemonSide.Read(buffer); err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	value.broadcast(bytes.Repeat([]byte("N"), maxHistoryBytes))

	buffer = make([]byte, 64)
	var seen []byte
	for len(seen) < maxHistoryBytes {
		daemonSide.SetReadDeadline(time.Now().Add(3 * time.Second))
		count, err := daemonSide.Read(buffer)
		seen = append(seen, buffer[:count]...)
		if err != nil {
			t.Fatalf("the replay stopped early after %d bytes: %v", len(seen), err)
		}
	}
	if replaced := bytes.IndexByte(seen[:maxHistoryBytes], 'N'); replaced >= 0 {
		t.Fatalf("the recording was rewritten %d bytes in, so the replay was reading the live buffer", replaced)
	}
	client.Close()
	<-attached
}

// The daemon outlives every client and fails where nobody is watching, so
// diagnosis depends entirely on what reaches daemon.log. It used to write
// nothing at all: the file existed and was always empty.
func TestDaemonRecordsItsLifecycleAndFailures(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "romty-log-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	socket := filepath.Join(base, "daemon.sock")
	server, err := New(socket, filepath.Join(base, "state.json"), "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	var recorded syncBuffer
	server.SetLogger(log.New(&recorded, "", 0))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(recorded.String(), "listening") {
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(recorded.String(), socket) {
		t.Fatalf("the daemon did not record that it started: %q", recorded.String())
	}

	cancel()
	<-done
	if !strings.Contains(recorded.String(), "stopped") {
		t.Fatalf("the daemon did not record that it stopped: %q", recorded.String())
	}
}

// A shell exiting is the one place the state file can fall behind with nothing
// to roll back to, so it has to say so.
func TestSessionExitIsRecordedWhenStateCannotBeSaved(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "romty-log-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	statePath := filepath.Join(home, "state.json")
	server, err := New(filepath.Join(home, "daemon.sock"), statePath, "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	// Read-only directory, so the temporary file the atomic save needs cannot
	// be created — the shape of a full disk or a permissions change.
	if err := os.Chmod(home, 0o500); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { os.Chmod(home, 0o700) })
	if probe, err := os.CreateTemp(home, "probe-"); err == nil {
		probe.Close()
		t.Skip("the state directory is still writable; running as root?")
	}
	var recorded syncBuffer
	server.SetLogger(log.New(&recorded, "", 0))

	server.sessionExited("tab-7")
	if !strings.Contains(recorded.String(), "tab-7") {
		t.Fatalf("the exit was not recorded: %q", recorded.String())
	}
	if !strings.Contains(recorded.String(), "persist state") {
		t.Fatalf("the failed save was not recorded: %q", recorded.String())
	}
}

// Bracketed paste is set once, early, and never again. Losing it to the
// recording's window means the shell can no longer tell pasted text from
// typed text, so a multi-line paste runs each line — data loss arriving long
// after the mode went missing.
func TestAttachRestoresModesTheRecordingNoLongerHolds(t *testing.T) {
	previous := maxHistoryBytes
	maxHistoryBytes = 2048
	t.Cleanup(func() { maxHistoryBytes = previous })

	value := newSessionForTest(nil)
	value.broadcast([]byte("\x1b[?2004h\x1b[?1h"))
	// Enough output to push those modes out of the recording entirely.
	value.broadcast([]byte(strings.Repeat("x", maxHistoryBytes*2)))
	if strings.Contains(string(value.history.bytes()), "2004h") {
		t.Fatal("the recording still holds the mode; the test proves nothing")
	}

	client, daemonSide := net.Pipe()
	defer daemonSide.Close()
	attached := make(chan error, 1)
	go func() { attached <- value.attach(client) }()

	buffer := make([]byte, 4096)
	var seen []byte
	for !bytes.Contains(seen, []byte("xxx")) {
		daemonSide.SetReadDeadline(time.Now().Add(3 * time.Second))
		count, err := daemonSide.Read(buffer)
		seen = append(seen, buffer[:count]...)
		if err != nil {
			t.Fatalf("the replay stopped early: %v", err)
		}
	}
	for _, mode := range []string{"\x1b[?1h", "\x1b[?2004h"} {
		position := bytes.Index(seen, []byte(mode))
		if position < 0 {
			t.Fatalf("the replay did not restore %q: %q", mode, seen[:64])
		}
		if position > bytes.Index(seen, []byte("xxx")) {
			t.Fatalf("%q was restored after the recording, so the recording could undo it", mode)
		}
	}
	client.Close()
	<-attached
}

// The environment must come from the request, not from the daemon's own. In
// tests both are the same process, so this passes one the daemon does not have.
func TestStartSessionUsesTheEnvironmentItIsGiven(t *testing.T) {
	if _, present := os.LookupEnv("ROMTY_ONLY_IN_THE_REQUEST"); present {
		t.Skip("the marker is already in the daemon's environment")
	}
	value, err := startSession("tab-1", t.TempDir(), "/bin/sh",
		append(os.Environ(), "ROMTY_ONLY_IN_THE_REQUEST=yes", "ROMTY_TAB_ID=stale"), 80, 24, func() {})
	if err != nil {
		t.Fatalf("startSession() error = %v", err)
	}
	t.Cleanup(value.close)

	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	go value.attach(local)

	seen := make(chan string, 1)
	go func() {
		buffer := make([]byte, 4096)
		var output []byte
		for {
			remote.SetReadDeadline(time.Now().Add(3 * time.Second))
			count, err := remote.Read(buffer)
			output = append(output, buffer[:count]...)
			if err != nil || bytes.Contains(output, []byte("marker=yes tab=tab-1")) {
				seen <- string(output)
				return
			}
		}
	}()
	if err := value.write([]byte("printf 'marker=%s tab=%s\\n' \"$ROMTY_ONLY_IN_THE_REQUEST\" \"$ROMTY_TAB_ID\"\n")); err != nil {
		t.Fatalf("write() error = %v", err)
	}
	if output := <-seen; !bytes.Contains([]byte(output), []byte("marker=yes tab=tab-1")) {
		t.Fatalf("the shell did not receive the request's environment: %q", output)
	}
}

// The bind, not the probe that precedes it, is what decides who owns the
// socket. Two daemons starting at once can both find nothing to dial and race
// to create one, and the one that loses the bind has lost a race rather than
// failed: reporting it as a bind error sent the ordinary outcome of that race
// to daemon.log as a failure and exited non-zero.
func TestListenPrivatelyReportsAlreadyRunningWhenTheBindIsLost(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "romty-bind-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	socket := filepath.Join(base, "daemon.sock")

	winner, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer winner.Close()

	listener, err := listenPrivately(socket)
	if err == nil {
		listener.Close()
		t.Fatal("listenPrivately() bound a socket another listener already owns")
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("listenPrivately() error = %v, want ErrAlreadyRunning", err)
	}
}

// The handshake is bounded in both directions. A peer that sends its request
// and then stops reading holds a goroutine and a file descriptor exactly as a
// peer that never sends one does, once the reply outgrows the socket buffer.
func TestReplyGivesUpOnAPeerThatStoppedReading(t *testing.T) {
	previous := requestTimeout
	requestTimeout = 100 * time.Millisecond
	t.Cleanup(func() { requestTimeout = previous })

	// net.Pipe buffers nothing, so the write blocks until someone reads, which
	// is what a full socket buffer looks like without having to fill one.
	daemonSide, peer := net.Pipe()
	defer daemonSide.Close()
	defer peer.Close()

	done := make(chan error, 1)
	go func() { done <- reply(daemonSide, protocol.Response{}) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("reply() to a peer that never read reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reply() blocked on a peer that never read")
	}
}

// Two daemons starting together could both find nothing to dial, and the
// second would unlink the first's socket and bind its own — leaving the first
// listening on a name no client could reach and still writing the state file
// the second now owned. The lock decides ownership before the socket is
// touched at all.
func TestServeReportsAlreadyRunningWhenAnotherDaemonHoldsTheLock(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "romty-lock-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	socket := filepath.Join(base, "daemon.sock")

	held, err := lockDaemon(socket + lockSuffix)
	if err != nil {
		t.Fatalf("lockDaemon() error = %v", err)
	}
	defer held.Close()

	server, err := New(socket, filepath.Join(base, "state.json"), "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.SetLogger(log.New(io.Discard, "", 0))

	// Serve runs on its own goroutine because a daemon that takes the lock
	// does not return: the failure this guards against is one that serves on
	// regardless, which as a direct call would simply never come back.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("Serve() error = %v, want ErrAlreadyRunning", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the daemon that lost the lock is serving anyway")
	}
	if _, err := os.Lstat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the daemon that lost the lock still went on to bind the socket")
	}
}

func TestLockDaemonDoesNotFollowASymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Symlink(target, filepath.Join(base, "daemon.sock.lock")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	if lock, err := lockDaemon(filepath.Join(base, "daemon.sock.lock")); err == nil {
		lock.Close()
		t.Fatal("lockDaemon() followed a symbolic link")
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(contents) != "unchanged" {
		t.Fatalf("symlink target = %q, want unchanged", contents)
	}
}

func TestLockDaemonRejectsAHardLink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	lockPath := filepath.Join(base, "daemon.sock.lock")
	if err := os.Link(target, lockPath); err != nil {
		t.Fatalf("Link() error = %v", err)
	}

	if lock, err := lockDaemon(lockPath); err == nil {
		lock.Close()
		t.Fatal("lockDaemon() accepted a multiply linked file")
	}
}

// The tabs a state file carries name shells that died with the last daemon, so
// they are cleared before the socket exists. A daemon that cannot clear them
// has nothing consistent to serve, and must not have touched the socket path a
// working daemon still owns.
func TestServeClearsStaleTabsBeforeTouchingTheSocket(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root writes into a directory it has no write permission on")
	}
	base, err := os.MkdirTemp("/tmp", "romty-stale-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })

	// The state file lives where it cannot be rewritten. Its own directory,
	// because Serve narrows the socket's directory and would undo the mode.
	stateDirectory := filepath.Join(base, "state")
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	statePath := filepath.Join(stateDirectory, "state.json")
	stale := `{"roots":[],"workspaces":[],"tabs":[{"id":"tab-1","workspace_id":"workspace-1","name":"1","running":true}]}`
	if err := os.WriteFile(statePath, []byte(stale), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Chmod(stateDirectory, 0o500); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { os.Chmod(stateDirectory, 0o700) })

	// A file standing at the socket path, the way a crash leaves one. Only a
	// daemon that got as far as prepareSocket removes it.
	socket := filepath.Join(base, "daemon.sock")
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	server, err := New(socket, statePath, "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server.SetLogger(log.New(io.Discard, "", 0))
	if err := server.Serve(context.Background()); err == nil {
		t.Fatal("Serve() started on a state file it could not clear")
	}
	if _, err := os.Lstat(socket); err != nil {
		t.Fatalf("the socket path was disturbed by a daemon that never served: %v", err)
	}
}

func TestShutdownDrainsAdmittedMutations(t *testing.T) {
	server := &Server{stop: make(chan struct{}), accepting: true}
	finish, ok := server.beginRequest(protocol.ActionCreateTab)
	if !ok {
		t.Fatal("server rejected a mutation before shutdown")
	}
	server.beginShutdown()
	if _, ok := server.beginRequest(protocol.ActionAddRoot); ok {
		t.Fatal("server admitted a new mutation during shutdown")
	}

	drained := make(chan struct{})
	go func() {
		server.mutations.Wait()
		close(drained)
	}()
	select {
	case <-drained:
		t.Fatal("shutdown passed an admitted mutation that was still running")
	case <-time.After(100 * time.Millisecond):
	}
	finish()
	select {
	case <-drained:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not continue after the admitted mutation finished")
	}
}

func TestSnapshotRevisionAdvancesWithPersistedState(t *testing.T) {
	base := t.TempDir()
	rootPath := filepath.Join(base, "projects")
	workspacePath := filepath.Join(rootPath, "alpha")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	server, err := New(filepath.Join(base, "daemon.sock"), filepath.Join(base, "state.json"), "/bin/sh")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	initial := server.snapshot().Revision
	added := server.addRoot(rootPath)
	if added.Error != "" || added.Snapshot == nil {
		t.Fatalf("addRoot() = %#v", added)
	}
	if added.Snapshot.Revision <= initial {
		t.Fatalf("revision after add = %d, want > %d", added.Snapshot.Revision, initial)
	}
	rootID := added.Snapshot.Roots[0].Root.ID
	ensured := server.ensureWorkspace(rootID, workspacePath)
	if ensured.Error != "" {
		t.Fatalf("ensureWorkspace() error = %s", ensured.Error)
	}
	afterWorkspace := server.snapshot().Revision
	if afterWorkspace <= added.Snapshot.Revision {
		t.Fatalf("revision after workspace = %d, want > %d", afterWorkspace, added.Snapshot.Revision)
	}
	removed := server.removeRoot(rootID)
	if removed.Error != "" || removed.Snapshot == nil {
		t.Fatalf("removeRoot() = %#v", removed)
	}
	if removed.Snapshot.Revision <= afterWorkspace {
		t.Fatalf("revision after remove = %d, want > %d", removed.Snapshot.Revision, afterWorkspace)
	}
}
