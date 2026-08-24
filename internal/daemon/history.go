package daemon

import "bytes"

const (
	escape = 0x1b
	bell   = 0x07
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
func stripQueries(history []byte) []byte {
	var filtered []byte
	copied := 0
	for index := 0; index < len(history); {
		end, drop := scanSequence(history, index)
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

// scanSequence returns the index just past the escape sequence starting at
// index and whether that sequence is a query. It returns index unchanged when
// there is no complete sequence to skip, so a truncated tail is never dropped.
func scanSequence(data []byte, index int) (int, bool) {
	if data[index] != escape || index+1 >= len(data) {
		return index, false
	}
	switch data[index+1] {
	case '[':
		return scanControlSequence(data, index)
	case ']':
		return scanOperatingSystemCommand(data, index)
	case 'P':
		return scanDeviceControlString(data, index)
	}
	return index, false
}

// scanControlSequence walks a CSI sequence by its grammar — parameter bytes,
// then intermediate bytes, then one final byte — so the end is exact regardless
// of whether the sequence is one romty knows.
func scanControlSequence(data []byte, index int) (int, bool) {
	cursor := index + 2
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
	}
	return cursor, false
}

func scanOperatingSystemCommand(data []byte, index int) (int, bool) {
	for cursor := index + 2; cursor < len(data); cursor++ {
		if data[cursor] == bell {
			return cursor + 1, isQueryPayload(data[index+2 : cursor])
		}
		if data[cursor] == escape && cursor+1 < len(data) && data[cursor+1] == '\\' {
			return cursor + 2, isQueryPayload(data[index+2 : cursor])
		}
	}
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

func scanDeviceControlString(data []byte, index int) (int, bool) {
	for cursor := index + 2; cursor+1 < len(data); cursor++ {
		if data[cursor] == escape && data[cursor+1] == '\\' {
			// DECRQSS, the request for a setting's current value.
			return cursor + 2, bytes.HasPrefix(data[index+2:cursor], []byte("$q"))
		}
	}
	return index, false
}
