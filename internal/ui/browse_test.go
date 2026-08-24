package ui

import (
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
	updated, _ := value.Update(key(tea.KeyF2, ""))
	result := updated.(dashboard)
	if result.modal != browseModal {
		t.Fatalf("F2 modal = %v, want the root picker", result.modal)
	}
	return result
}

func press(t *testing.T, value dashboard, message tea.KeyPressMsg) (dashboard, tea.Cmd) {
	t.Helper()
	updated, command := value.Update(message)
	return updated.(dashboard), command
}

func TestBrowsePickerListsDirectoriesOnly(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"alpha", "beta", ".hidden"} {
		if err := os.Mkdir(filepath.Join(home, name), 0o755); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
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

func TestBrowsePickerWalksInAndOut(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "alpha", "inner"), 0o755); err != nil {
		t.Fatalf("create tree: %v", err)
	}
	if err := os.Mkdir(filepath.Join(home, "beta"), 0o755); err != nil {
		t.Fatalf("create beta: %v", err)
	}

	value := browsing(t, &fakeBackend{}, home)
	value, _ = press(t, value, key('j', "j"))
	value, _ = press(t, value, key('k', "k"))
	value, command := press(t, value, key(tea.KeyRight, ""))
	if command != nil {
		t.Fatalf("→ command = %v, want none", command)
	}
	if value.browse.path != filepath.Join(home, "alpha") ||
		strings.Join(value.browse.entries, ",") != "inner" {
		t.Fatalf("after → = (path %q, entries %v), want alpha", value.browse.path, value.browse.entries)
	}

	// Stepping out lands on the directory just left, not at the top of home.
	value, _ = press(t, value, key(tea.KeyLeft, ""))
	if value.browse.path != home {
		t.Fatalf("after ← = %q, want %q", value.browse.path, home)
	}
	if selected, _ := value.browse.selected(); selected != filepath.Join(home, "alpha") {
		t.Fatalf("cursor after ← = %q, want alpha", selected)
	}
}

func TestBrowsePickerAddsHighlightedDirectory(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		if err := os.Mkdir(filepath.Join(home, name), 0o755); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

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

func TestBrowsePickerAddsTheOpenDirectoryWhenItHasNoChildren(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "alpha"), 0o755); err != nil {
		t.Fatalf("create alpha: %v", err)
	}

	backend := &fakeBackend{}
	value := browsing(t, backend, home)
	value, _ = press(t, value, key(tea.KeyRight, ""))
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
	if err := os.Mkdir(filepath.Join(home, "alpha"), 0o755); err != nil {
		t.Fatalf("create alpha: %v", err)
	}

	value := browsing(t, &fakeBackend{}, home)
	value, _ = press(t, value, key(tea.KeyRight, ""))
	value, _ = press(t, value, key(tea.KeyEscape, ""))
	value, _ = press(t, value, key(tea.KeyF2, ""))
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
