package protocol

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"

	"github.com/nalbam/romty/internal/model"
)

const (
	ActionPing            = "ping"
	ActionSnapshot        = "snapshot"
	ActionAddRoot         = "add_root"
	ActionRemoveRoot      = "remove_root"
	ActionEnsureWorkspace = "ensure_workspace"
	ActionCreateTab       = "create_tab"
	ActionAttach          = "attach"
	ActionResize          = "resize"
	ActionShutdown        = "shutdown"
)

type Request struct {
	Action      string `json:"action"`
	Path        string `json:"path,omitempty"`
	RootID      string `json:"root_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	TabID       string `json:"tab_id,omitempty"`
	Columns     uint16 `json:"columns,omitempty"`
	Rows        uint16 `json:"rows,omitempty"`
}

type Response struct {
	Error     string           `json:"error,omitempty"`
	Snapshot  *model.Snapshot  `json:"snapshot,omitempty"`
	Workspace *model.Workspace `json:"workspace,omitempty"`
	Tab       *model.Tab       `json:"tab,omitempty"`
}

func Write(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode protocol message: %w", err)
	}
	data = append(data, '\n')
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write protocol message: %w", err)
	}
	return nil
}

func Read(r *bufio.Reader, value any) error {
	data, err := r.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read protocol message: %w", err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("decode protocol message: %w", err)
	}
	return nil
}
