// Removing window-title sequences from guest output before the emulator
// parses them.
//
// The emulator's parser reads the raw byte 0x9C as the C1 string terminator
// even in the middle of a UTF-8 character. An agent that names its state in
// the title — "✳ 문서 개선" — sends that byte inside "✳" and "서", so the
// title ends mid-character and the rest of it is printed as text at the
// cursor, which the agent parks in its input box. The user reads a sentence
// there that nobody typed and that no key can delete, because it was never
// input.
//
// romty never shows a guest's title — the daemon tracks it separately — so
// the whole sequence can go before the emulator sees it. Removal has to
// survive chunk boundaries: the PTY hands over whatever happens to have
// arrived, so an introducer, a payload, a terminator, or a single character
// can each be cut in half.

package ui

import "github.com/opspresso/romty/internal/display"

const (
	escape            = 0x1b
	bell              = 0x07
	commandIntroducer = 0x9d
	stringTerminator  = 0x9c
)

// titleHoldLimit bounds a sequence held back between chunks. A window title
// is a line of text; anything longer is not one being split, and holding it
// forever would swallow the guest's output.
const titleHoldLimit = 4096

type titleScrubber struct {
	// pending is a title sequence a chunk boundary cut before its terminator.
	pending []byte
	// skip counts the continuation bytes of a character the last chunk cut,
	// so the C1-looking bytes among them are not read as introducers.
	skip int
}

// scrub returns data with every complete OSC 0, 1 and 2 sequence removed,
// holding one that is still missing its terminator until the next call.
func (s *titleScrubber) scrub(data []byte) []byte {
	scan := data
	if len(s.pending) > 0 {
		scan = append(s.pending, data...)
		s.pending = nil
	}
	var filtered []byte
	copied := 0

	finish := func(end int) []byte {
		if filtered == nil {
			return scan[:end]
		}
		return append(filtered, scan[copied:end]...)
	}
	// hold keeps the unterminated sequence for the next chunk, unless it has
	// grown past what a title could be — then it goes through unfiltered,
	// which is the old behaviour, rather than being withheld forever.
	hold := func(from int) []byte {
		if len(scan)-from > titleHoldLimit {
			return finish(len(scan))
		}
		s.pending = append([]byte(nil), scan[from:]...)
		return finish(from)
	}
	drop := func(from, to int) {
		if filtered == nil {
			filtered = make([]byte, 0, len(scan))
		}
		filtered = append(filtered, scan[copied:from]...)
		copied = to
	}

	index := 0
	for s.skip > 0 && index < len(scan) {
		if scan[index]&0xc0 != 0x80 {
			s.skip = 0
			break
		}
		index++
		s.skip--
	}

	for index < len(scan) {
		if length, split := display.Multibyte(scan, index); length > 0 {
			if split {
				s.skip = length - (len(scan) - index)
				return finish(len(scan))
			}
			index += length
			continue
		}
		var body int
		switch scan[index] {
		case commandIntroducer:
			body = index + 1
		case escape:
			if index+1 >= len(scan) {
				return hold(index)
			}
			if scan[index+1] != ']' {
				index++
				continue
			}
			body = index + 2
		default:
			index++
			continue
		}
		cursor := body
		for cursor < len(scan) && scan[cursor] >= '0' && scan[cursor] <= '9' {
			cursor++
		}
		if cursor >= len(scan) {
			return hold(index)
		}
		if scan[cursor] != ';' || cursor != body+1 || scan[body] > '2' {
			// Some other OSC: leave it to the emulator.
			index = body
			continue
		}
		position := cursor + 1
		for {
			if position >= len(scan) {
				return hold(index)
			}
			if length, split := display.Multibyte(scan, position); length > 0 {
				if split {
					return hold(index)
				}
				position += length
				continue
			}
			if scan[position] == bell || scan[position] == stringTerminator {
				drop(index, position+1)
				index = position + 1
				break
			}
			if scan[position] == escape {
				if position+1 >= len(scan) {
					return hold(index)
				}
				if scan[position+1] == '\\' {
					drop(index, position+2)
					index = position + 2
					break
				}
				// The escape abandons the string: the parser consumes what
				// came before it and reads on from the escape itself.
				drop(index, position)
				index = position
				break
			}
			position++
		}
	}
	return finish(len(scan))
}
