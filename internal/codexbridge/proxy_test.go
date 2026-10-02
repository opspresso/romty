package codexbridge

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func frame(op byte, fin, masked bool, payload []byte) []byte {
	head := []byte{op, 0}
	if fin {
		head[0] |= 0x80
	}
	n := len(payload)
	switch {
	case n < 126:
		head[1] = byte(n)
	case n <= 65535:
		head[1] = 126
		head = binary.BigEndian.AppendUint16(head, uint16(n))
	default:
		head[1] = 127
		head = binary.BigEndian.AppendUint64(head, uint64(n))
	}
	data := append([]byte(nil), payload...)
	if masked {
		head[1] |= 128
		mask := []byte{2, 7, 9, 13}
		head = append(head, mask...)
		for i := range data {
			data[i] ^= mask[i%4]
		}
	}
	return append(head, data...)
}

func TestRelayPreservesWireAndObservesFragmentedLifecycleMessages(t *testing.T) {
	for _, masked := range []bool{false, true} {
		stream := []byte("GET /rpc HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\n\r\n")
		stream = append(stream, frame(1, false, masked, []byte(`{"method":"turn/`))...)
		stream = append(stream, frame(9, true, masked, []byte("ping"))...)
		stream = append(stream, frame(0, true, masked, []byte(`completed"}`))...)
		stream = append(stream, frame(1, true, masked, []byte(strings.Repeat("private", 200000)))...)
		stream = append(stream, frame(1, true, masked, []byte(`{"id":1}`))...)
		var copied bytes.Buffer
		var observed []string
		invalidated := false
		if err := Relay(&copied, bytes.NewReader(stream), func(data []byte) {
			if data == nil {
				invalidated = true
				return
			}
			observed = append(observed, string(data))
		}); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(copied.Bytes(), stream) {
			t.Fatal("proxy changed wire bytes")
		}
		if !invalidated {
			t.Fatal("oversized message silently left status authoritative")
		}
		if len(observed) != 2 || observed[0] != `{"method":"turn/completed"}` || observed[1] != `{"id":1}` {
			t.Fatalf("observed = %q", observed)
		}
	}
}

func TestRelayRegistersRequestBeforeForwardingItsLastBytes(t *testing.T) {
	var output bytes.Buffer
	wire := append([]byte("HTTP/1.1 101 Switching Protocols\r\n\r\n"), frame(1, true, true, []byte(`{"id":1}`))...)
	if err := Relay(&output, bytes.NewReader(wire), func([]byte) {
		if output.Len() >= len(wire) {
			t.Fatal("peer could respond before the observer registered the request")
		}
	}); err != nil {
		t.Fatal(err)
	}
}
