package ui

import (
	"encoding/base64"
	"fmt"
	"image/color"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"github.com/opspresso/romty/internal/keyboard"
	"github.com/opspresso/romty/internal/version"
)

type embeddedTerminal struct {
	id        string
	stream    io.ReadWriteCloser
	emulator  *vt.SafeEmulator
	closeOnce sync.Once
	inputDone chan struct{}
	inputMu   sync.Mutex
	inputErr  error
	closed    atomic.Bool
	// applicationKeypad mirrors DECKPAM so keypad navigation and operators
	// keep the mode the guest requested.
	applicationKeypad bool
	// keyboard is the key encoding the guest asked for, which decides how a
	// key with modifiers is written to it.
	keyboard keyboard.State
	// guestMouse holds the mouse tracking modes the guest application asked
	// for. romty mirrors them to the host unless the mouse is blocked.
	guestMouse map[ansi.DECMode]bool
	// focused is what the guest was last told about having the keyboard. A
	// program that asked for focus events uses them to decide whether anyone
	// is looking, which is when Codex and Claude Code stay quiet.
	focused bool
	// cursorHidden, cursorShape and cursorSteady are the cursor as the guest
	// set it. A program that draws its own cursor hides the terminal's, and
	// one that redraws the screen hides it while it does.
	cursorHidden bool
	cursorShape  tea.CursorShape
	cursorSteady bool
	// held is the last frame the guest finished, shown while it draws the
	// next one inside a synchronized update. releaseScheduled says a timer is
	// already set to end a hold the guest never ends.
	held             *heldFrame
	releaseScheduled bool
	// titles removes window-title sequences before the emulator parses them,
	// because the emulator reads the 0x9C inside characters such as "서" as
	// the C1 string terminator and prints the rest of the title as text.
	titles titleScrubber
	// readBuffer is reused by every read. A read is issued only after the
	// previous output was written to the emulator, so no two reads overlap.
	readBuffer []byte
	// live is false while the recorded output replays, which already rang
	// its bells and made its copies when it first ran. bell and copies are
	// what the live output asked of the host since the last takeEffects.
	live   bool
	bell   bool
	copies []string
}

// maxClipboardBytes bounds one copy a guest asks for. A clipboard holds text
// a person selected, not megabytes of output.
const maxClipboardBytes = 1 << 20

// observeClipboard takes an OSC 52 sequence, "52;<selection>;<base64>", and
// keeps the text a guest asked to copy. Only writes are taken: a read ("?")
// would hand the host's clipboard to whatever runs in the tab, so it is
// never answered.
func (t *embeddedTerminal) observeClipboard(data []byte) {
	if !t.live {
		return
	}
	parts := strings.SplitN(string(data), ";", 3)
	if len(parts) != 3 || parts[2] == "?" || base64.StdEncoding.DecodedLen(len(parts[2])) > maxClipboardBytes {
		return
	}
	text, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return
	}
	t.copies = append(t.copies, string(text))
}

// takeEffects returns, and clears, what the live output asked of the host.
func (t *embeddedTerminal) takeEffects() (bell bool, copies []string) {
	bell, copies = t.bell, t.copies
	t.bell, t.copies = false, nil
	return bell, copies
}

// lockModifiers are the lock keys a key event reports as modifiers. They
// change no key's meaning for a guest, so encoding ignores them.
const lockModifiers = tea.ModCapsLock | tea.ModNumLock | tea.ModScrollLock

type terminalOutputMsg struct {
	terminal     *embeddedTerminal
	data         []byte
	err          error
	inputFailure bool
}

type replaySizer interface {
	ReplaySize() (uint16, uint16)
}

func newEmbeddedTerminal(id string, stream io.ReadWriteCloser, width, height int) *embeddedTerminal {
	emulator := vt.NewSafeEmulator(width, height)
	emulator.SetScrollbackSize(10_000)
	terminal := &embeddedTerminal{
		id:         id,
		stream:     stream,
		emulator:   emulator,
		inputDone:  make(chan struct{}),
		guestMouse: make(map[ansi.DECMode]bool),
		readBuffer: make([]byte, 32*1024),
		live:       true,
	}
	emulator.SetCallbacks(vt.Callbacks{
		EnableMode:  func(mode ansi.Mode) { terminal.trackGuestMode(mode, true) },
		DisableMode: func(mode ansi.Mode) { terminal.trackGuestMode(mode, false) },
		Bell: func() {
			if terminal.live {
				terminal.bell = true
			}
		},
		AltScreen:        terminal.keyboard.SetAltScreen,
		CursorVisibility: func(visible bool) { terminal.cursorHidden = !visible },
		// The emulator names this argument blink and passes it the opposite,
		// whether the cursor is steady.
		CursorStyle: func(style vt.CursorStyle, steady bool) {
			terminal.cursorShape, terminal.cursorSteady = tea.CursorShape(style), steady
		},
	})
	clampMargins(emulator)
	terminal.followKeyboardProtocol()
	// The emulator reports a mode as recognised once it has been set or reset.
	// Resetting it here makes the answer to "do you support synchronized
	// output" yes from the first time a program asks.
	_, _ = emulator.Write([]byte(ansi.ResetModeSynchronizedOutput))
	emulator.Emulator.RegisterOscHandler(52, func(data []byte) bool {
		terminal.observeClipboard(data)
		return true
	})
	go func() {
		defer close(terminal.inputDone)
		if _, err := io.Copy(stream, emulator); err != nil {
			terminal.inputMu.Lock()
			terminal.inputErr = fmt.Errorf("write terminal input: %w", err)
			terminal.inputMu.Unlock()
			if closer, ok := emulator.InputPipe().(io.Closer); ok {
				_ = closer.Close()
			}
			_ = stream.Close()
		}
	}()
	return terminal
}

// newEmbeddedTerminalWithReplay restores at the recorded size. The caller must
// apply its final layout after attaching the terminal and setting focus: an
// intermediate shrink discards cells that a later expansion cannot recover.
func newEmbeddedTerminalWithReplay(
	id string,
	stream io.ReadWriteCloser,
	replay []byte,
	fallbackWidth, fallbackHeight int,
) *embeddedTerminal {
	replayWidth, replayHeight := fallbackWidth, fallbackHeight
	if sized, ok := stream.(replaySizer); ok {
		columns, rows := sized.ReplaySize()
		if columns > 0 && rows > 0 {
			replayWidth, replayHeight = int(columns), int(rows)
		}
	}
	terminal := newEmbeddedTerminal(id, stream, replayWidth, replayHeight)
	terminal.live = false
	terminal.writeOutput(replay)
	terminal.live = true
	return terminal
}

// clampMargins keeps the guest's scroll region inside the screen romty is
// showing. A guest keeps drawing at the size it last knew, so shrinking the
// pane leaves DECSTBM and DECSLRM in flight that name rows or columns the
// emulator no longer has. The emulator takes those margins at face value and
// then indexes its buffer with them, which panics and takes the whole
// dashboard down with it.
//
// The handlers registered here run before the emulator's own, which read the
// same parameter storage: rewriting the parameter in place and returning false
// leaves the emulator to apply a region that fits rather than one that
// crashes. The bare Emulator is used because the write lock is already held
// while a sequence is being handled.
func clampMargins(safe *vt.SafeEmulator) {
	emulator := safe.Emulator
	// Set Top and Bottom Margins [ansi.DECSTBM].
	emulator.RegisterCsiHandler('r', func(params ansi.Params) bool {
		clampParameter(params, 1, emulator.Height())
		return false
	})
	// Set Left and Right Margins [ansi.DECSLRM].
	emulator.RegisterCsiHandler('s', func(params ansi.Params) bool {
		clampParameter(params, 1, emulator.Width())
		return false
	})
}

func clampParameter(params ansi.Params, index, limit int) {
	if index >= len(params) {
		return
	}
	// A missing parameter reads as zero, which is never above the limit, so
	// the emulator keeps its own default for it.
	if params[index].Param(0) <= limit {
		return
	}
	params[index] = ansi.Param(ansi.Parameter(limit, params[index].HasMore()))
}

// followKeyboardProtocol keeps the key encoding the guest asks for and answers
// the two questions a program asks to learn what terminal it is in. The
// emulator knows none of these sequences.
//
// The answers go to the guest only while the output is live: a replayed
// question was answered when it was first asked.
func (t *embeddedTerminal) followKeyboardProtocol() {
	emulator := t.emulator.Emulator
	request := func(private byte, final byte) {
		emulator.RegisterCsiHandler(ansi.Command(private, 0, final), func(params ansi.Params) bool {
			values := make([]int, len(params))
			for index := range params {
				values[index] = params[index].Param(-1)
			}
			return t.keyboard.Apply(private, values, final)
		})
	}
	request('>', 'u')
	request('<', 'u')
	request('=', 'u')
	// XTMODKEYS shares its final byte with SGR, which has no private marker.
	request('>', 'm')
	// The Kitty keyboard flags request. Answering it is what tells a program
	// the terminal encodes keys this way; Claude Code and Codex both ask.
	emulator.RegisterCsiHandler(ansi.Command('?', 0, 'u'), func(ansi.Params) bool {
		if t.live {
			_, _ = fmt.Fprintf(emulator.InputPipe(), "\x1b[?%du", t.keyboard.Flags())
		}
		return true
	})
	// XTVERSION, the terminal name and version request.
	emulator.RegisterCsiHandler(ansi.Command('>', 0, 'q'), func(params ansi.Params) bool {
		if value, _, _ := params.Param(0, 0); value != 0 {
			return false
		}
		if t.live {
			_, _ = io.WriteString(emulator.InputPipe(), "\x1bP>|romty "+version.String()+"\x1b\\")
		}
		return true
	})
	// The state the daemon sends after a replay, which the recording alone
	// cannot give: its pushes may be gone, or still there to apply twice.
	emulator.RegisterApcHandler(func(data []byte) bool {
		return t.keyboard.Restore(string(data))
	})
}

func (t *embeddedTerminal) read() tea.Cmd {
	return func() tea.Msg {
		buffer := t.readBuffer
		count, err := t.stream.Read(buffer)
		t.inputMu.Lock()
		inputFailure := t.inputErr != nil
		if t.inputErr != nil {
			err = t.inputErr
		}
		t.inputMu.Unlock()
		return terminalOutputMsg{terminal: t, data: buffer[:count], err: err, inputFailure: inputFailure}
	}
}

func (t *embeddedTerminal) writeOutput(data []byte) {
	_, _ = t.emulator.Write(t.titles.scrub(data))
}

func (t *embeddedTerminal) sendKey(message tea.KeyPressMsg) {
	key := tea.Key(message)
	var sequence string
	key, sequence = t.prepareKeypadKey(key)
	if sequence != "" {
		_, _ = io.WriteString(t.emulator.InputPipe(), sequence)
		return
	}
	var ok bool
	key, ok = layoutControlKey(key)
	if !ok {
		return
	}
	flags := t.keyboard.Flags()
	if flags&keyboard.Disambiguate != 0 {
		if sequence, ok := kittyKey(key, flags); ok {
			_, _ = io.WriteString(t.emulator.InputPipe(), sequence)
			return
		}
	}
	if text, ok := printableKeyText(key); ok {
		t.emulator.SendText(text)
		return
	}
	key.Mod &^= lockModifiers
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
	if sequence, ok := t.otherKey(key); ok {
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

func layoutControlKey(key tea.Key) (tea.Key, bool) {
	mod := key.Mod &^ lockModifiers
	if mod&tea.ModCtrl == 0 || mod&^(tea.ModAlt|tea.ModCtrl) != 0 {
		return key, true
	}
	// Ctrl chords follow the physical key across input layouts. Without a
	// base code, a printable non-ASCII value cannot be mapped safely.
	if key.BaseCode != 0 {
		key.Code = key.BaseCode
		key.ShiftedCode = 0
		key.Text = ""
		return key, true
	}
	if key.Code > unicode.MaxASCII && unicode.IsPrint(key.Code) {
		return key, false
	}
	return key, true
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
	switch mode {
	case ansi.Mode(ansi.ModeNumericKeypad):
		t.applicationKeypad = enabled
	case ansi.Mode(ansi.ModeSynchronizedOutput):
		t.holdFrame(enabled)
	}
	t.trackGuestMouse(mode, enabled)
}

// heldFrame is the screen and cursor as they were when a synchronized update
// began.
type heldFrame struct {
	lines  []string
	cursor *tea.Cursor
	since  time.Time
}

// frameHoldLimit is how long a synchronized update may keep the screen from
// changing. A program that dies between the two halves of the mode would
// otherwise freeze its tab. It is a variable so tests need not wait it out.
var frameHoldLimit = time.Second

// holdFrame begins or ends a synchronized update (mode 2026). A program
// sets the mode, draws a whole frame and resets it, and asks that nothing be
// shown in between: the bytes of one frame arrive over several reads, and a
// screen painted between two of them shows half of the old frame under half
// of the new one.
//
// It runs inside the emulator's write, which holds its lock, so it reads the
// bare emulator. A replay is not held: it is drawn off screen in one piece,
// and rendering every frame of a long recording would only slow it down.
func (t *embeddedTerminal) holdFrame(begin bool) {
	if !begin {
		t.held = nil
		return
	}
	if !t.live || t.held != nil {
		return
	}
	emulator := t.emulator.Emulator
	t.held = &heldFrame{
		lines:  splitLines(emulator.Render(), emulator.Height()),
		cursor: t.cursorAt(emulator.CursorPosition()),
		since:  now(),
	}
}

// heldNow is the frame being held, or nil when none is or its time is up.
func (t *embeddedTerminal) heldNow() *heldFrame {
	if t.held != nil && now().Sub(t.held.since) >= frameHoldLimit {
		t.held = nil
	}
	return t.held
}

// scheduleRelease reports whether a timer is needed to end the hold in case
// the guest never does. It answers yes once for each timer.
func (t *embeddedTerminal) scheduleRelease() bool {
	if t.held == nil || t.releaseScheduled {
		return false
	}
	t.releaseScheduled = true
	return true
}

// cursorAt is the cursor to show at position, or nil when the guest hid it.
func (t *embeddedTerminal) cursorAt(position uv.Position) *tea.Cursor {
	if t.cursorHidden {
		return nil
	}
	cursor := tea.NewCursor(position.X, position.Y)
	cursor.Shape, cursor.Blink = t.cursorShape, !t.cursorSteady
	return cursor
}

// cursor is the cursor to show, relative to the terminal's own origin, or
// nil when there is none to show.
func (t *embeddedTerminal) cursor() *tea.Cursor {
	if held := t.heldNow(); held != nil {
		if held.cursor == nil {
			return nil
		}
		cursor := *held.cursor
		return &cursor
	}
	return t.cursorAt(t.emulator.CursorPosition())
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

// guestWantsMotion reports whether the guest asked for the motion reports the
// host now delivers. romty keeps all-motion tracking on for its own hover
// highlights, so a guest that asked only for clicks must not be handed every
// pointer move; button-event tracking wants motion only while a button is held.
func (t *embeddedTerminal) guestWantsMotion(dragging bool) bool {
	if t.guestMouse[ansi.ModeMouseAnyEvent] {
		return true
	}
	return dragging && t.guestMouse[ansi.ModeMouseButtonEvent]
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
	key.Mod &^= lockModifiers
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

// otherKey encodes a modified key the emulator has no bytes for, the way the
// guest asked: as plain xterm bytes when it turned modifyOtherKeys off, and
// as a modifyOtherKeys sequence otherwise.
//
// A guest that never said is sent the sequence too. Claude Code, among
// others, reads it without asking for it, and Shift+Enter would otherwise be
// the same byte as Enter there.
func (t *embeddedTerminal) otherKey(key tea.Key) (string, bool) {
	if level, set := t.keyboard.OtherKeys(); set && level == 0 {
		return legacyModifiedKey(key)
	}
	return modifiedOtherKey(key)
}

// kittyModifierMask are the modifiers the Kitty keyboard protocol encodes.
const kittyModifierMask = xtermModifierMask | tea.ModSuper | tea.ModHyper

// kittyKey encodes a key the way the Kitty keyboard protocol's "disambiguate
// escape codes" flag asks, as CSI code ; modifiers u. It covers the keys that
// flag changes: Escape, a modified Enter, Tab or Backspace, and a text key
// held with anything but Shift. Every other key keeps the encoding it has
// without the protocol, which is what the protocol specifies for them.
func kittyKey(key tea.Key, flags int) (string, bool) {
	mod := key.Mod & kittyModifierMask
	code, special := modifiedOtherKeyCodes[key.Code]
	switch {
	case key.Code == tea.KeyEscape:
	case special && key.Code != tea.KeySpace:
		// Unmodified, these stay the bytes a shell reads, so a prompt is
		// still usable after a program dies with its flags set.
		if mod == 0 {
			return "", false
		}
	case key.Code <= unicode.MaxRune && unicode.IsPrint(key.Code):
		// Alone or with Shift the key is text.
		if mod&^tea.ModShift == 0 {
			return "", false
		}
		code = int(unicode.ToLower(key.Code))
	default:
		return "", false
	}
	var sequence strings.Builder
	fmt.Fprintf(&sequence, "\x1b[%d", code)
	if flags&keyboard.AlternateKeys != 0 && mod&tea.ModShift != 0 &&
		key.ShiftedCode != 0 && key.ShiftedCode != key.Code {
		fmt.Fprintf(&sequence, ":%d", key.ShiftedCode)
	}
	if parameter := kittyModifierParameter(mod); parameter > 1 {
		fmt.Fprintf(&sequence, ";%d", parameter)
	}
	sequence.WriteByte('u')
	return sequence.String(), true
}

// kittyModifierParameter is the protocol's one-based modifier encoding. It
// agrees with xterm's on Shift, Alt and Ctrl and parts with it after them.
func kittyModifierParameter(mod tea.KeyMod) int {
	parameter := 1
	for _, modifier := range [...]struct {
		bit    tea.KeyMod
		weight int
	}{
		{tea.ModShift, 1}, {tea.ModAlt, 2}, {tea.ModCtrl, 4},
		{tea.ModSuper, 8}, {tea.ModHyper, 16}, {tea.ModMeta, 32},
	} {
		if mod&modifier.bit != 0 {
			parameter += modifier.weight
		}
	}
	return parameter
}

// legacyControlBytes are the control characters xterm sends for Ctrl held
// with a key that is not a letter.
var legacyControlBytes = map[rune]byte{
	'2': 0x00, '@': 0x00, '3': 0x1b, '4': 0x1c, '5': 0x1d, '6': 0x1e,
	'7': 0x1f, '/': 0x1f, '8': 0x7f, '?': 0x7f,
}

// legacyModifiedKey is what a terminal with no extended keyboard protocol
// sends for the keys modifiedOtherKey covers: the modifier is dropped where
// the legacy encoding has no room for it, so Shift+Enter is Enter.
func legacyModifiedKey(key tea.Key) (string, bool) {
	mod := key.Mod & xtermModifierMask
	if mod&^tea.ModAlt == 0 || emulatorHandlesModifiedKey(key.Code, mod) {
		return "", false
	}
	var sequence string
	switch key.Code {
	case tea.KeyEnter:
		sequence = "\r"
	case tea.KeyTab:
		sequence = "\t"
	case tea.KeyEscape:
		sequence = "\x1b"
	case tea.KeyBackspace:
		sequence = "\x7f"
		if mod&tea.ModCtrl != 0 {
			sequence = "\x08"
		}
	default:
		value := key.Code
		if mod&tea.ModShift != 0 && key.ShiftedCode != 0 {
			value = key.ShiftedCode
		}
		if value > unicode.MaxRune || !unicode.IsPrint(value) {
			return "", false
		}
		sequence = string(value)
		if mod&tea.ModCtrl != 0 {
			if control, ok := legacyControlBytes[value]; ok {
				sequence = string(rune(control))
			} else if lower := unicode.ToLower(value); lower >= 'a' && lower <= 'z' {
				sequence = string(lower - 'a' + 1)
			}
		}
	}
	if mod&tea.ModAlt != 0 {
		sequence = "\x1b" + sequence
	}
	return sequence, true
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
	for _, modifier := range [...]struct {
		bit    tea.KeyMod
		weight int
	}{{tea.ModShift, 1}, {tea.ModAlt, 2}, {tea.ModCtrl, 4}, {tea.ModMeta, 8}} {
		if mod&modifier.bit != 0 {
			parameter += modifier.weight
		}
	}
	return parameter
}

func (t *embeddedTerminal) paste(value string) {
	t.emulator.Paste(value)
}

func (t *embeddedTerminal) resize(width, height int) {
	// A held frame was drawn for the old size.
	t.held = nil
	t.emulator.Resize(width, height)
}

// setFocus tells the guest it gained or lost the keyboard. The emulator sends
// the report only to a guest that asked for focus events.
func (t *embeddedTerminal) setFocus(focused bool) {
	if t.focused == focused {
		return
	}
	t.focused = focused
	if focused {
		t.emulator.Focus()
	} else {
		t.emulator.Blur()
	}
}

// setHostColors gives the emulator the colours of the terminal romty runs in,
// which is what a guest's colour query is really asking about. A program
// picks its palette from the answer, and the emulator's own answer is white
// on black whatever the user is looking at. A nil colour is one the host has
// not reported.
func (t *embeddedTerminal) setHostColors(foreground, background color.Color) {
	if foreground != nil {
		t.emulator.SetDefaultForegroundColor(foreground)
	}
	if background != nil {
		t.emulator.SetDefaultBackgroundColor(background)
	}
}

// size is the current viewport. Resize commands read it while holding the
// dashboard's resize lock so their round trips cannot leave the PTY stale.
func (t *embeddedTerminal) size() (uint16, uint16) {
	return clampSize(t.emulator.Width()), clampSize(t.emulator.Height())
}

func clampSize(value int) uint16 {
	return uint16(min(max(value, 0), 65535))
}

func (t *embeddedTerminal) render() []string {
	if held := t.heldNow(); held != nil {
		return held.lines
	}
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

// searchLines lists the lines a query appears on, oldest first, as absolute
// line numbers: the retained history and then the live screen, which is the
// coordinate renderViewport scrolls in.
//
// Matching is on the rendered text with its styling removed, so a phrase split
// by a colour change still matches what the user sees.
func (t *embeddedTerminal) searchLines(query string) []int {
	if query == "" {
		return nil
	}
	needle := strings.ToLower(query)
	matches := make([]int, 0)
	// scrollbackLen, not the buffer's own length: an application that owns the
	// screen has no history to scroll, and the viewport agrees.
	historyLen := t.scrollbackLen()
	history := t.emulator.Scrollback()
	for index := range historyLen {
		if lineMatches(history.Line(index).Render(), needle) {
			matches = append(matches, index)
		}
	}
	for index, line := range t.render() {
		if lineMatches(line, needle) {
			matches = append(matches, historyLen+index)
		}
	}
	return matches
}

func lineMatches(line, needle string) bool {
	return strings.Contains(strings.ToLower(ansi.Strip(line)), needle)
}

// inputDrainLimit is how long close waits for input already written to reach
// the daemon. It only runs out when the daemon has stopped reading.
var inputDrainLimit = 100 * time.Millisecond

// close lets go of the daemon and of everything the emulator holds. It is
// safe to call more than once.
//
// The guest is told it lost the focus first, and the input is closed before
// the stream so that report, like any key still in flight, is written rather
// than cut off by the stream closing under it.
func (t *embeddedTerminal) close() {
	t.closeOnce.Do(func() {
		t.closed.Store(true)
		t.setFocus(false)
		if closer, ok := t.emulator.InputPipe().(io.Closer); ok {
			_ = closer.Close()
		}
		select {
		case <-t.inputDone:
		case <-time.After(inputDrainLimit):
		}
		_ = t.stream.Close()
		<-t.inputDone
		_ = t.emulator.Close()
	})
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
