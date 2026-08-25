package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/charmbracelet/x/term"
	"github.com/opspresso/romty/internal/client"
	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/paths"
	"github.com/opspresso/romty/internal/protocol"
	"github.com/opspresso/romty/internal/version"
)

const helpText = `romty - persistent terminal workspace manager

Usage:
  romty
  romty <command>

Commands:
  status   Show daemon and session status
  version  Show the romty version
  help     Show this help
  doctor   Check the local romty environment
  list     List roots, workspaces, and sessions
  stop     Stop the daemon and all running sessions
`

const commandLabelWidth = 11

type commandTheme struct {
	enabled bool
}

func newCommandTheme(output io.Writer) commandTheme {
	if os.Getenv("NO_COLOR") != "" {
		return commandTheme{}
	}
	file, ok := output.(*os.File)
	return commandTheme{enabled: ok && term.IsTerminal(file.Fd())}
}

func (t commandTheme) paint(code, value string) string {
	if !t.enabled {
		return value
	}
	return "\x1b[" + code + "m" + value + "\x1b[0m"
}

func (t commandTheme) label(value string) string {
	return t.paint("36", value)
}

func (t commandTheme) good(value string) string {
	return t.paint("32", value)
}

func (t commandTheme) warning(value string) string {
	return t.paint("33", value)
}

func (t commandTheme) failure(value string) string {
	return t.paint("31", value)
}

func (t commandTheme) agent(agent model.Agent, value string) string {
	switch agent {
	case model.AgentClaude:
		return t.paint("38;2;217;119;87", value)
	case model.AgentCodex:
		return t.paint("38;2;59;130;246", value)
	default:
		return value
	}
}

func printHelp(output io.Writer, theme commandTheme) error {
	if !theme.enabled {
		_, err := io.WriteString(output, helpText)
		return err
	}
	_, err := fmt.Fprintf(output, `%s - persistent terminal workspace manager

%s
  romty
  romty <command>

%s
  %s   Show daemon and session status
  %s  Show the romty version
  %s     Show this help
  %s   Check the local romty environment
  %s     List roots, workspaces, and sessions
  %s     Stop the daemon and all running sessions
`, theme.label("romty"), theme.label("Usage:"), theme.label("Commands:"),
		theme.good("status"), theme.good("version"), theme.good("help"), theme.good("doctor"),
		theme.good("list"), theme.failure("stop"))
	return err
}

func printVersion(output io.Writer, theme commandTheme) error {
	_, err := fmt.Fprintf(output, "%s %s\n", theme.label("romty"), theme.good(version.String()))
	return err
}

func printField(output io.Writer, theme commandTheme, label, value string) error {
	padded := fmt.Sprintf("%-*s", commandLabelWidth, label+":")
	_, err := fmt.Fprintf(output, "%s %s\n", theme.label(padded), value)
	return err
}

type snapshotSummary struct {
	roots      int
	workspaces int
	sessions   int
	claude     int
	codex      int
	shell      int
}

type configDocument struct {
	LeftWidth        int  `json:"left_width,omitempty"`
	MousePassthrough bool `json:"mouse_passthrough,omitempty"`
}

func printStatus(output io.Writer, runtime paths.Paths, theme commandTheme) error {
	backend := client.New(runtime.Socket)
	daemonProtocol, err := backend.ProtocolVersion()
	if client.Unavailable(err) {
		return printField(output, theme, "daemon", theme.warning("stopped"))
	}
	if err != nil {
		return fmt.Errorf("inspect daemon: %w", err)
	}
	snapshot, err := backend.Snapshot()
	if err != nil {
		return fmt.Errorf("read daemon status: %w", err)
	}
	summary := summarizeSnapshot(snapshot)
	fields := []struct {
		label string
		value string
	}{
		{label: "daemon", value: theme.good("running")},
		{label: "version", value: version.String()},
		{label: "protocol", value: fmt.Sprint(daemonProtocol)},
		{label: "roots", value: fmt.Sprint(summary.roots)},
		{label: "workspaces", value: fmt.Sprint(summary.workspaces)},
		{label: "sessions", value: fmt.Sprintf("%d (claude: %d, codex: %d, shell: %d)",
			summary.sessions, summary.claude, summary.codex, summary.shell)},
	}
	for _, field := range fields {
		if err := printField(output, theme, field.label, field.value); err != nil {
			return err
		}
	}
	return nil
}

func summarizeSnapshot(snapshot model.Snapshot) snapshotSummary {
	summary := snapshotSummary{roots: len(snapshot.Roots)}
	for _, root := range snapshot.Roots {
		addTabs(&summary, root.Tabs)
		summary.workspaces += len(root.Directories)
		for _, workspace := range root.Directories {
			addTabs(&summary, workspace.Tabs)
		}
	}
	return summary
}

func addTabs(summary *snapshotSummary, tabs []model.Tab) {
	for _, tab := range tabs {
		if !tab.Running {
			continue
		}
		summary.sessions++
		switch tab.Agent {
		case model.AgentClaude:
			summary.claude++
		case model.AgentCodex:
			summary.codex++
		default:
			summary.shell++
		}
	}
}

func printList(output io.Writer, runtime paths.Paths, theme commandTheme) error {
	snapshot, err := client.New(runtime.Socket).Snapshot()
	if client.Unavailable(err) {
		return printField(output, theme, "daemon", theme.warning("stopped"))
	}
	if err != nil {
		return fmt.Errorf("list daemon state: %w", err)
	}
	if len(snapshot.Roots) == 0 {
		return printField(output, theme, "roots", theme.warning("none"))
	}
	for _, root := range snapshot.Roots {
		if err := printField(output, theme, "root", fmt.Sprintf("%q  %q", root.Root.Name, root.Root.Path)); err != nil {
			return err
		}
		if err := printTabs(output, theme, "  ", root.Tabs); err != nil {
			return err
		}
		if root.Error != "" {
			if err := printField(output, theme, "  error", theme.failure(fmt.Sprintf("%q", root.Error))); err != nil {
				return err
			}
		}
		for _, workspace := range root.Directories {
			if err := printField(output, theme, "  workspace",
				fmt.Sprintf("%q  %q", workspace.Workspace.Name, workspace.Workspace.Path)); err != nil {
				return err
			}
			if err := printTabs(output, theme, "    ", workspace.Tabs); err != nil {
				return err
			}
		}
	}
	return nil
}

func printTabs(output io.Writer, theme commandTheme, indent string, tabs []model.Tab) error {
	for _, tab := range tabs {
		if !tab.Running {
			continue
		}
		agent := string(tab.Agent)
		if agent == "" {
			agent = "shell"
		}
		value := fmt.Sprintf("%q  %s", tab.Name, theme.agent(tab.Agent, agent))
		if err := printField(output, theme, indent+"tab", value); err != nil {
			return err
		}
	}
	return nil
}

func printDoctor(output io.Writer, runtime paths.Paths, theme commandTheme) error {
	var report strings.Builder
	problems := 0
	printField(&report, theme, "version", version.String())
	problems += checkRuntime(&report, theme, runtime.Directory)
	problems += checkJSONFile(&report, theme, "state", runtime.State, &model.State{})
	problems += checkJSONFile(&report, theme, "config", runtime.Config, &configDocument{})
	problems += checkShell(&report, theme)
	problems += checkDaemon(&report, theme, runtime.Socket)
	if _, err := io.WriteString(output, report.String()); err != nil {
		return err
	}
	if problems == 0 {
		return nil
	}
	suffix := "s"
	if problems == 1 {
		suffix = ""
	}
	return fmt.Errorf("doctor found %d problem%s", problems, suffix)
}

func checkRuntime(output *strings.Builder, theme commandTheme, path string) int {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		printField(output, theme, "runtime", theme.warning("not created"))
		return 0
	}
	if err != nil {
		printField(output, theme, "runtime", theme.failure("error "+fmt.Sprintf("%q", err.Error())))
		return 1
	}
	stat, owned := info.Sys().(*syscall.Stat_t)
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !owned || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o077 != 0 {
		printField(output, theme, "runtime", theme.failure(fmt.Sprintf("error %q must be a private directory owned by the current user", path)))
		return 1
	}
	printField(output, theme, "runtime", theme.good("ok")+" "+fmt.Sprintf("%q", path))
	return 0
}

func checkJSONFile(output *strings.Builder, theme commandTheme, label, path string, target any) int {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		printField(output, theme, label, theme.warning("not created"))
		return 0
	}
	if err != nil {
		printField(output, theme, label, theme.failure("error "+fmt.Sprintf("%q", err.Error())))
		return 1
	}
	if err := json.Unmarshal(data, target); err != nil {
		printField(output, theme, label, theme.failure(fmt.Sprintf("error invalid JSON in %q: %q", path, err.Error())))
		return 1
	}
	printField(output, theme, label, theme.good("ok")+" "+fmt.Sprintf("%q", path))
	return 0
}

func checkShell(output *strings.Builder, theme commandTheme) int {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	resolved, err := exec.LookPath(shell)
	if err != nil {
		printField(output, theme, "shell", theme.failure("error "+fmt.Sprintf("%q", err.Error())))
		return 1
	}
	printField(output, theme, "shell", theme.good("ok")+" "+fmt.Sprintf("%q", resolved))
	return 0
}

func checkDaemon(output *strings.Builder, theme commandTheme, socket string) int {
	daemonProtocol, err := client.New(socket).ProtocolVersion()
	if client.Unavailable(err) {
		printField(output, theme, "daemon", theme.warning("stopped"))
		return 0
	}
	if err != nil {
		printField(output, theme, "daemon", theme.failure("error "+fmt.Sprintf("%q", err.Error())))
		return 1
	}
	if daemonProtocol != protocol.Version {
		printField(output, theme, "daemon", theme.failure(fmt.Sprintf("error protocol %d, client protocol %d", daemonProtocol, protocol.Version)))
		return 1
	}
	printField(output, theme, "daemon", theme.good("running")+fmt.Sprintf(" (protocol: %d)", daemonProtocol))
	return 0
}
