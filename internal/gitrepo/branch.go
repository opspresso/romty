package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
)

// Branch is a branch the user can switch a workspace to.
type Branch struct {
	// Name is the short name: "main" for a local branch, "origin/main" for
	// a remote-tracking one.
	Name string
	// Remote marks a remote-tracking branch with no local branch of the
	// same name. Switching to it creates that local branch.
	Remote  bool
	Current bool
}

// Branches lists the current branch, the other local branches most recently
// committed first, then the remote-tracking branches that have no local
// branch of the same name.
// The remote's own HEAD pointer is left out: it is an alias, not a branch.
// Names are the ref with its refs/heads/ or refs/remotes/ prefix removed,
// not git's shortest unambiguous name, which turns a branch that shares its
// name with a tag into heads/<name>, a name git switch does not accept.
func Branches(ctx context.Context, path string) ([]Branch, error) {
	output, err := stdout(ctx, path, nil, "for-each-ref", "--sort=-committerdate",
		"--format=%(HEAD)%00%(refname)%00%(refname:lstrip=2)%00%(symref)", "refs/heads", "refs/remotes")
	if err != nil {
		return nil, fmt.Errorf("list branches: %w", err)
	}
	var local, remote []Branch
	localNames := make(map[string]bool)
	for line := range bytes.SplitSeq(bytes.TrimSpace(output), []byte("\n")) {
		fields := strings.Split(string(line), "\x00")
		if len(fields) != 4 || fields[3] != "" {
			continue
		}
		branch := Branch{Name: fields[2], Current: fields[0] == "*"}
		if strings.HasPrefix(fields[1], "refs/remotes/") {
			branch.Remote = true
			remote = append(remote, branch)
			continue
		}
		localNames[branch.Name] = true
		if branch.Current {
			local = append([]Branch{branch}, local...)
			continue
		}
		local = append(local, branch)
	}
	for _, branch := range remote {
		_, name, found := strings.Cut(branch.Name, "/")
		if found && !localNames[name] {
			local = append(local, branch)
		}
	}
	return local, nil
}

// SwitchRequest is what to switch a workspace to.
type SwitchRequest struct {
	Branch string
	// Remote tracks the remote-tracking branch Branch in a new local branch.
	Remote bool
	// Create makes Branch a new branch at the current commit.
	Create bool
}

// errBranchName rejects a name git would read as an option.
var errBranchName = errors.New("branch name must not be empty or start with -")

// Switch runs git switch in the repository at path. It refuses, as git does,
// when local changes would be overwritten, so no work is lost; the output
// says why.
func Switch(ctx context.Context, path string, request SwitchRequest) (string, error) {
	name := strings.TrimSpace(request.Branch)
	if name == "" || strings.HasPrefix(name, "-") {
		return "", errBranchName
	}
	arguments := []string{"switch", name}
	switch {
	case request.Create:
		if output, err := combinedOutput(ctx, path, nil, "check-ref-format", "--branch", name); err != nil {
			return strings.TrimSpace(string(output)), fmt.Errorf("%q is not a valid branch name", name)
		}
		arguments = []string{"switch", "-c", name}
	case request.Remote:
		arguments = []string{"switch", "--track", name}
	}
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	output, err := combinedOutput(ctx, path, remoteEnvironment, arguments...)
	value := strings.TrimSpace(string(output))
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return value, fmt.Errorf("switch timed out after %s", actionTimeout)
	}
	return value, err
}
