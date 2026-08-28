package model

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
