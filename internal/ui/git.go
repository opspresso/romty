package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
)

const (
	gitCommandTimeout = 2 * time.Second
	gitFetchTimeout   = 10 * time.Second
	gitStatusWorkers  = 4
)

type gitState struct {
	Branch     string
	Revision   string
	Detached   bool
	Dirty      bool
	Conflicted bool
	Ahead      int
	Behind     int
}

func readGitState(path string, fetch bool) (gitState, bool) {
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		return gitState{}, false
	}
	if fetch {
		fetchGit(path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitCommandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "git", "-C", path, "status", "--porcelain=v2", "--branch", "--untracked-files=normal")
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	output, err := command.Output()
	if err != nil {
		return gitState{}, false
	}
	return parseGitState(string(output)), true
}

func parseGitState(output string) gitState {
	var state gitState
	for _, line := range strings.Split(output, "\n") {
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

func fetchGit(path string) {
	ctx, cancel := context.WithTimeout(context.Background(), gitFetchTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "git", "-C", path, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head")
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=Never")
	_ = command.Run()
}

func gitStates(paths []string, fetch bool) map[string]gitState {
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

	result := make(map[string]gitState)
	var mu sync.Mutex
	var workers sync.WaitGroup
	for range min(len(unique), gitStatusWorkers) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for path := range jobs {
				state, ok := readGitState(path, fetch)
				if ok {
					mu.Lock()
					result[path] = state
					mu.Unlock()
				}
			}
		}()
	}
	workers.Wait()
	return result
}

func (m dashboard) readGitStatus(forceFetch, reschedule bool) tea.Cmd {
	paths := m.workspacePaths()
	fetch := forceFetch || m.gitFetchedAt.IsZero() || now().Sub(m.gitFetchedAt) >= gitFetchInterval
	fetchedAt := time.Time{}
	if fetch {
		fetchedAt = now()
	}
	return func() tea.Msg {
		return gitStatusMsg{value: gitStates(paths, fetch), fetchedAt: fetchedAt, reschedule: reschedule}
	}
}

func (m dashboard) initialGitStatus() tea.Cmd {
	return tea.Batch(m.readGitStatus(false, true), m.readGitStatus(true, false))
}

func (m dashboard) refreshGitStatus() tea.Cmd {
	read := m.readGitStatus(false, true)
	return tea.Tick(gitRefreshInterval, func(time.Time) tea.Msg {
		return read()
	})
}

func (m dashboard) workspacePaths() []string {
	paths := make([]string, 0)
	for _, root := range m.state.Roots {
		for _, directory := range root.Directories {
			paths = append(paths, directory.Workspace.Path)
		}
	}
	return paths
}
