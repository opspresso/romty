package gitrepo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opspresso/romty/internal/testutil"
)

func TestReadGitChangedFilesBuildsCompleteSortedList(t *testing.T) {
	repository := initializeDiffRepository(t)

	testutil.WriteFile(t, repository, "modified.txt", "before\nafter\n")
	testutil.WriteFile(t, repository, "staged.txt", "before\nstaged\n")
	testutil.RunGit(t, "-C", repository, "add", "staged.txt")
	testutil.WriteFile(t, repository, "nested/untracked.txt", "new\n")

	files, err := ChangedFiles(repository)
	if err != nil {
		t.Fatalf("ChangedFiles() error = %v", err)
	}
	want := []ChangedFile{
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
	repository := initializeDiffRepository(t)
	testutil.RunGit(t, "-C", repository, "mv", "staged.txt", "renamed.txt")

	files, err := ChangedFiles(repository)
	if err != nil || len(files) != 1 {
		t.Fatalf("renamed files = (%#v, %v), want one file", files, err)
	}
	if files[0].Path != "renamed.txt" || files[0].OldPath != "staged.txt" || files[0].IndexStatus != 'R' {
		t.Fatalf("renamed file = %#v", files[0])
	}
	diff, err := FileDiff(repository, files[0])
	if err != nil || !strings.Contains(diff, "rename from staged.txt") || !strings.Contains(diff, "rename to renamed.txt") {
		t.Fatalf("renamed file diff = (%q, %v)", diff, err)
	}
}

func TestReadGitFileDiffCombinesStagedAndUnstagedChanges(t *testing.T) {
	repository := initializeDiffRepository(t)
	testutil.WriteFile(t, repository, "modified.txt", "before\nstaged\n")
	testutil.RunGit(t, "-C", repository, "add", "modified.txt")
	testutil.WriteFile(t, repository, "modified.txt", "before\nstaged\nunstaged\n")

	files, err := ChangedFiles(repository)
	if err != nil {
		t.Fatalf("ChangedFiles() error = %v", err)
	}
	file := changedFileAt(t, files, "modified.txt")
	diff, err := FileDiff(repository, file)
	if err != nil {
		t.Fatalf("FileDiff() error = %v", err)
	}
	for _, fragment := range []string{"Staged changes", "+staged", "Unstaged changes", "+unstaged"} {
		if !strings.Contains(diff, fragment) {
			t.Fatalf("combined diff does not contain %q:\n%s", fragment, diff)
		}
	}
}

func TestReadGitFileDiffShowsUntrackedFile(t *testing.T) {
	repository := initializeDiffRepository(t)
	testutil.WriteFile(t, repository, "nested/new file.txt", "first\nsecond\n")

	files, err := ChangedFiles(repository)
	if err != nil {
		t.Fatalf("ChangedFiles() error = %v", err)
	}
	diff, err := FileDiff(repository, changedFileAt(t, files, "nested/new file.txt"))
	if err != nil {
		t.Fatalf("FileDiff() error = %v", err)
	}
	if !strings.Contains(diff, "new file.txt") || !strings.Contains(diff, "+first") || !strings.Contains(diff, "+second") {
		t.Fatalf("untracked diff =\n%s", diff)
	}
}

func TestReadGitFileDiffDoesNotRunTextconv(t *testing.T) {
	repository := initializeDiffRepository(t)
	testutil.WriteFile(t, repository, ".gitattributes", "*.txt diff=romty-test\n")
	testutil.WriteFile(t, repository, "converter.sh", "printf invoked > converter-ran\ncat \"$1\"\n")
	testutil.RunGit(t, "-C", repository, "config", "diff.romty-test.textconv", "sh ./converter.sh")
	testutil.WriteFile(t, repository, "modified.txt", "after\n")
	diff, err := FileDiff(repository, ChangedFile{Path: "modified.txt", IndexStatus: ' ', WorkTreeStatus: 'M'})
	if err != nil || !strings.Contains(diff, "+after") {
		t.Fatalf("read diff: %q, %v", diff, err)
	}
	if _, err := os.Stat(filepath.Join(repository, "converter-ran")); !os.IsNotExist(err) {
		t.Fatalf("read-only diff ran the textconv command: %v", err)
	}
}

func TestReadGitFileDiffTreatsFilenameLiterally(t *testing.T) {
	repository := initializeDiffRepository(t)
	for _, name := range []string{"[ab].txt", "a.txt"} {
		testutil.WriteFile(t, repository, name, "before\n")
	}
	testutil.RunGit(t, "-C", repository, "add", ".")
	testutil.RunGit(t, "-C", repository, "commit", "-m", "filenames")
	testutil.WriteFile(t, repository, "[ab].txt", "selected\n")
	testutil.WriteFile(t, repository, "a.txt", "unselected\n")
	diff, err := FileDiff(repository, ChangedFile{Path: "[ab].txt", IndexStatus: ' ', WorkTreeStatus: 'M'})
	if err != nil || !strings.Contains(diff, "+selected") || strings.Contains(diff, "+unselected") {
		t.Fatalf("filename interpreted as a pattern: %q, %v", diff, err)
	}
}

func initializeDiffRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	testutil.RunGit(t, "init", "--initial-branch=main", repository)
	testutil.WriteFile(t, repository, "modified.txt", "before\n")
	testutil.WriteFile(t, repository, "staged.txt", "before\n")
	testutil.RunGit(t, "-C", repository, "add", ".")
	testutil.RunGit(t, "-C", repository, "commit", "-m", "initial")
	return repository
}

func changedFileAt(t *testing.T, files []ChangedFile, path string) ChangedFile {
	t.Helper()
	for _, file := range files {
		if file.Path == path {
			return file
		}
	}
	t.Fatalf("changed files %#v do not contain %q", files, path)
	return ChangedFile{}
}
