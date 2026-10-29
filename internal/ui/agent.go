// Agent presence and phase: the markers the tree draws, the animation that
// keeps them moving, the sounds a transition plays, and the jump that reaches
// the agent waiting for an answer.

package ui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/display"
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
		tab.SetAgentStatus(statuses[tab.ID])
	}
	m.agentAnimationActive = m.hasAnimatedAgent()
}

// agentAlert is an agent transition worth telling the user about: a turn that
// finished or failed, or an agent that started waiting for an answer.
type agentAlert struct {
	kind  sound.Kind
	tab   model.Tab
	agent model.Agent
	phase model.AgentPhase
}

// agentAlerts lists the transitions between the statuses romty last heard and
// statuses, in the order the tree draws their tabs. It does not read the
// settings: each channel that reports an alert decides for itself whether it
// is switched on.
func (m dashboard) agentAlerts(statuses map[string]model.AgentStatus) []agentAlert {
	var alerts []agentAlert
	for tab := range m.state.Tabs() {
		status, ok := statuses[tab.ID]
		if !ok || !status.Agent.ReportsPhases() {
			continue
		}
		heard := m.heardAgents[tab.ID]
		changedTurn := status.TurnID != "" && heard.TurnID != "" && status.TurnID != heard.TurnID
		switch {
		case !status.Estimated && (heard.Phase != status.Phase || changedTurn) &&
			status.Agent == heard.Agent &&
			(heard.SessionID == "" || status.SessionID == heard.SessionID) &&
			heard.Phase != model.AgentPhaseUnknown && heard.Phase != "" &&
			(status.Phase == model.AgentPhaseCompleted || status.Phase == model.AgentPhaseError):
			alerts = append(alerts, agentAlert{kind: sound.Done, tab: *tab, agent: status.Agent, phase: status.Phase})
		case !heard.Phase.Waiting() && status.Phase.Waiting():
			alerts = append(alerts, agentAlert{kind: sound.Waiting, tab: *tab, agent: status.Agent, phase: status.Phase})
		}
	}
	return alerts
}

// soundForAgentTransitions is the sound the transitions to statuses play: the
// first alert whose sound is switched on.
func (m dashboard) soundForAgentTransitions(statuses map[string]model.AgentStatus) (sound.Kind, bool) {
	for _, alert := range m.agentAlerts(statuses) {
		if alert.kind == sound.Done && m.config.SoundOnDone || alert.kind == sound.Waiting && m.config.SoundOnWaiting {
			return alert.kind, true
		}
	}
	return "", false
}

// notifyAgentAlerts asks the terminal romty is drawn in to show a desktop
// notification (OSC 9) for the first alert. The sequence travels to the
// terminal on the user's own device, which is what makes it reach a user
// who connects over SSH: the sound alerts play on the host instead. A
// terminal that does not support OSC 9 ignores it.
func (m dashboard) notifyAgentAlerts(alerts []agentAlert) tea.Cmd {
	if m.config.MuteNotifications || len(alerts) == 0 {
		return nil
	}
	text := m.agentAlertText(alerts[0])
	if more := len(alerts) - 1; more > 0 {
		text += fmt.Sprintf(" (+%d more)", more)
	}
	if alerts[0].kind == sound.Waiting {
		text += " · press a in romty"
	}
	return tea.Raw(ansi.Notify(text))
}

// agentAlertText names the agent, what it needs, and where it runs, so the
// notification can be acted on without opening romty first.
func (m dashboard) agentAlertText(alert agentAlert) string {
	label := agentPhaseLabels[alert.phase]
	// The agent comes from the new status: the tab still carries the last
	// one, which is empty for an agent whose first report is the alert.
	if name := agentDisplayNames[alert.agent]; name != "" {
		label = name + ": " + label
	}
	if place := m.tabPlace(alert.tab); place != "" {
		label += " · " + place
	}
	return "romty " + display.Text(label)
}

// agentDisplayNames are the agents' product names, as a notification and the
// rail write them.
var agentDisplayNames = map[model.Agent]string{
	model.AgentClaude:   "Claude Code",
	model.AgentCodex:    "Codex",
	model.AgentOpenCode: "OpenCode",
	model.AgentKiro:     "Kiro",
	model.AgentNode:     "Node.js",
}

// tabPlace is the workspace a tab runs in and the tab's name, as
// "workspace / tab".
func (m dashboard) tabPlace(tab model.Tab) string {
	for _, item := range m.workspaceItems() {
		for _, candidate := range item.tabs {
			if candidate.ID == tab.ID {
				return item.workspace.Name + " / " + tab.Name
			}
		}
	}
	return tab.Name
}

// attention counts the running agents that need the user: waiting for an
// answer, waiting for an approval, or failed.
type attention struct {
	input, approval, failed, bells int
}

func (m dashboard) attention() attention {
	var result attention
	for tab := range m.state.Tabs() {
		if !tab.Running {
			continue
		}
		switch tab.AgentPhase {
		case model.AgentPhaseWaitingInput:
			result.input++
		case model.AgentPhaseWaitingApproval:
			result.approval++
		case model.AgentPhaseError:
			result.failed++
		}
		if m.unseenBell(*tab) {
			result.bells++
		}
	}
	return result
}

// summary is the count of each kind of attention in the marker the tree draws
// for it, such as "▲2 ■1", and empty when nothing needs the user.
func (a attention) summary() string {
	parts := make([]string, 0, 3)
	for _, part := range []struct {
		marker string
		count  int
	}{{"▲", a.input}, {"■", a.approval}, {"★", a.failed}, {"!", a.bells}} {
		if part.count > 0 {
			parts = append(parts, fmt.Sprintf("%s%d", part.marker, part.count))
		}
	}
	return strings.Join(parts, " ")
}

func (m dashboard) hasAnimatedAgent() bool {
	for tab := range m.state.Tabs() {
		if tab.Running && tab.AgentPhase.Busy() {
			return true
		}
	}
	return false
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
	// The agent a notification just named comes first: that is the one the
	// user read about and pressed a for.
	if alerted := slices.IndexFunc(stops, func(stop waitingAgentStop) bool {
		return stop.tabID == m.lastAlertTab && stop.tabID != m.selectedTabID()
	}); alerted >= 0 {
		next = stops[alerted]
	}
	m.lastAlertTab = ""
	if next.tabID == m.selectedTabID() {
		// The only agent waiting is the one already open. Reopening it would
		// tear the live terminal down and attach a new one in its place, so
		// move the keyboard to it instead.
		m.focusTerminal()
		return m, nil
	}
	save := m.revealWorkspace(next.path)
	m.tabIndex = next.tabIndex
	return m, tea.Batch(save, m.selectWorkspace())
}

// waitingAgentStop addresses one waiting terminal the way the tree addresses
// it, so opening it is the same move the cursor and Enter would make.
type waitingAgentStop struct {
	path     string
	tabIndex int
	tabID    string
}

// waitingAgentStops lists the waiting terminals in the order the tree draws
// them, so repeated presses walk the screen rather than a map order that
// changes between snapshots. A collapsed root does not hide its waiting
// terminals from the walk.
func (m dashboard) waitingAgentStops() []waitingAgentStop {
	stops := make([]waitingAgentStop, 0)
	for _, item := range m.workspaceItems() {
		for tabIndex, tab := range runningTabs(item.tabs) {
			if tab.AgentPhase.Waiting() {
				stops = append(stops, waitingAgentStop{
					path: item.workspace.Path, tabIndex: tabIndex, tabID: tab.ID,
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

// agentPhaseLabels is how the rail names each phase of the open terminal's
// agent.
var agentPhaseLabels = map[model.AgentPhase]string{
	model.AgentPhaseUnknown:         "Status unavailable",
	model.AgentPhaseThinking:        "Thinking",
	model.AgentPhaseWorking:         "Working",
	model.AgentPhasePlanning:        "Planning",
	model.AgentPhaseCompacting:      "Compacting",
	model.AgentPhaseBackground:      "Background work",
	model.AgentPhaseIdle:            "Ready",
	model.AgentPhaseStopped:         "Stopped · completion unconfirmed",
	model.AgentPhaseCompleted:       "Completed",
	model.AgentPhaseInterrupted:     "Interrupted",
	model.AgentPhaseWaitingInput:    "Needs your answer",
	model.AgentPhaseWaitingApproval: "Needs your approval",
	model.AgentPhaseError:           "Failed",
}

// agentStatusNote is what the rail says about the open terminal's agent: its
// phase and, when the agent recorded them, its token and cost readings.
func (m dashboard) agentStatusNote() string {
	tab, ok := m.openTab()
	if !ok || tab.Agent == "" {
		return ""
	}
	if tab.Agent == model.AgentNode {
		return "Node.js running"
	}
	label := agentPhaseLabels[tab.AgentPhase]
	if tab.AgentPhase == model.AgentPhaseUnknown && m.hooksSkipped[tab.Agent] {
		label += " · run romty hooks"
	}
	if ledger := m.agentLedger(); ledger != "" {
		if label == "" {
			return ledger
		}
		return label + "  " + ledger
	}
	return label
}

// railNote is the note in the shortcut rail: the open terminal's agent, or in
// the tree the tabs of the workspace under the cursor, whose markers are what
// the user is reading there.
func (m dashboard) railNote() string {
	if m.daemonDown {
		return "daemon stopped · F5 starts it"
	}
	if m.focus == leftPane && !m.scrollback {
		return m.cursorTabsNote()
	}
	return m.agentStatusNote()
}

// cursorTabsNote names each running tab of the workspace under the cursor and
// what runs in it, such as "1: Claude Code · Working  server: Node.js", so the
// markers on the row can be read without learning them first.
func (m dashboard) cursorTabsNote() string {
	tabs := m.navigationTabs()
	parts := make([]string, 0, len(tabs))
	for _, tab := range tabs {
		parts = append(parts, display.Text(tab.Name)+": "+tabActivity(tab))
	}
	return strings.Join(parts, "  ")
}

// tabActivity is what runs in a tab, in words: the agent and its phase, the
// Node.js service, or a plain shell.
func tabActivity(tab model.Tab) string {
	switch {
	case tab.Agent == "":
		return "shell"
	case tab.Agent == model.AgentNode:
		return agentDisplayNames[tab.Agent]
	default:
		return agentDisplayNames[tab.Agent] + " · " + agentPhaseLabels[tab.AgentPhase]
	}
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
