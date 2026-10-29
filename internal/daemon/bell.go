package daemon

// bellScanner finds the terminal bells in a stream of output: BEL bytes that
// are not the terminator of an escape string. Shells end the title they set
// at every prompt with BEL (OSC 0 ... BEL), so counting every BEL byte would
// ring at every prompt. The scanner keeps its state between chunks, because a
// sequence can be split across two reads.
//
// Only 7-bit introducers and terminators count. A byte from 0x80 to 0x9F is
// read as text, never as an 8-bit C1 control: in UTF-8 output those bytes are
// the continuation bytes of characters such as "✳" (E2 9C B3), "서" (EC 84 9C)
// and "❯" (E2 9D AF), which agents put in their titles and shells in their
// prompts. Reading them as C1 ended strings early and started others, so
// a title rang and a real bell after a prompt did not.
type bellScanner struct {
	state bellState
}

type bellState uint8

const (
	bellNormal bellState = iota
	// bellEscape follows an ESC outside a string.
	bellEscape
	// bellString is inside an OSC, DCS, APC, PM or SOS string.
	bellString
	// bellStringEscape follows an ESC inside a string, which may start ST.
	bellStringEscape
)

// scan reports whether data rings the bell.
func (b *bellScanner) scan(data []byte) bool {
	rang := false
	for _, value := range data {
		switch b.state {
		case bellNormal:
			switch value {
			case 0x07:
				rang = true
			case 0x1b:
				b.state = bellEscape
			}
		case bellEscape:
			switch value {
			case ']', 'P', '_', '^', 'X':
				b.state = bellString
			case 0x1b:
			default:
				b.state = bellNormal
			}
		case bellString:
			switch value {
			case 0x07:
				b.state = bellNormal
			case 0x1b:
				b.state = bellStringEscape
			}
		case bellStringEscape:
			if value == '\\' {
				b.state = bellNormal
			} else if value != 0x1b {
				b.state = bellString
			}
		}
	}
	return rang
}
