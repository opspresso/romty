// Package screen is what romty needs around the terminal emulator wherever it
// runs one: in the dashboard, which draws a tab with it, and in the daemon,
// which follows a full-screen program with it so a reattaching dashboard can
// be shown the screen its recording no longer holds.
package screen

import (
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// Guard keeps a guest's output from crashing or stalling the emulator.
//
// A guest keeps drawing at the size it last knew, so shrinking the pane leaves
// DECSTBM and DECSLRM in flight that name rows or columns the emulator no
// longer has. The emulator takes those margins at face value and then indexes
// its buffer with them, which panics.
//
// It also takes a repeat count at face value. "Repeat the last character a
// billion times" is one short sequence, which a binary file sent to the
// terminal can hold by accident, and the emulator prints every one of them:
// minutes during which it reads nothing else.
//
// The handlers registered here run before the emulator's own, which read the
// same parameter storage: rewriting the parameter in place and returning false
// leaves the emulator to apply a value that fits.
func Guard(emulator *vt.Emulator) {
	clamp := func(final byte, index int, limit func() int) {
		emulator.RegisterCsiHandler(int(final), func(params ansi.Params) bool {
			clampParameter(params, index, limit())
			return false
		})
	}
	cells := func() int { return emulator.Width() * emulator.Height() }
	// Set Top and Bottom Margins [ansi.DECSTBM].
	clamp('r', 1, emulator.Height)
	// Set Left and Right Margins [ansi.DECSLRM].
	clamp('s', 1, emulator.Width)
	// Repeat Previous Character [ansi.REP]. A screen of them is all that can
	// be seen.
	clamp('b', 0, cells)
	// Cursor Horizontal Tabulation, Erase Character and Cursor Backward
	// Tabulation, which walk their count one step at a time.
	clamp('I', 0, emulator.Width)
	clamp('X', 0, emulator.Width)
	clamp('Z', 0, emulator.Width)
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
