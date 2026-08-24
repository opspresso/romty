package client

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/nalbam/romty/internal/paths"
)

func EnsureDaemon(runtime paths.Paths, executable string) error {
	backend := New(runtime.Socket)
	if err := backend.Ping(); err == nil {
		return nil
	}
	if err := runtime.Ensure(); err != nil {
		return err
	}

	logFile, err := os.OpenFile(runtime.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
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
