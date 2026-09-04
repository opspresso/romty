package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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
		writeDiffFile(t, workspace, name, content)
	}
	if err := os.Symlink(filepath.Join(workspace, "README.md"), filepath.Join(workspace, "linked-file")); err != nil {
		t.Logf("file symlink is unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(workspace, "node_modules"), filepath.Join(workspace, "linked-directory")); err != nil {
		t.Logf("directory symlink is unavailable: %v", err)
	}

	files, err := readWorkspaceFiles(workspace)
	if err != nil {
		t.Fatalf("readWorkspaceFiles() error = %v", err)
	}
	paths := make([]string, len(files))
	for index, file := range files {
		paths[index] = file.Path
	}
	want := []string{".env", "README.md", "node_modules/pkg/index.js", "src/.generated.go", "src/main.go"}
	if !slices.Equal(paths, want) {
		t.Fatalf("workspace files = %#v, want %#v", paths, want)
	}
}

func TestReadWorkspaceFileReturnsContents(t *testing.T) {
	workspace := t.TempDir()
	writeDiffFile(t, workspace, "nested/file.txt", "first\nsecond\n")

	content, err := readWorkspaceFile(workspace, "nested/file.txt")
	if err != nil || content != "first\nsecond\n" {
		t.Fatalf("readWorkspaceFile() = (%q, %v)", content, err)
	}
}

func TestHighlightWorkspaceFileSyntax(t *testing.T) {
	lines := normalizeWorkspaceFileLines("package main\n\n\tfunc main() {}\n")
	syntax, highlighted := highlightWorkspaceFileSyntax("main.go", lines)
	if !highlighted || len(syntax) != len(lines) || !syntax[0].newHighlighted ||
		gitSyntaxText(syntax[0].new) != "package main" {
		t.Fatalf("workspace file syntax = (%#v, %t)", syntax, highlighted)
	}
	for _, line := range lines {
		if strings.ContainsRune(line, '\t') {
			t.Fatalf("normalized workspace file contains a tab: %q", line)
		}
	}
}
