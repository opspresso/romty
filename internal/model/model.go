// Package model is what the daemon and the TUI agree romty is made of:
// roots, the workspaces under them, the terminal tabs inside those, and the
// snapshot that carries the whole tree across the socket.
package model

import (
	"iter"
	"time"
)

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
	AgentClaude   Agent = "claude"
	AgentCodex    Agent = "codex"
	AgentOpenCode Agent = "opencode"
	AgentKiro     Agent = "kiro"
	// AgentNode identifies a foreground Node.js service without agent lifecycle phases.
	AgentNode Agent = "node"
)

// ReportsPhases reports an agent whose hooks or runtime report turn phases.
// Node.js is detected as a foreground process only.
func (a Agent) ReportsPhases() bool {
	switch a {
	case AgentClaude, AgentCodex, AgentOpenCode, AgentKiro:
		return true
	}
	return false
}

// AgentSourceRuntime marks a status read from the agent's own runtime, such as
// the Codex app-server, rather than derived from hook events.
const AgentSourceRuntime = "runtime"

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
	AgentPhaseCompleted       AgentPhase = "completed"
	AgentPhaseInterrupted     AgentPhase = "interrupted"
	AgentPhaseStopped         AgentPhase = "stopped"
)

// Running reports a turn that is working without waiting for the user.
func (p AgentPhase) Running() bool {
	switch p {
	case AgentPhaseThinking, AgentPhaseWorking, AgentPhasePlanning, AgentPhaseCompacting:
		return true
	}
	return false
}

// Waiting reports a turn that stopped for the user's answer or approval.
func (p AgentPhase) Waiting() bool {
	return p == AgentPhaseWaitingInput || p == AgentPhaseWaitingApproval
}

// Busy reports work in progress, including background work after a turn
// ended. A busy phase is drawn as an animated marker.
func (p AgentPhase) Busy() bool {
	return p.Running() || p == AgentPhaseBackground
}

type AgentStatus struct {
	Agent Agent      `json:"agent"`
	Phase AgentPhase `json:"phase"`
	// Estimated phases come from terminal output. An estimated idle stops the
	// animation but does not establish that work completed.
	Estimated bool `json:"estimated,omitempty"`
	// Source identifies the lifecycle authority, not terminal activity.
	Source    string `json:"source,omitempty"`
	Active    bool   `json:"active,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	TurnID    string `json:"turn_id,omitempty"`
	// ContextTokens is what the agent's newest request carried into the model,
	// and CostUSD what the session has cost, both as the agent recorded them in
	// its own transcript. Zero means romty had nothing to read: the counters
	// are never estimated.
	ContextTokens int     `json:"context_tokens,omitempty"`
	CostUSD       float64 `json:"cost_usd,omitempty"`
}

type Tab struct {
	ID             string     `json:"id"`
	WorkspaceID    string     `json:"workspace_id"`
	Name           string     `json:"name"`
	Running        bool       `json:"running"`
	Agent          Agent      `json:"agent,omitempty"`
	AgentPhase     AgentPhase `json:"agent_phase,omitempty"`
	AgentSessionID string     `json:"agent_session_id,omitempty"`
	AgentTurnID    string     `json:"agent_turn_id,omitempty"`
	// AgentContextTokens and AgentCostUSD mirror the agent's own counters; see
	// AgentStatus.
	AgentContextTokens int     `json:"agent_context_tokens,omitempty"`
	AgentCostUSD       float64 `json:"agent_cost_usd,omitempty"`
}

// AgentStatus is the agent status the tab carries, the inverse of
// SetAgentStatus.
func (t Tab) AgentStatus() AgentStatus {
	return AgentStatus{
		Agent: t.Agent, Phase: t.AgentPhase, SessionID: t.AgentSessionID, TurnID: t.AgentTurnID,
		ContextTokens: t.AgentContextTokens, CostUSD: t.AgentCostUSD,
	}
}

// SetAgentStatus copies an agent status onto the tab it describes.
func (t *Tab) SetAgentStatus(status AgentStatus) {
	t.Agent = status.Agent
	t.AgentPhase = status.Phase
	t.AgentSessionID = status.SessionID
	t.AgentTurnID = status.TurnID
	t.AgentContextTokens = status.ContextTokens
	t.AgentCostUSD = status.CostUSD
}

type Snapshot struct {
	Revision uint64     `json:"revision"`
	Roots    []RootView `json:"roots"`
}

// Tabs yields every tab the snapshot holds, by address, a root's own tabs
// before those of the directories under it — the order the tree draws them.
//
// A snapshot keeps tabs at two levels: a root holds tabs of its own as well as
// those of its directories. Walking it through Tabs keeps a caller from
// missing the root's own terminals.
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
	// Closed lists the root's own closed sessions, newest first.
	Closed []ClosedSession `json:"closed,omitempty"`
}

type WorkspaceView struct {
	Workspace Workspace `json:"workspace"`
	Tabs      []Tab     `json:"tabs"`
	// Closed lists the workspace's closed sessions, newest first.
	Closed []ClosedSession `json:"closed,omitempty"`
}

// ClosedSession is a tab the user closed whose output and agent session the
// daemon kept, so the tab can be reopened with that output and agent.
type ClosedSession struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Agent    Agent     `json:"agent,omitempty"`
	ClosedAt time.Time `json:"closed_at"`
}
