package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

type replaySizedMemoryStream struct {
	*memoryStream
	columns uint16
	rows    uint16
}

func (s replaySizedMemoryStream) ReplaySize() (uint16, uint16) {
	return s.columns, s.rows
}

func TestEmbeddedTerminalForwardsKeysAndPaste(t *testing.T) {
	stream := newMemoryStream("")
	terminal := newEmbeddedTerminal("tab-1", stream, 40, 10)
	defer terminal.close()

	terminal.sendKey(key('x', "x"))
	terminal.sendKey(key(tea.KeyEnter, ""))
	terminal.paste("pasted")

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stream.String(), "x\rpasted") {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("terminal input = %q, want key and paste bytes", stream.String())
}

func TestEmbeddedTerminalRendersANSIOutput(t *testing.T) {
	stream := newMemoryStream("")
	terminal := newEmbeddedTerminal("tab-1", stream, 20, 5)
	defer terminal.close()

	terminal.writeOutput([]byte("hello\r\n\x1b[31mred\x1b[0m"))
	rendered := strings.Join(terminal.render(), "\n")
	if !strings.Contains(rendered, "hello") || !strings.Contains(rendered, "red") {
		t.Fatalf("rendered terminal = %q", rendered)
	}
}

func TestZshPartialLineMarkerNeedsItsRecordedWidth(t *testing.T) {
	const recordedWidth = 80
	replay := []byte("abc\x1b[1m\x1b[7m%\x1b[27m\x1b[1m\x1b[0m " +
		strings.Repeat(" ", recordedWidth-len("abc")-2) +
		"\r \r\r\x1b[0m\x1b[27m\x1b[24m\x1b[JPROMPT> ")

	direct := newEmbeddedTerminal("direct", newMemoryStream(""), 40, 6)
	direct.writeOutput(replay)
	directScreen := strings.Join(direct.render(), "\n")
	direct.close()

	stream := replaySizedMemoryStream{
		memoryStream: newMemoryStream(""), columns: recordedWidth, rows: 6,
	}
	restored := newEmbeddedTerminalWithReplay("restored", stream, replay, 40, 6)
	restoredScreen := strings.Join(restored.render(), "\n")
	restored.close()

	if !strings.Contains(directScreen, "%") {
		t.Fatalf("wrong-width replay did not reproduce the zsh marker:\n%s", directScreen)
	}
	if strings.Contains(restoredScreen, "%") {
		t.Fatalf("recorded-width replay retained the zsh marker:\n%s", restoredScreen)
	}
}

func TestEmbeddedTerminalForwardsKeypad(t *testing.T) {
	for _, probe := range []struct {
		name        string
		application bool
		key         tea.Key
		want        string
	}{
		{name: "number", key: tea.Key{Code: tea.KeyKp1, Text: "1", Mod: tea.ModNumLock}, want: "1"},
		{name: "application number", application: true, key: tea.Key{Code: tea.KeyKp1, Text: "1", Mod: tea.ModNumLock}, want: "\x1bOq"},
		{name: "divide", key: tea.Key{Code: tea.KeyKpDivide, Text: "/", Mod: tea.ModNumLock}, want: "/"},
		{name: "application divide", application: true, key: tea.Key{Code: tea.KeyKpDivide, Text: "/", Mod: tea.ModNumLock}, want: "\x1bOo"},
		{name: "navigation", key: tea.Key{Code: tea.KeyKpLeft}, want: "\x1b[D"},
		{name: "application navigation", application: true, key: tea.Key{Code: tea.KeyKpLeft}, want: "\x1bOt"},
		{name: "begin", key: tea.Key{Code: tea.KeyKpBegin}, want: "\x1b[E"},
		{name: "application begin", application: true, key: tea.Key{Code: tea.KeyKpBegin}, want: "\x1bOu"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			stream := newMemoryStream("")
			terminal := newEmbeddedTerminal("tab-1", stream, 40, 10)
			defer terminal.close()
			if probe.application {
				terminal.writeOutput([]byte("\x1b="))
			}
			terminal.sendKey(tea.KeyPressMsg(probe.key))

			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				if stream.String() == probe.want {
					return
				}
				time.Sleep(time.Millisecond)
			}
			t.Fatalf("guest received %q, want %q", stream.String(), probe.want)
		})
	}
}

// The emulator drops special keys held with Shift, Ctrl or Meta, so romty
// encodes them itself. This table pins both halves: what romty now sends, and
// what it still leaves to the emulator.
func TestEmbeddedTerminalForwardsModifiedKeys(t *testing.T) {
	for _, probe := range []struct {
		name string
		key  tea.Key
		want string
	}{
		{name: "left", key: tea.Key{Code: tea.KeyLeft}, want: "\x1b[D"},
		{name: "ctrl+left", key: tea.Key{Code: tea.KeyLeft, Mod: tea.ModCtrl}, want: "\x1b[1;5D"},
		{name: "ctrl+right", key: tea.Key{Code: tea.KeyRight, Mod: tea.ModCtrl}, want: "\x1b[1;5C"},
		{name: "shift+up", key: tea.Key{Code: tea.KeyUp, Mod: tea.ModShift}, want: "\x1b[1;2A"},
		{name: "shift+down", key: tea.Key{Code: tea.KeyDown, Mod: tea.ModShift}, want: "\x1b[1;2B"},
		{name: "ctrl+shift+left", key: tea.Key{Code: tea.KeyLeft, Mod: tea.ModCtrl | tea.ModShift}, want: "\x1b[1;6D"},
		{name: "ctrl+home", key: tea.Key{Code: tea.KeyHome, Mod: tea.ModCtrl}, want: "\x1b[1;5H"},
		{name: "ctrl+end", key: tea.Key{Code: tea.KeyEnd, Mod: tea.ModCtrl}, want: "\x1b[1;5F"},
		{name: "shift+pgup", key: tea.Key{Code: tea.KeyPgUp, Mod: tea.ModShift}, want: "\x1b[5;2~"},
		{name: "ctrl+delete", key: tea.Key{Code: tea.KeyDelete, Mod: tea.ModCtrl}, want: "\x1b[3;5~"},
		{name: "alt+ctrl+left", key: tea.Key{Code: tea.KeyLeft, Mod: tea.ModAlt | tea.ModCtrl}, want: "\x1b[1;7D"},
		{name: "an uppercase rune", key: tea.Key{Code: 'a', ShiftedCode: 'A', Text: "A", Mod: tea.ModShift}, want: "A"},
		{name: "a caps lock rune", key: tea.Key{Code: 'a', Text: "A", Mod: tea.ModCapsLock}, want: "A"},
		{name: "alt+uppercase rune", key: tea.Key{Code: 'a', ShiftedCode: 'A', Mod: tea.ModAlt | tea.ModShift}, want: "\x1bA"},
		{name: "a shifted symbol", key: tea.Key{Code: '1', ShiftedCode: '!', Text: "!", Mod: tea.ModShift}, want: "!"},
		{name: "layout text", key: tea.Key{Code: 'q', BaseCode: 'q', Text: "a"}, want: "a"},
		{name: "a grapheme cluster", key: tea.Key{Code: tea.KeyExtended, Text: "👨‍💻"}, want: "👨‍💻"},
		{name: "keypad text", key: tea.Key{Code: tea.KeyKpDivide, Text: "/", Mod: tea.ModNumLock}, want: "/"},
		{name: "shift+space", key: tea.Key{Code: tea.KeySpace, Text: " ", Mod: tea.ModShift}, want: " "},
		{name: "shift+enter", key: tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}, want: "\x1b[27;2;13~"},
		{name: "ctrl+enter", key: tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl}, want: "\x1b[27;5;13~"},
		{name: "ctrl+tab", key: tea.Key{Code: tea.KeyTab, Mod: tea.ModCtrl}, want: "\x1b[27;5;9~"},
		{name: "shift+backspace", key: tea.Key{Code: tea.KeyBackspace, Mod: tea.ModShift}, want: "\x1b[27;2;127~"},
		{name: "ctrl+shift+c", key: tea.Key{Code: 'c', ShiftedCode: 'C', Mod: tea.ModCtrl | tea.ModShift}, want: "\x1b[27;6;67~"},
		{name: "begin", key: tea.Key{Code: tea.KeyBegin}, want: "\x1b[E"},
		{name: "find", key: tea.Key{Code: tea.KeyFind}, want: "\x1b[1~"},
		{name: "shift+find", key: tea.Key{Code: tea.KeyFind, Mod: tea.ModShift}, want: "\x1b[1;2~"},
		{name: "select", key: tea.Key{Code: tea.KeySelect}, want: "\x1b[4~"},
		{name: "f13", key: tea.Key{Code: tea.KeyF13}, want: "\x1b[25~"},
		{name: "shift+f13", key: tea.Key{Code: tea.KeyF13, Mod: tea.ModShift}, want: "\x1b[25;2~"},
		{name: "alt+f13", key: tea.Key{Code: tea.KeyF13, Mod: tea.ModAlt}, want: "\x1b\x1b[25~"},
		{name: "f20", key: tea.Key{Code: tea.KeyF20}, want: "\x1b[34~"},

		// Still the emulator's job, and it gets these right.
		{name: "pgup", key: tea.Key{Code: tea.KeyPgUp}, want: "\x1b[5~"},
		{name: "home", key: tea.Key{Code: tea.KeyHome}, want: "\x1b[H"},
		{name: "up", key: tea.Key{Code: tea.KeyUp}, want: "\x1b[A"},
		{name: "shift+tab", key: tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}, want: "\x1b[Z"},
		{name: "alt+left", key: tea.Key{Code: tea.KeyLeft, Mod: tea.ModAlt}, want: "\x1b\x1b[D"},
		{name: "ctrl+c", key: tea.Key{Code: 'c', Mod: tea.ModCtrl}, want: "\x03"},
		{name: "ctrl+c with caps lock", key: tea.Key{Code: 'c', Mod: tea.ModCtrl | tea.ModCapsLock}, want: "\x03"},
		{name: "ctrl+c with Korean layout", key: tea.Key{Code: 'ㅊ', BaseCode: 'c', Mod: tea.ModCtrl}, want: "\x03"},
		{name: "ctrl+d with Korean layout", key: tea.Key{Code: 'ㅇ', BaseCode: 'd', Mod: tea.ModCtrl}, want: "\x04"},
		{name: "alt+ctrl+c", key: tea.Key{Code: 'c', Mod: tea.ModAlt | tea.ModCtrl}, want: "\x1b\x03"},
		{name: "alt+ctrl+c with Korean layout", key: tea.Key{Code: 'ㅊ', BaseCode: 'c', Mod: tea.ModAlt | tea.ModCtrl}, want: "\x1b\x03"},
		{name: "ctrl+space", key: tea.Key{Code: tea.KeySpace, Mod: tea.ModCtrl}, want: "\x00"},
		{name: "a repeated arrow", key: tea.Key{Code: tea.KeyUp, IsRepeat: true}, want: "\x1b[A"},
		{name: "a plain rune", key: tea.Key{Code: 'x', Text: "x"}, want: "x"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			stream := newMemoryStream("")
			terminal := newEmbeddedTerminal("tab-1", stream, 40, 10)
			defer terminal.close()

			terminal.sendKey(tea.KeyPressMsg(probe.key))
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				if stream.String() == probe.want {
					return
				}
				time.Sleep(time.Millisecond)
			}
			t.Fatalf("guest received %q, want %q", stream.String(), probe.want)
		})
	}
}

func TestEmbeddedTerminalIgnoresUnidentifiedNonASCIIControlKey(t *testing.T) {
	terminal := newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)
	defer terminal.close()

	terminal.sendKey(tea.KeyPressMsg(tea.Key{Code: 'ㅊ', Mod: tea.ModCtrl}))
	waitForGuestSilence(t, terminal, "")
}

func TestEmbeddedTerminalClampsScrollRegion(t *testing.T) {
	stream := newMemoryStream("")
	terminal := newEmbeddedTerminal("tab-1", stream, 20, 5)
	defer terminal.close()

	// A guest that has not seen the pane shrink yet asks for a scroll region
	// taller than the screen, then scrolls inside it.
	terminal.writeOutput([]byte("\x1b[1;40r"))
	terminal.writeOutput([]byte("a\x1bM"))

	lines := terminal.render()
	if len(lines) != 5 {
		t.Fatalf("rendered %d lines, want 5", len(lines))
	}
	if !strings.Contains(lines[1], "a") {
		t.Fatalf("rendered terminal = %q, want the reverse index to scroll %q onto the second row", lines, "a")
	}
}

func TestEmbeddedTerminalClampsHorizontalMargins(t *testing.T) {
	stream := newMemoryStream("")
	terminal := newEmbeddedTerminal("tab-1", stream, 20, 5)
	defer terminal.close()

	// Left and right margin mode, then margins wider than the screen, then an
	// insert that walks the whole region.
	terminal.writeOutput([]byte("\x1b[?69h\x1b[1;80s"))
	terminal.writeOutput([]byte("ab\x1b[H\x1b[2@"))

	lines := terminal.render()
	if !strings.Contains(lines[0], "  ab") {
		t.Fatalf("rendered terminal = %q, want the insert to push %q right", lines, "ab")
	}
}

func TestEmbeddedTerminalKeepsScrollRegionInRange(t *testing.T) {
	stream := newMemoryStream("")
	terminal := newEmbeddedTerminal("tab-1", stream, 20, 5)
	defer terminal.close()

	// A region the screen can hold is left exactly as the guest asked: the
	// last row sits outside it and must not move.
	terminal.writeOutput([]byte("\x1b[5;1Hz\x1b[1;3r"))
	terminal.writeOutput([]byte("a\x1bM"))

	lines := terminal.render()
	if !strings.Contains(lines[1], "a") {
		t.Fatalf("rendered terminal = %q, want %q scrolled onto the second row", lines, "a")
	}
	if !strings.Contains(lines[4], "z") {
		t.Fatalf("rendered terminal = %q, want %q left below the scroll region", lines, "z")
	}
}
