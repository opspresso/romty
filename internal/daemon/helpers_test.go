package daemon

import (
	"net"
	"time"
)

// Test conveniences over the production entry points.

func (s *session) attach(connection net.Conn) error {
	return s.attachClientReady(connection, "", func(int, uint16, uint16) error { return nil })
}

// attachReady announces the exact initial replay before writing it. The
// client can consume that history off-screen, then treat everything after the
// boundary as live output.
func (s *session) attachReady(connection net.Conn, ready func(int) error) error {
	return s.attachClientReady(connection, "", func(replayBytes int, _, _ uint16) error {
		return ready(replayBytes)
	})
}

// Tests scan agent statuses repeatedly as they change the processes behind
// them, so they run without the status cache; cache_test.go turns it back on.
func init() {
	agentStatusCacheTTL = 0
	terminateGrace = 200 * time.Millisecond
}
