package procinfo

import (
	"os"
	"reflect"
	"slices"
	"syscall"
	"testing"
)

func TestCurrentStartsWithThisProcessGroup(t *testing.T) {
	groups := Current().Groups
	own, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatalf("Getpgid() error = %v", err)
	}
	if len(groups) == 0 || groups[0] != own {
		t.Fatalf("Current().Groups = %v, want this process group %d first", groups, own)
	}
	parent, err := syscall.Getpgid(os.Getppid())
	if err != nil {
		t.Fatalf("Getpgid(parent) error = %v", err)
	}
	if !slices.Contains(groups, parent) {
		t.Fatalf("Current().Groups = %v, want the parent group %d", groups, parent)
	}
}

func TestProcessInfoRejectsAMissingProcess(t *testing.T) {
	if _, ok := processInfo(1 << 30); ok {
		t.Fatal("processInfo() read a process that does not exist")
	}
}

// tree is a process table keyed by process ID.
type tree map[int]process

func (t tree) read(pid int) (process, bool) {
	value, ok := t[pid]
	return value, ok
}

func TestAncestryFindsTheForegroundJobBehindATerminalWrapper(t *testing.T) {
	// Kiro CLI V3 under kiro-cli-term: the hook and the agent server have no
	// terminal; kiro-cli is the foreground job of the nested terminal (tpgid
	// 300), whose shell runs in the foreground job of the tab (tpgid 500).
	processes := tree{
		10: {parent: 20, group: 10},
		20: {parent: 30, group: 20},
		30: {parent: 40, group: 300, terminal: true, terminalGroup: 300},
		40: {parent: 50, group: 400, terminal: true, terminalGroup: 300},
		50: {parent: 1, group: 500, terminal: true, terminalGroup: 500},
	}
	got := ancestry(10, processes.read)
	if want := (Ancestry{Groups: []int{10, 20, 300, 400, 500}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("ancestry() = %+v, want %+v", got, want)
	}
}

func TestAncestryReportsABackgroundJob(t *testing.T) {
	// `kiro-cli &` in the same nested shell: the shell (group 400) keeps
	// the terminal, so the agent's group 600 is not its foreground.
	processes := tree{
		10: {parent: 60, group: 10},
		60: {parent: 40, group: 600, terminal: true, terminalGroup: 400},
		40: {parent: 50, group: 400, terminal: true, terminalGroup: 400},
		50: {parent: 1, group: 500, terminal: true, terminalGroup: 500},
	}
	if got := ancestry(10, processes.read); !got.BackgroundJob {
		t.Fatalf("ancestry() = %+v, want a background job", got)
	}
}

func TestAncestryWithoutATerminalIsNotABackgroundJob(t *testing.T) {
	processes := tree{10: {parent: 20, group: 10}, 20: {parent: 1, group: 20}}
	if got := ancestry(10, processes.read); got.BackgroundJob || !reflect.DeepEqual(got.Groups, []int{10, 20}) {
		t.Fatalf("ancestry() = %+v, want groups [10 20] and no background job", got)
	}
}
