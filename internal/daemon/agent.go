package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/opspresso/romty/internal/model"
	"golang.org/x/sys/unix"
)

var foregroundProcessGroup = func(terminal *os.File) (int, error) {
	return unix.IoctlGetInt(int(terminal.Fd()), unix.TIOCGPGRP)
}

var listProcesses = func() ([]byte, error) {
	return exec.Command("ps", "-axo", "pgid=,command=").Output()
}

func sessionAgents(sessions map[string]*session) map[string]model.Agent {
	groups := make(map[string]int, len(sessions))
	for tabID, value := range sessions {
		group, err := foregroundProcessGroup(value.pty)
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
	byGroup := processAgents(output)
	result := make(map[string]model.Agent)
	for tabID, group := range groups {
		if agent := byGroup[group]; agent != "" {
			result[tabID] = agent
		}
	}
	return result
}

func processAgents(output []byte) map[int]model.Agent {
	result := make(map[int]model.Agent)
	for line := range strings.SplitSeq(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		group, err := strconv.Atoi(fields[0])
		if err != nil || result[group] != "" {
			continue
		}
		command := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), fields[0]))
		if agent := commandAgent(command); agent != "" {
			result[group] = agent
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
	}

	if name != "node" && name != "nodejs" && name != "bun" && name != "deno" {
		return ""
	}
	script := strings.ToLower(strings.Join(fields[1:], " "))
	switch {
	case strings.Contains(script, "@anthropic-ai/claude-code"):
		return model.AgentClaude
	case strings.Contains(script, "@openai/codex"):
		return model.AgentCodex
	}
	return ""
}
