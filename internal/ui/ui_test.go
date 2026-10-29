package ui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/sound"
)

func TestMain(m *testing.M) {
	agentAnimationInterval = 0
	agentRefreshInterval = 0
	playSound = func(sound.Kind) error { return nil }
	os.Exit(m.Run())
}

type fakeBackend struct {
	snapshot               model.Snapshot
	agentStatuses          map[string]model.AgentStatus
	workspace              model.Workspace
	createdTab             model.Tab
	createdColumns         uint16
	createdRows            uint16
	createCount            int
	reopenedID             string
	closedTabID            string
	renamedTabID           string
	renamedTabName         string
	movedTabID             string
	movedOffset            int
	daemonInfo             model.DaemonInfo
	events                 model.EventStream
	gitRefreshes           []bool
	addedPath              string
	removedRootID          string
	removedWorkspaceRootID string
	removedWorkspacePath   string
	ensuredRootID          string
	ensuredPath            string
	openedTab              string
	snapshotCount          int
	shutdownCount          int
	stream                 *memoryStream
	// The real client fails: the daemon can be unreachable, past its
	// deadline, or refuse the request. A fake that only ever succeeds hides
	// every branch that handles those.
	failures map[string]error
}

type memoryStream struct {
	reader   *bytes.Reader
	mu       sync.Mutex
	written  bytes.Buffer
	writeErr error
	closed   bool
}

func newMemoryStream(output string) *memoryStream {
	return &memoryStream{reader: bytes.NewReader([]byte(output))}
}

func (s *memoryStream) Read(data []byte) (int, error) {
	return s.reader.Read(data)
}

func (s *memoryStream) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return s.written.Write(data)
}

func (s *memoryStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *memoryStream) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *memoryStream) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.written.String()
}

var _ io.ReadWriteCloser = (*memoryStream)(nil)

// fail makes the named method return err until cleared, the way a daemon that
// went away makes every call fail.
func (f *fakeBackend) fail(method string, err error) {
	if f.failures == nil {
		f.failures = make(map[string]error)
	}
	f.failures[method] = err
}

func (f *fakeBackend) failure(method string) error {
	return f.failures[method]
}

func (f *fakeBackend) AddRoot(path string) (model.Snapshot, error) {
	f.addedPath = path
	if err := f.failure("AddRoot"); err != nil {
		return model.Snapshot{}, err
	}
	return f.snapshot, nil
}

func (f *fakeBackend) RemoveRoot(rootID string) (model.Snapshot, error) {
	f.removedRootID = rootID
	if err := f.failure("RemoveRoot"); err != nil {
		return model.Snapshot{}, err
	}
	remaining := make([]model.RootView, 0, len(f.snapshot.Roots))
	for _, root := range f.snapshot.Roots {
		if root.Root.ID != rootID {
			remaining = append(remaining, root)
		}
	}
	f.snapshot.Roots = remaining
	return f.snapshot, nil
}

func (f *fakeBackend) RemoveWorkspace(rootID, path string) (model.Snapshot, error) {
	f.removedWorkspaceRootID = rootID
	f.removedWorkspacePath = path
	if err := f.failure("RemoveWorkspace"); err != nil {
		return model.Snapshot{}, err
	}
	for rootIndex := range f.snapshot.Roots {
		root := &f.snapshot.Roots[rootIndex]
		if root.Root.ID != rootID {
			continue
		}
		remaining := make([]model.WorkspaceView, 0, len(root.Directories))
		for _, directory := range root.Directories {
			if directory.Workspace.Path != path {
				remaining = append(remaining, directory)
			}
		}
		root.Directories = remaining
	}
	return f.snapshot, nil
}

func (f *fakeBackend) Snapshot() (model.Snapshot, error) {
	f.snapshotCount++
	if err := f.failure("Snapshot"); err != nil {
		return model.Snapshot{}, err
	}
	return f.snapshot, nil
}

func (f *fakeBackend) AgentStatuses() (map[string]model.AgentStatus, error) {
	if err := f.failure("AgentStatuses"); err != nil {
		return nil, err
	}
	return f.agentStatuses, nil
}

func (f *fakeBackend) EnsureWorkspace(rootID, path string) (model.Workspace, error) {
	f.ensuredRootID = rootID
	f.ensuredPath = path
	if err := f.failure("EnsureWorkspace"); err != nil {
		return model.Workspace{}, err
	}
	return f.workspace, nil
}

func (f *fakeBackend) CreateTab(workspaceID string, columns, rows uint16) (model.Tab, error) {
	f.createCount++
	f.createdColumns = columns
	f.createdRows = rows
	if err := f.failure("CreateTab"); err != nil {
		return model.Tab{}, err
	}
	for rootIndex := range f.snapshot.Roots {
		root := &f.snapshot.Roots[rootIndex]
		if f.workspace.ID == workspaceID && f.workspace.Path == root.Root.Path {
			root.Tabs = append(root.Tabs, f.createdTab)
			break
		}
		for directoryIndex := range root.Directories {
			directory := &root.Directories[directoryIndex]
			if directory.Workspace.ID == workspaceID {
				directory.Tabs = append(directory.Tabs, f.createdTab)
				break
			}
		}
	}
	return f.createdTab, nil
}

func (f *fakeBackend) MoveTab(tabID string, offset int) (model.Snapshot, error) {
	f.movedTabID, f.movedOffset = tabID, offset
	if err := f.failure("MoveTab"); err != nil {
		return model.Snapshot{}, err
	}
	return f.snapshot, nil
}

// Subscribe answers as a daemon from before subscriptions, so the fixtures
// poll the way they always have; events_test.go uses a stream of its own.
func (f *fakeBackend) Subscribe() (model.EventStream, error) {
	if f.events != nil {
		return f.events, nil
	}
	return nil, model.ErrNeedsNewerDaemon
}

func (f *fakeBackend) RefreshGit(fetch bool) error {
	f.gitRefreshes = append(f.gitRefreshes, fetch)
	return f.failure("RefreshGit")
}

func (f *fakeBackend) DaemonInfo() (model.DaemonInfo, error) {
	return f.daemonInfo, f.failure("DaemonInfo")
}

func (f *fakeBackend) RenameTab(tabID, name string) (model.Snapshot, error) {
	f.renamedTabID, f.renamedTabName = tabID, name
	if err := f.failure("RenameTab"); err != nil {
		return model.Snapshot{}, err
	}
	for tab := range f.snapshot.Tabs() {
		if tab.ID == tabID {
			tab.Name = name
		}
	}
	f.snapshot.Revision++
	return f.snapshot, nil
}

// chooseNewSession opens the highlighted workspace, which has no running
// terminal. Without closed sessions Enter starts a new one at once; with them
// it asks first, and New session is the choice's first row.
func chooseNewSession(t *testing.T, value *dashboard) tea.Cmd {
	t.Helper()
	item, ok := value.navigationItem()
	if !ok {
		t.Fatal("no workspace under the cursor")
	}
	command := send(value, key(tea.KeyEnter, ""))
	if len(item.closed) == 0 {
		if value.modal != noModal {
			t.Fatalf("a workspace with nothing to reopen opened modal %v", value.modal)
		}
		return command
	}
	if command != nil {
		t.Fatalf("selecting a workspace without a terminal ran %T before a choice", command())
	}
	if value.modal != workspaceActionsModal || !value.palette.sessionChoice {
		t.Fatalf("selecting a workspace without a terminal opened modal %v, want the session choice", value.modal)
	}
	return send(value, key(tea.KeyEnter, ""))
}

func (f *fakeBackend) ReopenTab(workspaceID, closedID string, columns, rows uint16) (model.Tab, error) {
	f.reopenedID = closedID
	return f.CreateTab(workspaceID, columns, rows)
}

func (f *fakeBackend) CloseTab(tabID string) (model.Snapshot, error) {
	f.closedTabID = tabID
	if err := f.failure("CloseTab"); err != nil {
		return model.Snapshot{}, err
	}
	for rootIndex := range f.snapshot.Roots {
		root := &f.snapshot.Roots[rootIndex]
		root.Tabs = removeTabForTest(root.Tabs, tabID)
		for directoryIndex := range root.Directories {
			directory := &root.Directories[directoryIndex]
			directory.Tabs = removeTabForTest(directory.Tabs, tabID)
		}
	}
	return f.snapshot, nil
}

func removeTabForTest(tabs []model.Tab, tabID string) []model.Tab {
	result := make([]model.Tab, 0, len(tabs))
	for _, tab := range tabs {
		if tab.ID != tabID {
			result = append(result, tab)
		}
	}
	return result
}

func (f *fakeBackend) OpenTerminal(tabID string) (io.ReadWriteCloser, []byte, error) {
	f.openedTab = tabID
	if err := f.failure("OpenTerminal"); err != nil {
		return nil, nil, err
	}
	f.stream = newMemoryStream("live output")
	return f.stream, []byte("\x1b[2J\x1b[Hembedded terminal"), nil
}

func (f *fakeBackend) Resize(_ string, columns, rows uint16) error {
	f.createdColumns = columns
	f.createdRows = rows
	return f.failure("Resize")
}

func (f *fakeBackend) Shutdown() error {
	f.shutdownCount++
	if err := f.failure("Shutdown"); err != nil {
		return err
	}
	return nil
}

func TestDashboardRejectsSnapshotsThatFinishOutOfOrder(t *testing.T) {
	tree := func(revision uint64, name string) model.Snapshot {
		return model.Snapshot{Revision: revision, Roots: []model.RootView{{
			Root: model.Root{ID: "root-1", Name: name, Path: "/projects"},
		}}}
	}

	value := newDashboard(&fakeBackend{}, tree(1, "initial"))
	send(&value, snapshotMsg{value: tree(2, "newest"), order: 2})
	send(&value, snapshotMsg{value: tree(1, "older revision"), order: 3})
	send(&value, snapshotMsg{value: tree(2, "older request"), order: 1})

	if got := value.state.Roots[0].Root.Name; got != "newest" {
		t.Fatalf("root after out-of-order snapshots = %q, want newest", got)
	}
}

func TestDashboardRecognisesCtrlShiftTAcrossInputLayouts(t *testing.T) {
	snapshot, _ := alphaWorkspaceSnapshot(nil)
	value := newDashboard(&fakeBackend{}, snapshot)
	value.setNavigation(1)
	physicalT := tea.KeyPressMsg(tea.Key{
		Code: 'ㅅ', BaseCode: 't', Mod: tea.ModCtrl | tea.ModShift,
	})

	_, command := value.Update(physicalT)
	if command == nil {
		t.Fatal("Ctrl+Shift+T with a Korean layout produced no tab command")
	}
}

func TestDashboardConfirmationActionsAreClickable(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 100, 24
	send(&value, key(tea.KeyF9, ""))

	plain := ansi.Strip(value.render())
	for _, label := range []string{"stop daemon", "cancel"} {
		if !strings.Contains(plain, label) {
			t.Fatalf("confirmation dialog has no clickable %q action:\n%s", label, plain)
		}
	}
	hits := value.modalActionHits(value.modalGeometry(value.width, value.dimensions().bodyHeight))
	if len(hits) != 2 {
		t.Fatalf("modal action hits = %#v, want confirm and cancel", hits)
	}
	confirm := hits[0]
	command := send(&value, tea.MouseClickMsg{
		X: (confirm.left + confirm.right) / 2, Y: confirm.row, Button: tea.MouseLeft,
	})
	if !value.shutdownPending || command == nil {
		t.Fatalf("confirm click = (pending %v, command %v)", value.shutdownPending, command)
	}

	value = newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 100, 24
	send(&value, key(tea.KeyF9, ""))
	hits = value.modalActionHits(value.modalGeometry(value.width, value.dimensions().bodyHeight))
	cancel := hits[1]
	command = send(&value, tea.MouseClickMsg{
		X: (cancel.left + cancel.right) / 2, Y: cancel.row, Button: tea.MouseLeft,
	})
	if value.modal != noModal || command != nil {
		t.Fatalf("cancel click = (modal %v, command %v)", value.modal, command)
	}
}

func TestDashboardAnimatesVisiblePendingWork(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.tabPending = true

	command := send(&value, struct{}{})
	if !value.agentAnimationPending || command == nil {
		t.Fatalf("pending tab animation = (scheduled %v, command %v)", value.agentAnimationPending, command)
	}
	status := ansi.Strip(value.renderStatus(100, value.dimensions().bodyHeight)[1])
	if !strings.Contains(status, "OPENING") || !strings.Contains(status, agentAnimationFrames[0]) {
		t.Fatalf("pending tab status = %q", status)
	}

	send(&value, agentAnimationMsg{})
	if value.agentAnimationFrame != 1 || !value.agentAnimationPending {
		t.Fatalf("pending animation = (frame %d, scheduled %v)", value.agentAnimationFrame, value.agentAnimationPending)
	}
}

func TestDashboardAnimatesDirectoryReads(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.modal = browseModal
	value.browse = browser{path: "/projects", loading: true}
	command := send(&value, struct{}{})
	if command == nil || !value.agentAnimationPending {
		t.Fatal("directory read did not schedule its activity indicator")
	}
	plain := ansi.Strip(strings.Join(value.renderBrowseModal(80, 20), "\n"))
	if !strings.Contains(plain, agentAnimationFrames[0]+" Reading") {
		t.Fatalf("directory activity = %q", plain)
	}
}

// waitForGuest blocks until the guest's stream holds want, so tests do not
// race the goroutine that drains the emulator's input pipe. A fixed sleep is
// long enough on an idle machine and too short under -race.
func waitForGuest(t *testing.T, stream *memoryStream, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stream.String(), want) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("guest received %q, want it to contain %q", stream.String(), want)
}

// guest is the stream the dashboard's open terminal writes guest input to.
func guest(value dashboard) *memoryStream {
	return value.terminal.stream.(*memoryStream)
}

// waitForGuestSilence puts a marker behind the event under test. Seeing that
// marker proves the copy goroutine processed everything before it.
func waitForGuestSilence(t *testing.T, terminal *embeddedTerminal, was string) string {
	t.Helper()
	stream := terminal.stream.(*memoryStream)
	marker := fmt.Sprintf("__romty_test_barrier_%d__", len(was))
	if _, err := io.WriteString(terminal.emulator.InputPipe(), marker); err != nil {
		t.Fatalf("write barrier: %v", err)
	}
	want := was + marker
	waitForGuest(t, stream, want)
	if now := stream.String(); now != want {
		t.Fatalf("guest received %q before barrier, want %q", now, want)
	}
	return want
}

// plainRows drops styling and trailing padding so viewport rows can be compared
// whether they are rendered from the live screen or from the scrollback.
func plainRows(lines []string) []string {
	rows := make([]string, 0, len(lines))
	for _, line := range lines {
		rows = append(rows, strings.TrimRight(ansi.Strip(line), " "))
	}
	return rows
}

// terminalDashboard returns a 120x30 dashboard focused on an empty terminal
// that fills the right pane.
func terminalDashboard(t *testing.T) dashboard {
	t.Helper()
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 120, 30
	view := value.dimensions()
	value.terminal = newEmbeddedTerminal("tab-1", newMemoryStream(""), view.rightWidth, view.terminalHeight)
	t.Cleanup(value.closeTerminal)
	value.focus = terminalPane
	return value
}

// scrolledDashboard returns a dashboard whose terminal holds `lines` rows of
// numbered output, all but the last screen of which sits in the scrollback.
func scrolledDashboard(t *testing.T, lines int) dashboard {
	t.Helper()
	value := terminalDashboard(t)
	var output strings.Builder
	for index := range lines {
		fmt.Fprintf(&output, "line-%03d\r\n", index)
	}
	value.terminal.writeOutput([]byte(output.String()))
	if value.terminal.scrollbackLen() == 0 {
		t.Fatalf("terminal produced no scrollback for %d lines at height %d", lines, value.dimensions().terminalHeight)
	}
	return value
}

// rawSequences runs a command the way the runtime does, flattening batches, and
// returns the escape sequences it asked the host terminal for.
func rawSequences(command tea.Cmd) []string {
	if command == nil {
		return nil
	}
	var sequences []string
	switch message := command().(type) {
	case tea.RawMsg:
		sequences = append(sequences, fmt.Sprint(message.Msg))
	case tea.BatchMsg:
		for _, batched := range message {
			sequences = append(sequences, rawSequences(batched)...)
		}
	}
	return sequences
}

func commandMessages(command tea.Cmd) []tea.Msg {
	if command == nil {
		return nil
	}
	message := command()
	batch, ok := message.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{message}
	}
	var messages []tea.Msg
	for _, batched := range batch {
		messages = append(messages, commandMessages(batched)...)
	}
	return messages
}

func commandMessage[T any](t *testing.T, command tea.Cmd) T {
	t.Helper()
	for _, message := range commandMessages(command) {
		if typed, ok := message.(T); ok {
			return typed
		}
	}
	var zero T
	t.Fatalf("command has no %T message", zero)
	return zero
}

func playedSound(command tea.Cmd) (sound.Kind, bool) {
	for _, message := range commandMessages(command) {
		if played, ok := message.(soundPlayedMsg); ok {
			return played.kind, true
		}
	}
	return "", false
}

// configRowReads reports whether the Config modal draws a setting with the
// value given. Name and value sit in columns of their own on one row.
func configRowReads(rendered, name, value string) bool {
	for line := range strings.SplitSeq(rendered, "\n") {
		_, after, ok := strings.Cut(line, name)
		if !ok {
			continue
		}
		if strings.Contains(after, value) {
			return true
		}
	}
	return false
}

// romty asks the host for all motion so it can draw its own hover highlights.
// A guest that asked only for clicks must not be handed those extra reports.
func TestDashboardWithholdsUnrequestedMotionFromTheGuest(t *testing.T) {
	value := scrolledDashboard(t, 200)
	// ?1000h is click tracking: button press and release, no motion.
	value.terminal.writeOutput([]byte("\x1b[?1000h\x1b[?1006h"))
	value.config.MousePassthrough = true
	if value.terminal.guestMouseMode() != tea.MouseModeCellMotion {
		t.Fatalf("guest mouse mode = %v, want click tracking", value.terminal.guestMouseMode())
	}
	if !value.guestOwnsMouse() {
		t.Fatal("passthrough did not hand the mouse to the guest")
	}

	x, y := value.dimensions().leftWidth+3+4, terminalTop+2
	value.Update(tea.MouseMotionMsg{X: x, Y: y})
	sent := waitForGuestSilence(t, value.terminal, "")

	// The buttons the guest did ask for still reach it.
	value.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	sent += "\x1b[<0;5;3M"
	waitForGuest(t, guest(value), sent)

	// ?1002h is button-event tracking: motion, but only while a button is held.
	value.terminal.writeOutput([]byte("\x1b[?1002h"))
	value.Update(tea.MouseMotionMsg{X: x, Y: y})
	sent = waitForGuestSilence(t, value.terminal, sent)
	value.Update(tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseLeft})
	waitForGuest(t, guest(value), sent+"\x1b[<32;5;3M")
}

func TestDashboardRequestsAlternateKeys(t *testing.T) {
	view := newDashboard(&fakeBackend{}, model.Snapshot{}).View()
	if !view.KeyboardEnhancements.ReportAlternateKeys {
		t.Fatal("dashboard did not request physical base key codes")
	}
}

// A guest that owns the screen pages its own output, so romty must hand the key
// over rather than swallowing it for a scrollback it cannot offer.
func TestDashboardForwardsPagingToTheGuestThatOwnsTheScreen(t *testing.T) {
	value := scrolledDashboard(t, 200)
	value.terminal.writeOutput([]byte("\x1b[?1049h"))

	value.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModShift}))
	// Sent unmodified: plain PgUp is the key such applications bind.
	waitForGuest(t, guest(value), "\x1b[5~")

	// On the main screen it still belongs to romty's own scrollback.
	value.terminal.writeOutput([]byte("\x1b[?1049l"))
	send(&value, tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp, Mod: tea.ModShift}))
	if !value.scrollback || value.scrollOffset == 0 {
		t.Fatalf("Shift+PgUp on the main screen = (scrollback %v, offset %d), want a page of history",
			value.scrollback, value.scrollOffset)
	}
}

// F7 and Ctrl+/ both move between panes in one key. Keeping both bindings
// matters on systems where a desktop environment holds one of them globally,
// and Ctrl+/ arrives under two names because only a terminal that speaks the
// Kitty keyboard protocol reports the key that was pressed rather than 0x1F.
func TestDashboardSwitchesPanesWithOneKey(t *testing.T) {
	for _, binding := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{name: "F7", key: key(tea.KeyF7, "")},
		{name: "Ctrl+/", key: controlSlashKey()},
		{name: "Ctrl+/ as 0x1F", key: controlUnderscoreKey()},
		// Ctrl+\ is an unadvertised alias for the same toggle, including on a
		// layout that reports the physical key separately from the character
		// it types.
		{name: "Ctrl+\\", key: controlBackslashKey()},
		{name: "Ctrl+\\ with an alternate layout", key: tea.KeyPressMsg(tea.Key{
			Code: '₩', BaseCode: '\\', Mod: tea.ModCtrl,
		})},
	} {
		t.Run(binding.name, func(t *testing.T) {
			value := scrolledDashboard(t, 200)
			command := send(&value, binding.key)
			if value.focus != leftPane || command == nil {
				t.Fatalf("terminal -> workspace = (focus %v, command %v), want a refreshed workspace", value.focus, command)
			}

			send(&value, binding.key)
			if value.focus != terminalPane {
				t.Fatalf("workspace -> terminal = focus %v, want the terminal", value.focus)
			}
		})
	}

	// Scrollback covers the terminal, so switching away from the terminal has
	// to leave it rather than land the keyboard behind the full-width view.
	value := scrolledDashboard(t, 200)
	send(&value, key(tea.KeyF6, ""))
	if !value.scrollback {
		t.Fatal("F6 did not open scrollback")
	}
	send(&value, key(tea.KeyF7, ""))
	if value.scrollback || value.focus != leftPane {
		t.Fatalf("F7 in scrollback = (scrollback %v, focus %v), want the workspace pane",
			value.scrollback, value.focus)
	}

	// Ctrl+/ is the one romty teaches. Ctrl+\ answers the same toggle without
	// taking a line of help or a keycap on the status bar for itself.
	value = scrolledDashboard(t, 200)
	keycap := value.styles.shortcutKey.Render(" Ctrl+\\ ")
	if help := strings.Join(value.helpEntries(), "\n"); strings.Contains(help, keycap) {
		t.Fatalf("help advertises the hidden Ctrl+\\ alias:\n%s", ansi.Strip(help))
	}
	if rendered := value.render(); strings.Contains(rendered, keycap) {
		t.Fatalf("status bar advertises the hidden Ctrl+\\ alias:\n%s", ansi.Strip(rendered))
	}
}

// narrowDashboard is a phone-sized dashboard with one workspace in the tree and
// a terminal open on it, so a render can be searched for the tree by name.
func narrowDashboard(t *testing.T, width int) dashboard {
	t.Helper()
	snapshot, workspace := alphaWorkspaceSnapshot(nil)
	value := newDashboard(&fakeBackend{snapshot: snapshot, workspace: workspace}, snapshot)
	value.width, value.height = width, 24
	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path
	value.focus = terminalPane
	view := value.dimensions()
	value.terminal = newEmbeddedTerminal("tab-1", newMemoryStream(""), view.rightWidth, view.terminalHeight)
	// The dashboard is settled rather than mid-transition, so the PTY has
	// already been told whichever width this layout gives it.
	value.terminalFullWidth = value.navigationHidden()
	t.Cleanup(value.closeTerminal)
	return value
}

// exitedDashboard opens a terminal on a workspace holding the given tabs, then
// ends its stream the way a shell exiting does, and returns the dashboard plus
// the refresh command it asked for.
func exitedDashboard(t *testing.T, tabs []model.Tab, open int) (dashboard, *fakeBackend, tea.Cmd) {
	t.Helper()
	snapshot, workspace := alphaWorkspaceSnapshot(tabs)
	backend := &fakeBackend{snapshot: snapshot, workspace: workspace}
	value := newDashboard(backend, snapshot)
	value.width = 120
	value.height = 40
	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path
	value.setNavigation(1)
	value.tabIndex = open
	value.focus = terminalPane
	value.terminal = newEmbeddedTerminal(tabs[open].ID, newMemoryStream(""), 89, 36)
	t.Cleanup(value.closeTerminal)

	refresh := send(&value, terminalOutputMsg{terminal: value.terminal, err: io.EOF})
	if value.terminal != nil {
		t.Fatal("the exited terminal is still attached")
	}
	if refresh == nil {
		t.Fatal("an exited terminal did not ask for a fresh snapshot")
	}
	return value, backend, refresh
}

// The move off an exited terminal is deferred until a snapshot says where to
// go. If that snapshot never arrives the move must be settled, not left armed
// to fire on whatever refresh comes next.
func TestDashboardSettlesAnExitWhenTheSnapshotFails(t *testing.T) {
	tabs := openTabs(2)
	value, backend, _ := exitedDashboard(t, tabs, 0)

	send(&value, snapshotMsg{err: errors.New("connect to daemon: no such file")})
	if value.terminalExited || value.focus != leftPane {
		t.Fatalf("failed refresh = (pending %v, focus %v), want the exit settled on the workspace pane",
			value.terminalExited, value.focus)
	}
	if !strings.Contains(value.errorMessage, "connect to daemon") {
		t.Fatalf("error message = %q, want the refresh failure", value.errorMessage)
	}

	// A healthy terminal opened afterwards must survive an ordinary refresh.
	value.terminal = newEmbeddedTerminal("tab-2", newMemoryStream(""), 89, 36)
	t.Cleanup(value.closeTerminal)
	value.focus = terminalPane
	value.tabIndex = 1
	command := send(&value, snapshotMsg{value: backend.snapshot})
	if command != nil || value.terminal == nil {
		t.Fatalf("later refresh = (command %v, terminal %v), want the open terminal left alone",
			command, value.terminal != nil)
	}
}

// Every user-visible failure the backend can produce has to reach the status
// bar and leave the dashboard usable.
func TestDashboardReportsBackendFailures(t *testing.T) {
	snapshot, workspace := alphaWorkspaceSnapshot(nil)

	for _, probe := range []struct {
		name   string
		method string
		act    func(*testing.T, dashboard) (tea.Model, tea.Cmd)
	}{
		{
			name:   "refresh",
			method: "Snapshot",
			act:    func(_ *testing.T, m dashboard) (tea.Model, tea.Cmd) { return m.Update(key(tea.KeyF5, "")) },
		},
		{
			name:   "opening a workspace",
			method: "EnsureWorkspace",
			act: func(t *testing.T, m dashboard) (tea.Model, tea.Cmd) {
				command := chooseNewSession(t, &m)
				return m, command
			},
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			backend := &fakeBackend{snapshot: snapshot, workspace: workspace}
			backend.fail(probe.method, errors.New("daemon: "+probe.method+" refused"))
			value := newDashboard(backend, snapshot)
			value.width = 120
			value.height = 40
			value.setNavigation(1)

			updated, command := probe.act(t, value)
			value = updated.(dashboard)
			if command == nil {
				t.Fatalf("%s produced no command to fail", probe.name)
			}
			for _, message := range commandMessages(command) {
				send(&value, message)
			}
			if !strings.Contains(value.errorMessage, probe.method+" refused") {
				t.Fatalf("error message = %q, want the %s failure", value.errorMessage, probe.method)
			}
			if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "ERROR") {
				t.Fatalf("the status bar does not show the failure:\n%s", rendered)
			}
			// The tree still renders and the keyboard still works.
			if !strings.Contains(ansi.Strip(value.render()), "alpha") {
				t.Fatalf("the workspace tree is gone after a failure:\n%s", ansi.Strip(value.render()))
			}
		})
	}
}

func twoRootSnapshot() model.Snapshot {
	return model.Snapshot{Roots: []model.RootView{
		{Root: model.Root{ID: "root-1", Name: "first", Path: "/first"}, Directories: []model.WorkspaceView{}},
		{Root: model.Root{ID: "root-2", Name: "second", Path: "/second"}, Directories: []model.WorkspaceView{}},
	}}
}

// Unbound letters always belong to the shell in the terminal pane.
func TestDashboardSendsUnboundLettersToTheShell(t *testing.T) {
	value := scrolledDashboard(t, 200)

	for _, letter := range []rune{'a', 'q', 'r', 's', 'd', 't'} {
		send(&value, key(letter, string(letter)))
		if value.scrollback || value.modal != noModal {
			t.Fatalf("%q in the terminal = (scrollback %v, modal %v), want it forwarded to the shell",
				letter, value.scrollback, value.modal)
		}
	}
}

// romty takes F1-F7 from the shell and stops there. A full-screen program
// binds the whole function key row: htop puts Kill on F9 and answers it with
// Enter, which is the same Enter that confirms stopping the daemon. Taking F9
// would turn that habit into every session dying at once, and F8 is Delete in
// the file managers this layout comes from.
func TestDashboardLeavesTheHighFunctionKeysToTheShell(t *testing.T) {
	snapshot := twoRootSnapshot()
	value := newDashboard(&fakeBackend{snapshot: snapshot}, snapshot)
	value.width, value.height = 120, 30
	value.terminal = newEmbeddedTerminal("tab-1", newMemoryStream(""), 60, 10)
	t.Cleanup(value.closeTerminal)
	value.focus = terminalPane

	for _, code := range []rune{tea.KeyF8, tea.KeyF9} {
		command := send(&value, key(code, ""))
		if value.modal != noModal || command != nil {
			t.Fatalf("%v in the terminal = (modal %v, command %v), want it forwarded to the shell",
				code, value.modal, command)
		}
	}

	// The workspace pane still answers them, next to their letters.
	value.focus = leftPane
	send(&value, key(tea.KeyF8, ""))
	if value.modal != workspaceActionsModal {
		t.Fatalf("F8 in the workspace = modal %v, want workspace actions", value.modal)
	}
	send(&value, key(tea.KeyEscape, ""))
	send(&value, key(tea.KeyF9, ""))
	if value.modal != shutdownModal {
		t.Fatalf("F9 in the workspace = modal %v, want the shutdown confirmation", value.modal)
	}
}

// cursorItem is the tree item the removal confirmation is opened about, which
// is what the workspace action palette captures when it opens over one.
func cursorItem(t *testing.T, value dashboard) navItem {
	t.Helper()
	item, ok := value.navigationItem()
	if !ok {
		t.Fatal("the cursor is on no tree item to remove")
	}
	return item
}

// The terminal's origin, the right pane's width and the mouse translation all
// assume the separator and tab bar are exactly as wide and tall as drawn. That
// agreement is not something the compiler can check, so check it here: change
// either and the cursor and the mouse silently point at the wrong cell.
func TestLayoutMatchesWhatIsRendered(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 120
	value.height = 40

	head, body := value.paneSeparators()
	for name, separator := range map[string]string{"first row": head, "other rows": body} {
		if width := lipgloss.Width(ansi.Strip(separator)); width != separatorWidth {
			t.Fatalf("the %s separator is %d cells wide, but the layout assumes %d",
				name, width, separatorWidth)
		}
	}

	bar := renderTabBar(value.styles, nil, 0, 40)
	if len(bar) != terminalTop {
		t.Fatalf("the tab bar is %d rows tall, but terminalTop is %d", len(bar), terminalTop)
	}

	// The origin has to be where the terminal's first cell actually lands.
	view := value.dimensions()
	originX, originY := view.terminalOrigin()
	if originX != view.leftWidth+separatorWidth || originY != terminalTop {
		t.Fatalf("terminal origin = (%d,%d), want (%d,%d)",
			originX, originY, view.leftWidth+separatorWidth, terminalTop)
	}
	if view.leftWidth+separatorWidth+view.rightWidth != value.width {
		t.Fatalf("panes and separator cover %d columns, want the full %d",
			view.leftWidth+separatorWidth+view.rightWidth, value.width)
	}
}

// The status bar has one slot. A refresh clears only the failures it answers,
// so the F5 a user presses right after another failure keeps the reason.
func TestDashboardKeepsErrorsARefreshDoesNotAnswer(t *testing.T) {
	snapshot := projectsSnapshot([]model.WorkspaceView{{Workspace: model.Workspace{ID: "w", Name: "alpha", Path: "/projects/alpha"}}})

	for _, probe := range []struct {
		name     string
		raise    func(dashboard) dashboard
		survives bool
	}{
		{
			name: "a failed resize",
			raise: func(m dashboard) dashboard {
				// A resize failure names its tab, so the terminal it is about
				// has to be the one on screen.
				m.terminal = newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)
				t.Cleanup(m.closeTerminal)
				send(&m, resizeFailedMsg{
					tabID: "tab-1",
					err:   errors.New("terminal session not found"),
				})
				return m
			},
			survives: true,
		},
		{
			name: "a daemon that would not stop",
			raise: func(m dashboard) dashboard {
				send(&m, daemonStoppedMsg{err: errors.New("connection reset")})
				return m
			},
			survives: true,
		},
		{
			name: "config that could not be written",
			raise: func(m dashboard) dashboard {
				send(&m, configSavedMsg{err: errors.New("read-only file system")})
				return m
			},
			survives: true,
		},
		{
			name: "a snapshot that failed",
			raise: func(m dashboard) dashboard {
				send(&m, snapshotMsg{err: errors.New("daemon: read root")})
				return m
			},
			survives: false,
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			value := newDashboard(&fakeBackend{snapshot: snapshot}, snapshot)
			value.width = 120
			value.height = 40
			value = probe.raise(value)
			raised := value.errorMessage
			if raised == "" {
				t.Fatal("nothing was raised")
			}

			send(&value, snapshotMsg{value: snapshot})
			if probe.survives && value.errorMessage != raised {
				t.Fatalf("a refresh erased %q, which it does not answer", raised)
			}
			if !probe.survives && value.errorMessage != "" {
				t.Fatalf("a refresh left %q standing, which it does answer", value.errorMessage)
			}
		})
	}
}

func TestDashboardKeepsGlobalKeysAtOnePrecedence(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.errorMessage = "boom"

	// Every function key works with a modal open, and each clears a stale error.
	for _, step := range []struct {
		message tea.KeyPressMsg
		want    modal
	}{
		{message: key(tea.KeyF1, ""), want: helpModal},
		{message: key(tea.KeyF3, ""), want: configModal},
		{message: key(tea.KeyF1, ""), want: helpModal},
	} {
		command := send(&value, step.message)
		if value.modal != step.want || command != nil || value.errorMessage != "" {
			t.Fatalf("%q with a modal open = (modal %v, command %v, error %q), want modal %v",
				step.message.String(), value.modal, command, value.errorMessage, step.want)
		}
	}
	if rendered := ansi.Strip(value.render()); strings.Contains(rendered, "ERROR") || !strings.Contains(rendered, "Esc") {
		t.Fatalf("status bar keeps the stale error instead of the modal hint:\n%s", rendered)
	}

	send(&value, key(tea.KeyF2, ""))
	if value.modal != browseModal {
		t.Fatalf("F2 with a modal open = modal %v, want the root picker", value.modal)
	}
	send(&value, key('/', "/"))
	if value.modal != inputModal {
		t.Fatalf("/ in the picker = modal %v, want the path modal", value.modal)
	}

	// Root input owns every key except its own cancel, so F4 cannot discard it.
	value.input.text = "/projects"
	command := send(&value, key(tea.KeyF4, ""))
	if value.result.Quit || command != nil || value.input.text != "/projects" {
		t.Fatalf("F4 while typing = (quit %v, command %v, input %q), want the input kept",
			value.result.Quit, command, value.input.text)
	}
	send(&value, key(tea.KeyEscape, ""))
	if value.modal != browseModal || value.input.text != "" {
		t.Fatalf("Esc while typing = (modal %v, input %q), want the picker back", value.modal, value.input.text)
	}
}

func shortcutOrder(rendered string, values ...string) bool {
	lines := strings.Split(ansi.Strip(rendered), "\n")
	status := lines[len(lines)-1]
	previous := -1
	for _, value := range values {
		index := strings.Index(status, value)
		if index <= previous {
			return false
		}
		previous = index
	}
	return true
}

func jumpToWaitingAgentKey() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: 'a', ShiftedCode: 'A', Mod: tea.ModCtrl | tea.ModShift})
}

// pressJumpToWaitingAgent presses Ctrl+Shift+A and settles the commands the
// jump produces, the way pressSwitchTab settles a switch.
func pressJumpToWaitingAgent(t *testing.T, value dashboard) dashboard {
	t.Helper()
	command := send(&value, jumpToWaitingAgentKey())
	if command == nil {
		t.Fatal("Ctrl+Shift+A produced no command")
	}
	settleSwitch(&value, command)
	return value
}

func switchTabKey(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Mod: tea.ModCtrl | tea.ModShift})
}

func newTabKey() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: 't', ShiftedCode: 'T', Mod: tea.ModCtrl | tea.ModShift})
}

// settleSwitch runs the commands a switch produces until it has settled: the
// workspace pane selects a workspace before opening a tab, the terminal pane
// opens one straight away, and either way the terminal that comes back has to
// be attached. The read command that ends the chain is left alone; reading is
// what the terminal does from then on.
func settleSwitch(value *dashboard, command tea.Cmd) {
	for step := 0; command != nil && step < 4; step++ {
		message := command()
		if _, reading := message.(terminalOutputMsg); reading {
			return
		}
		command = send(value, message)
	}
}

// pressSwitchTab presses Ctrl+Shift+code and settles the switch.
func pressSwitchTab(t *testing.T, value dashboard, code rune) dashboard {
	t.Helper()
	command := send(&value, switchTabKey(code))
	if command == nil {
		t.Fatalf("Ctrl+Shift+%s produced no command", switchTabKey(code).String())
	}
	settleSwitch(&value, command)
	return value
}

func TestDashboardReplacesControlCharactersInLabels(t *testing.T) {
	hostile := "name\x1b]8;;https://example.com\x07\n"
	safe := "name�]8;;https://example.com��"
	safePrefix := "name�]8;;https://"
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: hostile, Path: "/projects/workspace"}
	value := newDashboard(&fakeBackend{}, model.Snapshot{Roots: []model.RootView{{
		Root: model.Root{ID: "root-1", Name: hostile, Path: "/projects"},
		Directories: []model.WorkspaceView{{
			Workspace: workspace,
			Tabs:      []model.Tab{{ID: "tab-1", Name: hostile, Running: true}},
		}},
	}}})
	value.width = 120
	value.height = 40
	value.selectedWorkspaceID = workspace.ID
	value.selectedPath = workspace.Path

	rendered := value.render()
	if strings.Contains(rendered, "\x1b]8;;https://example.com") {
		t.Fatalf("dashboard rendered a terminal control sequence:\n%s", rendered)
	}
	if plain := ansi.Strip(rendered); strings.Count(plain, safePrefix) < 2 {
		t.Fatalf("dashboard labels do not contain the safe replacement prefix %q:\n%s", safePrefix, plain)
	}
	tabBar := strings.Join(renderTabBar(value.styles, []model.Tab{{Name: hostile}}, 0, 120), "\n")
	if strings.Contains(tabBar, "\x1b]8;;https://example.com") || !strings.Contains(ansi.Strip(tabBar), safe) {
		t.Fatalf("tab label was not rendered safely: %q", tabBar)
	}

	value.modal = inputModal
	value.input.text = hostile
	if rendered := strings.Join(value.renderModal(120, 36), "\n"); strings.Contains(rendered, "\x1b]8;;https://example.com") ||
		!strings.Contains(ansi.Strip(rendered), safe) {
		t.Fatalf("root input was not rendered safely: %q", rendered)
	}

	value.modal = noModal
	value.setError(treeError, hostile)
	if rendered := value.renderStatus(120, 36)[1]; strings.Contains(rendered, "\x1b]8;;https://example.com") ||
		!strings.Contains(ansi.Strip(rendered), safe) {
		t.Fatalf("error was not rendered safely: %q", rendered)
	}

	value.modal = removeSelectionModal
	value.removeTarget = navItem{root: model.Root{Name: hostile}, isRoot: true}
	if rendered := strings.Join(value.renderModal(120, 36), "\n"); strings.Contains(rendered, "\x1b]8;;https://example.com") ||
		!strings.Contains(ansi.Strip(rendered), safe) {
		t.Fatalf("confirmation was not rendered safely: %q", rendered)
	}
}

func TestDashboardUsesCompactLeftPane(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 120
	value.height = 40

	view := value.dimensions()
	leftWidth, rightWidth := view.leftWidth, view.rightWidth
	if leftWidth != 28 || rightWidth != 89 {
		t.Fatalf("pane widths = %d/%d, want 28/89", leftWidth, rightWidth)
	}

	value = newDashboardWithConfig(&fakeBackend{}, model.Snapshot{}, "", Config{LeftWidth: 24})
	value.width = 120
	view = value.dimensions()
	leftWidth, rightWidth = view.leftWidth, view.rightWidth
	if leftWidth != 24 || rightWidth != 93 {
		t.Fatalf("configured pane widths = %d/%d, want 24/93", leftWidth, rightWidth)
	}
}

func TestOverlayBoxKeepsWideBackdropAndBoxInTheirColumns(t *testing.T) {
	for _, probe := range []struct {
		name string
		base string
		want string
	}{
		{name: "Korean", base: "가나다라마바사아자차", want: "가 ╭──╮ 마바사아자차"},
		{name: "mixed English and Korean", base: "ab가cd나ef다gh라마ij", want: "ab ╭──╮ ef다gh라마ij"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			for _, base := range []string{probe.base, lipgloss.NewStyle().Foreground(lipgloss.Color("7")).Render(probe.base)} {
				got := overlayBox([]string{base}, []string{"╭──╮"}, 3, 0, 20, 1)[0]
				if plain := ansi.Strip(got); plain != probe.want {
					t.Fatalf("overlayBox() = %q, want %q", plain, probe.want)
				}
				if width := lipgloss.Width(got); width != 20 {
					t.Fatalf("overlayBox() width = %d, want 20", width)
				}
			}
		})
	}
}

// typeSearch opens the find prompt, types a query one key at a time, and
// confirms it.
func typeSearch(t *testing.T, value dashboard, query string) dashboard {
	t.Helper()
	send(&value, key('/', "/"))
	if !value.search.typing {
		t.Fatal("/ did not open the find prompt")
	}
	for _, letter := range query {
		send(&value, key(letter, string(letter)))
	}
	if value.search.query != query {
		t.Fatalf("typed query = %q, want %q", value.search.query, query)
	}
	send(&value, key(tea.KeyEnter, ""))
	if value.search.typing {
		t.Fatal("Enter did not close the find prompt")
	}
	return value
}

func scrollbackShows(value dashboard, text string) bool {
	for _, line := range plainRows(value.terminal.renderViewport(value.scrollOffset)) {
		if strings.Contains(line, text) {
			return true
		}
	}
	return false
}

func plainStatus(value dashboard) string {
	return ansi.Strip(strings.Join(value.renderStatus(value.width, value.dimensions().bodyHeight), "\n"))
}

// helpKeyNames spells one help keycap the way globalKeys names it, expanding
// the paired arrows and pages the reference writes as a single cap.
func helpKeyNames(keycap string) []string {
	lower := strings.ToLower(keycap)
	for _, pair := range []struct {
		suffix string
		tails  []string
	}{
		{suffix: "←/→", tails: []string{"left", "right"}},
		{suffix: "↑/↓", tails: []string{"up", "down"}},
		{suffix: "pgup/pgdn", tails: []string{"pgup", "pgdown"}},
	} {
		prefix, found := strings.CutSuffix(lower, pair.suffix)
		if !found {
			continue
		}
		names := make([]string, 0, len(pair.tails))
		for _, tail := range pair.tails {
			names = append(names, prefix+tail)
		}
		return names
	}
	return []string{lower}
}

func helpContainsShortcut(lines []string, description string, keys ...string) bool {
	for _, line := range lines {
		if !strings.Contains(line, description) {
			continue
		}
		matches := true
		for _, key := range keys {
			matches = matches && strings.Contains(line, key)
		}
		if matches {
			return true
		}
	}
	return false
}

// typeText presses one key for each character of text.
func typeText(value *dashboard, text string) {
	for _, character := range text {
		send(value, key(character, string(character)))
	}
}

func key(code rune, text string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Text: text})
}

func controlBackslashKey() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: '\\', Mod: tea.ModCtrl})
}

// controlSlashKey is Ctrl+/ as a terminal that speaks the Kitty keyboard
// protocol reports it, and controlUnderscoreKey is the same press from every
// other terminal, which sends 0x1F and is decoded as Ctrl+_.
func controlSlashKey() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: '/', Mod: tea.ModCtrl})
}

func controlUnderscoreKey() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: '_', Mod: tea.ModCtrl})
}

func scrollbackToggleKey() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: '\\', ShiftedCode: '|', Mod: tea.ModCtrl | tea.ModShift})
}

// A drop that follows a fresh attach is another turn of the same loop, so the
// healthy-attach reset must not hand it an immediate retry.
func TestDashboardStillBacksOffWhenDropsAreImmediate(t *testing.T) {
	tabs := openTabs(1)
	value, _, refresh := exitedDashboard(t, tabs, 0)

	open := send(&value, refresh())
	send(&value, open())
	if value.terminalOpenedAt.IsZero() {
		t.Fatal("an opened terminal recorded no attach time")
	}
	send(&value, terminalOutputMsg{terminal: value.terminal, err: io.EOF})
	command := send(&value, refresh())
	if value.reattachAttempts != 1 || command == nil {
		t.Fatalf("an immediate drop = (retries %d, command %v), want one counted retry",
			value.reattachAttempts, command)
	}
	if message := command(); message != (reopenTerminalMsg{tabID: "tab-1"}) {
		t.Fatalf("an immediate drop returned %T, want a delayed reopen", message)
	}
}

type delayedResizeBackend struct {
	*fakeBackend
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	columns uint16
}

func (b *delayedResizeBackend) Resize(_ string, columns, _ uint16) error {
	if columns == 120 {
		close(b.started)
		<-b.release
	}
	b.mu.Lock()
	b.columns = columns
	b.mu.Unlock()
	return nil
}

func TestDashboardPreservesReplayRightEdgeWhenOpeningOnPhone(t *testing.T) {
	for _, width := range []int{32, 60, 79, 120} {
		for _, alternate := range []bool{false, true} {
			t.Run(fmt.Sprintf("width=%d/alternate=%t", width, alternate), func(t *testing.T) {
				tabs := openTabs(1)
				snapshot, workspace := alphaWorkspaceSnapshot(tabs)
				value := newDashboard(&fakeBackend{snapshot: snapshot}, snapshot)
				value.width, value.height = width, 24
				value.selectedWorkspaceID, value.selectedPath = workspace.ID, workspace.Path
				value.setNavigation(1)
				columns, rows := value.terminalSize()
				if width < narrowLayoutWidth {
					columns = uint16(width)
				}
				replay := "LEFT" + strings.Repeat(" ", int(columns)-len("LEFTRIGHT")) + "RIGHT"
				if alternate {
					replay = "\x1b[?1049h" + replay
				}
				// Check both initial selection and reopening an attached workspace.
				for range 2 {
					stream := replaySizedMemoryStream{memoryStream: newMemoryStream(""), columns: columns, rows: rows}
					send(&value, terminalOpenedMsg{tabID: tabs[0].ID, stream: stream, replay: []byte(replay)})
					t.Cleanup(value.closeTerminal)
					if got := value.terminal.render()[0]; !strings.Contains(got, "RIGHT") {
						t.Fatalf("restored first row lost its right edge: %q", got)
					}
					if got, _ := value.terminal.size(); got != columns {
						t.Fatalf("terminal columns = %d, want %d", got, columns)
					}
				}
			})
		}
	}
}
