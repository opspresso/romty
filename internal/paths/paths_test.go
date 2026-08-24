package paths_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opspresso/romty/internal/paths"
	"github.com/opspresso/romty/internal/testutil"
)

func TestResolveIncludesConfigPath(t *testing.T) {
	// Not t.TempDir: on macOS the per-user temporary directory alone leaves a
	// daemon socket at the kernel's ceiling, which Resolve now refuses.
	directory := testutil.ShortTempDir(t)
	t.Setenv("ROMTY_HOME", directory)

	got, err := paths.Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	want := filepath.Join(directory, "config.json")
	if got.Config != want {
		t.Fatalf("Config = %q, want %q", got.Config, want)
	}
}

// A unix socket path has a hard ceiling in the kernel — 104 bytes on macOS,
// 108 on Linux — and ROMTY_HOME is documented for development and isolated
// testing, which is exactly where a deep path comes from. Everything romty
// does goes through that socket, so all three commands failed, each with
// `invalid argument` from bind or connect: no length, no limit, and nothing
// naming the one setting that causes it.
func TestResolveRejectsADirectoryTooDeepForTheSocket(t *testing.T) {
	deep := filepath.Join(t.TempDir(), strings.Repeat("d", 200))
	t.Setenv("ROMTY_HOME", deep)

	_, err := paths.Resolve()
	if err == nil {
		t.Fatal("Resolve() accepted a directory no unix socket can live in")
	}
	for _, want := range []string{"ROMTY_HOME", "unix socket"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to mention %q", err, want)
		}
	}
}

// The default romty home must clear the limit on its own, or romty would
// refuse to start for anyone who never set ROMTY_HOME.
func TestResolveAcceptsTheDefaultDirectory(t *testing.T) {
	t.Setenv("ROMTY_HOME", "")
	if _, err := os.UserConfigDir(); err != nil {
		t.Skipf("no user config directory here: %v", err)
	}

	if _, err := paths.Resolve(); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
}
