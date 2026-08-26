package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

const (
	maximumHighlightedDiffLines = 5000
	maximumHighlightedDiffBytes = 1 << 20
)

type gitSyntaxToken struct {
	kind  chroma.TokenType
	value string
}

type gitDiffLineSyntax struct {
	old            []gitSyntaxToken
	new            []gitSyntaxToken
	oldHighlighted bool
	newHighlighted bool
}

type gitDiffHunkLine struct {
	index int
	text  string
}

func highlightGitDiffSyntax(path string, lines []string) ([]gitDiffLineSyntax, bool) {
	if len(lines) > maximumHighlightedDiffLines {
		return nil, false
	}
	size := max(len(lines)-1, 0)
	for _, line := range lines {
		size += len(line)
		if size > maximumHighlightedDiffBytes {
			return nil, false
		}
	}
	lexer := lexers.Match(path)
	if lexer == nil || lexer.Config().Name == "fallback" || lexer.Config().Name == "plaintext" {
		return nil, false
	}
	return highlightGitDiffSyntaxWithLexer(lexer, lines)
}

func highlightGitDiffSyntaxWithLexer(lexer chroma.Lexer, lines []string) (syntax []gitDiffLineSyntax, highlighted bool) {
	defer func() {
		if recover() != nil {
			syntax, highlighted = nil, false
		}
	}()
	lexer = chroma.Coalesce(lexer)
	result := make([]gitDiffLineSyntax, len(lines))
	for index := 0; index < len(lines); {
		if !strings.HasPrefix(lines[index], "@@") {
			index++
			continue
		}
		index++
		oldLines, newLines := make([]gitDiffHunkLine, 0), make([]gitDiffHunkLine, 0)
	hunk:
		for index < len(lines) {
			line := lines[index]
			switch {
			case strings.HasPrefix(line, "@@"):
				break hunk
			case strings.HasPrefix(line, " "):
				oldLines = append(oldLines, gitDiffHunkLine{index: index, text: line[1:]})
				newLines = append(newLines, gitDiffHunkLine{index: index, text: line[1:]})
			case isRemovedDiffLine(line):
				oldLines = append(oldLines, gitDiffHunkLine{index: index, text: line[1:]})
			case isAddedDiffLine(line):
				newLines = append(newLines, gitDiffHunkLine{index: index, text: line[1:]})
			case isNoNewlineDiffLine(line):
			default:
				break hunk
			}
			index++
		}
		if !highlightGitDiffSide(lexer, oldLines, result, true) ||
			!highlightGitDiffSide(lexer, newLines, result, false) {
			return nil, false
		}
	}
	return result, true
}

func highlightGitDiffSide(lexer chroma.Lexer, lines []gitDiffHunkLine, result []gitDiffLineSyntax, old bool) bool {
	if len(lines) == 0 {
		return true
	}
	content := make([]string, len(lines))
	for index, line := range lines {
		content[index] = line.text
	}
	iterator, err := lexer.Tokenise(nil, strings.Join(content, "\n"))
	if err != nil {
		return false
	}
	tokens := make([][]gitSyntaxToken, len(lines))
	lineIndex := 0
	for token := iterator(); token != chroma.EOF && lineIndex < len(tokens); token = iterator() {
		parts := strings.Split(token.Value, "\n")
		for partIndex, part := range parts {
			if part != "" && lineIndex < len(tokens) {
				tokens[lineIndex] = append(tokens[lineIndex], gitSyntaxToken{kind: token.Type, value: part})
			}
			if partIndex < len(parts)-1 {
				lineIndex++
			}
		}
	}
	for index, line := range lines {
		if gitSyntaxText(tokens[index]) != line.text {
			return false
		}
		if old {
			result[line.index].old = tokens[index]
			result[line.index].oldHighlighted = true
		} else {
			result[line.index].new = tokens[index]
			result[line.index].newHighlighted = true
		}
	}
	return true
}

func gitSyntaxText(tokens []gitSyntaxToken) string {
	var result strings.Builder
	for _, token := range tokens {
		result.WriteString(token.value)
	}
	return result.String()
}

func (m dashboard) gitSyntaxStyle(token chroma.TokenType) lipgloss.Style {
	switch {
	case token.InCategory(chroma.Comment):
		return m.styles.syntaxComment
	case token.InCategory(chroma.Keyword):
		return m.styles.syntaxKeyword
	case token.InSubCategory(chroma.LiteralString):
		return m.styles.syntaxString
	case token.InSubCategory(chroma.LiteralNumber):
		return m.styles.syntaxNumber
	case token == chroma.NameBuiltin || token == chroma.NameBuiltinPseudo ||
		token == chroma.NameClass || token == chroma.NameDecorator ||
		token == chroma.NameFunction || token == chroma.NameFunctionMagic ||
		token == chroma.NameTag:
		return m.styles.syntaxName
	case token.InCategory(chroma.Operator):
		return m.styles.syntaxOperator
	case token == chroma.Error:
		return m.styles.errorText
	default:
		return m.styles.navigationItem
	}
}
