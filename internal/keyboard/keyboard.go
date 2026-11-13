// Package keyboard follows the keyboard protocol a program asked its terminal
// for. A program that wants more than the legacy encoding says so: it pushes
// Kitty keyboard flags (CSI > flags u) or sets xterm's modifyOtherKeys
// (CSI > 4 ; level m), and it reads only the encoding it asked for. Codex
// drops a modifyOtherKeys sequence unread, so Shift+Enter reaches it only as
// the CSI u sequence its flags request.
//
// The daemon and the dashboard both follow the same stream, so the rules live
// here once: the daemon to hand a reattaching dashboard the state its
// recording no longer holds, and the dashboard to encode the keys it forwards.
package keyboard

import (
	"strconv"
	"strings"
)

// The Kitty keyboard flags romty honours. Event types (2) need key releases
// and the two text flags (8, 16) need every key as an escape code; romty
// forwards neither, so it does not claim them.
const (
	Disambiguate  = 1
	AlternateKeys = 4
	supported     = Disambiguate | AlternateKeys
)

// maxDepth is how many pushes one screen keeps, which is what kitty keeps. A
// push beyond it drops the oldest entry.
const maxDepth = 8

// State is what a guest has asked of the keyboard so far. The zero value is a
// terminal nothing has been asked of.
type State struct {
	// stacks are the Kitty flags of the main and the alternate screen. The
	// protocol keeps the two apart, so a full-screen program that dies
	// without popping leaves the shell's screen as it was.
	stacks [2][]int
	alt    bool
	// otherKeys is xterm's modifyOtherKeys level, and otherKeysSet whether
	// the guest has said anything about it at all.
	otherKeys    int
	otherKeysSet bool
}

// Apply follows one CSI sequence, given its private marker, its numeric
// parameters and its final byte, and reports whether it was a keyboard
// request. A missing parameter is passed as a negative value.
func (s *State) Apply(private byte, params []int, final byte) bool {
	parameter := func(index, fallback int) int {
		if index < len(params) && params[index] >= 0 {
			return params[index]
		}
		return fallback
	}
	switch {
	case final == 'u' && private == '>':
		s.push(parameter(0, 0))
	case final == 'u' && private == '<':
		s.pop(parameter(0, 1))
	case final == 'u' && private == '=':
		s.set(parameter(0, 0), parameter(1, 1))
	case final == 'm' && private == '>' && parameter(0, -1) == 4:
		// XTMODKEYS. Without a value the resource goes back to its default,
		// which here is "never asked".
		s.otherKeys = parameter(1, 0)
		s.otherKeysSet = len(params) > 1 && params[1] >= 0
	default:
		return false
	}
	return true
}

// Reset forgets every request, which is what a full terminal reset (RIS) does
// to them. It is how `reset` at a prompt gets the keys back after a program
// died with its flags pushed. The screen the guest is on is followed apart
// from this and stays.
func (s *State) Reset() {
	s.stacks, s.otherKeys, s.otherKeysSet = [2][]int{}, 0, false
}

// SetAltScreen follows the guest onto and off the alternate screen.
func (s *State) SetAltScreen(on bool) {
	s.alt = on
}

func (s *State) stack() *[]int {
	if s.alt {
		return &s.stacks[1]
	}
	return &s.stacks[0]
}

func (s *State) push(flags int) {
	stack := s.stack()
	if len(*stack) == maxDepth {
		*stack = append((*stack)[:0], (*stack)[1:]...)
	}
	*stack = append(*stack, flags&supported)
}

// pop removes count entries. Popping more than was pushed empties the stack,
// which is how a program resets flags it did not set.
func (s *State) pop(count int) {
	stack := s.stack()
	*stack = (*stack)[:max(len(*stack)-max(count, 0), 0)]
}

// set changes the current flags in place: mode 1 replaces them, 2 adds the
// given bits and 3 removes them. With nothing pushed it starts the stack.
func (s *State) set(flags, mode int) {
	current := s.Flags()
	switch mode {
	case 2:
		current |= flags
	case 3:
		current &^= flags
	default:
		current = flags
	}
	stack := s.stack()
	if len(*stack) == 0 {
		*stack = append(*stack, 0)
	}
	(*stack)[len(*stack)-1] = current & supported
}

// Flags are the Kitty flags in force on the screen the guest is on.
func (s *State) Flags() int {
	stack := *s.stack()
	if len(stack) == 0 {
		return 0
	}
	return stack[len(stack)-1]
}

// OtherKeys is the modifyOtherKeys level the guest set, and whether it set one.
func (s *State) OtherKeys() (level int, set bool) {
	return s.otherKeys, s.otherKeysSet
}

// reportPrefix opens the application program command that carries a State.
// The command is private to romty: an APC is what a terminal that does not
// know it discards whole, which is what a dashboard older than this does.
const reportPrefix = "romty:keyboard;"

// Report is the sequence that puts another State into this one's condition.
// Pushes cannot be replayed to get there: a recording that still holds a push
// would apply it a second time, and the flags would outlive the program that
// popped them once.
func (s *State) Report() string {
	var report strings.Builder
	report.WriteString("\x1b_" + reportPrefix)
	for _, stack := range s.stacks {
		for index, flags := range stack {
			if index > 0 {
				report.WriteByte(',')
			}
			report.WriteString(strconv.Itoa(flags))
		}
		report.WriteByte(';')
	}
	if s.otherKeysSet {
		report.WriteString(strconv.Itoa(s.otherKeys))
	}
	report.WriteString("\x1b\\")
	return report.String()
}

// Restore takes the payload of an application program command and, when it is
// a Report, adopts the state it carries. The screen the guest is on is not
// part of it: the terminal that reads the report already knows.
func (s *State) Restore(payload string) bool {
	body, ok := strings.CutPrefix(payload, reportPrefix)
	if !ok {
		return false
	}
	fields := strings.Split(body, ";")
	if len(fields) != 3 {
		return false
	}
	var stacks [2][]int
	for index := range stacks {
		if fields[index] == "" {
			continue
		}
		for value := range strings.SplitSeq(fields[index], ",") {
			flags, err := strconv.Atoi(value)
			if err != nil || len(stacks[index]) == maxDepth {
				return false
			}
			stacks[index] = append(stacks[index], flags&supported)
		}
	}
	otherKeys, otherKeysSet := 0, fields[2] != ""
	if otherKeysSet {
		level, err := strconv.Atoi(fields[2])
		if err != nil {
			return false
		}
		otherKeys = level
	}
	s.stacks, s.otherKeys, s.otherKeysSet = stacks, otherKeys, otherKeysSet
	return true
}
