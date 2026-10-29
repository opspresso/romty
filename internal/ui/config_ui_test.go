package ui

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/sound"
)

func TestDashboardDragsAndPersistsTheWorkspaceDivider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	value := newDashboardWithConfig(&fakeBackend{}, model.Snapshot{}, path, Config{LeftWidth: 24})
	value.width, value.height = 120, 30
	seam := value.dimensions().leftWidth + 1

	command := send(&value, tea.MouseClickMsg{X: seam, Y: 5, Button: tea.MouseLeft})
	if !value.navigationResize || command != nil {
		t.Fatalf("divider press = (dragging %v, command %v)", value.navigationResize, command)
	}

	send(&value, tea.MouseMotionMsg{X: 35, Y: 5, Button: tea.MouseLeft})
	if value.paneWidth() != 34 {
		t.Fatalf("divider drag width = %d, want 34", value.paneWidth())
	}

	command = send(&value, tea.MouseReleaseMsg{X: 35, Y: 5, Button: tea.MouseLeft})
	if value.navigationResize || command == nil {
		t.Fatalf("divider release = (dragging %v, command %v)", value.navigationResize, command)
	}
	message := command()
	if saved, ok := message.(configSavedMsg); !ok || saved.err != nil {
		t.Fatalf("save result = %#v", message)
	}
	config, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if config.LeftWidth != 34 {
		t.Fatalf("saved width = %d, want 34", config.LeftWidth)
	}
}

func TestDashboardKeepsConfigControlsCompact(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 100, 24
	value.modal = configModal

	lines := configModalRows(t, value)
	body := make([]string, 0, len(lines))
	for _, line := range lines {
		body = append(body, strings.TrimSpace(strings.Trim(line, "│")))
	}
	rows := configRows()
	if len(body) != len(rows) {
		t.Fatalf("config modal body = %q, want one row per setting", body)
	}
	// Name, then value, then the key the row advertises — each in its own
	// column, all on the one row the setting owns.
	for index, row := range rows {
		name := strings.Index(body[index], row.name)
		hint := strings.LastIndex(body[index], row.hint)
		if name < 0 || hint <= name {
			t.Errorf("row %d = %q, want %q then %q", index, body[index], row.name, row.hint)
			continue
		}
		text := row.text(value)
		if text == "" {
			continue
		}
		if at := strings.Index(body[index][name+len(row.name):], text); at < 0 {
			t.Errorf("row %d = %q, want %q between the name and the key", index, body[index], text)
		}
	}
	// Values share a column, which is the whole point of splitting them out of
	// the name: one glance down the box says which settings are on. Measured
	// on the drawn line, cursor column and all, rather than on the trimmed one.
	column := -1
	for index, row := range rows {
		text := row.text(value)
		if text == "" {
			continue
		}
		drawn := lines[index]
		// Search after the name: "on" is also inside "Notifications".
		after := strings.Index(drawn, row.name) + len(row.name)
		at := lipgloss.Width(drawn[:after+strings.Index(drawn[after:], text)])
		if column < 0 {
			column = at
		} else if at != column {
			t.Errorf("row %d puts its value at column %d, want %d", index, at, column)
		}
	}

	// Keys stay subordinate to the settings while sharing their row, so every
	// control still fits on the shortest supported screen.
	rendered := strings.Join(value.renderModal(72, 22), "\n")
	if !strings.Contains(rendered, value.styles.empty.Render("d")) {
		t.Fatalf("the key hint is not muted:\n%s", rendered)
	}

	// The shortest screen romty lays out for still holds the whole box.
	value.height = 10
	if short := ansi.Strip(value.render()); !strings.Contains(short, "╰") {
		t.Fatalf("config modal lost its bottom edge at height %d:\n%s", value.height, short)
	}
}

func TestDashboardClicksConfigControls(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 100, 24
	send(&value, key(tea.KeyF3, ""))
	left, top := value.modalGeometry(value.width, value.dimensions().bodyHeight).contentOrigin()

	save := send(&value, tea.MouseClickMsg{X: left + 2, Y: top + 2, Button: tea.MouseLeft})
	if !value.config.MousePassthrough || save == nil || value.configIndex != 1 {
		t.Fatalf("mouse passthrough click = (enabled %v, save %v, cursor %d)",
			value.config.MousePassthrough, save, value.configIndex)
	}
	// The row is found by name, so adding a setting does not move the click.
	done := slices.IndexFunc(configRows(), func(row configRow) bool { return row.name == "Sound on done" })
	soundCommand := send(&value, tea.MouseClickMsg{X: left + 2, Y: top + configContentOffset(value.dimensions().bodyHeight) + done, Button: tea.MouseLeft})
	if played := commandMessage[soundPlayedMsg](t, soundCommand); played.kind != sound.Done {
		t.Fatalf("done sound click = %q, want done", played.kind)
	}
}

// A phone's SSH client is around 40 to 60 columns, where the workspace pane and
// its separator take half the screen from the terminal the keyboard is in.
func TestDashboardHidesWorkspacePaneOnANarrowScreen(t *testing.T) {
	value := narrowDashboard(t, 60)
	view := value.dimensions()
	if view.leftWidth != 0 || view.separator != 0 || view.rightWidth != 60 {
		t.Fatalf("narrow layout = (left %d, separator %d, right %d), want the terminal alone at 60",
			view.leftWidth, view.separator, view.rightWidth)
	}
	if originX, _ := view.terminalOrigin(); originX != 0 {
		t.Fatalf("terminal origin x = %d, want the first column", originX)
	}
	if rendered := ansi.Strip(value.render()); strings.Contains(rendered, "alpha") {
		t.Fatalf("workspace tree is still drawn:\n%s", rendered)
	}

	send(&value, controlSlashKey())
	if value.focus != leftPane {
		t.Fatalf("Ctrl+/ focus = %v, want the workspace pane", value.focus)
	}
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "alpha") {
		t.Fatalf("Ctrl+/ did not bring the workspace tree back:\n%s", rendered)
	}
}

// A shell wraps its output to the width it was told, so hiding the pane has to
// reach the PTY the way dragging the window does.
func TestDashboardResizesThePTYWhenTheWorkspacePaneHides(t *testing.T) {
	value := narrowDashboard(t, 60)
	backend := value.backend.(*fakeBackend)

	command := send(&value, controlSlashKey())
	if command == nil {
		t.Fatal("hiding the workspace pane did not ask the daemon to resize the PTY")
	}
	commandMessages(command)
	wantColumns, wantRows := value.terminalSize()
	if backend.createdColumns != wantColumns || backend.createdRows != wantRows {
		t.Fatalf("PTY size with the pane shown = %dx%d, want %dx%d",
			backend.createdColumns, backend.createdRows, wantColumns, wantRows)
	}

	command = send(&value, controlSlashKey())
	if command == nil {
		t.Fatal("showing the workspace pane did not ask the daemon to resize the PTY")
	}
	commandMessages(command)
	if wantColumns, _ = value.terminalSize(); backend.createdColumns != wantColumns {
		t.Fatalf("PTY columns with the pane hidden = %d, want the whole screen %d",
			backend.createdColumns, wantColumns)
	}
}

// A resize failure is its own message, not a snapshot, so a refresh does not
// erase it and the snapshot handler can trust message.value.
func TestDashboardReportsAFailedResizeAsItsOwnFailure(t *testing.T) {
	backend := &fakeBackend{}
	backend.fail("Resize", errors.New("terminal session not found"))
	value := newDashboard(backend, model.Snapshot{})
	value.terminal = newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)
	t.Cleanup(value.closeTerminal)

	command := send(&value, tea.WindowSizeMsg{Width: 100, Height: 30})
	if command == nil {
		t.Fatal("a resize produced no command")
	}
	message := command()
	if _, ok := message.(resizeFailedMsg); !ok {
		t.Fatalf("a failed resize produced %T, want resizeFailedMsg", message)
	}
	send(&value, message)
	if !strings.Contains(value.errorMessage, "resize terminal") {
		t.Fatalf("error message = %q, want the resize failure", value.errorMessage)
	}
	// The emulator has taken the new size even though the daemon refused.
	columns, rows := value.terminalSize()
	if value.terminal.emulator.Width() != int(columns) || value.terminal.emulator.Height() != int(rows) {
		t.Fatalf("emulator = %dx%d, want %dx%d",
			value.terminal.emulator.Width(), value.terminal.emulator.Height(), columns, rows)
	}
}

// Holding an arrow key in the config modal outruns the disk, so an answer can
// arrive under a width that has already moved on. The failure is still
// reported, and the newer width is saved again.
func TestDashboardReportsAConfigFailureTheWidthMovedPast(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 120, 40
	value.config.LeftWidth = 24

	command := send(&value, configSavedMsg{
		saved: Config{LeftWidth: 22},
		err:   errors.New("read-only file system"),
	})
	if !strings.Contains(value.errorMessage, "read-only file system") {
		t.Fatalf("error message = %q, want the save failure", value.errorMessage)
	}
	if command == nil {
		t.Fatal("the width that moved on was not saved again")
	}
}

// A resize is a round trip, so dragging a window and then switching tabs
// leaves one in flight for the tab just left. A failure for that tab must not
// be reported against whichever terminal took its place.
func TestDashboardIgnoresAResizeFailureForATerminalItLeft(t *testing.T) {
	backend := &fakeBackend{}
	backend.fail("Resize", errors.New("running terminal session not found"))
	value := newDashboard(backend, model.Snapshot{})
	value.terminal = newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)

	command := send(&value, tea.WindowSizeMsg{Width: 100, Height: 30})
	if command == nil {
		t.Fatal("a resize produced no command")
	}
	message := command()
	failure, ok := message.(resizeFailedMsg)
	if !ok {
		t.Fatalf("a failed resize produced %T, want resizeFailedMsg", message)
	}
	if failure.tabID != "tab-1" {
		t.Fatalf("resizeFailedMsg tabID = %q, want the tab it was sent for", failure.tabID)
	}

	// The user has moved on to another tab before the answer lands.
	value.closeTerminal()
	value.terminal = newEmbeddedTerminal("tab-2", newMemoryStream(""), 40, 10)
	t.Cleanup(value.closeTerminal)

	send(&value, failure)
	if value.errorMessage != "" {
		t.Fatalf("error message = %q, want nothing about a terminal the user left", value.errorMessage)
	}
}

func TestFormatTokensKeepsItsWidthAsItGrows(t *testing.T) {
	for _, probe := range []struct {
		count int
		want  string
	}{{count: 0, want: "0"}, {count: 999, want: "999"}, {count: 1000, want: "1k"},
		{count: 344103, want: "344k"}, {count: 1_250_000, want: "1.2M"}} {
		if got := formatTokens(probe.count); got != probe.want {
			t.Errorf("formatTokens(%d) = %q, want %q", probe.count, got, probe.want)
		}
	}
}

func TestDashboardAdjustsAndPersistsLeftPaneWidth(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 120
	value.height = 40
	value.configPath = configPath

	send(&value, key(',', ","))
	if value.modal != configModal {
		t.Fatalf("modal = %v, want config", value.modal)
	}
	saveCommand := send(&value, key(tea.KeyRight, ""))
	leftWidth := value.dimensions().leftWidth
	if saveCommand == nil || leftWidth != 29 {
		t.Fatalf("right adjustment = (command %v, width %d), want saved width 29", saveCommand, leftWidth)
	}
	send(&value, saveCommand())
	loaded, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if loaded.LeftWidth != 29 || !configRowReads(ansi.Strip(value.render()), "Workspace pane width", "29") {
		t.Fatalf("persisted config = %#v\n%s", loaded, ansi.Strip(value.render()))
	}

	send(&value, key(tea.KeyEscape, ""))
	if value.modal != noModal {
		t.Fatalf("modal after Esc = %v, want none", value.modal)
	}
}

// Dragging a window makes a burst of resizes, each asking the daemon on its
// own goroutine, and nothing orders the round trips. Whichever lands last has
// to leave the PTY at the size on screen, not at one the window had on the way
// past — a shell told the wrong size wraps its output wrongly until something
// resizes again.
func TestDashboardResizesToTheSizeOnScreenWhateverTheOrder(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	backend := value.backend.(*fakeBackend)
	value.terminal = newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)
	t.Cleanup(value.closeTerminal)

	wide := send(&value, tea.WindowSizeMsg{Width: 120, Height: 40})
	narrow := send(&value, tea.WindowSizeMsg{Width: 60, Height: 20})
	if wide == nil || narrow == nil {
		t.Fatal("a window resize did not ask the daemon to resize the PTY")
	}
	wantColumns, wantRows := value.terminalSize()

	// The earlier request lands last, which is what two goroutines are free to
	// do.
	narrow()
	wide()
	if backend.createdColumns != wantColumns || backend.createdRows != wantRows {
		t.Fatalf("resized the PTY to %dx%d, want the %dx%d that is on screen",
			backend.createdColumns, backend.createdRows, wantColumns, wantRows)
	}
}

func TestDashboardFitsPhoneTerminalWidth(t *testing.T) {
	for _, width := range []int{1, 10, 20, 24, 32, 39, 40, 60, 79} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			value := narrowDashboard(t, width)
			for _, focus := range []pane{terminalPane, leftPane} {
				value.focus = focus
				commandMessages(value.resizeTerminal())
				view := value.dimensions()
				if got := view.leftWidth + view.separator + view.rightWidth; got != width {
					t.Errorf("layout width = %d, want %d", got, width)
				}
				if focus == terminalPane {
					columns, _ := value.terminal.size()
					if int(columns) != width {
						t.Errorf("terminal columns = %d, want %d", columns, width)
					}
				}
				for row, line := range strings.Split(value.render(), "\n") {
					if got := lipgloss.Width(line); got > width {
						t.Errorf("row %d width = %d, exceeds %d", row, got, width)
					}
				}
			}
		})
	}
}

func TestDashboardOrdersInFlightResizes(t *testing.T) {
	backend := &delayedResizeBackend{fakeBackend: &fakeBackend{}, started: make(chan struct{}), release: make(chan struct{})}
	value := narrowDashboard(t, 160)
	value.backend = backend
	value.config.LeftWidth = 37
	// Start a wide request before the viewport shrinks, and delay its response.
	wide := send(&value, tea.WindowSizeMsg{Width: 160, Height: 24})
	wideDone := make(chan struct{})
	go func() { commandMessages(wide); close(wideDone) }()
	<-backend.started
	narrow := send(&value, tea.WindowSizeMsg{Width: 32, Height: 24})
	narrowDone := make(chan struct{})
	go func() { commandMessages(narrow); close(narrowDone) }()
	select {
	case <-narrowDone:
	case <-time.After(100 * time.Millisecond):
	}
	close(backend.release)
	<-wideDone
	<-narrowDone
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.columns != 32 {
		t.Fatalf("PTY columns = %d, want latest viewport width 32", backend.columns)
	}
}

func TestDashboardOrdersResizesAcrossReattachments(t *testing.T) {
	backend := &delayedResizeBackend{fakeBackend: &fakeBackend{}, started: make(chan struct{}), release: make(chan struct{})}
	value := narrowDashboard(t, 160)
	value.backend, value.config.LeftWidth = backend, 37
	wide := value.resizeTerminal()
	wideDone := make(chan struct{})
	go func() { wide(); close(wideDone) }()
	<-backend.started
	// Reattaching the same tab creates a new emulator while the previous
	// attachment's resize can still be in flight on another connection.
	stale := value.resizeTerminal()
	value.closeTerminal()
	value.width = 32
	value.terminal = newEmbeddedTerminal("tab-1", newMemoryStream(""), 32, 20)
	t.Cleanup(value.closeTerminal)
	narrow := value.resizeTerminal()
	narrowDone := make(chan struct{})
	go func() { narrow(); close(narrowDone) }()
	select {
	case <-narrowDone:
	case <-time.After(100 * time.Millisecond):
	}
	close(backend.release)
	<-wideDone
	<-narrowDone
	backend.mu.Lock()
	columns := backend.columns
	backend.mu.Unlock()
	if columns != 32 {
		t.Fatalf("reattached PTY columns = %d, want 32", columns)
	}
	// A queued command from the closed emulator must not resize its replacement.
	if message := stale(); message != nil {
		t.Fatalf("closed terminal resize returned %v", message)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.columns != 32 {
		t.Fatalf("stale command changed PTY columns to %d", backend.columns)
	}
}
