package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// pullChildrenBranch is the only branch PullChildren updates. A child on any
// other branch is in the middle of its own work and is left alone.
const pullChildrenBranch = "main"

// PullProgress reports the child repository PullChildren is about to handle:
// its directory name, its 1-based position, and how many repositories there are.
type PullProgress func(name string, index, total int)

// PullChildren pulls the immediate workspace directories of root that are on
// main and behind their upstream after a fetch. A .git file also identifies a
// linked worktree. Each failure is reported without stopping the remaining
// repositories; Git protects changes that would be overwritten. progress, when
// set, is called before each repository is handled.
func PullChildren(ctx context.Context, root string, progress PullProgress) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", fmt.Errorf("read root: %w", err)
	}
	var result strings.Builder
	pulled, current, skipped, failed := 0, 0, 0, 0
	repositories := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, entry.Name(), ".git")); err != nil {
			if !os.IsNotExist(err) {
				failed++
				fmt.Fprintf(&result, "\n%s: ERROR inspecting .git: %v\n", entry.Name(), err)
			}
			continue
		}
		repositories = append(repositories, entry.Name())
	}
	for index, name := range repositories {
		if err := ctx.Err(); err != nil {
			return result.String(), err
		}
		if progress != nil {
			progress(name, index+1, len(repositories))
		}
		outcome := pullChild(ctx, filepath.Join(root, name))
		if ctx.Err() != nil {
			return result.String(), ctx.Err()
		}
		switch outcome.status {
		case pullPulled:
			pulled++
			fmt.Fprintf(&result, "\n%s: PULLED\n", name)
		case pullCurrent:
			current++
			fmt.Fprintf(&result, "%s: up to date\n", name)
		case pullSkipped:
			skipped++
			fmt.Fprintf(&result, "%s: skipped, %s\n", name, outcome.reason)
		default:
			failed++
			fmt.Fprintf(&result, "\n%s: ERROR\n", name)
		}
		output := outcome.output
		// Keep a large diffstat from hiding results for later repositories.
		const maxRepositoryOutput = 16 << 10
		if len(output) > maxRepositoryOutput {
			if outcome.err != nil {
				output = "[output truncated]\n" + output[len(output)-maxRepositoryOutput:]
			} else {
				output = output[:maxRepositoryOutput] + "\n[output truncated]"
			}
		}
		if output != "" {
			fmt.Fprintln(&result, output)
		}
		if outcome.err != nil {
			fmt.Fprintf(&result, "Error: %v\n", outcome.err)
		}
	}
	return fmt.Sprintf("%d pulled, %d up to date, %d skipped, %d failed.\n%s",
		pulled, current, skipped, failed, result.String()), nil
}

type pullStatus int

const (
	pullFailed pullStatus = iota
	pullPulled
	pullCurrent
	pullSkipped
)

type pullOutcome struct {
	status pullStatus
	reason string
	output string
	err    error
}

// pullChild pulls one repository when it is on main and its upstream has
// commits it lacks. Only the fetch and the pull reach the remote.
func pullChild(ctx context.Context, path string) pullOutcome {
	branch, found, err := readReference(ctx, path, "symbolic-ref", "--quiet", "--short", "HEAD")
	switch {
	case err != nil:
		return pullOutcome{err: fmt.Errorf("read branch: %w", err)}
	case !found:
		return pullOutcome{status: pullSkipped, reason: "detached HEAD"}
	case branch != pullChildrenBranch:
		return pullOutcome{status: pullSkipped, reason: "on " + branch}
	}
	if _, found, err := readReference(ctx, path, "rev-parse", "--verify", "--quiet", "@{upstream}"); err != nil {
		return pullOutcome{err: fmt.Errorf("read upstream: %w", err)}
	} else if !found {
		return pullOutcome{status: pullSkipped, reason: "no upstream"}
	}
	if output, err := Run(ctx, path, ActionFetch); err != nil {
		return pullOutcome{output: output, err: err}
	}
	count, _, err := readReference(ctx, path, "rev-list", "--count", "HEAD..@{upstream}")
	if err == nil {
		var behind int
		if behind, err = strconv.Atoi(count); err == nil && behind == 0 {
			return pullOutcome{status: pullCurrent}
		}
	}
	if err != nil {
		return pullOutcome{err: fmt.Errorf("count upstream commits: %w", err)}
	}
	output, err := Run(ctx, path, ActionPull)
	if err != nil {
		return pullOutcome{output: output, err: err}
	}
	return pullOutcome{status: pullPulled, output: output}
}

// readReference runs a local git query. found is false when git exits with a
// status, which these queries use to say the reference does not exist; err is
// any other failure, such as a timeout.
func readReference(ctx context.Context, path string, arguments ...string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	output, err := stdout(ctx, path, readEnvironment, arguments...)
	var exit *exec.ExitError
	if errors.As(err, &exit) && ctx.Err() == nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(string(output)), true, nil
}
