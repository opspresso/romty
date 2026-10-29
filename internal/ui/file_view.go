// The file view: the changed-file diff view and the all-files browser that
// replace the terminal pane, their tree, and the content beside it.

package ui

import (
	"fmt"
	"maps"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"

	"github.com/opspresso/romty/internal/display"
	"github.com/opspresso/romty/internal/gitrepo"
	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/workspacefs"
)

var (
	loadGitChangedFiles = gitrepo.ChangedFiles
	loadGitFileDiff     = gitrepo.FileDiff
	loadWorkspaceFiles  = readWorkspaceFiles
	loadWorkspaceFile   = workspacefs.Read
)

type fileViewMode int

const (
	changedFilesView fileViewMode = iota
	allFilesView
)

// fileViewText is what the file view calls things in one mode.
type fileViewText struct {
	list, loadingList, emptyList, content, loadingContent, selectFile string
}

func (v fileViewMode) text() fileViewText {
	if v == allFilesView {
		return fileViewText{
			list: "Files", loadingList: "Loading files…", emptyList: "No files",
			content: "File", loadingContent: "Loading file…", selectFile: "Select a file",
		}
	}
	return fileViewText{
		list: "Changes", loadingList: "Loading changed files…", emptyList: "No changed files",
		content: "Diff", loadingContent: "Loading diff…", selectFile: "Select a changed file",
	}
}

type fileViewState struct {
	active            bool
	mode              fileViewMode
	target            model.Workspace
	files             []gitrepo.ChangedFile
	treeRows          []fileTreeRow
	treeIndex         int
	selectedDirectory string
	collapsed         map[string]bool
	fileIndex         int
	diffLines         []string
	splitRows         []gitSplitDiffRow
	diffSyntax        []gitDiffLineSyntax
	diffOffset        int
	split             bool
	syntaxHighlighted bool
	filesPending      bool
	diffPending       bool
	err               string
	request           uint64
}

// clearDiff drops whatever diff or file is on screen. Four paths reach that
// state — a file list that failed or arrived, content that was asked for, or
// content that failed — and setting the fields by hand at each is how one ends
// up leaving the previous file's highlighting under the next file's text, or
// its scroll offset under content that is shorter.
func (v *fileViewState) clearDiff() {
	v.diffLines = nil
	v.splitRows = nil
	v.diffSyntax = nil
	v.syntaxHighlighted = false
	v.diffOffset = 0
}

type fileListMsg struct {
	path    string
	request uint64
	files   []gitrepo.ChangedFile
	rows    []fileTreeRow
	err     error
}

type fileContentMsg struct {
	path              string
	filePath          string
	request           uint64
	lines             []string
	syntax            []gitDiffLineSyntax
	syntaxHighlighted bool
	err               error
}

func (m dashboard) toggleFileView() (tea.Model, tea.Cmd) {
	if m.fileView.active {
		m.fileView = fileViewState{request: m.fileView.request}
		return m, nil
	}
	if m.modal != noModal {
		return m, nil
	}
	target, ok := m.gitActionWorkspace()
	if !ok {
		m.setError(gitError, notAGitRepository)
		return m, nil
	}
	// Whether the target is a repository is openChangedFilesView's question, and it
	// asks it whichever way the view is opened.
	return m.openChangedFilesView(target)
}

func (m dashboard) openChangedFilesView(target navItem) (tea.Model, tea.Cmd) {
	if !target.hasGit {
		m.setError(gitError, notAGitRepository)
		return m, nil
	}
	m.stopScrollback()
	m.fileView = fileViewState{
		active: true, target: target.workspace, request: m.fileView.request,
		split: m.config.GitDiffView == gitDiffViewSplit,
	}
	m.clearAnyError()
	return m, m.loadChangedFiles()
}

func (m dashboard) openAllFilesView(target navItem) (tea.Model, tea.Cmd) {
	if target.isRoot || target.failure != "" {
		return m, nil
	}
	m.stopScrollback()
	m.fileView = fileViewState{
		active: true, mode: allFilesView, target: target.workspace, request: m.fileView.request,
	}
	m.clearAnyError()
	return m, m.loadChangedFiles()
}

func (m *dashboard) loadChangedFiles() tea.Cmd {
	m.fileView.request++
	request := m.fileView.request
	path := m.fileView.target.Path
	mode := m.fileView.mode
	m.fileView.filesPending = true
	m.fileView.diffPending = false
	m.fileView.err = ""
	return func() tea.Msg {
		var files []gitrepo.ChangedFile
		var err error
		if mode == allFilesView {
			files, err = loadWorkspaceFiles(path)
		} else {
			files, err = loadGitChangedFiles(path)
		}
		return fileListMsg{
			path: path, request: request, files: files, rows: fileViewTreeRows(files, mode), err: err,
		}
	}
}

func (m dashboard) handleFileList(message fileListMsg) (tea.Model, tea.Cmd) {
	if !m.fileView.active || message.path != m.fileView.target.Path || message.request != m.fileView.request {
		return m, nil
	}
	selectedPath := ""
	if m.fileView.fileIndex >= 0 && m.fileView.fileIndex < len(m.fileView.files) {
		selectedPath = m.fileView.files[m.fileView.fileIndex].Path
	}
	m.fileView.filesPending = false
	if message.err != nil {
		m.fileView.files = nil
		m.fileView.treeRows = nil
		m.fileView.clearDiff()
		m.fileView.err = message.err.Error()
		return m, nil
	}
	m.fileView.files = message.files
	if m.fileView.mode == allFilesView && m.fileView.collapsed == nil {
		m.fileView.collapsed = make(map[string]bool)
		for _, row := range message.rows {
			if row.directory {
				m.fileView.collapsed[row.path] = true
			}
		}
	}
	m.fileView.treeRows = visibleFileTreeRows(message.rows, m.fileView.collapsed)
	m.ensureFileTreeRows()
	selectedDirectory := m.fileView.selectedDirectory
	m.fileView.fileIndex = 0
	for index, file := range m.fileView.files {
		if file.Path == selectedPath {
			m.fileView.fileIndex = index
			break
		}
	}
	m.fileView.clearDiff()
	m.fileView.err = ""
	if len(m.fileView.files) == 0 {
		return m, nil
	}
	if selectedDirectory != "" {
		for index, row := range m.fileView.treeRows {
			if row.directory && row.path == selectedDirectory {
				m.fileView.treeIndex = index
				m.fileView.fileIndex = -1
				return m, nil
			}
		}
		m.fileView.selectedDirectory = ""
	}
	selectedFileVisible := false
	for index, row := range m.fileView.treeRows {
		if row.fileIndex == m.fileView.fileIndex {
			m.fileView.treeIndex = index
			selectedFileVisible = true
			break
		}
	}
	if !selectedFileVisible && len(m.fileView.treeRows) > 0 {
		row := m.fileView.treeRows[0]
		m.fileView.treeIndex = 0
		if row.directory {
			m.fileView.selectedDirectory = row.path
			m.fileView.fileIndex = -1
			return m, nil
		}
		m.fileView.fileIndex = row.fileIndex
	}
	return m, m.loadSelectedFileContent()
}

func (m *dashboard) loadSelectedFileContent() tea.Cmd {
	if m.fileView.fileIndex < 0 || m.fileView.fileIndex >= len(m.fileView.files) {
		return nil
	}
	m.fileView.request++
	request := m.fileView.request
	path := m.fileView.target.Path
	file := m.fileView.files[m.fileView.fileIndex]
	mode := m.fileView.mode
	m.fileView.diffPending = true
	m.fileView.clearDiff()
	m.fileView.err = ""
	return func() tea.Msg {
		var diff string
		var err error
		if mode == allFilesView {
			diff, err = loadWorkspaceFile(path, file.Path)
		} else {
			diff, err = loadGitFileDiff(path, file)
		}
		message := fileContentMsg{path: path, filePath: file.Path, request: request, err: err}
		if err == nil {
			if mode == allFilesView {
				message.lines = normalizeWorkspaceFileLines(diff)
				message.syntax, message.syntaxHighlighted = highlightWorkspaceFileSyntax(file.Path, message.lines)
			} else {
				message.lines = normalizeGitDiffLines(diff)
				message.syntax, message.syntaxHighlighted = highlightGitDiffSyntax(file.Path, message.lines)
			}
		}
		return message
	}
}

func (m dashboard) handleFileContent(message fileContentMsg) (tea.Model, tea.Cmd) {
	if !m.fileView.active || message.path != m.fileView.target.Path || message.request != m.fileView.request ||
		m.fileView.fileIndex < 0 || m.fileView.fileIndex >= len(m.fileView.files) ||
		message.filePath != m.fileView.files[m.fileView.fileIndex].Path {
		return m, nil
	}
	m.fileView.diffPending = false
	if message.err != nil {
		m.fileView.err = message.err.Error()
		m.fileView.clearDiff()
		return m, nil
	}
	m.fileView.diffLines = message.lines
	if m.fileView.mode == changedFilesView {
		// The pairing depends on content, not the viewport or theme. Build it
		// once per accepted result and reuse it for scrolling and rendering.
		m.fileView.splitRows = splitGitDiffRows(message.lines)
	}
	m.fileView.diffSyntax = message.syntax
	m.fileView.syntaxHighlighted = message.syntaxHighlighted
	m.fileView.err = ""
	return m, nil
}

func normalizeGitDiffLines(diff string) []string {
	lines := strings.Split(diff, "\n")
	for index, line := range lines {
		lines[index] = expandGitDiffTabs(line)
	}
	return lines
}

// expandGitDiffTabs expands tabs after a diff line's marker column, so the
// stops line up with the file's own columns.
func expandGitDiffTabs(line string) string {
	if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, " ") {
		return line[:1] + expandTabs(line[1:])
	}
	return expandTabs(line)
}

func (m dashboard) handleFileViewKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "f1":
		return m.openModal(helpModal)
	case "f4":
		return m.quit()
	case "f5":
		return m, m.loadChangedFiles()
	case "esc":
		return m.toggleFileView()
	case "f":
		// f opens the file view from the workspace pane, so it closes it
		// too: without the Kitty keyboard protocol Ctrl+Shift+F cannot.
		return m.toggleFileView()
	case "f6":
		if m.fileView.mode == allFilesView {
			return m, nil
		}
		m.fileView.split = !m.fileView.split
		m.config.GitDiffView = gitDiffViewSetting(m.fileView.split)
		m.fileView.diffOffset = min(m.fileView.diffOffset, m.maximumFileViewOffset())
		return m, m.saveConfig()
	case "up", "k":
		return m.moveFileViewItem(-1)
	case "down", "j":
		return m.moveFileViewItem(1)
	case "enter":
		return m.toggleFileTreeDirectory()
	case "left", "h":
		return m.setFileTreeDirectoryCollapsed(true)
	case "right", "l":
		return m.setFileTreeDirectoryCollapsed(false)
	case "ctrl+up":
		m.scrollFileView(-1)
	case "ctrl+down":
		m.scrollFileView(1)
	case "pgup", "ctrl+b":
		m.scrollFileView(-m.fileViewPageSize())
	case "pgdown", "ctrl+f":
		m.scrollFileView(m.fileViewPageSize())
	case "home", "g":
		m.fileView.diffOffset = 0
	case "end", "G":
		m.fileView.diffOffset = m.maximumFileViewOffset()
	}
	return m, nil
}

func (m dashboard) handleFileViewMouse(message tea.MouseMsg) (tea.Model, tea.Cmd) {
	mouse := message.Mouse()
	view := m.fileViewLayout()
	if mouse.Y < 0 || mouse.Y >= view.bodyHeight {
		return m, nil
	}
	if wheel, ok := message.(tea.MouseWheelMsg); ok {
		if mouse.X >= 0 && mouse.X < view.leftWidth {
			switch wheel.Button {
			case tea.MouseWheelUp:
				return m.moveFileViewItem(-wheelLines)
			case tea.MouseWheelDown:
				return m.moveFileViewItem(wheelLines)
			}
			return m, nil
		}
		if mouse.X >= view.leftWidth+view.separator {
			switch wheel.Button {
			case tea.MouseWheelUp:
				m.scrollFileView(-wheelLines)
			case tea.MouseWheelDown:
				m.scrollFileView(wheelLines)
			}
		}
		return m, nil
	}
	click, ok := message.(tea.MouseClickMsg)
	if !ok || click.Button != tea.MouseLeft || mouse.X < 0 || mouse.X >= view.leftWidth {
		return m, nil
	}
	m.ensureFileTreeRows()
	index, ok := m.fileTreeIndexAtRow(mouse.Y, view.bodyHeight)
	if !ok {
		return m, nil
	}
	row := m.fileView.treeRows[index]
	if row.directory {
		m.fileView.treeIndex = index
		return m.toggleFileTreeDirectory()
	}
	return m.moveFileViewItem(index - m.fileView.treeIndex)
}

func gitDiffViewSetting(split bool) string {
	if split {
		return gitDiffViewSplit
	}
	return gitDiffViewInline
}

func (m dashboard) moveFileViewItem(delta int) (tea.Model, tea.Cmd) {
	if m.fileView.filesPending || len(m.fileView.files) == 0 {
		return m, nil
	}
	m.ensureFileTreeRows()
	next := min(max(m.fileView.treeIndex+delta, 0), len(m.fileView.treeRows)-1)
	if next == m.fileView.treeIndex {
		return m, nil
	}
	m.fileView.treeIndex = next
	row := m.fileView.treeRows[next]
	if row.directory {
		m.fileView.selectedDirectory = row.path
		m.fileView.fileIndex = -1
		m.fileView.diffPending = false
		m.fileView.clearDiff()
		m.fileView.err = ""
		return m, nil
	}
	m.fileView.selectedDirectory = ""
	m.fileView.fileIndex = row.fileIndex
	return m, m.loadSelectedFileContent()
}

func (m *dashboard) ensureFileTreeRows() {
	if len(m.fileView.treeRows) == 0 && len(m.fileView.files) > 0 {
		m.fileView.treeRows = visibleFileTreeRows(fileViewTreeRows(m.fileView.files, m.fileView.mode), m.fileView.collapsed)
	}
	m.fileView.treeIndex = min(max(m.fileView.treeIndex, 0), max(len(m.fileView.treeRows)-1, 0))
}

func (m dashboard) toggleFileTreeDirectory() (tea.Model, tea.Cmd) {
	m.ensureFileTreeRows()
	if len(m.fileView.treeRows) == 0 || !m.fileView.treeRows[m.fileView.treeIndex].directory {
		return m, nil
	}
	row := m.fileView.treeRows[m.fileView.treeIndex]
	return m.setFileTreeDirectoryCollapsed(!m.fileView.collapsed[row.path])
}

func (m dashboard) setFileTreeDirectoryCollapsed(collapsed bool) (tea.Model, tea.Cmd) {
	m.ensureFileTreeRows()
	if len(m.fileView.treeRows) == 0 {
		return m, nil
	}
	row := m.fileView.treeRows[m.fileView.treeIndex]
	if !row.directory || m.fileView.collapsed[row.path] == collapsed {
		return m, nil
	}
	nextCollapsed := maps.Clone(m.fileView.collapsed)
	if nextCollapsed == nil {
		nextCollapsed = make(map[string]bool)
	}
	if collapsed {
		nextCollapsed[row.path] = true
	} else {
		delete(nextCollapsed, row.path)
	}
	m.fileView.collapsed = nextCollapsed
	m.fileView.treeRows = visibleFileTreeRows(fileViewTreeRows(m.fileView.files, m.fileView.mode), nextCollapsed)
	for index, candidate := range m.fileView.treeRows {
		if candidate.directory && candidate.path == row.path {
			m.fileView.treeIndex = index
			break
		}
	}
	m.fileView.selectedDirectory = row.path
	m.fileView.fileIndex = -1
	m.fileView.diffPending = false
	m.fileView.clearDiff()
	m.fileView.err = ""
	return m, nil
}

func (m *dashboard) scrollFileView(delta int) {
	m.fileView.diffOffset = min(max(m.fileView.diffOffset+delta, 0), m.maximumFileViewOffset())
}

func (m dashboard) fileViewPageSize() int {
	return max(m.dimensions().bodyHeight-2, 1)
}

func (m dashboard) maximumFileViewOffset() int {
	lineCount := len(m.fileView.diffLines)
	if m.fileView.mode == changedFilesView && m.fileView.split {
		lineCount = len(m.fileView.splitRows)
	}
	return max(lineCount-m.fileViewPageSize(), 0)
}

func (m dashboard) renderFileViewPanes(leftWidth, rightWidth, height int) []string {
	separator := " " + m.styles.divider.Render("│") + " "
	separator = truncate(separator, m.fileViewLayout().separator)
	return mergePanes(
		m.renderFileViewList(leftWidth, height), m.renderFileViewContent(rightWidth, height),
		leftWidth, rightWidth, height,
		func(int) string { return separator })
}

func (m dashboard) renderFileViewList(width, height int) []string {
	text := m.fileView.mode.text()
	title := " " + text.list + " · " + display.Text(m.fileView.target.Name) + " "
	lines := []string{m.paneHeader(m.styles.paneTitleActive, title, width), ""}
	switch {
	case m.fileView.filesPending:
		return append(lines, m.styles.empty.Render("  "+text.loadingList))
	case m.fileView.err != "" && len(m.fileView.files) == 0:
		return append(lines, m.styles.errorText.Render("  "+display.Text(m.fileView.err)))
	case len(m.fileView.files) == 0:
		return append(lines, m.styles.empty.Render("  "+text.emptyList))
	}
	rows := m.fileView.treeRows
	if len(rows) == 0 {
		rows = fileViewTreeRows(m.fileView.files, m.fileView.mode)
	}
	start, end := fileTreeWindow(rows, m.fileView.treeIndex, height)
	for _, row := range rows[start:end] {
		lines = append(lines, m.renderFileTreeRow(row, width))
	}
	return lines
}

func (m dashboard) fileTreeIndexAtRow(row, height int) (int, bool) {
	if row < 2 {
		return 0, false
	}
	start, end := fileTreeWindow(m.fileView.treeRows, m.fileView.treeIndex, height)
	index := start + row - 2
	return index, index >= start && index < end
}

func fileTreeWindow(rows []fileTreeRow, selectedRow, height int) (start, end int) {
	available := max(height-2, 0)
	selectedRow = min(max(selectedRow, 0), max(len(rows)-1, 0))
	start = min(max(selectedRow-available/2, 0), max(len(rows)-available, 0))
	return start, min(start+available, len(rows))
}

func (m dashboard) renderFileTreeRow(row fileTreeRow, width int) string {
	prefix := strings.Repeat("  ", row.depth) + "  "
	if row.directory {
		indicator, marker := " ", "▾ "
		style := m.styles.navigationRoot
		if m.fileView.collapsed[row.path] {
			marker = "▸ "
		}
		if row.path == m.fileView.selectedDirectory {
			indicator = "▌"
			style = m.styles.navigationSelected
		}
		line := indicator + strings.Repeat("  ", row.depth) + marker + display.Text(row.name) + "/"
		return style.Render(pad(truncate(line, width), width))
	}
	file := m.fileView.files[row.fileIndex]
	status := ""
	if m.fileView.mode == changedFilesView {
		status = gitChangedFileStatus(file)
	}
	indicator := " "
	style := m.styles.navigationItem
	if row.fileIndex == m.fileView.fileIndex {
		indicator = "▌"
		style = m.styles.navigationSelected
	}
	prefix = indicator + prefix
	name := " " + display.Text(row.name)
	used := lipgloss.Width(prefix) + lipgloss.Width(status)
	if used >= width {
		return style.Render(truncate(prefix+status+name, width))
	}
	name = pad(truncate(name, width-used), width-used)
	statusStyle := m.gitChangedFileStyle(file)
	if row.fileIndex == m.fileView.fileIndex {
		statusStyle = statusStyle.Background(style.GetBackground()).Bold(true)
	}
	return style.Render(prefix) + statusStyle.Render(status) + style.Render(name)
}

func (m dashboard) gitChangedFileStyle(file gitrepo.ChangedFile) lipgloss.Style {
	return m.gitStatusStyle(file.IndexStatus, file.WorkTreeStatus)
}

// gitStatusStyle is what a porcelain status code is drawn in: gone is red,
// new is green, changed is amber. The file view and the Git status result read
// the same two letters, and green had better mean the same thing in both.
func (m dashboard) gitStatusStyle(index, workTree byte) lipgloss.Style {
	status := string([]byte{index, workTree})
	switch {
	case strings.Contains(status, "U"), status == "AA", status == "DD", strings.Contains(status, "D"):
		return m.styles.diffRemoved
	case status == "??", index == 'A':
		return m.styles.diffAdded
	default:
		return m.styles.gitStatus
	}
}

func (m dashboard) renderFileViewContent(width, height int) []string {
	path := ""
	if m.fileView.fileIndex >= 0 && m.fileView.fileIndex < len(m.fileView.files) {
		path = m.fileView.files[m.fileView.fileIndex].Path
	}
	text := m.fileView.mode.text()
	title := " " + text.content
	if path != "" {
		title += " · " + display.Text(path)
	}
	if m.fileView.mode == changedFilesView {
		mode := "inline"
		if m.fileView.split {
			mode = "split"
		}
		title += " · " + mode
	}
	title += " "
	lines := []string{m.paneHeader(m.styles.paneTitle, title, width), ""}
	switch {
	case m.fileView.diffPending:
		return append(lines, m.styles.empty.Render("  "+text.loadingContent))
	case m.fileView.err != "":
		return append(lines, m.styles.errorText.Render("  "+display.Text(m.fileView.err)))
	case len(m.fileView.files) == 0 || m.fileView.fileIndex < 0 || m.fileView.fileIndex >= len(m.fileView.files):
		return append(lines, m.styles.empty.Render("  "+text.selectFile))
	}
	capacity := max(height-len(lines), 0)
	start := min(m.fileView.diffOffset, m.maximumFileViewOffset())
	if m.fileView.mode == changedFilesView && m.fileView.split {
		rows := m.fileView.splitRows
		end := min(start+capacity, len(rows))
		for _, row := range rows[start:end] {
			lines = append(lines, m.renderGitSplitDiffRow(row, width))
		}
		return lines
	}
	end := min(start+capacity, len(m.fileView.diffLines))
	for index, line := range m.fileView.diffLines[start:end] {
		if m.fileView.mode == allFilesView {
			lines = append(lines, m.renderWorkspaceFileLineAt(display.Text(line), start+index, width))
		} else {
			lines = append(lines, m.renderGitDiffLineAt(display.Text(line), start+index, width))
		}
	}
	return lines
}

func (m dashboard) renderWorkspaceFileLineAt(line string, index, width int) string {
	if !m.fileView.syntaxHighlighted || index < 0 || index >= len(m.fileView.diffSyntax) {
		return m.styles.navigationItem.Render(truncate(line, width))
	}
	tokens := m.fileView.diffSyntax[index].new
	if !m.fileView.diffSyntax[index].newHighlighted || gitSyntaxText(tokens) != line {
		return m.styles.navigationItem.Render(truncate(line, width))
	}
	var result strings.Builder
	for _, token := range tokens {
		result.WriteString(m.gitSyntaxStyle(token.kind).Render(display.Text(token.value)))
	}
	return truncate(result.String(), width)
}

type gitSplitDiffRow struct {
	full       string
	left       string
	right      string
	fullIndex  int
	leftIndex  int
	rightIndex int
}

type indexedGitDiffLine struct {
	text  string
	index int
}

func splitGitDiffRows(lines []string) []gitSplitDiffRow {
	rows := make([]gitSplitDiffRow, 0, len(lines))
	for index := 0; index < len(lines); {
		line := lines[index]
		if isRemovedDiffLine(line) || isAddedDiffLine(line) {
			removed, added := make([]indexedGitDiffLine, 0), make([]indexedGitDiffLine, 0)
			removedNoNewline := indexedGitDiffLine{index: -1}
			addedNoNewline := indexedGitDiffLine{index: -1}
			lastSide := byte(0)
		changeBlock:
			for index < len(lines) {
				switch {
				case isRemovedDiffLine(lines[index]):
					removed = append(removed, indexedGitDiffLine{text: lines[index], index: index})
					lastSide = '-'
				case isAddedDiffLine(lines[index]):
					added = append(added, indexedGitDiffLine{text: lines[index], index: index})
					lastSide = '+'
				case isNoNewlineDiffLine(lines[index]) && lastSide == '-':
					removedNoNewline = indexedGitDiffLine{text: lines[index], index: index}
				case isNoNewlineDiffLine(lines[index]) && lastSide == '+':
					addedNoNewline = indexedGitDiffLine{text: lines[index], index: index}
				default:
					break changeBlock
				}
				index++
			}
			for row := range max(len(removed), len(added)) {
				value := gitSplitDiffRow{fullIndex: -1, leftIndex: -1, rightIndex: -1}
				if row < len(removed) {
					value.left = removed[row].text
					value.leftIndex = removed[row].index
				}
				if row < len(added) {
					value.right = added[row].text
					value.rightIndex = added[row].index
				}
				rows = append(rows, value)
			}
			if removedNoNewline.index >= 0 || addedNoNewline.index >= 0 {
				value := gitSplitDiffRow{fullIndex: -1, leftIndex: -1, rightIndex: -1}
				if removedNoNewline.index >= 0 {
					value.left = removedNoNewline.text
					value.leftIndex = removedNoNewline.index
				}
				if addedNoNewline.index >= 0 {
					value.right = addedNoNewline.text
					value.rightIndex = addedNoNewline.index
				}
				rows = append(rows, value)
			}
			continue
		}
		if strings.HasPrefix(line, " ") {
			rows = append(rows, gitSplitDiffRow{left: line, right: line, fullIndex: -1, leftIndex: index, rightIndex: index})
		} else {
			rows = append(rows, gitSplitDiffRow{full: line, fullIndex: index, leftIndex: -1, rightIndex: -1})
		}
		index++
	}
	return rows
}

func isRemovedDiffLine(line string) bool {
	return strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---")
}

func isAddedDiffLine(line string) bool {
	return strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++")
}

func isNoNewlineDiffLine(line string) bool {
	return strings.HasPrefix(line, "\\ ")
}

func (m dashboard) renderGitSplitDiffRow(row gitSplitDiffRow, width int) string {
	if row.full != "" {
		return m.renderGitDiffLineAt(display.Text(row.full), row.fullIndex, width)
	}
	leftWidth := max((width-separatorWidth)/2, 1)
	rightWidth := max(width-leftWidth-separatorWidth, 1)
	separator := " " + m.styles.divider.Render("│") + " "
	left := pad(m.renderGitDiffLineAt(display.Text(row.left), row.leftIndex, leftWidth), leftWidth)
	right := m.renderGitDiffLineAt(display.Text(row.right), row.rightIndex, rightWidth)
	return left + separator + right
}

func (m dashboard) renderGitDiffLineAt(line string, index, width int) string {
	if !m.fileView.syntaxHighlighted || index < 0 || index >= len(m.fileView.diffSyntax) || len(line) == 0 {
		return m.renderGitDiffLine(line, width)
	}
	syntax := m.fileView.diffSyntax[index]
	var tokens []gitSyntaxToken
	highlighted := false
	prefixStyle := m.styles.navigationItem
	lineStyle := lipgloss.NewStyle()
	changed := false
	switch {
	case isRemovedDiffLine(line):
		tokens, highlighted, prefixStyle = syntax.old, syntax.oldHighlighted, m.styles.diffRemovedLine
		lineStyle, changed = m.styles.diffRemovedLine, true
	case isAddedDiffLine(line):
		tokens, highlighted, prefixStyle = syntax.new, syntax.newHighlighted, m.styles.diffAddedLine
		lineStyle, changed = m.styles.diffAddedLine, true
	case strings.HasPrefix(line, " "):
		tokens, highlighted = syntax.new, syntax.newHighlighted
	}
	if !highlighted || gitSyntaxText(tokens) != line[1:] {
		return m.renderGitDiffLine(line, width)
	}
	var result strings.Builder
	result.WriteString(prefixStyle.Render(line[:1]))
	for _, token := range tokens {
		style := m.gitSyntaxStyle(token.kind)
		if changed {
			style = style.Background(lineStyle.GetBackground())
		}
		result.WriteString(style.Render(display.Text(token.value)))
	}
	rendered := truncate(result.String(), width)
	if changed {
		rendered += lineStyle.Render(strings.Repeat(" ", max(width-lipgloss.Width(rendered), 0)))
	}
	return rendered
}

func (m dashboard) renderGitDiffLine(line string, width int) string {
	style := m.styles.navigationItem
	changed := false
	switch {
	case isAddedDiffLine(line):
		style, changed = m.styles.diffAddedLine, true
	case isRemovedDiffLine(line):
		style, changed = m.styles.diffRemovedLine, true
	case strings.HasPrefix(line, "@@"):
		style = m.styles.diffHunk
	case strings.HasPrefix(line, "diff --git"), strings.HasPrefix(line, "index "),
		strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"):
		style = m.styles.empty
	}
	line = truncate(line, width)
	if changed {
		line = pad(line, width)
	}
	return style.Render(line)
}

type fileTreeNode struct {
	name      string
	fileIndex int
	children  map[string]*fileTreeNode
}

type fileTreeRow struct {
	name      string
	path      string
	depth     int
	directory bool
	fileIndex int
}

func fileViewTreeRows(files []gitrepo.ChangedFile, mode fileViewMode) []fileTreeRow {
	return buildFileViewTreeRows(files, mode == allFilesView)
}

func buildFileViewTreeRows(files []gitrepo.ChangedFile, directoriesFirst bool) []fileTreeRow {
	root := &fileTreeNode{fileIndex: -1, children: make(map[string]*fileTreeNode)}
	for fileIndex, file := range files {
		parts := strings.Split(file.Path, "/")
		node := root
		for index, part := range parts {
			child, ok := node.children[part]
			if !ok {
				child = &fileTreeNode{name: part, fileIndex: -1, children: make(map[string]*fileTreeNode)}
				node.children[part] = child
			}
			if index == len(parts)-1 {
				child.fileIndex = fileIndex
			}
			node = child
		}
	}
	rows := make([]fileTreeRow, 0, len(files)*2)
	appendFileTreeRows(root, "", 0, directoriesFirst, &rows)
	return rows
}

func appendFileTreeRows(node *fileTreeNode, parent string, depth int, directoriesFirst bool, rows *[]fileTreeRow) {
	names := make([]string, 0, len(node.children))
	for name := range node.children {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		leftDirectory := len(node.children[names[i]].children) > 0
		rightDirectory := len(node.children[names[j]].children) > 0
		if directoriesFirst && leftDirectory != rightDirectory {
			return leftDirectory
		}
		return names[i] < names[j]
	})
	for _, name := range names {
		child := node.children[name]
		path := name
		if parent != "" {
			path = parent + "/" + name
		}
		directory := len(child.children) > 0
		if directory {
			*rows = append(*rows, fileTreeRow{name: child.name, path: path, depth: depth, directory: true, fileIndex: -1})
			appendFileTreeRows(child, path, depth+1, directoriesFirst, rows)
			continue
		}
		*rows = append(*rows, fileTreeRow{name: child.name, path: path, depth: depth, fileIndex: child.fileIndex})
	}
}

func visibleFileTreeRows(rows []fileTreeRow, collapsed map[string]bool) []fileTreeRow {
	visible := make([]fileTreeRow, 0, len(rows))
	hiddenBelow := -1
	for _, row := range rows {
		if hiddenBelow >= 0 {
			if row.depth > hiddenBelow {
				continue
			}
			hiddenBelow = -1
		}
		visible = append(visible, row)
		if row.directory && collapsed[row.path] {
			hiddenBelow = row.depth
		}
	}
	return visible
}

func gitChangedFileStatus(file gitrepo.ChangedFile) string {
	if file.IndexStatus == '?' && file.WorkTreeStatus == '?' {
		return "? "
	}
	var status strings.Builder
	if file.IndexStatus != ' ' {
		status.WriteByte(file.IndexStatus)
	}
	if file.WorkTreeStatus != ' ' {
		status.WriteByte(file.WorkTreeStatus)
	}
	if status.Len() == 0 {
		return "·"
	}
	return fmt.Sprintf("%-2s", status.String())
}

// readWorkspaceFiles lists every file of a workspace as file view entries
// with no Git status.
func readWorkspaceFiles(path string) ([]gitrepo.ChangedFile, error) {
	paths, err := workspacefs.List(path)
	if err != nil {
		return nil, err
	}
	files := make([]gitrepo.ChangedFile, len(paths))
	for index, filePath := range paths {
		files[index] = gitrepo.ChangedFile{Path: filePath, IndexStatus: ' ', WorkTreeStatus: ' '}
	}
	return files, nil
}

func normalizeWorkspaceFileLines(content string) []string {
	lines := strings.Split(content, "\n")
	for index, line := range lines {
		lines[index] = expandTabs(line)
	}
	return lines
}

// expandTabs replaces each tab with the spaces up to the next four-column
// stop, measured in display cells.
func expandTabs(line string) string {
	if !strings.ContainsRune(line, '\t') {
		return line
	}
	const tabWidth = 4
	var result strings.Builder
	column := 0
	for _, character := range line {
		if character == '\t' {
			spaces := tabWidth - column%tabWidth
			result.WriteString(strings.Repeat(" ", spaces))
			column += spaces
			continue
		}
		result.WriteRune(character)
		column += lipgloss.Width(string(character))
	}
	return result.String()
}

func highlightWorkspaceFileSyntax(path string, lines []string) ([]gitDiffLineSyntax, bool) {
	lexer, ok := syntaxLexer(path, lines)
	if !ok {
		return nil, false
	}
	return highlightWorkspaceFileSyntaxWithLexer(lexer, lines)
}

func highlightWorkspaceFileSyntaxWithLexer(lexer chroma.Lexer, lines []string) (syntax []gitDiffLineSyntax, highlighted bool) {
	defer func() {
		if recover() != nil {
			syntax, highlighted = nil, false
		}
	}()
	indexed := make([]gitDiffHunkLine, len(lines))
	for index, line := range lines {
		indexed[index] = gitDiffHunkLine{index: index, text: line}
	}
	result := make([]gitDiffLineSyntax, len(lines))
	if !highlightGitDiffSide(lexer, indexed, result, false) {
		return nil, false
	}
	return result, true
}
