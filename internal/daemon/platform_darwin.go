package daemon

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

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

// excludeFromBackup keeps Time Machine from copying the runtime directory:
// saved sessions hold terminal output, which can include secrets. tmutil sets
// a sticky exclusion that needs no administrator rights.
var excludeFromBackup = func(directory string) error {
	output, err := exec.Command("tmutil", "addexclusion", directory).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
