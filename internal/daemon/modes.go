package daemon

import (
	"fmt"
	"strconv"
	"strings"
)

// Terminal modes are sticky: a guest sets one once and it stays set for the
// rest of the session. The recording only keeps the last maxHistoryBytes, so a
// mode set before that window is simply gone, and a reattaching client ends up
// disagreeing with the guest about how the terminal behaves.
//
// The worst of those is bracketed paste. Without it a shell has no way to tell
// pasted text from typed text, so pasting several lines into zsh or fish runs
// each one instead of leaving them on the command line. That is data loss the
// user never asked for, arriving long after the mode was lost.
//
// Tracking the modes over the whole stream rather than the retained window
// makes the answer independent of truncation.
var trackedModes = []int{
	1,    // DECCKM, application cursor keys: changes what arrow keys send
	7,    // DECAWM, autowrap
	25,   // cursor visibility
	1000, // mouse: button events
	1002, // mouse: button events with drag
	1003, // mouse: any movement
	1004, // focus reporting
	1006, // mouse: SGR encoding
	1049, // alternate screen
	2004, // bracketed paste
}

// modeCarryLimit bounds what is held back between chunks. A mode sequence is a
// dozen bytes; anything longer is not one being split.
const modeCarryLimit = 128

// modeTracker follows the sticky terminal modes a guest turns on and off. It reads
// the same bytes the recording does, but keeps only the conclusion, so it is
// unaffected by the recording being trimmed.
type modeTracker struct {
	set map[int]bool
	// keypad is DECKPAM/DECKPNM, an ESC sequence rather than a CSI mode.
	keypad      bool
	keypadKnown bool
	// carry holds a sequence cut in half by a chunk boundary, since the PTY
	// hands over whatever happens to have arrived.
	carry []byte
}

func newModeTracker() *modeTracker {
	return &modeTracker{set: make(map[int]bool, len(trackedModes))}
}

func (t *modeTracker) observe(data []byte) {
	scan := data
	if len(t.carry) > 0 {
		scan = append(t.carry, data...)
		t.carry = nil
	}
	for index := 0; index < len(scan); {
		if scan[index] == escape {
			if index+1 >= len(scan) {
				t.hold(scan[index:])
				return
			}
			switch scan[index+1] {
			case '=':
				t.keypad, t.keypadKnown = true, true
				index += 2
				continue
			case '>':
				t.keypad, t.keypadKnown = false, true
				index += 2
				continue
			}
		}
		body, state := modeIntroducer(scan, index)
		switch state {
		case notAnIntroducer:
			index++
			continue
		case introducerSplit:
			// An escape at the very end of the chunk may be the start of one.
			t.hold(scan[index:])
			return
		}
		end, params, final, complete := scanModeSequence(scan, body)
		if !complete {
			t.hold(scan[index:])
			return
		}
		if final == 'h' || final == 'l' {
			t.apply(params, final == 'h')
		}
		index = end
	}
}

// hold keeps a sequence the chunk boundary cut in half, unless it is already
// longer than any mode sequence could be.
func (t *modeTracker) hold(tail []byte) {
	if len(tail) <= modeCarryLimit {
		t.carry = append([]byte(nil), tail...)
	}
}

func (t *modeTracker) apply(params string, enabled bool) {
	if !strings.HasPrefix(params, "?") {
		return
	}
	for _, field := range strings.Split(params[1:], ";") {
		mode, err := strconv.Atoi(field)
		if err != nil {
			continue
		}
		for _, tracked := range trackedModes {
			if mode == tracked {
				t.set[mode] = enabled
			}
		}
	}
}

// restore returns the sequences that put a fresh emulator into the state the
// guest believes the terminal is in. It is written before the recording, so
// any mode change still inside the recording replays on top and wins.
func (t *modeTracker) restore() []byte {
	var preamble strings.Builder
	for _, mode := range trackedModes {
		enabled, known := t.set[mode]
		if !known {
			continue
		}
		final := 'l'
		if enabled {
			final = 'h'
		}
		fmt.Fprintf(&preamble, "\x1b[?%d%c", mode, final)
	}
	if t.keypadKnown {
		if t.keypad {
			preamble.WriteString("\x1b=")
		} else {
			preamble.WriteString("\x1b>")
		}
	}
	return []byte(preamble.String())
}

type introducerState int

const (
	notAnIntroducer introducerState = iota
	introducerFound
	introducerSplit
)

// modeIntroducer reports where a CSI sequence's parameters begin, accepting
// both the ESC form and the single C1 byte, and distinguishing "not one" from
// "cannot tell yet because the chunk ended".
func modeIntroducer(data []byte, index int) (int, introducerState) {
	if data[index] == controlIntroducer {
		return index + 1, introducerFound
	}
	if data[index] != escape {
		return 0, notAnIntroducer
	}
	if index+1 >= len(data) {
		return 0, introducerSplit
	}
	if data[index+1] != '[' {
		return 0, notAnIntroducer
	}
	return index + 2, introducerFound
}

// scanModeSequence walks a CSI sequence by its grammar and reports its
// parameters and final byte, or that it is not all here yet.
func scanModeSequence(data []byte, body int) (end int, params string, final byte, complete bool) {
	cursor := body
	for cursor < len(data) && data[cursor] >= 0x30 && data[cursor] <= 0x3f {
		cursor++
	}
	parameters := string(data[body:cursor])
	for cursor < len(data) && data[cursor] >= 0x20 && data[cursor] <= 0x2f {
		cursor++
	}
	if cursor >= len(data) {
		return 0, "", 0, false
	}
	return cursor + 1, parameters, data[cursor], true
}
