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
// Reading it needs the session identifier and transcript path, which only a
// hook reports. Without them romty cannot tell which transcript belongs to
// which tab, and naming the wrong one would show another conversation's state.

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
// Claude Code session and its transcript path, keyed by tab. A tab with no
// hook, or an agent whose transcripts romty cannot read, is simply absent.
func (s *Server) sessionTranscripts(reported map[string]agentRuntime) map[string]transcript.Reading {
	configDir := claudeConfigDirectory()
	if configDir == "" {
		return nil
	}
	result := make(map[string]transcript.Reading)
	for tabID, runtime := range reported {
		if runtime.Agent != model.AgentClaude || runtime.SessionID == "" || runtime.TranscriptPath == "" {
			continue
		}
		value, ok := s.transcripts.ReadClaude(configDir, runtime.TranscriptPath, runtime.SessionID)
		if !ok {
			continue
		}
		result[tabID] = value
	}
	return result
}
