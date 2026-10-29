package client

import (
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"
	"syscall"
	"time"

	"github.com/opspresso/romty/internal/logfile"
	"github.com/opspresso/romty/internal/paths"
	"github.com/opspresso/romty/internal/service"
)

var daemonStartTimeout = 3 * time.Second

func EnsureDaemon(runtime paths.Paths, executable string) error {
	if err := runtime.Ensure(); err != nil {
		return err
	}
	backend := New(runtime.Socket)
	// Only "nothing is listening" is grounds for starting one. A socket that
	// answers but cannot be understood — held by another program, or a daemon
	// too wedged to reply — returns the ping's own error: a second daemon
	// would lose the lock to the first and hide that error.
	err := backend.Ping()
	if err == nil {
		return nil
	}
	if !Unavailable(err) {
		return err
	}
	held, err := daemonLockHeld(runtime.Socket)
	if err != nil {
		return err
	}
	if !held {
		// The login service starts the daemon when one is installed, so the
		// daemon is the service's rather than a child of this login.
		home, homeErr := os.UserHomeDir()
		installed, err := false, error(nil)
		if homeErr == nil {
			installed, err = service.Start(goruntime.GOOS, home)
		}
		if err != nil {
			return err
		}
		if !installed {
			if err := startDaemon(runtime, executable); err != nil {
				return err
			}
		}
	}
	// A daemon with a missing socket repairs its own listener. Starting a
	// competitor cannot help while that daemon owns the lock.
	deadline := time.Now().Add(daemonStartTimeout)
	for time.Now().Before(deadline) {
		err = backend.Ping()
		if err == nil {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !Unavailable(err) {
		return err
	}
	if held, lockErr := daemonLockHeld(runtime.Socket); lockErr != nil {
		return lockErr
	} else if held {
		return fmt.Errorf("daemon holds its lock but its socket is unreachable; existing sessions may still be running; see %s: %w", runtime.Log, err)
	}
	return fmt.Errorf("daemon did not become ready; see %s: %w", runtime.Log, err)
}

func startDaemon(runtime paths.Paths, executable string) error {
	logFile, err := logfile.Open(runtime.Log)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer logFile.Close()
	nullInput, err := os.Open(os.DevNull)
	if err != nil {
		return fmt.Errorf("open null input: %w", err)
	}
	defer nullInput.Close()

	command := exec.Command(executable, "daemon")
	command.Stdin = nullInput
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	if err := command.Process.Release(); err != nil {
		return fmt.Errorf("release daemon process: %w", err)
	}

	return nil
}
