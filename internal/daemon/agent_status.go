package daemon

import (
	"fmt"
	"strings"

	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/protocol"
)

type agentRuntime struct {
	model.AgentStatus
	SessionID string
}

func (s *Server) recordAgentEvent(tabID string, event *protocol.AgentEvent) protocol.Response {
	if tabID == "" || event == nil {
		return protocol.Response{Error: "tab and agent event are required"}
	}
	if event.Agent != model.AgentClaude && event.Agent != model.AgentCodex {
		return protocol.Response{Error: fmt.Sprintf("unsupported agent %q", event.Agent)}
	}
	for _, metadata := range []string{
		event.SessionID, event.HookEvent, event.ToolName,
		event.NotificationType, event.PermissionMode,
	} {
		if len(metadata) > 512 {
			return protocol.Response{Error: "agent event metadata is too long"}
		}
	}

	phase, terminal, recognized := phaseForAgentEvent(*event)
	if !recognized {
		return protocol.Response{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[tabID]; !ok {
		return protocol.Response{Error: "running terminal session not found"}
	}
	current, exists := s.agentStatuses[tabID]
	if terminal {
		if exists && current.Agent == event.Agent &&
			(event.SessionID == "" || current.SessionID == event.SessionID) {
			delete(s.agentStatuses, tabID)
		}
		return protocol.Response{}
	}
	if event.HookEvent == "SessionStart" || !exists {
		s.agentStatuses[tabID] = agentRuntime{
			AgentStatus: model.AgentStatus{Agent: event.Agent, Phase: phase},
			SessionID:   event.SessionID,
		}
		return protocol.Response{}
	}
	if current.Agent != event.Agent {
		return protocol.Response{}
	}
	if current.SessionID != "" && event.SessionID != "" && current.SessionID != event.SessionID {
		return protocol.Response{}
	}
	current.Phase = phase
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
		case "idle_prompt", "agent_needs_input", "elicitation_dialog", "elicitation_url_dialog":
			return model.AgentPhaseWaitingInput, false, true
		default:
			return "", false, false
		}
	case "Stop":
		if event.Background {
			return model.AgentPhaseBackground, false, true
		}
		return model.AgentPhaseIdle, false, true
	case "StopFailure":
		return model.AgentPhaseError, false, true
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
