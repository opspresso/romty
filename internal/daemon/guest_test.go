package daemon

import (
	"strings"
	"testing"
)

func TestGuestTrackerFollowsTheWindowTitle(t *testing.T) {
	for _, probe := range []struct {
		name   string
		output []string
		title  string
	}{
		{name: "no title was set", output: []string{"plain output\r\n"}, title: ""},
		{name: "icon and window", output: []string{"\x1b]0;working\x07"}, title: "working"},
		{name: "window alone", output: []string{"\x1b]2;action required\x07"}, title: "action required"},
		{
			name:   "the ESC form of the string terminator",
			output: []string{"\x1b]2;waiting\x1b\\"},
			title:  "waiting",
		},
		{
			name:   "the C1 introducer and terminator",
			output: []string{"\x9d2;waiting\x9c"},
			title:  "waiting",
		},
		{
			name:   "the icon name alone says nothing about the window",
			output: []string{"\x1b]2;window\x07", "\x1b]1;icon\x07"},
			title:  "window",
		},
		{
			name:   "the newest title wins",
			output: []string{"\x1b]2;first\x07", "work\r\n", "\x1b]2;second\x07"},
			title:  "second",
		},
		{
			name:   "a title split across two chunks",
			output: []string{"before\x1b]2;act", "ion required\x07 after"},
			title:  "action required",
		},
		{
			name:   "a title split one byte at a time",
			output: strings.Split("\x1b]2;hi\x07", ""),
			title:  "hi",
		},
		{
			name:   "an empty title clears the previous one",
			output: []string{"\x1b]2;working\x07", "\x1b]2;\x07"},
			title:  "",
		},
		{
			name:   "a body with no parameter separator is not a title",
			output: []string{"\x1b]2\x07"},
			title:  "",
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			tracker := newGuestTracker()
			for _, chunk := range probe.output {
				tracker.observe([]byte(chunk))
			}
			if tracker.title != probe.title {
				t.Fatalf("title = %q, want %q", tracker.title, probe.title)
			}
		})
	}
}

// A title carries text, so it is the one sequence a guest can make arbitrarily
// long. It must not become a way to make the tracker hold the rest of a chunk
// or to hide a mode behind it.
func TestGuestTrackerKeepsScanningPastAnUnterminatedTitle(t *testing.T) {
	tracker := newGuestTracker()
	tracker.observe([]byte("\x1b]2;" + strings.Repeat("x", titleCarryLimit+1)))
	if len(tracker.carry) != 0 {
		t.Fatalf("carry = %d bytes, want an over-long title dropped", len(tracker.carry))
	}
	// The mode after it is still seen, so the unterminated title cost only
	// itself.
	tracker.observe([]byte("\x1b[?2004h"))
	if restore := string(tracker.restore()); restore != "\x1b[?2004h" {
		t.Fatalf("restore = %q, want the mode that followed the title", restore)
	}
}

// A title's text is guest data: what it contains must not be read as a sequence
// of its own.
func TestGuestTrackerKeepsTitleTextVerbatim(t *testing.T) {
	tracker := newGuestTracker()
	tracker.observe([]byte("\x1b]2;build [?2004h] running\x07"))
	if tracker.title != "build [?2004h] running" {
		t.Fatalf("title = %q, want the text verbatim", tracker.title)
	}
	if restore := string(tracker.restore()); restore != "" {
		t.Fatalf("restore = %q, want nothing set from a title's text", restore)
	}
}

// An ESC inside an OSC body is not text. A terminal abandons the string there
// and parses from the ESC, so the tracker has to as well: reading it as title
// text would leave it disagreeing with the emulator it exists to restore.
func TestGuestTrackerAbandonsATitleAtAnEscape(t *testing.T) {
	tracker := newGuestTracker()
	tracker.observe([]byte("\x1b]2;\x1b[?2004h\x07"))
	if restore := string(tracker.restore()); restore != "\x1b[?2004h" {
		t.Fatalf("restore = %q, want the mode the ESC introduced", restore)
	}
	if tracker.title != "" {
		t.Fatalf("title = %q, want the abandoned string to name no window", tracker.title)
	}
}

// romty draws its own tab rail and never shows the guest's title, so replaying
// it would rename the host terminal the user is sitting in front of.
func TestGuestTrackerDoesNotRestoreTheWindowTitle(t *testing.T) {
	tracker := newGuestTracker()
	tracker.observe([]byte("\x1b]2;working\x07\x1b[?2004h"))
	if restore := string(tracker.restore()); restore != "\x1b[?2004h" {
		t.Fatalf("restore = %q, want only the mode", restore)
	}
}

func TestGuestTrackerCapsTheTitleItKeeps(t *testing.T) {
	tracker := newGuestTracker()
	tracker.observe([]byte("\x1b]2;" + strings.Repeat("x", maxTitleBytes+64) + "\x07"))
	if len(tracker.title) != maxTitleBytes {
		t.Fatalf("title = %d bytes, want it capped at %d", len(tracker.title), maxTitleBytes)
	}
}

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
			name:    "application keypad",
			output:  []string{"\x1b="},
			restore: "\x1b=",
		},
		{
			name:    "application keypad turned back to normal",
			output:  []string{"\x1b=", "work\r\n", "\x1b>"},
			restore: "\x1b>",
		},
		{
			name:    "application keypad split across chunks",
			output:  []string{"\x1b", "="},
			restore: "\x1b=",
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
			tracker := newGuestTracker()
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

	value := newSessionForTest(nil)
	value.guest.observe([]byte("\x1b[?2004h\x1b="))
	value.history.append([]byte("\x1b[?2004h\x1b="))
	// Enough output to push the mode out of the recording entirely.
	flood := []byte(strings.Repeat("x", maxHistoryBytes*2))
	value.guest.observe(flood)
	value.history.append(flood)

	if strings.Contains(string(value.history.bytes()), "2004h") {
		t.Fatal("the recording still holds the mode; the test proves nothing")
	}
	if got := string(value.guest.restore()); got != "\x1b[?2004h\x1b=" {
		t.Fatalf("restore() = %q, want sticky modes restored from outside the recording", got)
	}
}

// A stream of unterminated introducers must not make the carry buffer grow.
func TestModeTrackerBoundsWhatItHoldsBack(t *testing.T) {
	tracker := newGuestTracker()
	tracker.observe([]byte("\x1b[" + strings.Repeat("1;", modeCarryLimit)))
	if len(tracker.carry) > modeCarryLimit {
		t.Fatalf("carry = %d bytes, want at most %d", len(tracker.carry), modeCarryLimit)
	}
}

// "✳" and "서" both carry 0x9C, the C1 string terminator, in their UTF-8
// bytes. Read byte-wise that ends the title mid-character; the tracker must
// keep the whole line an agent names its state with.
func TestGuestTrackerReadsTitlesWithC1BytesInside(t *testing.T) {
	tracker := newGuestTracker()
	tracker.observe([]byte("\x1b]0;✳ 문서 개선\x07"))
	if tracker.title != "✳ 문서 개선" {
		t.Fatalf("title = %q, want %q", tracker.title, "✳ 문서 개선")
	}
}

// A chunk boundary can cut a character in half, leaving its continuation
// bytes — C1 bytes among them — at the head of the next chunk. Those must
// not be read as introducers, and what follows must still be tracked.
func TestGuestTrackerSurvivesACharacterCutByAChunk(t *testing.T) {
	value := []byte("문서\x1b]2;서\x07\x1b[?1049h")
	for cut := 0; cut <= len(value); cut++ {
		tracker := newGuestTracker()
		tracker.observe(value[:cut])
		tracker.observe(value[cut:])
		if tracker.title != "서" {
			t.Fatalf("cut at %d: title = %q, want %q", cut, tracker.title, "서")
		}
		if !tracker.set[1049] {
			t.Fatalf("cut at %d: alternate screen not tracked", cut)
		}
	}
}

// "욛" ends in 0x9B, the single-byte CSI. A mode sequence's worth of ASCII
// after it must stay text rather than become a mode the guest never set.
func TestGuestTrackerIgnoresModesInsideText(t *testing.T) {
	tracker := newGuestTracker()
	tracker.observe([]byte("욛?1049h"))
	if len(tracker.restore()) != 0 {
		t.Fatalf("restore = %q, want nothing tracked", tracker.restore())
	}
}
