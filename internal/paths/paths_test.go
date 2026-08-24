package paths_test

import (
	"path/filepath"
	"testing"

	"github.com/opspresso/romty/internal/paths"
)

func TestResolveIncludesConfigPath(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("ROMTY_HOME", directory)

	got, err := paths.Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	want := filepath.Join(directory, "config.json")
	if got.Config != want {
		t.Fatalf("Config = %q, want %q", got.Config, want)
	}
}
