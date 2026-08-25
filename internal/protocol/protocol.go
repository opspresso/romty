package protocol

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/opspresso/romty/internal/model"
)

// Version and MinimumVersion bound the protocol revisions this binary can
// speak. Ping advertises the range so peers choose the highest overlap instead
// of requiring the same build on both sides.
const (
	Version        = 5
	MinimumVersion = 1
)

const (
	CapabilityAgents           = "agents"
	CapabilitySnapshotRevision = "snapshot_revision"
	CapabilityRemoveWorkspace  = "remove_workspace"
	CapabilityReplayBoundary   = "replay_boundary"
)

var capabilities = []struct {
	name  string
	since int
}{
	{name: CapabilityAgents, since: 2},
	{name: CapabilitySnapshotRevision, since: 3},
	{name: CapabilityRemoveWorkspace, since: 4},
	{name: CapabilityReplayBoundary, since: 5},
}

const (
	ActionPing            = "ping"
	ActionSnapshot        = "snapshot"
	ActionAgents          = "agents"
	ActionAddRoot         = "add_root"
	ActionRemoveRoot      = "remove_root"
	ActionRemoveWorkspace = "remove_workspace"
	ActionEnsureWorkspace = "ensure_workspace"
	ActionCreateTab       = "create_tab"
	ActionAttach          = "attach"
	ActionResize          = "resize"
	ActionShutdown        = "shutdown"
)

// VersionExempt identifies the handshake and remedy that must work even when
// the peers have no ordinary protocol revision in common.
func VersionExempt(action string) bool {
	switch action {
	case ActionPing, ActionShutdown:
		return true
	}
	return false
}

type Request struct {
	Action string `json:"action"`
	// Version is the selected revision for ordinary requests and the client's
	// maximum during ping. A peer from before versioning leaves it zero.
	Version      int      `json:"version,omitempty"`
	MinVersion   int      `json:"min_version,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Path         string   `json:"path,omitempty"`
	RootID       string   `json:"root_id,omitempty"`
	WorkspaceID  string   `json:"workspace_id,omitempty"`
	TabID        string   `json:"tab_id,omitempty"`
	Columns      uint16   `json:"columns,omitempty"`
	Rows         uint16   `json:"rows,omitempty"`
	// Environment is the client's environment, sent with create_tab. The
	// daemon may have been started days ago from a different shell, so its
	// own environment is not the one the user is working in.
	Environment []string `json:"environment,omitempty"`
	// Shell is what the client wants to run, for the same reason.
	Shell string `json:"shell,omitempty"`
}

type Response struct {
	Error string `json:"error,omitempty"`
	// Version echoes the request revision so clients that predate range
	// negotiation still see the exact version they expect.
	Version      int                    `json:"version,omitempty"`
	MinVersion   int                    `json:"min_version,omitempty"`
	MaxVersion   int                    `json:"max_version,omitempty"`
	Capabilities []string               `json:"capabilities,omitempty"`
	Snapshot     *model.Snapshot        `json:"snapshot,omitempty"`
	Agents       map[string]model.Agent `json:"agents,omitempty"`
	Workspace    *model.Workspace       `json:"workspace,omitempty"`
	Tab          *model.Tab             `json:"tab,omitempty"`
	// ReplayBytes is the exact initial terminal history that follows an attach
	// response. Anything after it is live output.
	ReplayBytes int `json:"replay_bytes,omitempty"`
}

// SelectVersion returns the highest revision both peers support.
func SelectVersion(firstMin, firstMax, secondMin, secondMax int) (int, bool) {
	selected := min(firstMax, secondMax)
	if selected < max(firstMin, secondMin) {
		return 0, false
	}
	return selected, true
}

// CapabilitiesForVersion supplies the feature set of a peer from before
// capability advertisement existed.
func CapabilitiesForVersion(version int) []string {
	var result []string
	for _, capability := range capabilities {
		if version >= capability.since {
			result = append(result, capability.name)
		}
	}
	return result
}

func HasCapability(capabilities []string, target string) bool {
	for _, capability := range capabilities {
		if capability == target {
			return true
		}
	}
	return false
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

// MaxReplayBytes bounds what an attach asks the client to allocate before it
// restores a terminal. The daemon currently retains at most 8 MiB plus a small
// mode preamble, leaving room for that contract to grow without trusting an
// arbitrary length from the socket.
const MaxReplayBytes = 16 << 20

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
