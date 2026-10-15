package agenthooks

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/opspresso/romty/internal/testutil"
	"github.com/opspresso/romty/internal/version"
)

// assertFileHolds fails unless path still holds exactly want.
func assertFileHolds(t *testing.T, path string, want []byte, message string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatal(message)
	}
}

func TestDetectReportsOnlyAvailableAgentsAsPending(t *testing.T) {
	testutil.UseReleaseBuild(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	testutil.Set(t, &findExecutable, func(name string) (string, error) {
		if name == "claude" {
			return "/usr/local/bin/claude", nil
		}
		return "", errors.New("not found")
	})
	testutil.Set(t, &userHomeDirectory, func() (string, error) { return home, nil })

	statuses := Detect()
	if len(statuses) != 4 || statuses[0].State != StateMissing ||
		statuses[1].State != StateUnavailable || statuses[2].State != StateUnavailable || statuses[3].State != StateUnavailable {
		t.Fatalf("Detect() = %#v, want missing Claude and unavailable Codex, OpenCode and Kiro", statuses)
	}
	if got := Pending(statuses); !slices.Equal(got, []Provider{ProviderClaude}) {
		t.Fatalf("Pending() = %v, want Claude", got)
	}
}

func TestInstallMergesAndUpdatesRomtyHooks(t *testing.T) {
	testutil.UseReleaseBuild(t)
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

	results, err = Install([]Provider{ProviderClaude})
	if err != nil || len(results) != 1 || results[0].Action != ActionUnchanged {
		t.Fatalf("second Install() = (%#v, %v), want unchanged", results, err)
	}
	assertFileHolds(t, settings, data, "idempotent Install() rewrote current settings")
}

func TestInstallCreatesCodexHooksAndHonorsCodexHome(t *testing.T) {
	testutil.UseReleaseBuild(t)
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

func TestInstallCreatesOpenCodePluginAndHonorsConfigDirectory(t *testing.T) {
	testutil.UseReleaseBuild(t)
	directory := filepath.Join(t.TempDir(), "custom-opencode")
	t.Setenv("OPENCODE_CONFIG_DIR", directory)
	testutil.Set(t, &findRomtyExecutable, func() (string, error) { return "/usr/local/bin/romty", nil })
	// Detect consults PATH, which does not hold opencode on a clean machine.
	testutil.Set(t, &findExecutable, func(name string) (string, error) {
		if name == "opencode" {
			return "/usr/local/bin/opencode", nil
		}
		return "", errors.New("not found")
	})

	path := filepath.Join(directory, "plugins", "romty.js")
	results, err := Install([]Provider{ProviderOpenCode})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if len(results) != 1 || results[0].Action != ActionInstalled || results[0].Path != path {
		t.Fatalf("Install() = %#v", results)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, pluginMarker) || !strings.Contains(text, `const romty = "/usr/local/bin/romty"`) {
		t.Fatalf("plugin does not name the running romty:\n%s", text)
	}

	// A second install of the same build is idempotent.
	results, err = Install([]Provider{ProviderOpenCode})
	if err != nil || len(results) != 1 || results[0].Action != ActionUnchanged {
		t.Fatalf("second Install() = (%#v, %v), want unchanged", results, err)
	}
	assertFileHolds(t, path, data, "idempotent Install() rewrote the current plugin")

	// Moving romty makes the plugin name a stale path, which is an update.
	findRomtyExecutable = func() (string, error) { return "/opt/romty", nil }
	statuses := Detect()
	if len(statuses) != 4 || statuses[2].Provider != ProviderOpenCode || statuses[2].State != StateOutdated {
		t.Fatalf("Detect() = %#v, want OpenCode outdated", statuses)
	}
}

func TestOpenCodePluginRefusesToOverwriteAForeignFile(t *testing.T) {
	testutil.UseReleaseBuild(t)
	directory := filepath.Join(t.TempDir(), "opencode")
	t.Setenv("OPENCODE_CONFIG_DIR", directory)
	path := filepath.Join(directory, "plugins", "romty.js")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := []byte("export const Mine = async () => ({})\n")
	if err := os.WriteFile(path, foreign, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Install([]Provider{ProviderOpenCode}); err == nil || !strings.Contains(err.Error(), "not a romty-managed file") {
		t.Fatalf("Install() error = %v, want a refusal", err)
	}
	assertFileHolds(t, path, foreign, "Install() overwrote a file it did not write")
}

func TestDesiredCommandQuotesTheRunningExecutable(t *testing.T) {
	testutil.Set(t, &findRomtyExecutable, func() (string, error) {
		return "/opt/Romty App/romty's", nil
	})

	got, err := desiredCommand(ProviderCodex)
	if err != nil {
		t.Fatalf("desiredCommand() error = %v", err)
	}
	if want := `'/opt/Romty App/romty'"'"'s' hook codex`; got != want {
		t.Fatalf("desiredCommand() = %q, want %q", got, want)
	}
}

func TestInstallRefusesMalformedSettingsWithoutChangingThem(t *testing.T) {
	testutil.UseReleaseBuild(t)
	for _, probe := range []struct{ name, settings, error string }{
		{name: "hooks", settings: `{"hooks":[]}`, error: "hooks must be an object"},
		{name: "event", settings: `{"hooks":{"Stop":[{"hooks":"not-an-array"}]}}`, error: "entry hooks must be an array"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", home)
			path := filepath.Join(home, "settings.json")
			if err := os.WriteFile(path, []byte(probe.settings), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Install([]Provider{ProviderClaude}); err == nil || !strings.Contains(err.Error(), probe.error) {
				t.Fatalf("Install() error = %v, want %q", err, probe.error)
			}
			assertFileHolds(t, path, []byte(probe.settings), "Install() changed malformed settings")
		})
	}
}

func TestInstallPreservesASettingsSymlink(t *testing.T) {
	testutil.UseReleaseBuild(t)
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
	testutil.Set(t, &version.Value, "")
	testutil.Set(t, &findExecutable, func(string) (string, error) { return "/usr/local/bin/agent", nil })
	testutil.Set(t, &findRomtyExecutable, func() (string, error) {
		t.Fatal("development hook detection resolved the temporary executable")
		return "", nil
	})

	statuses := Detect()
	if len(statuses) != 4 || slices.ContainsFunc(statuses, func(status Status) bool { return status.State != StateDevelopment }) {
		t.Fatalf("Detect() = %#v, want development state for every agent", statuses)
	}
	if pending := Pending(statuses); len(pending) != 0 {
		t.Fatalf("Pending() = %v, want no development hooks", pending)
	}
	if _, err := Install([]Provider{ProviderClaude}); !errors.Is(err, ErrDevelopmentBuild) {
		t.Fatalf("Install() error = %v, want ErrDevelopmentBuild", err)
	}
}

// The daemon reads a provider's transcripts out of the same directory the hook
// settings live in. Two answers to "where is it" is two things to keep in step.
func TestConfigDirectoryFollowsTheProviderOverride(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/somewhere/else")
	directory, err := ConfigDirectory(ProviderClaude)
	if err != nil {
		t.Fatalf("ConfigDirectory() error = %v", err)
	}
	if directory != "/somewhere/else" {
		t.Fatalf("ConfigDirectory() = %q, want the override", directory)
	}

	t.Setenv("CLAUDE_CONFIG_DIR", "")
	testutil.Set(t, &userHomeDirectory, func() (string, error) { return "/home/example", nil })
	directory, err = ConfigDirectory(ProviderClaude)
	if err != nil {
		t.Fatalf("ConfigDirectory() error = %v", err)
	}
	if directory != "/home/example/.claude" {
		t.Fatalf("ConfigDirectory() = %q, want the default under home", directory)
	}

	// The settings file it installs sits inside the directory it reports.
	value, ok := findDefinition(ProviderClaude)
	if !ok {
		t.Fatal("no definition for Claude Code")
	}
	path, err := configurationPath(value)
	if err != nil {
		t.Fatalf("configurationPath() error = %v", err)
	}
	if filepath.Dir(path) != directory {
		t.Fatalf("settings path %q is not inside %q", path, directory)
	}

	if _, err := ConfigDirectory("nonsense"); err == nil {
		t.Error("ConfigDirectory accepted a provider romty does not have")
	}
}
