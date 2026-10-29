package daemon

import (
	"os/exec"
	"strconv"

	"golang.org/x/sys/unix"
)

// awakeCommand prevents idle sleep until the daemon exits: caffeinate -w ends
// with the process it watches.
func awakeCommand(daemon int) *exec.Cmd {
	return exec.Command("caffeinate", "-i", "-w", strconv.Itoa(daemon))
}

func redirect(from, to int) error {
	return unix.Dup2(from, to)
}
