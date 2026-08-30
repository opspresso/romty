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
		// The single-byte form of ESC [, which a terminal reads the same way.
		{name: "a C1 introducer", value: "a\u009bb", want: "a\ufffdb"},
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

// Byte scanners that react to C1 control bytes ask Multibyte first, because
// every C1 byte also occurs as a continuation byte inside ordinary text.
func TestMultibyteGroupsCharactersAndLeavesOtherBytesAlone(t *testing.T) {
	for _, probe := range []struct {
		name   string
		value  string
		length int
		split  bool
	}{
		{name: "ASCII", value: "a", length: 0},
		{name: "a two-byte character", value: "é", length: 2},
		// "서" is EC 84 9C: its last byte is the C1 string terminator.
		{name: "a three-byte character", value: "서", length: 3},
		{name: "a four-byte character", value: "😀", length: 4},
		{name: "a stray continuation byte", value: "\x9c", length: 0},
		{name: "a C1 introducer", value: "\x9b", length: 0},
		{name: "a lead byte before ASCII", value: "\xecab", length: 0},
		{name: "a character cut in half", value: "\xec\x84", length: 3, split: true},
		{name: "a lone lead byte", value: "\xec", length: 3, split: true},
	} {
		t.Run(probe.name, func(t *testing.T) {
			length, split := display.Multibyte([]byte(probe.value), 0)
			if length != probe.length || split != probe.split {
				t.Fatalf("Multibyte(%q) = %d, %v, want %d, %v",
					probe.value, length, split, probe.length, probe.split)
			}
		})
	}
}
