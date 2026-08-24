package ui

import (
	"io"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
)

type embeddedTerminal struct {
	id        string
	stream    io.ReadWriteCloser
	emulator  *vt.SafeEmulator
	active    bool
	close     sync.Once
	inputDone chan struct{}
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
		id:        id,
		stream:    stream,
		emulator:  emulator,
		active:    true,
		inputDone: make(chan struct{}),
	}
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

func (t *embeddedTerminal) paste(value string) {
	t.emulator.Paste(value)
}

func (t *embeddedTerminal) resize(width, height int) {
	t.emulator.Resize(width, height)
}

func (t *embeddedTerminal) render() []string {
	return splitLines(t.emulator.Render(), t.emulator.Height())
}

func (t *embeddedTerminal) scrollbackLen() int {
	return t.emulator.ScrollbackLen()
}

// renderViewport returns one screen worth of rows ending offset lines above the
// live output. An offset of zero renders the live screen unchanged.
func (t *embeddedTerminal) renderViewport(offset int) []string {
	screen := t.render()
	history := t.emulator.Scrollback()
	offset = min(max(offset, 0), history.Len())
	if offset == 0 {
		return screen
	}
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
