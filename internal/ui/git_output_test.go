package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCappedGitOutputCancelsOnlyOnOverflow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := cappedGitOutput{limit: 4, cancel: cancel}
	if n, err := output.Write([]byte("abcd")); n != 4 || err != nil || ctx.Err() != nil {
		t.Fatalf("exact limit: wrote %d, error %v, cancelled %v", n, err, ctx.Err())
	}
	for range 2 {
		if n, err := output.Write([]byte("extra")); n != 5 || err != nil {
			t.Fatalf("overflow drain: wrote %d, error %v", n, err)
		}
	}
	if ctx.Err() == nil || output.buffer.String() != "abcd" || !output.exceeded {
		t.Fatalf("overflow did not bound output and cancel: %q, %v", output.buffer.String(), ctx.Err())
	}
}

func TestGitDiffRejectsOversizedOutput(t *testing.T) {
	repository := initializeDiffRepository(t)
	writeDiffFile(t, repository, "large.txt", strings.Repeat("line\n", maximumGitOutputBytes/5))
	diff, err := readGitFileDiff(repository, gitChangedFile{Path: "large.txt", IndexStatus: '?', WorkTreeStatus: '?'})
	if err == nil || !strings.Contains(err.Error(), "output exceeds") || len(diff) != 0 {
		t.Fatalf("oversized diff: %d bytes, error %v", len(diff), err)
	}
}

func TestGitStatusRejectsOversizedOutput(t *testing.T) {
	bin := t.TempDir()
	git := filepath.Join(bin, "git")
	if err := os.WriteFile(git, []byte("#!/bin/sh\ndd if=/dev/zero bs=1048576 count=9 2>/dev/null\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if files, err := readGitChangedFiles(workspace); err == nil || !strings.Contains(err.Error(), "output exceeds") || files != nil {
		t.Fatalf("oversized changed-file status: %v, %v", files, err)
	}
	if _, ok := readGitState(workspace, false); ok {
		t.Fatal("oversized background Git status was accepted")
	}
}
