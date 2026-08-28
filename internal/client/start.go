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

func EnsureDaemon(runtime paths.Paths, executable string) error {
	if err := runtime.Ensure(); err != nil {
		return err
	}
	backend := New(runtime.Socket)
	// Only "nothing is listening" is grounds for starting one. Treating every
	// failed ping as an absent daemon meant a socket that answers but cannot
	// be understood — one held by another program, or a daemon too wedged to
	// reply — sent romty off to start a second daemon, which lost the lock to
	// the first and exited, leaving the user with "daemon did not become
	// ready" in place of what the ping actually said.
	err := backend.Ping()
	if err == nil {
		return nil
	}
	if !Unavailable(err) {
		return err
	}
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

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := backend.Ping(); err == nil {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not become ready; see %s", runtime.Log)
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
