package protocol

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/opspresso/romty/internal/model"
)

// Version is the protocol romty speaks. The daemon outlives the client binary
// — `brew upgrade` replaces romty while the old daemon keeps running — so a new
// client can meet an old daemon. Without a version that showed up as
// `unknown action "remove_root"`, or as a field silently missing from a reply.
const Version = 1

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
	Action string `json:"action"`
	// Version is what the client speaks. A daemon that predates the field
	// leaves it zero, which is how an old daemon is recognised.
	Version     int    `json:"version,omitempty"`
	Path        string `json:"path,omitempty"`
	RootID      string `json:"root_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	TabID       string `json:"tab_id,omitempty"`
	Columns     uint16 `json:"columns,omitempty"`
	Rows        uint16 `json:"rows,omitempty"`
	// Environment is the client's environment, sent with create_tab. The
	// daemon may have been started days ago from a different shell, so its
	// own environment is not the one the user is working in.
	Environment []string `json:"environment,omitempty"`
	// Shell is what the client wants to run, for the same reason.
	Shell string `json:"shell,omitempty"`
}

type Response struct {
	Error string `json:"error,omitempty"`
	// Version is what the daemon speaks, so a client can say which side is
	// out of date instead of reporting a puzzling missing field.
	Version   int              `json:"version,omitempty"`
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

// MaxMessageBytes bounds one framed message. Requests are a handful of short
// fields and responses a snapshot of a workspace tree, so this is far above any
// real message while still refusing a peer that never sends a newline.
const MaxMessageBytes = 8 << 20

func Read(r *bufio.Reader, value any) error {
	data, err := readLine(r)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("decode protocol message: %w", err)
	}
	return nil
}

// readLine reads one newline-framed message, refusing to buffer more than
// maxMessageBytes so a peer cannot grow the reader without end.
func readLine(r *bufio.Reader) ([]byte, error) {
	var message []byte
	for {
		chunk, err := r.ReadSlice('\n')
		message = append(message, chunk...)
		if len(message) > MaxMessageBytes {
			return nil, fmt.Errorf("protocol message exceeds %d bytes", MaxMessageBytes)
		}
		if err == nil {
			return message, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, fmt.Errorf("read protocol message: %w", err)
		}
	}
}
