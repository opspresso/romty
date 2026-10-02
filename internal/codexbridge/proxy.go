package codexbridge

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"sync"
)

// Relay forwards a WebSocket-over-UDS stream byte for byte, including its HTTP
// upgrade. Only small uncompressed text messages are copied for observation;
// large images/tool results and ping/pong frames pass through without storage.
// The observer runs before the final payload bytes are forwarded so a response
// cannot arrive before its request has been registered.
func Relay(dst io.Writer, src io.Reader, observe func([]byte)) error {
	reader := bufio.NewReaderSize(src, 32<<10)
	headerBytes := 0
	for {
		line, err := reader.ReadSlice('\n')
		headerBytes += len(line)
		if headerBytes > 64<<10 {
			return errors.New("oversized WebSocket upgrade")
		}
		if _, writeErr := dst.Write(line); writeErr != nil {
			return writeErr
		}
		if err != nil {
			return err
		}
		if string(line) == "\r\n" {
			break
		}
		if strings.HasPrefix(string(line), "HTTP/") && !strings.Contains(string(line), " 101 ") {
			_, err := io.Copy(dst, reader)
			return err
		}
	}
	var message []byte
	textMessage, oversized := false, false
	buffer := make([]byte, 32<<10)
	for {
		header := make([]byte, 2, 14)
		if _, err := io.ReadFull(reader, header); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		fin, opcode := header[0]&0x80 != 0, header[0]&0x0f
		masked := header[1]&0x80 != 0
		length := uint64(header[1] & 0x7f)
		switch length {
		case 126:
			header = header[:4]
			if _, err := io.ReadFull(reader, header[2:]); err != nil {
				return err
			}
			length = uint64(binary.BigEndian.Uint16(header[2:]))
		case 127:
			header = header[:10]
			if _, err := io.ReadFull(reader, header[2:]); err != nil {
				return err
			}
			length = binary.BigEndian.Uint64(header[2:])
		}
		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(reader, mask[:]); err != nil {
				return err
			}
			header = append(header, mask[:]...)
		}
		if opcode == 1 || opcode == 2 {
			message = message[:0]
			textMessage, oversized = opcode == 1 && header[0]&0x70 == 0, false
		}
		payload := opcode == 0 || opcode == 1
		if payload && uint64(len(message))+length > 1<<20 {
			message, oversized = nil, true
		}
		if _, err := dst.Write(header); err != nil {
			return err
		}
		for offset := uint64(0); offset < length; {
			chunk := buffer[:min(uint64(len(buffer)), length-offset)]
			if _, err := io.ReadFull(reader, chunk); err != nil {
				return err
			}
			if textMessage && payload && !oversized {
				for i, value := range chunk {
					if masked {
						value ^= mask[(offset+uint64(i))%4]
					}
					message = append(message, value)
				}
			}
			offset += uint64(len(chunk))
			if fin && offset == length && payload && textMessage && !oversized {
				observe(message)
			}
			if _, err := dst.Write(chunk); err != nil {
				return err
			}
		}
		if fin && payload {
			if oversized || !textMessage {
				// Missing an observable message must not leave a working
				// state latched if that message contained turn completion.
				observe(nil)
			} else if length == 0 {
				observe(message)
			}
			message, textMessage, oversized = message[:0], false, false
		}
	}
}

// Proxy closes both directions when either peer disconnects, so a failed
// native status channel cannot leave a last-known working state latched.
func Proxy(front, back io.ReadWriteCloser, observer *Observer) error {
	var once sync.Once
	closeBoth := func() { once.Do(func() { front.Close(); back.Close() }) }
	defer closeBoth()
	defer observer.Disconnect()
	results := make(chan error, 2)
	go func() { results <- Relay(back, front, observer.ClientMessage); closeBoth() }()
	go func() { results <- Relay(front, back, observer.ServerMessage); closeBoth() }()
	first := <-results
	<-results
	return first
}
