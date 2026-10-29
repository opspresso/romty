package daemon

import (
	"errors"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// terminateGrace is how long the processes of a closed tab have to exit on
// SIGHUP before they are killed. An agent uses it to write its session; a
// program that ignores SIGHUP, such as a job started with nohup, is killed at
// the end of it. It is a variable so tests need not wait it out.
var terminateGrace = 2 * time.Second

// terminateSession ends every process in the terminal session the shell
// leads, not only the shell. Closing the PTY hangs up the foreground job, but
// a job the shell put in the background has its own process group and the
// hangup does not reach it, so it would outlive the tab with its ports and
// CPU. Each process gets SIGHUP, the signal a closed terminal sends; what is
// still running after terminateGrace gets SIGKILL.
//
// reaped reports that the daemon already reaped the shell. Its PID may then
// belong to an unrelated process, so only the other members are signalled:
// the kernel does not reuse a PID while it is still a session ID, so a
// process whose session is the shell's is one of the tab's.
//
// It returns how many processes had to be killed.
func terminateSession(leader int, reaped bool) int {
	members := sessionMembers(leader, !reaped)
	for _, pid := range members {
		_ = unix.Kill(pid, syscall.SIGHUP)
	}
	deadline := time.Now().Add(terminateGrace)
	for time.Now().Before(deadline) {
		members = alive(members)
		if len(members) == 0 {
			return 0
		}
		time.Sleep(25 * time.Millisecond)
	}
	members = alive(members)
	for _, pid := range members {
		_ = unix.Kill(pid, syscall.SIGKILL)
	}
	return len(members)
}

// sessionMembers lists the processes whose session the shell leads, the shell
// included. The process list may be unreadable; the shell is then the only
// member known, and it is still ended.
func sessionMembers(leader int, includeLeader bool) []int {
	members := make([]int, 0, 4)
	if includeLeader {
		members = append(members, leader)
	}
	output, err := listProcesses()
	if err != nil {
		return members
	}
	for _, value := range parseProcesses(output) {
		if value.pid == leader || value.pid <= 1 {
			continue
		}
		if sid, err := unix.Getsid(value.pid); err == nil && sid == leader {
			members = append(members, value.pid)
		}
	}
	return members
}

// alive keeps the processes that still exist. A zombie still exists until its
// parent reaps it, so the shell, which the daemon reaps, can count as alive
// for a moment after it exited; the grace period covers that.
func alive(pids []int) []int {
	result := pids[:0:0]
	for _, pid := range pids {
		if err := unix.Kill(pid, 0); err == nil || errors.Is(err, syscall.EPERM) {
			result = append(result, pid)
		}
	}
	return result
}

// closeSessionsTogether closes sessions in parallel, so the grace periods of
// many tabs overlap rather than add up when the daemon stops.
func closeSessionsTogether(sessions []*session) {
	var group sync.WaitGroup
	for _, value := range sessions {
		group.Go(value.close)
	}
	group.Wait()
}
