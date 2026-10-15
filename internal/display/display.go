// Package display prepares text that came from outside romty for a terminal.
//
// Names, paths, branches and error messages reach romty from a filesystem, a
// Git repository or an agent, and every one of them ends up written to the
// user's terminal. A control character in one of those is not text: it moves
// the cursor, clears the screen, or opens a sequence the terminal will answer.
// The TUI and the command line both use this package, so they scrub alike.
package display

import (
	"strings"
	"unicode"
)

// Control bytes the terminal parsers in romty look for. The C1 introducers
// are the single-byte forms of ESC [, ESC ] and ESC P, and ST the single-byte
// string terminator. A terminal accepts either form, so a scanner that only
// knows the ESC form misses the same sequence in the other.
const (
	Escape = 0x1b
	Bell   = 0x07
	CSI    = 0x9b
	OSC    = 0x9d
	DCS    = 0x90
	ST     = 0x9c
)

// replacement is what a control character becomes. It is one cell wide, so a
// line that held one still measures the width romty laid out for it.
const replacement = '�'

// Text replaces every control character with a visible placeholder, leaving
// the rest of the string exactly as it was.
func Text(value string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return replacement
		}
		return character
	}, value)
}

// Multibyte reports the byte length of the UTF-8 character starting at index
// when that character is longer than one byte, and whether data ends before
// the character does. A length of zero means the byte stands alone: ASCII, a
// stray continuation byte, or nothing UTF-8 begins with.
//
// Byte scanners need the distinction because the C1 control bytes 0x80–0x9F
// all occur as continuation bytes inside ordinary text — "서" ends in 0x9C,
// the C1 string terminator, and "✳" carries it in the middle — and reading
// one as a control chops the character in half.
func Multibyte(data []byte, index int) (length int, split bool) {
	var size int
	switch b := data[index]; {
	case b >= 0xc2 && b <= 0xdf:
		size = 2
	case b >= 0xe0 && b <= 0xef:
		size = 3
	case b >= 0xf0 && b <= 0xf4:
		size = 4
	default:
		return 0, false
	}
	for offset := 1; offset < size; offset++ {
		if index+offset >= len(data) {
			return size, true
		}
		if data[index+offset]&0xc0 != 0x80 {
			return 0, false
		}
	}
	return size, false
}
