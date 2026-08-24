package jsonfile_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/opspresso/romty/internal/jsonfile"
)

type document struct {
	Name  string `json:"name"`
	Count int    `json:"count,omitempty"`
}

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	want := document{Name: "projects", Count: 3}

	if err := jsonfile.Write(path, want); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	got, err := jsonfile.Read[document](path)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got != want {
		t.Fatalf("Read() = %#v, want %#v", got, want)
	}
}

// romty starts with no state and no config, and writes both on first use, so
// a file that is not there is the ordinary case rather than a failure.
func TestReadTreatsAMissingFileAsEmpty(t *testing.T) {
	got, err := jsonfile.Read[document](filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("Read() error = %v, want a missing file to read as empty", err)
	}
	if got != (document{}) {
		t.Fatalf("Read() = %#v, want the zero value", got)
	}
}

// A file romty cannot decode has to name itself. The daemon refuses to start
// on one, and an error that says only "unexpected end of JSON input" leaves
// the user with nothing to go on.
func TestReadNamesAFileItCannotDecode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := jsonfile.Read[document](path)
	if err == nil {
		t.Fatal("Read() accepted a file that is not JSON")
	}
	if !strings.Contains(err.Error(), "state.json") {
		t.Fatalf("error = %q, want it to name the file", err)
	}
}

// The state file holds every directory the user works in and the config their
// preferences, and the socket beside them is already narrowed to this user.
// A file left readable by the rest of the machine would undo that.
func TestWriteNarrowsTheFileWhateverTheUmaskIs(t *testing.T) {
	previous := syscall.Umask(0)
	defer syscall.Umask(previous)

	path := filepath.Join(t.TempDir(), "state.json")
	if err := jsonfile.Write(path, document{Name: "projects"}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode = %04o, want 0600", mode)
	}
}

// The replacement goes through a temporary file so a crash mid-write leaves
// the previous version rather than a truncated one. That temporary must not
// outlive the write: the romty home is a directory the user opens.
func TestWriteLeavesNoTemporaryBehind(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")

	for _, value := range []document{{Name: "first"}, {Name: "second", Count: 2}} {
		if err := jsonfile.Write(path, value); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != "state.json" {
			t.Fatalf("Write() left %q behind", entry.Name())
		}
	}

	got, err := jsonfile.Read[document](path)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if want := (document{Name: "second", Count: 2}); got != want {
		t.Fatalf("Read() = %#v, want the second write", got)
	}
}

// A value that cannot be encoded must not cost the version already on disk.
// Encoding before anything is created is what makes that true.
func TestWriteKeepsThePreviousVersionWhenEncodingFails(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	if err := jsonfile.Write(path, document{Name: "projects"}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	// A channel is the shape encoding/json refuses.
	if err := jsonfile.Write(path, map[string]any{"broken": make(chan int)}); err == nil {
		t.Fatal("Write() accepted a value that cannot be encoded")
	}

	got, err := jsonfile.Read[document](path)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if want := (document{Name: "projects"}); got != want {
		t.Fatalf("Read() = %#v, want the version that was already there", got)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory holds %d entries, want the state file alone", len(entries))
	}
}

// Write creates the directory it is pointed at, because the daemon's state and
// the TUI's config are both written before anything else has reason to make
// the romty home.
func TestWriteCreatesTheDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "romty", "config.json")

	if err := jsonfile.Write(path, document{Name: "projects"}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	got, err := jsonfile.Read[document](path)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if want := (document{Name: "projects"}); got != want {
		t.Fatalf("Read() = %#v, want %#v", got, want)
	}
}
