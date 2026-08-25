package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitStateUsesFetchedUpstream(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	source := filepath.Join(base, "source")
	workspace := filepath.Join(base, "workspace")
	runGit(t, "init", "--bare", "--initial-branch=main", remote)
	runGit(t, "init", "--initial-branch=main", source)
	writeGitFile(t, source, "one")
	runGit(t, "-C", source, "add", "status.txt")
	runGit(t, "-C", source, "-c", "user.name=romty", "-c", "user.email=romty@example.com", "commit", "-m", "one")
	state, ok := readGitState(source, false)
	if !ok || state.Branch != "main" || state.Dirty || state.Ahead != 0 || state.Behind != 0 {
		t.Fatalf("repository without upstream state = %#v, %v", state, ok)
	}
	runGit(t, "-C", source, "remote", "add", "origin", remote)
	runGit(t, "-C", source, "push", "-u", "origin", "main")
	runGit(t, "clone", remote, workspace)

	writeGitFile(t, source, "two")
	runGit(t, "-C", source, "add", "status.txt")
	runGit(t, "-C", source, "-c", "user.name=romty", "-c", "user.email=romty@example.com", "commit", "-m", "two")
	runGit(t, "-C", source, "push", "origin", "main")

	state, ok = readGitState(workspace, false)
	if !ok || state.Behind != 0 {
		t.Fatalf("state before fetch = %#v, %v, want stale upstream left unchanged", state, ok)
	}
	state, ok = readGitState(workspace, true)
	if !ok || state.Branch != "main" || state.Dirty || state.Ahead != 0 || state.Behind != 1 {
		t.Fatalf("state with fetch = %#v, %v, want clean main behind by one", state, ok)
	}
	states := gitStates([]string{workspace, workspace, ""}, false)
	if len(states) != 1 || states[workspace].Behind != 1 {
		t.Fatalf("workspace statuses = %#v, want the fetched repository once", states)
	}

	writeGitFile(t, workspace, "local")
	if err := os.WriteFile(filepath.Join(workspace, "untracked.txt"), []byte("new"), 0o600); err != nil {
		t.Fatalf("write untracked Git fixture: %v", err)
	}
	state, ok = readGitState(workspace, false)
	if !ok || !state.Dirty {
		t.Fatalf("modified and untracked workspace state = %#v, %v, want dirty", state, ok)
	}
	runGit(t, "-C", workspace, "add", "status.txt", "untracked.txt")
	runGit(t, "-C", workspace, "-c", "user.name=romty", "-c", "user.email=romty@example.com", "commit", "-m", "local")
	state, ok = readGitState(workspace, false)
	if !ok || state.Dirty || state.Ahead != 1 || state.Behind != 1 {
		t.Fatalf("diverged workspace state = %#v, %v, want clean and one ahead/behind", state, ok)
	}
}

func TestParseGitStateRecognizesDetachedConflict(t *testing.T) {
	state := parseGitState(strings.Join([]string{
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
	if state, ok := readGitState(t.TempDir(), true); ok {
		t.Fatalf("non-repository state = %#v, want no state", state)
	}
}

func runGit(t *testing.T, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}

func writeGitFile(t *testing.T, directory, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, "status.txt"), []byte(value), 0o600); err != nil {
		t.Fatalf("write Git fixture: %v", err)
	}
}
