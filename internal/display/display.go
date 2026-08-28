// Package display prepares text that came from outside romty for a terminal.
//
// Names, paths, branches and error messages reach romty from a filesystem, a
// Git repository or an agent, and every one of them ends up written to the
// user's terminal. A control character in one of those is not text: it moves
// the cursor, clears the screen, or opens a sequence the terminal will answer.
// The TUI and the command line each had their own copy of the same scrub, and
// two copies of a rule like that agree only until one of them is fixed.
package display

import (
	"strings"
	"unicode"
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
