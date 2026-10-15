package procinfo

import "golang.org/x/sys/unix"

// processInfo reads a process's parent, process group and controlling
// terminal from the kernel. A process without a terminal has the device -1.
func processInfo(pid int) (process, bool) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info.Proc.P_pid != int32(pid) {
		return process{}, false
	}
	return process{
		parent:        int(info.Eproc.Ppid),
		group:         int(info.Eproc.Pgid),
		terminal:      info.Eproc.Tdev != -1,
		terminalGroup: int(info.Eproc.Tpgid),
	}, true
}
