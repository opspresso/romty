package ui

import (
	"strings"
	"testing"
)

// The reason the scrubber exists: the emulator reads the 0x9C inside "✳" and
// "서" as the C1 string terminator, ends the title there, and prints the rest
// of it as text at the cursor — a sentence in the guest's input box that
// nobody typed.
func TestGuestTitleNeverRendersAsText(t *testing.T) {
	value := []byte("\x1b]0;✳ 문서 개선\aafter")
	for cut := 0; cut <= len(value); cut++ {
		terminal := newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 5)
		terminal.writeOutput(value[:cut])
		terminal.writeOutput(value[cut:])
		rendered := strings.Join(terminal.render(), "\n")
		terminal.close()
		if strings.Contains(rendered, "문서") {
			t.Fatalf("cut at %d: title rendered as text: %q", cut, rendered)
		}
		if !strings.Contains(rendered, "after") {
			t.Fatalf("cut at %d: text after the title is gone: %q", cut, rendered)
		}
	}
}

// A hyperlink the guest prints reaches the host terminal in the standard
// order, OSC 8 ; params ; URI, even when it arrives in pieces.
func TestHyperlinksReachTheHostIntact(t *testing.T) {
	for _, chunks := range [][]string{
		{"\x1b]8;;https://romty.dev\x1b\\romty\x1b]8;;\x1b\\ site\r\n"},
		{"\x1b]8;id=1;https://ro", "mty.dev\aromty\x1b]8;;\a site\r\n"},
	} {
		terminal := newEmbeddedTerminal("x", newMemoryStream(""), 40, 4)
		for _, chunk := range chunks {
			terminal.writeOutput([]byte(chunk))
		}
		row := terminal.renderViewport(0)[0]
		terminal.close()
		if !strings.Contains(row, "8;id=1;https://romty.dev") && !strings.Contains(row, "8;;https://romty.dev") {
			t.Fatalf("rendered link = %q, want OSC 8 ; params ; URI", row)
		}
	}
}
