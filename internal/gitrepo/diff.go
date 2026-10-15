package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

const diffTimeout = 10 * time.Second

// ChangedFile is one path git status reports, with its index and work tree
// status letters as git prints them.
type ChangedFile struct {
	Path           string
	OldPath        string
	IndexStatus    byte
	WorkTreeStatus byte
}

// ChangedFiles lists the changed and untracked files of the repository at
// path, sorted by path.
func ChangedFiles(path string) ([]ChangedFile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
	defer cancel()
	output, err := stdout(ctx, path, readEnvironment,
		"status", "--porcelain=v1", "-z", "--untracked-files=all")
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("read changed files timed out after %s", diffTimeout)
	}
	if err != nil {
		return nil, fmt.Errorf("read changed files: %w", err)
	}
	files, err := parseChangedFiles(string(output))
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func parseChangedFiles(output string) ([]ChangedFile, error) {
	records := strings.Split(output, "\x00")
	files := make([]ChangedFile, 0, len(records))
	for index := 0; index < len(records); index++ {
		record := records[index]
		if record == "" {
			continue
		}
		if len(record) < 4 || record[2] != ' ' {
			return nil, fmt.Errorf("parse Git status: malformed record %q", record)
		}
		file := ChangedFile{
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

// FileDiff is the staged and unstaged diff of one changed file, or the whole
// content of an untracked one.
func FileDiff(path string, file ChangedFile) (string, error) {
	sections := make([]string, 0, 2)
	paths := []string{file.Path}
	if file.OldPath != "" {
		paths = append([]string{file.OldPath}, paths...)
	}
	if file.IndexStatus == '?' && file.WorkTreeStatus == '?' {
		output, err := runDiff(path, true, "--no-index", "--", "/dev/null", file.Path)
		if err != nil {
			return "", fmt.Errorf("read untracked file diff: %w", err)
		}
		return diffSection("Untracked file", output), nil
	}
	if file.IndexStatus != ' ' {
		arguments := append([]string{"--cached", "--"}, paths...)
		output, err := runDiff(path, false, arguments...)
		if err != nil {
			return "", fmt.Errorf("read staged diff: %w", err)
		}
		if output != "" {
			sections = append(sections, diffSection("Staged changes", output))
		}
	}
	if file.WorkTreeStatus != ' ' {
		arguments := append([]string{"--"}, paths...)
		output, err := runDiff(path, false, arguments...)
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

func runDiff(path string, allowDifferenceExit bool, arguments ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
	defer cancel()
	arguments = append([]string{"--literal-pathspecs", "diff", "--no-ext-diff", "--no-textconv", "--no-color"}, arguments...)
	output, err := combinedOutput(ctx, path, readEnvironment, arguments...)
	value := strings.TrimRight(string(output), "\n")
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return value, fmt.Errorf("git diff timed out after %s", diffTimeout)
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
