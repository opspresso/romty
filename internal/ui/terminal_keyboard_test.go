package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Codex pushes Kitty keyboard flags and reads only that encoding: it drops a
// modifyOtherKeys sequence unread, so Shift+Enter did nothing in it.
func TestEmbeddedTerminalEncodesKeysTheWayTheGuestAsked(t *testing.T) {
	const (
		codex      = "\x1b[>4;0m\x1b[>7u"
		claudeCode = "\x1b[>5u\x1b[>4;2m"
		plainXterm = "\x1b[>4;0m"
	)
	for _, probe := range []struct {
		name  string
		guest string
		key   tea.Key
		want  string
	}{
		{name: "shift+enter for Codex", guest: codex, key: tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}, want: "\x1b[13;2u"},
		{name: "ctrl+enter for Codex", guest: codex, key: tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl}, want: "\x1b[13;5u"},
		{name: "alt+enter for Codex", guest: codex, key: tea.Key{Code: tea.KeyEnter, Mod: tea.ModAlt}, want: "\x1b[13;3u"},
		{name: "shift+enter for Claude Code", guest: claudeCode, key: tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}, want: "\x1b[13;2u"},
		{name: "escape", guest: codex, key: tea.Key{Code: tea.KeyEscape}, want: "\x1b[27u"},
		{name: "shift+tab", guest: codex, key: tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}, want: "\x1b[9;2u"},
		{name: "ctrl+backspace", guest: codex, key: tea.Key{Code: tea.KeyBackspace, Mod: tea.ModCtrl}, want: "\x1b[127;5u"},
		{name: "ctrl+c", guest: codex, key: tea.Key{Code: 'c', Mod: tea.ModCtrl}, want: "\x1b[99;5u"},
		{name: "ctrl+c with Korean layout", guest: codex, key: tea.Key{Code: 'ㅊ', BaseCode: 'c', Mod: tea.ModCtrl}, want: "\x1b[99;5u"},
		{name: "ctrl+c with caps lock", guest: codex, key: tea.Key{Code: 'c', Mod: tea.ModCtrl | tea.ModCapsLock}, want: "\x1b[99;5u"},
		{name: "alt+b", guest: codex, key: tea.Key{Code: 'b', Text: "b", Mod: tea.ModAlt}, want: "\x1b[98;3u"},
		{name: "ctrl+space", guest: codex, key: tea.Key{Code: tea.KeySpace, Mod: tea.ModCtrl}, want: "\x1b[32;5u"},
		{name: "super+k", guest: codex, key: tea.Key{Code: 'k', Mod: tea.ModSuper}, want: "\x1b[107;9u"},
		{name: "the shifted key rides along when asked for", guest: codex,
			key: tea.Key{Code: 'a', ShiftedCode: 'A', Mod: tea.ModAlt | tea.ModShift}, want: "\x1b[97:65;4u"},
		{name: "the shifted key stays out when not asked for", guest: "\x1b[>1u",
			key: tea.Key{Code: 'a', ShiftedCode: 'A', Mod: tea.ModAlt | tea.ModShift}, want: "\x1b[97;4u"},

		// What the protocol leaves alone, so a shell still works when a
		// program dies with its flags set.
		{name: "enter", guest: codex, key: tea.Key{Code: tea.KeyEnter}, want: "\r"},
		{name: "tab", guest: codex, key: tea.Key{Code: tea.KeyTab}, want: "\t"},
		{name: "backspace", guest: codex, key: tea.Key{Code: tea.KeyBackspace}, want: "\x7f"},
		{name: "a plain rune", guest: codex, key: tea.Key{Code: 'x', Text: "x"}, want: "x"},
		{name: "an uppercase rune", guest: codex, key: tea.Key{Code: 'a', ShiftedCode: 'A', Text: "A", Mod: tea.ModShift}, want: "A"},
		{name: "shift+space", guest: codex, key: tea.Key{Code: tea.KeySpace, Text: " ", Mod: tea.ModShift}, want: " "},
		{name: "a grapheme cluster", guest: codex, key: tea.Key{Code: tea.KeyExtended, Text: "한"}, want: "한"},
		{name: "up", guest: codex, key: tea.Key{Code: tea.KeyUp}, want: "\x1b[A"},
		{name: "ctrl+left", guest: codex, key: tea.Key{Code: tea.KeyLeft, Mod: tea.ModCtrl}, want: "\x1b[1;5D"},
		{name: "shift+pgup", guest: codex, key: tea.Key{Code: tea.KeyPgUp, Mod: tea.ModShift}, want: "\x1b[5;2~"},

		// The flags are popped when the program leaves.
		{name: "shift+enter after Codex left", guest: codex + "\x1b[<1u", key: tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}, want: "\r"},
		{name: "ctrl+c after Codex left", guest: codex + "\x1b[<1u", key: tea.Key{Code: 'c', Mod: tea.ModCtrl}, want: "\x03"},

		// A guest that turned modifyOtherKeys off reads plain xterm bytes.
		{name: "shift+enter without the protocol", guest: plainXterm, key: tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}, want: "\r"},
		{name: "alt+shift+enter without the protocol", guest: plainXterm, key: tea.Key{Code: tea.KeyEnter, Mod: tea.ModAlt | tea.ModShift}, want: "\x1b\r"},
		{name: "ctrl+tab without the protocol", guest: plainXterm, key: tea.Key{Code: tea.KeyTab, Mod: tea.ModCtrl}, want: "\t"},
		{name: "shift+backspace without the protocol", guest: plainXterm, key: tea.Key{Code: tea.KeyBackspace, Mod: tea.ModShift}, want: "\x7f"},
		{name: "ctrl+backspace without the protocol", guest: plainXterm, key: tea.Key{Code: tea.KeyBackspace, Mod: tea.ModCtrl}, want: "\x08"},
		{name: "ctrl+shift+c without the protocol", guest: plainXterm, key: tea.Key{Code: 'c', ShiftedCode: 'C', Mod: tea.ModCtrl | tea.ModShift}, want: "\x03"},
		{name: "ctrl+/ without the protocol", guest: plainXterm, key: tea.Key{Code: '/', Mod: tea.ModCtrl}, want: "\x1f"},
		{name: "ctrl+1 without the protocol", guest: plainXterm, key: tea.Key{Code: '1', Mod: tea.ModCtrl}, want: "1"},

		// A guest that said nothing keeps the sequence romty always sent.
		{name: "shift+enter for a guest that asked nothing", key: tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}, want: "\x1b[27;2;13~"},
		{name: "shift+enter after modifyOtherKeys was reset", guest: plainXterm + "\x1b[>4m", key: tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}, want: "\x1b[27;2;13~"},
		{name: "shift+enter with modifyOtherKeys on", guest: "\x1b[>4;2m", key: tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}, want: "\x1b[27;2;13~"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			terminal := newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)
			defer terminal.close()

			terminal.writeOutput([]byte(probe.guest))
			terminal.sendKey(tea.KeyPressMsg(probe.key))
			waitForGuestSilence(t, terminal, probe.want)
		})
	}
}

// Each screen keeps its own flags, so a full-screen program's push does not
// follow the user back to the shell's screen.
func TestEmbeddedTerminalKeepsKeyboardFlagsPerScreen(t *testing.T) {
	terminal := newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)
	defer terminal.close()

	terminal.writeOutput([]byte("\x1b[?1049h\x1b[>1u"))
	terminal.sendKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	sent := waitForGuestSilence(t, terminal, "\x1b[27u")

	terminal.writeOutput([]byte("\x1b[?1049l"))
	terminal.sendKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	waitForGuestSilence(t, terminal, sent+"\x1b")
}

// Answering the flags request is what tells a program the protocol is there;
// without an answer Claude Code and Codex both conclude it is not.
func TestEmbeddedTerminalAnswersWhatTerminalItIs(t *testing.T) {
	terminal := newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)
	defer terminal.close()

	terminal.writeOutput([]byte("\x1b[?u"))
	sent := waitForGuestSilence(t, terminal, "\x1b[?0u")
	terminal.writeOutput([]byte("\x1b[>7u\x1b[?u"))
	sent = waitForGuestSilence(t, terminal, sent+"\x1b[?5u")

	terminal.writeOutput([]byte("\x1b[>0q"))
	stream := terminal.stream.(*memoryStream)
	waitForGuest(t, stream, sent+"\x1bP>|romty ")
	waitForGuest(t, stream, "\x1b\\")
}

// A replayed question was answered when it was first asked. Answering it
// again types the reply at whatever is reading the terminal now.
func TestEmbeddedTerminalDoesNotAnswerAReplay(t *testing.T) {
	replay := []byte("\x1b[?u\x1b[>0q\x1b[>q")
	terminal := newEmbeddedTerminalWithReplay("tab-1", newMemoryStream(""), replay, 40, 10)
	defer terminal.close()

	waitForGuestSilence(t, terminal, "")
}

// The recording of a running Codex holds its push, and the daemon's own
// record of the modes is replayed before it. Without the report that closes
// the replay the flags would be held twice and outlive the pop Codex sends
// when it leaves, encoding Ctrl+C for a shell that reads it as text.
func TestEmbeddedTerminalTakesTheKeyboardStateThatClosesAReplay(t *testing.T) {
	replay := []byte("\x1b[>7u\x1b[>7u\x1b[>4;2m" + "\x1b_romty:keyboard;5;;0\x1b\\")
	terminal := newEmbeddedTerminalWithReplay("tab-1", newMemoryStream(""), replay, 40, 10)
	defer terminal.close()

	terminal.sendKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}))
	sent := waitForGuestSilence(t, terminal, "\x1b[13;2u")

	terminal.writeOutput([]byte("\x1b[<1u"))
	terminal.sendKey(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	sent = waitForGuestSilence(t, terminal, sent+"\x03")
	terminal.sendKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}))
	waitForGuestSilence(t, terminal, sent+"\r")
}

// A session restored after a stop replays what a dead program printed. Its
// pushes belong to a process that is gone, and the report says so.
func TestEmbeddedTerminalForgetsTheKeyboardStateOfADeadGuest(t *testing.T) {
	replay := []byte("\x1b[>7u\x1b[>4;0m" + "\x1b_romty:keyboard;;;\x1b\\")
	terminal := newEmbeddedTerminalWithReplay("tab-1", newMemoryStream(""), replay, 40, 10)
	defer terminal.close()

	terminal.sendKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	sent := waitForGuestSilence(t, terminal, "\x1b")
	terminal.sendKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}))
	waitForGuestSilence(t, terminal, sent+"\x1b[27;2;13~")
}
