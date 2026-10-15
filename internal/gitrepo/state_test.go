package gitrepo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opspresso/romty/internal/testutil"
)

func TestGitStateUsesFetchedUpstream(t *testing.T) {
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	source := filepath.Join(base, "source")
	workspace := filepath.Join(base, "workspace")
	testutil.RunGit(t, "init", "--bare", "--initial-branch=main", remote)
	testutil.RunGit(t, "init", "--initial-branch=main", source)
	testutil.WriteFile(t, source, "status.txt", "one")
	testutil.RunGit(t, "-C", source, "add", "status.txt")
	testutil.RunGit(t, "-C", source, "commit", "-m", "one")
	state, ok := readState(source, false)
	if !ok || state.Branch != "main" || state.Dirty || state.Ahead != 0 || state.Behind != 0 {
		t.Fatalf("repository without upstream state = %#v, %v", state, ok)
	}
	testutil.RunGit(t, "-C", source, "remote", "add", "origin", remote)
	testutil.RunGit(t, "-C", source, "push", "-u", "origin", "main")
	testutil.RunGit(t, "clone", remote, workspace)

	testutil.WriteFile(t, source, "status.txt", "two")
	testutil.RunGit(t, "-C", source, "add", "status.txt")
	testutil.RunGit(t, "-C", source, "commit", "-m", "two")
	testutil.RunGit(t, "-C", source, "push", "origin", "main")

	state, ok = readState(workspace, false)
	if !ok || state.Behind != 0 {
		t.Fatalf("state before fetch = %#v, %v, want stale upstream left unchanged", state, ok)
	}
	state, ok = readState(workspace, true)
	if !ok || state.Branch != "main" || state.Dirty || state.Ahead != 0 || state.Behind != 1 {
		t.Fatalf("state with fetch = %#v, %v, want clean main behind by one", state, ok)
	}
	states := States([]string{workspace, workspace, ""}, false)
	if len(states) != 1 || states[workspace].Behind != 1 {
		t.Fatalf("workspace statuses = %#v, want the fetched repository once", states)
	}

	testutil.WriteFile(t, workspace, "status.txt", "local")
	if err := os.WriteFile(filepath.Join(workspace, "untracked.txt"), []byte("new"), 0o600); err != nil {
		t.Fatalf("write untracked Git fixture: %v", err)
	}
	state, ok = readState(workspace, false)
	if !ok || !state.Dirty {
		t.Fatalf("modified and untracked workspace state = %#v, %v, want dirty", state, ok)
	}
	testutil.RunGit(t, "-C", workspace, "add", "status.txt", "untracked.txt")
	testutil.RunGit(t, "-C", workspace, "commit", "-m", "local")
	state, ok = readState(workspace, false)
	if !ok || state.Dirty || state.Ahead != 1 || state.Behind != 1 {
		t.Fatalf("diverged workspace state = %#v, %v, want clean and one ahead/behind", state, ok)
	}
}

func TestParseGitStateRecognizesDetachedConflict(t *testing.T) {
	state := parseState(strings.Join([]string{
		"# branch.oid 0123456789abcdef",
		"# branch.head (detached)",
		"# branch.ab +2 -3",
		"u UU N... 100644 100644 100644 100644 a b c conflict.txt",
	}, "\n"))
	if !state.Detached || state.Revision != "0123456789abcdef" || !state.Dirty || !state.Conflicted ||
		state.Ahead != 2 || state.Behind != 3 {
		t.Fatalf("detached conflict state = %#v", state)
	}
}

func TestGitStateIgnoresDirectoriesWithoutGitMetadata(t *testing.T) {
	if state, ok := readState(t.TempDir(), true); ok {
		t.Fatalf("non-repository state = %#v, want no state", state)
	}
}
