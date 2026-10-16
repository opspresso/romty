package agenthooks

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/opspresso/romty/internal/testutil"
)

func TestCodexTitleAddsIdentityWithoutChangingOtherSettings(t *testing.T) {
	for name, input := range map[string]string{
		"defaults":             "# keep me\nmodel = 'local-model'\n[features]\nhooks = false\n",
		"table":                "# before\n[tui] # title table\ntheme = 'dark'\nterminal_title = [\n  # selected fields\n  'model', 'project-name',\n] # after\n[other]\nvalue = 3\n",
		"dotted":               `tui.terminal_title = ['model'] # preserved`,
		"quoted":               `"tui"."terminal_title" = ['model']`,
		"inline":               `tui = { terminal_title = ['model'], theme = 'dark' }`,
		"inline without title": `tui = { theme = 'dark' }`,
		"empty inline":         `tui = {}`,
		"table without title":  "[tui]\ntheme = 'dark'\n",
		"table at eof":         "[tui]",
		"dotted without title": "tui.theme = 'dark'\n",
	} {
		t.Run(name, func(t *testing.T) {
			data, changed, err := normalizeCodexTitle([]byte(input))
			if err != nil || !changed {
				t.Fatalf("normalize = %s, %v, %v", data, changed, err)
			}
			var before, after map[string]any
			if err := toml.Unmarshal([]byte(input), &before); err != nil {
				t.Fatal(err)
			}
			if err := toml.Unmarshal(data, &after); err != nil {
				t.Fatal(err)
			}
			tui := after["tui"].(map[string]any)
			if tui["terminal_title"].([]any)[0] != "thread-id" {
				t.Fatalf("identity was not added first: %s", data)
			}
			previous, _ := before["tui"].(map[string]any)
			if _, exists := previous["terminal_title"]; !exists {
				want := []any{"thread-id", "activity", "thread-title", "project-name"}
				if !reflect.DeepEqual(tui["terminal_title"], want) {
					t.Fatalf("default title = %v, want %v", tui["terminal_title"], want)
				}
			}
			if previous, ok := before["tui"].(map[string]any); ok {
				if title, exists := previous["terminal_title"]; exists {
					tui["terminal_title"] = title
				} else {
					delete(tui, "terminal_title")
				}
			} else {
				delete(after, "tui")
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("unrelated configuration changed: before=%v after=%v", before, after)
			}
			for _, comment := range []string{"# keep me", "# title table", "# selected fields", "# after", "# preserved"} {
				if bytes.Contains([]byte(input), []byte(comment)) && !bytes.Contains(data, []byte(comment)) {
					t.Fatalf("lost %s", comment)
				}
			}
			again, changed, err := normalizeCodexTitle(data)
			if err != nil || changed || !bytes.Equal(again, data) {
				t.Fatal("title installation is not idempotent")
			}
		})
	}
}

func TestCodexTitleRepairsLegacyNameWithoutChangingOtherText(t *testing.T) {
	for name, tc := range map[string]struct{ input, want string }{
		"old defaults": {
			`tui.terminal_title = ["thread-id", "activity", "thread-name", "project-name"]`,
			`tui.terminal_title = ["thread-id", "activity", "thread-title", "project-name"]`,
		},
		"table": {
			"[tui]\nterminal_title = [\n  'thread-id',\n  # thread-name comment\n  'thread-name',\n] # preserve\n[other]\nname = 'thread-name'\n",
			"[tui]\nterminal_title = [\n  'thread-id',\n  # thread-name comment\n  \"thread-title\",\n] # preserve\n[other]\nname = 'thread-name'\n",
		},
		"quoted": {
			`"tui"."terminal_title" = ["thread-id", "thread-name"]`,
			`"tui"."terminal_title" = ["thread-id", "thread-title"]`,
		},
		"inline": {
			`tui = { terminal_title = ["session-id", "thread-name"], theme = "thread-name" }`,
			`tui = { terminal_title = ["session-id", "thread-title"], theme = "thread-name" }`,
		},
		"escaped": {
			`tui.terminal_title = ["thread-id", "thread-\u006eame"]`,
			`tui.terminal_title = ["thread-id", "thread-title"]`,
		},
		"multiple": {
			`tui.terminal_title = ["thread-name", "thread-id", 'thread-name']`,
			`tui.terminal_title = ["thread-title", "thread-id", "thread-title"]`,
		},
		"without identity": {
			`tui.terminal_title = ["thread-name", "model"]`,
			`tui.terminal_title = ["thread-id", "thread-title", "model"]`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, changed, err := normalizeCodexTitle([]byte(tc.input))
			if err != nil || !changed || string(got) != tc.want {
				t.Fatalf("normalize = %s, %v, %v; want %s", got, changed, err, tc.want)
			}
			again, changed, err := normalizeCodexTitle(got)
			if err != nil || changed || !bytes.Equal(again, got) {
				t.Fatal("title repair is not idempotent")
			}
		})
	}
}

func TestCodexTitleRepairMarksInstalledHooksOutdated(t *testing.T) {
	testutil.UseReleaseBuild(t)
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if _, err := Install([]Provider{ProviderCodex}); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(home, "hooks.json")
	hooks, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte(`tui.terminal_title = ["thread-id", "activity", "thread-name", "project-name"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	definition, _ := findDefinition(ProviderCodex)
	if got := inspect(definition); got.State != StateOutdated || got.Err != nil {
		t.Fatalf("legacy title status = %#v, want outdated", got)
	}
	results, err := Install([]Provider{ProviderCodex})
	if err != nil || len(results) != 1 || results[0].Action != ActionUpdated {
		t.Fatalf("repair = %v, %v; want updated", results, err)
	}
	assertFileHolds(t, path, []byte(`tui.terminal_title = ["thread-id", "activity", "thread-title", "project-name"]`), "legacy title was not repaired")
	assertFileHolds(t, hooksPath, hooks, "title repair changed installed hooks")
	if got := inspect(definition); got.State != StateCurrent || got.Err != nil {
		t.Fatalf("repaired title status = %#v, want current", got)
	}
}

func TestCodexTitlePreservesExplicitChoicesAndRefusesMalformedConfig(t *testing.T) {
	for _, input := range []string{`tui.terminal_title = []`, `tui.terminal_title = ["session-id", "model"]`} {
		got, changed, err := normalizeCodexTitle([]byte(input))
		if err != nil || changed || string(got) != input {
			t.Fatalf("explicit selection changed: %s, %v", got, err)
		}
	}
	testutil.UseReleaseBuild(t)
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	broken := []byte("[tui\n")
	if err := os.WriteFile(filepath.Join(home, "config.toml"), broken, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Install([]Provider{ProviderCodex}); err == nil {
		t.Fatal("malformed TOML was accepted")
	}
	if _, err := os.Stat(filepath.Join(home, "hooks.json")); !os.IsNotExist(err) {
		t.Fatalf("hooks were written before configuration validation: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(home, "config.toml")); !bytes.Equal(data, broken) {
		t.Fatal("malformed config was overwritten")
	}
}
