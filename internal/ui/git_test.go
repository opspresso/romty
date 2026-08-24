package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitBehindUsesFetchedUpstream(t *testing.T) {
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
	if got := gitBehind(source); got != 0 {
		t.Fatalf("repository without upstream behind = %d, want 0", got)
	}
	runGit(t, "-C", source, "remote", "add", "origin", remote)
	runGit(t, "-C", source, "push", "-u", "origin", "main")
	runGit(t, "clone", remote, workspace)

	writeGitFile(t, source, "two")
	runGit(t, "-C", source, "add", "status.txt")
	runGit(t, "-C", source, "-c", "user.name=romty", "-c", "user.email=romty@example.com", "commit", "-m", "two")
	runGit(t, "-C", source, "push", "origin", "main")

	if got := gitBehind(workspace); got != 0 {
		t.Fatalf("behind before fetch = %d, want the stale local upstream left unchanged", got)
	}
	runGit(t, "-C", workspace, "fetch")
	if got := gitBehind(workspace); got != 1 {
		t.Fatalf("behind after fetch = %d, want 1", got)
	}
	statuses := gitBehindWorkspaces([]string{workspace, workspace, ""})
	if len(statuses) != 1 || statuses[workspace] != 1 {
		t.Fatalf("workspace statuses = %#v, want the fetched repository once", statuses)
	}
	runGit(t, "-C", workspace, "pull", "--ff-only")
	if got := gitBehind(workspace); got != 0 {
		t.Fatalf("behind after pull = %d, want 0", got)
	}
}

func TestGitBehindIgnoresDirectoriesWithoutGitMetadata(t *testing.T) {
	if got := gitBehind(t.TempDir()); got != 0 {
		t.Fatalf("non-repository behind = %d, want 0", got)
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
