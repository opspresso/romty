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
	return configDirectory(agenthooks.ProviderClaude)
}

// configDirectory is where a provider keeps its records, or "" when unknown.
func configDirectory(provider agenthooks.Provider) string {
	directory, err := agenthooks.ConfigDirectory(provider)
	if err != nil {
		return ""
	}
	return directory
}

// sessionTranscripts reads the session record of each tab whose hook reported
// a Claude Code session and its transcript path, or a Kiro CLI session, keyed
// by tab. A tab with no hook, or an agent whose records romty cannot read, is
// simply absent.
func (s *Server) sessionTranscripts(reported map[string]agentRuntime) map[string]transcript.Reading {
	claudeDir, kiroDir := claudeConfigDirectory(), configDirectory(agenthooks.ProviderKiro)
	result := make(map[string]transcript.Reading)
	for tabID, runtime := range reported {
		if runtime.SessionID == "" {
			continue
		}
		var value transcript.Reading
		var ok bool
		switch {
		case runtime.Agent == model.AgentClaude && runtime.TranscriptPath != "":
			value, ok = s.transcripts.ReadClaude(claudeDir, runtime.TranscriptPath, runtime.SessionID)
		case runtime.Agent == model.AgentKiro:
			value, ok = s.transcripts.ReadKiro(kiroDir, runtime.SessionID)
		}
		if ok {
			result[tabID] = value
		}
	}
	return result
}
