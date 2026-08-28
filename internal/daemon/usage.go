package daemon

import (
	"os"
	"path/filepath"

	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/usage"
)

// Agents keep their own ledger: how many tokens the last request carried and
// what the session has cost. romty reads that ledger rather than counting
// anything itself, so the numbers on screen are the agent's own.
//
// Reading it needs the session identifier, which only a hook reports. Without
// one romty cannot tell which of a directory's transcripts belongs to which
// tab, and naming the wrong one would show a number from another conversation.

var userHomeDirectory = os.UserHomeDir

// claudeConfigDirectory is where Claude Code keeps its transcripts, honouring
// the same override the hook installer does.
func claudeConfigDirectory() string {
	if override := os.Getenv("CLAUDE_CONFIG_DIR"); override != "" {
		return override
	}
	home, err := userHomeDirectory()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// sessionUsage reads the agent ledger for each tab whose hook reported a
// session, keyed by tab. A tab with no hook, or an agent whose transcripts
// romty cannot read, is simply absent.
func (s *Server) sessionUsage(reported map[string]agentRuntime, workspaces map[string]string) map[string]usage.Usage {
	configDir := claudeConfigDirectory()
	if configDir == "" {
		return nil
	}
	result := make(map[string]usage.Usage)
	for tabID, runtime := range reported {
		if runtime.Agent != model.AgentClaude || runtime.SessionID == "" {
			continue
		}
		value, ok := s.usage.ReadClaude(configDir, workspaces[tabID], runtime.SessionID)
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
