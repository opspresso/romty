package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unicode"

	"github.com/charmbracelet/x/term"
	"github.com/opspresso/romty/internal/agenthooks"
	"github.com/opspresso/romty/internal/client"
	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/paths"
	"github.com/opspresso/romty/internal/version"
)

// command is one romty subcommand: the word that runs it, what help says about
// it, and whether it is the one help colours as destructive.
type command struct {
	name        string
	description string
	destructive bool
}

// commands is what romty accepts, in the order help prints them. It is the only
// place they are named: help, the colour help writes, and the check that
// refuses an unknown word all read this. Naming them again in any of those is
// how one of them falls behind.
var commands = []command{
	{name: "status", description: "Show daemon and session status"},
	{name: "version", description: "Show the romty version"},
	{name: "help", description: "Show this help"},
	{name: "doctor", description: "Check the local romty environment"},
	{name: "hooks", description: "Install or update agent status hooks"},
	{name: "list", description: "List roots, workspaces, and sessions"},
	{name: "stop", description: "Stop the daemon and all running sessions", destructive: true},
}

// knownCommand reports whether romty has a command by that name. The empty word
// opens the TUI, and daemon is what the TUI starts behind it rather than
// something a user runs, so neither is in the help list.
func knownCommand(name string) bool {
	if name == "" || name == "daemon" {
		return true
	}
	for _, value := range commands {
		if value.name == name {
			return true
		}
	}
	return false
}

// helpNameWidth is the column the descriptions line up in.
const helpNameWidth = 9

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

// failed and okay are the two sentences a doctor check writes about what it
// inspected. Spelling them out at each call site is what left one check
// quoting its path and another not, and "error" inside the value here and
// beside the label there.
func (t commandTheme) failed(reason string) string {
	return t.failure("error " + strconv.Quote(reason))
}

func (t commandTheme) okay(detail string) string {
	return t.good("ok") + " " + strconv.Quote(detail)
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
	var help strings.Builder
	fmt.Fprintf(&help, "%s - persistent terminal workspace manager\n\n%s\n  romty\n  romty <command>\n\n%s\n",
		theme.label("romty"), theme.label("Usage:"), theme.label("Commands:"))
	for _, value := range commands {
		paint := theme.good
		if value.destructive {
			paint = theme.failure
		}
		padding := strings.Repeat(" ", max(helpNameWidth-len(value.name), 1))
		fmt.Fprintf(&help, "  %s%s%s\n", paint(value.name), padding, value.description)
	}
	_, err := io.WriteString(output, help.String())
	return err
}

func installAgentHooks(output io.Writer, theme commandTheme) error {
	statuses := agenthooks.Detect()
	results, installErr := agenthooks.Install(agenthooks.Pending(statuses))
	byProvider := make(map[agenthooks.Provider]agenthooks.Result, len(results))
	for _, result := range results {
		byProvider[result.Provider] = result
	}
	var failures []error
	for _, status := range statuses {
		label := string(status.Provider)
		value := ""
		switch status.State {
		case agenthooks.StateUnavailable:
			value = theme.warning("not found")
		case agenthooks.StateDevelopment:
			value = theme.warning("development build; skipped")
		case agenthooks.StateInvalid:
			value = theme.failure("invalid")
			failures = append(failures, fmt.Errorf("%s hooks: %w", status.Provider.DisplayName(), status.Err))
		case agenthooks.StateCurrent:
			value = theme.good("current") + "  " + commandText(status.Path)
		case agenthooks.StateMissing, agenthooks.StateOutdated:
			if result, ok := byProvider[status.Provider]; ok {
				value = theme.good(string(result.Action)) + "  " + commandText(result.Path)
			} else {
				value = theme.failure("failed") + "  " + commandText(status.Path)
			}
		}
		if err := printField(output, theme, label, value); err != nil {
			return err
		}
	}
	if installErr != nil {
		failures = append(failures, installErr)
	}
	if err := errors.Join(failures...); err != nil {
		return fmt.Errorf("configure agent hooks: %w", err)
	}
	return nil
}

func commandText(value string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return '�'
		}
		return character
	}, value)
}

func printVersion(output io.Writer, theme commandTheme) error {
	_, err := fmt.Fprintf(output, "%s %s\n", theme.label("romty"), theme.good(version.String()))
	return err
}

func fieldLine(theme commandTheme, label, value string) string {
	padded := fmt.Sprintf("%-*s", commandLabelWidth, label+":")
	return theme.label(padded) + " " + value + "\n"
}

func printField(output io.Writer, theme commandTheme, label, value string) error {
	_, err := io.WriteString(output, fieldLine(theme, label, value))
	return err
}

// writeField is printField for a report built in memory, where a write has no
// failure mode. The doctor checks called printField and dropped its error,
// which reads as an unchecked write rather than as one that cannot fail.
func writeField(output *strings.Builder, theme commandTheme, label, value string) {
	output.WriteString(fieldLine(theme, label, value))
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
		summary.workspaces += len(root.Directories)
	}
	for tab := range snapshot.Tabs() {
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
	return summary
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
		} else if tab.AgentPhase != "" && tab.AgentPhase != model.AgentPhaseUnknown {
			agent += "/" + string(tab.AgentPhase)
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
	writeField(&report, theme, "version", version.String())
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
		writeField(output, theme, "runtime", theme.warning("not created"))
		return 0
	}
	if err != nil {
		writeField(output, theme, "runtime", theme.failed(err.Error()))
		return 1
	}
	stat, owned := info.Sys().(*syscall.Stat_t)
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !owned ||
		stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o077 != 0 {
		writeField(output, theme, "runtime",
			theme.failed(path+" must be a private directory owned by the current user"))
		return 1
	}
	writeField(output, theme, "runtime", theme.okay(path))
	return 0
}

func checkJSONFile(output *strings.Builder, theme commandTheme, label, path string, target any) int {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		writeField(output, theme, label, theme.warning("not created"))
		return 0
	}
	if err != nil {
		writeField(output, theme, label, theme.failed(err.Error()))
		return 1
	}
	if err := json.Unmarshal(data, target); err != nil {
		writeField(output, theme, label, theme.failed(fmt.Sprintf("invalid JSON in %s: %s", path, err)))
		return 1
	}
	writeField(output, theme, label, theme.okay(path))
	return 0
}

func checkShell(output *strings.Builder, theme commandTheme) int {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	resolved, err := exec.LookPath(shell)
	if err != nil {
		writeField(output, theme, "shell", theme.failed(err.Error()))
		return 1
	}
	writeField(output, theme, "shell", theme.okay(resolved))
	return 0
}

func checkDaemon(output *strings.Builder, theme commandTheme, socket string) int {
	backend := client.New(socket)
	daemonProtocol, err := backend.ProtocolVersion()
	if client.Unavailable(err) {
		writeField(output, theme, "daemon", theme.warning("stopped"))
		return 0
	}
	if err == nil {
		_, err = backend.Snapshot()
	}
	if err != nil {
		writeField(output, theme, "daemon", theme.failed(err.Error()))
		return 1
	}
	writeField(output, theme, "daemon", theme.good("running")+fmt.Sprintf(" (protocol: %d)", daemonProtocol))
	return 0
}
