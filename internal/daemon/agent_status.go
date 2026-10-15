package daemon

import (
	"slices"
	"strings"
	"time"

	"github.com/opspresso/romty/internal/codexstate"

	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/protocol"
)

type agentRuntime struct {
	model.AgentStatus
	// Tools holds every tool call that has started and not finished, with the
	// phase it puts the turn in: work, or a wait for the user's answer or
	// approval. A finished tool cannot see that a sibling still runs.
	Tools               map[string]model.AgentPhase
	PendingElicitations int
	// UpdatedAt is when the newest hook event was recorded. A transcript
	// interrupt counts only if it is newer.
	UpdatedAt time.Time
	// TranscriptPath is the session transcript the newest hook event named.
	TranscriptPath string
}

// agentNow is the clock hook events are stamped with.
var agentNow = time.Now

func (s *Server) recordAgentEvent(tabID string, event *protocol.AgentEvent) protocol.Response {
	if event == nil || tabID == "" && event.Agent != model.AgentCodex {
		return protocol.Response{Error: "tab and agent event are required"}
	}
	if err := event.Validate(); err != nil {
		return protocol.Response{Error: err.Error()}
	}
	// Subagents share the parent session and ROMTY_TAB_ID. Their tool and
	// lifecycle hooks must not replace the foreground agent's phase.
	if event.AgentID != "" {
		return protocol.Response{}
	}

	phase, terminal, recognized := phaseForAgentEvent(*event)
	if !recognized {
		return protocol.Response{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if event.Agent == model.AgentCodex {
		if !codexstate.IsSessionID(event.SessionID) {
			return protocol.Response{}
		}
		// Shared-daemon hook environments do not identify a terminal. Route
		// only by the session identity emitted by that terminal's own TUI.
		matched := ""
		for id, session := range s.sessions {
			if session == nil {
				continue
			}
			if ownsCodexSession(session.codexPrefix(), event.SessionID) {
				if matched != "" {
					return protocol.Response{}
				}
				matched = id
			}
		}
		if matched == "" {
			return protocol.Response{}
		}
		tabID = matched
	}

	value, ok := s.sessions[tabID]
	if !ok {
		return protocol.Response{Error: errNoSession}
	}
	if (event.Agent == model.AgentOpenCode || event.Agent == model.AgentKiro) && !runsInForeground(value, event.ProcessGroups) {
		// The event came from a process outside this tab's foreground job,
		// such as OpenCode's shared background service.
		return protocol.Response{}
	}
	current, exists := s.agentStatuses[tabID]
	if exists && current.Source == "runtime" && current.Agent == event.Agent && current.SessionID == event.SessionID && current.Phase != model.AgentPhaseUnknown {
		return protocol.Response{}
	}
	if terminal {
		if exists && current.Agent == event.Agent &&
			(event.SessionID == "" || current.SessionID == event.SessionID) {
			delete(s.agentStatuses, tabID)
		}
		return protocol.Response{}
	}
	if event.HookEvent == "SessionStart" || !exists || event.Agent == model.AgentCodex && current.SessionID != event.SessionID {
		current = agentRuntime{
			AgentStatus: model.AgentStatus{
				Agent: event.Agent, Phase: phase, SessionID: event.SessionID, TurnID: event.TurnID,
			},
		}
	}
	if current.Agent != event.Agent {
		return protocol.Response{}
	}
	if current.SessionID != "" && event.SessionID != "" && current.SessionID != event.SessionID {
		return protocol.Response{}
	}
	if event.HookEvent != "UserPromptSubmit" && current.TurnID != "" && event.TurnID != "" && current.TurnID != event.TurnID {
		return protocol.Response{}
	}
	// A delayed tool result cannot restart a stopped response. A continued
	// Stop hook can still report fresh work through PreToolUse.
	if turnEnded(current.Phase) && toolFinished(event.HookEvent) {
		return protocol.Response{}
	}
	if event.HookEvent == "Notification" && event.NotificationType == "idle_prompt" {
		// Claude Code finished responding about a minute ago. After a Stop
		// that confirms completion; with no Stop the turn was interrupted,
		// because Claude Code runs no Stop hook for an interrupt.
		switch {
		case current.Phase == model.AgentPhaseStopped || current.Phase == model.AgentPhaseBackground:
			phase = model.AgentPhaseCompleted
		case current.Phase.Running():
			phase = model.AgentPhaseInterrupted
		default:
			return protocol.Response{}
		}
	}
	current.Phase = phase
	current.Source = ""
	current.UpdatedAt = agentNow()
	if event.HookEvent == "UserPromptSubmit" || event.HookEvent == "Stop" || turnEnded(phase) {
		// A new or ended turn has no running tools or open elicitations. A
		// Stop that waits for background work waits for no tool call.
		current.Tools = nil
		current.PendingElicitations = 0
	}
	switch event.HookEvent {
	case "Elicitation":
		current.PendingElicitations++
	case "ElicitationResult":
		current.PendingElicitations = max(current.PendingElicitations-1, 0)
	}
	if event.ToolUseID != "" {
		switch {
		case event.HookEvent == "PreToolUse" || event.HookEvent == "PermissionRequest":
			if current.Tools == nil {
				current.Tools = make(map[string]model.AgentPhase)
			}
			current.Tools[event.ToolUseID] = phase
		case toolFinished(event.HookEvent):
			delete(current.Tools, event.ToolUseID)
		}
	}
	if current.Phase.Running() || current.Phase.Waiting() {
		current.Phase = resolveTurnPhase(current.Phase, current.Tools, current.PendingElicitations)
	}
	if event.HookEvent == "UserPromptSubmit" || current.TurnID == "" {
		current.TurnID = event.TurnID
	}
	if current.SessionID == "" {
		current.SessionID = event.SessionID
	}
	if event.TranscriptPath != "" {
		current.TranscriptPath = event.TranscriptPath
	}
	s.agentStatuses[tabID] = current
	return protocol.Response{}
}

// scanAgents returns the agent in the foreground of each tab. It also forgets
// the hook-reported status of a checked tab whose agent has exited: not every
// exit runs a SessionEnd hook, and a stale status would otherwise come back
// when the same agent starts again and be saved as a session to resume. A
// status reported after the scan began is kept, because the scan cannot have
// seen the agent that reported it.
func (s *Server) scanAgents(sessions map[string]*session) map[string]model.Agent {
	scannedAt := agentNow()
	agents, checked := sessionAgents(sessions)
	s.mu.Lock()
	defer s.mu.Unlock()
	for tabID := range checked {
		runtime, ok := s.agentStatuses[tabID]
		if ok && runtime.Agent != agents[tabID] && runtime.UpdatedAt.Before(scannedAt) {
			delete(s.agentStatuses, tabID)
		}
	}
	return agents
}

// runsInForeground reports whether the tab's foreground job is among the
// process groups above the hook that sent an event.
func runsInForeground(value *session, groups []int) bool {
	if value == nil {
		return false
	}
	foreground, err := value.foregroundProcessGroup()
	return err == nil && slices.Contains(groups, foreground)
}

// resolveTurnPhase lets the turn's open waits and running tools outrank the
// phase of the event that just arrived. An approval outranks a question, a
// question outranks work, and running work outranks thinking.
func resolveTurnPhase(phase model.AgentPhase, tools map[string]model.AgentPhase, elicitations int) model.AgentPhase {
	waiting := model.AgentPhase("")
	running := model.AgentPhase("")
	for _, tool := range tools {
		switch tool {
		case model.AgentPhaseWaitingApproval:
			waiting = tool
		case model.AgentPhaseWaitingInput:
			if waiting == "" {
				waiting = tool
			}
		default:
			running = tool
		}
	}
	switch {
	case waiting == model.AgentPhaseWaitingApproval:
		return waiting
	case elicitations > 0:
		return model.AgentPhaseWaitingInput
	case waiting != "":
		return waiting
	case running != "" && (phase == model.AgentPhaseThinking || phase == model.AgentPhasePlanning):
		if phase == model.AgentPhasePlanning {
			return model.AgentPhasePlanning
		}
		return running
	}
	return phase
}

func toolFinished(hookEvent string) bool {
	return hookEvent == "PostToolUse" || hookEvent == "PostToolUseFailure" || hookEvent == "PermissionDenied"
}

func turnEnded(phase model.AgentPhase) bool {
	switch phase {
	case model.AgentPhaseStopped, model.AgentPhaseCompleted, model.AgentPhaseInterrupted, model.AgentPhaseError:
		return true
	}
	return false
}

// applyTranscriptInterrupt ends a running or waiting turn that the user
// interrupted after its newest hook event. Claude Code runs no hook for an
// interrupt or a rejected tool; its transcript records both.
func applyTranscriptInterrupt(runtime agentRuntime, interruptedAt time.Time) (agentRuntime, bool) {
	if interruptedAt.IsZero() || !interruptedAt.After(runtime.UpdatedAt) ||
		!runtime.Phase.Running() && !runtime.Phase.Waiting() {
		return runtime, false
	}
	runtime.Phase = model.AgentPhaseInterrupted
	runtime.Tools = nil
	runtime.PendingElicitations = 0
	runtime.UpdatedAt = interruptedAt
	return runtime, true
}

func phaseForAgentEvent(event protocol.AgentEvent) (model.AgentPhase, bool, bool) {
	planning := strings.EqualFold(event.PermissionMode, "plan")
	thinking := model.AgentPhaseThinking
	working := model.AgentPhaseWorking
	if planning {
		thinking = model.AgentPhasePlanning
		working = model.AgentPhasePlanning
	}

	switch event.HookEvent {
	case "SessionStart":
		return model.AgentPhaseIdle, false, true
	case "UserPromptSubmit":
		return thinking, false, true
	case "PreToolUse":
		if agentToolNeedsInput(event.ToolName) {
			return model.AgentPhaseWaitingInput, false, true
		}
		return working, false, true
	case "PostToolUse", "PostToolUseFailure", "PermissionDenied", "ElicitationResult", "PostCompact":
		return thinking, false, true
	case "PermissionRequest":
		return model.AgentPhaseWaitingApproval, false, true
	case "Elicitation":
		return model.AgentPhaseWaitingInput, false, true
	case "PreCompact":
		return model.AgentPhaseCompacting, false, true
	case "Notification":
		switch event.NotificationType {
		case "permission_prompt":
			return model.AgentPhaseWaitingApproval, false, true
		case "idle_prompt":
			return model.AgentPhaseCompleted, false, true
		case "agent_needs_input", "elicitation_dialog", "elicitation_url_dialog":
			return model.AgentPhaseWaitingInput, false, true
		default:
			return "", false, false
		}
	case "Stop":
		if event.Background {
			return model.AgentPhaseBackground, false, true
		}
		// OpenCode and Kiro run Stop only for a turn that completed.
		if event.Agent == model.AgentOpenCode || event.Agent == model.AgentKiro {
			return model.AgentPhaseCompleted, false, true
		}
		return model.AgentPhaseStopped, false, true
	case "StopFailure":
		return model.AgentPhaseError, false, true
	case "Interrupt":
		return model.AgentPhaseInterrupted, false, true
	case "SessionEnd":
		return "", true, true
	default:
		return "", false, false
	}
}

func agentToolNeedsInput(name string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(name, "-", ""), "_", ""))
	return normalized == "askuserquestion" || normalized == "requestuserinput"
}
