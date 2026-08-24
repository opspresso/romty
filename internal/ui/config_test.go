package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	want := Config{LeftWidth: 24}

	if err := saveConfig(path, want); err != nil {
		t.Fatalf("saveConfig() error = %v", err)
	}
	got, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if got != want {
		t.Fatalf("loadConfig() = %#v, want %#v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestConfigMissingFileUsesResponsiveWidth(t *testing.T) {
	got, err := loadConfig(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if got.LeftWidth != 0 {
		t.Fatalf("loadConfig() = %#v, want responsive width", got)
	}
}
