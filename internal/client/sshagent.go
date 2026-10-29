package client

import (
	"os"
	"path/filepath"
	"strings"
)

// sshAgentLink is the name, beside the daemon socket, of the link romty points
// at the SSH agent of the client that connected last.
const sshAgentLink = "ssh-agent.sock"

// LinkSSHAgent points the runtime directory's ssh-agent.sock at the SSH agent
// in environment and returns environment with SSH_AUTH_SOCK naming that link.
//
// An SSH login forwards its agent through a socket whose path is new on every
// login. A shell keeps the SSH_AUTH_SOCK it started with, so a tab opened
// yesterday would name a socket that went away with yesterday's login, and
// git push or commit signing in it would fail. Tabs given the link instead
// reach the agent of whoever connected last, the way tmux's
// update-environment works.
//
// An environment without SSH_AUTH_SOCK is returned unchanged. One whose agent
// socket is gone leaves the link where it was: the previous agent is more use
// than a path that does not exist.
func LinkSSHAgent(directory string, environment []string) []string {
	link := filepath.Join(directory, sshAgentLink)
	index := -1
	current := ""
	for position, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "SSH_AUTH_SOCK="); ok {
			index, current = position, value
		}
	}
	if index < 0 || current == "" {
		return environment
	}
	// A shell inside a romty tab already names the link.
	if current != link {
		if info, err := os.Stat(current); err == nil && info.Mode()&os.ModeSocket != 0 {
			replaceLink(link, current)
		}
	}
	result := append([]string(nil), environment...)
	result[index] = "SSH_AUTH_SOCK=" + link
	return result
}

// replaceLink points link at target in one step, so a shell reading it never
// finds it missing.
func replaceLink(link, target string) {
	temporary := link + ".new"
	_ = os.Remove(temporary)
	if err := os.Symlink(target, temporary); err != nil {
		return
	}
	if err := os.Rename(temporary, link); err != nil {
		_ = os.Remove(temporary)
	}
}

// environment is what a tab created by this client runs with.
func (c *Client) environment() []string {
	return LinkSSHAgent(filepath.Dir(c.socket), os.Environ())
}
