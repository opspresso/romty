package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"

	"github.com/opspresso/romty/internal/gitrepo"
	"github.com/opspresso/romty/internal/workspacefs"
)

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
