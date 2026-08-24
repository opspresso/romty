package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	gitCommandTimeout = 2 * time.Second
	gitStatusWorkers  = 4
)

func gitBehind(path string) int {
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitCommandTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, "git", "-C", path, "rev-list", "--count", "HEAD..@{upstream}").Output()
	if err != nil {
		return 0
	}
	behind, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || behind < 1 {
		return 0
	}
	return behind
}

func gitBehindWorkspaces(paths []string) map[string]int {
	unique := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if path != "" {
			unique[path] = struct{}{}
		}
	}
	jobs := make(chan string, len(unique))
	for path := range unique {
		jobs <- path
	}
	close(jobs)

	result := make(map[string]int)
	var mu sync.Mutex
	var workers sync.WaitGroup
	for range min(len(unique), gitStatusWorkers) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for path := range jobs {
				behind := gitBehind(path)
				if behind > 0 {
					mu.Lock()
					result[path] = behind
					mu.Unlock()
				}
			}
		}()
	}
	workers.Wait()
	return result
}
