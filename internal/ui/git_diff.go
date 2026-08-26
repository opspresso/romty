package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

const gitDiffTimeout = 10 * time.Second

type gitChangedFile struct {
	Path           string
	OldPath        string
	IndexStatus    byte
	WorkTreeStatus byte
}

func readGitChangedFiles(path string) ([]gitChangedFile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitDiffTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "git", "-C", path, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	output, err := command.Output()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("read changed files timed out after %s", gitDiffTimeout)
	}
	if err != nil {
		return nil, fmt.Errorf("read changed files: %w", err)
	}
	files, err := parseGitChangedFiles(string(output))
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func parseGitChangedFiles(output string) ([]gitChangedFile, error) {
	records := strings.Split(output, "\x00")
	files := make([]gitChangedFile, 0, len(records))
	for index := 0; index < len(records); index++ {
		record := records[index]
		if record == "" {
			continue
		}
		if len(record) < 4 || record[2] != ' ' {
			return nil, fmt.Errorf("parse Git status: malformed record %q", record)
		}
		file := gitChangedFile{
			Path:           record[3:],
			IndexStatus:    record[0],
			WorkTreeStatus: record[1],
		}
		if file.IndexStatus == 'R' || file.IndexStatus == 'C' ||
			file.WorkTreeStatus == 'R' || file.WorkTreeStatus == 'C' {
			index++
			if index >= len(records) || records[index] == "" {
				return nil, fmt.Errorf("parse Git status: missing source path for %q", file.Path)
			}
			file.OldPath = records[index]
		}
		files = append(files, file)
	}
	return files, nil
}

func readGitFileDiff(path string, file gitChangedFile) (string, error) {
	sections := make([]string, 0, 2)
	if file.IndexStatus == '?' && file.WorkTreeStatus == '?' {
		output, err := runGitDiff(path, true, "diff", "--no-index", "--no-ext-diff", "--no-color", "--", "/dev/null", file.Path)
		if err != nil {
			return "", fmt.Errorf("read untracked file diff: %w", err)
		}
		return diffSection("Untracked file", output), nil
	}
	if file.IndexStatus != ' ' {
		output, err := runGitDiff(path, false, "diff", "--cached", "--no-ext-diff", "--no-color", "--", file.Path)
		if err != nil {
			return "", fmt.Errorf("read staged diff: %w", err)
		}
		if output != "" {
			sections = append(sections, diffSection("Staged changes", output))
		}
	}
	if file.WorkTreeStatus != ' ' {
		output, err := runGitDiff(path, false, "diff", "--no-ext-diff", "--no-color", "--", file.Path)
		if err != nil {
			return "", fmt.Errorf("read unstaged diff: %w", err)
		}
		if output != "" {
			sections = append(sections, diffSection("Unstaged changes", output))
		}
	}
	if len(sections) == 0 {
		return "No textual changes.", nil
	}
	return strings.Join(sections, "\n\n"), nil
}

func runGitDiff(path string, allowDifferenceExit bool, arguments ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitDiffTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "git", append([]string{"-C", path}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	output, err := command.CombinedOutput()
	value := strings.TrimRight(string(output), "\n")
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return value, fmt.Errorf("Git diff timed out after %s", gitDiffTimeout)
	}
	var exitError *exec.ExitError
	if allowDifferenceExit && errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return value, nil
	}
	return value, err
}

func diffSection(title, output string) string {
	if output == "" {
		return title
	}
	return title + "\n" + output
}
