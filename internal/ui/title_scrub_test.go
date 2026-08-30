package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestTitleScrubberRemovesTitleSequences(t *testing.T) {
	for _, probe := range []struct{ name, value, want string }{
		{name: "plain text", value: "hello 한국어", want: "hello 한국어"},
		{
			name:  "a BEL-terminated title",
			value: "before\x1b]0;✳ 문서 개선\aafter",
			want:  "beforeafter",
		},
		{
			name:  "an ST-terminated title",
			value: "before\x1b]2;✳ 문서 개선\x1b\\after",
			want:  "beforeafter",
		},
		{
			name:  "a C1-terminated title",
			value: "before\x1b]0;title\x9cafter",
			want:  "beforeafter",
		},
		{
			name:  "an icon name",
			value: "before\x1b]1;icon\aafter",
			want:  "beforeafter",
		},
		{
			name:  "a C1-introduced title",
			value: "before\x9d0;title\aafter",
			want:  "beforeafter",
		},
		{
			// The escape abandons the string; what it introduces stays.
			name:  "a title abandoned at an escape",
			value: "before\x1b]0;title\x1b[31mred",
			want:  "before\x1b[31mred",
		},
		{
			// OSC 21 is not a title even though it starts with a title digit.
			name:  "an OSC with a longer command",
			value: "before\x1b]21;value\aafter",
			want:  "before\x1b]21;value\aafter",
		},
		{
			name:  "a hyperlink",
			value: "\x1b]8;;https://romty.dev\x1b\\romty\x1b]8;;\x1b\\",
			want:  "\x1b]8;;https://romty.dev\x1b\\romty\x1b]8;;\x1b\\",
		},
		{
			name:  "a clipboard write",
			value: "\x1b]52;c;aGk=\a",
			want:  "\x1b]52;c;aGk=\a",
		},
		{
			// "서" ends in the C1 introducer's neighbourhood: EC 84 9C. The
			// 0x9C inside it must not begin or end anything.
			name:  "text whose bytes look like C1 controls",
			value: "문서 개선\x1b[1m웛6n",
			want:  "문서 개선\x1b[1m웛6n",
		},
		{
			name:  "two titles in one chunk",
			value: "a\x1b]0;one\ab\x1b]2;two\ac",
			want:  "abc",
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			var scrubber titleScrubber
			if got := string(scrubber.scrub([]byte(probe.value))); got != probe.want {
				t.Fatalf("scrub(%q) = %q, want %q", probe.value, got, probe.want)
			}
		})
	}
}

// The PTY hands over whatever happens to have arrived, so a title, its
// terminator, or a single character can be cut anywhere. Scrubbing the two
// halves must reach the same conclusion as scrubbing the whole.
func TestTitleScrubberSurvivesEveryChunkBoundary(t *testing.T) {
	value := []byte("한글 text\x1b]0;✳ 문서 개선\a middle \x9d2;서\x9c\x1b]1;t\x1b\\\x1b[31m붉은\x1b]8;;x\x1b\\ end")
	var whole titleScrubber
	want := string(whole.scrub(value))
	for cut := 0; cut <= len(value); cut++ {
		var scrubber titleScrubber
		got := string(scrubber.scrub(value[:cut])) + string(scrubber.scrub(value[cut:]))
		if got != want {
			t.Fatalf("cut at %d: scrub halves = %q, want %q", cut, got, want)
		}
	}
}

// A sequence that never terminates cannot be held forever: past the bound it
// goes through unfiltered rather than swallowing the guest's output.
func TestTitleScrubberBoundsWhatItHoldsBack(t *testing.T) {
	runaway := []byte("\x1b]0;" + strings.Repeat("x", titleHoldLimit+1))
	var scrubber titleScrubber
	got := scrubber.scrub(runaway)
	if !bytes.Equal(got, runaway) {
		t.Fatalf("scrub gave %d bytes, want the %d unfiltered", len(got), len(runaway))
	}
	if len(scrubber.pending) != 0 {
		t.Fatalf("scrubber held %d bytes past its bound", len(scrubber.pending))
	}
}

// The reason the scrubber exists: the emulator reads the 0x9C inside "✳" and
// "서" as the C1 string terminator, ends the title there, and prints the rest
// of it as text at the cursor — a sentence in the guest's input box that
// nobody typed.
func TestGuestTitleNeverRendersAsText(t *testing.T) {
	value := []byte("\x1b]0;✳ 문서 개선\aafter")
	for cut := 0; cut <= len(value); cut++ {
		terminal := newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 5)
		terminal.writeOutput(value[:cut])
		terminal.writeOutput(value[cut:])
		rendered := strings.Join(terminal.render(), "\n")
		terminal.close()
		if strings.Contains(rendered, "문서") {
			t.Fatalf("cut at %d: title rendered as text: %q", cut, rendered)
		}
		if !strings.Contains(rendered, "after") {
			t.Fatalf("cut at %d: text after the title is gone: %q", cut, rendered)
		}
	}
}
