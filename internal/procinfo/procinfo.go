// Package procinfo reads the process tree a hook command runs in.
//
// A shared agent server can run a hook on behalf of any terminal, and its
// environment can carry the tab ID of whichever terminal started it. The
// process groups above the hook show where it really runs: a hook started by
// an agent in a terminal tab has that tab's foreground job among them.
package procinfo

import "os"

// maxDepth bounds the walk up the process tree. A hook sits a few processes
// below its agent; a deeper chain is not one romty needs to follow.
const maxDepth = 16

// process is what the kernel reports about one process.
type process struct {
	parent, group int
	// terminal reports a controlling terminal, and terminalGroup is that
	// terminal's foreground process group.
	terminal      bool
	terminalGroup int
}

// Ancestry is where a hook runs, read from the process tree above it.
type Ancestry struct {
	// Groups are the process groups of this process and its ancestors,
	// nearest first, without duplicates.
	Groups []int
	// BackgroundJob reports that the nearest of these processes with a
	// controlling terminal is not in that terminal's foreground group: the
	// hook runs for a job the shell put in the background, such as
	// `kiro-cli &`. A terminal wrapper such as kiro-cli-term makes the tab's
	// foreground job an ancestor of every process in the tab, so the groups
	// alone cannot tell such a job from the one the user sees.
	BackgroundJob bool
}

// Current reads the ancestry of this process. The walk stops at process 1 or
// at a process it cannot read.
func Current() Ancestry {
	return ancestry(os.Getpid(), processInfo)
}

func ancestry(pid int, read func(int) (process, bool)) Ancestry {
	var result Ancestry
	seen := make(map[int]bool)
	checked := false
	for depth := 0; pid > 1 && depth < maxDepth; depth++ {
		value, ok := read(pid)
		if !ok {
			break
		}
		if !seen[value.group] {
			seen[value.group] = true
			result.Groups = append(result.Groups, value.group)
		}
		if !checked && value.terminal {
			checked = true
			result.BackgroundJob = value.group != value.terminalGroup
		}
		pid = value.parent
	}
	return result
}
