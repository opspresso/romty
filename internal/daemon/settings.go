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

// settings reads config.json. A missing file is the defaults. A file that
// cannot be read or parsed is logged and treated as the defaults too, except
// that keeping awake is then off: the daemon does not hold a machine awake
// on a setting it could not read.
func (s *Server) settings() settings {
	if s.configPath == "" {
		return settings{AllowSleep: true}
	}
	data, err := os.ReadFile(s.configPath)
	if errors.Is(err, os.ErrNotExist) {
		return settings{}
	}
	var value settings
	if err == nil {
		err = json.Unmarshal(data, &value)
	}
	if err != nil {
		s.logger.Printf("read config: %v", err)
		return settings{AllowSleep: true}
	}
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
