package workspacefs

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/opspresso/romty/internal/testutil"
)

func TestReadWorkspaceFilesIncludesDotFilesAndExcludesDotDirectories(t *testing.T) {
	workspace := t.TempDir()
	for name, content := range map[string]string{
		"README.md":                 "project\n",
		".env":                      "SECRET=value\n",
		".git/objects/object":       "git data\n",
		"src/.cache/result":         "cached\n",
		"src/.generated.go":         "generated\n",
		"src/main.go":               "package main\n",
		"node_modules/pkg/index.js": "module.exports = {}\n",
	} {
		testutil.WriteFile(t, workspace, name, content)
	}
	if err := os.Symlink(filepath.Join(workspace, "README.md"), filepath.Join(workspace, "linked-file")); err != nil {
		t.Logf("file symlink is unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(workspace, "node_modules"), filepath.Join(workspace, "linked-directory")); err != nil {
		t.Logf("directory symlink is unavailable: %v", err)
	}

	files, err := List(workspace)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	paths := files
	want := []string{".env", "README.md", "node_modules/pkg/index.js", "src/.generated.go", "src/main.go"}
	if !slices.Equal(paths, want) {
		t.Fatalf("workspace files = %#v, want %#v", paths, want)
	}
}

func TestReadWorkspaceFileReturnsContents(t *testing.T) {
	workspace := t.TempDir()
	testutil.WriteFile(t, workspace, "nested/file.txt", "first\nsecond\n")

	content, err := Read(workspace, "nested/file.txt")
	if err != nil || content != "first\nsecond\n" {
		t.Fatalf("Read() = (%q, %v)", content, err)
	}
}

func TestReadWorkspaceFileRejectsReplacedParent(t *testing.T) {
	workspace, outside := t.TempDir(), t.TempDir()
	testutil.WriteFile(t, workspace, "nested/file.txt", "inside")
	testutil.WriteFile(t, outside, "file.txt", "outside")
	files, err := List(workspace)
	if err != nil || len(files) != 1 {
		t.Fatalf("list files: %v, %v", files, err)
	}
	if err := os.Rename(filepath.Join(workspace, "nested"), filepath.Join(workspace, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "nested")); err != nil {
		t.Fatal(err)
	}
	if content, err := Read(workspace, files[0]); err == nil || content != "" {
		t.Fatalf("replaced parent escaped workspace: content %q, error %v", content, err)
	}
}

func TestReadWorkspaceFileRejectsReplacedWorkspace(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	outside := filepath.Join(parent, "outside")
	testutil.WriteFile(t, workspace, "file.txt", "inside")
	testutil.WriteFile(t, outside, "file.txt", "outside")
	files, err := List(workspace)
	if err != nil || len(files) != 1 {
		t.Fatalf("list files: %v, %v", files, err)
	}
	if err := os.Rename(workspace, filepath.Join(parent, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, workspace); err != nil {
		t.Fatal(err)
	}
	if content, err := Read(workspace, files[0]); err == nil || content != "" {
		t.Fatalf("replaced workspace escaped its root: content %q, error %v", content, err)
	}
}

func TestReadWorkspaceFileRejectsOversizedFile(t *testing.T) {
	workspace := t.TempDir()
	file, err := os.Create(filepath.Join(workspace, "large.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := file.Truncate(maxFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	if content, err := Read(workspace, "large.log"); err == nil || content != "" {
		t.Fatalf("oversized file returned %d bytes, error %v", len(content), err)
	}
}
