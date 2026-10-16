package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Action is a Git command the user starts on a workspace. Each one can reach
// the remote, so each runs without a credential prompt.
type Action int

const (
	ActionStatus Action = iota
	ActionFetch
	ActionPull
	ActionPush
)

// actionTimeout bounds one action. A fetch, pull or push over a slow link can
// take a while, but one that takes longer than this is stuck.
const actionTimeout = 2 * time.Minute

// String is the action's name as the user sees it.
func (a Action) String() string {
	switch a {
	case ActionStatus:
		return "Status"
	case ActionFetch:
		return "Fetch"
	case ActionPull:
		return "Pull"
	case ActionPush:
		return "Push"
	default:
		return "Git"
	}
}

// arguments is the git command line of an action. Pull is fast-forward only,
// so it never creates a merge commit.
func (a Action) arguments() ([]string, error) {
	switch a {
	case ActionStatus:
		return []string{"status", "--short", "--branch"}, nil
	case ActionFetch:
		return []string{"fetch"}, nil
	case ActionPull:
		return []string{"pull", "--ff-only"}, nil
	case ActionPush:
		return []string{"push"}, nil
	default:
		return nil, fmt.Errorf("unknown Git action %d", a)
	}
}

// Run runs action in the repository at path and returns its combined output
// without surrounding white space. Cancelling ctx stops the command.
func Run(ctx context.Context, path string, action Action) (string, error) {
	arguments, err := action.arguments()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	output, err := combinedOutput(ctx, path, remoteEnvironment, arguments...)
	value := strings.TrimSpace(string(output))
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return value, fmt.Errorf("%s timed out after %s", action, actionTimeout)
	}
	return value, err
}
