package codexbridge

import (
	"testing"

	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/protocol"
)

func TestNativeLifecycleDoesNotConfuseToolHooksQuestionsAndCompletion(t *testing.T) {
	var events []protocol.AgentEvent
	o := NewObserver(func(e protocol.AgentEvent) { events = append(events, e) })
	o.ClientMessage([]byte(`{"id":1,"method":"thread/start","params":{}}`))
	o.ServerMessage([]byte(`{"id":1,"result":{"thread":{"id":"root","status":{"type":"idle"}}}}`))
	assert := func(phase model.AgentPhase) {
		t.Helper()
		if len(events) == 0 || events[len(events)-1].Runtime.Phase != phase {
			t.Fatalf("events = %+v, want %s", events, phase)
		}
		if err := events[len(events)-1].Validate(); err != nil {
			t.Fatal(err)
		}
	}
	assert(model.AgentPhaseIdle)
	o.ServerMessage([]byte(`{"method":"turn/started","params":{"threadId":"root","turn":{"id":"one","status":"inProgress"}}}`))
	assert(model.AgentPhaseWorking)
	o.ServerMessage(nil)
	assert(model.AgentPhaseUnknown)
	o.ServerMessage([]byte(`{"method":"thread/status/changed","params":{"threadId":"root","status":{"type":"active","activeFlags":[]}}}`))
	assert(model.AgentPhaseWorking)
	for _, event := range []string{
		`{"method":"item/completed","params":{"threadId":"root","item":{"type":"commandExecution"}}}`,
		`{"method":"hook/completed","params":{"threadId":"root","run":{"eventName":"Stop"}}}`,
		`{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"child-turn","status":"completed"}}}`,
	} {
		o.ServerMessage([]byte(event))
		assert(model.AgentPhaseWorking)
	}
	o.ServerMessage([]byte(`{"method":"thread/status/changed","params":{"threadId":"root","status":{"type":"active","activeFlags":["waitingOnUserInput"]}}}`))
	assert(model.AgentPhaseWaitingInput)
	o.ServerMessage([]byte(`{"method":"item/completed","params":{"threadId":"root","item":{"type":"commandExecution"}}}`))
	assert(model.AgentPhaseWaitingInput)
	// Async questions can remain pending even after this turn ends.
	o.ServerMessage([]byte(`{"method":"turn/completed","params":{"threadId":"root","turn":{"id":"one","status":"completed"}}}`))
	assert(model.AgentPhaseWaitingInput)
	o.ServerMessage([]byte(`{"method":"thread/status/changed","params":{"threadId":"root","status":{"type":"idle"}}}`))
	assert(model.AgentPhaseCompleted)
	o.ServerMessage([]byte(`{"method":"turn/started","params":{"threadId":"root","turn":{"id":"two"}}}`))
	o.ServerMessage([]byte(`{"method":"turn/completed","params":{"threadId":"root","turn":{"id":"one","status":"completed"}}}`))
	assert(model.AgentPhaseWorking)
	o.ServerMessage([]byte(`{"method":"thread/status/changed","params":{"threadId":"root","status":{"type":"active","activeFlags":["waitingOnApproval"]}}}`))
	assert(model.AgentPhaseWaitingApproval)
	o.ServerMessage([]byte(`{"method":"thread/status/changed","params":{"threadId":"root","status":{"type":"active","activeFlags":[]}}}`))
	assert(model.AgentPhaseWorking)
	o.ServerMessage([]byte(`{"method":"turn/completed","params":{"threadId":"root","turn":{"id":"two","status":"interrupted"}}}`))
	assert(model.AgentPhaseInterrupted)
	o.Disconnect()
	assert(model.AgentPhaseUnknown)
	if events[len(events)-1].HookEvent != "RuntimeDisconnected" {
		t.Fatal("disconnect did not invalidate the source")
	}
}

func TestNativeIdleIsNotCompletionAndFailureIsDistinct(t *testing.T) {
	var latest protocol.AgentEvent
	o := NewObserver(func(e protocol.AgentEvent) { latest = e })
	o.ClientMessage([]byte(`{"id":"r","method":"thread/resume","params":{"threadId":"root"}}`))
	o.ServerMessage([]byte(`{"id":"r","result":{"thread":{"id":"root","status":{"type":"active","activeFlags":[]}}}}`))
	o.ServerMessage([]byte(`{"method":"thread/status/changed","params":{"threadId":"root","status":{"type":"idle"}}}`))
	if latest.Runtime.Phase != model.AgentPhaseIdle {
		t.Fatal("idle fabricated a turn completion")
	}
	o.ServerMessage([]byte(`{"method":"turn/completed","params":{"threadId":"root","turn":{"id":"t","status":"failed"}}}`))
	if latest.Runtime.Phase != model.AgentPhaseError {
		t.Fatal("failure was not preserved")
	}
	o.ServerMessage([]byte(`{"method":"thread/status/changed","params":{"threadId":"root","status":{"type":"idle"}}}`))
	if latest.Runtime.Phase != model.AgentPhaseError {
		t.Fatal("idle erased the final outcome")
	}
}

func TestGoalContinuationDoesNotAnnounceCompletionBetweenTurns(t *testing.T) {
	var latest protocol.AgentEvent
	o := NewObserver(func(e protocol.AgentEvent) { latest = e })
	o.ClientMessage([]byte(`{"id":1,"method":"thread/start","params":{}}`))
	o.ServerMessage([]byte(`{"id":1,"result":{"thread":{"id":"root","status":{"type":"idle"}}}}`))
	o.ServerMessage([]byte(`{"method":"thread/goal/updated","params":{"threadId":"root","goal":{"status":"active"}}}`))
	o.ServerMessage([]byte(`{"method":"turn/started","params":{"threadId":"root","turn":{"id":"one"}}}`))
	o.ServerMessage([]byte(`{"method":"turn/completed","params":{"threadId":"root","turn":{"id":"one","status":"completed"}}}`))
	if latest.Runtime.Phase != model.AgentPhaseBackground {
		t.Fatalf("active goal announced completion: %+v", latest.Runtime)
	}
	o.ServerMessage([]byte(`{"method":"thread/status/changed","params":{"threadId":"root","status":{"type":"idle"}}}`))
	if latest.Runtime.Phase != model.AgentPhaseBackground {
		t.Fatal("idle gap completed an active goal")
	}
	o.ServerMessage([]byte(`{"method":"thread/goal/updated","params":{"threadId":"root","goal":{"status":"blocked"}}}`))
	if latest.Runtime.Phase != model.AgentPhaseStopped {
		t.Fatal("blocked goal claimed completion")
	}
	o.ServerMessage([]byte(`{"method":"thread/goal/updated","params":{"threadId":"root","goal":{"status":"complete"}}}`))
	if latest.Runtime.Phase != model.AgentPhaseCompleted {
		t.Fatal("completed goal was not reported")
	}
}

func TestBackgroundRequestsAndLateResumeResponsesDoNotReplaceCurrentThread(t *testing.T) {
	var latest protocol.AgentEvent
	o := NewObserver(func(e protocol.AgentEvent) { latest = e })
	for _, id := range []string{"old", "new", "tui-dynamic-child"} {
		o.ClientMessage([]byte(`{"id":"` + id + `","method":"thread/resume","params":{}}`))
	}
	for _, id := range []string{"new", "tui-dynamic-child", "old"} {
		o.ServerMessage([]byte(`{"id":"` + id + `","result":{"thread":{"id":"` + id + `","status":{"type":"idle"}}}}`))
	}
	if latest.SessionID != "new" {
		t.Fatalf("bound %q instead of latest foreground request", latest.SessionID)
	}
}

func TestNewTurnPreservesAnOutstandingAsyncQuestion(t *testing.T) {
	var latest protocol.AgentEvent
	o := NewObserver(func(e protocol.AgentEvent) { latest = e })
	o.ClientMessage([]byte(`{"id":1,"method":"thread/start","params":{}}`))
	o.ServerMessage([]byte(`{"id":1,"result":{"thread":{"id":"root","status":{"type":"active","activeFlags":["waitingOnUserInput"]}}}}`))
	o.ServerMessage([]byte(`{"method":"turn/started","params":{"threadId":"root","turn":{"id":"next"}}}`))
	if latest.Runtime.Phase != model.AgentPhaseWaitingInput {
		t.Fatalf("new turn cleared an unanswered question: %+v", latest.Runtime)
	}
	o.ServerMessage([]byte(`{"method":"thread/status/changed","params":{"threadId":"root","status":{"type":"active","activeFlags":[]}}}`))
	if latest.Runtime.Phase != model.AgentPhaseWorking {
		t.Fatalf("resolved question did not resume work: %+v", latest.Runtime)
	}
}

func TestResumeDoesNotReuseAnOldTurnOutcome(t *testing.T) {
	var latest protocol.AgentEvent
	o := NewObserver(func(e protocol.AgentEvent) { latest = e })
	o.ClientMessage([]byte(`{"id":1,"method":"thread/start","params":{}}`))
	o.ServerMessage([]byte(`{"id":1,"result":{"thread":{"id":"root","status":{"type":"idle"}}}}`))
	o.ServerMessage([]byte(`{"method":"turn/started","params":{"threadId":"root","turn":{"id":"old"}}}`))
	o.ServerMessage([]byte(`{"method":"turn/completed","params":{"threadId":"root","turn":{"id":"old","status":"completed"}}}`))
	o.ClientMessage([]byte(`{"id":2,"method":"thread/resume","params":{"threadId":"root"}}`))
	o.ServerMessage([]byte(`{"id":2,"result":{"thread":{"id":"root","status":{"type":"active","activeFlags":[]},"turns":[]}}}`))
	// Paginated resume omits turns. The resumed turn began while detached.
	o.ServerMessage([]byte(`{"method":"turn/completed","params":{"threadId":"root","turn":{"id":"new","status":"failed"}}}`))
	if latest.Runtime.Phase != model.AgentPhaseError {
		t.Fatalf("resumed failure was discarded using the old turn ID: %+v", latest.Runtime)
	}
}

func TestClearedGoalResponseDoesNotKeepAResumedThreadWorking(t *testing.T) {
	var latest protocol.AgentEvent
	o := NewObserver(func(e protocol.AgentEvent) { latest = e })
	o.ClientMessage([]byte(`{"id":1,"method":"thread/start","params":{}}`))
	o.ServerMessage([]byte(`{"id":1,"result":{"thread":{"id":"root","status":{"type":"idle"}}}}`))
	o.ServerMessage([]byte(`{"method":"thread/goal/updated","params":{"threadId":"root","goal":{"status":"active"}}}`))
	o.ClientMessage([]byte(`{"id":2,"method":"thread/goal/get","params":{"threadId":"root"}}`))
	o.ServerMessage([]byte(`{"id":2,"result":{"goal":null}}`))
	if latest.Runtime.Phase != model.AgentPhaseIdle {
		t.Fatalf("cleared goal still animates: %+v", latest.Runtime)
	}
}

func TestDelayedSnapshotsDoNotReplaceLiveLifecycleEvents(t *testing.T) {
	var latest protocol.AgentEvent
	o := NewObserver(func(e protocol.AgentEvent) { latest = e })
	o.ClientMessage([]byte(`{"id":1,"method":"thread/resume","params":{"threadId":"root"}}`))
	o.ServerMessage([]byte(`{"method":"turn/started","params":{"threadId":"root","turn":{"id":"new"}}}`))
	o.ServerMessage([]byte(`{"id":1,"result":{"thread":{"id":"root","status":{"type":"idle"}}}}`))
	if latest.Runtime.Phase != model.AgentPhaseWorking || latest.TurnID != "new" {
		t.Fatalf("stale resume snapshot replaced live work: %+v", latest)
	}
	o.ClientMessage([]byte(`{"id":2,"method":"thread/goal/get","params":{"threadId":"root"}}`))
	o.ServerMessage([]byte(`{"method":"thread/goal/updated","params":{"threadId":"root","goal":{"status":"active"}}}`))
	o.ServerMessage([]byte(`{"id":2,"result":{"goal":null}}`))
	o.ServerMessage([]byte(`{"method":"turn/completed","params":{"threadId":"root","turn":{"id":"new","status":"completed"}}}`))
	if latest.Runtime.Phase != model.AgentPhaseBackground {
		t.Fatalf("stale goal snapshot cleared the active goal: %+v", latest.Runtime)
	}
}

func TestMissingGoalFieldDoesNotConfirmGoalRemoval(t *testing.T) {
	var latest protocol.AgentEvent
	o := NewObserver(func(e protocol.AgentEvent) { latest = e })
	o.ClientMessage([]byte(`{"id":1,"method":"thread/start","params":{}}`))
	o.ServerMessage([]byte(`{"id":1,"result":{"thread":{"id":"root","status":{"type":"idle"}}}}`))
	o.ServerMessage([]byte(`{"method":"thread/goal/updated","params":{"threadId":"root","goal":{"status":"active"}}}`))
	o.ClientMessage([]byte(`{"id":2,"method":"thread/goal/get","params":{"threadId":"root"}}`))
	o.ServerMessage([]byte(`{"id":2,"result":{}}`))
	if latest.Runtime.Phase != model.AgentPhaseBackground {
		t.Fatal("missing goal field cleared the active goal")
	}
	o.ClientMessage([]byte(`{"id":3,"method":"thread/goal/clear","params":{"threadId":"root"}}`))
	o.ServerMessage([]byte(`{"id":3,"result":{}}`))
	if latest.Runtime.Phase != model.AgentPhaseIdle {
		t.Fatal("successful explicit clear did not remove the goal")
	}
}
