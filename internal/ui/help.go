// The shortcut reference. It is data first and rendering second, so what the
// modal shows can be checked against the key table it documents.

package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/opspresso/romty/internal/version"
)

func (m dashboard) scrollHelp(delta int) (tea.Model, tea.Cmd) {
	bodyHeight := m.dimensions().bodyHeight
	m.helpOffset = min(max(m.helpOffset+delta, 0), m.maximumHelpOffset(bodyHeight))
	return m, nil
}

func (m dashboard) handleHelpMouse(message tea.MouseMsg) (tea.Model, tea.Cmd) {
	wheel, ok := message.(tea.MouseWheelMsg)
	if !ok {
		return m, nil
	}
	switch wheel.Button {
	case tea.MouseWheelUp:
		return m.scrollHelp(-3)
	case tea.MouseWheelDown:
		return m.scrollHelp(3)
	}
	return m, nil
}

func (m dashboard) maximumHelpOffset(height int) int {
	return max(len(m.helpEntries())-modalCapacity(height), 0)
}

// helpEntry is one line of the reference: a section heading, or a shortcut and
// the keys that run it. Keeping the keys as data rather than only as a rendered
// string lets the reference be checked against the key table it documents —
// counting rendered lines said nothing about whether a shortcut was documented
// at all.
type helpEntry struct {
	section     string
	note        string
	description string
	keys        []string
}

func (e helpEntry) isSection() bool {
	return e.section != ""
}

// helpReference is every line of the shortcut reference in the order the modal
// shows them.
func helpReference() []helpEntry {
	return []helpEntry{
		{section: "GLOBAL", note: "function keys both panes; other keys contextual"},
		{description: "Help", keys: []string{"F1", "?"}},
		{description: "Add root", keys: []string{"F2"}},
		{description: "Config", keys: []string{"F3", ","}},
		{description: "Quit", keys: []string{"F4", "Ctrl+C"}},
		{description: "Refresh workspaces/files", keys: []string{"F5"}},
		{description: "Toggle scrollback", keys: []string{"F6", "Ctrl+Shift+\\"}},
		{description: "Toggle pane focus", keys: []string{"F7", "Ctrl+/"}},
		{section: "WORKSPACE", note: "workspace pane only"},
		{description: "Workspace actions", keys: []string{"F8"}},
		{description: "Stop daemon", keys: []string{"F9"}},
		{description: "About", keys: []string{"i"}},
		{description: "Focus terminal", keys: []string{"Tab"}},
		{section: "SWITCH", note: "workspace and terminal context"},
		{description: "New tab", keys: []string{"Ctrl+Shift+T"}},
		{description: "Git actions", keys: []string{"Ctrl+Shift+G"}},
		{description: "Toggle file view", keys: []string{"Ctrl+Shift+F"}},
		{description: "Switch tab", keys: []string{"Ctrl+Shift+←/→"}},
		{description: "Switch workspace", keys: []string{"Ctrl+Shift+↑/↓"}},
		{description: "Jump to a waiting agent", keys: []string{"Ctrl+Shift+A"}},
		{section: "MOVE", note: "lists, output and file view"},
		{description: "Move one item / line", keys: []string{"↑/↓", "k/j"}},
		{description: "Tab; picker child/parent", keys: []string{"←/→", "h/l"}},
		{description: "Previous / next page", keys: []string{"PgUp/PgDn", "Ctrl+B/F"}},
		{description: "First / last item/line", keys: []string{"Home/End", "g/G"}},
		{description: "Enter / page scrollback", keys: []string{"Shift+PgUp/PgDn"}},
		{description: "Find in scrollback", keys: []string{"/"}},
		{description: "Next / previous match", keys: []string{"n/N"}},
		{description: "Scroll Help/history/diff", keys: []string{"Wheel"}},
		{section: "FILE DIFF", note: "changed file tree and diff"},
		{description: "Toggle diff layout", keys: []string{"F6"}},
		{description: "Scroll diff one line", keys: []string{"Ctrl+↑/↓"}},
		{section: "MOUSE", note: "dashboard chrome"},
		{description: "Open workspace or tab", keys: []string{"Click"}},
		{description: "Move workspace cursor", keys: []string{"Wheel over tree"}},
		{description: "Resize workspace pane", keys: []string{"Drag divider"}},
		{section: "CONTEXT", note: "workspace, picker, modals and prompts"},
		{description: "Activate / submit", keys: []string{"Enter"}},
		{description: "Close / cancel / leave", keys: []string{"Esc"}},
		{description: "Type a picker path", keys: []string{"/"}},
		{description: "Erase path character", keys: []string{"Backspace"}},
		{description: "Adjust pane width", keys: []string{"←/→", "[/]"}},
		{description: "Toggle scrollback mouse", keys: []string{"m"}},
		{description: "Toggle agent sounds", keys: []string{"d", "b"}},
		{description: "Test the done sound", keys: []string{"s"}},
	}
}

func (m dashboard) helpEntries() []string {
	reference := helpReference()
	lines := make([]string, 0, len(reference)+1)
	lines = append(lines, m.styles.modalStrong.Render("romty")+
		m.styles.empty.Render("  "+version.String())+
		m.styles.modalBody.Render("  "+tagline))
	for _, entry := range reference {
		if entry.isSection() {
			lines = append(lines, renderHelpSection(m.styles, entry.section, entry.note))
			continue
		}
		lines = append(lines, renderHelpShortcut(m.styles, entry.description, entry.keys...))
	}
	return lines
}

// renderHelpModal windows the shortcut list so the box always fits the body and
// stays terminated; the title carries the visible range when it has to scroll.
func (m dashboard) renderHelpModal(width, height int) []string {
	entries := m.helpEntries()
	capacity := modalCapacity(height)
	if len(entries) <= capacity {
		return modalBox(m.styles, width, "Help", entries...)
	}
	offset := min(max(m.helpOffset, 0), len(entries)-capacity)
	title := fmt.Sprintf("Help %d-%d/%d", offset+1, offset+capacity, len(entries))
	return modalBox(m.styles, width, title, entries[offset:offset+capacity]...)
}

func renderHelpSection(styles *uiStyles, title, note string) string {
	return styles.modalBorder.Render("── ") + styles.modalTitle.Render(title) + "  " + styles.empty.Render(note)
}

func renderHelpShortcut(styles *uiStyles, description string, keys ...string) string {
	keycaps := make([]string, 0, len(keys))
	for _, key := range keys {
		keycaps = append(keycaps, styles.shortcutKey.Render(" "+key+" "))
	}
	separator := styles.empty.Render(" or ")
	return pad(strings.Join(keycaps, separator), helpKeyColumnWidth) + styles.modalBody.Render(description)
}
