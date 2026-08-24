package daemon

import (
	"strings"
	"testing"
	"time"
)

func TestStripQueriesRemovesQueriesAndKeepsOutput(t *testing.T) {
	for _, probe := range []struct {
		name    string
		history string
		want    string
	}{
		{
			name:    "cursor position report request",
			history: "before\x1b[6nafter",
			want:    "beforeafter",
		},
		{
			name:    "primary device attributes",
			history: "before\x1b[cafter",
			want:    "beforeafter",
		},
		{
			name:    "secondary device attributes",
			history: "before\x1b[>0cafter",
			want:    "beforeafter",
		},
		{
			name:    "an echoed device attributes reply",
			history: "before\x1b[?62;1;6;22cafter",
			want:    "beforeafter",
		},
		{
			name:    "background color query terminated by BEL",
			history: "before\x1b]11;?\x07after",
			want:    "beforeafter",
		},
		{
			name:    "foreground color query terminated by ST",
			history: "before\x1b]10;?\x1b\\after",
			want:    "beforeafter",
		},
		{
			name:    "indexed color query",
			history: "before\x1b]4;1;?\x07after",
			want:    "beforeafter",
		},
		{
			name:    "mode state request",
			history: "before\x1b[?2026$pafter",
			want:    "beforeafter",
		},
		{
			name:    "terminal version request",
			history: "before\x1b[>0qafter",
			want:    "beforeafter",
		},
		{
			name:    "setting value request",
			history: "before\x1bP$qm\x1b\\after",
			want:    "beforeafter",
		},
		{
			name:    "8-bit cursor position report request",
			history: "before\x9b6nafter",
			want:    "beforeafter",
		},
		{
			name:    "8-bit device attributes",
			history: "before\x9bcafter",
			want:    "beforeafter",
		},
		{
			name:    "8-bit background color query",
			history: "before\x9d11;?\x07after",
			want:    "beforeafter",
		},
		{
			name:    "8-bit setting value request",
			history: "before\x90$qm\x9cafter",
			want:    "beforeafter",
		},
		{
			name:    "an OSC closed by the 8-bit string terminator",
			history: "before\x1b]11;?\x9cafter",
			want:    "beforeafter",
		},
		{
			name:    "in-band resize is dropped because the emulator answers it",
			history: "before\x1b[?2048hafter",
			want:    "beforeafter",
		},
		{
			name:    "a soft reset is not a query",
			history: "before\x1b[!pafter",
			want:    "before\x1b[!pafter",
		},
		{
			name:    "a cursor shape request is not a query",
			history: "before\x1b[2 qafter",
			want:    "before\x1b[2 qafter",
		},
		{
			name:    "another mode enable is not a query",
			history: "before\x1b[?2004hafter",
			want:    "before\x1b[?2004hafter",
		},
		{
			name:    "a startup burst of every query at once",
			history: "prompt$ \x1b[6n\x1b[c\x1b]10;?\x1b\\\x1b]11;?\x07done",
			want:    "prompt$ done",
		},
		{
			name:    "plain output is untouched",
			history: "hello\r\nworld\r\n",
			want:    "hello\r\nworld\r\n",
		},
		{
			name:    "styling and cursor movement are untouched",
			history: "\x1b[2J\x1b[H\x1b[1;31mred\x1b[0m\x1b[10;20H\x1b[2K",
			want:    "\x1b[2J\x1b[H\x1b[1;31mred\x1b[0m\x1b[10;20H\x1b[2K",
		},
		{
			name:    "a window title is not a query",
			history: "\x1b]0;~/projects\x07prompt",
			want:    "\x1b]0;~/projects\x07prompt",
		},
		{
			name:    "a title ending in a question mark is not a query",
			history: "\x1b]0;what;?\x07prompt",
			want:    "\x1b]0;what;?\x07prompt",
		},
		{
			name:    "a color reply is not a query",
			history: "\x1b]11;rgb:0000/0000/0000\x07prompt",
			want:    "\x1b]11;rgb:0000/0000/0000\x07prompt",
		},
		{
			name:    "an OSC setting a color is not a query",
			history: "\x1b]4;1;rgb:ff/00/00\x07prompt",
			want:    "\x1b]4;1;rgb:ff/00/00\x07prompt",
		},
		{
			name:    "a truncated query at the end is left alone",
			history: "before\x1b[6",
			want:    "before\x1b[6",
		},
		{
			name:    "a truncated OSC at the end is left alone",
			history: "before\x1b]11;?",
			want:    "before\x1b]11;?",
		},
		{
			name:    "a bare escape is left alone",
			history: "before\x1b",
			want:    "before\x1b",
		},
		{
			name:    "empty history",
			history: "",
			want:    "",
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			if got := string(stripQueries([]byte(probe.history))); got != probe.want {
				t.Fatalf("stripQueries(%q)\n got %q\nwant %q", probe.history, got, probe.want)
			}
		})
	}
}

// Untouched history must come back as the same backing array, so replaying a
// full 8 MB buffer does not copy it on every attach.
func TestStripQueriesDoesNotCopyCleanHistory(t *testing.T) {
	history := []byte("hello\r\nworld\r\n")
	filtered := stripQueries(history)
	if len(filtered) != len(history) || &filtered[0] != &history[0] {
		t.Fatalf("clean history was copied: %p vs %p", &filtered[0], &history[0])
	}
}

func TestStripQueriesLeavesTheSourceUnchanged(t *testing.T) {
	original := "before\x1b[6nafter"
	history := []byte(original)
	stripQueries(history)
	if string(history) != original {
		t.Fatalf("history was mutated: %q, want %q", history, original)
	}
}

// Every query the emulator answers has to be gone, or the answer reaches a
// shell that asked nothing. Checked by shape rather than by exact sequence so
// the intent stays readable.
func TestStripQueriesRemovesEveryAnsweredQueryFromRealisticHistory(t *testing.T) {
	history := strings.Join([]string{
		"\x1b]0;bruce@host: ~/projects\x07",
		"\x1b[1;32m➜\x1b[0m  agent-studio ",
		"\x1b[6n",
		"\x1b[c",
		"\x1b]10;?\x1b\\",
		"\x1b]11;?\x1b\\",
		"\x1b[?2026$p",
		"npm run dev\r\n",
	}, "")
	filtered := string(stripQueries([]byte(history)))

	for _, query := range []string{"\x1b[6n", "\x1b[c", "\x1b]10;?", "\x1b]11;?", "$p"} {
		if strings.Contains(filtered, query) {
			t.Fatalf("query %q survived the filter: %q", query, filtered)
		}
	}
	for _, kept := range []string{"\x1b]0;bruce@host: ~/projects\x07", "\x1b[1;32m➜\x1b[0m", "npm run dev\r\n"} {
		if !strings.Contains(filtered, kept) {
			t.Fatalf("filter dropped screen content %q: %q", kept, filtered)
		}
	}
}

// The scanner's memory of exhausted terminator searches is what keeps replay
// linear. Without it this input took minutes, and it runs while the session
// lock is held, so a regression stalls every client of that terminal.
func TestStripQueriesStaysLinearOnUnterminatedIntroducers(t *testing.T) {
	for _, probe := range []struct {
		name       string
		introducer string
	}{
		{name: "OSC", introducer: "\x1b]"},
		{name: "DCS", introducer: "\x1bP"},
		{name: "8-bit OSC", introducer: "\x9d"},
		{name: "8-bit DCS", introducer: "\x90"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			// maxHistoryBytes of output whose terminators never arrive, the
			// shape of `cat`ing a binary file.
			var history []byte
			for len(history) < maxHistoryBytes {
				history = append(history, strings.Repeat("x", 4096)...)
				history = append(history, probe.introducer...)
			}
			history = history[:maxHistoryBytes]

			done := make(chan int, 1)
			go func() { done <- len(stripQueries(history)) }()
			select {
			case length := <-done:
				if length != len(history) {
					t.Fatalf("filtered length = %d, want the input kept whole at %d", length, len(history))
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("stripQueries did not finish %d bytes in 5s; the scanner is rescanning", len(history))
			}
		})
	}
}

func BenchmarkStripQueries(b *testing.B) {
	var history []byte
	for len(history) < maxHistoryBytes {
		history = append(history, strings.Repeat("x", 4096)...)
		history = append(history, "\x1b]"...)
	}
	history = history[:maxHistoryBytes]
	b.SetBytes(int64(len(history)))
	b.ReportAllocs()
	for b.Loop() {
		stripQueries(history)
	}
}
