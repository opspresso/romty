package ui

import (
	"io"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

type embeddedTerminal struct {
	id        string
	stream    io.ReadWriteCloser
	emulator  *vt.SafeEmulator
	active    bool
	close     sync.Once
	inputDone chan struct{}
	// guestMouse holds the mouse tracking modes the guest application asked
	// for. romty only mirrors them to the host when passthrough is enabled.
	guestMouse map[ansi.DECMode]bool
}

type terminalOutputMsg struct {
	terminal *embeddedTerminal
	data     []byte
	err      error
}

func newEmbeddedTerminal(id string, stream io.ReadWriteCloser, width, height int) *embeddedTerminal {
	emulator := vt.NewSafeEmulator(width, height)
	emulator.SetScrollbackSize(10_000)
	terminal := &embeddedTerminal{
		id:         id,
		stream:     stream,
		emulator:   emulator,
		active:     true,
		inputDone:  make(chan struct{}),
		guestMouse: make(map[ansi.DECMode]bool),
	}
	emulator.SetCallbacks(vt.Callbacks{
		EnableMode:  func(mode ansi.Mode) { terminal.trackGuestMouse(mode, true) },
		DisableMode: func(mode ansi.Mode) { terminal.trackGuestMouse(mode, false) },
	})
	go func() {
		defer close(terminal.inputDone)
		_, _ = io.Copy(stream, emulator)
	}()
	return terminal
}

func (t *embeddedTerminal) read() tea.Cmd {
	return func() tea.Msg {
		buffer := make([]byte, 32*1024)
		count, err := t.stream.Read(buffer)
		return terminalOutputMsg{terminal: t, data: buffer[:count], err: err}
	}
}

func (t *embeddedTerminal) writeOutput(data []byte) {
	_, _ = t.emulator.Write(data)
}

func (t *embeddedTerminal) sendKey(message tea.KeyPressMsg) {
	key := tea.Key(message)
	t.emulator.SendKey(uv.KeyPressEvent(uv.Key{
		Text:        key.Text,
		Mod:         uv.KeyMod(key.Mod),
		Code:        key.Code,
		ShiftedCode: key.ShiftedCode,
		BaseCode:    key.BaseCode,
		IsRepeat:    key.IsRepeat,
	}))
}

// mouseTrackingModes are the guest mouse modes romty knows how to mirror,
// ordered from the least to the most detailed.
var mouseTrackingModes = []ansi.DECMode{
	ansi.ModeMouseX10,
	ansi.ModeMouseNormal,
	ansi.ModeMouseButtonEvent,
	ansi.ModeMouseAnyEvent,
}

func (t *embeddedTerminal) trackGuestMouse(mode ansi.Mode, enabled bool) {
	for _, tracked := range mouseTrackingModes {
		if mode == ansi.Mode(tracked) {
			t.guestMouse[tracked] = enabled
			return
		}
	}
}

// guestMouseMode reports the host mouse mode needed to satisfy the guest, or
// MouseModeNone when the guest never asked for the mouse.
func (t *embeddedTerminal) guestMouseMode() tea.MouseMode {
	if t.guestMouse[ansi.ModeMouseAnyEvent] {
		return tea.MouseModeAllMotion
	}
	for _, tracked := range mouseTrackingModes {
		if t.guestMouse[tracked] {
			return tea.MouseModeCellMotion
		}
	}
	return tea.MouseModeNone
}

func (t *embeddedTerminal) sendMouse(event uv.MouseEvent) {
	t.emulator.SendMouse(event)
}

func (t *embeddedTerminal) paste(value string) {
	t.emulator.Paste(value)
}

func (t *embeddedTerminal) resize(width, height int) {
	t.emulator.Resize(width, height)
}

func (t *embeddedTerminal) render() []string {
	return splitLines(t.emulator.Render(), t.emulator.Height())
}

// scrollbackLen reports the retained history. The alternate screen has none:
// the guest application owns that buffer, and the main screen's history belongs
// to whatever ran before the application started.
func (t *embeddedTerminal) scrollbackLen() int {
	if t.emulator.IsAltScreen() {
		return 0
	}
	return t.emulator.ScrollbackLen()
}

func (t *embeddedTerminal) altScreen() bool {
	return t.emulator.IsAltScreen()
}

// renderViewport returns one screen worth of rows ending offset lines above the
// live output. An offset of zero renders the live screen unchanged.
func (t *embeddedTerminal) renderViewport(offset int) []string {
	screen := t.render()
	offset = min(max(offset, 0), t.scrollbackLen())
	if offset == 0 {
		return screen
	}
	history := t.emulator.Scrollback()
	lines := make([]string, 0, len(screen))
	for index := history.Len() - offset; len(lines) < len(screen); index++ {
		if index < history.Len() {
			lines = append(lines, history.Line(index).Render())
			continue
		}
		lines = append(lines, screen[index-history.Len()])
	}
	return lines
}

func (t *embeddedTerminal) cursorPosition() uv.Position {
	return t.emulator.CursorPosition()
}

func (t *embeddedTerminal) disconnect() {
	t.active = false
	t.close.Do(func() {
		_ = t.stream.Close()
		if closer, ok := t.emulator.InputPipe().(io.Closer); ok {
			_ = closer.Close()
		}
		<-t.inputDone
		_ = t.emulator.Close()
	})
}

func (t *embeddedTerminal) closeTerminal() {
	t.disconnect()
}

func splitLines(value string, height int) []string {
	lines := strings.Split(value, "\n")
	if len(lines) > height {
		return lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines
}
