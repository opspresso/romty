package daemon

import (
	"time"

	"github.com/opspresso/romty/internal/screen"
)

// screenWait bounds how long an attach waits for the screen follower to reach
// the end of the recording. The follower is normally already there; a client
// is not kept waiting for one that is not. It is a variable so tests need not
// wait it out.
var screenWait = 250 * time.Millisecond

// screenChunkBytes is how much of the recording the follower takes at a time.
// The copy is made with the session lock held, so it is kept short.
const screenChunkBytes = 64 << 10

// screenFollower is the session's side of following what a full-screen
// program has drawn. The picture itself is a screen.Tracker that only the
// follower's goroutine touches; these fields say how far it has read and what
// is waiting for it, and the session lock guards them.
//
// The follower reads the recording at its own pace rather than being handed
// each chunk. The emulator behind it parses whatever a program prints, and
// the PTY read loop must not wait on that: a parser that stalls then costs a
// reattaching client its picture, not every tab its output.
type screenFollower struct {
	// wake tells the goroutine there is something to do. It is nil for a
	// session nothing follows.
	wake chan struct{}
	// position is how far into the recording the tracker has read.
	position uint64
	// events are resizes and snapshot requests, each at the position of the
	// recording it applies to, oldest first.
	events []screenEvent
	// stopped says the goroutine is gone: the session's output ended, or the
	// emulator failed on it. fault is that failure, and unreported says
	// nobody has been told of it yet.
	stopped    bool
	fault      error
	unreported bool
}

// screenEvent is a resize, or with snapshot set a request for the screen as
// it stands at position.
type screenEvent struct {
	position uint64
	columns  uint16
	rows     uint16
	snapshot chan []byte
}

func (s *session) wakeScreen() {
	select {
	case s.screen.wake <- struct{}{}:
	default:
	}
}

// requestScreenLocked asks for the screen as it stands at the end of the
// recording, and returns nil when there is none to ask for: nothing follows
// this session, or its guest is not on the alternate screen.
func (s *session) requestScreenLocked() <-chan []byte {
	if s.screen.wake == nil || s.screen.stopped || !s.guest.set[1049] {
		return nil
	}
	answer := make(chan []byte, 1)
	s.screen.events = append(s.screen.events, screenEvent{position: s.history.total, snapshot: answer})
	return answer
}

// resizeScreenLocked has the follower take the new size when it reaches the
// output that was printed after it.
func (s *session) resizeScreenLocked(columns, rows uint16) {
	if s.screen.wake == nil || s.screen.stopped {
		return
	}
	s.screen.events = append(s.screen.events, screenEvent{position: s.history.total, columns: columns, rows: rows})
	s.wakeScreen()
}

// screenFault returns why following this session's screen stopped, the first
// time it is asked after that happened.
func (s *session) screenFault() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.screen.unreported {
		return nil
	}
	s.screen.unreported = false
	return s.screen.fault
}

// followScreen runs until the session's output ends.
func (s *session) followScreen(columns, rows uint16) {
	tracker := screen.NewTracker(int(columns), int(rows))
	defer tracker.Close()
	for {
		for s.screenStep(tracker) {
		}
		select {
		case <-s.screen.wake:
		case <-s.readDone:
			for s.screenStep(tracker) {
			}
			s.stopFollowingScreen(nil)
			return
		}
	}
}

// screenStep does the next thing waiting for the follower, an event or a
// piece of the recording, and reports whether there was one.
func (s *session) screenStep(tracker *screen.Tracker) bool {
	s.mu.Lock()
	if s.screen.stopped {
		s.mu.Unlock()
		return false
	}
	lost := s.screen.position < s.history.oldest()
	if lost {
		// The recording dropped output the tracker had not read, so its
		// picture has a hole in it. Nobody waiting can be given the screen
		// as of an earlier position any more.
		s.screen.position = s.history.oldest()
		for index := range s.screen.events {
			if answer := s.screen.events[index].snapshot; answer != nil {
				answer <- nil
				s.screen.events[index].snapshot = nil
			}
		}
	}
	var event *screenEvent
	var data []byte
	limit := s.history.total
	if len(s.screen.events) > 0 {
		if next := s.screen.events[0]; next.position <= s.screen.position {
			event = &next
			s.screen.events = s.screen.events[1:]
		} else {
			limit = next.position
		}
	}
	if event == nil {
		data = s.history.readFrom(s.screen.position, min(int(limit-s.screen.position), screenChunkBytes))
		s.screen.position += uint64(len(data))
	}
	s.mu.Unlock()

	if lost {
		tracker.Reset()
	}
	switch {
	case event != nil && event.snapshot != nil:
		event.snapshot <- tracker.Snapshot()
	case event != nil && event.columns > 0:
		tracker.Resize(int(event.columns), int(event.rows))
	case event != nil:
		// A request already answered when its position was lost.
	case len(data) > 0:
		if err := tracker.Write(data); err != nil {
			s.stopFollowingScreen(err)
			return false
		}
	default:
		return false
	}
	return true
}

// stopFollowingScreen ends following for good and answers whoever is waiting.
// It is given the error when the emulator failed on this session's output,
// and nil when the output ended. The session itself is unaffected: a client
// is sent the recording alone, as before.
func (s *session) stopFollowingScreen(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.screen.stopped = true
	s.screen.fault, s.screen.unreported = err, err != nil
	for _, event := range s.screen.events {
		if event.snapshot != nil {
			event.snapshot <- nil
		}
	}
	s.screen.events = nil
}
