package codexstate

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/opspresso/romty/internal/model"
)

const sessionID = "01234567-89ab-7000-8000-123456789abc"

type fixture struct {
	mu             sync.Mutex
	status         string
	flags          []string
	turn           string
	outcome        string
	goal           string
	ids            []string
	missingGoal    bool
	unmaterialized bool
}

func fakeAppServer(t *testing.T) (string, *fixture) {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "r-codex-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	directory := filepath.Join(home, "app-server-control")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(directory, "app-server-control.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fixture{status: "idle", ids: []string{sessionID}}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/rpc" {
			t.Errorf("unexpected endpoint %s", req.URL.Path)
			return
		}
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var request struct {
				ID     uint64         `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := conn.ReadJSON(&request); err != nil {
				return
			}
			if request.Method == "initialized" {
				continue
			}
			f.mu.Lock()
			var result any
			var responseError any
			switch request.Method {
			case "initialize":
				result = map[string]any{}
			case "thread/loaded/list":
				result = map[string]any{"data": f.ids, "nextCursor": nil}
			case "thread/read":
				if request.Params["includeTurns"] != false {
					t.Error("conversation history was requested")
				}
				flags := f.flags
				if flags == nil {
					flags = []string{}
				}
				result = map[string]any{"thread": map[string]any{"id": request.Params["threadId"], "status": map[string]any{"type": f.status, "activeFlags": flags}}}
			case "thread/turns/list":
				if f.unmaterialized {
					responseError = map[string]any{"code": -32600, "message": "thread " + request.Params["threadId"].(string) + " is not materialized yet; thread/turns/list is unavailable before first user message"}
					break
				}
				if request.Params["limit"] != float64(1) || request.Params["itemsView"] != "notLoaded" || request.Params["sortDirection"] != "desc" {
					t.Errorf("unexpected turn query %+v", request.Params)
				}
				turns := []map[string]any{}
				if f.turn != "" {
					turns = append(turns, map[string]any{"id": f.turn, "status": f.outcome})
				}
				result = map[string]any{"data": turns}
			case "thread/goal/get":
				if f.missingGoal {
					result = map[string]any{}
					break
				}
				var goal any
				if f.goal != "" {
					goal = map[string]any{"status": f.goal, "objective": "private content must not leave the reader"}
				}
				result = map[string]any{"goal": goal}
			default:
				t.Errorf("non-read-only request %s", request.Method)
				f.mu.Unlock()
				return
			}
			response := map[string]any{"id": request.ID, "result": result}
			if responseError != nil {
				delete(response, "result")
				response["error"] = responseError
			}
			err = conn.WriteJSON(response)
			f.mu.Unlock()
			if err != nil {
				return
			}
		}
	})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return home, f
}

func TestReadLifecycleWithOrdinaryCodexSessionIdentity(t *testing.T) {
	home, f := fakeAppServer(t)
	r := NewReader()
	defer r.Close()
	prefix := SessionPrefix(sessionID[:29] + "... | project")
	read := func(want model.AgentPhase) State {
		t.Helper()
		states, err := r.Read(context.Background(), home, []string{prefix})
		if err != nil {
			t.Fatal(err)
		}
		got := states[prefix]
		if got.SessionID != sessionID || got.Phase != want {
			t.Fatalf("state = %+v, want %s", got, want)
		}
		data, _ := json.Marshal(got)
		if strings.Contains(string(data), "private content") {
			t.Fatal("reader retained goal content")
		}
		return got
	}
	read(model.AgentPhaseIdle)
	f.mu.Lock()
	f.unmaterialized = true
	f.mu.Unlock()
	if got := read(model.AgentPhaseIdle); got.TurnID != "" {
		t.Fatal("a fresh session fabricated a completed turn")
	}
	f.mu.Lock()
	f.unmaterialized = false
	f.mu.Unlock()
	set := func(status, turn, outcome, goal string, flags ...string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.status, f.turn, f.outcome, f.goal, f.flags = status, turn, outcome, goal, flags
	}
	set("active", "one", "inProgress", "")
	read(model.AgentPhaseWorking)
	set("active", "one", "inProgress", "", "waitingOnUserInput")
	read(model.AgentPhaseWaitingInput)
	set("active", "two", "inProgress", "", "waitingOnUserInput")
	read(model.AgentPhaseWaitingInput)
	set("active", "two", "inProgress", "", "waitingOnApproval")
	read(model.AgentPhaseWaitingApproval)
	set("idle", "two", "completed", "active")
	read(model.AgentPhaseBackground)
	set("idle", "two", "completed", "blocked")
	read(model.AgentPhaseStopped)
	set("idle", "two", "completed", "")
	read(model.AgentPhaseCompleted)
	set("idle", "three", "failed", "")
	if got := read(model.AgentPhaseError); got.TurnID != "three" {
		t.Fatal("reader reused an old turn")
	}
	set("idle", "four", "interrupted", "")
	read(model.AgentPhaseInterrupted)
	f.mu.Lock()
	f.missingGoal = true
	f.mu.Unlock()
	if _, err := r.Read(context.Background(), home, []string{prefix}); err == nil {
		t.Fatal("missing goal metadata was treated as a completed goal")
	}
	if err := os.Chmod(filepath.Join(home, "app-server-control", "app-server-control.sock"), 0o666); err != nil {
		t.Fatal(err)
	}
	r.Close()
	if _, err := r.Read(context.Background(), home, []string{prefix}); err == nil {
		t.Fatal("non-private socket accepted")
	}
}

func TestSessionIdentityDoesNotGuessFromDirectoriesOrAmbiguousPrefixes(t *testing.T) {
	for _, title := range []string{"Working in project", "project | Action required", sessionID + " | " + "fedcba98-7654-7000-8000-123456789abc"} {
		if got := SessionPrefix(title); got != "" {
			t.Fatalf("ambiguous title %q matched %q", title, got)
		}
	}
	if got := matchSession(sessionID[:29], []string{sessionID, sessionID[:29] + "0000000"}); got != "" {
		t.Fatalf("ambiguous shortened ID matched %q", got)
	}
	if got := matchSession("01234567", []string{sessionID}); got != "" {
		t.Fatalf("short identity matched %q", got)
	}
}
