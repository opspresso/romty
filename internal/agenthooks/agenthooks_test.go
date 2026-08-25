package agenthooks

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/opspresso/romty/internal/version"
)

func TestDetectReportsOnlyAvailableAgentsAsPending(t *testing.T) {
	useReleaseBuild(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	previousFind := findExecutable
	previousHome := userHomeDirectory
	findExecutable = func(name string) (string, error) {
		if name == "claude" {
			return "/usr/local/bin/claude", nil
		}
		return "", errors.New("not found")
	}
	userHomeDirectory = func() (string, error) { return home, nil }
	t.Cleanup(func() {
		findExecutable = previousFind
		userHomeDirectory = previousHome
	})

	statuses := Detect()
	if len(statuses) != 2 || statuses[0].State != StateMissing || statuses[1].State != StateUnavailable {
		t.Fatalf("Detect() = %#v, want missing Claude and unavailable Codex", statuses)
	}
	if got := Pending(statuses); !slices.Equal(got, []Provider{ProviderClaude}) {
		t.Fatalf("Pending() = %v, want Claude", got)
	}
}

func TestInstallMergesAndUpdatesRomtyHooks(t *testing.T) {
	useReleaseBuild(t)
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	settings := filepath.Join(home, "claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := `{
  "theme": "dark",
  "large": 9007199254740993,
  "hooks": {
    "Stop": [{
      "hooks": [
        {"type":"command","command":"custom-hook"},
        {"type":"command","command":"/usr/local/bin/romty","args":["hook","claude","unexpected"],"if":"Bash(*)","async":"yes","asyncRewake":true,"shell":"powershell","timeout":9},
        {"type":"command","command":"romty hook claude","timeout":1}
      ]
    }],
    "OldEvent": [{"hooks":[{"type":"command","command":"romty hook claude","timeout":1}]}]
  }
}`
	if err := os.WriteFile(settings, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	results, err := Install([]Provider{ProviderClaude})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionUpdated {
		t.Fatalf("Install() = %#v, want one update", results)
	}
	data, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"large": 9007199254740993`) || !strings.Contains(text, `"command": "custom-hook"`) {
		t.Fatalf("Install() did not preserve existing settings:\n%s", text)
	}
	if count := strings.Count(text, `hook claude"`); count != len(definitions[0].events) {
		t.Fatalf("romty hook count = %d, want %d:\n%s", count, len(definitions[0].events), text)
	}
	if strings.Contains(text, "OldEvent") || strings.Contains(text, "/usr/local/bin/romty") {
		t.Fatalf("Install() retained obsolete romty hooks:\n%s", text)
	}
	for _, field := range []string{`"args"`, `"if"`, `"async"`, `"asyncRewake"`, `"shell"`} {
		if strings.Contains(text, field) {
			t.Fatalf("Install() retained romty execution field %s:\n%s", field, text)
		}
	}

	before := append([]byte(nil), data...)
	results, err = Install([]Provider{ProviderClaude})
	if err != nil || len(results) != 1 || results[0].Action != ActionUnchanged {
		t.Fatalf("second Install() = (%#v, %v), want unchanged", results, err)
	}
	after, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before, after) {
		t.Fatal("idempotent Install() rewrote current settings")
	}
}

func TestInstallCreatesCodexHooksAndHonorsCodexHome(t *testing.T) {
	useReleaseBuild(t)
	home := filepath.Join(t.TempDir(), "custom-codex")
	t.Setenv("CODEX_HOME", home)

	results, err := Install([]Provider{ProviderCodex})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionInstalled || results[0].Path != filepath.Join(home, "hooks.json") {
		t.Fatalf("Install() = %#v", results)
	}
	data, err := os.ReadFile(filepath.Join(home, "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(data), `hook codex"`); count != len(definitions[1].events) {
		t.Fatalf("Codex hook count = %d, want %d", count, len(definitions[1].events))
	}
}

func TestDesiredCommandQuotesTheRunningExecutable(t *testing.T) {
	previousFind := findRomtyExecutable
	findRomtyExecutable = func() (string, error) {
		return "/opt/Romty App/romty's", nil
	}
	t.Cleanup(func() { findRomtyExecutable = previousFind })

	got, err := desiredCommand(ProviderCodex)
	if err != nil {
		t.Fatalf("desiredCommand() error = %v", err)
	}
	if want := `'/opt/Romty App/romty'"'"'s' hook codex`; got != want {
		t.Fatalf("desiredCommand() = %q, want %q", got, want)
	}
}

func TestInstallRefusesMalformedSettingsWithoutChangingThem(t *testing.T) {
	useReleaseBuild(t)
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	path := filepath.Join(home, "settings.json")
	broken := []byte(`{"hooks":[]}`)
	if err := os.WriteFile(path, broken, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Install([]Provider{ProviderClaude}); err == nil || !strings.Contains(err.Error(), "hooks must be an object") {
		t.Fatalf("Install() error = %v, want invalid hooks", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, broken) {
		t.Fatal("Install() changed malformed settings")
	}
}

func TestInstallRefusesMalformedEventWithoutChangingSettings(t *testing.T) {
	useReleaseBuild(t)
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	path := filepath.Join(home, "settings.json")
	broken := []byte(`{"hooks":{"Stop":[{"hooks":"not-an-array"}]}}`)
	if err := os.WriteFile(path, broken, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Install([]Provider{ProviderClaude}); err == nil || !strings.Contains(err.Error(), "entry hooks must be an array") {
		t.Fatalf("Install() error = %v, want invalid event hooks", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, broken) {
		t.Fatal("Install() changed settings with a malformed event")
	}
}

func TestInstallPreservesASettingsSymlink(t *testing.T) {
	useReleaseBuild(t)
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	target := filepath.Join(home, "dotfiles", "claude.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, "claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, settings); err != nil {
		t.Fatal(err)
	}

	if _, err := Install([]Provider{ProviderClaude}); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	info, err := os.Lstat(settings)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("Install() replaced the settings symlink")
	}
}

func TestDevelopmentBuildDoesNotInspectOrInstallHooks(t *testing.T) {
	originalVersion := version.Value
	originalFind := findExecutable
	originalRomty := findRomtyExecutable
	version.Value = ""
	findExecutable = func(string) (string, error) { return "/usr/local/bin/agent", nil }
	findRomtyExecutable = func() (string, error) {
		t.Fatal("development hook detection resolved the temporary executable")
		return "", nil
	}
	t.Cleanup(func() {
		version.Value = originalVersion
		findExecutable = originalFind
		findRomtyExecutable = originalRomty
	})

	statuses := Detect()
	if len(statuses) != 2 || statuses[0].State != StateDevelopment || statuses[1].State != StateDevelopment {
		t.Fatalf("Detect() = %#v, want development state for both agents", statuses)
	}
	if pending := Pending(statuses); len(pending) != 0 {
		t.Fatalf("Pending() = %v, want no development hooks", pending)
	}
	if _, err := Install([]Provider{ProviderClaude}); !errors.Is(err, ErrDevelopmentBuild) {
		t.Fatalf("Install() error = %v, want ErrDevelopmentBuild", err)
	}
}

func useReleaseBuild(t *testing.T) {
	t.Helper()
	original := version.Value
	version.Value = "0.15.0"
	t.Cleanup(func() { version.Value = original })
}
