package ui

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// browser is the root picker's state: the directory it has open, the
// directories inside it, and where the cursor sits among them.
type browser struct {
	path    string
	entries []string
	cursor  int
	// failure explains a directory romty could not read, in the picker itself.
	// The status bar cannot carry it: while the picker is open that row is
	// showing the picker's own shortcuts.
	failure string
}

// userHomeDirectory is where the picker opens. A home romty cannot resolve
// still gives a usable picker, because the cursor walks from wherever it lands.
func userHomeDirectory() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}

// startBrowse opens the picker at the home directory. It starts there every
// time rather than where it was left, so F2 lands somewhere predictable.
func (m dashboard) startBrowse() (tea.Model, tea.Cmd) {
	m.browse = openBrowser(m.homePath)
	return m.openModal(browseModal)
}

func openBrowser(path string) browser {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return browser{path: path, failure: err.Error()}
	}
	value := browser{path: filepath.Clean(absolute)}
	entries, err := readSubdirectories(value.path)
	if err != nil {
		value.failure = failureReason(err)
		return value
	}
	value.entries = entries
	return value
}

// readSubdirectories lists the directories a root could be picked from.
// Dot-directories are left out: home holds dozens of them and none of them is
// a workspace. Typing a path still reaches those.
func readSubdirectories(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if !isDirectory(path, entry) {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

// isDirectory follows a symlink before deciding. A root is stored by its
// resolved path anyway, and a linked directory — a checkout on another volume,
// say — is exactly the kind of place a root is picked from.
func isDirectory(parent string, entry os.DirEntry) bool {
	if entry.IsDir() {
		return true
	}
	if entry.Type()&os.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(filepath.Join(parent, entry.Name()))
	return err == nil && info.IsDir()
}

// failureReason drops the path a read error repeats. The box already shows
// which directory is open, and the reason is at the end of the message — which
// is the half a narrow box drops.
func failureReason(err error) string {
	var pathError *fs.PathError
	if errors.As(err, &pathError) {
		return pathError.Err.Error()
	}
	return err.Error()
}

func (b *browser) moveCursor(delta int) {
	if len(b.entries) == 0 {
		return
	}
	b.cursor = min(max(b.cursor+delta, 0), len(b.entries)-1)
}

// selectEntry puts the cursor on a named directory, so stepping out of one
// lands back on it rather than at the top of its parent.
func (b *browser) selectEntry(name string) {
	for index, entry := range b.entries {
		if entry == name {
			b.cursor = index
			return
		}
	}
}

func (b browser) selected() (string, bool) {
	if b.cursor < 0 || b.cursor >= len(b.entries) {
		return "", false
	}
	return filepath.Join(b.path, b.entries[b.cursor]), true
}

func (m dashboard) handleBrowseKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "up", "k":
		m.browse.moveCursor(-1)
	case "down", "j":
		m.browse.moveCursor(1)
	case "right", "l":
		return m.openBrowseSelection()
	case "left", "h":
		return m.browseParent()
	case "enter":
		return m.addBrowseSelection()
	case "/":
		// Walking to a distant path is slow and a pasted one cannot be walked
		// at all, so the prompt romty always had stays one key away.
		return m.startRootInput()
	}
	return m, nil
}

func (m dashboard) openBrowseSelection() (tea.Model, tea.Cmd) {
	path, ok := m.browse.selected()
	if !ok {
		return m, nil
	}
	m.browse = openBrowser(path)
	return m, nil
}

func (m dashboard) browseParent() (tea.Model, tea.Cmd) {
	parent := filepath.Dir(m.browse.path)
	if parent == m.browse.path {
		return m, nil
	}
	child := filepath.Base(m.browse.path)
	m.browse = openBrowser(parent)
	m.browse.selectEntry(child)
	return m, nil
}

// addBrowseSelection adds the highlighted directory. One with no
// subdirectories to highlight adds itself, so a walk that ends in an empty
// directory is not a dead end.
func (m dashboard) addBrowseSelection() (tea.Model, tea.Cmd) {
	path, ok := m.browse.selected()
	if !ok {
		if m.browse.failure != "" {
			return m, nil
		}
		path = m.browse.path
	}
	m.modal = noModal
	return m, m.addRoot(path)
}

// renderBrowseModal windows the directory list around the cursor so the box
// always fits the body, the way the help modal windows its shortcuts.
func (m dashboard) renderBrowseModal(width, height int) []string {
	contentWidth := max(width-6, 0)
	lines := []string{m.styles.empty.Render(shortenPath(m.browse.path, contentWidth)), ""}
	// The path line and the blank under it take two of the box's rows.
	capacity := max(modalCapacity(height)-len(lines), 1)
	switch {
	case m.browse.failure != "":
		lines = append(lines, m.styles.errorText.Render(m.browse.failure))
	case len(m.browse.entries) == 0:
		lines = append(lines, m.styles.empty.Render("No subdirectories — Enter adds this one"))
	default:
		start := 0
		if len(m.browse.entries) > capacity {
			start = min(max(m.browse.cursor-capacity/2, 0), len(m.browse.entries)-capacity)
		}
		for index := start; index < len(m.browse.entries) && index-start < capacity; index++ {
			lines = append(lines, m.renderBrowseEntry(index, contentWidth))
		}
	}
	return modalBox(m.styles, width, "Add root", lines...)
}

func (m dashboard) renderBrowseEntry(index, width int) string {
	name := m.browse.entries[index]
	style := m.styles.modalBody
	indicator := "  "
	if index == m.browse.cursor {
		style = m.styles.navigationSelected
		indicator = "▌ "
	}
	label := pad(truncate(indicator+name, max(width-2, 1)), max(width-2, 0)) + " ▸"
	return style.Render(truncate(label, width))
}

// shortenPath keeps the end of a long path, which is the part that says where
// the picker is. truncate keeps the front, which is the part every path shares.
func shortenPath(path string, width int) string {
	if width <= 0 || lipgloss.Width(path) <= width {
		return path
	}
	return ansi.TruncateLeft(path, lipgloss.Width(path)-width+1, "…")
}
