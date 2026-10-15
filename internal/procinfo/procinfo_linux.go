package procinfo

import (
	"os"
	"strconv"
	"strings"
)

// processInfo reads a process's parent, process group and controlling
// terminal from /proc. The command name can hold spaces and parentheses, so
// fields are counted from the last closing parenthesis. A process without a
// terminal has the device 0.
func processInfo(pid int) (process, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return process{}, false
	}
	text := string(data)
	end := strings.LastIndexByte(text, ')')
	if end < 0 {
		return process{}, false
	}
	// state ppid pgrp session tty_nr tpgid
	fields := strings.Fields(text[end+1:])
	if len(fields) < 6 {
		return process{}, false
	}
	var numbers [4]int
	for index, field := range []string{fields[1], fields[2], fields[4], fields[5]} {
		value, err := strconv.Atoi(field)
		if err != nil {
			return process{}, false
		}
		numbers[index] = value
	}
	return process{
		parent: numbers[0], group: numbers[1], terminal: numbers[2] != 0, terminalGroup: numbers[3],
	}, true
}
