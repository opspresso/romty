package client

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/opspresso/romty/internal/paths"
)

// maxDaemonLogBytes is a variable so rotation tests need not create a multi-megabyte file.
var maxDaemonLogBytes int64 = 3 << 20

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
		if err := startDaemon(runtime, executable); err != nil {
			return err
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
		if !Unavailable(err) {
			return err
		}
		time.Sleep(25 * time.Millisecond)
	}
	if held, lockErr := daemonLockHeld(runtime.Socket); lockErr != nil {
		return lockErr
	} else if held {
		return fmt.Errorf("daemon holds its lock but its socket is unreachable; existing sessions may still be running; see %s: %w", runtime.Log, err)
	}
	return fmt.Errorf("daemon did not become ready; see %s: %w", runtime.Log, err)
}

func startDaemon(runtime paths.Paths, executable string) error {
	logFile, err := openDaemonLog(runtime.Log)
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

func openDaemonLog(path string) (*os.File, error) {
	file, info, err := openPrivateLog(path)
	if err != nil {
		return nil, err
	}
	if info.Size() < maxDaemonLogBytes {
		return file, nil
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	archive := path + ".1"
	if err := os.Remove(archive); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove archived daemon log: %w", err)
	}
	if err := os.Rename(path, archive); err != nil {
		return nil, fmt.Errorf("archive daemon log: %w", err)
	}
	file, _, err = openPrivateLog(path)
	return file, err
}

func openPrivateLog(path string) (*os.File, os.FileInfo, error) {
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_APPEND|syscall.O_WRONLY|
		syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		file.Close()
		return nil, nil, fmt.Errorf("daemon log must be a regular file owned only by the current user")
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, nil, err
	}
	if err := syscall.SetNonblock(fd, false); err != nil {
		file.Close()
		return nil, nil, err
	}
	return file, info, nil
}
