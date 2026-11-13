package daemon

import (
	"slices"
	"strings"

	"github.com/opspresso/romty/internal/version"
)

// hostTerminalVariables and hostTerminalPrefixes name the variables a program
// reads to learn which terminal or multiplexer it is running in. They describe
// the terminal the user started romty from. A program in a tab is not talking
// to that terminal: it is talking to romty, and it keeps running after that
// terminal is closed and another one attaches.
//
// Left in place they make a program use what the named terminal supports and
// romty does not — Claude Code its Ghostty notifications, Codex the options of
// a tmux it is not inside — and make the same program behave differently in a
// tab opened over SSH, where none of them are set.
var (
	hostTerminalVariables = []string{
		"COLORTERM", "ITERM_PROFILE", "ITERM_SESSION_ID", "KONSOLE_VERSION",
		"LC_TERMINAL", "LC_TERMINAL_VERSION", "STY", "TERMINAL_EMULATOR",
		"TERM_PROGRAM", "TERM_PROGRAM_VERSION", "TERM_SESSION_ID", "TMUX",
		"TMUX_PANE", "VTE_VERSION", "WT_PROFILE_ID", "WT_SESSION",
	}
	hostTerminalPrefixes = []string{"ALACRITTY_", "GHOSTTY_", "KITTY_", "WEZTERM_", "ZELLIJ"}
)

// guestEnvironment is the environment of a tab's shell: the client's, with
// the terminal it describes replaced by the one the shell is really in.
//
// COLORTERM is set because the tab's terminal keeps 24-bit colour whatever the
// host has, and the dashboard reduces it to what the host can show when it
// draws. An SSH login does not carry the variable, so without it every agent
// started over SSH fell back to 256 colours.
func guestEnvironment(environment []string, tabID string) []string {
	guest := make([]string, 0, len(environment)+6)
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if name == "TERM" || name == "ROMTY" || name == "ROMTY_TAB_ID" ||
			slices.Contains(hostTerminalVariables, name) ||
			slices.ContainsFunc(hostTerminalPrefixes, func(prefix string) bool { return strings.HasPrefix(name, prefix) }) {
			continue
		}
		guest = append(guest, entry)
	}
	return append(guest,
		"TERM=xterm-256color", "COLORTERM=truecolor",
		"TERM_PROGRAM=romty", "TERM_PROGRAM_VERSION="+version.String(),
		"ROMTY=1", "ROMTY_TAB_ID="+tabID,
	)
}
