package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/model"
)

// browsing returns a dashboard with the root picker open on home, which is a
// directory the test owns rather than the one the user happens to have.
func browsing(t *testing.T, backend Backend, home string) dashboard {
	t.Helper()
	value := newDashboard(backend, model.Snapshot{})
	value.width = 120
	value.height = 40
	value.homePath = home
	updated, command := value.Update(key(tea.KeyF2, ""))
	result := updated.(dashboard)
	if result.modal != browseModal {
		t.Fatalf("F2 modal = %v, want the root picker", result.modal)
	}
	return load(t, result, command)
}

func press(t *testing.T, value dashboard, message tea.KeyPressMsg) (dashboard, tea.Cmd) {
	t.Helper()
	updated, command := value.Update(message)
	return updated.(dashboard), command
}

// load runs a pending directory read and delivers it, the way the program
// would. The picker reads off the update loop, so nothing is listed until the
// message lands.
func load(t *testing.T, value dashboard, command tea.Cmd) dashboard {
	t.Helper()
	if command == nil {
		t.Fatal("directory read command = nil")
	}
	message := commandMessage[browserMsg](t, command)
	updated, _ := value.Update(message)
	return updated.(dashboard)
}

// walk presses a key that opens another directory and delivers the read.
func walk(t *testing.T, value dashboard, message tea.KeyPressMsg) dashboard {
	t.Helper()
	value, command := press(t, value, message)
	return load(t, value, command)
}

func makeDirectories(t *testing.T, parent string, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := os.MkdirAll(filepath.Join(parent, name), 0o755); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
}

func TestBrowsePickerListsDirectoriesOnly(t *testing.T) {
	home := t.TempDir()
	makeDirectories(t, home, "alpha", "beta", ".hidden")
	if err := os.WriteFile(filepath.Join(home, "notes.txt"), nil, 0o644); err != nil {
		t.Fatalf("create file: %v", err)
	}

	value := browsing(t, &fakeBackend{}, home)
	if got := strings.Join(value.browse.entries, ","); got != "alpha,beta" {
		t.Fatalf("entries = %q, want the visible directories", got)
	}
	rendered := ansi.Strip(value.render())
	if !strings.Contains(rendered, "▌ alpha") || !strings.Contains(rendered, "beta") {
		t.Fatalf("the picker does not show the directories:\n%s", rendered)
	}
	if strings.Contains(rendered, "notes.txt") || strings.Contains(rendered, ".hidden") {
		t.Fatalf("the picker shows a file or a dot-directory:\n%s", rendered)
	}
}

func TestBrowsePickerOpensClickedDirectory(t *testing.T) {
	home := t.TempDir()
	makeDirectories(t, home, "alpha", "beta")
	value := browsing(t, &fakeBackend{}, home)
	left, top := value.modalGeometry(value.width, value.dimensions().bodyHeight).contentOrigin()

	updated, command := value.Update(tea.MouseClickMsg{X: left + 2, Y: top + 4, Button: tea.MouseLeft})
	value = updated.(dashboard)
	if command == nil || value.browse.path != filepath.Join(home, "beta") || !value.browse.loading {
		t.Fatalf("clicked directory = (path %q, loading %v, command %v)",
			value.browse.path, value.browse.loading, command)
	}
}

func TestBrowsePickerHighlightsHoveredDirectory(t *testing.T) {
	home := t.TempDir()
	makeDirectories(t, home, "alpha", "beta")
	value := browsing(t, &fakeBackend{}, home)
	left, top := value.modalGeometry(value.width, value.dimensions().bodyHeight).contentOrigin()
	before := value.render()

	updated, _ := value.Update(tea.MouseMotionMsg{X: left + 2, Y: top + 4})
	value = updated.(dashboard)
	if value.hover.kind != hoverBrowseRow || value.hover.index != 2 || value.render() == before {
		t.Fatalf("browse hover = %#v", value.hover)
	}
}

func TestBrowsePickerReplacesControlCharactersInNames(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 120
	value.height = 40
	value.modal = browseModal
	value.browse = browser{
		path:    "/tmp/name\x1b]8;;https://example.com\x07",
		entries: []string{"child\x1b]8;;https://example.com\x07\n"},
		cursor:  1,
	}

	rendered := value.render()
	if strings.Contains(rendered, "\x1b]8;;https://example.com") {
		t.Fatalf("root picker rendered a terminal control sequence:\n%s", rendered)
	}
	plain := ansi.Strip(rendered)
	for _, safe := range []string{
		"name�]8;;https://example.com�",
		"child�]8;;https://example.com��",
	} {
		if !strings.Contains(plain, safe) {
			t.Fatalf("root picker does not contain the safe replacement %q:\n%s", safe, plain)
		}
	}
}

// The open directory is a row of its own, so adding the directory you walked
// into does not depend on what it happens to contain.
func TestBrowsePickerAddsTheOpenDirectoryFromItsOwnRow(t *testing.T) {
	home := t.TempDir()
	makeDirectories(t, home, "alpha", "beta")

	backend := &fakeBackend{}
	value := browsing(t, backend, home)
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "this directory") {
		t.Fatalf("the picker has no row for the open directory:\n%s", rendered)
	}

	// The cursor starts on the first directory, because a walk is headed in.
	if selected, inside := value.browse.selected(); !inside || selected != filepath.Join(home, "alpha") {
		t.Fatalf("cursor starts on (%q, inside %v), want alpha", selected, inside)
	}

	value, _ = press(t, value, key(tea.KeyUp, ""))
	if selected, inside := value.browse.selected(); inside || selected != home {
		t.Fatalf("cursor above the first directory = (%q, inside %v), want the open directory", selected, inside)
	}
	// The open directory cannot be walked into; it is already open.
	if _, command := press(t, value, key(tea.KeyRight, "")); command != nil {
		t.Fatalf("→ on the open directory = %v, want no read", command)
	}

	value, command := press(t, value, key(tea.KeyEnter, ""))
	if command == nil {
		t.Fatal("add root command = nil")
	}
	if value.modal != noModal {
		t.Fatalf("modal after Enter = %v, want the picker closed", value.modal)
	}
	command()
	if backend.addedPath != home {
		t.Fatalf("added path = %q, want the open directory", backend.addedPath)
	}
}

func TestBrowsePickerWalksInAndOut(t *testing.T) {
	home := t.TempDir()
	makeDirectories(t, home, filepath.Join("alpha", "inner"), "beta")

	value := browsing(t, &fakeBackend{}, home)
	value, _ = press(t, value, key('j', "j"))
	value, _ = press(t, value, key('k', "k"))
	value = walk(t, value, key(tea.KeyRight, ""))
	if value.browse.path != filepath.Join(home, "alpha") ||
		strings.Join(value.browse.entries, ",") != "inner" {
		t.Fatalf("after → = (path %q, entries %v), want alpha", value.browse.path, value.browse.entries)
	}

	// Stepping out lands on the directory just left, not at the top of home.
	value = walk(t, value, key(tea.KeyLeft, ""))
	if value.browse.path != home {
		t.Fatalf("after ← = %q, want %q", value.browse.path, home)
	}
	if selected, _ := value.browse.selected(); selected != filepath.Join(home, "alpha") {
		t.Fatalf("cursor after ← = %q, want alpha", selected)
	}
}

func TestBrowsePickerAddsHighlightedDirectory(t *testing.T) {
	home := t.TempDir()
	makeDirectories(t, home, "alpha", "beta")

	backend := &fakeBackend{}
	value := browsing(t, backend, home)
	value, _ = press(t, value, key(tea.KeyDown, ""))
	value, command := press(t, value, key(tea.KeyEnter, ""))
	if command == nil {
		t.Fatal("add root command = nil")
	}
	if value.modal != noModal {
		t.Fatalf("modal after Enter = %v, want the picker closed", value.modal)
	}
	command()
	if backend.addedPath != filepath.Join(home, "beta") {
		t.Fatalf("added path = %q, want beta", backend.addedPath)
	}
}

func TestBrowsePickerAddsAnEmptyDirectoryItWalkedInto(t *testing.T) {
	home := t.TempDir()
	makeDirectories(t, home, "alpha")

	backend := &fakeBackend{}
	value := browsing(t, backend, home)
	value = walk(t, value, key(tea.KeyRight, ""))
	if len(value.browse.entries) != 0 {
		t.Fatalf("entries in an empty directory = %v, want none", value.browse.entries)
	}
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "No subdirectories") {
		t.Fatalf("the picker does not say the directory is empty:\n%s", rendered)
	}

	_, command := press(t, value, key(tea.KeyEnter, ""))
	if command == nil {
		t.Fatal("add root command = nil")
	}
	command()
	if backend.addedPath != filepath.Join(home, "alpha") {
		t.Fatalf("added path = %q, want the open directory", backend.addedPath)
	}
}

// The cursor stops at both ends. The wheel arrives as three arrow keys per
// notch, so a list that wrapped would jump to the other end mid-scroll.
func TestBrowsePickerStopsAtBothEnds(t *testing.T) {
	home := t.TempDir()
	makeDirectories(t, home, "alpha", "beta", "gamma")

	value := browsing(t, &fakeBackend{}, home)
	for range 5 {
		value, _ = press(t, value, key(tea.KeyUp, ""))
	}
	if value.browse.cursor != 0 {
		t.Fatalf("cursor after ↑ past the top = %d, want 0", value.browse.cursor)
	}
	for range 8 {
		value, _ = press(t, value, key(tea.KeyDown, ""))
	}
	if value.browse.cursor != value.browse.rows()-1 {
		t.Fatalf("cursor after ↓ past the end = %d, want %d", value.browse.cursor, value.browse.rows()-1)
	}
}

// A long listing takes pages and ends, the way scrollback does.
func TestBrowsePickerMovesByPageAndToTheEnds(t *testing.T) {
	home := t.TempDir()
	for index := range 60 {
		makeDirectories(t, home, string(rune('a'+index%26))+string(rune('a'+index/26)))
	}

	value := browsing(t, &fakeBackend{}, home)
	value.height = 20
	page := max(value.browseCapacity()-1, 1)
	value, _ = press(t, value, key(tea.KeyPgDown, ""))
	if value.browse.cursor != 1+page {
		t.Fatalf("cursor after PgDn = %d, want %d", value.browse.cursor, 1+page)
	}
	value, _ = press(t, value, key(tea.KeyPgUp, ""))
	if value.browse.cursor != 1 {
		t.Fatalf("cursor after PgUp = %d, want 1", value.browse.cursor)
	}
	value, _ = press(t, value, key(tea.KeyEnd, ""))
	if value.browse.cursor != value.browse.rows()-1 {
		t.Fatalf("cursor after End = %d, want the last row", value.browse.cursor)
	}
	value, _ = press(t, value, key(tea.KeyHome, ""))
	if value.browse.cursor != 0 {
		t.Fatalf("cursor after Home = %d, want the open directory", value.browse.cursor)
	}
}

// An unreadable directory belongs in the box, because the status bar is busy
// showing the picker's shortcuts while the picker is open.
func TestBrowsePickerReportsAnUnreadableDirectory(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads every directory")
	}
	home := t.TempDir()
	if err := os.Chmod(home, 0o000); err != nil {
		t.Fatalf("make home unreadable: %v", err)
	}
	t.Cleanup(func() { os.Chmod(home, 0o700) })

	backend := &fakeBackend{}
	value := browsing(t, backend, home)
	if value.browse.failure == "" {
		t.Fatal("failure = empty, want the read error")
	}
	if rendered := ansi.Strip(value.render()); !strings.Contains(rendered, "permission denied") {
		t.Fatalf("the picker hides the read error:\n%s", rendered)
	}

	// There is nothing to add, so Enter must not send the daemon a request.
	_, command := press(t, value, key(tea.KeyEnter, ""))
	if command != nil {
		t.Fatalf("Enter on an unreadable directory = %v, want no request", command)
	}
}

// A linked directory is exactly the kind of place a root is picked from: a
// checkout kept on another volume and linked into home.
func TestBrowsePickerFollowsLinkedDirectories(t *testing.T) {
	home := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(home, "linked")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(target, "missing"), filepath.Join(home, "broken")); err != nil {
		t.Fatalf("create broken symlink: %v", err)
	}

	value := browsing(t, &fakeBackend{}, home)
	if got := strings.Join(value.browse.entries, ","); got != "linked" {
		t.Fatalf("entries = %q, want the linked directory alone", got)
	}
}

// Directories are read off the update loop, so a slow one cannot stop romty
// from drawing — and its answer must not drag the user back once they moved on.
func TestBrowsePickerIgnoresAReadItHasLeftBehind(t *testing.T) {
	home := t.TempDir()
	makeDirectories(t, home, filepath.Join("alpha", "inner"))

	value := browsing(t, &fakeBackend{}, home)
	stale := value.browse.path
	value = walk(t, value, key(tea.KeyRight, ""))

	updated, _ := value.Update(browserMsg{value: browser{path: stale, entries: []string{"alpha"}}})
	value = updated.(dashboard)
	if value.browse.path != filepath.Join(home, "alpha") ||
		strings.Join(value.browse.entries, ",") != "inner" {
		t.Fatalf("a stale read replaced the open directory: (path %q, entries %v)",
			value.browse.path, value.browse.entries)
	}
}

func TestBrowsePickerHandsOffToTheTypedPrompt(t *testing.T) {
	value := browsing(t, &fakeBackend{}, t.TempDir())

	value, command := press(t, value, key('/', "/"))
	if !value.inputMode || value.modal != noModal || command != nil {
		t.Fatalf("/ result = (input %v, modal %v, command %v), want the typed prompt",
			value.inputMode, value.modal, command)
	}
}

func TestBrowsePickerStartsAtHomeEveryTime(t *testing.T) {
	home := t.TempDir()
	makeDirectories(t, home, "alpha")

	value := browsing(t, &fakeBackend{}, home)
	value = walk(t, value, key(tea.KeyRight, ""))
	value, _ = press(t, value, key(tea.KeyEscape, ""))
	value, command := press(t, value, key(tea.KeyF2, ""))
	value = load(t, value, command)
	if value.browse.path != home {
		t.Fatalf("reopened picker at %q, want %q", value.browse.path, home)
	}
}

func TestShortenPathKeepsTheEnd(t *testing.T) {
	for _, probe := range []struct {
		path  string
		width int
		want  string
	}{
		{path: "/home/user", width: 20, want: "/home/user"},
		{path: "/home/user/workspace", width: 10, want: "…workspace"},
		{path: "/home/user", width: 0, want: "/home/user"},
	} {
		if got := shortenPath(probe.path, probe.width); got != probe.want {
			t.Fatalf("shortenPath(%q, %d) = %q, want %q", probe.path, probe.width, got, probe.want)
		}
	}
}

// The picker draws a window of its entries and a click has to land in the same
// one. Two copies of that arithmetic drift, and the click starts opening a
// neighbouring row.
func TestBrowseClicksFollowTheScrolledWindow(t *testing.T) {
	home := t.TempDir()
	for index := range 40 {
		if err := os.Mkdir(filepath.Join(home, fmt.Sprintf("child-%02d", index)), 0o755); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}
	}
	value := browsing(t, &fakeBackend{}, home)
	// Deep enough into the list that the window has scrolled off the first row.
	value.browse.cursor = 30

	start, capacity := value.browseWindow(value.dimensions().bodyHeight)
	if start == 0 {
		t.Fatalf("window start = %d, want the list scrolled", start)
	}
	for row := range capacity {
		index, ok := value.browseIndexAtContentRow(row + browseHeaderRows)
		if !ok {
			break
		}
		if index != start+row {
			t.Fatalf("content row %d = index %d, want %d", row, index, start+row)
		}
	}

	// The entry a click on the first content row resolves to is the one drawn
	// there — the modal's own top border sits above the content.
	index, ok := value.browseIndexAtContentRow(browseHeaderRows)
	if !ok {
		t.Fatal("the first content row resolved to no entry")
	}
	screen := plainRows(value.renderBrowseModal(value.width, value.dimensions().bodyHeight))
	drawn := screen[1+browseHeaderRows]
	if name := value.browse.entries[index-1]; !strings.Contains(drawn, name) {
		t.Fatalf("first content row draws %q, want the clicked entry %q", drawn, name)
	}
}
