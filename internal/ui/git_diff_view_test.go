package ui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/model"
)

func TestDashboardTogglesGitDiffViewForContextWorkspace(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace}},
	}}}
	previousFiles, previousDiff := loadGitChangedFiles, loadGitFileDiff
	t.Cleanup(func() { loadGitChangedFiles, loadGitFileDiff = previousFiles, previousDiff })
	loadGitChangedFiles = func(path string) ([]gitChangedFile, error) {
		if path != workspace.Path {
			t.Fatalf("changed files path = %q, want %q", path, workspace.Path)
		}
		return []gitChangedFile{
			{Path: "README.md", WorkTreeStatus: 'M', IndexStatus: ' '},
			{Path: "internal/ui/view.go", WorkTreeStatus: '?', IndexStatus: '?'},
		}, nil
	}
	loadGitFileDiff = func(path string, file gitChangedFile) (string, error) {
		if path != workspace.Path || file.Path != "README.md" {
			t.Fatalf("diff target = (%q, %q), want (%q, README.md)", path, file.Path, workspace.Path)
		}
		return "diff --git a/README.md b/README.md\n@@ -1 +1 @@\n-old\n+new", nil
	}

	value := newDashboard(&fakeBackend{}, snapshot)
	value.gitStates = map[string]gitState{workspace.Path: {Branch: "main", Dirty: true}}
	value.setNavigation(1)

	updated, filesCommand := value.Update(gitDiffViewKey())
	value = updated.(dashboard)
	if filesCommand == nil || !value.gitDiff.active || value.gitDiff.target.Path != workspace.Path || !value.gitDiff.filesPending {
		t.Fatalf("open diff view = (command %v, state %#v)", filesCommand, value.gitDiff)
	}
	updated, diffCommand := value.Update(filesCommand())
	value = updated.(dashboard)
	if diffCommand == nil || len(value.gitDiff.files) != 2 || value.gitDiff.fileIndex != 0 || !value.gitDiff.diffPending {
		t.Fatalf("changed files result = (command %v, state %#v)", diffCommand, value.gitDiff)
	}
	updated, _ = value.Update(diffCommand())
	value = updated.(dashboard)
	rendered := ansi.Strip(value.render())
	for _, fragment := range []string{"Changes · alpha", "README.md", "▾ internal/", "▾ ui/", "view.go", "Diff · README.md", "-old", "+new"} {
		if !strings.Contains(rendered, fragment) {
			t.Fatalf("Git diff view does not contain %q:\n%s", fragment, rendered)
		}
	}

	updated, command := value.Update(gitDiffViewKey())
	value = updated.(dashboard)
	if command != nil || value.gitDiff.active {
		t.Fatalf("close diff view = (command %v, state %#v)", command, value.gitDiff)
	}
}

func TestDashboardNavigatesAndScrollsGitDiffView(t *testing.T) {
	previousDiff := loadGitFileDiff
	t.Cleanup(func() { loadGitFileDiff = previousDiff })
	loadGitFileDiff = func(_ string, file gitChangedFile) (string, error) {
		return file.Path + "\n" + strings.Join([]string{"one", "two", "three", "four", "five", "six", "seven"}, "\n"), nil
	}
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 80
	value.height = 10
	value.gitDiff = gitDiffView{
		active: true,
		target: model.Workspace{Name: "alpha", Path: "/projects/alpha"},
		files: []gitChangedFile{
			{Path: "first.txt", WorkTreeStatus: 'M', IndexStatus: ' '},
			{Path: "second.txt", WorkTreeStatus: 'M', IndexStatus: ' '},
		},
		diffLines: []string{"first.txt"},
	}

	updated, command := value.Update(key(tea.KeyDown, ""))
	value = updated.(dashboard)
	if command == nil || value.gitDiff.fileIndex != 1 || !value.gitDiff.diffPending {
		t.Fatalf("Down = (command %v, state %#v)", command, value.gitDiff)
	}
	updated, _ = value.Update(command())
	value = updated.(dashboard)
	if value.gitDiff.diffPending || len(value.gitDiff.diffLines) == 0 || value.gitDiff.diffLines[0] != "second.txt" {
		t.Fatalf("second file diff = %#v", value.gitDiff)
	}

	updated, _ = value.Update(key(tea.KeyPgDown, ""))
	value = updated.(dashboard)
	if value.gitDiff.diffOffset == 0 {
		t.Fatalf("PgDown diff offset = %d, want scrolled", value.gitDiff.diffOffset)
	}
	updated, _ = value.Update(key(tea.KeyHome, ""))
	value = updated.(dashboard)
	if value.gitDiff.diffOffset != 0 {
		t.Fatalf("Home diff offset = %d, want 0", value.gitDiff.diffOffset)
	}
	updated, _ = value.Update(key(tea.KeyEnd, ""))
	value = updated.(dashboard)
	if value.gitDiff.diffOffset != value.maximumGitDiffOffset() {
		t.Fatalf("End diff offset = %d, want %d", value.gitDiff.diffOffset, value.maximumGitDiffOffset())
	}

}

func TestDashboardColorsChangedFilesByStatus(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.gitDiff = gitDiffView{
		active:    true,
		fileIndex: -1,
		files: []gitChangedFile{
			{Path: "added.txt", IndexStatus: 'A', WorkTreeStatus: ' '},
			{Path: "modified.txt", IndexStatus: ' ', WorkTreeStatus: 'M'},
			{Path: "deleted.txt", IndexStatus: 'D', WorkTreeStatus: ' '},
		},
	}

	rows := gitDiffTreeRows(value.gitDiff.files)
	rendered := make([]string, 0, len(rows))
	for _, row := range rows {
		rendered = append(rendered, value.renderGitDiffTreeRow(row, 30))
	}
	joined := strings.Join(rendered, "\n")
	for _, status := range []string{
		value.styles.diffAdded.Render("A "),
		value.styles.gitStatus.Render("M "),
		value.styles.diffRemoved.Render("D "),
	} {
		if !strings.Contains(joined, status) {
			t.Fatalf("file tree does not contain colored status %q:\n%q", status, joined)
		}
	}
}

func TestSplitGitDiffRowsPairRemovedAndAddedLines(t *testing.T) {
	rows := splitGitDiffRows([]string{
		"Staged changes",
		"diff --git a/file.txt b/file.txt",
		"@@ -1,3 +1,4 @@",
		" same",
		"-old one",
		"-old two",
		"+new one",
		"+new two",
		"+new three",
		" tail",
	})
	want := []gitSplitDiffRow{
		{full: "Staged changes"},
		{full: "diff --git a/file.txt b/file.txt"},
		{full: "@@ -1,3 +1,4 @@"},
		{left: " same", right: " same"},
		{left: "-old one", right: "+new one"},
		{left: "-old two", right: "+new two"},
		{right: "+new three"},
		{left: " tail", right: " tail"},
	}
	if len(rows) != len(want) {
		t.Fatalf("split rows = %#v, want %#v", rows, want)
	}
	for index := range want {
		if rows[index] != want[index] {
			t.Fatalf("split row %d = %#v, want %#v", index, rows[index], want[index])
		}
	}
}

func TestDashboardTogglesInlineAndSplitGitDiff(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 120
	value.height = 20
	value.gitDiff = gitDiffView{
		active:    true,
		target:    model.Workspace{Name: "alpha", Path: "/projects/alpha"},
		files:     []gitChangedFile{{Path: "file.txt", IndexStatus: ' ', WorkTreeStatus: 'M'}},
		diffLines: []string{"@@ -1 +1 @@", "-old", "+new"},
	}

	inline := ansi.Strip(value.render())
	if !strings.Contains(inline, "Diff · file.txt · inline") {
		t.Fatalf("inline title is missing:\n%s", inline)
	}
	updated, _ := value.Update(key('v', "v"))
	value = updated.(dashboard)
	split := ansi.Strip(value.render())
	if !value.gitDiff.split || !strings.Contains(split, "Diff · file.txt · split") ||
		!lineContainsInOrder(split, "-old", "│", "+new") {
		t.Fatalf("split view is incomplete:\n%s", split)
	}
	updated, _ = value.Update(key('v', "v"))
	value = updated.(dashboard)
	if value.gitDiff.split {
		t.Fatal("a second v did not restore inline view")
	}
}

func TestDashboardPersistsAndRestoresGitDiffViewMode(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace}},
	}}}
	configPath := filepath.Join(t.TempDir(), "config.json")
	value := newDashboardWithConfig(&fakeBackend{}, snapshot, configPath, Config{})
	value.gitStates = map[string]gitState{workspace.Path: {Branch: "main", Dirty: true}}
	value.setNavigation(1)

	updated, _ := value.Update(gitDiffViewKey())
	value = updated.(dashboard)
	if value.gitDiff.split {
		t.Fatal("new file view did not default to inline")
	}
	updated, save := value.Update(key('v', "v"))
	value = updated.(dashboard)
	if save == nil || !value.gitDiff.split {
		t.Fatalf("v = (save %v, split %t), want split persisted", save, value.gitDiff.split)
	}
	updated, _ = value.Update(save())
	value = updated.(dashboard)
	loaded, err := loadConfig(configPath)
	if err != nil || loaded.GitDiffView != "split" {
		t.Fatalf("stored config = (%#v, %v), want split", loaded, err)
	}

	updated, _ = value.Update(gitDiffViewKey())
	value = updated.(dashboard)
	updated, _ = value.Update(gitDiffViewKey())
	value = updated.(dashboard)
	if !value.gitDiff.split {
		t.Fatal("reopened file view forgot split mode")
	}

	restarted := newDashboardWithConfig(&fakeBackend{}, snapshot, configPath, loaded)
	restarted.gitStates = value.gitStates
	restarted.setNavigation(1)
	updated, _ = restarted.Update(gitDiffViewKey())
	restarted = updated.(dashboard)
	if !restarted.gitDiff.split {
		t.Fatal("restarted dashboard forgot split mode")
	}
}

func lineContainsInOrder(value string, fragments ...string) bool {
	for _, line := range strings.Split(value, "\n") {
		position := 0
		matched := true
		for _, fragment := range fragments {
			index := strings.Index(line[position:], fragment)
			if index < 0 {
				matched = false
				break
			}
			position += index + len(fragment)
		}
		if matched {
			return true
		}
	}
	return false
}

func TestDashboardGitDiffViewHandlesCleanAndFailedReads(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.gitDiff = gitDiffView{active: true, target: model.Workspace{Name: "alpha", Path: "/projects/alpha"}, filesPending: true, request: 1}

	updated, command := value.Update(gitChangedFilesMsg{path: "/projects/alpha", request: 1})
	value = updated.(dashboard)
	if command != nil || value.gitDiff.filesPending || !strings.Contains(ansi.Strip(value.render()), "No changed files") {
		t.Fatalf("clean repository = (command %v, state %#v)\n%s", command, value.gitDiff, ansi.Strip(value.render()))
	}

	value.gitDiff.filesPending = true
	value.gitDiff.request++
	updated, command = value.Update(gitChangedFilesMsg{
		path: "/projects/alpha", request: value.gitDiff.request, err: errors.New("status failed"),
	})
	value = updated.(dashboard)
	if command != nil || value.gitDiff.filesPending || value.gitDiff.err == "" || !strings.Contains(ansi.Strip(value.render()), "status failed") {
		t.Fatalf("failed repository read = (command %v, state %#v)\n%s", command, value.gitDiff, ansi.Strip(value.render()))
	}
}

func TestDashboardRecognisesCtrlShiftFForGitDiffAcrossInputLayouts(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace}},
	}}}
	value := newDashboard(&fakeBackend{}, snapshot)
	value.gitStates = map[string]gitState{workspace.Path: {Branch: "main"}}
	value.setNavigation(1)
	physicalF := tea.KeyPressMsg(tea.Key{Code: 'ㄹ', BaseCode: 'f', Mod: tea.ModCtrl | tea.ModShift})

	updated, command := value.Update(physicalF)
	value = updated.(dashboard)
	if command == nil || !value.gitDiff.active {
		t.Fatalf("Ctrl+Shift+F with a Korean layout = (command %v, state %#v)", command, value.gitDiff)
	}
}

func TestDashboardGitDiffViewOwnsInputAndKeepsHelpModalUsable(t *testing.T) {
	stream := newMemoryStream("")
	terminal := newEmbeddedTerminal("tab-1", stream, 40, 10)
	defer terminal.close()
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.focus = terminalPane
	value.terminal = terminal
	value.gitDiff = gitDiffView{active: true, target: model.Workspace{Name: "alpha", Path: "/projects/alpha"}}

	updated, _ := value.Update(key('x', "x"))
	value = updated.(dashboard)
	updated, _ = value.Update(tea.PasteMsg{Content: "pasted"})
	value = updated.(dashboard)
	if stream.String() != "" {
		t.Fatalf("hidden terminal received input %q", stream.String())
	}

	updated, _ = value.Update(key(tea.KeyF1, ""))
	value = updated.(dashboard)
	if value.modal != helpModal || !value.gitDiff.active {
		t.Fatalf("F1 = (modal %v, file view %t), want help over file view", value.modal, value.gitDiff.active)
	}
	updated, _ = value.Update(key(tea.KeyEscape, ""))
	value = updated.(dashboard)
	if value.modal != noModal || !value.gitDiff.active {
		t.Fatalf("Esc in help = (modal %v, file view %t), want only help closed", value.modal, value.gitDiff.active)
	}
}

func gitDiffViewKey() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: 'f', ShiftedCode: 'F', Mod: tea.ModCtrl | tea.ModShift})
}
