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
