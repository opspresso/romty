// Package procinfo reads the process tree a hook command runs in.
//
// A shared agent server can run a hook on behalf of any terminal, and its
// environment can carry the tab ID of whichever terminal started it. The
// process groups above the hook show where it really runs: a hook started by
// an agent in a terminal tab has that tab's foreground job among them.
package procinfo

// maxDepth bounds the walk up the process tree. A hook sits a few processes
// below its agent; a deeper chain is not one romty needs to follow.
const maxDepth = 16

// AncestorGroups returns the process groups of this process and its
// ancestors, nearest first, without duplicates. It stops at process 1 or at a
// process it cannot read.
func AncestorGroups() []int {
	var groups []int
	seen := make(map[int]bool)
	for pid, depth := selfPID(), 0; pid > 1 && depth < maxDepth; depth++ {
		parent, group, ok := processInfo(pid)
		if !ok {
			break
		}
		if !seen[group] {
			seen[group] = true
			groups = append(groups, group)
		}
		pid = parent
	}
	return groups
}
