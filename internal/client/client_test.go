package client

import (
	"path/filepath"
	"testing"
)

func TestNormalizePathExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := normalizePath("~/projects")
	if err != nil {
		t.Fatalf("normalizePath() error = %v", err)
	}
	want := filepath.Join(home, "projects")
	if got != want {
		t.Fatalf("normalizePath() = %q, want %q", got, want)
	}
}
