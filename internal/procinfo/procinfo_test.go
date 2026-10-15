package procinfo

import (
	"os"
	"slices"
	"syscall"
	"testing"
)

func TestAncestorGroupsStartWithThisProcessGroup(t *testing.T) {
	groups := AncestorGroups()
	own, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatalf("Getpgid() error = %v", err)
	}
	if len(groups) == 0 || groups[0] != own {
		t.Fatalf("AncestorGroups() = %v, want this process group %d first", groups, own)
	}
	parent, err := syscall.Getpgid(os.Getppid())
	if err != nil {
		t.Fatalf("Getpgid(parent) error = %v", err)
	}
	if !slices.Contains(groups, parent) {
		t.Fatalf("AncestorGroups() = %v, want the parent group %d", groups, parent)
	}
}

func TestProcessInfoRejectsAMissingProcess(t *testing.T) {
	if _, _, ok := processInfo(1 << 30); ok {
		t.Fatal("processInfo() read a process that does not exist")
	}
}
