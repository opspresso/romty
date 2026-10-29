package daemon

import (
	"errors"
	"os"
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
// The shell is signalled through its os.Process, which knows when the daemon
// has reaped it and, on Linux, holds a pidfd: a shell that already exited is
// never confused with a later process given its PID. The other members are
// found by their session ID, which the kernel does not hand to a new process
// while any process still has it, and each is checked again before SIGKILL.
// A process the daemon may not signal, such as one run with sudo, cannot be
// ended from here and is not waited for.
//
// processes is a process list to use, or nil to read one now.
func terminateSession(shell *os.Process, processes []process) {
	leader := shell.Pid
	members := sessionMembers(leader, processes)
	_ = shell.Signal(syscall.SIGHUP)
	members = signalMembers(members, leader, syscall.SIGHUP)
	deadline := time.Now().Add(terminateGrace)
	for time.Now().Before(deadline) {
		members = signalMembers(members, leader, 0)
		if len(members) == 0 && shell.Signal(syscall.Signal(0)) != nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = shell.Signal(syscall.SIGKILL)
	signalMembers(members, leader, syscall.SIGKILL)
}

// sessionMembers lists the processes, other than the shell, whose session the
// shell leads. An unreadable process list leaves the shell as the only
// process ended.
func sessionMembers(leader int, processes []process) []int {
	if processes == nil {
		output, err := listProcesses()
		if err != nil {
			return nil
		}
		processes = parseProcesses(output)
	}
	members := make([]int, 0, 4)
	for _, value := range processes {
		if value.pid != leader && value.pid > 1 && inSession(value.pid, leader) {
			members = append(members, value.pid)
		}
	}
	return members
}

func inSession(pid, leader int) bool {
	sid, err := unix.Getsid(pid)
	return err == nil && sid == leader
}

// signalMembers sends signal to the members still in the session and returns
// them. Signal 0 only checks. A member that left, or that the daemon may not
// signal, is dropped.
func signalMembers(members []int, leader int, signal syscall.Signal) []int {
	result := members[:0:0]
	for _, pid := range members {
		if !inSession(pid, leader) {
			continue
		}
		if err := unix.Kill(pid, signal); err == nil {
			result = append(result, pid)
		} else if !errors.Is(err, syscall.ESRCH) && !errors.Is(err, syscall.EPERM) {
			result = append(result, pid)
		}
	}
	return result
}

// closeSessionsTogether closes sessions in parallel, so the grace periods of
// many tabs overlap rather than add up when the daemon stops. One process
// list serves every tab.
func closeSessionsTogether(sessions []*session) {
	var processes []process
	if output, err := listProcesses(); err == nil {
		processes = parseProcesses(output)
	}
	var group sync.WaitGroup
	for _, value := range sessions {
		group.Go(func() { value.closeWith(processes) })
	}
	group.Wait()
}
