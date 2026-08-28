package ui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/opspresso/romty/internal/model"
)

var (
	loadGitChangedFiles = readGitChangedFiles
	loadGitFileDiff     = readGitFileDiff
)

type gitDiffView struct {
	active            bool
	target            model.Workspace
	files             []gitChangedFile
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

type gitChangedFilesMsg struct {
	path    string
	request uint64
	files   []gitChangedFile
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
	if !ok || !target.hasGit {
		m.setError(gitError, "selected workspace is not a Git repository")
		return m, nil
	}
	return m.openGitDiffView(target)
}

func (m dashboard) openGitDiffView(target navItem) (tea.Model, tea.Cmd) {
	if !target.hasGit {
		m.setError(gitError, "selected workspace is not a Git repository")
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

func (m *dashboard) loadChangedFiles() tea.Cmd {
	m.gitDiff.request++
	request := m.gitDiff.request
	path := m.gitDiff.target.Path
	m.gitDiff.filesPending = true
	m.gitDiff.diffPending = false
	m.gitDiff.err = ""
	return func() tea.Msg {
		files, err := loadGitChangedFiles(path)
		return gitChangedFilesMsg{path: path, request: request, files: files, err: err}
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
		m.gitDiff.diffLines = nil
		m.gitDiff.diffSyntax = nil
		m.gitDiff.syntaxHighlighted = false
		m.gitDiff.err = message.err.Error()
		return m, nil
	}
	m.gitDiff.files = message.files
	m.gitDiff.fileIndex = 0
	for index, file := range m.gitDiff.files {
		if file.Path == selectedPath {
			m.gitDiff.fileIndex = index
			break
		}
	}
	m.gitDiff.diffLines = nil
	m.gitDiff.diffSyntax = nil
	m.gitDiff.syntaxHighlighted = false
	m.gitDiff.diffOffset = 0
	m.gitDiff.err = ""
	if len(m.gitDiff.files) == 0 {
		return m, nil
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
	m.gitDiff.diffPending = true
	m.gitDiff.diffLines = nil
	m.gitDiff.diffSyntax = nil
	m.gitDiff.syntaxHighlighted = false
	m.gitDiff.diffOffset = 0
	m.gitDiff.err = ""
	return func() tea.Msg {
		diff, err := loadGitFileDiff(path, file)
		message := gitFileDiffMsg{path: path, filePath: file.Path, request: request, err: err}
		if err == nil {
			message.lines = normalizeGitDiffLines(diff)
			message.syntax, message.syntaxHighlighted = highlightGitDiffSyntax(file.Path, message.lines)
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
		m.gitDiff.diffLines = nil
		m.gitDiff.diffSyntax = nil
		m.gitDiff.syntaxHighlighted = false
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
		m.gitDiff.split = !m.gitDiff.split
		m.gitDiffSplit = m.gitDiff.split
		m.gitDiff.diffOffset = min(m.gitDiff.diffOffset, m.maximumGitDiffOffset())
		return m, m.saveConfig()
	case "up", "k":
		return m.moveGitDiffFile(-1)
	case "down", "j":
		return m.moveGitDiffFile(1)
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
	next := min(max(m.gitDiff.fileIndex+delta, 0), len(m.gitDiff.files)-1)
	if next == m.gitDiff.fileIndex {
		return m, nil
	}
	m.gitDiff.fileIndex = next
	return m, m.loadSelectedFileDiff()
}

func (m *dashboard) scrollGitDiff(delta int) {
	m.gitDiff.diffOffset = min(max(m.gitDiff.diffOffset+delta, 0), m.maximumGitDiffOffset())
}

func (m dashboard) gitDiffPageSize() int {
	return max(m.dimensions().bodyHeight-2, 1)
}

func (m dashboard) maximumGitDiffOffset() int {
	lineCount := len(m.gitDiff.diffLines)
	if m.gitDiff.split {
		lineCount = len(splitGitDiffRows(m.gitDiff.diffLines))
	}
	return max(lineCount-m.gitDiffPageSize(), 0)
}

func (m dashboard) renderGitDiffPanes(leftWidth, rightWidth, height int) []string {
	left := m.renderGitChangedFiles(leftWidth, height)
	right := m.renderGitFileDiff(rightWidth, height)
	separator := " " + m.styles.divider.Render("│") + " "
	lines := make([]string, 0, height)
	for row := range height {
		leftLine, rightLine := "", ""
		if row < len(left) {
			leftLine = left[row]
		}
		if row < len(right) {
			rightLine = right[row]
		}
		lines = append(lines, pad(truncate(leftLine, leftWidth), leftWidth)+separator+truncate(rightLine, rightWidth))
	}
	return lines
}

func (m dashboard) renderGitChangedFiles(width, height int) []string {
	title := " Changes · " + displayText(m.gitDiff.target.Name) + " "
	header := m.styles.paneTitleActive.Render(truncate(title, width))
	header += m.styles.tabRail.Render(strings.Repeat("─", max(width-lipgloss.Width(header), 0)))
	lines := []string{header, ""}
	switch {
	case m.gitDiff.filesPending:
		return append(lines, m.styles.empty.Render("  Loading changed files…"))
	case m.gitDiff.err != "" && len(m.gitDiff.files) == 0:
		return append(lines, m.styles.errorText.Render("  "+displayText(m.gitDiff.err)))
	case len(m.gitDiff.files) == 0:
		return append(lines, m.styles.empty.Render("  No changed files"))
	}
	rows := gitDiffTreeRows(m.gitDiff.files)
	available := max(height-len(lines), 0)
	selectedRow := 0
	for index, row := range rows {
		if row.fileIndex == m.gitDiff.fileIndex {
			selectedRow = index
			break
		}
	}
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
		return m.styles.navigationRoot.Render(truncate(strings.Repeat("  ", row.depth)+"▾ "+displayText(row.name)+"/", width))
	}
	file := m.gitDiff.files[row.fileIndex]
	status := gitChangedFileStatus(file)
	indicator := " "
	style := m.styles.navigationItem
	if row.fileIndex == m.gitDiff.fileIndex {
		indicator = "▌"
		style = m.styles.navigationSelected
	}
	prefix = indicator + prefix
	name := " " + displayText(row.name)
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
	status := string([]byte{file.IndexStatus, file.WorkTreeStatus})
	switch {
	case strings.Contains(status, "U"), status == "AA", status == "DD", strings.Contains(status, "D"):
		return m.styles.diffRemoved
	case status == "??", file.IndexStatus == 'A':
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
	if path != "" {
		title += " · " + displayText(path)
	}
	mode := "inline"
	if m.gitDiff.split {
		mode = "split"
	}
	title += " · " + mode + " "
	header := m.styles.paneTitle.Render(truncate(title, width))
	header += m.styles.tabRail.Render(strings.Repeat("─", max(width-lipgloss.Width(header), 0)))
	lines := []string{header, ""}
	switch {
	case m.gitDiff.diffPending:
		return append(lines, m.styles.empty.Render("  Loading diff…"))
	case m.gitDiff.err != "":
		return append(lines, m.styles.errorText.Render("  "+displayText(m.gitDiff.err)))
	case len(m.gitDiff.files) == 0:
		return append(lines, m.styles.empty.Render("  Select a changed file"))
	}
	capacity := max(height-len(lines), 0)
	start := min(m.gitDiff.diffOffset, m.maximumGitDiffOffset())
	if m.gitDiff.split {
		rows := splitGitDiffRows(m.gitDiff.diffLines)
		end := min(start+capacity, len(rows))
		for _, row := range rows[start:end] {
			lines = append(lines, m.renderGitSplitDiffRow(row, width))
		}
		return lines
	}
	end := min(start+capacity, len(m.gitDiff.diffLines))
	for index, line := range m.gitDiff.diffLines[start:end] {
		lines = append(lines, m.renderGitDiffLineAt(displayText(line), start+index, width))
	}
	return lines
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
		return m.renderGitDiffLineAt(displayText(row.full), row.fullIndex, width)
	}
	leftWidth := max((width-separatorWidth)/2, 1)
	rightWidth := max(width-leftWidth-separatorWidth, 1)
	separator := " " + m.styles.divider.Render("│") + " "
	left := pad(m.renderGitDiffLineAt(displayText(row.left), row.leftIndex, leftWidth), leftWidth)
	right := m.renderGitDiffLineAt(displayText(row.right), row.rightIndex, rightWidth)
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
		result.WriteString(style.Render(displayText(token.value)))
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
	case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
		style, changed = m.styles.diffAddedLine, true
	case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
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
	appendGitDiffTreeRows(root, 0, &rows)
	return rows
}

func appendGitDiffTreeRows(node *gitDiffTreeNode, depth int, rows *[]gitDiffTreeRow) {
	names := make([]string, 0, len(node.children))
	for name := range node.children {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		child := node.children[name]
		directory := len(child.children) > 0
		if directory {
			*rows = append(*rows, gitDiffTreeRow{name: child.name, depth: depth, directory: true, fileIndex: -1})
			appendGitDiffTreeRows(child, depth+1, rows)
			continue
		}
		*rows = append(*rows, gitDiffTreeRow{name: child.name, depth: depth, fileIndex: child.fileIndex})
	}
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
