package protocol_test

import (
	"bufio"
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/opspresso/romty/internal/protocol"
)

func TestMessageRoundTrip(t *testing.T) {
	want := protocol.Request{Action: protocol.ActionResize, TabID: "tab-1", Columns: 120, Rows: 40}
	var stream bytes.Buffer

	if err := protocol.Write(&stream, want); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	var got protocol.Request
	if err := protocol.Read(bufio.NewReader(&stream), &got); err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Read() = %#v, want %#v", got, want)
	}
}

func TestAttachResponseRoundTrip(t *testing.T) {
	want := protocol.Response{Version: protocol.Version, ReplayBytes: 123456}
	var stream bytes.Buffer

	if err := protocol.Write(&stream, want); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	var got protocol.Response
	if err := protocol.Read(bufio.NewReader(&stream), &got); err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Read() = %#v, want %#v", got, want)
	}
}

// A peer that never sends a newline must not be able to grow the reader
// without end: the daemon is a long-lived process reachable by anything
// running as the same user.
func TestReadRefusesAnUnboundedMessage(t *testing.T) {
	var flood bytes.Buffer
	flood.WriteString(`{"action":"`)
	flood.Write(bytes.Repeat([]byte("x"), protocol.MaxMessageBytes+1024))

	var request protocol.Request
	err := protocol.Read(bufio.NewReader(&flood), &request)
	if err == nil {
		t.Fatal("Read() accepted a message with no newline in sight")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Read() error = %v, want the size limit", err)
	}
}

func TestReadAcceptsAMessageLargerThanTheReadBuffer(t *testing.T) {
	long := strings.Repeat("d", 128*1024)
	var encoded bytes.Buffer
	if err := protocol.Write(&encoded, protocol.Request{Action: protocol.ActionAddRoot, Path: long}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	var request protocol.Request
	if err := protocol.Read(bufio.NewReader(&encoded), &request); err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if request.Path != long {
		t.Fatalf("Path length = %d, want %d", len(request.Path), len(long))
	}
}

// The daemon outlives the client binary, so a new client can meet an old
// daemon. A reply with no version is one that predates the field.
func TestVersionDistinguishesAnOlderDaemon(t *testing.T) {
	var encoded bytes.Buffer
	if err := protocol.Write(&encoded, protocol.Response{}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	var response protocol.Response
	if err := protocol.Read(bufio.NewReader(&encoded), &response); err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if response.Version != 0 {
		t.Fatalf("an unstamped reply reports version %d, want 0", response.Version)
	}
	if protocol.Version == 0 {
		t.Fatal("the current version is 0, so an old daemon is indistinguishable")
	}
}
