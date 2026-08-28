// Answering one client request: the handshake that agrees a protocol revision,
// the table that routes an action, and the gates that let a shutdown finish
// what is in flight without admitting anything new.

package daemon

import (
	"bufio"
	"fmt"
	"net"
	"time"

	"github.com/opspresso/romty/internal/protocol"
)

func (s *Server) handle(connection net.Conn) {
	defer connection.Close()
	// A client that connects and never finishes a request would otherwise hold
	// a goroutine and a file descriptor for the daemon's whole life, and could
	// grow the read buffer without bound.
	if err := connection.SetReadDeadline(time.Now().Add(requestTimeout)); err != nil {
		return
	}
	reader := bufio.NewReader(connection)
	var request protocol.Request
	if err := protocol.Read(reader, &request); err != nil {
		_ = reply(connection, protocol.Response{Error: err.Error()})
		return
	}

	// Stamped with the version the client selected, not the daemon's own: a
	// client checks the version before it reads the error, so a reply carrying
	// the daemon's version would be refused for the mismatch it is reporting
	// and the sentence naming the remedy would never reach the user.
	if err := checkClientVersion(request); err != nil {
		_ = replyFor(connection, request, protocol.Response{Error: err.Error()})
		return
	}
	if err := checkClientCapability(request); err != nil {
		_ = replyFor(connection, request, protocol.Response{Error: err.Error()})
		return
	}

	// The request is in; what follows is either a long-lived attach or a short
	// reply, and neither wants the read deadline that bounded the handshake.
	if err := connection.SetReadDeadline(time.Time{}); err != nil {
		return
	}
	if request.Action == protocol.ActionAttach {
		select {
		case s.attachments <- struct{}{}:
			defer func() { <-s.attachments }()
		default:
			_ = replyFor(connection, request, protocol.Response{Error: "too many terminal attachments"})
			return
		}
		finish, ok := s.beginRequest(request.Action)
		if !ok {
			_ = replyFor(connection, request, protocol.Response{Error: "daemon is shutting down"})
			return
		}
		finish()
		s.handleAttach(connection, request)
		return
	}
	if request.Action == protocol.ActionShutdown {
		// Stop even when the acknowledgement cannot be delivered, so a client
		// that already timed out never leaves the daemon running unnoticed.
		s.beginShutdown()
		_ = reply(connection, protocol.Response{})
		return
	}
	_ = replyFor(connection, request, s.dispatch(request))
}

// checkClientVersion accepts a selected revision inside the supported range.
// Ping and shutdown stay version-exempt: one negotiates that range and the
// other remains the remedy even when no overlap exists.
func checkClientVersion(request protocol.Request) error {
	if protocol.VersionExempt(request.Action) ||
		request.Version >= protocol.MinimumVersion && request.Version <= protocol.Version {
		return nil
	}
	return fmt.Errorf("daemon supports protocol %d..%d but the client selected %d; %s",
		protocol.MinimumVersion, protocol.Version, request.Version, protocol.Remedy)
}

func checkClientCapability(request protocol.Request) error {
	var required string
	switch request.Action {
	case protocol.ActionAgents:
		required = protocol.CapabilityAgents
	case protocol.ActionAgentStatuses, protocol.ActionAgentEvent:
		required = protocol.CapabilityAgentStatus
	case protocol.ActionRemoveWorkspace:
		required = protocol.CapabilityRemoveWorkspace
	case protocol.ActionCloseTab:
		required = protocol.CapabilityCloseTab
	}
	if required == "" || protocol.HasCapability(requestCapabilities(request), required) {
		return nil
	}
	return fmt.Errorf("action %q requires capability %q; %s", request.Action, required, protocol.Remedy)
}

func requestCapabilities(request protocol.Request) []string {
	supported := protocol.CapabilitiesForVersion(request.Version)
	if request.MinVersion == 0 && len(request.Capabilities) == 0 {
		return supported
	}
	result := make([]string, 0, len(supported))
	for _, capability := range supported {
		if protocol.HasCapability(request.Capabilities, capability) {
			result = append(result, capability)
		}
	}
	return result
}

// reply is for requests without a selected revision, while replyFor echoes the
// negotiated one. Both bound the write so a client that stops reading cannot
// hold a daemon goroutine and file descriptor indefinitely.
func reply(connection net.Conn, response protocol.Response) error {
	response.Version = protocol.Version
	return writeResponse(connection, response)
}

func replyFor(connection net.Conn, request protocol.Request, response protocol.Response) error {
	response.Version = request.Version
	return writeResponse(connection, response)
}

func writeResponse(connection net.Conn, response protocol.Response) error {
	if err := connection.SetWriteDeadline(time.Now().Add(requestTimeout)); err != nil {
		return err
	}
	defer connection.SetWriteDeadline(time.Time{})
	return protocol.Write(connection, response)
}

func (s *Server) stopped() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

func (s *Server) dispatch(request protocol.Request) protocol.Response {
	finish, ok := s.beginRequest(request.Action)
	if !ok {
		return protocol.Response{Error: "daemon is shutting down"}
	}
	defer finish()

	switch request.Action {
	case protocol.ActionPing:
		return protocol.Response{
			MinVersion:   protocol.MinimumVersion,
			MaxVersion:   protocol.Version,
			Capabilities: protocol.CapabilitiesForVersion(protocol.Version),
		}
	case protocol.ActionSnapshot:
		return s.snapshotResponse()
	case protocol.ActionAgents:
		return protocol.Response{Agents: s.agents()}
	case protocol.ActionAgentStatuses:
		return protocol.Response{AgentStatuses: s.agentStatusesSnapshot()}
	case protocol.ActionAgentEvent:
		return s.recordAgentEvent(request.TabID, request.AgentEvent)
	case protocol.ActionAddRoot:
		return s.addRoot(request.Path)
	case protocol.ActionRemoveRoot:
		return s.removeRoot(request.RootID)
	case protocol.ActionRemoveWorkspace:
		return s.removeWorkspace(request.RootID, request.Path)
	case protocol.ActionEnsureWorkspace:
		return s.ensureWorkspace(request.RootID, request.Path)
	case protocol.ActionCreateTab:
		return s.createTab(request)
	case protocol.ActionCloseTab:
		return s.closeTab(request.TabID)
	case protocol.ActionResize:
		return s.resize(request.TabID, request.ClientID, request.Columns, request.Rows)
	default:
		return protocol.Response{Error: fmt.Sprintf("unknown action %q", request.Action)}
	}
}

func (s *Server) beginRequest(action string) (func(), bool) {
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	if !s.accepting {
		return nil, false
	}
	switch action {
	case protocol.ActionCreateTab, protocol.ActionCloseTab, protocol.ActionResize:
		s.mutations.Add(1)
		return s.mutations.Done, true
	default:
		return func() {}, true
	}
}

func (s *Server) beginMutation() (func(), bool) {
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	if !s.accepting {
		return nil, false
	}
	s.mutations.Add(1)
	return s.mutations.Done, true
}

func (s *Server) beginShutdown() {
	s.requestMu.Lock()
	if s.accepting {
		s.accepting = false
		s.mu.Lock()
		s.stopping = true
		s.mu.Unlock()
	}
	s.requestMu.Unlock()
	s.stopOnce.Do(func() { close(s.stop) })
}
