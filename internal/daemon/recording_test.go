package daemon

import (
	"bytes"
	"strings"
	"testing"

	"github.com/opspresso/romty/internal/testutil"
)

// withHistoryLimit sets the recording's ceiling for one test, so the wrapping
// paths can be reached without building eight megabytes.
func withHistoryLimit(t *testing.T, limit int) {
	t.Helper()
	testutil.Set(t, &maxHistoryBytes, limit)
}

// The recording is a ring, so what it holds is the last limit bytes in the
// order the shell printed them — whatever mixture of chunk sizes and wraps got
// it there.
func TestRecordingKeepsTheNewestBytesInOrder(t *testing.T) {
	withHistoryLimit(t, 8)

	for _, probe := range []struct {
		name   string
		chunks []string
		want   string
	}{
		{name: "empty", chunks: nil, want: ""},
		{name: "under the limit", chunks: []string{"abc"}, want: "abc"},
		{name: "exactly the limit", chunks: []string{"abcdefgh"}, want: "abcdefgh"},
		{name: "one chunk over", chunks: []string{"abcdefghij"}, want: "cdefghij"},
		{name: "filled a byte at a time", chunks: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}, want: "bcdefghi"},
		{name: "wrapping across chunks", chunks: []string{"abcde", "fghij"}, want: "cdefghij"},
		{name: "a chunk far over the limit", chunks: []string{"ab", strings.Repeat("x", 100) + "yz"}, want: "xxxxxxyz"},
		{name: "an empty chunk changes nothing", chunks: []string{"abcd", "", "ef"}, want: "abcdef"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			var value recording
			for _, chunk := range probe.chunks {
				value.append([]byte(chunk))
			}
			if got := string(value.bytes()); got != probe.want {
				t.Fatalf("bytes() = %q, want %q", got, probe.want)
			}
		})
	}
}

// Every tab holds a recording, and most of them hold a prompt and a command or
// two. Claiming the ceiling on the first byte would make twenty idle terminals
// cost what twenty busy ones do.
func TestRecordingGrowsIntoItsLimit(t *testing.T) {
	withHistoryLimit(t, 8<<20)

	var value recording
	value.append([]byte("$ "))
	if len(value.data) > 64 {
		t.Fatalf("a two-byte recording holds %d bytes, want it to grow into the limit", len(value.data))
	}
	if got := string(value.bytes()); got != "$ " {
		t.Fatalf("bytes() = %q, want what was recorded", got)
	}

	// And it does reach the limit, rather than growing without end.
	chunk := make([]byte, 1<<20)
	for range 16 {
		value.append(chunk)
	}
	if len(value.data) != maxHistoryBytes {
		t.Fatalf("a filled recording holds %d bytes, want the limit of %d", len(value.data), maxHistoryBytes)
	}
	if value.size != maxHistoryBytes {
		t.Fatalf("size = %d, want the limit of %d", value.size, maxHistoryBytes)
	}
}

// bytes() hands the replay a slice of its own. The ring is written in place,
// so anything else would be rewritten under the replay's feet — which is the
// whole reason attach copies before releasing the session lock.
func TestRecordingHandsOutACopy(t *testing.T) {
	withHistoryLimit(t, 8)

	var value recording
	value.append([]byte("abcdefgh"))
	held := value.bytes()
	value.append([]byte("12345678"))

	if string(held) != "abcdefgh" {
		t.Fatalf("an earlier bytes() now reads %q; the ring was aliased", held)
	}
	if got := string(value.bytes()); got != "12345678" {
		t.Fatalf("bytes() = %q, want the newest bytes", got)
	}
}

// Only tests move the limit, but a ring whose length disagrees with it would
// wrap at the wrong place and interleave old bytes into new output.
func TestRecordingRebuildsWhenTheLimitMoves(t *testing.T) {
	withHistoryLimit(t, 8)

	var value recording
	value.append([]byte("abcdefghij"))

	maxHistoryBytes = 4
	value.append([]byte("KL"))
	if got := string(value.bytes()); got != "ijKL" {
		t.Fatalf("bytes() after shrinking = %q, want the newest four", got)
	}

	maxHistoryBytes = 12
	value.append([]byte("MN"))
	if got := string(value.bytes()); got != "ijKLMN" {
		t.Fatalf("bytes() after growing = %q, want what was held plus the new bytes", got)
	}
}

// Appending to the ring costs the chunk, not the recording: raising the limit
// must not slow this down. Shifting the front off a flat buffer instead would
// move the whole recording on every chunk, under the session lock.
func BenchmarkRecordingAppend(b *testing.B) {
	chunk := bytes.Repeat([]byte("x"), 32*1024)
	var value recording
	for range maxHistoryBytes/len(chunk) + 1 {
		value.append(chunk)
	}
	b.SetBytes(int64(len(chunk)))
	for b.Loop() {
		value.append(chunk)
	}
}

// A reader that follows the recording keeps a position in it, which has to
// mean the same byte after older ones were dropped or the ring was rebuilt.
func TestRecordingReadsFromAPositionThatOutlivesTrimming(t *testing.T) {
	testutil.Set(t, &maxHistoryBytes, 8)
	var value recording
	value.append([]byte("abcdef"))
	if got := string(value.readFrom(2, 3)); got != "cde" {
		t.Fatalf("readFrom(2, 3) = %q, want %q", got, "cde")
	}

	// The ring wraps: positions 0 to 3 are gone.
	value.append([]byte("ghijkl"))
	if value.oldest() != 4 || value.total != 12 {
		t.Fatalf("oldest, total = %d, %d, want 4, 12", value.oldest(), value.total)
	}
	if got := string(value.readFrom(4, 100)); got != "efghijkl" {
		t.Fatalf("readFrom(oldest) = %q, want everything held", got)
	}
	if got := string(value.readFrom(9, 2)); got != "jk" {
		t.Fatalf("readFrom(9, 2) = %q, want %q", got, "jk")
	}
	if got := value.readFrom(12, 5); len(got) != 0 {
		t.Fatalf("readFrom(total) = %q, want nothing yet", got)
	}

	// Trimming an idle tab rebuilds the ring; the positions stay.
	value.reshape(4)
	if value.oldest() != 8 {
		t.Fatalf("oldest after a trim = %d, want 8", value.oldest())
	}
	if got := string(value.readFrom(10, 5)); got != "kl" {
		t.Fatalf("readFrom(10) after a trim = %q, want %q", got, "kl")
	}

	// A chunk larger than the limit keeps only its tail, and still counts.
	value.append([]byte("0123456789"))
	if value.total != 22 || string(value.readFrom(value.oldest(), 100)) != "23456789" {
		t.Fatalf("after an oversized chunk: total %d, held %q", value.total, value.readFrom(value.oldest(), 100))
	}
}
