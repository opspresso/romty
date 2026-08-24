package daemon

import (
	"os"
	"reflect"
	"testing"

	"github.com/opspresso/romty/internal/model"
)

func TestProcessAgentsRecogniseClaudeCodeAndCodex(t *testing.T) {
	output := []byte(`  101 /bin/zsh
  202 /Users/me/.local/bin/claude
  303 node /usr/local/lib/node_modules/@openai/codex/bin/codex.js
  404 vim codex-notes.md
`)
	want := map[int]model.Agent{
		202: model.AgentClaude,
		303: model.AgentCodex,
	}

	if got := processAgents(output); !reflect.DeepEqual(got, want) {
		t.Fatalf("processAgents() = %#v, want %#v", got, want)
	}
}

func TestProcessAgentsRecogniseNativeAgentExecutables(t *testing.T) {
	output := []byte(`  101 /opt/homebrew/bin/claude-code --resume
  202 /Users/me/.cache/codex-aarch64-apple-darwin
`)
	want := map[int]model.Agent{
		101: model.AgentClaude,
		202: model.AgentCodex,
	}

	if got := processAgents(output); !reflect.DeepEqual(got, want) {
		t.Fatalf("processAgents() = %#v, want %#v", got, want)
	}
}

func TestSessionAgentsMatchTabsByForegroundProcessGroup(t *testing.T) {
	claudePTY := new(os.File)
	codexPTY := new(os.File)
	groups := map[*os.File]int{claudePTY: 101, codexPTY: 202}
	previousGroup := foregroundProcessGroup
	previousList := listProcesses
	foregroundProcessGroup = func(terminal *os.File) (int, error) { return groups[terminal], nil }
	listProcesses = func() ([]byte, error) {
		return []byte("101 claude\n202 codex\n"), nil
	}
	t.Cleanup(func() {
		foregroundProcessGroup = previousGroup
		listProcesses = previousList
	})

	sessions := map[string]*session{
		"tab-1": {pty: claudePTY},
		"tab-2": {pty: codexPTY},
	}
	want := map[string]model.Agent{
		"tab-1": model.AgentClaude,
		"tab-2": model.AgentCodex,
	}
	if got := sessionAgents(sessions); !reflect.DeepEqual(got, want) {
		t.Fatalf("sessionAgents() = %#v, want %#v", got, want)
	}
}
