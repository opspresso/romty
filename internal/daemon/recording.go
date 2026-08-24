package daemon

// recording keeps the last maxHistoryBytes a shell printed, which is what a
// reattaching client is shown so its screen matches the one it left.
//
// It is a ring. The obvious shape — append, then shift the front off once the
// buffer is over its limit — copies the entire recording on every chunk the
// PTY hands over: 8 MiB moved for each 32 KiB read, all of it holding the
// session lock. That capped a terminal's output at the speed of a memmove
// rather than the speed of the shell, around 200 MB/s on a fast machine, and
// every attach, detach and resize queued behind it. A `cat` of a large file or
// a verbose build is well inside the range where that shows.
//
// A ring writes each byte once and never moves it again. The cost of ordering
// the bytes moves to bytes(), which runs once per attach — and attach already
// had to copy the recording, so nothing was added there.
type recording struct {
	// data is the ring. It grows into the limit rather than starting there:
	// every tab holds one, and most of them hold a prompt and a command or
	// two, so claiming the ceiling on the first byte would make twenty idle
	// terminals cost what twenty busy ones do.
	data []byte
	// start is where the oldest byte sits, and size how many are held. Until
	// the ring has wrapped, start is 0 and size is how far into data the
	// recording reaches.
	start int
	size  int
}

// append records data, dropping whatever no longer fits.
func (r *recording) append(data []byte) {
	limit := maxHistoryBytes
	if limit <= 0 {
		r.data, r.start, r.size = nil, 0, 0
		return
	}
	if len(data) == 0 {
		return
	}
	// Only the tail of an oversized chunk can survive, and writing the rest
	// would be writing over it.
	if len(data) > limit {
		data = data[len(data)-limit:]
	}
	if len(r.data) > limit {
		// The limit shrank, which only a test does, but a ring longer than the
		// limit would hold more than it is allowed to.
		r.reshape(limit)
	}
	if needed := r.size + len(data); needed > len(r.data) && len(r.data) < limit {
		// Doubling, so filling the recording costs a constant number of copies
		// per byte rather than one pass per chunk.
		r.reshape(min(max(needed, 2*len(r.data)), limit))
	}

	capacity := len(r.data)
	end := (r.start + r.size) % capacity
	written := copy(r.data[end:], data)
	copy(r.data, data[written:])

	r.size += len(data)
	if r.size > capacity {
		// The write ran over the oldest bytes, so the oldest is now wherever
		// it stopped.
		r.start = (r.start + r.size - capacity) % capacity
		r.size = capacity
	}
}

// bytes returns what is held, oldest first, as a slice of its own. The copy is
// what makes it safe to replay with the session lock released: append writes
// into the ring in place, so an aliased slice would be rewritten under the
// replay's feet.
func (r *recording) bytes() []byte {
	result := make([]byte, r.size)
	if r.size == 0 {
		return result
	}
	front := copy(result, r.data[r.start:min(r.start+r.size, len(r.data))])
	copy(result[front:], r.data)
	return result
}

// reshape rebuilds the ring at a new length, keeping the newest bytes that
// still fit. It is how the ring both grows towards the limit and follows a
// limit that moved.
func (r *recording) reshape(capacity int) {
	held := r.bytes()
	if len(held) > capacity {
		held = held[len(held)-capacity:]
	}
	r.data = make([]byte, capacity)
	// Laid out from the front, so the oldest byte is at index zero and the
	// next write lands at start+size, which is where held ends.
	copy(r.data, held)
	r.start = 0
	r.size = len(held)
}
