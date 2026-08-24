package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	minimumLeftWidth = 18
	maximumLeftWidth = 40
)

type Config struct {
	LeftWidth int `json:"left_width,omitempty"`
}

func loadConfig(path string) (Config, error) {
	if path == "" {
		return Config{}, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var value Config
	if err := json.Unmarshal(data, &value); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
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
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("set config permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close config: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

func validateConfig(value Config) error {
	if value.LeftWidth != 0 && (value.LeftWidth < minimumLeftWidth || value.LeftWidth > maximumLeftWidth) {
		return fmt.Errorf("left_width must be between %d and %d", minimumLeftWidth, maximumLeftWidth)
	}
	return nil
}
