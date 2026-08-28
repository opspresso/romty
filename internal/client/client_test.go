package client

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/paths"
	"github.com/opspresso/romty/internal/protocol"
)

func TestNormalizePathExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := normalizePath("~/projects")
	if err != nil {
		t.Fatalf("normalizePath() error = %v", err)
	}
	want := filepath.Join(home, "projects")
	if got != want {
		t.Fatalf("normalizePath() = %q, want %q", got, want)
	}
}

// A daemon from before the version field must be named as the problem, in
// terms the user can act on, rather than surfacing as a puzzling missing field
// or an unknown action.
func TestCallReportsAnOutdatedDaemon(t *testing.T) {
	socket := serveUnversioned(t)

	_, err := New(socket).Snapshot()
	if err == nil {
		t.Fatal("Snapshot() accepted a daemon that speaks a different protocol")
	}
	for _, want := range []string{"protocol 1..5", "0..0"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to mention %q", err, want)
		}
	}
}

func TestProtocolVersionReportsAnOutdatedDaemon(t *testing.T) {
	socket := serveUnversioned(t)

	got, err := New(socket).ProtocolVersion()
	if err != nil {
		t.Fatalf("ProtocolVersion() error = %v", err)
	}
	if got != 0 {
		t.Fatalf("ProtocolVersion() = %d, want the unstamped daemon version 0", got)
	}
}

// The daemon answers ping and shutdown whatever version asked, and this side
// has to accept those answers. It did not, and the two calls that carry the
// remedy were exactly the two that could not reach it: ping reported a
// mismatched daemon as no daemon at all, so romty started a second one and
// gave up with "daemon did not become ready", and `romty stop` stopped the
// daemon and then called that a failure to stop it.
func TestExemptCallsSurviveAnOutdatedDaemon(t *testing.T) {
	socket := serveUnversioned(t)
	backend := New(socket)

	if err := backend.Ping(); err != nil {
		t.Fatalf("Ping() error = %v, want an outdated daemon to answer it", err)
	}
	if err := backend.Shutdown(); err != nil {
		t.Fatalf("Shutdown() error = %v, want an outdated daemon to answer it", err)
	}
}

func TestShutdownWaitsForTheDaemonToReleaseItsSocket(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "daemon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatalf("Chmod() socket error = %v", err)
	}
	defer listener.Close()

	acknowledged := make(chan struct{})
	release := make(chan struct{})
	go func() {
		for requestIndex := 0; requestIndex < 2; requestIndex++ {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			if _, err := bufio.NewReader(connection).ReadBytes('\n'); err != nil {
				connection.Close()
				return
			}
			if err := protocol.Write(connection, protocol.Response{
				Version:      protocol.Version,
				MinVersion:   protocol.MinimumVersion,
				Capabilities: protocol.CapabilitiesForVersion(protocol.Version),
			}); err != nil {
				connection.Close()
				return
			}
			connection.Close()
		}
		close(acknowledged)
		<-release
		listener.Close()
	}()

	done := make(chan error, 1)
	go func() { done <- New(socket).Shutdown() }()
	select {
	case <-acknowledged:
	case <-time.After(3 * time.Second):
		t.Fatal("fake daemon did not acknowledge shutdown")
	}
	select {
	case err := <-done:
		t.Fatalf("Shutdown() returned before daemon cleanup: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Shutdown() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown() did not return after daemon cleanup")
	}
}

// serveUnversioned stands in for a daemon that answers without a version, the
// way one from before the field would.
func serveUnversioned(t *testing.T) string {
	t.Helper()
	socket := filepath.Join(shortTempDir(t), "daemon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatalf("Chmod() socket error = %v", err)
	}
	t.Cleanup(func() { listener.Close() })

	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				request, err := bufio.NewReader(connection).ReadBytes('\n')
				if err != nil {
					return
				}
				connection.Write([]byte("{}\n"))
				if strings.Contains(string(request), `"action":"shutdown"`) {
					listener.Close()
				}
			}()
		}
	}()
	return socket
}

// Starting a second daemon is the answer to nothing listening, and to nothing
// else. An outdated daemon is listening, so EnsureDaemon must let the caller
// reach it and be told which side to restart, rather than starting a rival
// that loses the lock and exits — which is what turned a mismatch into
// "daemon did not become ready".
func TestEnsureDaemonAcceptsAnOutdatedDaemon(t *testing.T) {
	socket := serveUnversioned(t)

	// A path with nothing on it, so an attempt to start a daemon is a failure
	// naming itself rather than a silent second process.
	if err := EnsureDaemon(runtimeFor(socket), "/nonexistent/romty"); err != nil {
		t.Fatalf("EnsureDaemon() error = %v, want an outdated daemon to count as running", err)
	}
}

// A socket that answers but cannot be understood is not an absent daemon
// either. Reporting what the ping said beats spending three seconds waiting
// for a daemon that was never going to start.
func TestEnsureDaemonReportsASocketThatAnswersNothing(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "daemon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatalf("Chmod() socket error = %v", err)
	}
	defer listener.Close()
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			// Accepted and then ignored, which is the whole of this daemon.
			t.Cleanup(func() { connection.Close() })
		}
	}()

	previous := handshakeTimeout
	handshakeTimeout = 200 * time.Millisecond
	defer func() { handshakeTimeout = previous }()

	err = EnsureDaemon(runtimeFor(socket), "/nonexistent/romty")
	if err == nil {
		t.Fatal("EnsureDaemon() succeeded against a socket that answers nothing")
	}
	if strings.Contains(err.Error(), "did not become ready") {
		t.Fatalf("error = %q, want what the ping said rather than a startup timeout", err)
	}
}

func TestEnsureDaemonRejectsAPermissiveSocket(t *testing.T) {
	socket := serveUnversioned(t)
	if err := os.Chmod(socket, 0o666); err != nil {
		t.Fatalf("Chmod() socket error = %v", err)
	}

	if err := EnsureDaemon(runtimeFor(socket), "/nonexistent/romty"); err == nil {
		t.Fatal("EnsureDaemon() trusted a group- and world-accessible socket")
	}
}

func TestEnsureDaemonDoesNotFollowALogSymlink(t *testing.T) {
	directory := shortTempDir(t)
	target := filepath.Join(directory, "target")
	runtime := runtimeFor(filepath.Join(directory, "daemon.sock"))
	if err := os.Symlink(target, runtime.Log); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	if err := EnsureDaemon(runtime, "/nonexistent/romty"); err == nil {
		t.Fatal("EnsureDaemon() accepted a symbolic link as daemon.log")
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("log symlink target was created: %v", err)
	}
}

func TestOpenDaemonLogRotatesAtTheSizeLimit(t *testing.T) {
	directory := shortTempDir(t)
	path := filepath.Join(directory, "daemon.log")
	if err := os.WriteFile(path, []byte("full"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	previous := maxDaemonLogBytes
	maxDaemonLogBytes = 4
	t.Cleanup(func() { maxDaemonLogBytes = previous })

	file, err := openDaemonLog(path)
	if err != nil {
		t.Fatalf("openDaemonLog() error = %v", err)
	}
	if _, err := file.WriteString("new"); err != nil {
		t.Fatalf("WriteString() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	archive, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("ReadFile() archive error = %v", err)
	}
	if string(archive) != "full" {
		t.Fatalf("archive = %q, want %q", archive, "full")
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() current error = %v", err)
	}
	if string(current) != "new" {
		t.Fatalf("current log = %q, want %q", current, "new")
	}
}

func TestOpenDaemonLogKeepsOneArchive(t *testing.T) {
	directory := shortTempDir(t)
	path := filepath.Join(directory, "daemon.log")
	if err := os.WriteFile(path, []byte("current"), 0o600); err != nil {
		t.Fatalf("WriteFile() current error = %v", err)
	}
	if err := os.WriteFile(path+".1", []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile() archive error = %v", err)
	}
	previous := maxDaemonLogBytes
	maxDaemonLogBytes = 1
	t.Cleanup(func() { maxDaemonLogBytes = previous })

	file, err := openDaemonLog(path)
	if err != nil {
		t.Fatalf("openDaemonLog() error = %v", err)
	}
	file.Close()

	archive, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("ReadFile() archive error = %v", err)
	}
	if string(archive) != "current" {
		t.Fatalf("archive = %q, want %q", archive, "current")
	}
}

func TestOpenDaemonLogRejectsAHardLink(t *testing.T) {
	directory := shortTempDir(t)
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	path := filepath.Join(directory, "daemon.log")
	if err := os.Link(target, path); err != nil {
		t.Fatalf("Link() error = %v", err)
	}

	if file, err := openDaemonLog(path); err == nil {
		file.Close()
		t.Fatal("openDaemonLog() accepted a multiply linked file")
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(contents) != "unchanged" {
		t.Fatalf("hard link target = %q, want unchanged", contents)
	}
}

// runtimeFor points a romty home at an existing socket.
func runtimeFor(socket string) paths.Paths {
	directory := filepath.Dir(socket)
	return paths.Paths{
		Directory: directory,
		Socket:    socket,
		State:     filepath.Join(directory, "state.json"),
		Config:    filepath.Join(directory, "config.json"),
		Log:       filepath.Join(directory, "daemon.log"),
	}
}

// A daemon can accept a connection and then say nothing — stopped, wedged, or
// another program holding the socket path. Attach has to give up on that: it
// runs on the goroutine that opens a terminal, so a read with no deadline
// leaves the tab never appearing and nothing to say why.
func TestOpenAttachGivesUpOnADaemonThatNeverAnswers(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "daemon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatalf("Chmod() socket error = %v", err)
	}
	defer listener.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		// Accepted and then ignored, which is the whole of this daemon.
		accepted <- connection
	}()

	previous := handshakeTimeout
	handshakeTimeout = 200 * time.Millisecond
	defer func() { handshakeTimeout = previous }()

	done := make(chan error, 1)
	go func() {
		_, _, err := New(socket).OpenAttach("tab-1")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("OpenAttach() succeeded against a daemon that never answered")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OpenAttach() is still waiting on a daemon that never answers")
	}
	if connection := <-accepted; connection != nil {
		connection.Close()
	}
}

// Opening a terminal prepares the recorded history before handing it to the
// UI, so a long session can be restored without rendering every read along the
// way. Bytes after that exact boundary stay on the stream as live output.
func TestOpenTerminalSeparatesReplayFromLiveOutput(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "daemon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatalf("Chmod() socket error = %v", err)
	}
	defer listener.Close()

	replay := bytes.Repeat([]byte("recorded output\r\n"), 8192)
	live := []byte("live output\r\n")
	go func() {
		if err := answerNegotiation(listener, protocol.Version, protocol.MinimumVersion); err != nil {
			return
		}
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		var request protocol.Request
		if err := protocol.Read(reader, &request); err != nil {
			return
		}
		if err := protocol.Write(connection, protocol.Response{
			Version:       protocol.Version,
			ReplayBytes:   len(replay),
			ReplayColumns: 100,
			ReplayRows:    30,
		}); err != nil {
			return
		}
		if _, err := connection.Write(replay); err != nil {
			return
		}
		_, _ = connection.Write(live)
	}()

	stream, restored, err := New(socket).OpenTerminal("tab-1")
	if err != nil {
		t.Fatalf("OpenTerminal() error = %v", err)
	}
	defer stream.Close()
	if !bytes.Equal(restored, replay) {
		t.Fatalf("replay length = %d, want %d", len(restored), len(replay))
	}
	if sized, ok := stream.(interface{ ReplaySize() (uint16, uint16) }); !ok {
		t.Fatal("terminal stream does not expose its replay size")
	} else if columns, rows := sized.ReplaySize(); columns != 100 || rows != 30 {
		t.Fatalf("replay size = %dx%d, want 100x30", columns, rows)
	}
	gotLive := make([]byte, len(live))
	if _, err := io.ReadFull(stream, gotLive); err != nil {
		t.Fatalf("ReadFull() live output error = %v", err)
	}
	if !bytes.Equal(gotLive, live) {
		t.Fatalf("live output = %q, want %q", gotLive, live)
	}
}

func TestTerminalRequestsShareAClientIdentity(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "daemon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatalf("Chmod() socket error = %v", err)
	}
	defer listener.Close()

	requests := make(chan protocol.Request, 2)
	go func() {
		if err := answerNegotiation(listener, protocol.Version, protocol.MinimumVersion); err != nil {
			return
		}
		attachment, err := listener.Accept()
		if err != nil {
			return
		}
		defer attachment.Close()
		var attach protocol.Request
		if err := protocol.Read(bufio.NewReader(attachment), &attach); err != nil {
			return
		}
		requests <- attach
		if err := protocol.Write(attachment, protocol.Response{Version: protocol.Version}); err != nil {
			return
		}

		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		var resize protocol.Request
		if err := protocol.Read(bufio.NewReader(connection), &resize); err != nil {
			return
		}
		requests <- resize
		_ = protocol.Write(connection, protocol.Response{Version: protocol.Version})
	}()

	backend := New(socket)
	stream, _, err := backend.OpenTerminal("tab-1")
	if err != nil {
		t.Fatalf("OpenTerminal() error = %v", err)
	}
	defer stream.Close()
	if err := backend.Resize("tab-1", 120, 40); err != nil {
		t.Fatalf("Resize() error = %v", err)
	}
	attach := <-requests
	resize := <-requests
	if attach.ClientID == "" || resize.ClientID != attach.ClientID {
		t.Fatalf("client IDs = attach %q, resize %q", attach.ClientID, resize.ClientID)
	}
}

func TestOpenTerminalFallsBackToALegacyReplayStream(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "daemon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatalf("Chmod() socket error = %v", err)
	}
	defer listener.Close()

	legacyReplay := []byte("legacy replay\r\n")
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		var ping protocol.Request
		if err := protocol.Read(bufio.NewReader(connection), &ping); err != nil {
			connection.Close()
			return
		}
		_ = protocol.Write(connection, protocol.Response{
			Version: 4,
			Error:   "this daemon speaks protocol 4 but the client speaks 5",
		})
		connection.Close()

		connection, err = listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		var attach protocol.Request
		if err := protocol.Read(bufio.NewReader(connection), &attach); err != nil {
			return
		}
		if attach.Version != 4 {
			return
		}
		if err := protocol.Write(connection, protocol.Response{Version: 4}); err != nil {
			return
		}
		_, _ = connection.Write(legacyReplay)
	}()

	stream, restored, err := New(socket).OpenTerminal("tab-1")
	if err != nil {
		t.Fatalf("OpenTerminal() error = %v", err)
	}
	defer stream.Close()
	if len(restored) != 0 {
		t.Fatalf("prepared replay = %q, want legacy output left on the stream", restored)
	}
	got := make([]byte, len(legacyReplay))
	if _, err := io.ReadFull(stream, got); err != nil {
		t.Fatalf("ReadFull() legacy replay error = %v", err)
	}
	if !bytes.Equal(got, legacyReplay) {
		t.Fatalf("legacy replay = %q, want %q", got, legacyReplay)
	}
}

func TestClientSelectsTheHighestProtocolSharedWithAFutureDaemon(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "daemon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatalf("Chmod() socket error = %v", err)
	}
	defer listener.Close()

	selected := make(chan int, 1)
	go func() {
		if err := answerNegotiation(listener, 8, 5); err != nil {
			return
		}
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		var request protocol.Request
		if err := protocol.Read(bufio.NewReader(connection), &request); err != nil {
			return
		}
		selected <- request.Version
		_ = protocol.Write(connection, protocol.Response{
			Version:  request.Version,
			Snapshot: &model.Snapshot{},
		})
	}()

	if _, err := New(socket).Snapshot(); err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if got := <-selected; got != protocol.Version {
		t.Fatalf("selected protocol = %d, want %d", got, protocol.Version)
	}
}

func TestClientUsesAnOlderDaemonSelectedProtocol(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "daemon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatalf("Chmod() socket error = %v", err)
	}
	defer listener.Close()

	selected := make(chan protocol.Request, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		var ping protocol.Request
		if err := protocol.Read(bufio.NewReader(connection), &ping); err != nil {
			connection.Close()
			return
		}
		_ = protocol.Write(connection, protocol.Response{
			Version: 1,
			Error:   "this daemon speaks protocol 1 but the client speaks 5",
		})
		connection.Close()

		connection, err = listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		var request protocol.Request
		if err := protocol.Read(bufio.NewReader(connection), &request); err != nil {
			return
		}
		selected <- request
		_ = protocol.Write(connection, protocol.Response{
			Version:  1,
			Snapshot: &model.Snapshot{},
		})
	}()

	if _, err := New(socket).Snapshot(); err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	request := <-selected
	if request.Version != 1 || len(request.Capabilities) != 0 {
		t.Fatalf("selected request = %#v, want protocol 1 without optional capabilities", request)
	}
}

// A socket that answers but names no protocol of its own is not a daemon this
// client can negotiate with. What it said is the only clue there is, and
// swallowing it leaves EnsureDaemon treating the socket as healthy.
func TestPingReportsAPeerThatNamesNoProtocol(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "daemon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatalf("Chmod() socket error = %v", err)
	}

	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		var request protocol.Request
		if err := protocol.Read(bufio.NewReader(connection), &request); err == nil {
			_ = protocol.Write(connection, protocol.Response{Error: "decode protocol message: unexpected end"})
		}
		connection.Close()
		listener.Close()
	}()

	err = New(socket).Ping()
	if err == nil || !strings.Contains(err.Error(), "decode protocol message") {
		t.Fatalf("Ping() error = %v, want the peer's own refusal", err)
	}
	if Unavailable(err) {
		t.Fatal("Ping() reported an answering socket as an absent daemon")
	}
}

// The daemon stamps a refusal with the version the client selected, so the
// client's version check does not refuse the very reply that is reporting the
// mismatch and hide the sentence naming the remedy.
func TestClientShowsADaemonRefusalForTheSelectedProtocol(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "daemon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatalf("Chmod() socket error = %v", err)
	}

	refusal := "daemon supports protocol 1..4 but the client selected 5; " + protocol.Remedy
	go func() {
		if err := answerNegotiation(listener, protocol.Version, protocol.MinimumVersion); err != nil {
			return
		}
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		listener.Close()
		var request protocol.Request
		if err := protocol.Read(bufio.NewReader(connection), &request); err != nil {
			return
		}
		_ = protocol.Write(connection, protocol.Response{Version: request.Version, Error: refusal})
	}()

	_, err = New(socket).Snapshot()
	if err == nil || !strings.Contains(err.Error(), refusal) {
		t.Fatalf("Snapshot() error = %v, want the daemon's refusal %q", err, refusal)
	}
}

// endsWithItsError delivers everything it has and reports the end of the
// stream in the same call, which io.Reader permits and a socket that is closed
// as its last bytes are read can produce.
type endsWithItsError struct{ data []byte }

func (r *endsWithItsError) Read(data []byte) (int, error) {
	count := copy(data, r.data)
	r.data = r.data[count:]
	if len(r.data) > 0 {
		return count, nil
	}
	return count, io.EOF
}

func TestReadReplayKeepsAReplayThatEndsWithItsError(t *testing.T) {
	connection, peer := net.Pipe()
	defer connection.Close()
	defer peer.Close()

	replay := make([]byte, len("restored history"))
	if err := readReplay(connection, &endsWithItsError{data: []byte("restored history")}, replay); err != nil {
		t.Fatalf("readReplay() error = %v, want the completed replay kept", err)
	}
	if string(replay) != "restored history" {
		t.Fatalf("replay = %q, want %q", replay, "restored history")
	}
}

func TestClientDegradesFeaturesMissingFromAnOlderDaemon(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "daemon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatalf("Chmod() socket error = %v", err)
	}

	go func() {
		_ = answerNegotiation(listener, 1, 1)
		listener.Close()
	}()
	backend := New(socket)
	agents, err := backend.Agents()
	if err != nil || len(agents) != 0 {
		t.Fatalf("Agents() = (%v, %v), want an empty supported fallback", agents, err)
	}
	statuses, err := backend.AgentStatuses()
	if err != nil || len(statuses) != 0 {
		t.Fatalf("AgentStatuses() = (%v, %v), want an empty supported fallback", statuses, err)
	}
	if _, err := backend.RemoveWorkspace("root-1", "/workspace"); err == nil ||
		!strings.Contains(err.Error(), "does not support") ||
		!strings.Contains(err.Error(), protocol.Remedy) {
		t.Fatalf("RemoveWorkspace() error = %v, want an unsupported capability and its remedy", err)
	}
}

// A mismatch is only useful to a user who is told what to do about it, and
// romty stop is what every one of them resolves to.
func TestNegotiationFailureNamesItsRemedy(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "daemon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatalf("Chmod() socket error = %v", err)
	}

	go func() {
		_ = answerNegotiation(listener, protocol.Version+3, protocol.Version+1)
		listener.Close()
	}()
	_, err = New(socket).Snapshot()
	if err == nil {
		t.Fatal("Snapshot() succeeded against a daemon with no shared protocol")
	}
	if !strings.Contains(err.Error(), protocol.Remedy) {
		t.Fatalf("Snapshot() error = %v, want it to name %q", err, protocol.Remedy)
	}
}

func TestOpenTerminalGivesUpWhenReplayStopsMakingProgress(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "daemon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatalf("Chmod() socket error = %v", err)
	}
	defer listener.Close()

	release := make(chan struct{})
	go func() {
		if err := answerNegotiation(listener, protocol.Version, protocol.MinimumVersion); err != nil {
			return
		}
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		var request protocol.Request
		if err := protocol.Read(reader, &request); err != nil {
			return
		}
		if err := protocol.Write(connection, protocol.Response{
			Version:     protocol.Version,
			ReplayBytes: 1024,
		}); err != nil {
			return
		}
		if _, err := connection.Write([]byte("partial")); err != nil {
			return
		}
		<-release
	}()
	t.Cleanup(func() { close(release) })

	previous := replayReadTimeout
	replayReadTimeout = 200 * time.Millisecond
	t.Cleanup(func() { replayReadTimeout = previous })

	if _, _, err := New(socket).OpenTerminal("tab-1"); err == nil {
		t.Fatal("OpenTerminal() waited forever for an incomplete replay")
	}
}

func answerNegotiation(listener net.Listener, maximum, minimum int) error {
	connection, err := listener.Accept()
	if err != nil {
		return err
	}
	defer connection.Close()
	reader := bufio.NewReader(connection)
	var request protocol.Request
	if err := protocol.Read(reader, &request); err != nil {
		return err
	}
	return protocol.Write(connection, protocol.Response{
		Version:      request.Version,
		MinVersion:   minimum,
		MaxVersion:   maximum,
		Capabilities: protocol.CapabilitiesForVersion(min(maximum, protocol.Version)),
	})
}

// shortTempDir keeps the socket path within the portable 104-byte limit, which
// the per-user temporary directory on macOS does not leave room for.
func shortTempDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "romty-client-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	return directory
}
