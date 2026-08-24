package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/nalbam/romty/internal/client"
	"github.com/nalbam/romty/internal/daemon"
	"github.com/nalbam/romty/internal/paths"
	"github.com/nalbam/romty/internal/ui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "romty:", err)
		os.Exit(1)
	}
}

func run() error {
	if os.Getenv("ROMTY") == "1" {
		return fmt.Errorf("cannot run romty inside a romty terminal")
	}
	runtime, err := paths.Resolve()
	if err != nil {
		return err
	}
	if len(os.Args) == 2 && os.Args[1] == "daemon" {
		return runDaemon(runtime)
	}
	if len(os.Args) != 1 {
		return fmt.Errorf("usage: romty")
	}

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	if err := client.EnsureDaemon(runtime, executable); err != nil {
		return err
	}
	backend := client.New(runtime.Socket)
	snapshot, err := backend.Snapshot()
	if err != nil {
		return err
	}
	_, err = ui.Run(backend, snapshot)
	return err
}

func runDaemon(runtime paths.Paths) error {
	if err := runtime.Ensure(); err != nil {
		return err
	}
	server, err := daemon.New(runtime.Socket, runtime.State, "")
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := server.Serve(ctx); err != nil && !errors.Is(err, daemon.ErrAlreadyRunning) {
		return err
	}
	return nil
}
