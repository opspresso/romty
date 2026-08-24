package ui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode"

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
	// applicationKeypad mirrors DECKPAM so keypad navigation and operators
	// keep the mode the guest requested.
	applicationKeypad bool
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
		EnableMode:  func(mode ansi.Mode) { terminal.trackGuestMode(mode, true) },
		DisableMode: func(mode ansi.Mode) { terminal.trackGuestMode(mode, false) },
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
	var sequence string
	key, sequence = t.prepareKeypadKey(key)
	if sequence != "" {
		_, _ = io.WriteString(t.emulator.InputPipe(), sequence)
		return
	}
	if text, ok := printableKeyText(key); ok {
		t.emulator.SendText(text)
		return
	}
	key.Mod &^= tea.ModCapsLock | tea.ModNumLock | tea.ModScrollLock
	if sequence, ok := emulatorMissingSpecialKey(key); ok {
		_, _ = io.WriteString(t.emulator.InputPipe(), sequence)
		return
	}
	if sequence, ok := modifiedSpecialKey(key); ok {
		// Written to the same pipe SendKey uses, so it keeps its place in the
		// order the user typed.
		_, _ = io.WriteString(t.emulator.InputPipe(), sequence)
		return
	}
	if sequence, ok := modifiedOtherKey(key); ok {
		_, _ = io.WriteString(t.emulator.InputPipe(), sequence)
		return
	}
	t.emulator.SendKey(uv.KeyPressEvent(uv.Key{
		Mod:         uv.KeyMod(key.Mod),
		Code:        key.Code,
		ShiftedCode: key.ShiftedCode,
		BaseCode:    key.BaseCode,
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

func (t *embeddedTerminal) trackGuestMode(mode ansi.Mode, enabled bool) {
	if mode == ansi.Mode(ansi.ModeNumericKeypad) {
		t.applicationKeypad = enabled
	}
	t.trackGuestMouse(mode, enabled)
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

// printableKeyText keeps the text the host decoded instead of asking the
// emulator to reconstruct it from Code. Code is the unshifted key for ASCII
// capitals and cannot represent a multi-rune grapheme at all.
func printableKeyText(key tea.Key) (string, bool) {
	if key.Mod&(tea.ModCtrl|tea.ModMeta|tea.ModHyper|tea.ModSuper) != 0 {
		return "", false
	}
	if key.Code >= tea.KeyKpEnter && key.Code <= tea.KeyKpBegin {
		// The emulator preserves application keypad mode for the keys it
		// supports. Do not flatten those events into their display text.
		return "", false
	}
	text := key.Text
	if text == "" {
		switch {
		case key.ShiftedCode != 0:
			text = string(key.ShiftedCode)
		case unicode.IsPrint(key.Code):
			text = string(key.Code)
		default:
			return "", false
		}
	}
	if key.Mod&tea.ModAlt != 0 {
		text = "\x1b" + text
	}
	return text, true
}

type keypadNavigationKey struct {
	normal      rune
	application rune
}

var keypadNavigationKeys = map[rune]keypadNavigationKey{
	tea.KeyKpInsert: {normal: tea.KeyInsert, application: tea.KeyKp0},
	tea.KeyKpEnd:    {normal: tea.KeyEnd, application: tea.KeyKp1},
	tea.KeyKpDown:   {normal: tea.KeyDown, application: tea.KeyKp2},
	tea.KeyKpPgDown: {normal: tea.KeyPgDown, application: tea.KeyKp3},
	tea.KeyKpLeft:   {normal: tea.KeyLeft, application: tea.KeyKp4},
	tea.KeyKpBegin:  {normal: tea.KeyBegin, application: tea.KeyKp5},
	tea.KeyKpRight:  {normal: tea.KeyRight, application: tea.KeyKp6},
	tea.KeyKpHome:   {normal: tea.KeyHome, application: tea.KeyKp7},
	tea.KeyKpUp:     {normal: tea.KeyUp, application: tea.KeyKp8},
	tea.KeyKpPgUp:   {normal: tea.KeyPgUp, application: tea.KeyKp9},
	tea.KeyKpDelete: {normal: tea.KeyDelete, application: tea.KeyKpDecimal},
}

func (t *embeddedTerminal) prepareKeypadKey(key tea.Key) (tea.Key, string) {
	if key.Code < tea.KeyKpEnter || key.Code > tea.KeyKpBegin {
		return key, ""
	}
	key.Text = ""
	key.Mod &^= tea.ModCapsLock | tea.ModNumLock | tea.ModScrollLock
	if mapped, ok := keypadNavigationKeys[key.Code]; ok {
		key.Code = mapped.normal
		if t.applicationKeypad && key.Mod&xtermModifierMask == 0 {
			key.Code = mapped.application
		}
		return key, ""
	}
	if key.Code == tea.KeyKpDivide || key.Code == tea.KeyKpSep {
		value, final := '/', 'o'
		if key.Code == tea.KeyKpSep {
			value, final = ',', 'l'
		}
		if t.applicationKeypad && key.Mod&xtermModifierMask == 0 {
			return key, fmt.Sprintf("\x1bO%c", final)
		}
		key.Code = value
		key.Text = string(value)
	}
	return key, ""
}

// The emulator encodes unmodified special keys and Ctrl with a letter, but a
// special key held with Shift, Ctrl or Meta falls through its table and
// produces no bytes at all. Without the encodings below romty swallows
// ctrl+left, shift+up, ctrl+home and their neighbours for every guest
// application, with nothing to show the user why.
//
// xterm spells these as CSI with a modifier parameter: arrows, Home, End and
// F1 to F4 take a letter final, the rest a number and a tilde.
const xtermModifierMask = tea.ModShift | tea.ModAlt | tea.ModCtrl | tea.ModMeta

var (
	missingKeySequences = map[rune]string{
		tea.KeyBegin: "\x1b[E", tea.KeyFind: "\x1b[1~", tea.KeySelect: "\x1b[4~",
		tea.KeyF13: "\x1b[25~", tea.KeyF14: "\x1b[26~", tea.KeyF15: "\x1b[28~", tea.KeyF16: "\x1b[29~",
		tea.KeyF17: "\x1b[31~", tea.KeyF18: "\x1b[32~", tea.KeyF19: "\x1b[33~", tea.KeyF20: "\x1b[34~",
	}
	modifiedKeyFinals = map[rune]byte{
		tea.KeyUp: 'A', tea.KeyDown: 'B', tea.KeyRight: 'C', tea.KeyLeft: 'D',
		tea.KeyBegin: 'E', tea.KeyEnd: 'F', tea.KeyHome: 'H',
		tea.KeyF1: 'P', tea.KeyF2: 'Q', tea.KeyF3: 'R', tea.KeyF4: 'S',
	}
	modifiedKeyNumbers = map[rune]int{
		tea.KeyFind: 1, tea.KeyInsert: 2, tea.KeyDelete: 3, tea.KeySelect: 4, tea.KeyPgUp: 5, tea.KeyPgDown: 6,
		tea.KeyF5: 15, tea.KeyF6: 17, tea.KeyF7: 18, tea.KeyF8: 19,
		tea.KeyF9: 20, tea.KeyF10: 21, tea.KeyF11: 23, tea.KeyF12: 24,
		tea.KeyF13: 25, tea.KeyF14: 26, tea.KeyF15: 28, tea.KeyF16: 29,
		tea.KeyF17: 31, tea.KeyF18: 32, tea.KeyF19: 33, tea.KeyF20: 34,
	}
)

func emulatorMissingSpecialKey(key tea.Key) (string, bool) {
	mod := key.Mod & xtermModifierMask
	if mod&^tea.ModAlt != 0 {
		return "", false
	}
	sequence, ok := missingKeySequences[key.Code]
	if !ok {
		return "", false
	}
	if mod == tea.ModAlt {
		sequence = "\x1b" + sequence
	}
	return sequence, true
}

func modifiedSpecialKey(key tea.Key) (string, bool) {
	mod := key.Mod & xtermModifierMask
	if mod&^tea.ModAlt == 0 {
		// Unmodified, or Alt alone, which the emulator encodes by prefixing
		// an escape. Leave both to it.
		return "", false
	}
	parameter := modifierParameter(mod)
	if final, ok := modifiedKeyFinals[key.Code]; ok {
		return fmt.Sprintf("\x1b[1;%d%c", parameter, final), true
	}
	if number, ok := modifiedKeyNumbers[key.Code]; ok {
		return fmt.Sprintf("\x1b[%d;%d~", number, parameter), true
	}
	return "", false
}

var modifiedOtherKeyCodes = map[rune]int{
	tea.KeyBackspace: 127,
	tea.KeyTab:       9,
	tea.KeyEnter:     13,
	tea.KeyEscape:    27,
	tea.KeySpace:     32,
}

// modifiedOtherKey covers keys Bubble Tea can distinguish after enabling
// xterm modifyOtherKeys, but which the emulator cannot encode yet.
func modifiedOtherKey(key tea.Key) (string, bool) {
	mod := key.Mod & xtermModifierMask
	if mod&^tea.ModAlt == 0 || emulatorHandlesModifiedKey(key.Code, mod) {
		return "", false
	}
	code, ok := modifiedOtherKeyCodes[key.Code]
	if !ok {
		value := key.Code
		if key.ShiftedCode != 0 {
			value = key.ShiftedCode
		}
		if !unicode.IsPrint(value) {
			return "", false
		}
		code = int(value)
	}
	return fmt.Sprintf("\x1b[27;%d;%d~", modifierParameter(mod), code), true
}

func emulatorHandlesModifiedKey(code rune, mod tea.KeyMod) bool {
	if code == tea.KeyTab && mod == tea.ModShift {
		return true
	}
	if mod&tea.ModCtrl == 0 || mod&^(tea.ModAlt|tea.ModCtrl) != 0 {
		return false
	}
	if code >= 'a' && code <= 'z' {
		return true
	}
	switch code {
	case tea.KeySpace, '[', '\\', ']', '^', '_':
		return true
	default:
		return false
	}
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
