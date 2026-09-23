package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

const workspaceFileTimeout = 10 * time.Second

const maximumWorkspaceFileBytes = 8 << 20

func readWorkspaceFiles(path string) ([]gitChangedFile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), workspaceFileTimeout)
	defer cancel()
	files := make([]gitChangedFile, 0)
	err := filepath.WalkDir(path, func(filePath string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if filePath != path && entry.IsDir() && strings.HasPrefix(entry.Name(), ".") {
			return filepath.SkipDir
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(path, filePath)
		if err != nil {
			return err
		}
		files = append(files, gitChangedFile{
			Path: filepath.ToSlash(relative), IndexStatus: ' ', WorkTreeStatus: ' ',
		})
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("read workspace files timed out after %s", workspaceFileTimeout)
		}
		return nil, fmt.Errorf("read workspace files: %w", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func readWorkspaceFile(path, filePath string) (string, error) {
	relative := filepath.Clean(filepath.FromSlash(filePath))
	if relative == "." || filepath.IsAbs(relative) || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("read workspace file: invalid path %q", filePath)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return "", fmt.Errorf("open workspace: %w", err)
	}
	defer root.Close()
	openedRoot, err := root.Stat(".")
	if err != nil {
		return "", fmt.Errorf("inspect workspace: %w", err)
	}
	currentRoot, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect workspace: %w", err)
	}
	if !currentRoot.IsDir() || !os.SameFile(openedRoot, currentRoot) {
		return "", fmt.Errorf("read workspace file: workspace changed while opening")
	}
	info, err := root.Lstat(relative)
	if err != nil {
		return "", fmt.Errorf("read workspace file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("read workspace file: %q is not a regular file", filePath)
	}
	// Anchor path resolution to the workspace even if a parent is replaced
	// after listing. Nonblocking open also lets us reject a raced-in FIFO.
	file, err := root.OpenFile(relative, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("read workspace file: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect workspace file: %w", err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return "", fmt.Errorf("read workspace file: %q changed while opening", filePath)
	}
	if opened.Size() > maximumWorkspaceFileBytes {
		return "", fmt.Errorf("read workspace file: exceeds %d MiB preview limit", maximumWorkspaceFileBytes>>20)
	}
	// The file can grow after Stat, so enforce the limit on the read as well.
	content, err := io.ReadAll(io.LimitReader(file, maximumWorkspaceFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("read workspace file: %w", err)
	}
	if len(content) > maximumWorkspaceFileBytes {
		return "", fmt.Errorf("read workspace file: exceeds %d MiB preview limit", maximumWorkspaceFileBytes>>20)
	}
	return string(content), nil
}

func normalizeWorkspaceFileLines(content string) []string {
	lines := strings.Split(content, "\n")
	for index, line := range lines {
		lines[index] = expandWorkspaceFileTabs(line)
	}
	return lines
}

func expandWorkspaceFileTabs(line string) string {
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
