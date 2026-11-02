package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opspresso/romty/internal/testutil"
)

func TestPullChildrenContinuesAfterFailure(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspaces")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(base, "remote.git")
	source := filepath.Join(base, "source")
	testutil.RunGit(t, "init", "--bare", "--initial-branch=main", remote)
	testutil.RunGit(t, "init", "--initial-branch=main", source)
	testutil.WriteFile(t, source, "file", "old")
	testutil.RunGit(t, "-C", source, "add", "file")
	testutil.RunGit(t, "-C", source, "commit", "-m", "initial")
	testutil.RunGit(t, "-C", source, "remote", "add", "origin", remote)
	testutil.RunGit(t, "-C", source, "push", "-u", "origin", "main")
	good := filepath.Join(root, "z-good")
	conflict := filepath.Join(root, "a-dirty")
	testutil.RunGit(t, "clone", remote, good)
	testutil.RunGit(t, "clone", remote, conflict)
	testutil.WriteFile(t, conflict, "file", "local")
	testutil.WriteFile(t, source, "file", "updated")
	testutil.RunGit(t, "-C", source, "commit", "-am", "update")
	testutil.RunGit(t, "-C", source, "push")
	testutil.WriteFile(t, root, "ordinary/file", "untouched")
	output, err := PullChildren(context.Background(), root)
	if err != nil || !strings.Contains(output, "1 succeeded, 1 failed") || !strings.Contains(output, "a-dirty: ERROR") || !strings.Contains(output, "z-good: OK") {
		t.Fatalf("PullChildren = (%q, %v)", output, err)
	}
	for path, want := range map[string]string{filepath.Join(good, "file"): "updated", filepath.Join(conflict, "file"): "local"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("read %s = (%q, %v), want %q", path, got, err, want)
		}
	}
}

func TestPullChildrenReportsUnreadableRootAndCancellation(t *testing.T) {
	if _, err := PullChildren(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing root succeeded")
	}
	root := t.TempDir()
	testutil.WriteFile(t, root, "child/file", "x")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PullChildren(ctx, root); err != context.Canceled {
		t.Fatalf("cancelled pull error = %v", err)
	}
}
