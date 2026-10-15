package daemon

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/testutil"
)

func TestProcessAgentsRecogniseClaudeCodeAndCodex(t *testing.T) {
	output := []byte(`  101 1 101 101 /bin/zsh
  202 1 202 202 /Users/me/.local/bin/claude
  303 1 303 303 node /usr/local/lib/node_modules/@openai/codex/bin/codex.js
  404 1 404 404 vim codex-notes.md
  505 1 505 505 /opt/homebrew/bin/opencode
  606 1 606 606 node /home/me/scripts/opencode-notes.js
  707 1 707 707 bun /usr/local/bin/opencode
`)
	want := map[int]model.Agent{
		202: model.AgentClaude,
		303: model.AgentCodex,
		505: model.AgentOpenCode,
		606: model.Agent("node"),
		707: model.AgentOpenCode,
	}

	if got := processAgents(parseProcesses(output)); !reflect.DeepEqual(got, want) {
		t.Fatalf("processAgents() = %#v, want %#v", got, want)
	}
}

func TestProcessAgentsRecogniseNativeAgentExecutables(t *testing.T) {
	output := []byte(`  101 1 101 101 /opt/homebrew/bin/claude-code --resume
  202 1 202 202 /Users/me/.cache/codex-aarch64-apple-darwin
  303 1 303 303 /opt/homebrew/bin/opencode --continue
  404 1 404 404 kiro-cli
  404 1 404 404 /Users/me/.local/bin/kiro-cli-chat
  505 1 505 505 vim kiro-notes.md
`)
	want := map[int]model.Agent{
		101: model.AgentClaude,
		202: model.AgentCodex,
		303: model.AgentOpenCode,
		404: model.AgentKiro,
	}

	if got := processAgents(parseProcesses(output)); !reflect.DeepEqual(got, want) {
		t.Fatalf("processAgents() = %#v, want %#v", got, want)
	}
}

func TestCommandAgentRecognisesNodeServices(t *testing.T) {
	for _, probe := range []struct {
		command string
		want    model.Agent
	}{
		{command: "node server.js", want: "node"},
		{command: "/opt/homebrew/bin/node --watch server.js", want: "node"},
		{command: "nodejs server.js", want: "node"},
		{command: "node /project/node_modules/vite/bin/vite.js", want: "node"},
		{command: "npm run dev", want: "node"},
		{command: "pnpm dev", want: "node"},
		{command: "yarn start", want: "node"},
		{command: "next-server (v16.1.1)", want: "node"},
		{command: "node /usr/local/lib/node_modules/@anthropic-ai/claude-code/cli.js", want: model.AgentClaude},
		{command: "node /usr/local/lib/node_modules/@openai/codex/bin/codex.js", want: model.AgentCodex},
		{command: "node /usr/local/bin/opencode", want: model.AgentOpenCode},
		{command: "node /project/node_modules/opencode-ai/bin/opencode", want: model.AgentOpenCode},
		{command: "vim node-server.js"},
		{command: "zsh -c node server.js"},
		{command: "node-exporter"},
		{command: "bun server.ts"},
		{command: "deno run server.ts"},
	} {
		t.Run(probe.command, func(t *testing.T) {
			if got := commandAgent(probe.command); got != probe.want {
				t.Fatalf("commandAgent(%q) = %q, want %q", probe.command, got, probe.want)
			}
		})
	}
}

func TestProcessAgentsPreferAgentsOverNodeWrappers(t *testing.T) {
	for _, agent := range []model.Agent{model.AgentClaude, model.AgentCodex, model.AgentOpenCode} {
		for _, output := range []string{
			"101 1 101 101 node wrapper.js\n101 1 101 101 " + string(agent) + "\n",
			"101 1 101 101 " + string(agent) + "\n101 1 101 101 node wrapper.js\n",
		} {
			if got := processAgents(parseProcesses([]byte(output)))[101]; got != agent {
				t.Fatalf("processAgents(%q)[101] = %q, want %q", output, got, agent)
			}
		}
	}
}

func TestSessionAgentsMatchTabsByForegroundProcessGroup(t *testing.T) {
	claudePTY := new(os.File)
	codexPTY := new(os.File)
	opencodePTY := new(os.File)
	nodePTY := new(os.File)
	groups := map[*os.File]int{claudePTY: 101, codexPTY: 202, opencodePTY: 303, nodePTY: 404}
	stubForeground(t, func(terminal *os.File) int { return groups[terminal] },
		"101 1 101 101 claude\n202 1 202 202 codex\n303 1 303 303 opencode\n404 1 404 404 node server.js\n505 1 505 505 node background.js\n")

	sessions := map[string]*session{
		"tab-1": newSessionForTest(claudePTY),
		"tab-2": newSessionForTest(codexPTY),
		"tab-3": newSessionForTest(opencodePTY),
		"tab-4": newSessionForTest(nodePTY),
	}
	want := map[string]model.Agent{
		"tab-1": model.AgentClaude,
		"tab-2": model.AgentCodex,
		"tab-3": model.AgentOpenCode,
		"tab-4": model.Agent("node"),
	}
	if got := sessionAgents(sessions); !reflect.DeepEqual(got, want) {
		t.Fatalf("sessionAgents() = %#v, want %#v", got, want)
	}
}

func TestSessionAgentsFollowNestedTerminals(t *testing.T) {
	// Kiro CLI's shell integration runs the shell in its own terminal: the
	// tab's foreground job is the wrapper (group 600) and the agent runs in
	// the foreground of the nested terminal (group 620). The tab's background
	// job (group 630) and a detached service (group 640) do not mark one.
	stubForeground(t, group(600), `600 1 600 600 zsh (kiro-cli-term)
610 600 610 620 /bin/zsh
620 610 620 620 kiro-cli
621 620 620 620 /Users/me/.local/bin/kiro-cli-chat
630 600 630 600 sleep 100
640 600 640 0 node service.js
`)

	got := sessionAgents(map[string]*session{"tab-1": newSessionForTest(new(os.File))})
	if want := map[string]model.Agent{"tab-1": model.AgentKiro}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sessionAgents() = %#v, want %#v", got, want)
	}
}

func TestForegroundJobStopsAtTheFirstAgent(t *testing.T) {
	// Codex runs its commands in nested terminals; the tab still shows Codex.
	processes := parseProcesses([]byte(`700 1 700 700 codex
710 700 710 720 /bin/bash
720 710 720 720 node server.js
`))
	if got := foregroundJob(processes, processAgents(processes), 700); got != 700 {
		t.Fatalf("foregroundJob() = %d, want 700", got)
	}
}

func TestForegroundJobKeepsAGroupWithoutNestedTerminal(t *testing.T) {
	processes := parseProcesses([]byte("800 1 800 800 /bin/zsh\n810 800 800 800 vim notes.md\n"))
	if got := foregroundJob(processes, processAgents(processes), 800); got != 800 {
		t.Fatalf("foregroundJob() = %d, want 800", got)
	}
}

func TestCommandAgentIgnoresKiroTerminalWrapper(t *testing.T) {
	for _, command := range []string{"kiro-cli-term", "/Users/me/.local/bin/kiro-cli-term --login"} {
		if got := commandAgent(command); got != "" {
			t.Fatalf("commandAgent(%q) = %q, want none", command, got)
		}
	}
}

func TestParseProcessesSkipsUnreadableRows(t *testing.T) {
	got := parseProcesses([]byte("garbage\n1 2 3\n101 1 101 -1   /bin/zsh  -l \nx 1 2 3 cmd\n"))
	want := []process{{pid: 101, parent: 1, group: 101, terminalGroup: -1, command: "/bin/zsh  -l"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseProcesses() = %#v, want %#v", got, want)
	}
}

func TestListProcessesTimesOut(t *testing.T) {
	testutil.Set(t, &processListTimeout, 20*time.Millisecond)
	testutil.Set(t, &runProcessList, func(ctx context.Context) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})

	result := make(chan error, 1)
	go func() {
		_, err := listProcesses()
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("listProcesses() error = %v, want deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("listProcesses() ignored its timeout")
	}
}
