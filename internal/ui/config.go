package ui

import (
	"encoding/json"
	"fmt"

	"github.com/opspresso/romty/internal/jsonfile"
)

const (
	minimumLeftWidth  = 18
	maximumLeftWidth  = 40
	gitDiffViewInline = "inline"
	gitDiffViewSplit  = "split"
)

type Config struct {
	LeftWidth int `json:"left_width,omitempty"`
	// MousePassthrough hands the mouse to applications that ask for it, at the
	// cost of the host terminal's drag selection while they run.
	MousePassthrough bool   `json:"mouse_passthrough,omitempty"`
	GitDiffView      string `json:"git_diff_view,omitempty"`
	unknownFields    map[string]json.RawMessage
}

func (c *Config) UnmarshalJSON(data []byte) error {
	type configValue Config
	var value configValue
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	delete(fields, "left_width")
	delete(fields, "mouse_passthrough")
	delete(fields, "git_diff_view")
	*c = Config(value)
	c.unknownFields = fields
	return nil
}

func (c Config) MarshalJSON() ([]byte, error) {
	type configValue Config
	data, err := json.Marshal(configValue(c))
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for key, value := range c.unknownFields {
		if _, known := fields[key]; !known {
			fields[key] = value
		}
	}
	return json.Marshal(fields)
}

func loadConfig(path string) (Config, error) {
	if path == "" {
		return Config{}, nil
	}
	value, err := jsonfile.Read[Config](path)
	if err != nil {
		return Config{}, err
	}
	if err := validateConfig(value); err != nil {
		return Config{}, err
	}
	return value, nil
}

func saveConfig(path string, value Config) error {
	if path == "" {
		return nil
	}
	if err := validateConfig(value); err != nil {
		return err
	}
	return jsonfile.Write(path, value)
}

func validateConfig(value Config) error {
	if value.LeftWidth != 0 && (value.LeftWidth < minimumLeftWidth || value.LeftWidth > maximumLeftWidth) {
		return fmt.Errorf("left_width must be between %d and %d", minimumLeftWidth, maximumLeftWidth)
	}
	if value.GitDiffView != "" && value.GitDiffView != gitDiffViewInline && value.GitDiffView != gitDiffViewSplit {
		return fmt.Errorf("git_diff_view must be %q or %q", gitDiffViewInline, gitDiffViewSplit)
	}
	return nil
}
