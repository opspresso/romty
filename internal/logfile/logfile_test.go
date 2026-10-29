package logfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/opspresso/romty/internal/testutil"
)

func TestOpenDaemonLogRotatesAtTheSizeLimit(t *testing.T) {
	directory := testutil.ShortTempDir(t)
	path := filepath.Join(directory, "daemon.log")
	if err := os.WriteFile(path, []byte("full"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	testutil.Set(t, &MaxBytes, 4)

	file, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := file.WriteString("new"); err != nil {
		t.Fatalf("WriteString() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	archive, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("ReadFile() archive error = %v", err)
	}
	if string(archive) != "full" {
		t.Fatalf("archive = %q, want %q", archive, "full")
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() current error = %v", err)
	}
	if string(current) != "new" {
		t.Fatalf("current log = %q, want %q", current, "new")
	}
}

func TestOpenDaemonLogKeepsOneArchive(t *testing.T) {
	directory := testutil.ShortTempDir(t)
	path := filepath.Join(directory, "daemon.log")
	if err := os.WriteFile(path, []byte("current"), 0o600); err != nil {
		t.Fatalf("WriteFile() current error = %v", err)
	}
	if err := os.WriteFile(path+".1", []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile() archive error = %v", err)
	}
	testutil.Set(t, &MaxBytes, 1)

	file, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	file.Close()

	archive, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("ReadFile() archive error = %v", err)
	}
	if string(archive) != "current" {
		t.Fatalf("archive = %q, want %q", archive, "current")
	}
}

func TestOpenDaemonLogRejectsAHardLink(t *testing.T) {
	directory := testutil.ShortTempDir(t)
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	path := filepath.Join(directory, "daemon.log")
	if err := os.Link(target, path); err != nil {
		t.Fatalf("Link() error = %v", err)
	}

	if file, err := Open(path); err == nil {
		file.Close()
		t.Fatal("Open() accepted a multiply linked file")
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(contents) != "unchanged" {
		t.Fatalf("hard link target = %q, want unchanged", contents)
	}
}

func TestRotateIfFullArchivesARunningLog(t *testing.T) {
	directory := testutil.ShortTempDir(t)
	path := filepath.Join(directory, "daemon.log")
	if err := os.WriteFile(path, []byte("ab"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	testutil.Set(t, &MaxBytes, 4)
	if file, err := RotateIfFull(path); file != nil || err != nil {
		t.Fatalf("RotateIfFull() under the limit = (%v, %v), want nothing", file, err)
	}
	if err := os.WriteFile(path, []byte("full"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	file, err := RotateIfFull(path)
	if err != nil || file == nil {
		t.Fatalf("RotateIfFull() at the limit = (%v, %v)", file, err)
	}
	file.Close()
	archived, _ := os.ReadFile(path + ".1")
	current, _ := os.ReadFile(path)
	if string(archived) != "full" || len(current) != 0 {
		t.Fatalf("after rotation = (archive %q, current %q)", archived, current)
	}
}
