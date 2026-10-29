package daemon

// bellScanner finds the terminal bells in a stream of output: BEL bytes that
// are not the terminator of an escape string. Shells end the title they set
// at every prompt with BEL (OSC 0 ... BEL), so counting every BEL byte would
// ring at every prompt. The scanner keeps its state between chunks, because a
// sequence can be split across two reads.
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
			case 0x90, 0x98, 0x9d, 0x9e, 0x9f:
				// The 8-bit introducers of DCS, SOS, OSC, PM and APC.
				b.state = bellString
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
			case 0x07, 0x9c:
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
