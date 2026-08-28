// Scrollback: entering and leaving the retained output, moving the viewport
// through it, and finding text in it.

package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/opspresso/romty/internal/display"
)

// Scrollback mode is the only state where romty wants the wheel, and it takes
// it as the arrow keys the host's alternate scroll sends rather than by asking
// for mouse events, which would take the host's native drag selection away —
// the very thing scrollback exists to give you.
func (m dashboard) handleScrollbackKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.terminal == nil {
		m.stopScrollback()
		return m, nil
	}
	if m.searchMode {
		return m.handleSearchKey(message)
	}
	// Only while a search is standing: without one these are ordinary keys, and
	// an ordinary key in scrollback leaves it and reaches the shell.
	if step, ok := searchStep(message.String()); ok && len(m.searchMatches) > 0 {
		m.stepSearch(step)
		return m, nil
	}
	switch message.String() {
	case "esc":
		m.stopScrollback()
	case "up":
		m.scrollTerminal(1)
	case "down":
		m.scrollTerminal(-1)
	case "/":
		m.searchMode = true
		m.searchQuery = ""
		m.searchMatches, m.searchIndex = nil, 0
	default:
		m.stopScrollback()
		m.terminal.sendKey(message)
	}
	return m, nil
}

// searchStep is the direction a key walks the matches in, if it is one of the
// two that do. Positive is towards older output, the way scrollback scrolls.
func searchStep(name string) (int, bool) {
	switch name {
	case "n":
		return 1, true
	case "N":
		return -1, true
	}
	return 0, false
}

// handleSearchKey reads the find prompt. The query is applied on Enter rather
// than as it is typed: a search walks every retained line, and doing that on
// each keystroke would make typing into a full history stutter.
func (m dashboard) handleSearchKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "esc":
		m.searchMode = false
		m.searchQuery = ""
	case "enter":
		query := strings.TrimSpace(m.searchQuery)
		m.searchMode = false
		if query == "" {
			m.searchMatches, m.searchIndex = nil, 0
			return m, nil
		}
		m.searchMatches = m.terminal.searchLines(query)
		m.searchIndex = 0
		if len(m.searchMatches) == 0 {
			m.setNotice(terminalError, "no output matches "+display.Text(query))
			return m, nil
		}
		m.clearError(terminalError)
		// The newest match first: the line a user is looking for is usually the
		// most recent one, and scrollback opens at the newest output.
		m.searchIndex = len(m.searchMatches) - 1
		m.scrollToLine(m.searchMatches[m.searchIndex])
	default:
		m.searchQuery = editText(m.searchQuery, message)
	}
	return m, nil
}

// stepSearch moves to the next match, delta negative towards older output. It
// wraps, so a walk off either end continues rather than stopping silently.
func (m *dashboard) stepSearch(delta int) {
	if len(m.searchMatches) == 0 {
		return
	}
	count := len(m.searchMatches)
	m.searchIndex = ((m.searchIndex-delta)%count + count) % count
	m.scrollToLine(m.searchMatches[m.searchIndex])
}

// scrollToLine puts an absolute line into view, below the top row so the lines
// around it can be read too.
func (m *dashboard) scrollToLine(line int) {
	if m.terminal == nil {
		return
	}
	historyLen := m.terminal.scrollbackLen()
	// renderViewport draws from historyLen-offset downwards, so that is the
	// line the top row shows.
	m.scrollOffset = min(max(historyLen-line+m.dimensions().terminalHeight/2, 0), historyLen)
}

func (m dashboard) toggleScrollback() (tea.Model, tea.Cmd) {
	if m.scrollback {
		m.stopScrollback()
		return m, nil
	}
	return m.pageHistory(0)
}

func (m dashboard) pageHistory(pages int) (tea.Model, tea.Cmd) {
	if !m.scrollback && !m.startScrollback() {
		// An application that owns the screen pages its own output. Send it the
		// unmodified key, because that is what such applications bind.
		if pages != 0 && m.focus == terminalPane && m.terminal != nil && m.terminal.altScreen() {
			m.terminal.sendKey(pagingKey(pages))
			return m, nil
		}
		m.setNotice(terminalError, m.scrollbackUnavailable())
		return m, nil
	}
	m.scrollTerminal(pages * m.scrollbackPage())
	return m, nil
}

func pagingKey(pages int) tea.KeyPressMsg {
	if pages < 0 {
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown})
	}
	return tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp})
}

func (m *dashboard) startScrollback() bool {
	// Scrollback is a view of the terminal, and the file view is what is on
	// screen instead of it. Opening it there set a mode nothing drew: the file
	// view kept rendering, romty asked the host for alternate scroll on its
	// behalf, and closing the file view landed the user in a scrollback they
	// never asked for.
	if m.gitDiff.active || m.terminal == nil || m.terminal.scrollbackLen() == 0 {
		return false
	}
	m.scrollback = true
	m.modal = noModal
	m.clearAnyError()
	return true
}

// scrollbackUnavailable explains why there is nothing for scrollback mode to
// show, so an application that owns the screen is not mistaken for a bug.
func (m dashboard) scrollbackUnavailable() string {
	switch {
	case m.gitDiff.active:
		return "close the file view with Ctrl+Shift+F to scroll the terminal"
	case m.terminal == nil:
		return "open a terminal to scroll its output"
	case m.terminal.altScreen():
		return "the running application owns the screen; scroll inside it"
	default:
		return "no output has scrolled off this terminal yet"
	}
}

func (m *dashboard) stopScrollback() {
	m.scrollback = false
	m.scrollOffset = 0
	m.searchMode = false
	m.searchQuery = ""
	m.searchMatches, m.searchIndex = nil, 0
	// Copy mode fills the screen with the terminal, so leaving it lands in the
	// terminal rather than back in the workspace tree — including when
	// scrollback was opened from the tree, because the tree is not what was
	// on screen.
	m.focusTerminal()
}

// scrollTerminal moves the viewport by delta lines, positive towards the oldest
// output. Offset zero is the live screen.
func (m *dashboard) scrollTerminal(delta int) {
	if m.terminal == nil {
		return
	}
	m.scrollOffset = min(max(m.scrollOffset+delta, 0), m.terminal.scrollbackLen())
}

func (m dashboard) scrollbackPage() int {
	terminalHeight := m.dimensions().terminalHeight
	return max(terminalHeight-1, 1)
}

// scrollbackPosition reports how far back the viewport sits in the history.
func (m dashboard) scrollbackPosition() string {
	if m.terminal == nil {
		return "0/0"
	}
	return fmt.Sprintf("%d/%d", m.scrollOffset, m.terminal.scrollbackLen())
}
