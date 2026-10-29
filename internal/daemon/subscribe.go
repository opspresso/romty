package daemon

import (
	"io"
	"maps"
	"net"
	"reflect"
	"sync"
	"time"

	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/protocol"
)

// publishInterval is how often the publisher looks for changes to send. It
// matches what a TUI polled for agent statuses, halved.
var publishInterval = time.Second

// maxSubscribers bounds the open subscriptions; each is a TUI.
const maxSubscribers = 32

// subscriber is one open subscription. events is buffered so the publisher
// never waits on a slow client; a client that falls that far behind is
// disconnected and resubscribes.
type subscriber struct {
	events chan model.Event
	done   chan struct{}
}

// subscriptions is the publisher's state: who listens, and what they were
// last sent, so only changes are sent.
type subscriptions struct {
	mu       sync.Mutex
	members  map[*subscriber]struct{}
	revision uint64
	bells    map[string]time.Time
	statuses map[string]model.AgentStatus
	git      map[string]model.GitState
	// refresh asks the publisher to read Git now; true also fetches.
	refresh chan bool
}

func newSubscriptions() *subscriptions {
	return &subscriptions{members: make(map[*subscriber]struct{}), refresh: make(chan bool, 1)}
}

func (s *subscriptions) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.members)
}

// requestRefresh asks for a Git read without waiting for it. A fetch
// request is not lost to a pending plain one.
func (s *subscriptions) requestRefresh(fetch bool) {
	select {
	case s.refresh <- fetch:
	default:
		if fetch {
			select {
			case <-s.refresh:
			default:
			}
			select {
			case s.refresh <- true:
			default:
			}
		}
	}
}

// handleSubscribe answers a subscribe request and streams events until the
// client goes or the daemon stops. The first event is the whole picture.
func (s *Server) handleSubscribe(connection net.Conn, request protocol.Request) {
	value := &subscriber{events: make(chan model.Event, 16), done: make(chan struct{})}
	s.subs.mu.Lock()
	if len(s.subs.members) >= maxSubscribers {
		s.subs.mu.Unlock()
		_ = replyFor(connection, request, protocol.Response{Error: "too many subscriptions"})
		return
	}
	s.subs.members[value] = struct{}{}
	git := maps.Clone(s.subs.git)
	s.subs.mu.Unlock()
	defer func() {
		s.subs.mu.Lock()
		delete(s.subs.members, value)
		s.subs.mu.Unlock()
	}()
	if err := replyFor(connection, request, protocol.Response{}); err != nil {
		return
	}
	// A subscriber sends nothing after its request, so a read returns only
	// when it goes. Without this a TUI that quit or lost its SSH session
	// would hold its slot until the next write failed, which on a quiet
	// daemon may be never.
	gone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, connection)
		close(gone)
	}()
	snapshot := s.snapshot()
	first := model.Event{Snapshot: &snapshot, AgentStatuses: s.agentStatusesSnapshot(), GitStates: git}
	if err := writeEvent(connection, first); err != nil {
		return
	}
	if git == nil {
		// The first subscriber of a daemon has no Git state yet.
		s.subs.requestRefresh(false)
	}
	for {
		select {
		case event := <-value.events:
			if err := writeEvent(connection, event); err != nil {
				return
			}
		case <-value.done:
			return
		case <-gone:
			return
		case <-s.stop:
			return
		}
	}
}

func writeEvent(connection net.Conn, event model.Event) error {
	if err := connection.SetWriteDeadline(time.Now().Add(requestTimeout)); err != nil {
		return err
	}
	return protocol.Write(connection, event)
}

// broadcast sends event to every subscriber, dropping one whose queue is full.
func (s *Server) broadcast(event model.Event) {
	s.subs.mu.Lock()
	defer s.subs.mu.Unlock()
	for member := range s.subs.members {
		select {
		case member.events <- event:
		default:
			delete(s.subs.members, member)
			close(member.done)
		}
	}
}

// publish runs while the daemon does, sending subscribers what changed in
// the tree and the agent statuses. Git is read on a loop of its own, because a
// fetch can take many seconds and must not hold back an agent's change. With
// no subscriber neither loop does anything.
func (s *Server) publish() {
	ticker := time.NewTicker(publishInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
		}
		if s.subs.count() == 0 {
			continue
		}
		s.publishOnce()
	}
}

// publishGit reads Git state for the subscribers on its own schedule, and at
// once when one asks.
func (s *Server) publishGit() {
	ticker := time.NewTicker(gitPollInterval)
	defer ticker.Stop()
	for {
		force, fetch := false, false
		select {
		case <-s.stop:
			return
		case <-ticker.C:
		case fetch = <-s.subs.refresh:
			force = true
		}
		if s.subs.count() == 0 {
			continue
		}
		fetch = fetch || s.git.fetchDue()
		git := s.git.poll(s.workspacePaths(), s.printedSince, force, fetch)
		s.subs.mu.Lock()
		changed := force || !maps.Equal(git, s.subs.git)
		s.subs.git = git
		s.subs.mu.Unlock()
		if changed {
			s.broadcast(model.Event{GitStates: git})
		}
	}
}

func (s *Server) publishOnce() {
	var event model.Event
	s.mu.Lock()
	revision := s.revision
	bells := make(map[string]time.Time, len(s.sessions))
	for id, value := range s.sessions {
		bells[id] = value.lastBell()
	}
	s.mu.Unlock()

	s.subs.mu.Lock()
	treeChanged := revision != s.subs.revision || !maps.Equal(bells, s.subs.bells)
	s.subs.mu.Unlock()

	if treeChanged {
		snapshot := s.snapshot()
		event.Snapshot = &snapshot
	}
	statuses := s.agentStatusesSnapshot()

	s.subs.mu.Lock()
	if treeChanged {
		s.subs.revision, s.subs.bells = revision, bells
	}
	if !reflect.DeepEqual(statuses, s.subs.statuses) {
		s.subs.statuses = statuses
		event.AgentStatuses = statuses
	}
	s.subs.mu.Unlock()
	if event.Snapshot != nil || event.AgentStatuses != nil {
		s.broadcast(event)
	}
}
