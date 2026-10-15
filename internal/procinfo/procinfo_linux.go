package procinfo

import (
	"os"
	"strconv"
	"strings"
)

func selfPID() int { return os.Getpid() }

// processInfo reads a process's parent and process group from /proc. The
// command name can hold spaces and parentheses, so fields are counted from
// the last closing parenthesis.
func processInfo(pid int) (parent, group int, ok bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0, false
	}
	text := string(data)
	end := strings.LastIndexByte(text, ')')
	if end < 0 {
		return 0, 0, false
	}
	fields := strings.Fields(text[end+1:])
	if len(fields) < 3 {
		return 0, 0, false
	}
	parent, parentErr := strconv.Atoi(fields[1])
	group, groupErr := strconv.Atoi(fields[2])
	return parent, group, parentErr == nil && groupErr == nil
}
