package ui

import (
	"fmt"

	"github.com/opspresso/romty/internal/jsonfile"
)

const (
	minimumLeftWidth = 18
	maximumLeftWidth = 40
)

type Config struct {
	LeftWidth int `json:"left_width,omitempty"`
	// MousePassthrough hands the mouse to applications that ask for it, at the
	// cost of the host terminal's drag selection while they run.
	MousePassthrough bool `json:"mouse_passthrough,omitempty"`
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
	return nil
}
