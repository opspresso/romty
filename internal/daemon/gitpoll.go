package daemon

import (
	"maps"
	"sync"
	"time"

	"github.com/opspresso/romty/internal/gitrepo"
	"github.com/opspresso/romty/internal/model"
)

// The Git state the daemon reads once for every client. A TUI that read it
// itself ran git status in every workspace every ten seconds; 34 repositories
// took 1.1 s of CPU a round, and each open TUI did it again.
var (
	// gitPollInterval is how often the poller looks at every repository.
	gitPollInterval = 10 * time.Second
	// gitStaleAfter is the longest a repository goes unread. An edit to a
	// tracked file changes no file the signature watches, so a repository
	// whose terminals print nothing is still read this often.
	gitStaleAfter = time.Minute
	// gitFetchInterval is how often remote-tracking refs are fetched.
	gitFetchInterval = 5 * time.Minute
)

const gitWorkers = 4

// gitPoller keeps the last state read from each repository.
type gitPoller struct {
	mu        sync.Mutex
	reads     map[string]gitRead
	fetchedAt time.Time
}

type gitRead struct {
	state     model.GitState
	ok        bool
	signature string
	at        time.Time
}

// poll reads the repositories that may have changed and returns the state of
// every repository. A repository is read when its signature changed, when a
// terminal in it printed since the last read (the cue that an agent or a
// command is working there), when the last read is older than gitStaleAfter,
// or when force is set. fetch fetches every repository first.
func (p *gitPoller) poll(paths []string, printedSince func(path string, since time.Time) bool, force, fetch bool) map[string]model.GitState {
	p.mu.Lock()
	if p.reads == nil {
		p.reads = make(map[string]gitRead)
	}
	previous := maps.Clone(p.reads)
	p.mu.Unlock()

	now := time.Now()
	type job struct {
		path      string
		signature string
	}
	jobs := make([]job, 0, len(paths))
	current := make(map[string]gitRead, len(paths))
	for _, path := range paths {
		signature := gitrepo.Signature(path)
		last, seen := previous[path]
		if !force && !fetch && seen && signature == last.signature &&
			now.Sub(last.at) < gitStaleAfter && !printedSince(path, last.at) {
			current[path] = last
			continue
		}
		jobs = append(jobs, job{path: path, signature: signature})
	}

	var mu sync.Mutex
	var workers sync.WaitGroup
	queue := make(chan job, len(jobs))
	for _, value := range jobs {
		queue <- value
	}
	close(queue)
	for range min(len(jobs), gitWorkers) {
		workers.Go(func() {
			for value := range queue {
				state, ok := gitrepo.Read(value.path, fetch)
				mu.Lock()
				current[value.path] = gitRead{state: state, ok: ok, signature: value.signature, at: time.Now()}
				mu.Unlock()
			}
		})
	}
	workers.Wait()

	p.mu.Lock()
	p.reads = current
	if fetch {
		p.fetchedAt = now
	}
	p.mu.Unlock()
	return statesOf(current)
}

// fetchDue reports whether remote-tracking refs are due a fetch.
func (p *gitPoller) fetchDue() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return time.Since(p.fetchedAt) >= gitFetchInterval
}

// states is the state of every repository as last read.
func (p *gitPoller) states() map[string]model.GitState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return statesOf(p.reads)
}

func statesOf(reads map[string]gitRead) map[string]model.GitState {
	result := make(map[string]model.GitState, len(reads))
	for path, read := range reads {
		if read.ok {
			result[path] = read.state
		}
	}
	return result
}

// workspacePaths lists every workspace directory under the roots, the way
// the snapshot finds them.
func (s *Server) workspacePaths() []string {
	snapshot := s.snapshot()
	paths := make([]string, 0)
	for _, root := range snapshot.Roots {
		for _, directory := range root.Directories {
			paths = append(paths, directory.Workspace.Path)
		}
	}
	return paths
}

// printedSince reports whether a terminal in the workspace at path printed
// after since.
func (s *Server) printedSince(path string, since time.Time) bool {
	s.mu.Lock()
	workspaces := make(map[string]string, len(s.value.Workspaces))
	for _, workspace := range s.value.Workspaces {
		workspaces[workspace.ID] = workspace.Path
	}
	sessions := make([]*session, 0)
	for _, tab := range s.value.Tabs {
		if workspaces[tab.WorkspaceID] == path {
			if value := s.sessions[tab.ID]; value != nil {
				sessions = append(sessions, value)
			}
		}
	}
	s.mu.Unlock()
	for _, value := range sessions {
		if value.lastOutput().After(since) {
			return true
		}
	}
	return false
}
