package agenthooks

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/opspresso/romty/internal/testutil"
)

func useKiro(t *testing.T) string {
	t.Helper()
	testutil.UseReleaseBuild(t)
	directory := filepath.Join(t.TempDir(), "custom-kiro")
	t.Setenv("KIRO_HOME", directory)
	testutil.Set(t, &findRomtyExecutable, func() (string, error) { return "/usr/local/bin/romty", nil })
	testutil.Set(t, &findExecutable, func(name string) (string, error) {
		if name == "kiro-cli" {
			return "/usr/local/bin/kiro-cli", nil
		}
		return "", errors.New("not found")
	})
	return directory
}

func TestInstallWritesKiroHookFilesAndHonorsKiroHome(t *testing.T) {
	directory := useKiro(t)
	main := filepath.Join(directory, "hooks", "romty.json")
	sessionEnd := filepath.Join(directory, "hooks", "romty-session-end.json")

	results, err := Install([]Provider{ProviderKiro})
	if err != nil || len(results) != 1 || results[0].Action != ActionInstalled || results[0].Path != main {
		t.Fatalf("Install() = (%#v, %v), want both Kiro files installed", results, err)
	}
	triggers := func(path string) []string {
		t.Helper()
		var file struct {
			Version string `json:"version"`
			Hooks   []struct {
				Description string `json:"description"`
				Trigger     string `json:"trigger"`
				Action      struct {
					Type    string `json:"type"`
					Command string `json:"command"`
				} `json:"action"`
			} `json:"hooks"`
		}
		data, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(data, &file) != nil || file.Version != "v1" {
			t.Fatalf("%s is not a v1 Kiro hook file: %v\n%s", path, err, data)
		}
		var names []string
		for _, hook := range file.Hooks {
			if !strings.Contains(hook.Description, kiroMarker) || hook.Action.Type != "command" ||
				hook.Action.Command != "'/usr/local/bin/romty' hook kiro" {
				t.Fatalf("hook %#v does not run the marked romty command", hook)
			}
			names = append(names, hook.Trigger)
		}
		return names
	}
	// SessionEnd has its own file: a Kiro surface without that trigger may
	// reject the file, and the lifecycle hooks must still load.
	if got := triggers(main); !slices.Equal(got, kiroLifecycleTriggers) {
		t.Fatalf("main triggers = %v, want %v", got, kiroLifecycleTriggers)
	}
	if got := triggers(sessionEnd); !slices.Equal(got, []string{"SessionEnd"}) {
		t.Fatalf("session end triggers = %v", got)
	}

	if results, err = Install([]Provider{ProviderKiro}); err != nil || results[0].Action != ActionUnchanged {
		t.Fatalf("second Install() = (%#v, %v), want unchanged", results, err)
	}

	// A deleted file is restored without rewriting the one that is current.
	data, _ := os.ReadFile(main)
	if err := os.Remove(sessionEnd); err != nil {
		t.Fatal(err)
	}
	if statuses := Detect(); statuses[3].Provider != ProviderKiro || statuses[3].State != StateOutdated {
		t.Fatalf("Detect() = %#v, want Kiro outdated", statuses)
	}
	if results, err = Install([]Provider{ProviderKiro}); err != nil || results[0].Action != ActionUpdated {
		t.Fatalf("repair Install() = (%#v, %v), want updated", results, err)
	}
	assertFileHolds(t, main, data, "repair rewrote the current Kiro hook file")
	if _, err := os.Stat(sessionEnd); err != nil {
		t.Fatalf("repair did not restore %s: %v", sessionEnd, err)
	}
}

func TestKiroHooksRefuseToOverwriteAForeignFile(t *testing.T) {
	directory := useKiro(t)
	path := filepath.Join(directory, "hooks", "romty.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := []byte(`{"version":"v1","hooks":[]}`)
	if err := os.WriteFile(path, foreign, 0o600); err != nil {
		t.Fatal(err)
	}
	if statuses := Detect(); statuses[3].State != StateInvalid {
		t.Fatalf("Detect() = %#v, want the foreign Kiro file invalid", statuses)
	}
	if _, err := Install([]Provider{ProviderKiro}); err == nil {
		t.Fatal("Install() overwrote a Kiro hook file romty did not write")
	}
	assertFileHolds(t, path, foreign, "Install() changed a foreign Kiro hook file")
}
