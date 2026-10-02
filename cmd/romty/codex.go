package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/opspresso/romty/internal/client"
	"github.com/opspresso/romty/internal/codexbridge"
	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/paths"
	"github.com/opspresso/romty/internal/protocol"
)

// runCodex puts a transparent observer on this TUI's own app-server
// connection. Shared-daemon hooks inherit the daemon's environment, so their
// ROMTY_TAB_ID cannot identify the terminal that owns a conversation.
func runCodex(arguments []string, input io.Reader, output io.Writer) error {
	tabID := os.Getenv("ROMTY_TAB_ID")
	if tabID == "" {
		return fmt.Errorf("run `romty codex` inside a romty terminal")
	}
	for _, arg := range arguments {
		if arg == "--no-daemon" || arg == "--remote" || strings.HasPrefix(arg, "--remote=") {
			return fmt.Errorf("romty codex manages the app-server connection; remove %s", arg)
		}
	}
	executable, err := exec.LookPath("codex")
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	runtime, err := paths.Resolve()
	if err != nil {
		return err
	}
	backend := client.New(runtime.Socket)
	// Check compatibility before starting either Codex process.
	probe := protocol.AgentEvent{Agent: model.AgentCodex, SessionID: "connecting", HookEvent: "RuntimeStatus",
		Runtime: &model.AgentStatus{Agent: model.AgentCodex, Phase: model.AgentPhaseUnknown, Source: "runtime"}}
	if err := backend.ReportAgentEvent(tabID, probe); err != nil {
		return err
	}
	defer func() {
		probe.HookEvent = "RuntimeDisconnected"
		_ = backend.ReportAgentEvent(tabID, probe)
	}()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	startCtx, stopStart := context.WithTimeout(ctx, 20*time.Second)
	start := exec.CommandContext(startCtx, executable, "app-server", "daemon", "start")
	start.Stderr = os.Stderr
	err = start.Run()
	stopStart()
	if err != nil {
		return fmt.Errorf("start Codex app-server: %w", err)
	}

	directory, err := os.MkdirTemp("", "romty-codex-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	socket := filepath.Join(directory, "bridge.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.Chmod(socket, 0o600); err != nil {
		return err
	}

	// A slow/missing romty daemon must never stall approval replies or model
	// output. Coalesce pending status snapshots; the latest state wins.
	updates := make(chan protocol.AgentEvent, 1)
	var queueMu sync.Mutex
	var latestGeneration uint64
	queueStatus := func(event protocol.AgentEvent) {
		queueMu.Lock()
		defer queueMu.Unlock()
		if event.RuntimeGeneration < latestGeneration {
			return
		}
		latestGeneration = event.RuntimeGeneration
		select {
		case updates <- event:
			return
		default:
		}
		select {
		case <-updates:
		default:
		}
		select {
		case updates <- event:
		default:
		}
	}
	var reporters sync.WaitGroup
	reporters.Add(1)
	go func() {
		defer reporters.Done()
		for event := range updates {
			if err := backend.ReportAgentEvent(tabID, event); err != nil {
				fmt.Fprintln(os.Stderr, "romty: native status delivery failed:", err)
			}
		}
	}()
	var connections sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		var generation uint64
		for {
			front, err := listener.Accept()
			if err != nil {
				return
			}
			generation++
			connectionGeneration := generation
			connections.Add(1)
			go func() {
				defer connections.Done()
				defer front.Close()
				back, err := openCodexProxy(ctx, executable)
				if err != nil {
					fmt.Fprintln(os.Stderr, "romty: connect Codex app-server:", err)
					return
				}
				observer := codexbridge.NewObserver(func(event protocol.AgentEvent) {
					event.RuntimeID, event.RuntimeGeneration = filepath.Base(directory), connectionGeneration
					queueStatus(event)
				})
				_ = codexbridge.Proxy(front, back, observer)
			}()
		}
	}()

	args := codexArguments(socket, cwd, arguments)
	command := exec.CommandContext(ctx, executable, args...)
	command.Stdin, command.Stdout, command.Stderr = input, output, os.Stderr
	err = command.Run()
	listener.Close()
	cancel()
	<-acceptDone
	connections.Wait()
	close(updates)
	reporters.Wait()
	return err
}

func codexArguments(socket, cwd string, arguments []string) []string {
	args := []string{"--remote", "unix://" + socket}
	hasDirectory := false
	for _, argument := range arguments {
		if argument == "--" {
			break
		}
		if argument == "--cd" || strings.HasPrefix(argument, "--cd=") || strings.HasPrefix(argument, "-C") {
			hasDirectory = true
		}
	}
	// Remote TUI mode otherwise defaults to the server's working directory,
	// which can belong to the terminal that originally started the daemon.
	if !hasDirectory {
		args = append(args, "--cd", cwd)
	}
	return append(args, arguments...)
}

type codexProxyProcess struct {
	io.Reader
	io.Writer
	command *exec.Cmd
	input   io.Closer
	output  io.Closer
	once    sync.Once
}

func openCodexProxy(ctx context.Context, executable string) (*codexProxyProcess, error) {
	command := exec.CommandContext(ctx, executable, "app-server", "proxy")
	command.Stderr = os.Stderr
	input, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, err
	}
	if err := command.Start(); err != nil {
		input.Close()
		output.Close()
		return nil, err
	}
	return &codexProxyProcess{Reader: output, Writer: input, command: command, input: input, output: output}, nil
}

func (p *codexProxyProcess) Close() error {
	p.once.Do(func() {
		p.input.Close()
		p.output.Close()
		_ = p.command.Process.Kill()
		_ = p.command.Wait()
	})
	return nil
}
