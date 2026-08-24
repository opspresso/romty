package daemon

import (
	"io"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
)

// The contract is not "a list of sequences is removed" but "a replayed
// recording makes the emulator say nothing back". Asserting the contract keeps
// the filter honest when the emulator learns to answer something new.
func TestStrippedHistoryMakesTheEmulatorSilent(t *testing.T) {
	for _, probe := range []struct {
		name    string
		history string
	}{
		{"cursor position request", "\x1b[6n"},
		{"device attributes", "\x1b[c"},
		{"secondary device attributes", "\x1b[>c"},
		{"foreground color", "\x1b]10;?\x1b\\"},
		{"background color", "\x1b]11;?\x07"},
		{"cursor color", "\x1b]12;?\x07"},
		{"indexed color", "\x1b]4;1;?\x07"},
		{"mode state", "\x1b[?2026$p"},
		{"setting value", "\x1bP$qm\x1b\\"},
		{"in-band resize enable", "\x1b[?2048h"},
		{"8-bit cursor position request", "\x9b6n"},
		{"8-bit device attributes", "\x9bc"},
		{"8-bit background color", "\x9d11;?\x07"},
		{"8-bit setting value", "\x90$qm\x9c"},
		{"a shell session that asked everything", "prompt$ \x1b[6n\x1b[c\x1b]11;?\x07\x9b6n\x1b[?2048hdone\r\n"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			raw := replyTo(t, probe.history)
			if raw == "" {
				// The filter drops this anyway; the unit table covers that.
				// If the emulator ever learns to answer it, this case wakes up.
				t.Skip("the emulator does not answer this sequence today")
			}
			if filtered := replyTo(t, string(stripQueries([]byte(probe.history)))); filtered != "" {
				t.Fatalf("replaying the filtered history still answers %q (unfiltered answers %q)", filtered, raw)
			}
		})
	}
}

// A resize after replay must stay silent too: in-band resize keeps reporting
// for as long as the mode is set, so a replayed enable is not a one-off.
func TestStrippedHistoryStaysSilentAcrossResize(t *testing.T) {
	emulator, replies := newProbeEmulator(t)
	emulator.Write(stripQueries([]byte("\x1b[?2048h")))
	emulator.Resize(100, 30)
	if answer := settle(replies); answer != "" {
		t.Fatalf("resizing after replay answered %q", answer)
	}
}

// replyTo returns what the emulator sends back after being fed history. The
// answer, when there is one, arrives on the copy goroutine, so wait for it
// rather than guessing a duration that -race can outlast.
func replyTo(t *testing.T, history string) string {
	t.Helper()
	emulator, replies := newProbeEmulator(t)
	emulator.Write([]byte(history))
	return settle(replies)
}

// settle waits until the reply buffer stops growing, so a silent result means
// the emulator had nothing to say rather than that it was still saying it.
func settle(replies *syncBuffer) string {
	previous := replies.String()
	for stable := 0; stable < 3; stable++ {
		time.Sleep(20 * time.Millisecond)
		if current := replies.String(); current != previous {
			previous = current
			stable = 0
		}
	}
	return previous
}

func newProbeEmulator(t *testing.T) (*vt.SafeEmulator, *syncBuffer) {
	t.Helper()
	emulator := vt.NewSafeEmulator(80, 24)
	replies := &syncBuffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(replies, emulator)
	}()
	t.Cleanup(func() {
		if closer, ok := emulator.InputPipe().(io.Closer); ok {
			_ = closer.Close()
		}
		<-done
		_ = emulator.Close()
	})
	return emulator, replies
}
