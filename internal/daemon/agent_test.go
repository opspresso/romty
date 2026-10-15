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
	output := []byte(`  101 /bin/zsh
  202 /Users/me/.local/bin/claude
  303 node /usr/local/lib/node_modules/@openai/codex/bin/codex.js
  404 vim codex-notes.md
  505 /opt/homebrew/bin/opencode
  606 node /home/me/scripts/opencode-notes.js
  707 bun /usr/local/bin/opencode
`)
	want := map[int]model.Agent{
		202: model.AgentClaude,
		303: model.AgentCodex,
		505: model.AgentOpenCode,
		606: model.Agent("node"),
		707: model.AgentOpenCode,
	}

	if got := processAgents(output); !reflect.DeepEqual(got, want) {
		t.Fatalf("processAgents() = %#v, want %#v", got, want)
	}
}

func TestProcessAgentsRecogniseNativeAgentExecutables(t *testing.T) {
	output := []byte(`  101 /opt/homebrew/bin/claude-code --resume
  202 /Users/me/.cache/codex-aarch64-apple-darwin
  303 /opt/homebrew/bin/opencode --continue
  404 kiro-cli
  404 /Users/me/.local/bin/kiro-cli-chat
  505 vim kiro-notes.md
`)
	want := map[int]model.Agent{
		101: model.AgentClaude,
		202: model.AgentCodex,
		303: model.AgentOpenCode,
		404: model.AgentKiro,
	}

	if got := processAgents(output); !reflect.DeepEqual(got, want) {
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
			"101 node wrapper.js\n101 " + string(agent) + "\n",
			"101 " + string(agent) + "\n101 node wrapper.js\n",
		} {
			if got := processAgents([]byte(output))[101]; got != agent {
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
		"101 claude\n202 codex\n303 opencode\n404 node server.js\n505 node background.js\n")

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
