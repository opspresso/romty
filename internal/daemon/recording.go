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
	// data is the ring, always exactly the limit it was built for. A limit
	// that changes — which only tests do — rebuilds it around what is held.
	data []byte
	// start is where the oldest byte sits, and size how many are held. The
	// two are the whole of the ring's state: size < len(data) means it has
	// not wrapped yet, and start is 0.
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
	if len(r.data) != limit {
		r.resize(limit)
	}
	// Only the tail of an oversized chunk can survive, and writing the rest
	// would be writing over it.
	if len(data) > limit {
		data = data[len(data)-limit:]
	}

	end := (r.start + r.size) % limit
	written := copy(r.data[end:], data)
	copy(r.data, data[written:])

	r.size += len(data)
	if r.size > limit {
		// The write ran over the oldest bytes, so the oldest is now wherever
		// it stopped.
		r.start = (r.start + r.size - limit) % limit
		r.size = limit
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

// resize rebuilds the ring at a new limit, keeping the newest bytes that still
// fit. Only tests change the limit, but a ring whose length disagrees with it
// would wrap at the wrong place, so this is not an optional tidy-up.
func (r *recording) resize(limit int) {
	held := r.bytes()
	if len(held) > limit {
		held = held[len(held)-limit:]
	}
	r.data = make([]byte, limit)
	// Laid out from the front, so the oldest byte is at index zero and the
	// next write lands at start+size, which is where held ends.
	copy(r.data, held)
	r.start = 0
	r.size = len(held)
}
