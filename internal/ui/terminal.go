package ui

import (
	"fmt"
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
	if sequence, ok := modifiedSpecialKey(key); ok {
		// Written to the same pipe SendKey uses, so it keeps its place in the
		// order the user typed.
		_, _ = io.WriteString(t.emulator.InputPipe(), sequence)
		return
	}
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

// The emulator encodes unmodified special keys and Ctrl with a letter, but a
// special key held with Shift, Ctrl or Meta falls through its table and
// produces no bytes at all. Without the encodings below romty swallows
// ctrl+left, shift+up, ctrl+home and their neighbours for every guest
// application, with nothing to show the user why.
//
// xterm spells these as CSI with a modifier parameter: arrows, Home, End and
// F1 to F4 take a letter final, the rest a number and a tilde.
var (
	modifiedKeyFinals = map[rune]byte{
		tea.KeyUp: 'A', tea.KeyDown: 'B', tea.KeyRight: 'C', tea.KeyLeft: 'D',
		tea.KeyBegin: 'E', tea.KeyEnd: 'F', tea.KeyHome: 'H',
		tea.KeyF1: 'P', tea.KeyF2: 'Q', tea.KeyF3: 'R', tea.KeyF4: 'S',
	}
	modifiedKeyNumbers = map[rune]int{
		tea.KeyInsert: 2, tea.KeyDelete: 3, tea.KeyPgUp: 5, tea.KeyPgDown: 6,
		tea.KeyF5: 15, tea.KeyF6: 17, tea.KeyF7: 18, tea.KeyF8: 19,
		tea.KeyF9: 20, tea.KeyF10: 21, tea.KeyF11: 23, tea.KeyF12: 24,
	}
)

func modifiedSpecialKey(key tea.Key) (string, bool) {
	if key.Mod&^tea.ModAlt == 0 {
		// Unmodified, or Alt alone, which the emulator encodes by prefixing
		// an escape. Leave both to it.
		return "", false
	}
	parameter := modifierParameter(key.Mod)
	if final, ok := modifiedKeyFinals[key.Code]; ok {
		return fmt.Sprintf("\x1b[1;%d%c", parameter, final), true
	}
	if number, ok := modifiedKeyNumbers[key.Code]; ok {
		return fmt.Sprintf("\x1b[%d;%d~", number, parameter), true
	}
	return "", false
}

// modifierParameter is xterm's one-based modifier encoding: shift 1, alt 2,
// ctrl 4, meta 8, summed onto a base of 1.
func modifierParameter(mod tea.KeyMod) int {
	parameter := 1
	for bit, weight := range map[tea.KeyMod]int{
		tea.ModShift: 1,
		tea.ModAlt:   2,
		tea.ModCtrl:  4,
		tea.ModMeta:  8,
	} {
		if mod&bit != 0 {
			parameter += weight
		}
	}
	return parameter
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
