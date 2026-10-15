// Package codexstate reads lifecycle metadata from Codex's local app-server.
// The TUI's thread-id title item binds a terminal to a thread without changing
// the command the user runs or inspecting conversation text.
package codexstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/opspresso/romty/internal/model"
)

// Codex shows a session ID in the terminal title either whole (36 characters)
// or cut after its fifth group to 29 characters followed by "...".
const (
	sessionIDLength     = 36
	shortSessionIDBytes = 29
)

// Bounds on what one Reader holds and accepts from an app-server.
const (
	maxConnections    = 16
	loadedPageSize    = 128
	maxLoadedSessions = 512
)

var titleID = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-(?:[0-9a-f]{12}|[0-9a-f]{5}\.\.\.)`)

// SessionPrefix returns the lower-case session ID, whole or shortened, that a
// terminal title shows. It returns "" when the title shows none or shows two
// different ones, so an ambiguous title binds no session.
func SessionPrefix(title string) string {
	matches := titleID.FindAllStringIndex(title, -1)
	prefix := ""
	for _, span := range matches {
		if span[0] > 0 && identifierByte(title[span[0]-1]) || span[1] < len(title) && identifierByte(title[span[1]]) {
			continue
		}
		candidate := strings.TrimSuffix(strings.ToLower(title[span[0]:span[1]]), "...")
		if prefix != "" && prefix != candidate {
			return ""
		}
		prefix = candidate
	}
	return prefix
}

// IsSessionID reports whether value is one whole, lower-case Codex session ID.
func IsSessionID(value string) bool {
	return len(value) == sessionIDLength && SessionPrefix(value) == value
}

func identifierByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_'
}

// Reader keeps one app-server connection per Codex home and reads session
// status through it. It is safe for concurrent use.
type Reader struct {
	mu          sync.Mutex
	connections map[string]*rpc
}

func NewReader() *Reader { return &Reader{connections: make(map[string]*rpc)} }

// Close closes every open app-server connection.
func (r *Reader) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, client := range r.connections {
		client.conn.Close()
	}
	clear(r.connections)
}

// Read returns the status of each loaded session that exactly one prefix
// matches, keyed by that prefix. It returns only uniquely matched sessions. An absent, ambiguous or
// unreachable session is not guessed from its directory or most recent file.
func (r *Reader) Read(ctx context.Context, home string, prefixes []string) (map[string]model.AgentStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	client := r.connections[home]
	if client == nil {
		var err error
		client, err = connect(ctx, home)
		if err != nil {
			return nil, err
		}
		if len(r.connections) >= maxConnections {
			for key, old := range r.connections {
				old.conn.Close()
				delete(r.connections, key)
			}
		}
		r.connections[home] = client
	}
	result, err := client.read(ctx, prefixes)
	if err != nil {
		client.conn.Close()
		delete(r.connections, home)
	}
	return result, err
}

type rpc struct {
	conn *websocket.Conn
	next uint64
}

type rpcError struct {
	method  string
	code    int
	message string
}

func (e *rpcError) Error() string { return fmt.Sprintf("Codex %s failed (%d)", e.method, e.code) }

type goalField struct {
	present bool
	value   *struct {
		Status string `json:"status"`
	}
}

func (g *goalField) UnmarshalJSON(data []byte) error {
	g.present = true
	return json.Unmarshal(data, &g.value)
}

func connect(ctx context.Context, home string) (*rpc, error) {
	path := filepath.Join(home, "app-server-control", "app-server-control.sock")
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("Codex socket is not private to the current user")
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	parentOwner, ok := parent.Sys().(*syscall.Stat_t)
	if !ok || parentOwner.Uid != uint32(os.Geteuid()) || parent.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("Codex socket directory can be replaced by another user")
	}
	dialer := websocket.Dialer{HandshakeTimeout: time.Second, NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	conn, response, err := dialer.DialContext(ctx, "ws://localhost/rpc", nil)
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(1 << 20)
	client := &rpc{conn: conn}
	var initialized struct{}
	if err := client.call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "romty", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}}, &initialized); err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.WriteJSON(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		conn.Close()
		return nil, err
	}
	return client, nil
}

func (c *rpc) call(ctx context.Context, method string, params any, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, _ := ctx.Deadline()
	c.conn.SetReadDeadline(deadline)
	c.conn.SetWriteDeadline(deadline)
	c.next++
	if err := c.conn.WriteJSON(map[string]any{"id": c.next, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		var response struct {
			ID     uint64          `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := c.conn.ReadJSON(&response); err != nil {
			return err
		}
		if response.ID != c.next {
			continue
		}
		if response.Error != nil {
			return &rpcError{method: method, code: response.Error.Code, message: response.Error.Message}
		}
		if len(response.Result) == 0 {
			return errors.New("Codex returned no result")
		}
		return json.Unmarshal(response.Result, out)
	}
}

func (c *rpc) read(ctx context.Context, prefixes []string) (map[string]model.AgentStatus, error) {
	var ids []string
	cursor := ""
	for {
		var page struct {
			Data       *[]string `json:"data"`
			NextCursor *string   `json:"nextCursor"`
		}
		params := map[string]any{"limit": loadedPageSize}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := c.call(ctx, "thread/loaded/list", params, &page); err != nil {
			return nil, err
		}
		if page.Data == nil {
			return nil, errors.New("Codex returned no loaded-session list")
		}
		ids = append(ids, (*page.Data)...)
		if len(ids) > maxLoadedSessions {
			return nil, errors.New("too many loaded Codex sessions to bind safely")
		}
		if page.NextCursor == nil {
			break
		}
		if *page.NextCursor == cursor {
			return nil, errors.New("Codex repeated its pagination cursor")
		}
		cursor = *page.NextCursor
	}
	result := make(map[string]model.AgentStatus)
	for _, prefix := range prefixes {
		if _, exists := result[prefix]; exists {
			continue
		}
		id := matchSession(prefix, ids)
		if id == "" {
			continue
		}
		state, err := c.readSession(ctx, id)
		if err != nil {
			return nil, err
		}
		result[prefix] = state
	}
	return result, nil
}

func matchSession(prefix string, ids []string) string {
	if len(prefix) != shortSessionIDBytes && len(prefix) != sessionIDLength {
		return ""
	}
	matched := ""
	for _, id := range ids {
		if !IsSessionID(id) || !strings.HasPrefix(id, prefix) {
			continue
		}
		if matched != "" && matched != id {
			return ""
		}
		matched = id
	}
	return matched
}

func (c *rpc) readSession(ctx context.Context, id string) (model.AgentStatus, error) {
	var thread struct {
		Thread struct {
			ID     string `json:"id"`
			Status struct {
				Type  string    `json:"type"`
				Flags *[]string `json:"activeFlags"`
			} `json:"status"`
		} `json:"thread"`
	}
	if err := c.call(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": false}, &thread); err != nil {
		return model.AgentStatus{}, err
	}
	if thread.Thread.ID != id {
		return model.AgentStatus{}, errors.New("Codex returned a different thread")
	}
	state := model.AgentStatus{Agent: model.AgentCodex, Source: "runtime", Phase: model.AgentPhaseUnknown, SessionID: id}
	switch thread.Thread.Status.Type {
	case "active":
		if thread.Thread.Status.Flags == nil {
			return model.AgentStatus{}, errors.New("Codex returned no active-state flags")
		}
		// An approval outranks a question. An unknown flag makes the
		// phase unknown rather than a guess.
		state.Phase, state.Active = model.AgentPhaseWorking, true
		for _, flag := range *thread.Thread.Status.Flags {
			switch flag {
			case "waitingOnApproval":
				state.Phase = model.AgentPhaseWaitingApproval
			case "waitingOnUserInput":
				if state.Phase != model.AgentPhaseWaitingApproval {
					state.Phase = model.AgentPhaseWaitingInput
				}
			default:
				state.Phase = model.AgentPhaseUnknown
				return state, nil
			}
		}
	case "systemError":
		state.Phase = model.AgentPhaseError
	case "idle":
		state.Phase = model.AgentPhaseIdle
	default:
		return state, nil
	}
	if state.Active || state.Phase == model.AgentPhaseError {
		return state, nil
	}
	var turns struct {
		Data *[]struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := c.call(ctx, "thread/turns/list", map[string]any{"threadId": id, "limit": 1, "sortDirection": "desc", "itemsView": "notLoaded"}, &turns); err != nil {
		// Codex 0.160 creates history on the first user message. This exact
		// response confirms a fresh idle session, not a completed turn.
		var failure *rpcError
		if errors.As(err, &failure) && failure.code == -32600 && failure.message == "thread "+id+" is not materialized yet; thread/turns/list is unavailable before first user message" {
			return state, nil
		}
		return model.AgentStatus{}, err
	}
	if turns.Data == nil {
		return model.AgentStatus{}, errors.New("Codex returned no turn metadata")
	}
	if len(*turns.Data) > 0 {
		state.TurnID = (*turns.Data)[0].ID
		if state.TurnID == "" || len(state.TurnID) > 512 {
			return model.AgentStatus{}, errors.New("Codex returned no valid turn identity")
		}
		switch (*turns.Data)[0].Status {
		case "completed":
			state.Phase = model.AgentPhaseCompleted
		case "interrupted":
			state.Phase = model.AgentPhaseInterrupted
		case "failed":
			state.Phase = model.AgentPhaseError
		case "inProgress":
			state.Phase, state.Active = model.AgentPhaseWorking, true
		default:
			state.Phase = model.AgentPhaseUnknown
		}
	}
	var goal struct {
		Goal goalField `json:"goal"`
	}
	if err := c.call(ctx, "thread/goal/get", map[string]any{"threadId": id}, &goal); err != nil {
		return model.AgentStatus{}, err
	}
	if !goal.Goal.present {
		return model.AgentStatus{}, errors.New("Codex returned no goal metadata")
	}
	if goal.Goal.value != nil {
		switch goal.Goal.value.Status {
		case "active", "paused", "blocked", "usageLimited", "budgetLimited", "complete":
		default:
			return model.AgentStatus{}, errors.New("Codex returned an unknown goal status")
		}
	}
	if goal.Goal.value != nil && !state.Active && (state.Phase == model.AgentPhaseIdle || state.Phase == model.AgentPhaseCompleted) {
		switch goal.Goal.value.Status {
		case "active":
			state.Phase, state.Active = model.AgentPhaseBackground, true
		case "paused", "blocked", "usageLimited", "budgetLimited":
			state.Phase = model.AgentPhaseStopped
		}
	}
	return state, nil
}
