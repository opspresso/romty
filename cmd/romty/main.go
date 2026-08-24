package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/opspresso/romty/internal/client"
	"github.com/opspresso/romty/internal/daemon"
	"github.com/opspresso/romty/internal/paths"
	"github.com/opspresso/romty/internal/ui"
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
	if len(os.Args) == 2 {
		switch os.Args[1] {
		case "daemon":
			return runDaemon(runtime)
		case "stop":
			// Stopping an already stopped daemon is a no-op, not a failure.
			if err := client.New(runtime.Socket).Shutdown(); err != nil && !client.Unavailable(err) {
				return fmt.Errorf("stop daemon: %w", err)
			}
			return nil
		default:
			return fmt.Errorf("usage: romty [stop]")
		}
	}
	if len(os.Args) != 1 {
		return fmt.Errorf("usage: romty [stop]")
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
	_, err = ui.Run(backend, snapshot, runtime.Config)
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
	if err := server.Serve(ctx); err != nil {
		if errors.Is(err, daemon.ErrAlreadyRunning) {
			// Losing the race for the socket is the ordinary outcome of two
			// clients starting at once, not a failure — but exiting in
			// silence left daemon.log empty, and the caller that gives up
			// with "see daemon.log" pointing at nothing.
			fmt.Fprintln(os.Stderr, "romty: another daemon already owns the socket; exiting")
			return nil
		}
		return err
	}
	return nil
}
