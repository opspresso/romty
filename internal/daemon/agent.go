package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/opspresso/romty/internal/model"
)

var foregroundProcessGroup = func(terminal *os.File) (int, error) {
	return unix.IoctlGetInt(int(terminal.Fd()), unix.TIOCGPGRP)
}

var processListTimeout = 2 * time.Second

var runProcessList = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,pgid=,tpgid=,command=").Output()
}

// maxNestedTerminals bounds how many nested pseudo-terminals romty follows
// from a tab to the job the user sees.
const maxNestedTerminals = 4

// process is one row of the process list. terminalGroup is the foreground
// process group of the process's controlling terminal, or 0 or -1 when it has
// none.
type process struct {
	pid, parent, group, terminalGroup int
	command                           string
}

func listProcesses() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), processListTimeout)
	defer cancel()
	return runProcessList(ctx)
}

func sessionAgents(sessions map[string]*session) map[string]model.Agent {
	groups := make(map[string]int, len(sessions))
	for tabID, value := range sessions {
		group, err := value.foregroundProcessGroup()
		if err == nil {
			groups[tabID] = group
		}
	}
	if len(groups) == 0 {
		return nil
	}

	output, err := listProcesses()
	if err != nil {
		return nil
	}
	processes := parseProcesses(output)
	byGroup := processAgents(processes)
	result := make(map[string]model.Agent)
	for tabID, group := range groups {
		if agent := byGroup[foregroundJob(processes, byGroup, group)]; agent != "" {
			result[tabID] = agent
		}
	}
	return result
}

// foregroundJob returns the process group the user sees in a tab whose
// foreground group is group. A terminal wrapper such as Kiro CLI's
// kiro-cli-term runs the shell in a nested pseudo-terminal, so the tab's
// foreground job is the wrapper and the agent runs in the foreground of the
// nested terminal. A child of the job whose terminal has another foreground
// group marks such a terminal. The walk stops at the first job that runs an
// agent, because an agent such as Codex runs its own commands in nested
// terminals too.
func foregroundJob(processes []process, agents map[int]model.Agent, group int) int {
	for depth := 0; depth < maxNestedTerminals && agents[group] == ""; depth++ {
		members := make(map[int]bool)
		for _, value := range processes {
			if value.group == group {
				members[value.pid] = true
			}
		}
		nested := 0
		for _, value := range processes {
			if members[value.parent] && value.terminalGroup > 0 && value.terminalGroup != group {
				nested = value.terminalGroup
				break
			}
		}
		if nested == 0 {
			break
		}
		group = nested
	}
	return group
}

// parseProcesses reads "pid ppid pgid tpgid command" rows and skips rows it
// cannot read.
func parseProcesses(output []byte) []process {
	var result []process
	for line := range strings.SplitSeq(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		var numbers [4]int
		valid := true
		for index := range numbers {
			value, err := strconv.Atoi(fields[index])
			numbers[index], valid = value, valid && err == nil
		}
		if !valid {
			continue
		}
		command := line
		for range numbers {
			_, command, _ = strings.Cut(strings.TrimLeft(command, " "), " ")
		}
		command = strings.TrimSpace(command)
		result = append(result, process{
			pid: numbers[0], parent: numbers[1], group: numbers[2], terminalGroup: numbers[3], command: command,
		})
	}
	return result
}

func (s *session) foregroundProcessGroup() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, os.ErrClosed
	}
	return foregroundProcessGroup(s.pty)
}

func processAgents(processes []process) map[int]model.Agent {
	result := make(map[int]model.Agent)
	for _, value := range processes {
		if result[value.group] != "" && result[value.group] != model.AgentNode {
			continue
		}
		// A Node.js launcher can share the agent's foreground group. The
		// specific agent takes precedence regardless of process-list order.
		if agent := commandAgent(value.command); agent != "" {
			result[value.group] = agent
		}
	}
	return result
}

func commandAgent(command string) model.Agent {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	name := strings.ToLower(filepath.Base(fields[0]))
	switch {
	case name == "claude" || name == "claude-code":
		return model.AgentClaude
	case name == "codex" || strings.HasPrefix(name, "codex-"):
		return model.AgentCodex
	case name == "opencode" || strings.HasPrefix(name, "opencode-"):
		return model.AgentOpenCode
	case name == "kiro-cli-term":
		// Kiro's terminal wrapper runs the shell, not the agent.
		return ""
	case name == "kiro-cli" || strings.HasPrefix(name, "kiro-cli-"):
		return model.AgentKiro
	case name == "npm" || name == "pnpm" || name == "yarn" || name == "next-server":
		// Node.js tools can replace their process title, hiding the runtime.
		return model.AgentNode
	}

	if name != "node" && name != "nodejs" && name != "bun" && name != "deno" {
		return ""
	}
	arguments := fields[1:]
	script := strings.ToLower(strings.Join(arguments, " "))
	switch {
	case strings.Contains(script, "@anthropic-ai/claude-code"):
		return model.AgentClaude
	case strings.Contains(script, "@openai/codex"):
		return model.AgentCodex
	case strings.Contains(script, "opencode-ai") || runsExecutable(arguments, "opencode"):
		return model.AgentOpenCode
	}
	if name == "node" || name == "nodejs" {
		return model.AgentNode
	}
	return ""
}

// runsExecutable reports whether any argument names the executable, ignoring a
// script extension: `node /usr/local/bin/opencode` and `bun opencode.ts` both
// name it, while `node opencode-notes.js` — a file that merely mentions it —
// does not.
func runsExecutable(arguments []string, name string) bool {
	for _, argument := range arguments {
		base := filepath.Base(argument)
		base = strings.TrimSuffix(base, filepath.Ext(base))
		if strings.EqualFold(base, name) {
			return true
		}
	}
	return false
}
