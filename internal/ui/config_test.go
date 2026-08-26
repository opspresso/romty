package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/opspresso/romty/internal/model"
)

func TestConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	want := Config{
		LeftWidth:         24,
		GitDiffView:       "split",
		LastWorkspacePath: "/projects/romty",
		LastTabID:         "tab-2",
	}

	if err := saveConfig(path, want); err != nil {
		t.Fatalf("saveConfig() error = %v", err)
	}
	got, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if got.LeftWidth != want.LeftWidth || got.MousePassthrough != want.MousePassthrough ||
		got.GitDiffView != want.GitDiffView || got.LastWorkspacePath != want.LastWorkspacePath ||
		got.LastTabID != want.LastTabID {
		t.Fatalf("loadConfig() = %#v, want %#v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestConfigRejectsUnknownGitDiffView(t *testing.T) {
	err := saveConfig(filepath.Join(t.TempDir(), "config.json"), Config{GitDiffView: "sideways"})
	if err == nil || !strings.Contains(err.Error(), "git_diff_view") {
		t.Fatalf("saveConfig() error = %v, want invalid git_diff_view", err)
	}
}

func TestConfigMissingFileUsesResponsiveWidth(t *testing.T) {
	got, err := loadConfig(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if got.LeftWidth != 0 {
		t.Fatalf("loadConfig() = %#v, want responsive width", got)
	}
}

// Saving used to rebuild the document from dashboard fields, so a setting
// nobody remembered to copy across was erased the moment the user touched the
// pane width — and an older romty would erase what a newer one had written.
func TestAdjustingOneSettingKeepsTheRest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"left_width":24,"mouse_passthrough":true,"git_diff_view":"split","future":{"enabled":true}}`+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	loaded, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	value := newDashboardWithConfig(&fakeBackend{}, model.Snapshot{}, path, loaded)
	value.width = 120
	value.height = 40
	updated, _ := value.Update(key(',', ","))
	value = updated.(dashboard)
	updated, save := value.Update(key(tea.KeyRight, ""))
	value = updated.(dashboard)
	if save == nil {
		t.Fatal("adjusting the width produced no save")
	}
	save()

	written, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig() after save error = %v", err)
	}
	if written.LeftWidth != 25 {
		t.Fatalf("left_width = %d, want the adjusted 25", written.LeftWidth)
	}
	if !written.MousePassthrough {
		t.Fatal("adjusting the pane width erased mouse_passthrough")
	}
	if written.GitDiffView != "split" {
		t.Fatalf("adjusting the pane width changed git_diff_view to %q", written.GitDiffView)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() after save error = %v", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("Unmarshal() after save error = %v", err)
	}
	future, ok := document["future"]
	if !ok {
		t.Fatal("adjusting the pane width erased an unknown setting")
	}
	var setting struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(future, &setting); err != nil {
		t.Fatalf("Unmarshal() future setting error = %v", err)
	}
	if !setting.Enabled {
		t.Fatal("adjusting the pane width changed an unknown setting")
	}
}
