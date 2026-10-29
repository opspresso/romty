package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opspresso/romty/internal/testutil"
)

// Without an installed service Start does nothing, so the caller starts the
// daemon; with one it starts the service, and a failure says how to go on.
func TestStartUsesTheInstalledService(t *testing.T) {
	for _, probe := range []struct{ goos, want string }{
		{"darwin", "launchctl kickstart gui/"},
		{"linux", "systemctl --user start romty.service"},
	} {
		t.Run(probe.goos, func(t *testing.T) {
			home := t.TempDir()
			var ran []string
			testutil.Set(t, &Run, func(name string, arguments ...string) ([]byte, error) {
				ran = append(ran, name+" "+strings.Join(arguments, " "))
				return nil, nil
			})
			if installed, err := Start(probe.goos, home); installed || err != nil || len(ran) != 0 {
				t.Fatalf("Start() without a service = (%v, %v, ran %q)", installed, err, ran)
			}
			path, _ := File(probe.goos, home)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			if installed, err := Start(probe.goos, home); !installed || err != nil || len(ran) != 1 || !strings.HasPrefix(ran[0], probe.want) {
				t.Fatalf("Start() with a service = (%v, %v, ran %q)", installed, err, ran)
			}
			testutil.Set(t, &Run, func(string, ...string) ([]byte, error) { return []byte("no such job"), errors.New("exit 1") })
			if _, err := Start(probe.goos, home); err == nil || !strings.Contains(err.Error(), "romty service uninstall") {
				t.Fatalf("failed start = %v, want the way on", err)
			}
		})
	}
}
