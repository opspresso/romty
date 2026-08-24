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

type Tab struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Running     bool   `json:"running"`
}

type Snapshot struct {
	Roots []RootView `json:"roots"`
}

type RootView struct {
	Root        Root            `json:"root"`
	Directories []WorkspaceView `json:"directories"`
}

type WorkspaceView struct {
	Workspace Workspace `json:"workspace"`
	Tabs      []Tab     `json:"tabs"`
}
