package gitrepo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PullChildren pulls the immediate workspace directories of root. A .git file
// also identifies a linked worktree. Each failure is reported without stopping
// the remaining repositories; Git protects changes that would be overwritten.
func PullChildren(ctx context.Context, root string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", fmt.Errorf("read root: %w", err)
	}
	var result strings.Builder
	succeeded, failed := 0, 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result.String(), err
		}
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
			if !os.IsNotExist(err) {
				failed++
				fmt.Fprintf(&result, "\n%s: ERROR inspecting .git: %v\n", entry.Name(), err)
			}
			continue
		}
		output, err := Run(ctx, path, ActionPull)
		if ctx.Err() != nil {
			return result.String(), ctx.Err()
		}
		status := "OK"
		if err != nil {
			status = "ERROR"
			failed++
		} else {
			succeeded++
		}
		fmt.Fprintf(&result, "\n%s: %s\n", entry.Name(), status)
		// Keep a large diffstat from hiding results for later repositories.
		const maxRepositoryOutput = 16 << 10
		if len(output) > maxRepositoryOutput {
			if err != nil {
				output = "[output truncated]\n" + output[len(output)-maxRepositoryOutput:]
			} else {
				output = output[:maxRepositoryOutput] + "\n[output truncated]"
			}
		}
		if output != "" {
			fmt.Fprintln(&result, output)
		}
		if err != nil {
			fmt.Fprintf(&result, "Error: %v\n", err)
		}
	}
	return fmt.Sprintf("%d succeeded, %d failed.\n%s", succeeded, failed, result.String()), nil
}
