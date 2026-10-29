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
	"github.com/opspresso/romty/internal/model"
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

func runCommandWithInput(arguments []string, output io.Writer, input io.Reader) error {
	if len(arguments) == 2 && arguments[0] == "hook" {
		if !model.Agent(arguments[1]).ReportsPhases() {
			return fmt.Errorf("unknown hook provider %q", arguments[1])
		}
		runHookCommand(arguments[1], input)
		return nil
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
	}
	if !knownCommand(command) {
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
	// Point the shared agent link at this login's agent now, so tabs started
	// in an earlier login reach it at once rather than at the next new tab.
	client.LinkSSHAgent(runtime.Directory, os.Environ())
	backend := client.New(runtime.Socket)
	snapshot, err := backend.Snapshot()
	if err != nil {
		return err
	}
	restart := func() error { return client.EnsureDaemon(runtime, executable) }
	_, err = ui.Run(backend, snapshot, runtime.Config, agenthooks.Detect(), restart)
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
	server.SetLogFile(runtime.Log)
	server.SetConfigFile(runtime.Config)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := server.Serve(ctx); err != nil {
		if errors.Is(err, daemon.ErrAlreadyRunning) {
			// Losing the race for the socket is the ordinary outcome of two
			// clients starting at once, not a failure. The line goes to
			// daemon.log, which is where "daemon did not become ready" sends
			// the user.
			fmt.Fprintln(os.Stderr, "romty: another daemon already owns the socket; exiting")
			return nil
		}
		return err
	}
	return nil
}
