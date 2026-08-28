package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/opspresso/romty/internal/model"
)

func TestConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	want := Config{
		LeftWidth:         24,
		GitDiffView:       "split",
		SoundOnDone:       true,
		SoundOnWaiting:    true,
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
		got.LastTabID != want.LastTabID || !got.SoundOnDone || !got.SoundOnWaiting {
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
	if err := os.WriteFile(path, []byte(`{"left_width":24,"mouse_passthrough":true,"sound_on_done":true,"git_diff_view":"split","future":{"enabled":true}}`+"\n"), 0o600); err != nil {
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
	if !written.SoundOnDone {
		t.Fatal("adjusting the pane width erased sound_on_done")
	}

	// The other way round: the mouse setting persists and leaves the width be.
	updated, save = value.Update(key('m', "m"))
	value = updated.(dashboard)
	if save == nil {
		t.Fatal("toggling the scrollback mouse produced no save")
	}
	save()
	if written, err = loadConfig(path); err != nil {
		t.Fatalf("loadConfig() after toggle error = %v", err)
	}
	if !written.ScrollbackMouse || written.LeftWidth != 25 {
		t.Fatalf("after toggle = (scrollback_mouse %v, left_width %d), want the setting saved beside the width",
			written.ScrollbackMouse, written.LeftWidth)
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

// The known-field list is read from the struct's own tags. A hand-kept copy
// falls behind, and a field missing from it is filed as unknown — so turning
// that setting off would drop it from the document under omitempty and then
// restore the old value from the copy.
func TestConfigKnowsEveryFieldItDeclares(t *testing.T) {
	document := []byte(`{"left_width":24,"mouse_passthrough":true,"scrollback_mouse":true,` +
		`"sound_on_done":true,"sound_on_waiting":true,"git_diff_view":"split",` +
		`"last_workspace_path":"/w","last_tab_id":"tab-1","custom":{"kept":true}}`)
	var value Config
	if err := json.Unmarshal(document, &value); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(value.unknownFields) != 1 {
		t.Fatalf("unknown fields = %v, want only the custom one", value.unknownFields)
	}
	if _, ok := value.unknownFields["custom"]; !ok {
		t.Fatalf("unknown fields = %v, want the custom one kept", value.unknownFields)
	}

	// Turning every boolean off must leave them off, not restore them from the
	// copy of the document they were read from.
	value.MousePassthrough, value.ScrollbackMouse = false, false
	value.SoundOnDone, value.SoundOnWaiting = false, false
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var written map[string]json.RawMessage
	if err := json.Unmarshal(data, &written); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	for _, name := range []string{"mouse_passthrough", "scrollback_mouse", "sound_on_done", "sound_on_waiting"} {
		if _, present := written[name]; present {
			t.Errorf("%s is still in the document after being turned off: %s", name, data)
		}
	}
	if _, present := written["custom"]; !present {
		t.Errorf("custom field was not preserved: %s", data)
	}
}

// The modal, its keys, its clicks and its hover all read one list of settings.
// A row that draws but answers nothing, or answers for its neighbour, is what
// four separate lists produce.
func TestConfigRowsDrawAndAnswerTogether(t *testing.T) {
	value := newDashboard(&fakeBackend{}, model.Snapshot{})
	value.width, value.height = 120, 40
	value.modal = configModal
	rows := configRows()

	lines := plainRows(value.renderModal(value.width, value.dimensions().bodyHeight))
	for index, row := range rows {
		// One border row above the content.
		drawn := lines[1+index]
		label := row.label(value)
		if !strings.Contains(drawn, label) {
			t.Errorf("row %d draws %q, want %q", index, drawn, label)
		}
		if !strings.Contains(drawn, row.hint) {
			t.Errorf("row %d draws %q, want the %q key", index, drawn, row.hint)
		}
		if row.action == nil {
			continue
		}
		if _, _, ok := value.runConfigRow(index); !ok {
			t.Errorf("row %d draws an action a click cannot reach", index)
		}
		for _, key := range row.keys {
			if _, _, ok := value.runConfigKey(key); !ok {
				t.Errorf("row %d key %q runs nothing", index, key)
			}
		}
	}

	// The pointer highlights exactly the rows that exist.
	if _, ok := value.browseIndexAtContentRow(0); ok {
		t.Error("the picker answered for a config row")
	}
	if target := value.hoverTargetAtRow(len(rows) - 1); target.kind != hoverConfigRow {
		t.Errorf("last row hover = %v, want a config row", target.kind)
	}
	if target := value.hoverTargetAtRow(len(rows)); target.kind == hoverConfigRow {
		t.Error("a row past the last setting was highlighted as one")
	}
}

// hoverTargetAtRow is what hoverTargetAt resolves for a content row of the open
// modal, addressed by row rather than by pixel.
func (m dashboard) hoverTargetAtRow(row int) hoverTarget {
	width, height := max(m.width, 40), m.dimensions().bodyHeight
	lines := m.renderModal(width, height)
	left := max((width-lipgloss.Width(lines[0]))/2, 0) + 3
	_, top := m.modalContentOrigin(width, height)
	return m.hoverTargetAt(tea.Mouse{X: left, Y: top + row})
}
