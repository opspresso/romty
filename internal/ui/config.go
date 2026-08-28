package ui

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/opspresso/romty/internal/jsonfile"
	"github.com/opspresso/romty/internal/sound"
)

const (
	minimumLeftWidth  = 18
	maximumLeftWidth  = 40
	gitDiffViewInline = "inline"
	gitDiffViewSplit  = "split"
)

type Config struct {
	LeftWidth int `json:"left_width,omitempty"`
	// MousePassthrough hands the mouse to applications that ask for it, at the
	// cost of the host terminal's drag selection while they run.
	MousePassthrough bool `json:"mouse_passthrough,omitempty"`
	// ScrollbackMouse keeps the mouse in scrollback rather than returning it to
	// the host. Scrolling then works on a terminal with no alternate scroll, at
	// the cost of the drag selection scrollback otherwise offers.
	ScrollbackMouse   bool   `json:"scrollback_mouse,omitempty"`
	SoundOnDone       bool   `json:"sound_on_done,omitempty"`
	SoundOnWaiting    bool   `json:"sound_on_waiting,omitempty"`
	GitDiffView       string `json:"git_diff_view,omitempty"`
	LastWorkspacePath string `json:"last_workspace_path,omitempty"`
	LastTabID         string `json:"last_tab_id,omitempty"`
	unknownFields     map[string]json.RawMessage
}

// knownConfigFields are the JSON names Config declares, read from its own tags
// rather than listed a second time. A field added to the struct but forgotten
// in a hand-kept list would be filed as unknown, and MarshalJSON would then
// restore the old value of anything omitempty had just dropped — turning a
// setting off would silently turn it back on.
var knownConfigFields = configFieldNames()

func configFieldNames() map[string]struct{} {
	names := make(map[string]struct{})
	value := reflect.TypeFor[Config]()
	for index := range value.NumField() {
		name, _, _ := strings.Cut(value.Field(index).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			names[name] = struct{}{}
		}
	}
	return names
}

func (c *Config) UnmarshalJSON(data []byte) error {
	type configValue Config
	var value configValue
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for name := range knownConfigFields {
		delete(fields, name)
	}
	*c = Config(value)
	c.unknownFields = fields
	return nil
}

func (c Config) MarshalJSON() ([]byte, error) {
	type configValue Config
	data, err := json.Marshal(configValue(c))
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for key, value := range c.unknownFields {
		if _, known := fields[key]; !known {
			fields[key] = value
		}
	}
	return json.Marshal(fields)
}

func loadConfig(path string) (Config, error) {
	if path == "" {
		return Config{}, nil
	}
	value, err := jsonfile.Read[Config](path)
	if err != nil {
		return Config{}, err
	}
	if err := validateConfig(value); err != nil {
		return Config{}, err
	}
	return value, nil
}

func saveConfig(path string, value Config) error {
	if path == "" {
		return nil
	}
	if err := validateConfig(value); err != nil {
		return err
	}
	return jsonfile.Write(path, value)
}

func validateConfig(value Config) error {
	if value.LeftWidth != 0 && (value.LeftWidth < minimumLeftWidth || value.LeftWidth > maximumLeftWidth) {
		return fmt.Errorf("left_width must be between %d and %d", minimumLeftWidth, maximumLeftWidth)
	}
	if value.GitDiffView != "" && value.GitDiffView != gitDiffViewInline && value.GitDiffView != gitDiffViewSplit {
		return fmt.Errorf("git_diff_view must be %q or %q", gitDiffViewInline, gitDiffViewSplit)
	}
	return nil
}

// configRow is one setting in the Config modal: what the row says, the key it
// shows, the keys that run it, and what running it does.
type configRow struct {
	label  func(dashboard) string
	hint   string
	keys   []string
	action func(dashboard) (tea.Model, tea.Cmd)
}

// configRows are the settings in the order the modal draws them. Drawing a row,
// answering its key, answering a click on it and highlighting it under the
// pointer are four readings of one list; keeping four lists is how a setting
// ends up drawn but dead, or answering for the row above it.
//
// The pane width has no action of its own: it is the one setting a click cannot
// express, because there is no single value to click towards. The arrow keys
// and the wheel move it instead.
func configRows() []configRow {
	return []configRow{
		{
			label: func(m dashboard) string { return fmt.Sprintf("Left pane: %d", m.paneWidth()) },
			hint:  "←/→",
		},
		{
			label:  func(m dashboard) string { return "Scrollback mouse: " + onOff(m.scrollbackMouse) },
			hint:   "m",
			keys:   []string{"m"},
			action: dashboard.toggleScrollbackMouse,
		},
		{
			label:  func(m dashboard) string { return "Sound on done: " + onOff(m.soundOnDone) },
			hint:   "d",
			keys:   []string{"d"},
			action: dashboard.toggleSoundOnDone,
		},
		{
			label:  func(m dashboard) string { return "Sound on waiting: " + onOff(m.soundOnWaiting) },
			hint:   "b",
			keys:   []string{"b"},
			action: dashboard.toggleSoundOnWaiting,
		},
		{
			label:  func(dashboard) string { return "Test sound" },
			hint:   "s",
			keys:   []string{"s"},
			action: func(m dashboard) (tea.Model, tea.Cmd) { return m, soundAlert(sound.Done) },
		},
	}
}

// runConfigKey runs the setting a key belongs to, if any does.
func (m dashboard) runConfigKey(name string) (tea.Model, tea.Cmd, bool) {
	for _, row := range configRows() {
		if row.action == nil {
			continue
		}
		for _, key := range row.keys {
			if key == name {
				updated, command := row.action(m)
				return updated, command, true
			}
		}
	}
	return m, nil, false
}

// runConfigRow runs the setting drawn on one content row, if that row has one.
func (m dashboard) runConfigRow(row int) (tea.Model, tea.Cmd, bool) {
	rows := configRows()
	if row < 0 || row >= len(rows) || rows[row].action == nil {
		return m, nil, false
	}
	updated, command := rows[row].action(m)
	return updated, command, true
}
