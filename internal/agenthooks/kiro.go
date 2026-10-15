package agenthooks

import (
	"encoding/json"
	"fmt"
)

// kiroMarker identifies a Kiro hook file as romty's own. It is written into
// each hook's description, the one free-text field Kiro keeps.
const kiroMarker = "romty-kiro-hooks: 1"

// kiroLifecycleTriggers are the Kiro CLI V3 triggers romty needs for a turn.
// SessionEnd is in a file of its own: Kiro IDE and the V2 engine do not list
// that trigger, and a surface that rejects it then skips only that file.
var kiroLifecycleTriggers = []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"}

// kiroHooks returns the generator of one standalone Kiro hook file
// (~/.kiro/hooks/*.json, version v1). Each hook runs `romty hook kiro`, which
// reads the event name from the payload Kiro writes to its standard input.
func kiroHooks(triggers []string) func() ([]byte, error) {
	return func() ([]byte, error) {
		command, err := desiredCommand(ProviderKiro)
		if err != nil {
			return nil, err
		}
		type action struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		}
		type hook struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Trigger     string `json:"trigger"`
			Action      action `json:"action"`
			Timeout     int    `json:"timeout"`
			Enabled     bool   `json:"enabled"`
		}
		hooks := make([]hook, 0, len(triggers))
		for _, trigger := range triggers {
			hooks = append(hooks, hook{
				Name:        "romty " + trigger,
				Description: kiroMarker + " — report " + trigger + " to the romty tab",
				Trigger:     trigger,
				Action:      action{Type: "command", Command: command},
				Timeout:     1,
				Enabled:     true,
			})
		}
		data, err := json.MarshalIndent(map[string]any{"version": "v1", "hooks": hooks}, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("encode Kiro hooks: %w", err)
		}
		return append(data, '\n'), nil
	}
}
