package daemon

import "bytes"

const (
	escape = 0x1b
	bell   = 0x07
	// C1 introducers: the single-byte forms of ESC [, ESC ] and ESC P, plus the
	// single-byte string terminator. A terminal accepts either form, so a filter
	// that only knows the ESC form leaves the same query standing in the other.
	controlIntroducer = 0x9b
	commandIntroducer = 0x9d
	stringIntroducer  = 0x90
	stringTerminator  = 0x9c
)

// stripQueries removes terminal queries from recorded PTY output.
//
// The history exists so a reattaching client can restore the screen, but a
// terminal emulator has no way to tell a recording from live output: it answers
// every query it finds. Those answers reach a shell that asked nothing and land
// on its command line as if typed, which is how strings such as
// "62;1;6;22c" or "11;rgb:0000/0000/0000" appear at a prompt.
//
// A query describes an exchange that already finished, so it is not part of the
// screen and is dropped before replay. Anything that is not a complete,
// recognised query is copied through untouched.
//
// The rule is not "remove these sequences" but "leave the emulator with nothing
// to say", which is what TestStrippedHistoryMakesTheEmulatorSilent asserts.
// That is why both the ESC and the C1 form of every introducer are handled, and
// why in-band resize is dropped even though enabling a mode is not a question.
func stripQueries(history []byte) []byte {
	var scanner historyScanner
	var filtered []byte
	copied := 0
	for index := 0; index < len(history); {
		end, drop := scanner.scanSequence(history, index)
		if end == index {
			index++
			continue
		}
		if drop {
			if filtered == nil {
				filtered = make([]byte, 0, len(history))
			}
			filtered = append(filtered, history[copied:index]...)
			copied = end
		}
		index = end
	}
	if filtered == nil {
		return history
	}
	return append(filtered, history[copied:]...)
}

// historyScanner remembers terminator searches that ran off the end of the
// buffer. Scanning only moves forward, so once no terminator exists after one
// position, none exists after any later one either. Without that memory a
// buffer holding many unterminated introducers — what `cat`ing a binary file
// leaves behind — costs one full scan each, which is quadratic: 8 MiB of such
// output took three seconds, all of it holding the session lock.
type historyScanner struct {
	noStringTerminator bool
	noBellOrTerminator bool
}

// scanSequence returns the index just past the escape sequence starting at
// index and whether that sequence is a query. It returns index unchanged when
// there is no complete sequence to skip, so a truncated tail is never dropped.
func (s *historyScanner) scanSequence(data []byte, index int) (int, bool) {
	switch data[index] {
	case escape:
		if index+1 >= len(data) {
			return index, false
		}
		switch data[index+1] {
		case '[':
			return scanControlSequence(data, index, index+2)
		case ']':
			return s.scanOperatingSystemCommand(data, index, index+2)
		case 'P':
			return s.scanDeviceControlString(data, index, index+2)
		}
	case controlIntroducer:
		return scanControlSequence(data, index, index+1)
	case commandIntroducer:
		return s.scanOperatingSystemCommand(data, index, index+1)
	case stringIntroducer:
		return s.scanDeviceControlString(data, index, index+1)
	}
	return index, false
}

// scanControlSequence walks a CSI sequence by its grammar — parameter bytes,
// then intermediate bytes, then one final byte — so the end is exact regardless
// of whether the sequence is one romty knows.
func scanControlSequence(data []byte, index, body int) (int, bool) {
	cursor := body
	parameters := cursor
	for cursor < len(data) && data[cursor] >= 0x30 && data[cursor] <= 0x3f {
		cursor++
	}
	private := data[parameters:cursor]
	intermediates := cursor
	for cursor < len(data) && data[cursor] >= 0x20 && data[cursor] <= 0x2f {
		cursor++
	}
	if cursor >= len(data) {
		return index, false
	}
	middle := data[intermediates:cursor]
	final := data[cursor]
	cursor++
	switch {
	case final == 'n':
		// DSR, including the cursor position report request ESC[6n.
		return cursor, true
	case final == 'c':
		// Device attributes, both the request and an echoed reply.
		return cursor, true
	case final == 'p' && bytes.Equal(middle, []byte("$")):
		// DECRQM, the mode state request.
		return cursor, true
	case final == 'q' && bytes.HasPrefix(private, []byte(">")):
		// XTVERSION, the terminal name and version request.
		return cursor, true
	case final == 'h' && bytes.Equal(private, []byte("?2048")):
		// In-band resize. Setting it is not a question, but the emulator
		// answers it like one and keeps answering on every later resize, so a
		// replayed enable injects reports into a shell that never asked. The
		// guest still learns about resizes through SIGWINCH.
		return cursor, true
	}
	return cursor, false
}

func (s *historyScanner) scanOperatingSystemCommand(data []byte, index, body int) (int, bool) {
	if s.noBellOrTerminator {
		return index, false
	}
	for cursor := body; cursor < len(data); cursor++ {
		if data[cursor] == bell || data[cursor] == stringTerminator {
			return cursor + 1, isQueryPayload(data[body:cursor])
		}
		if data[cursor] == escape && cursor+1 < len(data) && data[cursor+1] == '\\' {
			return cursor + 2, isQueryPayload(data[body:cursor])
		}
	}
	// Neither terminator exists in the rest of the buffer, so a string
	// terminator alone cannot either.
	s.noBellOrTerminator = true
	s.noStringTerminator = true
	return index, false
}

// isQueryPayload reports whether an OSC payload asks the terminal for a value,
// which is the numeric "10;?" form used by the color queries. A payload with
// anything else before the "?" is a value being set, such as a window title
// that happens to end in a question mark.
func isQueryPayload(payload []byte) bool {
	if len(payload) <= 2 || !bytes.HasSuffix(payload, []byte(";?")) {
		return false
	}
	for _, character := range payload[:len(payload)-2] {
		if (character < '0' || character > '9') && character != ';' {
			return false
		}
	}
	return true
}

func (s *historyScanner) scanDeviceControlString(data []byte, index, body int) (int, bool) {
	if s.noStringTerminator {
		return index, false
	}
	for cursor := body; cursor < len(data); cursor++ {
		if data[cursor] == stringTerminator {
			// DECRQSS, the request for a setting's current value.
			return cursor + 1, bytes.HasPrefix(data[body:cursor], []byte("$q"))
		}
		if data[cursor] == escape && cursor+1 < len(data) && data[cursor+1] == '\\' {
			return cursor + 2, bytes.HasPrefix(data[body:cursor], []byte("$q"))
		}
	}
	s.noStringTerminator = true
	return index, false
}
