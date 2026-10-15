package procinfo

import (
	"os"

	"golang.org/x/sys/unix"
)

func selfPID() int { return os.Getpid() }

// processInfo reads a process's parent and process group from the kernel.
func processInfo(pid int) (parent, group int, ok bool) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info.Proc.P_pid != int32(pid) {
		return 0, 0, false
	}
	return int(info.Eproc.Ppid), int(info.Eproc.Pgid), true
}
