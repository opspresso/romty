package daemon

import (
	"strings"
	"testing"
)

func TestModeTrackerFollowsWhatTheGuestSet(t *testing.T) {
	for _, probe := range []struct {
		name    string
		output  []string
		restore string
	}{
		{
			name:    "nothing was set",
			output:  []string{"plain output\r\n"},
			restore: "",
		},
		{
			name:    "bracketed paste",
			output:  []string{"\x1b[?2004h"},
			restore: "\x1b[?2004h",
		},
		{
			name:    "bracketed paste turned back off",
			output:  []string{"\x1b[?2004h", "work\r\n", "\x1b[?2004l"},
			restore: "\x1b[?2004l",
		},
		{
			name:    "several modes in one sequence",
			output:  []string{"\x1b[?1049;1006;2004h"},
			restore: "\x1b[?1006h\x1b[?1049h\x1b[?2004h",
		},
		{
			name:    "the C1 form of the introducer",
			output:  []string{"\x9b?2004h"},
			restore: "\x1b[?2004h",
		},
		{
			name:    "a sequence split across two chunks",
			output:  []string{"before\x1b[?20", "04h after"},
			restore: "\x1b[?2004h",
		},
		{
			name:    "a sequence split one byte at a time",
			output:  []string{"\x1b", "[", "?", "2", "0", "0", "4", "h"},
			restore: "\x1b[?2004h",
		},
		{
			name:    "modes romty does not track are ignored",
			output:  []string{"\x1b[?12h\x1b[?2026h"},
			restore: "",
		},
		{
			name:    "an ANSI mode is not a private mode",
			output:  []string{"\x1b[4h"},
			restore: "",
		},
		{
			name:    "a cursor movement is not a mode",
			output:  []string{"\x1b[2004H"},
			restore: "",
		},
		{
			name:    "the last word wins",
			output:  []string{"\x1b[?1049h", "\x1b[?1049l", "\x1b[?1049h"},
			restore: "\x1b[?1049h",
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			tracker := newModeTracker()
			for _, chunk := range probe.output {
				tracker.observe([]byte(chunk))
			}
			if got := string(tracker.restore()); got != probe.restore {
				t.Fatalf("restore() = %q, want %q", got, probe.restore)
			}
		})
	}
}

// The whole point is to outlive the recording, so the tracker must not depend
// on the bytes still being in it.
func TestModeTrackerOutlivesTheRecording(t *testing.T) {
	previous := maxHistoryBytes
	maxHistoryBytes = 1024
	t.Cleanup(func() { maxHistoryBytes = previous })

	value := newSessionForTest()
	value.modes.observe([]byte("\x1b[?2004h"))
	value.history.append([]byte("\x1b[?2004h"))
	// Enough output to push the mode out of the recording entirely.
	flood := []byte(strings.Repeat("x", maxHistoryBytes*2))
	value.modes.observe(flood)
	value.history.append(flood)

	if strings.Contains(string(value.history.bytes()), "2004h") {
		t.Fatal("the recording still holds the mode; the test proves nothing")
	}
	if got := string(value.modes.restore()); got != "\x1b[?2004h" {
		t.Fatalf("restore() = %q, want bracketed paste restored from outside the recording", got)
	}
}

// A stream of unterminated introducers must not make the carry buffer grow.
func TestModeTrackerBoundsWhatItHoldsBack(t *testing.T) {
	tracker := newModeTracker()
	tracker.observe([]byte("\x1b[" + strings.Repeat("1;", modeCarryLimit)))
	if len(tracker.carry) > modeCarryLimit {
		t.Fatalf("carry = %d bytes, want at most %d", len(tracker.carry), modeCarryLimit)
	}
}
