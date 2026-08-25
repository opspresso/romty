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
}

type Tab struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspace_id"`
	Name        string     `json:"name"`
	Running     bool       `json:"running"`
	Agent       Agent      `json:"agent,omitempty"`
	AgentPhase  AgentPhase `json:"agent_phase,omitempty"`
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
