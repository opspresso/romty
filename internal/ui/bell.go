// Terminal bells and clipboard copies from guests. The open terminal's go
// straight to the host terminal; a bell in another tab is marked in the tree
// and announced, because that tab is not on screen to be heard.

package ui

import (
	"maps"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/display"
	"github.com/opspresso/romty/internal/model"
)

// terminalEffects passes on what the open terminal's live output asked of the
// host: its bell, and the text it copied, unless Config blocks copying.
func (m *dashboard) terminalEffects() tea.Cmd {
	if m.terminal == nil {
		return nil
	}
	bell, copies := m.terminal.takeEffects()
	commands := make([]tea.Cmd, 0, len(copies)+1)
	if bell {
		commands = append(commands, tea.Raw("\a"))
	}
	if !m.config.BlockClipboard && len(copies) > 0 {
		// The clipboard holds one value, and commands in a batch run in no
		// set order, so only the last copy is sent.
		commands = append(commands, tea.SetClipboard(copies[len(copies)-1]))
	}
	return tea.Batch(commands...)
}

// unseenBell reports a tab whose program rang the bell since the user last
// had it open. The open terminal's bell is heard, so it is never unseen.
func (m dashboard) unseenBell(tab model.Tab) bool {
	if tab.BellAt.IsZero() || m.terminal != nil && m.terminal.id == tab.ID {
		return false
	}
	return tab.BellAt.After(m.seenBells[tab.ID])
}

// noteBells records the bells in a new tree: the open tab's are seen, and a
// new bell in another tab is announced. The first tree after startup is the
// state romty found, so its bells are only recorded.
func (m *dashboard) noteBells() tea.Cmd {
	seen := maps.Clone(m.seenBells)
	if seen == nil {
		seen = make(map[string]time.Time)
	}
	known := make(map[string]time.Time)
	var announce *model.Tab
	for tab := range m.state.Tabs() {
		known[tab.ID] = tab.BellAt
		if m.terminal != nil && m.terminal.id == tab.ID {
			seen[tab.ID] = tab.BellAt
			continue
		}
		if m.knownBells != nil && tab.BellAt.After(m.knownBells[tab.ID]) && m.unseenBell(*tab) && announce == nil {
			value := *tab
			announce = &value
		}
	}
	m.seenBells, m.knownBells = seen, known
	if announce == nil || m.config.MuteNotifications {
		return nil
	}
	return tea.Raw(ansi.Notify("romty bell · " + display.Text(m.tabPlace(*announce))))
}

// markBellSeen records that the user opened tab.
func (m *dashboard) markBellSeen(tabID string) {
	seen := maps.Clone(m.seenBells)
	if seen == nil {
		seen = make(map[string]time.Time)
	}
	seen[tabID] = time.Now()
	m.seenBells = seen
}
