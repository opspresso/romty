package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/opspresso/romty/internal/gitrepo"
	"github.com/opspresso/romty/internal/model"
)

// fakeStream hands out the events and the error it is given, in order.
type fakeStream struct {
	events chan model.Event
	err    error
	closed bool
}

func (s *fakeStream) Next() (model.Event, error) {
	event, ok := <-s.events
	if !ok {
		return model.Event{}, s.err
	}
	return event, nil
}

func (s *fakeStream) Close() error {
	s.closed = true
	return nil
}

// Pushed events take the paths polled results take: the tree, the agent
// statuses and the Git states all land, and the next event is awaited.
func TestPushedEventsUpdateTheDashboard(t *testing.T) {
	snapshot, workspace := alphaWorkspaceSnapshot(openTabs(1))
	stream := &fakeStream{events: make(chan model.Event, 1)}
	backend := &fakeBackend{snapshot: snapshot, events: stream}
	value := newDashboard(backend, model.Snapshot{})

	send(&value, value.subscribe()())
	if value.events != stream || value.polling {
		t.Fatal("the dashboard did not take the stream")
	}
	snapshot.Revision = 7
	stream.events <- model.Event{
		Snapshot:      &snapshot,
		AgentStatuses: map[string]model.AgentStatus{"tab-1": {Agent: model.AgentClaude, Phase: model.AgentPhaseWorking}},
		GitStates:     map[string]gitrepo.State{workspace.Path: {Branch: "main"}},
	}
	command := send(&value, nextEvent(stream)())
	if value.state.Revision != 7 || value.gitStates[workspace.Path].Branch != "main" {
		t.Fatalf("after the event = (revision %d, git %+v)", value.state.Revision, value.gitStates)
	}
	if value.state.Roots[0].Directories[0].Tabs[0].AgentPhase != model.AgentPhaseWorking {
		t.Fatal("the pushed agent status was not applied")
	}
	if command == nil {
		t.Fatal("the next event is not awaited")
	}

	// Asking for Git while subscribed asks the daemon rather than running git.
	value.readGitStatus(true, false)()
	if len(backend.gitRefreshes) != 1 || !backend.gitRefreshes[0] {
		t.Fatalf("refreshes = %v, want one fetch asked of the daemon", backend.gitRefreshes)
	}

	// A stream that ends with the daemon puts the dashboard in the stopped
	// state and tries again.
	stream.err = gone
	close(stream.events)
	if command := send(&value, nextEvent(stream)()); command == nil || !value.daemonDown || value.events != nil || !stream.closed {
		t.Fatalf("after the stream ended = (down %v, events %v, closed %v)", value.daemonDown, value.events, stream.closed)
	}
}

// A daemon from before subscriptions is polled, once.
func TestAnOldDaemonIsPolled(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	if command := send(&value, value.subscribe()()); command == nil || !value.polling {
		t.Fatal("an old daemon did not start polling")
	}
	if command := send(&value, subscribedMsg{err: model.ErrNeedsNewerDaemon}); command != nil {
		t.Fatal("polling started twice")
	}
	if send(&value, subscribedMsg{err: errors.New("x")}); value.daemonDown {
		t.Fatal("an ordinary error was taken for a stopped daemon")
	}
}

// A second subscribe that wins a race is closed, a retry while subscribed
// does nothing, and an event from a stream no longer read closes it.
func TestDuplicateSubscriptionsAreClosed(t *testing.T) {
	first := &fakeStream{events: make(chan model.Event)}
	second := &fakeStream{events: make(chan model.Event)}
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	send(&value, subscribedMsg{stream: first})
	send(&value, subscribedMsg{stream: second})
	if value.events != first || !second.closed {
		t.Fatalf("after two subscribes = (kept first %v, second closed %v)", value.events == first, second.closed)
	}
	if command := send(&value, resubscribeMsg{}); command != nil {
		t.Fatal("a retry subscribed again while subscribed")
	}
	stale := &fakeStream{events: make(chan model.Event)}
	send(&value, eventMsg{stream: stale})
	if !stale.closed {
		t.Fatal("an event from a stale stream did not close it")
	}
}

// A daemon that refuses another subscriber is polled, and the dashboard says so.
func TestARefusedSubscriptionPolls(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	command := send(&value, subscribedMsg{err: &model.RefusedError{Message: "too many subscriptions"}})
	if command == nil || !value.polling || !strings.Contains(value.errorMessage, "too many subscriptions") {
		t.Fatalf("refused subscription = (polling %v, message %q)", value.polling, value.errorMessage)
	}
}
