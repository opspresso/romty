package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadGitChangedFilesBuildsCompleteSortedList(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repository := initializeDiffRepository(t)

	writeDiffFile(t, repository, "modified.txt", "before\nafter\n")
	writeDiffFile(t, repository, "staged.txt", "before\nstaged\n")
	runGit(t, "-C", repository, "add", "staged.txt")
	writeDiffFile(t, repository, "nested/untracked.txt", "new\n")

	files, err := readGitChangedFiles(repository)
	if err != nil {
		t.Fatalf("readGitChangedFiles() error = %v", err)
	}
	want := []gitChangedFile{
		{Path: "modified.txt", IndexStatus: ' ', WorkTreeStatus: 'M'},
		{Path: "nested/untracked.txt", IndexStatus: '?', WorkTreeStatus: '?'},
		{Path: "staged.txt", IndexStatus: 'M', WorkTreeStatus: ' '},
	}
	if len(files) != len(want) {
		t.Fatalf("changed files = %#v, want %#v", files, want)
	}
	for index := range want {
		if files[index] != want[index] {
			t.Fatalf("changed file %d = %#v, want %#v", index, files[index], want[index])
		}
	}
}

func TestReadGitChangedFilesPreservesRenameSource(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repository := initializeDiffRepository(t)
	runGit(t, "-C", repository, "mv", "staged.txt", "renamed.txt")

	files, err := readGitChangedFiles(repository)
	if err != nil || len(files) != 1 {
		t.Fatalf("renamed files = (%#v, %v), want one file", files, err)
	}
	if files[0].Path != "renamed.txt" || files[0].OldPath != "staged.txt" || files[0].IndexStatus != 'R' {
		t.Fatalf("renamed file = %#v", files[0])
	}
}

func TestReadGitFileDiffCombinesStagedAndUnstagedChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repository := initializeDiffRepository(t)
	writeDiffFile(t, repository, "modified.txt", "before\nstaged\n")
	runGit(t, "-C", repository, "add", "modified.txt")
	writeDiffFile(t, repository, "modified.txt", "before\nstaged\nunstaged\n")

	files, err := readGitChangedFiles(repository)
	if err != nil {
		t.Fatalf("readGitChangedFiles() error = %v", err)
	}
	file := changedFileAt(t, files, "modified.txt")
	diff, err := readGitFileDiff(repository, file)
	if err != nil {
		t.Fatalf("readGitFileDiff() error = %v", err)
	}
	for _, fragment := range []string{"Staged changes", "+staged", "Unstaged changes", "+unstaged"} {
		if !strings.Contains(diff, fragment) {
			t.Fatalf("combined diff does not contain %q:\n%s", fragment, diff)
		}
	}
}

func TestReadGitFileDiffShowsUntrackedFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repository := initializeDiffRepository(t)
	writeDiffFile(t, repository, "nested/new file.txt", "first\nsecond\n")

	files, err := readGitChangedFiles(repository)
	if err != nil {
		t.Fatalf("readGitChangedFiles() error = %v", err)
	}
	diff, err := readGitFileDiff(repository, changedFileAt(t, files, "nested/new file.txt"))
	if err != nil {
		t.Fatalf("readGitFileDiff() error = %v", err)
	}
	if !strings.Contains(diff, "new file.txt") || !strings.Contains(diff, "+first") || !strings.Contains(diff, "+second") {
		t.Fatalf("untracked diff =\n%s", diff)
	}
}

func initializeDiffRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	runGit(t, "init", "--initial-branch=main", repository)
	writeDiffFile(t, repository, "modified.txt", "before\n")
	writeDiffFile(t, repository, "staged.txt", "before\n")
	runGit(t, "-C", repository, "add", ".")
	runGit(t, "-C", repository, "-c", "user.name=romty", "-c", "user.email=romty@example.com", "commit", "-m", "initial")
	return repository
}

func writeDiffFile(t *testing.T, repository, name, content string) {
	t.Helper()
	path := filepath.Join(repository, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create Git fixture directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write Git fixture: %v", err)
	}
}

func changedFileAt(t *testing.T, files []gitChangedFile, path string) gitChangedFile {
	t.Helper()
	for _, file := range files {
		if file.Path == path {
			return file
		}
	}
	t.Fatalf("changed files %#v do not contain %q", files, path)
	return gitChangedFile{}
}
