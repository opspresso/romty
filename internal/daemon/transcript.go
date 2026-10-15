package daemon

import (
	"github.com/opspresso/romty/internal/agenthooks"
	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/transcript"
)

// Agents keep their own record of a session: how many tokens the last request
// carried, what the session has cost, and whether the user interrupted its last
// turn. romty reads that record rather than counting or guessing anything, so
// the numbers and the interrupted phase on screen are the agent's own.
//
// Reading it needs the session identifier, which only a hook reports. Without
// one romty cannot tell which of a directory's transcripts belongs to which
// tab, and naming the wrong one would show another conversation's state.

// claudeConfigDirectory is where Claude Code keeps its transcripts. The hook
// installer already knows how to find a provider's directory, including the
// environment variable that overrides it, so it is asked rather than copied.
func claudeConfigDirectory() string {
	directory, err := agenthooks.ConfigDirectory(agenthooks.ProviderClaude)
	if err != nil {
		return ""
	}
	return directory
}

// sessionTranscripts reads the transcript of each tab whose hook reported a
// Claude Code session, keyed by tab. A tab with no hook, or an agent whose
// transcripts romty cannot read, is simply absent.
func (s *Server) sessionTranscripts(reported map[string]agentRuntime, workspaces map[string]string) map[string]transcript.Reading {
	configDir := claudeConfigDirectory()
	if configDir == "" {
		return nil
	}
	result := make(map[string]transcript.Reading)
	for tabID, runtime := range reported {
		if runtime.Agent != model.AgentClaude || runtime.SessionID == "" {
			continue
		}
		value, ok := s.transcripts.ReadClaude(configDir, workspaces[tabID], runtime.SessionID)
		if !ok {
			continue
		}
		result[tabID] = value
	}
	return result
}

// tabWorkspaces maps each running tab to the directory its shell was started
// in. It must be called with the server lock held.
func (s *Server) tabWorkspaces() map[string]string {
	paths := make(map[string]string, len(s.value.Workspaces))
	for _, workspace := range s.value.Workspaces {
		paths[workspace.ID] = workspace.Path
	}
	result := make(map[string]string, len(s.value.Tabs))
	for _, tab := range s.value.Tabs {
		if path := paths[tab.WorkspaceID]; path != "" {
			result[tab.ID] = path
		}
	}
	return result
}
