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
	diffMessage := diffCommand()
	loadedDiff := diffMessage.(gitFileDiffMsg)
	if loadedDiff.lines == nil || !loadedDiff.syntaxHighlighted {
		t.Fatalf("background diff result was not prepared for rendering: %#v", loadedDiff)
	}
	updated, _ = value.Update(diffMessage)
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

func TestDashboardLoadsAndRendersEveryWorkspaceFile(t *testing.T) {
	workspace := model.Workspace{ID: "workspace-1", RootID: "root-1", Name: "alpha", Path: "/projects/alpha"}
	snapshot := model.Snapshot{Roots: []model.RootView{{
		Root:        model.Root{ID: "root-1", Name: "projects", Path: "/projects"},
		Directories: []model.WorkspaceView{{Workspace: workspace}},
	}}}
	previousFiles, previousFile := loadWorkspaceFiles, loadWorkspaceFile
	t.Cleanup(func() { loadWorkspaceFiles, loadWorkspaceFile = previousFiles, previousFile })
	loadWorkspaceFiles = func(path string) ([]gitChangedFile, error) {
		if path != workspace.Path {
			t.Fatalf("workspace files path = %q, want %q", path, workspace.Path)
		}
		return []gitChangedFile{{Path: "README.md"}, {Path: "internal/ui/view.go"}}, nil
	}
	loadWorkspaceFile = func(path, filePath string) (string, error) {
		if path != workspace.Path || filePath != "README.md" {
			t.Fatalf("workspace file target = (%q, %q), want (%q, README.md)", path, filePath, workspace.Path)
		}
		return "# Alpha\n", nil
	}

	value := newDashboard(&fakeBackend{}, snapshot)
	value.setNavigation(1)
	target, _ := value.navigationItem()
	updated, filesCommand := value.openAllFilesView(target)
	value = updated.(dashboard)
	updated, fileCommand := value.Update(filesCommand())
	value = updated.(dashboard)
	if fileCommand == nil || value.gitDiff.fileIndex != 0 || !value.gitDiff.collapsed["internal"] {
		t.Fatalf("initial all-files tree = (command %v, state %#v), want README.md loading with directories collapsed",
			fileCommand, value.gitDiff)
	}
	updated, _ = value.Update(fileCommand())
	value = updated.(dashboard)

	rendered := ansi.Strip(value.render())
	for _, fragment := range []string{"Files · alpha", "▸ internal/", "README.md", "File · README.md", "# Alpha"} {
		if !strings.Contains(rendered, fragment) {
			t.Fatalf("workspace file view does not contain %q:\n%s", fragment, rendered)
		}
	}
	if strings.Contains(rendered, "view.go") {
		t.Fatalf("workspace file view started with internal expanded:\n%s", rendered)
	}
}

func TestAllFilesTreeSortsDirectoriesBeforeFiles(t *testing.T) {
	files := []gitChangedFile{
		{Path: "alpha.txt"},
		{Path: "zeta/last.txt"},
		{Path: "beta/first.txt"},
		{Path: "omega.txt"},
	}

	rows := fileViewTreeRows(files, allFilesView)
	want := []string{"beta", "beta/first.txt", "zeta", "zeta/last.txt", "alpha.txt", "omega.txt"}
	if len(rows) != len(want) {
		t.Fatalf("all-files rows = %#v, want paths %q", rows, want)
	}
	for index, path := range want {
		if rows[index].path != path {
			t.Fatalf("all-files row %d = %q, want %q", index, rows[index].path, path)
		}
	}

	changedRows := fileViewTreeRows(files, changedFilesView)
	if changedRows[0].path != "alpha.txt" {
		t.Fatalf("changed-files first row = %q, want existing name order", changedRows[0].path)
	}
}

func TestAllFilesTreePreservesExpandedDirectoriesOnRefresh(t *testing.T) {
	files := []gitChangedFile{{Path: "internal/ui/view.go"}, {Path: "README.md"}}
	rows := fileViewTreeRows(files, allFilesView)
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.gitDiff = gitDiffView{
		active: true, mode: allFilesView, filesPending: true, request: 1,
	}

	updated, command := value.Update(gitChangedFilesMsg{path: "", request: 1, files: files, rows: rows})
	value = updated.(dashboard)
	if command != nil || !value.gitDiff.collapsed["internal"] || !value.gitDiff.collapsed["internal/ui"] {
		t.Fatalf("initial collapsed directories = (command %v, collapsed %v)", command, value.gitDiff.collapsed)
	}
	updated, _ = value.Update(key(tea.KeyRight, ""))
	value = updated.(dashboard)
	if value.gitDiff.collapsed["internal"] {
		t.Fatalf("expanded directory remains collapsed: %v", value.gitDiff.collapsed)
	}

	value.gitDiff.filesPending = true
	value.gitDiff.request = 2
	updated, _ = value.Update(gitChangedFilesMsg{path: "", request: 2, files: files, rows: rows})
	value = updated.(dashboard)
	if value.gitDiff.collapsed["internal"] || !value.gitDiff.collapsed["internal/ui"] {
		t.Fatalf("refresh changed expanded state: %v", value.gitDiff.collapsed)
	}
}

func TestDashboardCollapsesAndExpandsFileTreeDirectories(t *testing.T) {
	files := []gitChangedFile{
		{Path: "internal/ui/first.go", IndexStatus: ' ', WorkTreeStatus: 'M'},
		{Path: "internal/ui/second.go", IndexStatus: ' ', WorkTreeStatus: 'M'},
		{Path: "README.md", IndexStatus: ' ', WorkTreeStatus: 'M'},
	}
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.gitDiff = gitDiffView{
		active: true, files: files, treeRows: gitDiffTreeRows(files),
		fileIndex: -1, treeIndex: 2,
	}

	updated, command := value.Update(key(tea.KeyEnter, ""))
	value = updated.(dashboard)
	if command != nil || !value.gitDiff.collapsed["internal/ui"] || len(value.gitDiff.treeRows) != 3 {
		t.Fatalf("collapse directory = (command %v, state %#v)", command, value.gitDiff)
	}
	rendered := ansi.Strip(strings.Join(value.renderGitChangedFiles(40, 10), "\n"))
	if !strings.Contains(rendered, "▸ ui/") || strings.Contains(rendered, "first.go") {
		t.Fatalf("collapsed tree is incomplete:\n%s", rendered)
	}

	updated, command = value.Update(key(tea.KeyRight, ""))
	value = updated.(dashboard)
	if command != nil || value.gitDiff.collapsed["internal/ui"] || len(value.gitDiff.treeRows) != 5 {
		t.Fatalf("expand directory = (command %v, state %#v)", command, value.gitDiff)
	}
	rendered = ansi.Strip(strings.Join(value.renderGitChangedFiles(40, 10), "\n"))
	if !strings.Contains(rendered, "▾ ui/") || !strings.Contains(rendered, "first.go") {
		t.Fatalf("expanded tree is incomplete:\n%s", rendered)
	}

	updated, command = value.Update(key(tea.KeyDown, ""))
	value = updated.(dashboard)
	if command == nil || value.gitDiff.fileIndex != 0 || value.gitDiff.selectedDirectory != "" {
		t.Fatalf("move from directory to file = (command %v, state %#v)", command, value.gitDiff)
	}
	updated, command = value.Update(key(tea.KeyUp, ""))
	value = updated.(dashboard)
	if command != nil || value.gitDiff.fileIndex != -1 || value.gitDiff.selectedDirectory != "internal/ui" {
		t.Fatalf("move from file to directory = (command %v, state %#v)", command, value.gitDiff)
	}

	updated, command = value.Update(key(tea.KeyLeft, ""))
	value = updated.(dashboard)
	if command != nil || !value.gitDiff.collapsed["internal/ui"] {
		t.Fatalf("collapse directory with Left = (command %v, state %#v)", command, value.gitDiff)
	}

	value.gitDiff.filesPending = true
	value.gitDiff.request = 1
	updated, command = value.Update(gitChangedFilesMsg{
		path: "", request: 1, files: files, rows: gitDiffTreeRows(files),
	})
	value = updated.(dashboard)
	if command != nil || !value.gitDiff.collapsed["internal/ui"] || len(value.gitDiff.treeRows) != 3 ||
		value.gitDiff.selectedDirectory != "internal/ui" {
		t.Fatalf("refresh collapsed directory = (command %v, state %#v)", command, value.gitDiff)
	}
}

func TestDashboardExpandsTabsInLoadedGitDiff(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 100
	value.height = 20
	value.gitDiff = gitDiffView{
		active:    true,
		target:    model.Workspace{Name: "alpha", Path: "/projects/alpha"},
		files:     []gitChangedFile{{Path: "file.go", WorkTreeStatus: 'M'}},
		fileIndex: 0,
		request:   1,
	}

	lines := normalizeGitDiffLines("@@ -1 +1 @@\n-\told\tvalue\n+\tnew\tvalue")
	syntax, syntaxHighlighted := highlightGitDiffSyntax("file.go", lines)
	updated, _ := value.handleGitFileDiff(gitFileDiffMsg{
		path:              "/projects/alpha",
		filePath:          "file.go",
		request:           1,
		lines:             lines,
		syntax:            syntax,
		syntaxHighlighted: syntaxHighlighted,
	})
	value = updated.(dashboard)

	for _, line := range value.gitDiff.diffLines {
		if strings.ContainsRune(line, '\t') {
			t.Fatalf("loaded diff still contains a tab: %q", line)
		}
	}
	for _, split := range []bool{false, true} {
		value.gitDiff.split = split
		rendered := ansi.Strip(strings.Join(value.renderGitFileDiff(80, 10), "\n"))
		for _, fragment := range []string{"-    old value", "+    new value"} {
			if !strings.Contains(rendered, fragment) {
				t.Fatalf("split=%t diff does not contain %q:\n%s", split, fragment, rendered)
			}
		}
		if strings.ContainsRune(rendered, '�') {
			t.Fatalf("split=%t diff contains a replacement character:\n%s", split, rendered)
		}
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
	maximum := value.gitDiff.diffOffset
	updated, _ = value.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp, Mod: tea.ModCtrl}))
	value = updated.(dashboard)
	if value.gitDiff.diffOffset != maximum-1 {
		t.Fatalf("Ctrl+Up diff offset = %d, want %d", value.gitDiff.diffOffset, maximum-1)
	}
	updated, _ = value.Update(tea.MouseWheelMsg{
		X: value.dimensions().leftWidth + separatorWidth,
		Y: 4, Button: tea.MouseWheelUp,
	})
	value = updated.(dashboard)
	if value.gitDiff.diffOffset != max(maximum-4, 0) {
		t.Fatalf("wheel up diff offset = %d, want %d", value.gitDiff.diffOffset, max(maximum-4, 0))
	}
	if value.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("file view mouse mode = %v, want cell motion", value.View().MouseMode)
	}
}

func TestDashboardUsesMouseInFileTree(t *testing.T) {
	previousFile := loadWorkspaceFile
	t.Cleanup(func() { loadWorkspaceFile = previousFile })
	loadWorkspaceFile = func(_ string, filePath string) (string, error) {
		return filePath, nil
	}
	files := []gitChangedFile{
		{Path: "folder/inside.txt"},
		{Path: "root.txt"},
		{Path: "tail.txt"},
	}
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 80
	value.height = 10
	value.gitDiff = gitDiffView{
		active: true, mode: allFilesView, target: model.Workspace{Path: "/workspace"},
		files: files, treeRows: visibleGitDiffTreeRows(fileViewTreeRows(files, allFilesView), map[string]bool{"folder": true}),
		collapsed: map[string]bool{"folder": true}, fileIndex: -1, selectedDirectory: "folder",
	}

	updated, command := value.Update(tea.MouseClickMsg{X: 2, Y: 2, Button: tea.MouseLeft})
	value = updated.(dashboard)
	if command != nil || value.gitDiff.collapsed["folder"] || len(value.gitDiff.treeRows) != 4 {
		t.Fatalf("folder click = (command %v, state %#v), want expanded", command, value.gitDiff)
	}

	updated, command = value.Update(tea.MouseClickMsg{X: 2, Y: 3, Button: tea.MouseLeft})
	value = updated.(dashboard)
	if command == nil || value.gitDiff.fileIndex != 0 {
		t.Fatalf("file click = (command %v, state %#v), want inside.txt loading", command, value.gitDiff)
	}

	updated, command = value.Update(tea.MouseWheelMsg{X: 2, Y: 3, Button: tea.MouseWheelDown})
	value = updated.(dashboard)
	if command == nil || value.gitDiff.fileIndex != 2 {
		t.Fatalf("file-tree wheel = (command %v, state %#v), want tail.txt loading", command, value.gitDiff)
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
		{full: "Staged changes", fullIndex: 0, leftIndex: -1, rightIndex: -1},
		{full: "diff --git a/file.txt b/file.txt", fullIndex: 1, leftIndex: -1, rightIndex: -1},
		{full: "@@ -1,3 +1,4 @@", fullIndex: 2, leftIndex: -1, rightIndex: -1},
		{left: " same", right: " same", fullIndex: -1, leftIndex: 3, rightIndex: 3},
		{left: "-old one", right: "+new one", fullIndex: -1, leftIndex: 4, rightIndex: 6},
		{left: "-old two", right: "+new two", fullIndex: -1, leftIndex: 5, rightIndex: 7},
		{right: "+new three", fullIndex: -1, leftIndex: -1, rightIndex: 8},
		{left: " tail", right: " tail", fullIndex: -1, leftIndex: 9, rightIndex: 9},
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

func TestSplitGitDiffRowsKeepNoNewlineMarkersWithChanges(t *testing.T) {
	for _, test := range []struct {
		name  string
		lines []string
		want  []gitSplitDiffRow
	}{
		{
			name:  "both sides",
			lines: []string{"-old", "\\ No newline at end of file", "+new", "\\ No newline at end of file"},
			want: []gitSplitDiffRow{
				{left: "-old", right: "+new", fullIndex: -1, leftIndex: 0, rightIndex: 2},
				{left: "\\ No newline at end of file", right: "\\ No newline at end of file", fullIndex: -1, leftIndex: 1, rightIndex: 3},
			},
		},
		{
			name:  "removed side only",
			lines: []string{"-old", "\\ No newline at end of file", "+new one", "+new two"},
			want: []gitSplitDiffRow{
				{left: "-old", right: "+new one", fullIndex: -1, leftIndex: 0, rightIndex: 2},
				{right: "+new two", fullIndex: -1, leftIndex: -1, rightIndex: 3},
				{left: "\\ No newline at end of file", fullIndex: -1, leftIndex: 1, rightIndex: -1},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			rows := splitGitDiffRows(test.lines)
			if len(rows) != len(test.want) {
				t.Fatalf("split rows = %#v, want %#v", rows, test.want)
			}
			for index := range test.want {
				if rows[index] != test.want[index] {
					t.Fatalf("split row %d = %#v, want %#v", index, rows[index], test.want[index])
				}
			}
		})
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
	updated, command := value.Update(key('v', "v"))
	value = updated.(dashboard)
	if command != nil || value.gitDiff.split {
		t.Fatalf("v = (command %v, split %t), want no shortcut action", command, value.gitDiff.split)
	}
	updated, _ = value.Update(key(tea.KeyF6, ""))
	value = updated.(dashboard)
	split := ansi.Strip(value.render())
	if !value.gitDiff.split || !strings.Contains(split, "Diff · file.txt · split") ||
		!lineContainsInOrder(split, "-old", "│", "+new") {
		t.Fatalf("split view is incomplete:\n%s", split)
	}
	updated, _ = value.Update(key(tea.KeyF6, ""))
	value = updated.(dashboard)
	if value.gitDiff.split {
		t.Fatal("a second F6 did not restore inline view")
	}
}

func TestDashboardRefreshesGitDiffWithF5Only(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.gitDiff = gitDiffView{active: true, target: model.Workspace{Name: "alpha", Path: "/projects/alpha"}}

	updated, command := value.Update(key('r', "r"))
	value = updated.(dashboard)
	if command != nil || value.gitDiff.filesPending {
		t.Fatalf("r = (command %v, pending %t), want no shortcut action", command, value.gitDiff.filesPending)
	}
	updated, command = value.Update(key(tea.KeyF5, ""))
	value = updated.(dashboard)
	if command == nil || !value.gitDiff.filesPending {
		t.Fatalf("F5 = (command %v, pending %t), want file refresh", command, value.gitDiff.filesPending)
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
	updated, save := value.Update(key(tea.KeyF6, ""))
	value = updated.(dashboard)
	if save == nil || !value.gitDiff.split {
		t.Fatalf("F6 = (save %v, split %t), want split persisted", save, value.gitDiff.split)
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

// Scrollback is a view of the terminal, and the file view is what is on screen
// instead of it. F6 is the file view's own layout toggle, but a modal drawn
// over the view routes the function keys back to their global meanings — and
// F6 there used to open a scrollback nothing drew, which the user then fell
// into on closing the file view.
func TestDashboardRefusesScrollbackWhileTheFileViewIsOpen(t *testing.T) {
	terminal := newEmbeddedTerminal("tab-1", newMemoryStream(""), 40, 10)
	defer terminal.close()
	for range 40 {
		terminal.writeOutput([]byte("history line\r\n"))
	}
	if terminal.scrollbackLen() == 0 {
		t.Fatal("the terminal has no history for scrollback to show")
	}
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 120, 40
	value.focus = terminalPane
	value.terminal = terminal
	value.gitDiff = gitDiffView{active: true, target: model.Workspace{Name: "alpha", Path: "/projects/alpha"}}

	updated, _ := value.Update(key(tea.KeyF1, ""))
	value = updated.(dashboard)
	updated, _ = value.Update(key(tea.KeyF6, ""))
	value = updated.(dashboard)
	if value.scrollback {
		t.Fatal("F6 over the file view opened a scrollback nothing draws")
	}
	if !value.gitDiff.active {
		t.Fatal("F6 over the file view closed it")
	}
	if value.errorMessage == "" {
		t.Fatal("F6 over the file view said nothing about why scrollback did not open")
	}
}

func gitDiffViewKey() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: 'f', ShiftedCode: 'F', Mod: tea.ModCtrl | tea.ModShift})
}
