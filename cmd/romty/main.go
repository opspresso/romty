package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/opspresso/romty/internal/agenthooks"
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
	return runCommandWithInput(os.Args[1:], os.Stdout, os.Stdin)
}

func runCommand(arguments []string, output io.Writer) error {
	return runCommandWithInput(arguments, output, os.Stdin)
}

func runCommandWithInput(arguments []string, output io.Writer, input io.Reader) error {
	if len(arguments) == 2 && arguments[0] == "hook" {
		switch arguments[1] {
		case "claude", "codex":
			runHookCommand(arguments[1], input)
			return nil
		default:
			return fmt.Errorf("unknown hook provider %q", arguments[1])
		}
	}
	if len(arguments) > 1 {
		return fmt.Errorf("usage: romty <command>; run `romty help` for details")
	}
	command := ""
	if len(arguments) == 1 {
		command = arguments[0]
	}
	theme := newCommandTheme(output)
	switch command {
	case "help", "-h", "--help":
		return printHelp(output, theme)
	case "version", "-v", "--version":
		return printVersion(output, theme)
	case "", "daemon", "stop", "status", "doctor", "hooks", "list":
	default:
		return fmt.Errorf("unknown command %q; run `romty help` for usage", command)
	}

	if os.Getenv("ROMTY") == "1" && (command == "" || command == "daemon" || command == "stop") {
		return fmt.Errorf("cannot run romty inside a romty terminal")
	}
	if command == "hooks" {
		return installAgentHooks(output, theme)
	}
	runtime, err := paths.Resolve()
	if err != nil {
		return err
	}
	switch command {
	case "daemon":
		return runDaemon(runtime)
	case "stop":
		if err := runtime.Ensure(); err != nil {
			return err
		}
		// Stopping an already stopped daemon is a no-op, not a failure.
		if err := client.New(runtime.Socket).Shutdown(); err != nil && !client.Unavailable(err) {
			return fmt.Errorf("stop daemon: %w", err)
		}
		return nil
	case "status":
		return printStatus(output, runtime, theme)
	case "doctor":
		return printDoctor(output, runtime, theme)
	case "list":
		return printList(output, runtime, theme)
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
	_, err = ui.Run(backend, snapshot, runtime.Config, agenthooks.Detect())
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
