// Agent presence and phase: the markers the tree draws, the animation that
// keeps them moving, the sounds a transition plays, and the jump that reaches
// the agent waiting for an answer.
package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/sound"
)

// agentAnimationInterval is a variable so tests can execute batched animation
// commands without sleeping between frames.
var agentAnimationInterval = 120 * time.Millisecond

var agentRefreshInterval = 2 * time.Second

var agentAnimationFrames = [...]string{"◐", "◓", "◑", "◒"}

func soundAlert(kind sound.Kind) tea.Cmd {
	return func() tea.Msg {
		_ = playSound(kind)
		return soundPlayedMsg{kind: kind}
	}
}

func (m dashboard) refreshAgents() tea.Cmd {
	backend := m.backend
	return tea.Tick(agentRefreshInterval, func(time.Time) tea.Msg {
		value, err := backend.AgentStatuses()
		return agentSnapshotMsg{value: value, err: err}
	})
}

func animateAgentMarker() tea.Cmd {
	return tea.Tick(agentAnimationInterval, func(time.Time) tea.Msg {
		return agentAnimationMsg{}
	})
}

func (m *dashboard) updateAgents(statuses map[string]model.AgentStatus) {
	for tab := range m.state.Tabs() {
		applyAgentStatus(tab, statuses)
	}
	m.agentAnimationActive = m.hasAnimatedAgent()
}

func applyAgentStatus(tab *model.Tab, statuses map[string]model.AgentStatus) {
	status := statuses[tab.ID]
	tab.Agent = status.Agent
	tab.AgentPhase = status.Phase
	tab.AgentContextTokens = status.ContextTokens
	tab.AgentCostUSD = status.CostUSD
}

func (m dashboard) soundForAgentTransitions(statuses map[string]model.AgentStatus) (sound.Kind, bool) {
	changed := func(tab model.Tab) (sound.Kind, bool) {
		status, ok := statuses[tab.ID]
		if !ok || status.Agent != model.AgentClaude && status.Agent != model.AgentCodex {
			return "", false
		}
		if m.soundOnDone && animatedAgentPhase(tab.AgentPhase) &&
			(status.Phase == model.AgentPhaseIdle || status.Phase == model.AgentPhaseError) {
			return sound.Done, true
		}
		if m.soundOnWaiting && !waitingAgentPhase(tab.AgentPhase) && waitingAgentPhase(status.Phase) {
			return sound.Waiting, true
		}
		return "", false
	}
	for tab := range m.state.Tabs() {
		if kind, ok := changed(*tab); ok {
			return kind, true
		}
	}
	return "", false
}

func waitingAgentPhase(phase model.AgentPhase) bool {
	return phase == model.AgentPhaseWaitingInput || phase == model.AgentPhaseWaitingApproval
}

func (m dashboard) hasAnimatedAgent() bool {
	for tab := range m.state.Tabs() {
		if tab.Running && animatedAgentPhase(tab.AgentPhase) {
			return true
		}
	}
	return false
}

func animatedAgentPhase(phase model.AgentPhase) bool {
	switch phase {
	case model.AgentPhaseThinking, model.AgentPhaseWorking, model.AgentPhasePlanning,
		model.AgentPhaseCompacting, model.AgentPhaseBackground:
		return true
	default:
		return false
	}
}

// jumpToWaitingAgent opens the next terminal whose agent stopped to ask
// something. The tab markers and the optional sound already say that an agent
// is waiting; without this the user still has to walk the tree to find which
// one, which is the work a notification exists to save.
func (m dashboard) jumpToWaitingAgent() (tea.Model, tea.Cmd) {
	if m.modal != noModal {
		// A modal is a question waiting for an answer, and the jump would land
		// behind it.
		return m, nil
	}
	stops := m.waitingAgentStops()
	if len(stops) == 0 {
		m.setNotice(treeError, "no agent is waiting")
		return m, nil
	}
	next := nextWaitingStop(stops, m.selectedTabID())
	if next.tabID == m.selectedTabID() {
		// The only agent waiting is the one already open. Reopening it would
		// tear the live terminal down and attach a new one in its place, so
		// move the keyboard to it instead.
		m.focusTerminal()
		return m, nil
	}
	m.setNavigation(next.navIndex)
	m.tabIndex = next.tabIndex
	return m, m.selectWorkspace()
}

// waitingAgentStop addresses one waiting terminal the way the tree addresses
// it, so opening it is the same move the cursor and Enter would make.
type waitingAgentStop struct {
	navIndex int
	tabIndex int
	tabID    string
}

// waitingAgentStops lists the waiting terminals in the order the tree draws
// them, so repeated presses walk the screen rather than a map order that
// changes between snapshots.
func (m dashboard) waitingAgentStops() []waitingAgentStop {
	stops := make([]waitingAgentStop, 0)
	for navIndex, item := range m.navigationItems() {
		for tabIndex, tab := range runningTabs(item.tabs) {
			if waitingAgentPhase(tab.AgentPhase) {
				stops = append(stops, waitingAgentStop{
					navIndex: navIndex, tabIndex: tabIndex, tabID: tab.ID,
				})
			}
		}
	}
	return stops
}

// nextWaitingStop is the stop after the open terminal, wrapping at the end. A
// terminal that is not itself waiting leaves the walk at the first stop, which
// is where a user who was reading something else expects to land.
func nextWaitingStop(stops []waitingAgentStop, openTabID string) waitingAgentStop {
	for index, stop := range stops {
		if stop.tabID == openTabID {
			return stops[(index+1)%len(stops)]
		}
	}
	return stops[0]
}

// agentLedger is what the open terminal's agent has spent, as the agent's own
// transcript records it. It is empty when no agent is open, or when romty had
// nothing to read: the counters are reported, never estimated.
func (m dashboard) agentLedger() string {
	tab, ok := m.openTab()
	if !ok {
		return ""
	}
	parts := make([]string, 0, 2)
	if tab.AgentContextTokens > 0 {
		parts = append(parts, formatTokens(tab.AgentContextTokens)+" ctx")
	}
	if tab.AgentCostUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f", tab.AgentCostUSD))
	}
	return strings.Join(parts, "  ")
}

// formatTokens abbreviates a count so it keeps its width as it grows, the way
// an agent's own status line writes it.
func formatTokens(count int) string {
	switch {
	case count >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(count)/1_000_000)
	case count >= 1_000:
		return fmt.Sprintf("%dk", count/1_000)
	default:
		return fmt.Sprintf("%d", count)
	}
}
