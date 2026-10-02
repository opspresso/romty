package agenthooks

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pelletier/go-toml/v2"
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

func TestCodexTitlePreservesExplicitChoicesAndRefusesMalformedConfig(t *testing.T) {
	for _, input := range []string{`tui.terminal_title = []`, `tui.terminal_title = ["session-id", "model"]`} {
		got, changed, err := normalizeCodexTitle([]byte(input))
		if err != nil || changed || string(got) != input {
			t.Fatalf("explicit selection changed: %s, %v", got, err)
		}
	}
	useReleaseBuild(t)
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
