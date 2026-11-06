package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/opspresso/romty/internal/testutil"
)

func TestPullChildrenPullsOnlyMainRepositoriesBehindUpstream(t *testing.T) {
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
	testutil.RunGit(t, "-C", source, "push", "origin", "main:feature")
	good := filepath.Join(root, "z-good")
	conflict := filepath.Join(root, "a-dirty")
	feature := filepath.Join(root, "b-feature")
	local := filepath.Join(root, "c-local")
	for _, path := range []string{good, conflict, feature} {
		testutil.RunGit(t, "clone", remote, path)
	}
	testutil.RunGit(t, "-C", feature, "switch", "feature")
	testutil.RunGit(t, "init", "--initial-branch=main", local)
	testutil.WriteFile(t, conflict, "file", "local")
	testutil.WriteFile(t, source, "file", "updated")
	testutil.RunGit(t, "-C", source, "commit", "-am", "update")
	testutil.RunGit(t, "-C", source, "push", "origin", "main", "main:feature")
	testutil.WriteFile(t, root, "ordinary/file", "untouched")
	var visited []string
	output, err := PullChildren(context.Background(), root, func(name string, index, total int) {
		if total != 4 || index != len(visited)+1 {
			t.Errorf("progress %s = %d/%d", name, index, total)
		}
		visited = append(visited, name)
	})
	for _, want := range []string{"1 pulled, 0 up to date, 2 skipped, 1 failed", "a-dirty: ERROR", "b-feature: skipped, on feature", "c-local: skipped, no upstream", "z-good: PULLED"} {
		if err != nil || !strings.Contains(output, want) {
			t.Fatalf("PullChildren = (%q, %v), want %q", output, err, want)
		}
	}
	if !slices.Equal(visited, []string{"a-dirty", "b-feature", "c-local", "z-good"}) {
		t.Fatalf("progress = %v", visited)
	}
	for path, want := range map[string]string{filepath.Join(good, "file"): "updated", filepath.Join(conflict, "file"): "local", filepath.Join(feature, "file"): "old"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("read %s = (%q, %v), want %q", path, got, err, want)
		}
	}

	output, err = PullChildren(context.Background(), root, nil)
	if err != nil || !strings.Contains(output, "0 pulled, 1 up to date, 2 skipped, 1 failed") || !strings.Contains(output, "z-good: up to date") {
		t.Fatalf("second PullChildren = (%q, %v)", output, err)
	}
}

func TestPullChildrenSkipsDetachedHead(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "detached")
	testutil.RunGit(t, "init", "--initial-branch=main", repository)
	testutil.WriteFile(t, repository, "file", "x")
	testutil.RunGit(t, "-C", repository, "add", "file")
	testutil.RunGit(t, "-C", repository, "commit", "-m", "initial")
	testutil.RunGit(t, "-C", repository, "switch", "--detach")
	output, err := PullChildren(context.Background(), root, nil)
	if err != nil || !strings.Contains(output, "detached: skipped, detached HEAD") {
		t.Fatalf("PullChildren = (%q, %v)", output, err)
	}
}

func TestPullChildrenReportsUnreadableRootAndCancellation(t *testing.T) {
	if _, err := PullChildren(context.Background(), filepath.Join(t.TempDir(), "missing"), nil); err == nil {
		t.Fatal("missing root succeeded")
	}
	root := t.TempDir()
	testutil.RunGit(t, "init", filepath.Join(root, "child"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PullChildren(ctx, root, nil); err != context.Canceled {
		t.Fatalf("cancelled pull error = %v", err)
	}
}
