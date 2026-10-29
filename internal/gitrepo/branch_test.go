package gitrepo

import (
	"context"
	"strings"
	"testing"

	"github.com/opspresso/romty/internal/testutil"
)

// branchRepository has main, feature, and a clone whose origin carries main,
// feature, and remote-only.
func branchRepository(t *testing.T) string {
	t.Helper()
	origin := t.TempDir()
	testutil.RunGit(t, "init", "--initial-branch=main", origin)
	testutil.WriteFile(t, origin, "a.txt", "a\n")
	testutil.RunGit(t, "-C", origin, "add", ".")
	testutil.RunGit(t, "-C", origin, "commit", "-m", "initial")
	testutil.RunGit(t, "-C", origin, "branch", "feature")
	testutil.RunGit(t, "-C", origin, "branch", "remote-only")
	clone := t.TempDir()
	testutil.RunGit(t, "clone", "--quiet", origin, clone)
	testutil.RunGit(t, "-C", clone, "branch", "feature", "origin/feature")
	return clone
}

func TestBranchesListsLocalThenRemoteOnly(t *testing.T) {
	repository := branchRepository(t)
	branches, err := Branches(context.Background(), repository)
	if err != nil {
		t.Fatalf("Branches() error = %v", err)
	}
	names := make([]string, 0, len(branches))
	current := ""
	for _, branch := range branches {
		names = append(names, branch.Name)
		if branch.Current {
			current = branch.Name
		}
	}
	joined := strings.Join(names, ",")
	if current != "main" || !branches[0].Current {
		t.Fatalf("current branch = %q first=%v, want main listed first (%s)", current, branches[0].Current, joined)
	}
	// origin/main and origin/feature have local branches; origin/HEAD is an
	// alias. Only origin/remote-only is offered from the remote.
	if strings.Contains(joined, "origin/main") || strings.Contains(joined, "origin/feature") || strings.Contains(joined, "HEAD") {
		t.Fatalf("branches = %s, want remote branches with a local twin and HEAD left out", joined)
	}
	last := branches[len(branches)-1]
	if last.Name != "origin/remote-only" || !last.Remote {
		t.Fatalf("last branch = %+v, want the remote-only branch after the local ones", last)
	}
}

func TestSwitchLocalRemoteAndNewBranches(t *testing.T) {
	repository := branchRepository(t)
	ctx := context.Background()
	for _, request := range []SwitchRequest{
		{Branch: "feature"},
		{Branch: "origin/remote-only", Remote: true},
		{Branch: "topic/new", Create: true},
	} {
		if output, err := Switch(ctx, repository, request); err != nil {
			t.Fatalf("Switch(%+v) = (%q, %v)", request, output, err)
		}
	}
	branches, err := Branches(ctx, repository)
	if err != nil {
		t.Fatalf("Branches() error = %v", err)
	}
	for _, branch := range branches {
		if branch.Current && branch.Name != "topic/new" {
			t.Fatalf("current branch = %q, want topic/new", branch.Name)
		}
		if branch.Name == "origin/remote-only" {
			t.Fatal("tracking the remote branch left it listed as remote-only")
		}
	}
}

func TestSwitchRefusesOptionsAndInvalidNames(t *testing.T) {
	repository := branchRepository(t)
	for _, request := range []SwitchRequest{
		{Branch: ""},
		{Branch: "--orphan"},
		{Branch: "bad..name", Create: true},
	} {
		if _, err := Switch(context.Background(), repository, request); err == nil {
			t.Fatalf("Switch(%+v) succeeded, want it refused", request)
		}
	}
}

// git refuses a switch that would overwrite local changes, and the output
// says why, so no work is lost.
func TestSwitchKeepsConflictingLocalChanges(t *testing.T) {
	repository := branchRepository(t)
	testutil.RunGit(t, "-C", repository, "switch", "feature")
	testutil.WriteFile(t, repository, "a.txt", "feature\n")
	testutil.RunGit(t, "-C", repository, "commit", "-am", "feature change")
	testutil.RunGit(t, "-C", repository, "switch", "main")
	testutil.WriteFile(t, repository, "a.txt", "local edit\n")

	output, err := Switch(context.Background(), repository, SwitchRequest{Branch: "feature"})
	if err == nil || !strings.Contains(output, "would be overwritten") {
		t.Fatalf("Switch() = (%q, %v), want git's refusal", output, err)
	}
}
