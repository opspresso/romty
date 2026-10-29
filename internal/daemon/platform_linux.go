package daemon

import (
	"net"
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

// peerUID is the user ID of the process at the other end of a unix socket.
func peerUID(connection *net.UnixConn) (int, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return -1, err
	}
	uid, credErr := -1, error(nil)
	if err := raw.Control(func(fd uintptr) {
		var cred *unix.Ucred
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if credErr == nil {
			uid = int(cred.Uid)
		}
	}); err != nil {
		return -1, err
	}
	return uid, credErr
}
