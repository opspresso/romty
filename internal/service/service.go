// Package service locates and starts the login service that runs the romty
// daemon: a LaunchAgent on macOS, a systemd user unit on Linux. Once one is
// installed, every start of the daemon goes through it, so a daemon started
// after `romty stop` or an upgrade is the service's and not a child of one
// login.
package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Label names the launchd job; the systemd unit is Unit.
const (
	Label = "dev.romty.daemon"
	Unit  = "romty.service"
)

// Run runs a system tool and returns its combined output. It is a variable
// so tests do not touch the machine's launchd or systemd.
var Run = func(name string, arguments ...string) ([]byte, error) {
	return exec.Command(name, arguments...).CombinedOutput()
}

// File is where the service definition lives for the system goos.
func File(goos, home string) (string, error) {
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", Label+".plist"), nil
	case "linux":
		return filepath.Join(home, ".config", "systemd", "user", Unit), nil
	default:
		return "", fmt.Errorf("romty service supports macOS and Linux, not %s", goos)
	}
}

// LaunchdDomain is the launchd domain of the current user's login session.
func LaunchdDomain() string {
	return "gui/" + strconv.Itoa(os.Getuid())
}

// Start starts the installed service and reports whether one is installed.
// Without one it does nothing, and the caller starts the daemon itself.
func Start(goos, home string) (bool, error) {
	path, err := File(goos, home)
	if err != nil {
		return false, nil
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	var name string
	var arguments []string
	switch goos {
	case "darwin":
		name, arguments = "launchctl", []string{"kickstart", LaunchdDomain() + "/" + Label}
	default:
		name, arguments = "systemctl", []string{"--user", "start", Unit}
	}
	if output, err := Run(name, arguments...); err != nil {
		return true, fmt.Errorf("start the romty service with %s %s: %w: %s; run romty service uninstall to start the daemon without it",
			name, strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
	return true, nil
}
