package gitrepo

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/opspresso/romty/internal/testutil"
)

func TestActionArguments(t *testing.T) {
	probes := []struct {
		action Action
		want   []string
	}{
		{action: ActionStatus, want: []string{"status", "--short", "--branch"}},
		{action: ActionFetch, want: []string{"fetch"}},
		{action: ActionPull, want: []string{"pull", "--ff-only"}},
		{action: ActionPush, want: []string{"push"}},
	}
	for _, probe := range probes {
		arguments, err := probe.action.arguments()
		if err != nil || !reflect.DeepEqual(arguments, probe.want) {
			t.Fatalf("%s arguments = (%v, %v), want %v", probe.action, arguments, err, probe.want)
		}
	}
	if _, err := Action(99).arguments(); err == nil {
		t.Fatal("unknown Git action did not fail")
	}
}

func TestRunStatusAction(t *testing.T) {
	repository := t.TempDir()
	testutil.RunGit(t, "init", "--initial-branch=main", repository)
	testutil.WriteFile(t, repository, "untracked.txt", "new")

	output, err := Run(context.Background(), repository, ActionStatus)
	if err != nil || !strings.Contains(output, "## No commits yet on main") || !strings.Contains(output, "?? untracked.txt") {
		t.Fatalf("Run(status) = (%q, %v), want branch and untracked file", output, err)
	}
}
