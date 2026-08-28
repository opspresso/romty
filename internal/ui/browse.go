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
//
// The cursor counts from the open directory itself, which is row zero: adding
// the directory you walked into is the reason to walk into it, and a row that
// is always there beats a key that only works when the listing is empty.
type browser struct {
	path    string
	entries []string
	cursor  int
	// loading is set while the directory is being read, which happens off the
	// update loop: a stale network mount can take tens of seconds to answer,
	// and romty must not stop drawing terminals until it does.
	loading bool
	// preferred is the directory the cursor should land on once the read
	// finishes, so stepping out of a directory lands back on it.
	preferred string
	// failure explains a directory romty could not read, in the picker itself.
	// The status bar cannot carry it: while the picker is open that row is
	// showing the picker's own shortcuts.
	failure string
}

// browserMsg carries a finished directory read back to the update loop.
type browserMsg struct {
	value browser
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
	model, _ := m.openModal(browseModal)
	value := model.(dashboard)
	return value.openDirectory(m.homePath, "")
}

// openDirectory shows the directory at once and reads it in the background.
// preferred names the entry the cursor should land on when the read lands.
func (m dashboard) openDirectory(path, preferred string) (tea.Model, tea.Cmd) {
	absolute := cleanPath(path)
	m.browse = browser{path: absolute, loading: true, preferred: preferred}
	return m, func() tea.Msg {
		return browserMsg{value: readDirectory(absolute)}
	}
}

// handleBrowserRead accepts a read only for the directory still on screen. A
// user who moves on before a slow directory answers would otherwise be thrown
// back into it.
func (m dashboard) handleBrowserRead(message browserMsg) (tea.Model, tea.Cmd) {
	if m.modal != browseModal || message.value.path != m.browse.path {
		return m, nil
	}
	value := message.value
	if m.browse.preferred != "" {
		value.selectEntry(m.browse.preferred)
	}
	m.browse = value
	return m, nil
}

func cleanPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return filepath.Clean(absolute)
}

func readDirectory(path string) browser {
	value := browser{path: path}
	entries, err := readSubdirectories(path)
	if err != nil {
		value.failure = failureReason(err)
		return value
	}
	value.entries = entries
	if len(entries) > 0 {
		// The open directory is row zero, but a walk is usually headed
		// further in, so the cursor starts on the first directory it could
		// walk into.
		value.cursor = 1
	}
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

// rows counts the open directory and everything inside it.
func (b browser) rows() int {
	return len(b.entries) + 1
}

func (b *browser) moveCursor(delta int) {
	b.cursor = min(max(b.cursor+delta, 0), b.rows()-1)
}

// selectEntry puts the cursor on a named directory, so stepping out of one
// lands back on it rather than on the parent's own row.
func (b *browser) selectEntry(name string) {
	for index, entry := range b.entries {
		if entry == name {
			b.cursor = index + 1
			return
		}
	}
}

// selected returns the highlighted path and whether it is a directory inside
// the open one, which is the only kind the picker can walk into.
func (b browser) selected() (string, bool) {
	if b.cursor <= 0 || b.cursor > len(b.entries) {
		return b.path, false
	}
	return filepath.Join(b.path, b.entries[b.cursor-1]), true
}

func (m dashboard) handleBrowseKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	page := max(m.browseCapacity()-1, 1)
	switch message.String() {
	case "up", "k":
		m.browse.moveCursor(-1)
	case "down", "j":
		m.browse.moveCursor(1)
	case "pgup", "ctrl+b":
		m.browse.moveCursor(-page)
	case "pgdown", "ctrl+f":
		m.browse.moveCursor(page)
	case "home", "g":
		m.browse.moveCursor(-m.browse.rows())
	case "end", "G":
		m.browse.moveCursor(m.browse.rows())
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

func (m dashboard) handleBrowseMouse(message tea.MouseMsg) (tea.Model, tea.Cmd, bool) {
	row, inside := m.modalContentRow(message.Mouse(), max(m.width, 40), m.dimensions().bodyHeight)
	if !inside {
		return m, nil, false
	}
	if wheel, ok := message.(tea.MouseWheelMsg); ok {
		switch wheel.Button {
		case tea.MouseWheelUp:
			m.browse.moveCursor(-3)
		case tea.MouseWheelDown:
			m.browse.moveCursor(3)
		default:
			return m, nil, false
		}
		return m, nil, true
	}
	click, ok := message.(tea.MouseClickMsg)
	if !ok || click.Button != tea.MouseLeft || row < 2 || m.browse.loading || m.browse.failure != "" {
		return m, nil, false
	}
	index, ok := m.browseIndexAtContentRow(row)
	if !ok {
		return m, nil, true
	}
	m.browse.cursor = index
	if index == 0 {
		updated, command := m.addBrowseSelection()
		return updated, command, true
	}
	updated, command := m.openBrowseSelection()
	return updated, command, true
}

func (m dashboard) browseIndexAtContentRow(row int) (int, bool) {
	if row < browseHeaderRows || m.browse.loading || m.browse.failure != "" {
		return 0, false
	}
	start, _ := m.browseWindow(m.dimensions().bodyHeight)
	index := start + row - browseHeaderRows
	return index, index >= 0 && index < m.browse.rows()
}

// browseHeaderRows are the path and the blank line the picker draws above its
// list, which is what separates a content row from an entry index.
const browseHeaderRows = 2

// browseWindow is the first entry the picker draws and how many of them fit.
// Drawing the list and deciding which entry a click landed on are the same
// question asked twice; answering it in two places is how a click starts
// opening the row above the one under the pointer.
func (m dashboard) browseWindow(height int) (start, capacity int) {
	capacity = max(modalCapacity(height)-browseHeaderRows, 1)
	if rows := m.browse.rows(); rows > capacity {
		start = min(max(m.browse.cursor-capacity/2, 0), rows-capacity)
	}
	return start, capacity
}

func (m dashboard) openBrowseSelection() (tea.Model, tea.Cmd) {
	path, inside := m.browse.selected()
	if !inside {
		return m, nil
	}
	return m.openDirectory(path, "")
}

func (m dashboard) browseParent() (tea.Model, tea.Cmd) {
	parent := filepath.Dir(m.browse.path)
	if parent == m.browse.path {
		return m, nil
	}
	return m.openDirectory(parent, filepath.Base(m.browse.path))
}

// addBrowseSelection adds the highlighted directory, which is the open one
// when the cursor is on its row.
func (m dashboard) addBrowseSelection() (tea.Model, tea.Cmd) {
	path, inside := m.browse.selected()
	if !inside && m.browse.failure != "" {
		return m, nil
	}
	m.modal = noModal
	return m, m.addRoot(path)
}

// browseCapacity is how many rows the picker shows at once, which is what a
// page key moves by.
func (m dashboard) browseCapacity() int {
	// The path line and the blank under it take two of the box's rows.
	return max(modalCapacity(m.dimensions().bodyHeight)-2, 1)
}

// renderBrowseModal windows the directory list around the cursor so the box
// always fits the body, the way the help modal windows its shortcuts.
func (m dashboard) renderBrowseModal(width, height int) []string {
	contentWidth := max(width-6, 0)
	lines := []string{m.styles.empty.Render(shortenPath(displayText(m.browse.path), contentWidth)), ""}
	start, capacity := m.browseWindow(height)
	switch {
	case m.browse.failure != "":
		lines = append(lines, m.styles.errorText.Render(displayText(m.browse.failure)))
	case m.browse.loading:
		frame := agentAnimationFrames[m.agentAnimationFrame%len(agentAnimationFrames)]
		lines = append(lines, m.styles.empty.Render(frame+" Reading…"))
	default:
		rows := m.browse.rows()
		for index := start; index < rows && index-start < capacity; index++ {
			lines = append(lines, m.renderBrowseRow(index, contentWidth))
		}
		if len(m.browse.entries) == 0 {
			lines = append(lines, "", m.styles.empty.Render("No subdirectories"))
		}
	}
	return modalBox(m.styles, width, "Add root", lines...)
}

// renderBrowseRow draws row zero as the open directory and the rest as what is
// inside it. Only the latter carry the marker that says they can be opened.
func (m dashboard) renderBrowseRow(index, width int) string {
	name := ".  this directory"
	marker := "  "
	if index > 0 {
		name = displayText(m.browse.entries[index-1])
		marker = " ▸"
	}
	style := m.styles.modalBody
	indicator := "  "
	if index == m.browse.cursor {
		style = m.styles.navigationSelected
		indicator = "▌ "
	} else if m.hover.kind == hoverBrowseRow && m.hover.index == index {
		style = m.styles.interactiveHover
	}
	label := pad(truncate(indicator+name, max(width-2, 1)), max(width-2, 0)) + marker
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
