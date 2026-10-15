package agenthooks

import (
	"bytes"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// Codex's own TUI emits thread-id in its OSC title. Adding that metadata lets
// romty identify a tab without changing its command, PATH, arguments or backend.
func codexTitleConfiguration() (string, []byte, bool, error) {
	home, err := ConfigDirectory(ProviderCodex)
	if err != nil {
		return "", nil, false, err
	}
	path := filepath.Join(home, "config.toml")
	data, _, err := readConfiguration(path)
	if err != nil {
		return path, nil, false, err
	}
	updated, changed, err := normalizeCodexTitle(data)
	return path, updated, changed, err
}

func normalizeCodexTitle(data []byte) ([]byte, bool, error) {
	var config struct {
		TUI struct {
			TerminalTitle *[]string `toml:"terminal_title"`
		} `toml:"tui"`
	}
	if err := toml.Unmarshal(data, &config); err != nil {
		return nil, false, fmt.Errorf("decode Codex config.toml: %w", err)
	}
	items := config.TUI.TerminalTitle
	// An explicitly empty title list is an opt-out. Keep every selected item.
	if items != nil && (len(*items) == 0 || slices.Contains(*items, "thread-id") || slices.Contains(*items, "session-id")) {
		return data, false, nil
	}
	const defaults = `["thread-id", "activity", "thread-name", "project-name"]`
	var parser unstable.Parser
	parser.Reset(data)
	var table []string
	insertAt := -1
	var updated []byte
	var visit func(*unstable.Node, []string)
	visit = func(node *unstable.Node, parent []string) {
		keys, lastKeyEnd := keyPath(node, parent)
		value := node.Value()
		// The parsed key gives an exact starting point; quoted '=' characters
		// inside keys or string values cannot be mistaken for the assignment.
		start := lastKeyEnd + bytes.IndexByte(data[lastKeyEnd:], '=') + 1
		for start < len(data) && (data[start] == ' ' || data[start] == '\t') {
			start++
		}
		if slices.Equal(keys, []string{"tui", "terminal_title"}) && value.Kind == unstable.Array {
			updated = insertBytes(data, start+1, `"thread-id", `)
		} else if slices.Equal(keys, []string{"tui"}) && value.Kind == unstable.InlineTable && items == nil {
			field := "terminal_title = " + defaults
			if value.Child() != nil {
				field += ", "
			}
			updated = insertBytes(data, start+1, field)
		} else if value.Kind == unstable.InlineTable {
			for it := value.Children(); it.Next(); {
				if it.Node().Kind == unstable.KeyValue {
					visit(it.Node(), keys)
				}
			}
		}
	}
	for parser.NextExpression() {
		node := parser.Expression()
		switch node.Kind {
		case unstable.Table, unstable.ArrayTable:
			var lastKeyEnd int
			table, lastKeyEnd = keyPath(node, nil)
			if node.Kind == unstable.Table && slices.Equal(table, []string{"tui"}) {
				end := bytes.IndexByte(data[lastKeyEnd:], '\n')
				if end < 0 {
					insertAt = len(data)
				} else {
					insertAt = lastKeyEnd + end + 1
				}
			}
		case unstable.KeyValue:
			visit(node, table)
		}
	}
	if err := parser.Error(); err != nil {
		return nil, false, err
	}
	if updated == nil {
		if items != nil {
			return nil, false, fmt.Errorf("cannot locate Codex terminal_title")
		}
		if insertAt >= 0 {
			field := "terminal_title = " + defaults + "\n"
			if insertAt == len(data) && len(data) > 0 && data[len(data)-1] != '\n' {
				field = "\n" + field
			}
			updated = insertBytes(data, insertAt, field)
		} else {
			updated = insertBytes(data, 0, "tui.terminal_title = "+defaults+"\n")
		}
	}
	// Validate the edited document with the stable decoder as well as the AST.
	if err := toml.Unmarshal(updated, &config); err != nil {
		return nil, false, fmt.Errorf("validate Codex title configuration: %w", err)
	}
	return updated, true, nil
}

// keyPath appends the dotted key of a TOML node to parent, and reports the
// byte offset just past the key's last part.
func keyPath(node *unstable.Node, parent []string) ([]string, int) {
	keys := append([]string(nil), parent...)
	end := 0
	for it := node.Key(); it.Next(); {
		key := it.Node()
		keys = append(keys, string(key.Data))
		end = int(key.Raw.Offset + key.Raw.Length)
	}
	return keys, end
}

func insertBytes(data []byte, at int, value string) []byte {
	out := make([]byte, 0, len(data)+len(value))
	out = append(out, data[:at]...)
	out = append(out, value...)
	return append(out, data[at:]...)
}
