package daemon

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// awakeCommand blocks idle sleep through logind for as long as it runs. The
// parent-death signal ends it with the daemon, however the daemon stops.
func awakeCommand(int) *exec.Cmd {
	command := exec.Command("systemd-inhibit", "--what=idle:sleep", "--who=romty",
		"--why=An agent is working in romty", "--mode=block", "sleep", "infinity")
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	return command
}

func redirect(from, to int) error {
	return unix.Dup3(from, to, 0)
}

// excludeFromBackup does nothing on Linux, which has no backup exclusion
// every backup tool honours. The documentation says which directory to
// exclude.
var excludeFromBackup = func(string) error {
	return nil
}
