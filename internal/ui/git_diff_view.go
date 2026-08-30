package ui

import (
	"fmt"
	"maps"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/opspresso/romty/internal/display"
	"github.com/opspresso/romty/internal/model"
)

var (
	loadGitChangedFiles = readGitChangedFiles
	loadGitFileDiff     = readGitFileDiff
	loadWorkspaceFiles  = readWorkspaceFiles
	loadWorkspaceFile   = readWorkspaceFile
)

type fileViewMode int

const (
	changedFilesView fileViewMode = iota
	allFilesView
)

type gitDiffView struct {
	active            bool
	mode              fileViewMode
	target            model.Workspace
	files             []gitChangedFile
	treeRows          []gitDiffTreeRow
	treeIndex         int
	selectedDirectory string
	collapsed         map[string]bool
	fileIndex         int
	diffLines         []string
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
func (v *gitDiffView) clearDiff() {
	v.diffLines = nil
	v.diffSyntax = nil
	v.syntaxHighlighted = false
	v.diffOffset = 0
}

type gitChangedFilesMsg struct {
	path    string
	request uint64
	files   []gitChangedFile
	rows    []gitDiffTreeRow
	err     error
}

type gitFileDiffMsg struct {
	path              string
	filePath          string
	request           uint64
	lines             []string
	syntax            []gitDiffLineSyntax
	syntaxHighlighted bool
	err               error
}

func (m dashboard) toggleGitDiffView() (tea.Model, tea.Cmd) {
	if m.gitDiff.active {
		m.gitDiff = gitDiffView{request: m.gitDiff.request}
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
	// Whether the target is a repository is openGitDiffView's question, and it
	// asks it whichever way the view is opened.
	return m.openGitDiffView(target)
}

func (m dashboard) openGitDiffView(target navItem) (tea.Model, tea.Cmd) {
	if !target.hasGit {
		m.setError(gitError, notAGitRepository)
		return m, nil
	}
	m.stopScrollback()
	m.gitDiff = gitDiffView{
		active: true, target: target.workspace, request: m.gitDiff.request,
		split: m.gitDiffSplit,
	}
	m.clearAnyError()
	return m, m.loadChangedFiles()
}

func (m dashboard) openAllFilesView(target navItem) (tea.Model, tea.Cmd) {
	if target.isRoot || target.failure != "" {
		return m, nil
	}
	m.stopScrollback()
	m.gitDiff = gitDiffView{
		active: true, mode: allFilesView, target: target.workspace, request: m.gitDiff.request,
	}
	m.clearAnyError()
	return m, m.loadChangedFiles()
}

func (m *dashboard) loadChangedFiles() tea.Cmd {
	m.gitDiff.request++
	request := m.gitDiff.request
	path := m.gitDiff.target.Path
	mode := m.gitDiff.mode
	m.gitDiff.filesPending = true
	m.gitDiff.diffPending = false
	m.gitDiff.err = ""
	return func() tea.Msg {
		var files []gitChangedFile
		var err error
		if mode == allFilesView {
			files, err = loadWorkspaceFiles(path)
		} else {
			files, err = loadGitChangedFiles(path)
		}
		return gitChangedFilesMsg{
			path: path, request: request, files: files, rows: gitDiffTreeRows(files), err: err,
		}
	}
}

func (m dashboard) handleGitChangedFiles(message gitChangedFilesMsg) (tea.Model, tea.Cmd) {
	if !m.gitDiff.active || message.path != m.gitDiff.target.Path || message.request != m.gitDiff.request {
		return m, nil
	}
	selectedPath := ""
	if m.gitDiff.fileIndex >= 0 && m.gitDiff.fileIndex < len(m.gitDiff.files) {
		selectedPath = m.gitDiff.files[m.gitDiff.fileIndex].Path
	}
	m.gitDiff.filesPending = false
	if message.err != nil {
		m.gitDiff.files = nil
		m.gitDiff.treeRows = nil
		m.gitDiff.clearDiff()
		m.gitDiff.err = message.err.Error()
		return m, nil
	}
	m.gitDiff.files = message.files
	m.gitDiff.treeRows = visibleGitDiffTreeRows(message.rows, m.gitDiff.collapsed)
	if len(m.gitDiff.files) > 0 && len(m.gitDiff.treeRows) == 0 {
		m.gitDiff.treeRows = visibleGitDiffTreeRows(gitDiffTreeRows(m.gitDiff.files), m.gitDiff.collapsed)
	}
	selectedDirectory := m.gitDiff.selectedDirectory
	m.gitDiff.fileIndex = 0
	for index, file := range m.gitDiff.files {
		if file.Path == selectedPath {
			m.gitDiff.fileIndex = index
			break
		}
	}
	m.gitDiff.clearDiff()
	m.gitDiff.err = ""
	if len(m.gitDiff.files) == 0 {
		return m, nil
	}
	if selectedDirectory != "" {
		for index, row := range m.gitDiff.treeRows {
			if row.directory && row.path == selectedDirectory {
				m.gitDiff.treeIndex = index
				m.gitDiff.fileIndex = -1
				return m, nil
			}
		}
		m.gitDiff.selectedDirectory = ""
	}
	for index, row := range m.gitDiff.treeRows {
		if row.fileIndex == m.gitDiff.fileIndex {
			m.gitDiff.treeIndex = index
			break
		}
	}
	return m, m.loadSelectedFileDiff()
}

func (m *dashboard) loadSelectedFileDiff() tea.Cmd {
	if m.gitDiff.fileIndex < 0 || m.gitDiff.fileIndex >= len(m.gitDiff.files) {
		return nil
	}
	m.gitDiff.request++
	request := m.gitDiff.request
	path := m.gitDiff.target.Path
	file := m.gitDiff.files[m.gitDiff.fileIndex]
	mode := m.gitDiff.mode
	m.gitDiff.diffPending = true
	m.gitDiff.clearDiff()
	m.gitDiff.err = ""
	return func() tea.Msg {
		var diff string
		var err error
		if mode == allFilesView {
			diff, err = loadWorkspaceFile(path, file.Path)
		} else {
			diff, err = loadGitFileDiff(path, file)
		}
		message := gitFileDiffMsg{path: path, filePath: file.Path, request: request, err: err}
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

func (m dashboard) handleGitFileDiff(message gitFileDiffMsg) (tea.Model, tea.Cmd) {
	if !m.gitDiff.active || message.path != m.gitDiff.target.Path || message.request != m.gitDiff.request ||
		m.gitDiff.fileIndex < 0 || m.gitDiff.fileIndex >= len(m.gitDiff.files) ||
		message.filePath != m.gitDiff.files[m.gitDiff.fileIndex].Path {
		return m, nil
	}
	m.gitDiff.diffPending = false
	if message.err != nil {
		m.gitDiff.err = message.err.Error()
		m.gitDiff.clearDiff()
		return m, nil
	}
	m.gitDiff.diffLines = message.lines
	m.gitDiff.diffSyntax = message.syntax
	m.gitDiff.syntaxHighlighted = message.syntaxHighlighted
	m.gitDiff.err = ""
	return m, nil
}

func normalizeGitDiffLines(diff string) []string {
	lines := strings.Split(diff, "\n")
	for index, line := range lines {
		lines[index] = expandGitDiffTabs(line)
	}
	return lines
}

func expandGitDiffTabs(line string) string {
	if !strings.ContainsRune(line, '\t') {
		return line
	}
	const tabWidth = 4
	prefix := ""
	if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, " ") {
		prefix, line = line[:1], line[1:]
	}
	var result strings.Builder
	result.WriteString(prefix)
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

func (m dashboard) handleGitDiffKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "f1":
		return m.openModal(helpModal)
	case "f4":
		return m.quit()
	case "f5":
		return m, m.loadChangedFiles()
	case "esc":
		return m.toggleGitDiffView()
	case "f6":
		if m.gitDiff.mode == allFilesView {
			return m, nil
		}
		m.gitDiff.split = !m.gitDiff.split
		m.gitDiffSplit = m.gitDiff.split
		m.gitDiff.diffOffset = min(m.gitDiff.diffOffset, m.maximumGitDiffOffset())
		return m, m.saveConfig()
	case "up", "k":
		return m.moveGitDiffFile(-1)
	case "down", "j":
		return m.moveGitDiffFile(1)
	case "enter":
		return m.toggleGitDiffDirectory()
	case "left", "h":
		return m.setGitDiffDirectoryCollapsed(true)
	case "right", "l":
		return m.setGitDiffDirectoryCollapsed(false)
	case "ctrl+up":
		m.scrollGitDiff(-1)
	case "ctrl+down":
		m.scrollGitDiff(1)
	case "pgup", "ctrl+b":
		m.scrollGitDiff(-m.gitDiffPageSize())
	case "pgdown", "ctrl+f":
		m.scrollGitDiff(m.gitDiffPageSize())
	case "home", "g":
		m.gitDiff.diffOffset = 0
	case "end", "G":
		m.gitDiff.diffOffset = m.maximumGitDiffOffset()
	}
	return m, nil
}

func (m dashboard) handleGitDiffMouse(message tea.MouseMsg) (tea.Model, tea.Cmd) {
	wheel, ok := message.(tea.MouseWheelMsg)
	if !ok || wheel.X < m.gitDiffLayout().leftWidth+separatorWidth {
		return m, nil
	}
	switch wheel.Button {
	case tea.MouseWheelUp:
		m.scrollGitDiff(-3)
	case tea.MouseWheelDown:
		m.scrollGitDiff(3)
	}
	return m, nil
}

func gitDiffViewSetting(split bool) string {
	if split {
		return gitDiffViewSplit
	}
	return gitDiffViewInline
}

func (m dashboard) moveGitDiffFile(delta int) (tea.Model, tea.Cmd) {
	if m.gitDiff.filesPending || len(m.gitDiff.files) == 0 {
		return m, nil
	}
	m.ensureGitDiffTreeRows()
	next := min(max(m.gitDiff.treeIndex+delta, 0), len(m.gitDiff.treeRows)-1)
	if next == m.gitDiff.treeIndex {
		return m, nil
	}
	m.gitDiff.treeIndex = next
	row := m.gitDiff.treeRows[next]
	if row.directory {
		m.gitDiff.selectedDirectory = row.path
		m.gitDiff.fileIndex = -1
		m.gitDiff.diffPending = false
		m.gitDiff.clearDiff()
		m.gitDiff.err = ""
		return m, nil
	}
	m.gitDiff.selectedDirectory = ""
	m.gitDiff.fileIndex = row.fileIndex
	return m, m.loadSelectedFileDiff()
}

func (m *dashboard) ensureGitDiffTreeRows() {
	if len(m.gitDiff.treeRows) == 0 && len(m.gitDiff.files) > 0 {
		m.gitDiff.treeRows = visibleGitDiffTreeRows(gitDiffTreeRows(m.gitDiff.files), m.gitDiff.collapsed)
	}
	m.gitDiff.treeIndex = min(max(m.gitDiff.treeIndex, 0), max(len(m.gitDiff.treeRows)-1, 0))
}

func (m dashboard) toggleGitDiffDirectory() (tea.Model, tea.Cmd) {
	m.ensureGitDiffTreeRows()
	if len(m.gitDiff.treeRows) == 0 || !m.gitDiff.treeRows[m.gitDiff.treeIndex].directory {
		return m, nil
	}
	row := m.gitDiff.treeRows[m.gitDiff.treeIndex]
	return m.setGitDiffDirectoryCollapsed(!m.gitDiff.collapsed[row.path])
}

func (m dashboard) setGitDiffDirectoryCollapsed(collapsed bool) (tea.Model, tea.Cmd) {
	m.ensureGitDiffTreeRows()
	if len(m.gitDiff.treeRows) == 0 {
		return m, nil
	}
	row := m.gitDiff.treeRows[m.gitDiff.treeIndex]
	if !row.directory || m.gitDiff.collapsed[row.path] == collapsed {
		return m, nil
	}
	nextCollapsed := maps.Clone(m.gitDiff.collapsed)
	if nextCollapsed == nil {
		nextCollapsed = make(map[string]bool)
	}
	if collapsed {
		nextCollapsed[row.path] = true
	} else {
		delete(nextCollapsed, row.path)
	}
	m.gitDiff.collapsed = nextCollapsed
	m.gitDiff.treeRows = visibleGitDiffTreeRows(gitDiffTreeRows(m.gitDiff.files), nextCollapsed)
	for index, candidate := range m.gitDiff.treeRows {
		if candidate.directory && candidate.path == row.path {
			m.gitDiff.treeIndex = index
			break
		}
	}
	m.gitDiff.selectedDirectory = row.path
	m.gitDiff.fileIndex = -1
	m.gitDiff.diffPending = false
	m.gitDiff.clearDiff()
	m.gitDiff.err = ""
	return m, nil
}

func (m *dashboard) scrollGitDiff(delta int) {
	m.gitDiff.diffOffset = min(max(m.gitDiff.diffOffset+delta, 0), m.maximumGitDiffOffset())
}

func (m dashboard) gitDiffPageSize() int {
	return max(m.dimensions().bodyHeight-2, 1)
}

func (m dashboard) maximumGitDiffOffset() int {
	lineCount := len(m.gitDiff.diffLines)
	if m.gitDiff.mode == changedFilesView && m.gitDiff.split {
		lineCount = len(splitGitDiffRows(m.gitDiff.diffLines))
	}
	return max(lineCount-m.gitDiffPageSize(), 0)
}

func (m dashboard) renderGitDiffPanes(leftWidth, rightWidth, height int) []string {
	separator := " " + m.styles.divider.Render("│") + " "
	return mergePanes(
		m.renderGitChangedFiles(leftWidth, height), m.renderGitFileDiff(rightWidth, height),
		leftWidth, rightWidth, height,
		func(int) string { return separator })
}

func (m dashboard) renderGitChangedFiles(width, height int) []string {
	title, loading, empty := " Changes · ", "  Loading changed files…", "  No changed files"
	if m.gitDiff.mode == allFilesView {
		title, loading, empty = " Files · ", "  Loading files…", "  No files"
	}
	title += display.Text(m.gitDiff.target.Name) + " "
	lines := []string{m.paneHeader(m.styles.paneTitleActive, title, width), ""}
	switch {
	case m.gitDiff.filesPending:
		return append(lines, m.styles.empty.Render(loading))
	case m.gitDiff.err != "" && len(m.gitDiff.files) == 0:
		return append(lines, m.styles.errorText.Render("  "+display.Text(m.gitDiff.err)))
	case len(m.gitDiff.files) == 0:
		return append(lines, m.styles.empty.Render(empty))
	}
	rows := m.gitDiff.treeRows
	if len(rows) == 0 {
		rows = gitDiffTreeRows(m.gitDiff.files)
	}
	available := max(height-len(lines), 0)
	selectedRow := min(max(m.gitDiff.treeIndex, 0), max(len(rows)-1, 0))
	start := min(max(selectedRow-available/2, 0), max(len(rows)-available, 0))
	end := min(start+available, len(rows))
	for _, row := range rows[start:end] {
		lines = append(lines, m.renderGitDiffTreeRow(row, width))
	}
	return lines
}

func (m dashboard) renderGitDiffTreeRow(row gitDiffTreeRow, width int) string {
	prefix := strings.Repeat("  ", row.depth) + "  "
	if row.directory {
		indicator, marker := " ", "▾ "
		style := m.styles.navigationRoot
		if m.gitDiff.collapsed[row.path] {
			marker = "▸ "
		}
		if row.path == m.gitDiff.selectedDirectory {
			indicator = "▌"
			style = m.styles.navigationSelected
		}
		line := indicator + strings.Repeat("  ", row.depth) + marker + display.Text(row.name) + "/"
		return style.Render(pad(truncate(line, width), width))
	}
	file := m.gitDiff.files[row.fileIndex]
	status := ""
	if m.gitDiff.mode == changedFilesView {
		status = gitChangedFileStatus(file)
	}
	indicator := " "
	style := m.styles.navigationItem
	if row.fileIndex == m.gitDiff.fileIndex {
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
	if row.fileIndex == m.gitDiff.fileIndex {
		statusStyle = statusStyle.Background(style.GetBackground()).Bold(true)
	}
	return style.Render(prefix) + statusStyle.Render(status) + style.Render(name)
}

func (m dashboard) gitChangedFileStyle(file gitChangedFile) lipgloss.Style {
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

func (m dashboard) renderGitFileDiff(width, height int) []string {
	path := ""
	if m.gitDiff.fileIndex >= 0 && m.gitDiff.fileIndex < len(m.gitDiff.files) {
		path = m.gitDiff.files[m.gitDiff.fileIndex].Path
	}
	title := " Diff"
	if m.gitDiff.mode == allFilesView {
		title = " File"
	}
	if path != "" {
		title += " · " + display.Text(path)
	}
	if m.gitDiff.mode == changedFilesView {
		mode := "inline"
		if m.gitDiff.split {
			mode = "split"
		}
		title += " · " + mode
	}
	title += " "
	lines := []string{m.paneHeader(m.styles.paneTitle, title, width), ""}
	switch {
	case m.gitDiff.diffPending:
		message := "  Loading diff…"
		if m.gitDiff.mode == allFilesView {
			message = "  Loading file…"
		}
		return append(lines, m.styles.empty.Render(message))
	case m.gitDiff.err != "":
		return append(lines, m.styles.errorText.Render("  "+display.Text(m.gitDiff.err)))
	case len(m.gitDiff.files) == 0 || m.gitDiff.fileIndex < 0 || m.gitDiff.fileIndex >= len(m.gitDiff.files):
		message := "  Select a changed file"
		if m.gitDiff.mode == allFilesView {
			message = "  Select a file"
		}
		return append(lines, m.styles.empty.Render(message))
	}
	capacity := max(height-len(lines), 0)
	start := min(m.gitDiff.diffOffset, m.maximumGitDiffOffset())
	if m.gitDiff.mode == changedFilesView && m.gitDiff.split {
		rows := splitGitDiffRows(m.gitDiff.diffLines)
		end := min(start+capacity, len(rows))
		for _, row := range rows[start:end] {
			lines = append(lines, m.renderGitSplitDiffRow(row, width))
		}
		return lines
	}
	end := min(start+capacity, len(m.gitDiff.diffLines))
	for index, line := range m.gitDiff.diffLines[start:end] {
		if m.gitDiff.mode == allFilesView {
			lines = append(lines, m.renderWorkspaceFileLineAt(display.Text(line), start+index, width))
		} else {
			lines = append(lines, m.renderGitDiffLineAt(display.Text(line), start+index, width))
		}
	}
	return lines
}

func (m dashboard) renderWorkspaceFileLineAt(line string, index, width int) string {
	if !m.gitDiff.syntaxHighlighted || index < 0 || index >= len(m.gitDiff.diffSyntax) {
		return m.styles.navigationItem.Render(truncate(line, width))
	}
	tokens := m.gitDiff.diffSyntax[index].new
	if !m.gitDiff.diffSyntax[index].newHighlighted || gitSyntaxText(tokens) != line {
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
	if !m.gitDiff.syntaxHighlighted || index < 0 || index >= len(m.gitDiff.diffSyntax) || len(line) == 0 {
		return m.renderGitDiffLine(line, width)
	}
	syntax := m.gitDiff.diffSyntax[index]
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

type gitDiffTreeNode struct {
	name      string
	fileIndex int
	children  map[string]*gitDiffTreeNode
}

type gitDiffTreeRow struct {
	name      string
	path      string
	depth     int
	directory bool
	fileIndex int
}

func gitDiffTreeRows(files []gitChangedFile) []gitDiffTreeRow {
	root := &gitDiffTreeNode{fileIndex: -1, children: make(map[string]*gitDiffTreeNode)}
	for fileIndex, file := range files {
		parts := strings.Split(file.Path, "/")
		node := root
		for index, part := range parts {
			child, ok := node.children[part]
			if !ok {
				child = &gitDiffTreeNode{name: part, fileIndex: -1, children: make(map[string]*gitDiffTreeNode)}
				node.children[part] = child
			}
			if index == len(parts)-1 {
				child.fileIndex = fileIndex
			}
			node = child
		}
	}
	rows := make([]gitDiffTreeRow, 0, len(files)*2)
	appendGitDiffTreeRows(root, "", 0, &rows)
	return rows
}

func appendGitDiffTreeRows(node *gitDiffTreeNode, parent string, depth int, rows *[]gitDiffTreeRow) {
	names := make([]string, 0, len(node.children))
	for name := range node.children {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		child := node.children[name]
		path := name
		if parent != "" {
			path = parent + "/" + name
		}
		directory := len(child.children) > 0
		if directory {
			*rows = append(*rows, gitDiffTreeRow{name: child.name, path: path, depth: depth, directory: true, fileIndex: -1})
			appendGitDiffTreeRows(child, path, depth+1, rows)
			continue
		}
		*rows = append(*rows, gitDiffTreeRow{name: child.name, path: path, depth: depth, fileIndex: child.fileIndex})
	}
}

func visibleGitDiffTreeRows(rows []gitDiffTreeRow, collapsed map[string]bool) []gitDiffTreeRow {
	visible := make([]gitDiffTreeRow, 0, len(rows))
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

func gitChangedFileStatus(file gitChangedFile) string {
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
