// Package codexbridge observes Codex's native app-server protocol on the
// connection owned by one terminal. It never infers completion from output,
// silence, a tool finishing, or a Stop hook.
package codexbridge

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/protocol"
)

type threadState struct {
	turn            string
	status          model.AgentStatus
	outcome         model.AgentPhase
	goal            string
	activityVersion uint64
	goalVersion     uint64
}

type pendingRequest struct {
	sequence uint64
	version  uint64
	method   string
	threadID string
}

// Observer holds identifiers and lifecycle metadata only. Prompt and tool
// contents pass through the proxy but are neither decoded nor retained here.
type Observer struct {
	mu               sync.Mutex
	selected         string
	threads          map[string]threadState
	requests         map[string]pendingRequest
	version          uint64
	requestSequence  uint64
	selectedSequence uint64
	lastSession      string
	lastTurn         string
	lastStatus       model.AgentStatus
	report           func(protocol.AgentEvent)
}

func NewObserver(report func(protocol.AgentEvent)) *Observer {
	return &Observer{threads: make(map[string]threadState), requests: make(map[string]pendingRequest), report: report}
}

type message struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		ThreadID     string `json:"threadId"`
		ThreadSource string `json:"threadSource"`
		Turn         struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
		Status runtimeStatus `json:"status"`
		Goal   *goalStatus   `json:"goal"`
	} `json:"params"`
	Result *struct {
		Goal   goalResult `json:"goal"`
		Thread *struct {
			ID     string        `json:"id"`
			Status runtimeStatus `json:"status"`
			Turns  []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turns"`
		} `json:"thread"`
	} `json:"result"`
}

// A missing field does not confirm that a goal was cleared; an explicit null does.
type goalResult struct {
	present bool
	value   *goalStatus
}

func (g *goalResult) UnmarshalJSON(data []byte) error {
	g.present = true
	return json.Unmarshal(data, &g.value)
}

type goalStatus struct {
	ThreadID string `json:"threadId"`
	Status   string `json:"status"`
}

type runtimeStatus struct {
	Type        string   `json:"type"`
	ActiveFlags []string `json:"activeFlags"`
}

func (o *Observer) ClientMessage(data []byte) {
	var m message
	if json.Unmarshal(data, &m) != nil {
		return
	}
	switch m.Method {
	case "thread/start", "thread/resume", "thread/fork", "thread/goal/get", "thread/goal/set", "thread/goal/clear":
		if len(m.ID) == 0 || len(m.ID) > protocol.MaxAgentEventMetadataBytes {
			return
		}
		var requestID string
		_ = json.Unmarshal(m.ID, &requestID)
		// Codex 0.160 uses this namespace for tool-created background
		// threads on the same connection. They are not TUI navigation.
		if strings.HasPrefix(requestID, "tui-dynamic-") || m.Params.ThreadSource != "" && m.Params.ThreadSource != "user" {
			return
		}
		o.mu.Lock()
		defer o.mu.Unlock()
		// Bound outstanding requests if a malformed peer never responds.
		if len(o.requests) >= 128 {
			clear(o.requests)
		}
		o.requestSequence++
		o.requests[string(m.ID)] = pendingRequest{sequence: o.requestSequence, version: o.version, method: m.Method, threadID: m.Params.ThreadID}
	}
}

func (o *Observer) ServerMessage(data []byte) {
	if len(data) == 0 {
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.selected != "" {
			o.version++
			state := o.threads[o.selected]
			state.status, state.outcome = nativeStatus(model.AgentPhaseUnknown, false), ""
			state.activityVersion = o.version
			o.threads[o.selected] = state
			o.emit(state)
		}
		return
	}
	var m message
	if json.Unmarshal(data, &m) != nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.version++
	if len(m.ID) > 0 && m.Method == "" {
		request, tracked := o.requests[string(m.ID)]
		delete(o.requests, string(m.ID))
		if !tracked || m.Result == nil {
			return
		}
		if strings.HasPrefix(request.method, "thread/goal/") {
			id := request.threadID
			if id == "" || len(id) > protocol.MaxAgentEventMetadataBytes {
				return
			}
			state := o.threads[id]
			if state.goalVersion > request.version {
				return
			}
			goal := m.Result.Goal
			if request.method != "thread/goal/clear" && !goal.present {
				return
			}
			state.goal = ""
			if goal.value != nil {
				if goal.value.ThreadID != id {
					return
				}
				state.goal = goal.value.Status
			}
			state.goalVersion = o.version
			o.store(id, state)
			if id == o.selected {
				o.emit(state)
			}
			return
		}
		if request.sequence >= o.selectedSequence && m.Result.Thread != nil && m.Result.Thread.ID != "" && len(m.Result.Thread.ID) <= protocol.MaxAgentEventMetadataBytes {
			o.selectedSequence = request.sequence
			o.selected = m.Result.Thread.ID
			state := o.threads[o.selected]
			// A resume snapshot replaces cached history, but must not overwrite
			// a live event that arrived while the request was in flight.
			if state.activityVersion <= request.version {
				state.turn, state.outcome = "", ""
				state.status = statusForRuntime(m.Result.Thread.Status, model.AgentStatus{})
				for _, turn := range m.Result.Thread.Turns {
					if turn.Status == "inProgress" && len(turn.ID) <= protocol.MaxAgentEventMetadataBytes {
						state.turn = turn.ID
					}
				}
				state.activityVersion = o.version
			}
			if state.goalVersion <= request.version {
				state.goal, state.goalVersion = "", o.version
			}
			o.store(o.selected, state)
			o.emit(state)
		}
		return
	}
	id := m.Params.ThreadID
	if id == "" || len(id) > protocol.MaxAgentEventMetadataBytes || len(m.Params.Turn.ID) > protocol.MaxAgentEventMetadataBytes {
		return
	}
	state := o.threads[id]
	switch m.Method {
	case "turn/started":
		if m.Params.Turn.ID == "" {
			return
		}
		state.turn = m.Params.Turn.ID
		state.outcome = ""
		if state.status.Phase != model.AgentPhaseWaitingInput && state.status.Phase != model.AgentPhaseWaitingApproval {
			state.status = nativeStatus(model.AgentPhaseWorking, true)
		}
	case "turn/completed":
		if m.Params.Turn.ID == "" {
			return
		}
		if state.turn != "" && state.turn != m.Params.Turn.ID {
			return
		}
		state.turn = m.Params.Turn.ID
		switch m.Params.Turn.Status {
		case "completed":
			state.outcome = model.AgentPhaseCompleted
		case "interrupted":
			state.outcome = model.AgentPhaseInterrupted
		case "failed":
			state.outcome = model.AgentPhaseError
		default:
			return
		}
		if state.status.Phase != model.AgentPhaseWaitingInput && state.status.Phase != model.AgentPhaseWaitingApproval {
			state.status = nativeStatus(state.outcome, false)
		}
	case "thread/status/changed":
		state.status = statusForRuntime(m.Params.Status, state.status)
		if m.Params.Status.Type == "idle" && state.outcome != "" {
			state.status = nativeStatus(state.outcome, false)
		}
	case "thread/closed":
		state.status = nativeStatus(model.AgentPhaseUnknown, false)
	case "thread/goal/updated":
		if m.Params.Goal == nil {
			return
		}
		state.goal = m.Params.Goal.Status
		state.goalVersion = o.version
	case "thread/goal/cleared":
		state.goal = ""
		state.goalVersion = o.version
	default:
		return
	}
	if m.Method != "thread/goal/updated" && m.Method != "thread/goal/cleared" {
		state.activityVersion = o.version
	}
	o.store(id, state)
	if id == o.selected {
		o.emit(state)
	}
}

func (o *Observer) store(id string, state threadState) {
	if _, exists := o.threads[id]; !exists && len(o.threads) >= 256 {
		selected, found := o.threads[o.selected]
		clear(o.threads)
		if found {
			o.threads[o.selected] = selected
		}
	}
	o.threads[id] = state
}

func nativeStatus(phase model.AgentPhase, active bool) model.AgentStatus {
	return model.AgentStatus{Agent: model.AgentCodex, Phase: phase, Source: "runtime", Active: active}
}

func statusForRuntime(status runtimeStatus, previous model.AgentStatus) model.AgentStatus {
	switch status.Type {
	case "active":
		phase := model.AgentPhaseWorking
		for _, flag := range status.ActiveFlags {
			if flag == "waitingOnUserInput" {
				phase = model.AgentPhaseWaitingInput
			}
		}
		for _, flag := range status.ActiveFlags {
			if flag == "waitingOnApproval" {
				phase = model.AgentPhaseWaitingApproval
			}
		}
		return nativeStatus(phase, true)
	case "idle":
		// Idle describes readiness, not the outcome of the preceding turn.
		// Retain a terminal result only if turn/completed established it.
		switch previous.Phase {
		case model.AgentPhaseCompleted, model.AgentPhaseInterrupted, model.AgentPhaseError:
			return previous
		}
		return nativeStatus(model.AgentPhaseIdle, false)
	case "systemError":
		return nativeStatus(model.AgentPhaseError, false)
	default:
		return nativeStatus(model.AgentPhaseUnknown, false)
	}
}

func (o *Observer) emit(state threadState) {
	if o.report != nil {
		if !state.status.Active && (state.status.Phase == model.AgentPhaseCompleted || state.status.Phase == model.AgentPhaseIdle) {
			switch state.goal {
			case "active":
				state.status = nativeStatus(model.AgentPhaseBackground, true)
			case "paused", "blocked", "usageLimited", "budgetLimited":
				state.status = nativeStatus(model.AgentPhaseStopped, false)
			}
		}
		if o.lastSession == o.selected && o.lastTurn == state.turn && o.lastStatus == state.status {
			return
		}
		o.lastSession, o.lastTurn, o.lastStatus = o.selected, state.turn, state.status
		o.report(protocol.AgentEvent{Agent: model.AgentCodex, SessionID: o.selected, TurnID: state.turn, HookEvent: "RuntimeStatus", Runtime: &state.status})
	}
}

func (o *Observer) Disconnect() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.selected != "" {
		status := nativeStatus(model.AgentPhaseUnknown, false)
		if o.report != nil {
			o.report(protocol.AgentEvent{Agent: model.AgentCodex, SessionID: o.selected, HookEvent: "RuntimeDisconnected", Runtime: &status})
		}
	}
	clear(o.threads)
	clear(o.requests)
	o.selected = ""
	o.lastSession = ""
}
