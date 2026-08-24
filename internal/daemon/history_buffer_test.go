package daemon

import (
	"bytes"
	"sync"
)

// syncBuffer collects what the emulator writes back, which happens on the copy
// goroutine while the test reads it.
type syncBuffer struct {
	mu      sync.Mutex
	written bytes.Buffer
}

func (b *syncBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.written.Write(data)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.written.String()
}
