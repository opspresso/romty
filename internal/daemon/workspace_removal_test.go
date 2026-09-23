package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/opspresso/romty/internal/model"
)

func TestWorkspaceRemovalRemainsInsideOpenedRoot(t *testing.T) {
	base := t.TempDir()
	// Registered roots use canonical paths (including macOS's /private prefix).
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	rootPath, outside := filepath.Join(base, "root"), filepath.Join(base, "outside")
	for _, parent := range []string{rootPath, outside} {
		if err := os.MkdirAll(filepath.Join(parent, "workspace"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	directory, path, err := openWorkspaceForRemoval(model.Root{Path: rootPath}, filepath.Join(rootPath, "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	moved := filepath.Join(base, "moved")
	if err := os.Rename(rootPath, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, rootPath); err != nil {
		t.Fatal(err)
	}
	if err := directory.RemoveAll(filepath.Base(path)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "workspace")); err != nil {
		t.Fatalf("deletion escaped to the replacement root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(moved, "workspace")); !os.IsNotExist(err) {
		t.Fatalf("validated workspace was not removed: %v", err)
	}
}
