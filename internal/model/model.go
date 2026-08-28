// Package model is what the daemon and the TUI agree romty is made of:
// roots, the workspaces under them, the terminal tabs inside those, and the
// snapshot that carries the whole tree across the socket.
package model

import "iter"

type State struct {
	Roots      []Root      `json:"roots"`
	Workspaces []Workspace `json:"workspaces"`
	Tabs       []Tab       `json:"tabs"`
}

type Root struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type Workspace struct {
	ID     string `json:"id"`
	RootID string `json:"root_id"`
	Name   string `json:"name"`
	Path   string `json:"path"`
}

type Agent string

const (
	AgentClaude Agent = "claude"
	AgentCodex  Agent = "codex"
)

type AgentPhase string

const (
	AgentPhaseUnknown         AgentPhase = "unknown"
	AgentPhaseThinking        AgentPhase = "thinking"
	AgentPhaseWorking         AgentPhase = "working"
	AgentPhasePlanning        AgentPhase = "planning"
	AgentPhaseCompacting      AgentPhase = "compacting"
	AgentPhaseIdle            AgentPhase = "idle"
	AgentPhaseWaitingInput    AgentPhase = "waiting_input"
	AgentPhaseWaitingApproval AgentPhase = "waiting_approval"
	AgentPhaseBackground      AgentPhase = "background"
	AgentPhaseError           AgentPhase = "error"
)

type AgentStatus struct {
	Agent Agent      `json:"agent"`
	Phase AgentPhase `json:"phase"`
	// ContextTokens is what the agent's newest request carried into the model,
	// and CostUSD what the session has cost, both as the agent recorded them in
	// its own transcript. Zero means romty had nothing to read: the counters
	// are never estimated.
	ContextTokens int     `json:"context_tokens,omitempty"`
	CostUSD       float64 `json:"cost_usd,omitempty"`
}

type Tab struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspace_id"`
	Name        string     `json:"name"`
	Running     bool       `json:"running"`
	Agent       Agent      `json:"agent,omitempty"`
	AgentPhase  AgentPhase `json:"agent_phase,omitempty"`
	// AgentContextTokens and AgentCostUSD mirror the agent's own counters; see
	// AgentStatus.
	AgentContextTokens int     `json:"agent_context_tokens,omitempty"`
	AgentCostUSD       float64 `json:"agent_cost_usd,omitempty"`
}

type Snapshot struct {
	Revision uint64     `json:"revision"`
	Roots    []RootView `json:"roots"`
}

// Tabs yields every tab the snapshot holds, by address, a root's own tabs
// before those of the directories under it — the order the tree draws them.
//
// A snapshot keeps tabs at two levels, and four callers walked both by hand.
// Each of them had to remember that a root holds tabs of its own; one that
// forgets reads as working code that is simply blind to every terminal opened
// on a root itself.
func (s *Snapshot) Tabs() iter.Seq[*Tab] {
	return func(yield func(*Tab) bool) {
		for rootIndex := range s.Roots {
			root := &s.Roots[rootIndex]
			for index := range root.Tabs {
				if !yield(&root.Tabs[index]) {
					return
				}
			}
			for directoryIndex := range root.Directories {
				directory := &root.Directories[directoryIndex]
				for index := range directory.Tabs {
					if !yield(&directory.Tabs[index]) {
						return
					}
				}
			}
		}
	}
}

type RootView struct {
	Root Root  `json:"root"`
	Tabs []Tab `json:"tabs"`
	// Error explains why Directories is empty: a root can be unmounted,
	// deleted, or made unreadable while romty is running, and one such root
	// must not take the whole snapshot with it.
	Error       string          `json:"error,omitempty"`
	Directories []WorkspaceView `json:"directories"`
}

type WorkspaceView struct {
	Workspace Workspace `json:"workspace"`
	Tabs      []Tab     `json:"tabs"`
}
