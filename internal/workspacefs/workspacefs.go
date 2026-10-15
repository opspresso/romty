// Package workspacefs reads the filesystem for the TUI: the directories the
// root picker offers, and the files of a workspace for the file view. File
// reads stay inside the workspace even when a directory in it is replaced by
// a symlink while the file is being opened.
package workspacefs

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
)

const (
	// listTimeout bounds walking a workspace.
	listTimeout = 10 * time.Second
	// maxFileBytes bounds the file a preview reads.
	maxFileBytes = 8 << 20
)

// List returns every regular file under path as a slash-separated relative
// path, sorted. It skips dot-directories but keeps dot-files, and does not
// follow symlinks.
func List(path string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()
	files := make([]string, 0)
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
		files = append(files, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("read workspace files timed out after %s", listTimeout)
		}
		return nil, fmt.Errorf("read workspace files: %w", err)
	}
	sort.Strings(files)
	return files, nil
}

// Read returns the content of filePath, a path List returned, under the
// workspace at path. It refuses a path that leaves the workspace, a file that
// is not regular, a file or workspace replaced while it was being opened, and
// a file larger than the preview limit.
func Read(path, filePath string) (string, error) {
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
	if opened.Size() > maxFileBytes {
		return "", fmt.Errorf("read workspace file: exceeds %d MiB preview limit", maxFileBytes>>20)
	}
	// The file can grow after Stat, so enforce the limit on the read as well.
	content, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("read workspace file: %w", err)
	}
	if len(content) > maxFileBytes {
		return "", fmt.Errorf("read workspace file: exceeds %d MiB preview limit", maxFileBytes>>20)
	}
	return string(content), nil
}

// Subdirectories lists the directories under path a root could be picked
// from, sorted.
// Dot-directories are left out: home holds dozens of them and none of them is
// a workspace. Typing a path still reaches those.
func Subdirectories(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if !isDirectory(path, entry) {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

// isDirectory follows a symlink before deciding. A root is stored by its
// resolved path anyway, and a linked directory — a checkout on another volume,
// say — is exactly the kind of place a root is picked from.
func isDirectory(parent string, entry os.DirEntry) bool {
	if entry.IsDir() {
		return true
	}
	if entry.Type()&os.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(filepath.Join(parent, entry.Name()))
	return err == nil && info.IsDir()
}
