package display_test

import (
	"strings"
	"testing"

	"github.com/opspresso/romty/internal/display"
)

// A name, a path or a branch reaches romty from a filesystem or a repository
// and is written straight to the user's terminal. A control character in one
// is not text: it moves the cursor, clears the screen, or opens a sequence the
// terminal answers into the shell of whoever is looking at it.
func TestTextNeutralizesControlCharacters(t *testing.T) {
	for _, probe := range []struct{ name, value, want string }{
		{name: "ordinary text", value: "romty", want: "romty"},
		{name: "text romty must not alter", value: "café ✓ 한국어", want: "café ✓ 한국어"},
		{name: "an escape sequence", value: "a\x1b[2Jb", want: "a�[2Jb"},
		{name: "a newline", value: "a\nb", want: "a�b"},
		{name: "a carriage return", value: "a\rb", want: "a�b"},
		{name: "a bell", value: "a\x07b", want: "a�b"},
		{name: "a C1 introducer", value: "ab", want: "a�b"},
		{name: "a NUL", value: "a\x00b", want: "a�b"},
		{name: "nothing at all", value: "", want: ""},
	} {
		t.Run(probe.name, func(t *testing.T) {
			if got := display.Text(probe.value); got != probe.want {
				t.Fatalf("Text(%q) = %q, want %q", probe.value, got, probe.want)
			}
		})
	}
}

// Every control character becomes exactly one rune, so a caller that measured
// a name before writing it measured the number of cells it writes.
func TestTextKeepsTheRuneCount(t *testing.T) {
	value := "a\x1b\x07\r\n\x00b"
	if got, want := len([]rune(display.Text(value))), len([]rune(value)); got != want {
		t.Fatalf("Text(%q) is %d runes, want %d", value, got, want)
	}
	if strings.ContainsFunc(display.Text(value), func(character rune) bool {
		return character < 0x20
	}) {
		t.Fatal("Text left a control character standing")
	}
}
