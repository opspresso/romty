package daemon

import (
	"strings"

	"github.com/opspresso/romty/internal/codexstate"

	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/protocol"
)

type agentRuntime struct {
	model.AgentStatus
	SessionID           string
	TurnID              string
	PendingTools        map[string]model.AgentPhase
	PendingElicitations int
}

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
		if len(event.SessionID) != 36 || codexstate.SessionPrefix(event.SessionID) != event.SessionID {
			return protocol.Response{}
		}
		// Shared-daemon hook environments do not identify a terminal. Route
		// only by the session identity emitted by that terminal's own TUI.
		matched := ""
		for id, session := range s.sessions {
			if session == nil {
				continue
			}
			title, _ := session.agentIdentity()
			prefix := codexstate.SessionPrefix(title)
			if prefix != "" && strings.HasPrefix(event.SessionID, prefix) {
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

	if _, ok := s.sessions[tabID]; !ok {
		return protocol.Response{Error: errNoSession}
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
			AgentStatus: model.AgentStatus{Agent: event.Agent, Phase: phase},
			SessionID:   event.SessionID,
			TurnID:      event.TurnID,
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
	if (current.Phase == model.AgentPhaseStopped || current.Phase == model.AgentPhaseCompleted || current.Phase == model.AgentPhaseInterrupted || current.Phase == model.AgentPhaseError) &&
		(event.HookEvent == "PostToolUse" || event.HookEvent == "PostToolUseFailure") {
		return protocol.Response{}
	}
	current.Phase = phase
	current.Source = ""
	if event.HookEvent == "UserPromptSubmit" {
		current.PendingTools = nil
		current.PendingElicitations = 0
	}
	switch event.HookEvent {
	case "Elicitation":
		current.PendingElicitations++
	case "ElicitationResult":
		current.PendingElicitations = max(current.PendingElicitations-1, 0)
	}
	if event.ToolUseID != "" {
		switch event.HookEvent {
		case "PreToolUse", "PermissionRequest":
			if phase == model.AgentPhaseWaitingInput || phase == model.AgentPhaseWaitingApproval {
				if current.PendingTools == nil {
					current.PendingTools = make(map[string]model.AgentPhase)
				}
				current.PendingTools[event.ToolUseID] = phase
			}
		case "PostToolUse", "PostToolUseFailure":
			delete(current.PendingTools, event.ToolUseID)
		}
	}
	if event.HookEvent == "Stop" || event.HookEvent == "StopFailure" || event.HookEvent == "Interrupt" {
		current.PendingTools = nil
		current.PendingElicitations = 0
	}
	if current.PendingElicitations > 0 {
		current.Phase = model.AgentPhaseWaitingInput
	}
	for _, pending := range current.PendingTools {
		current.Phase = pending
		if pending == model.AgentPhaseWaitingApproval {
			break
		}
	}
	if event.HookEvent == "UserPromptSubmit" || current.TurnID == "" {
		current.TurnID = event.TurnID
	}
	if current.SessionID == "" {
		current.SessionID = event.SessionID
	}
	s.agentStatuses[tabID] = current
	return protocol.Response{}
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
	case "PostToolUse", "PostToolUseFailure", "ElicitationResult", "PostCompact":
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
		if event.Agent == model.AgentOpenCode {
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
