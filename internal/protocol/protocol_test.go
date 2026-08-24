package protocol_test

import (
	"bufio"
	"bytes"
	"reflect"
	"testing"

	"github.com/nalbam/romty/internal/protocol"
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
