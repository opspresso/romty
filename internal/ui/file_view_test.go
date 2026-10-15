package ui

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opspresso/romty/internal/gitrepo"
	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/testutil"
)

func TestDashboardTogglesTheFileViewForContextWorkspace(t *testing.T) {
	snapshot, workspace := alphaWorkspaceSnapshot(nil)
	testutil.Set(t, &loadGitChangedFiles, func(path string) ([]gitrepo.ChangedFile, error) {
		if path != workspace.Path {
			t.Fatalf("changed files path = %q, want %q", path, workspace.Path)
		}
		return []gitrepo.ChangedFile{
			{Path: "README.md", WorkTreeStatus: 'M', IndexStatus: ' '},
			{Path: "internal/ui/view.go", WorkTreeStatus: '?', IndexStatus: '?'},
		}, nil
	})
	testutil.Set(t, &loadGitFileDiff, func(path string, file gitrepo.ChangedFile) (string, error) {
		if path != workspace.Path || file.Path != "README.md" {
			t.Fatalf("diff target = (%q, %q), want (%q, README.md)", path, file.Path, workspace.Path)
		}
		return "diff --git a/README.md b/README.md\n@@ -1 +1 @@\n-old\n+new", nil
	})

	value := newDashboard(&fakeBackend{}, snapshot)
	value.gitStates = map[string]gitrepo.State{workspace.Path: {Branch: "main", Dirty: true}}
	value.setNavigation(1)

	filesCommand := send(&value, fileViewKey())
	if filesCommand == nil || !value.fileView.active || value.fileView.target.Path != workspace.Path || !value.fileView.filesPending {
		t.Fatalf("open diff view = (command %v, state %#v)", filesCommand, value.fileView)
	}
	diffCommand := send(&value, filesCommand())
	if diffCommand == nil || len(value.fileView.files) != 2 || value.fileView.fileIndex != 0 || !value.fileView.diffPending {
		t.Fatalf("changed files result = (command %v, state %#v)", diffCommand, value.fileView)
	}
	diffMessage := diffCommand()
	loadedDiff := diffMessage.(fileContentMsg)
	if loadedDiff.lines == nil || !loadedDiff.syntaxHighlighted {
		t.Fatalf("background diff result was not prepared for rendering: %#v", loadedDiff)
	}
	send(&value, diffMessage)
	rendered := ansi.Strip(value.render())
	for _, fragment := range []string{"Changes · alpha", "README.md", "▾ internal/", "▾ ui/", "view.go", "Diff · README.md", "-old", "+new"} {
		if !strings.Contains(rendered, fragment) {
			t.Fatalf("Git diff view does not contain %q:\n%s", fragment, rendered)
		}
	}

	command := send(&value, fileViewKey())
	if command != nil || value.fileView.active {
		t.Fatalf("close diff view = (command %v, state %#v)", command, value.fileView)
	}
}

func TestDashboardLoadsAndRendersEveryWorkspaceFile(t *testing.T) {
	snapshot, workspace := alphaWorkspaceSnapshot(nil)
	testutil.Set(t, &loadWorkspaceFiles, func(path string) ([]gitrepo.ChangedFile, error) {
		if path != workspace.Path {
			t.Fatalf("workspace files path = %q, want %q", path, workspace.Path)
		}
		return []gitrepo.ChangedFile{{Path: "README.md"}, {Path: "internal/ui/view.go"}}, nil
	})
	testutil.Set(t, &loadWorkspaceFile, func(path, filePath string) (string, error) {
		if path != workspace.Path || filePath != "README.md" {
			t.Fatalf("workspace file target = (%q, %q), want (%q, README.md)", path, filePath, workspace.Path)
		}
		return "# Alpha\n", nil
	})

	value := newDashboard(&fakeBackend{}, snapshot)
	value.setNavigation(1)
	target, _ := value.navigationItem()
	updated, filesCommand := value.openAllFilesView(target)
	value = updated.(dashboard)
	fileCommand := send(&value, filesCommand())
	if fileCommand == nil || value.fileView.fileIndex != 0 || !value.fileView.collapsed["internal"] {
		t.Fatalf("initial all-files tree = (command %v, state %#v), want README.md loading with directories collapsed",
			fileCommand, value.fileView)
	}
	send(&value, fileCommand())

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
	files := []gitrepo.ChangedFile{
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
	files := []gitrepo.ChangedFile{{Path: "internal/ui/view.go"}, {Path: "README.md"}}
	rows := fileViewTreeRows(files, allFilesView)
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.fileView = fileViewState{
		active: true, mode: allFilesView, filesPending: true, request: 1,
	}

	command := send(&value, fileListMsg{path: "", request: 1, files: files, rows: rows})
	if command != nil || !value.fileView.collapsed["internal"] || !value.fileView.collapsed["internal/ui"] {
		t.Fatalf("initial collapsed directories = (command %v, collapsed %v)", command, value.fileView.collapsed)
	}
	send(&value, key(tea.KeyRight, ""))
	if value.fileView.collapsed["internal"] {
		t.Fatalf("expanded directory remains collapsed: %v", value.fileView.collapsed)
	}

	value.fileView.filesPending = true
	value.fileView.request = 2
	send(&value, fileListMsg{path: "", request: 2, files: files, rows: rows})
	if value.fileView.collapsed["internal"] || !value.fileView.collapsed["internal/ui"] {
		t.Fatalf("refresh changed expanded state: %v", value.fileView.collapsed)
	}
}

func TestDashboardCollapsesAndExpandsFileTreeDirectories(t *testing.T) {
	files := []gitrepo.ChangedFile{
		{Path: "internal/ui/first.go", IndexStatus: ' ', WorkTreeStatus: 'M'},
		{Path: "internal/ui/second.go", IndexStatus: ' ', WorkTreeStatus: 'M'},
		{Path: "README.md", IndexStatus: ' ', WorkTreeStatus: 'M'},
	}
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.fileView = fileViewState{
		active: true, files: files, treeRows: fileTreeRowsOf(files),
		fileIndex: -1, treeIndex: 2,
	}

	command := send(&value, key(tea.KeyEnter, ""))
	if command != nil || !value.fileView.collapsed["internal/ui"] || len(value.fileView.treeRows) != 3 {
		t.Fatalf("collapse directory = (command %v, state %#v)", command, value.fileView)
	}
	rendered := ansi.Strip(strings.Join(value.renderGitChangedFiles(40, 10), "\n"))
	if !strings.Contains(rendered, "▸ ui/") || strings.Contains(rendered, "first.go") {
		t.Fatalf("collapsed tree is incomplete:\n%s", rendered)
	}

	command = send(&value, key(tea.KeyRight, ""))
	if command != nil || value.fileView.collapsed["internal/ui"] || len(value.fileView.treeRows) != 5 {
		t.Fatalf("expand directory = (command %v, state %#v)", command, value.fileView)
	}
	rendered = ansi.Strip(strings.Join(value.renderGitChangedFiles(40, 10), "\n"))
	if !strings.Contains(rendered, "▾ ui/") || !strings.Contains(rendered, "first.go") {
		t.Fatalf("expanded tree is incomplete:\n%s", rendered)
	}

	command = send(&value, key(tea.KeyDown, ""))
	if command == nil || value.fileView.fileIndex != 0 || value.fileView.selectedDirectory != "" {
		t.Fatalf("move from directory to file = (command %v, state %#v)", command, value.fileView)
	}
	command = send(&value, key(tea.KeyUp, ""))
	if command != nil || value.fileView.fileIndex != -1 || value.fileView.selectedDirectory != "internal/ui" {
		t.Fatalf("move from file to directory = (command %v, state %#v)", command, value.fileView)
	}

	command = send(&value, key(tea.KeyLeft, ""))
	if command != nil || !value.fileView.collapsed["internal/ui"] {
		t.Fatalf("collapse directory with Left = (command %v, state %#v)", command, value.fileView)
	}

	value.fileView.filesPending = true
	value.fileView.request = 1
	command = send(&value, fileListMsg{
		path: "", request: 1, files: files, rows: fileTreeRowsOf(files),
	})
	if command != nil || !value.fileView.collapsed["internal/ui"] || len(value.fileView.treeRows) != 3 ||
		value.fileView.selectedDirectory != "internal/ui" {
		t.Fatalf("refresh collapsed directory = (command %v, state %#v)", command, value.fileView)
	}
}

func TestDashboardExpandsTabsInLoadedGitDiff(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 100
	value.height = 20
	value.fileView = fileViewState{
		active:    true,
		target:    model.Workspace{Name: "alpha", Path: "/projects/alpha"},
		files:     []gitrepo.ChangedFile{{Path: "file.go", WorkTreeStatus: 'M'}},
		fileIndex: 0,
		request:   1,
	}

	lines := normalizeGitDiffLines("@@ -1 +1 @@\n-\told\tvalue\n+\tnew\tvalue")
	syntax, syntaxHighlighted := highlightGitDiffSyntax("file.go", lines)
	updated, _ := value.handleFileContent(fileContentMsg{
		path:              "/projects/alpha",
		filePath:          "file.go",
		request:           1,
		lines:             lines,
		syntax:            syntax,
		syntaxHighlighted: syntaxHighlighted,
	})
	value = updated.(dashboard)

	for _, line := range value.fileView.diffLines {
		if strings.ContainsRune(line, '\t') {
			t.Fatalf("loaded diff still contains a tab: %q", line)
		}
	}
	for _, split := range []bool{false, true} {
		value.fileView.split = split
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

func TestDashboardNavigatesAndScrollsTheFileView(t *testing.T) {
	testutil.Set(t, &loadGitFileDiff, func(_ string, file gitrepo.ChangedFile) (string, error) {
		return file.Path + "\n" + strings.Join([]string{"one", "two", "three", "four", "five", "six", "seven"}, "\n"), nil
	})
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 80
	value.height = 10
	value.fileView = fileViewState{
		active: true,
		target: model.Workspace{Name: "alpha", Path: "/projects/alpha"},
		files: []gitrepo.ChangedFile{
			{Path: "first.txt", WorkTreeStatus: 'M', IndexStatus: ' '},
			{Path: "second.txt", WorkTreeStatus: 'M', IndexStatus: ' '},
		},
		diffLines: []string{"first.txt"},
	}

	command := send(&value, key(tea.KeyDown, ""))
	if command == nil || value.fileView.fileIndex != 1 || !value.fileView.diffPending {
		t.Fatalf("Down = (command %v, state %#v)", command, value.fileView)
	}
	send(&value, command())
	if value.fileView.diffPending || len(value.fileView.diffLines) == 0 || value.fileView.diffLines[0] != "second.txt" {
		t.Fatalf("second file diff = %#v", value.fileView)
	}

	send(&value, key(tea.KeyPgDown, ""))
	if value.fileView.diffOffset == 0 {
		t.Fatalf("PgDown diff offset = %d, want scrolled", value.fileView.diffOffset)
	}
	send(&value, key(tea.KeyHome, ""))
	if value.fileView.diffOffset != 0 {
		t.Fatalf("Home diff offset = %d, want 0", value.fileView.diffOffset)
	}
	send(&value, key(tea.KeyEnd, ""))
	if value.fileView.diffOffset != value.maximumFileViewOffset() {
		t.Fatalf("End diff offset = %d, want %d", value.fileView.diffOffset, value.maximumFileViewOffset())
	}
	maximum := value.fileView.diffOffset
	send(&value, tea.KeyPressMsg(tea.Key{Code: tea.KeyUp, Mod: tea.ModCtrl}))
	if value.fileView.diffOffset != maximum-1 {
		t.Fatalf("Ctrl+Up diff offset = %d, want %d", value.fileView.diffOffset, maximum-1)
	}
	send(&value, tea.MouseWheelMsg{
		X: value.dimensions().leftWidth + separatorWidth,
		Y: 4, Button: tea.MouseWheelUp,
	})
	if value.fileView.diffOffset != max(maximum-4, 0) {
		t.Fatalf("wheel up diff offset = %d, want %d", value.fileView.diffOffset, max(maximum-4, 0))
	}
	if value.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("file view mouse mode = %v, want cell motion", value.View().MouseMode)
	}
}

func TestDashboardUsesMouseInFileTree(t *testing.T) {
	testutil.Set(t, &loadWorkspaceFile, func(_ string, filePath string) (string, error) {
		return filePath, nil
	})
	files := []gitrepo.ChangedFile{
		{Path: "folder/inside.txt"},
		{Path: "root.txt"},
		{Path: "tail.txt"},
	}
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 80
	value.height = 10
	value.fileView = fileViewState{
		active: true, mode: allFilesView, target: model.Workspace{Path: "/workspace"},
		files: files, treeRows: visibleFileTreeRows(fileViewTreeRows(files, allFilesView), map[string]bool{"folder": true}),
		collapsed: map[string]bool{"folder": true}, fileIndex: -1, selectedDirectory: "folder",
	}

	command := send(&value, tea.MouseClickMsg{X: 2, Y: 2, Button: tea.MouseLeft})
	if command != nil || value.fileView.collapsed["folder"] || len(value.fileView.treeRows) != 4 {
		t.Fatalf("folder click = (command %v, state %#v), want expanded", command, value.fileView)
	}

	command = send(&value, tea.MouseClickMsg{X: 2, Y: 3, Button: tea.MouseLeft})
	if command == nil || value.fileView.fileIndex != 0 {
		t.Fatalf("file click = (command %v, state %#v), want inside.txt loading", command, value.fileView)
	}

	command = send(&value, tea.MouseWheelMsg{X: 2, Y: 3, Button: tea.MouseWheelDown})
	if command == nil || value.fileView.fileIndex != 2 {
		t.Fatalf("file-tree wheel = (command %v, state %#v), want tail.txt loading", command, value.fileView)
	}
}

func TestDashboardColorsChangedFilesByStatus(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.fileView = fileViewState{
		active:    true,
		fileIndex: -1,
		files: []gitrepo.ChangedFile{
			{Path: "added.txt", IndexStatus: 'A', WorkTreeStatus: ' '},
			{Path: "modified.txt", IndexStatus: ' ', WorkTreeStatus: 'M'},
			{Path: "deleted.txt", IndexStatus: 'D', WorkTreeStatus: ' '},
		},
	}

	rows := fileTreeRowsOf(value.fileView.files)
	rendered := make([]string, 0, len(rows))
	for _, row := range rows {
		rendered = append(rendered, value.renderFileTreeRow(row, 30))
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
	if !slices.Equal(rows, want) {
		t.Fatalf("split rows = %#v, want %#v", rows, want)
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
			if rows := splitGitDiffRows(test.lines); !slices.Equal(rows, test.want) {
				t.Fatalf("split rows = %#v, want %#v", rows, test.want)
			}
		})
	}
}

func TestDashboardTogglesInlineAndSplitGitDiff(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width = 120
	value.height = 20
	value.fileView = fileViewState{
		active:    true,
		target:    model.Workspace{Name: "alpha", Path: "/projects/alpha"},
		files:     []gitrepo.ChangedFile{{Path: "file.txt", IndexStatus: ' ', WorkTreeStatus: 'M'}},
		diffLines: []string{"@@ -1 +1 @@", "-old", "+new"},
	}
	value.fileView.splitRows = splitGitDiffRows(value.fileView.diffLines)

	inline := ansi.Strip(value.render())
	if !strings.Contains(inline, "Diff · file.txt · inline") {
		t.Fatalf("inline title is missing:\n%s", inline)
	}
	command := send(&value, key('v', "v"))
	if command != nil || value.fileView.split {
		t.Fatalf("v = (command %v, split %t), want no shortcut action", command, value.fileView.split)
	}
	send(&value, key(tea.KeyF6, ""))
	split := ansi.Strip(value.render())
	if !value.fileView.split || !strings.Contains(split, "Diff · file.txt · split") ||
		!lineContainsInOrder(split, "-old", "│", "+new") {
		t.Fatalf("split view is incomplete:\n%s", split)
	}
	send(&value, key(tea.KeyF6, ""))
	if value.fileView.split {
		t.Fatal("a second F6 did not restore inline view")
	}
}

func TestSplitDiffReplacesAndClearsPreparedRows(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 100, 10
	value.fileView = fileViewState{
		active: true, split: true, target: model.Workspace{Path: "/workspace"},
		files: []gitrepo.ChangedFile{{Path: "file.txt"}}, request: 1,
	}
	for _, text := range []string{"first", "second"} {
		send(&value, fileContentMsg{
			path: "/workspace", filePath: "file.txt", request: value.fileView.request,
			lines: []string{"@@ -1 +1 @@", "-old", "+" + text},
		})
		rendered := ansi.Strip(strings.Join(value.renderGitFileDiff(80, 8), "\n"))
		if !lineContainsInOrder(rendered, "-old", "│", "+"+text) || value.maximumFileViewOffset() != 0 {
			t.Fatalf("prepared split rows did not follow new content:\n%s", rendered)
		}
		if text == "second" && strings.Contains(rendered, "+first") {
			t.Fatal("split view retained the previous result")
		}
		value.fileView.request++
	}
	value.fileView.clearDiff()
	if len(value.fileView.splitRows) != 0 || value.maximumFileViewOffset() != 0 {
		t.Fatal("clearing the diff retained split content")
	}
}

func TestDashboardRefreshesGitDiffWithF5Only(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.fileView = fileViewState{active: true, target: model.Workspace{Name: "alpha", Path: "/projects/alpha"}}

	command := send(&value, key('r', "r"))
	if command != nil || value.fileView.filesPending {
		t.Fatalf("r = (command %v, pending %t), want no shortcut action", command, value.fileView.filesPending)
	}
	command = send(&value, key(tea.KeyF5, ""))
	if command == nil || !value.fileView.filesPending {
		t.Fatalf("F5 = (command %v, pending %t), want file refresh", command, value.fileView.filesPending)
	}
}

func TestDashboardPersistsAndRestoresTheDiffLayout(t *testing.T) {
	snapshot, workspace := alphaWorkspaceSnapshot(nil)
	configPath := filepath.Join(t.TempDir(), "config.json")
	value := newDashboardWithConfig(&fakeBackend{}, snapshot, configPath, Config{})
	value.gitStates = map[string]gitrepo.State{workspace.Path: {Branch: "main", Dirty: true}}
	value.setNavigation(1)

	send(&value, fileViewKey())
	if value.fileView.split {
		t.Fatal("new file view did not default to inline")
	}
	save := send(&value, key(tea.KeyF6, ""))
	if save == nil || !value.fileView.split {
		t.Fatalf("F6 = (save %v, split %t), want split persisted", save, value.fileView.split)
	}
	send(&value, save())
	loaded, err := loadConfig(configPath)
	if err != nil || loaded.GitDiffView != "split" {
		t.Fatalf("stored config = (%#v, %v), want split", loaded, err)
	}

	send(&value, fileViewKey())
	send(&value, fileViewKey())
	if !value.fileView.split {
		t.Fatal("reopened file view forgot split mode")
	}

	restarted := newDashboardWithConfig(&fakeBackend{}, snapshot, configPath, loaded)
	restarted.gitStates = value.gitStates
	restarted.setNavigation(1)
	send(&restarted, fileViewKey())
	if !restarted.fileView.split {
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
	value.fileView = fileViewState{active: true, target: model.Workspace{Name: "alpha", Path: "/projects/alpha"}, filesPending: true, request: 1}

	command := send(&value, fileListMsg{path: "/projects/alpha", request: 1})
	if command != nil || value.fileView.filesPending || !strings.Contains(ansi.Strip(value.render()), "No changed files") {
		t.Fatalf("clean repository = (command %v, state %#v)\n%s", command, value.fileView, ansi.Strip(value.render()))
	}

	value.fileView.filesPending = true
	value.fileView.request++
	command = send(&value, fileListMsg{
		path: "/projects/alpha", request: value.fileView.request, err: errors.New("status failed"),
	})
	if command != nil || value.fileView.filesPending || value.fileView.err == "" || !strings.Contains(ansi.Strip(value.render()), "status failed") {
		t.Fatalf("failed repository read = (command %v, state %#v)\n%s", command, value.fileView, ansi.Strip(value.render()))
	}
}

func TestDashboardRecognisesCtrlShiftFForGitDiffAcrossInputLayouts(t *testing.T) {
	snapshot, workspace := alphaWorkspaceSnapshot(nil)
	value := newDashboard(&fakeBackend{}, snapshot)
	value.gitStates = map[string]gitrepo.State{workspace.Path: {Branch: "main"}}
	value.setNavigation(1)
	physicalF := tea.KeyPressMsg(tea.Key{Code: 'ㄹ', BaseCode: 'f', Mod: tea.ModCtrl | tea.ModShift})

	command := send(&value, physicalF)
	if command == nil || !value.fileView.active {
		t.Fatalf("Ctrl+Shift+F with a Korean layout = (command %v, state %#v)", command, value.fileView)
	}
}

func TestDashboardGitDiffViewOwnsInputAndKeepsHelpModalUsable(t *testing.T) {
	stream := newMemoryStream("")
	terminal := newEmbeddedTerminal("tab-1", stream, 40, 10)
	defer terminal.close()
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.focus = terminalPane
	value.terminal = terminal
	value.fileView = fileViewState{active: true, target: model.Workspace{Name: "alpha", Path: "/projects/alpha"}}

	send(&value, key('x', "x"))
	send(&value, tea.PasteMsg{Content: "pasted"})
	if stream.String() != "" {
		t.Fatalf("hidden terminal received input %q", stream.String())
	}

	send(&value, key(tea.KeyF1, ""))
	if value.modal != helpModal || !value.fileView.active {
		t.Fatalf("F1 = (modal %v, file view %t), want help over file view", value.modal, value.fileView.active)
	}
	send(&value, key(tea.KeyEscape, ""))
	if value.modal != noModal || !value.fileView.active {
		t.Fatalf("Esc in help = (modal %v, file view %t), want only help closed", value.modal, value.fileView.active)
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
	value.fileView = fileViewState{active: true, target: model.Workspace{Name: "alpha", Path: "/projects/alpha"}}

	send(&value, key(tea.KeyF1, ""))
	send(&value, key(tea.KeyF6, ""))
	if value.scrollback {
		t.Fatal("F6 over the file view opened a scrollback nothing draws")
	}
	if !value.fileView.active {
		t.Fatal("F6 over the file view closed it")
	}
	if value.errorMessage == "" {
		t.Fatal("F6 over the file view said nothing about why scrollback did not open")
	}
}

func fileViewKey() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: 'f', ShiftedCode: 'F', Mod: tea.ModCtrl | tea.ModShift})
}

func TestHighlightWorkspaceFileSyntax(t *testing.T) {
	lines := normalizeWorkspaceFileLines("package main\n\n\tfunc main() {}\n")
	syntax, highlighted := highlightWorkspaceFileSyntax("main.go", lines)
	if !highlighted || len(syntax) != len(lines) || !syntax[0].newHighlighted ||
		gitSyntaxText(syntax[0].new) != "package main" {
		t.Fatalf("workspace file syntax = (%#v, %t)", syntax, highlighted)
	}
	for _, line := range lines {
		if strings.ContainsRune(line, '\t') {
			t.Fatalf("normalized workspace file contains a tab: %q", line)
		}
	}
}
