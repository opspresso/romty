// Package gitrepo runs the git commands romty needs: the branch and status
// line of each workspace, the changed files and their diffs, and the actions
// the user starts. Every command is bounded in time and in output, and none
// of them waits on a credential prompt.
package gitrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	statusTimeout = 2 * time.Second
	fetchTimeout  = 10 * time.Second
	statusWorkers = 4
)

// readEnvironment and remoteEnvironment are what romty adds to the
// environment of the git it runs. A read must not take the index lock a user's
// own git may be holding, and anything that reaches a remote must not stop on a
// credential prompt: it would be waiting on a terminal romty never shows.
var (
	readEnvironment   = []string{"GIT_OPTIONAL_LOCKS=0"}
	remoteEnvironment = []string{"GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=Never"}
)

// command builds a git invocation inside a repository, bounded by ctx.
func command(ctx context.Context, path string, environment []string, arguments ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", path}, arguments...)...)
	cmd.Env = append(os.Environ(), environment...)
	return cmd
}

// State is the branch line romty shows for a repository.
type State struct {
	Branch     string
	Revision   string
	Detached   bool
	Dirty      bool
	Conflicted bool
	Ahead      int
	Behind     int
}

func readState(path string, fetch bool) (State, bool) {
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		return State{}, false
	}
	if fetch {
		fetchRemote(path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
	defer cancel()
	output, err := stdout(ctx, path, readEnvironment,
		"status", "--porcelain=v2", "--branch", "--untracked-files=normal")
	if err != nil {
		return State{}, false
	}
	return parseState(string(output)), true
}

func parseState(output string) State {
	var state State
	for line := range strings.SplitSeq(output, "\n") {
		switch {
		case strings.HasPrefix(line, "# branch.oid "):
			state.Revision = strings.TrimPrefix(line, "# branch.oid ")
		case strings.HasPrefix(line, "# branch.head "):
			state.Branch = strings.TrimPrefix(line, "# branch.head ")
			if state.Branch == "(detached)" {
				state.Branch = ""
				state.Detached = true
			}
		case strings.HasPrefix(line, "# branch.ab "):
			fields := strings.Fields(strings.TrimPrefix(line, "# branch.ab "))
			if len(fields) == 2 {
				state.Ahead, _ = strconv.Atoi(strings.TrimPrefix(fields[0], "+"))
				state.Behind, _ = strconv.Atoi(strings.TrimPrefix(fields[1], "-"))
			}
		case strings.HasPrefix(line, "u "):
			state.Dirty = true
			state.Conflicted = true
		case line != "" && !strings.HasPrefix(line, "# "):
			state.Dirty = true
		}
	}
	return state
}

func fetchRemote(path string) {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	_ = command(ctx, path, remoteEnvironment,
		"fetch", "--quiet", "--no-tags", "--no-write-fetch-head").Run()
}

// States reads the state of every repository among paths, a few at a time.
// A path that is not a repository, or whose status cannot be read, is left
// out. With fetch set, each repository fetches its remote first.
func States(paths []string, fetch bool) map[string]State {
	unique := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if path != "" {
			unique[path] = struct{}{}
		}
	}
	jobs := make(chan string, len(unique))
	for path := range unique {
		jobs <- path
	}
	close(jobs)

	result := make(map[string]State)
	var mu sync.Mutex
	var workers sync.WaitGroup
	for range min(len(unique), statusWorkers) {
		workers.Go(func() {
			for path := range jobs {
				state, ok := readState(path, fetch)
				if ok {
					mu.Lock()
					result[path] = state
					mu.Unlock()
				}
			}
		})
	}
	workers.Wait()
	return result
}
