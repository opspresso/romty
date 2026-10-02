package daemon

import (
	"context"
	"time"

	"github.com/opspresso/romty/internal/codexstate"
	"github.com/opspresso/romty/internal/model"
)

type codexStatusReader interface {
	Read(context.Context, string, []string) (map[string]codexstate.State, error)
	Close()
}

type codexReport struct {
	prefix string
	state  codexstate.State
}

func (s *Server) readCodexStatuses(sessions map[string]*session, agents map[string]model.Agent) map[string]codexReport {
	if s.codex == nil {
		return nil
	}
	type identity struct{ tab, prefix string }
	byHome := make(map[string][]identity)
	for tabID, agent := range agents {
		if agent != model.AgentCodex {
			continue
		}
		title, home := sessions[tabID].agentIdentity()
		prefix := codexstate.SessionPrefix(title)
		if prefix != "" && home != "" {
			byHome[home] = append(byHome[home], identity{tab: tabID, prefix: prefix})
		}
	}
	result := make(map[string]codexReport)
	s.mu.Lock()
	for home := range s.codexErrors {
		if _, active := byHome[home]; !active {
			delete(s.codexErrors, home)
		}
	}
	s.mu.Unlock()
	// Leave room for foreground-process detection inside the client's request
	// deadline, even when several tabs use different Codex homes.
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	for home, identities := range byHome {
		prefixes := make([]string, 0, len(identities))
		for _, identity := range identities {
			prefixes = append(prefixes, identity.prefix)
		}
		states, err := s.codex.Read(ctx, home, prefixes)
		s.mu.Lock()
		if s.codexErrors == nil {
			s.codexErrors = make(map[string]string)
		}
		if err != nil {
			if s.codexErrors[home] != err.Error() && s.logger != nil {
				s.logger.Printf("Codex status unavailable: %v", err)
			}
			s.codexErrors[home] = err.Error()
		} else {
			delete(s.codexErrors, home)
		}
		s.mu.Unlock()
		if err != nil {
			continue
		}
		for _, identity := range identities {
			if state, ok := states[identity.prefix]; ok {
				result[identity.tab] = codexReport{prefix: identity.prefix, state: state}
			}
		}
	}
	return result
}
