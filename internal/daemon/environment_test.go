package daemon

import (
	"slices"
	"strings"
	"testing"
)

// A tab's shell is in romty's terminal, not in the one romty was started
// from, and it outlives that one. What it inherits must not say otherwise.
func TestGuestEnvironmentNamesTheTerminalTheShellIsIn(t *testing.T) {
	guest := guestEnvironment([]string{
		"HOME=/home/user", "PATH=/usr/bin", "LANG=ko_KR.UTF-8", "SSH_AUTH_SOCK=/tmp/agent",
		"EDITOR=vim", "VSCODE_GIT_IPC_HANDLE=/tmp/vscode", "TERMINFO=/opt/terminfo",
		"TERM=xterm-ghostty", "COLORTERM=24bit", "TERM_PROGRAM=ghostty", "TERM_PROGRAM_VERSION=1.4.0",
		"GHOSTTY_RESOURCES_DIR=/Applications/Ghostty.app", "GHOSTTY_SHELL_FEATURES=cursor",
		"KITTY_WINDOW_ID=3", "WEZTERM_PANE=1", "ITERM_SESSION_ID=w0t0p0", "LC_TERMINAL=iTerm2",
		"TMUX=/tmp/tmux-501/default,1,0", "TMUX_PANE=%1", "ZELLIJ=0", "ZELLIJ_SESSION_NAME=main",
		"VTE_VERSION=7600", "WT_SESSION=abc", "ROMTY=0", "ROMTY_TAB_ID=stale",
	}, "tab-1")

	values := make(map[string]string, len(guest))
	for _, entry := range guest {
		name, value, _ := strings.Cut(entry, "=")
		if _, twice := values[name]; twice {
			t.Errorf("%s is set twice: %v", name, guest)
		}
		values[name] = value
	}

	for name, want := range map[string]string{
		"TERM": "xterm-256color", "COLORTERM": "truecolor", "TERM_PROGRAM": "romty",
		"ROMTY": "1", "ROMTY_TAB_ID": "tab-1",
		// The rest of the login is the user's and stays.
		"HOME": "/home/user", "PATH": "/usr/bin", "LANG": "ko_KR.UTF-8", "SSH_AUTH_SOCK": "/tmp/agent",
		"EDITOR": "vim", "VSCODE_GIT_IPC_HANDLE": "/tmp/vscode", "TERMINFO": "/opt/terminfo",
	} {
		if got, set := values[name]; !set || got != want {
			t.Errorf("%s = %q (set %v), want %q", name, got, set, want)
		}
	}
	if values["TERM_PROGRAM_VERSION"] == "" || values["TERM_PROGRAM_VERSION"] == "1.4.0" {
		t.Errorf("TERM_PROGRAM_VERSION = %q, want romty's own", values["TERM_PROGRAM_VERSION"])
	}
	for _, name := range []string{
		"GHOSTTY_RESOURCES_DIR", "GHOSTTY_SHELL_FEATURES", "KITTY_WINDOW_ID", "WEZTERM_PANE",
		"ITERM_SESSION_ID", "LC_TERMINAL", "TMUX", "TMUX_PANE", "ZELLIJ", "ZELLIJ_SESSION_NAME",
		"VTE_VERSION", "WT_SESSION",
	} {
		if value, set := values[name]; set {
			t.Errorf("%s=%q reached the shell, want the host terminal's identity left behind", name, value)
		}
	}
}

func TestGuestEnvironmentKeepsVariablesThatOnlyLookAlike(t *testing.T) {
	guest := guestEnvironment([]string{"TERMINAL=foot", "KITTYCAT=1", "TMUX_PLUGIN_MANAGER_PATH=/p", "MY_TERM_PROGRAM=x"}, "tab-1")
	for _, entry := range []string{"TERMINAL=foot", "KITTYCAT=1", "TMUX_PLUGIN_MANAGER_PATH=/p", "MY_TERM_PROGRAM=x"} {
		if !slices.Contains(guest, entry) {
			t.Errorf("%s was dropped", entry)
		}
	}
}
