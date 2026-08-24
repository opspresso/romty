package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestEmbeddedTerminalForwardsKeysAndPaste(t *testing.T) {
	stream := newMemoryStream("")
	terminal := newEmbeddedTerminal("tab-1", stream, 40, 10)
	defer terminal.closeTerminal()

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
	defer terminal.closeTerminal()

	terminal.writeOutput([]byte("hello\r\n\x1b[31mred\x1b[0m"))
	rendered := strings.Join(terminal.render(), "\n")
	if !strings.Contains(rendered, "hello") || !strings.Contains(rendered, "red") {
		t.Fatalf("rendered terminal = %q", rendered)
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

		// Still the emulator's job, and it gets these right.
		{name: "pgup", key: tea.Key{Code: tea.KeyPgUp}, want: "\x1b[5~"},
		{name: "home", key: tea.Key{Code: tea.KeyHome}, want: "\x1b[H"},
		{name: "up", key: tea.Key{Code: tea.KeyUp}, want: "\x1b[A"},
		{name: "shift+tab", key: tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}, want: "\x1b[Z"},
		{name: "alt+left", key: tea.Key{Code: tea.KeyLeft, Mod: tea.ModAlt}, want: "\x1b\x1b[D"},
		{name: "ctrl+c", key: tea.Key{Code: 'c', Mod: tea.ModCtrl}, want: "\x03"},
		{name: "a plain rune", key: tea.Key{Code: 'x', Text: "x"}, want: "x"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			stream := newMemoryStream("")
			terminal := newEmbeddedTerminal("tab-1", stream, 40, 10)
			defer terminal.closeTerminal()

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
