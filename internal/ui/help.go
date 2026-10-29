// The shortcut reference. It is data first and rendering second, so what the
// modal shows can be checked against the key table it documents.

package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/opspresso/romty/internal/display"
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
		return m.scrollHelp(-wheelLines)
	case tea.MouseWheelDown:
		return m.scrollHelp(wheelLines)
	}
	return m, nil
}

func (m dashboard) maximumHelpOffset(height int) int {
	return max(len(m.helpEntries())-modalCapacity(height), 0)
}

// helpEntry is one line of the reference: a section heading, or a shortcut and
// the keys that run it. Keeping the keys as data rather than only as a rendered
// string lets the reference be checked against the key table it documents.
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
		{description: "Refresh, or restart a stopped daemon", keys: []string{"F5"}},
		{description: "Toggle scrollback", keys: []string{"F6", "Ctrl+Shift+\\"}},
		{description: "Toggle pane focus", keys: []string{"F7", "Ctrl+/"}},
		{section: "WORKSPACE", note: "workspace pane only"},
		{description: "Workspace actions", keys: []string{"F8"}},
		{description: "Stop daemon", keys: []string{"F9"}},
		{description: "About", keys: []string{"i"}},
		{description: "Focus terminal", keys: []string{"Tab"}},
		{description: "Collapse / expand root", keys: []string{"Space"}},
		{description: "Find a workspace", keys: []string{"/"}},
		{section: "SWITCH", note: "chord in any pane; letter in the workspace pane"},
		{description: "Jump to a waiting agent", keys: []string{"Ctrl+Shift+A", "a"}},
		{description: "New tab", keys: []string{"Ctrl+Shift+T", "t"}},
		{description: "Git actions", keys: []string{"Ctrl+Shift+G", "g"}},
		{description: "Toggle file view", keys: []string{"Ctrl+Shift+F", "f"}},
		{description: "Switch tab", keys: []string{"Ctrl+Shift+←/→"}},
		{description: "Move tab", keys: []string{"Ctrl+Shift+PgUp/PgDn", "</>"}},
		{description: "Switch workspace", keys: []string{"Ctrl+Shift+↑/↓"}},
		{section: "MOVE", note: "lists, output and file view"},
		{description: "Move one item / line", keys: []string{"↑/↓", "k/j"}},
		{description: "Tab; picker child/parent", keys: []string{"←/→", "h/l"}},
		{description: "Previous / next page", keys: []string{"PgUp/PgDn", "Ctrl+B/F"}},
		{description: "First / last item/line", keys: []string{"Home/End", "g/G"}},
		{description: "Enter / page scrollback", keys: []string{"Shift+PgUp/PgDn"}},
		{description: "Find in scrollback or Help", keys: []string{"/"}},
		{description: "Next / previous match", keys: []string{"n/N"}},
		{description: "Scroll Help/history/diff", keys: []string{"Wheel"}},
		{section: "FILE VIEW", note: "file tree, contents and diffs"},
		{description: "Collapse / expand folder", keys: []string{"←/→", "Enter"}},
		{description: "Toggle diff layout", keys: []string{"F6"}},
		{description: "Scroll content one line", keys: []string{"Ctrl+↑/↓"}},
		{section: "MOUSE", note: "dashboard chrome"},
		{description: "Open workspace or tab", keys: []string{"Click"}},
		{description: "Collapse / expand root", keys: []string{"Click ▾/▸"}},
		{description: "Rename tab", keys: []string{"Double-click tab"}},
		{description: "Scroll workspace list", keys: []string{"Wheel over tree"}},
		{description: "Resize workspace pane", keys: []string{"Drag divider"}},
		{section: "CONTEXT", note: "workspace, picker, Config and modals"},
		{description: "Activate / submit / toggle", keys: []string{"Enter"}},
		{description: "Close / cancel / leave", keys: []string{"Esc"}},
		{description: "Type a picker path", keys: []string{"/"}},
		{description: "Erase typed character", keys: []string{"Backspace"}},
		{description: "Adjust pane width", keys: []string{"←/→", "[/]"}},
		{description: "Toggle mouse passthrough", keys: []string{"p"}},
		{description: "Toggle scrollback mouse", keys: []string{"m"}},
		{description: "Toggle notifications", keys: []string{"n"}},
		{description: "Toggle keeping the machine awake", keys: []string{"w"}},
		{description: "Toggle programs copying (OSC 52)", keys: []string{"c"}},
		{description: "Toggle and hear agent sounds", keys: []string{"d", "b"}},
	}
}

// handleHelpSearchKey edits the Help filter while `/` has the keyboard. The
// list narrows with every key; Enter keeps the filter and gives the arrow
// keys back, and Esc drops it.
func (m dashboard) handleHelpSearchKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "enter":
		m.helpTyping = false
	case "esc":
		m.helpTyping, m.helpQuery = false, ""
	default:
		m.helpQuery = editText(m.helpQuery, message)
	}
	m.helpOffset = 0
	return m, nil
}

// filterHelp keeps the shortcuts whose description or keys contain query,
// ignoring case, and the headings of the sections they are in.
func filterHelp(reference []helpEntry, query string) []helpEntry {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return reference
	}
	matches := func(entry helpEntry) bool {
		if strings.Contains(strings.ToLower(entry.description), query) {
			return true
		}
		for _, key := range entry.keys {
			if strings.Contains(strings.ToLower(key), query) {
				return true
			}
		}
		return false
	}
	result := make([]helpEntry, 0, len(reference))
	var section *helpEntry
	for index, entry := range reference {
		if entry.isSection() {
			section = &reference[index]
			continue
		}
		if !matches(entry) {
			continue
		}
		if section != nil {
			result = append(result, *section)
			section = nil
		}
		result = append(result, entry)
	}
	return result
}

func (m dashboard) helpEntries() []string {
	reference := filterHelp(helpReference(), m.helpQuery)
	lines := make([]string, 0, len(reference)+1)
	if m.helpQuery == "" {
		lines = append(lines, m.styles.modalStrong.Render("romty")+
			m.styles.empty.Render("  "+version.String())+
			m.styles.modalBody.Render("  "+tagline))
	} else if len(reference) == 0 {
		lines = append(lines, m.styles.empty.Render("No shortcut matches "+display.Text(m.helpQuery)))
	}
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
