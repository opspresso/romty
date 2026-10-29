package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"time"
)

// settings are the parts of config.json the daemon acts on. The TUI writes
// the file; the daemon reads it each time it needs a value, so a change takes
// effect without a restart.
type settings struct {
	// AllowSleep turns off holding the machine awake while an agent works.
	AllowSleep bool `json:"allow_sleep"`
	// SaveOutput "off" saves no terminal output with a stopped or closed
	// tab: a reopened session then starts empty, still with its agent's
	// resume command. Anything else saves the output.
	SaveOutput string `json:"save_output"`
	// OutputRetentionDays is how long saved sessions are kept, 1 to 30;
	// 0 keeps them 7 days.
	OutputRetentionDays int `json:"output_retention_days"`
}

// settings reads config.json. A missing file is the defaults. Each field is
// read on its own, so one mistyped value does not discard the others. What
// cannot be read errs on the side of privacy and the machine: output is not
// saved and the machine is not held awake.
func (s *Server) settings() settings {
	unreadable := settings{AllowSleep: true, SaveOutput: "off"}
	if s.configPath == "" {
		return settings{AllowSleep: true}
	}
	data, err := os.ReadFile(s.configPath)
	if errors.Is(err, os.ErrNotExist) {
		return settings{}
	}
	var fields map[string]json.RawMessage
	if err == nil {
		err = json.Unmarshal(data, &fields)
	}
	if err != nil {
		s.logger.Printf("read config: %v", err)
		return unreadable
	}
	var value settings
	read := func(name string, target any, fallback func()) {
		raw, ok := fields[name]
		if !ok {
			return
		}
		if err := json.Unmarshal(raw, target); err != nil {
			s.logger.Printf("read config %s: %v", name, err)
			fallback()
		}
	}
	read("allow_sleep", &value.AllowSleep, func() { value.AllowSleep = true })
	read("save_output", &value.SaveOutput, func() { value.SaveOutput = "off" })
	read("output_retention_days", &value.OutputRetentionDays, func() { value.OutputRetentionDays = 0 })
	return value
}

// retention is how long saved sessions are kept.
func (s *Server) retention() time.Duration {
	days := s.settings().OutputRetentionDays
	if days < 1 || days > 30 {
		return defaultRetention
	}
	return time.Duration(days) * 24 * time.Hour
}
