package agenthooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/opspresso/romty/internal/jsonfile"
	"github.com/opspresso/romty/internal/version"
)

type Provider string

const (
	ProviderClaude Provider = "claude"
	ProviderCodex  Provider = "codex"
)

type State string

const (
	StateUnavailable State = "unavailable"
	StateDevelopment State = "development"
	StateCurrent     State = "current"
	StateMissing     State = "missing"
	StateOutdated    State = "outdated"
	StateInvalid     State = "invalid"
)

type Status struct {
	Provider Provider
	State    State
	Path     string
	Err      error
}

type Action string

const (
	ActionInstalled Action = "installed"
	ActionUpdated   Action = "updated"
	ActionUnchanged Action = "unchanged"
)

type Result struct {
	Provider Provider
	Action   Action
	Path     string
}

var ErrDevelopmentBuild = errors.New("agent hook installation is disabled for development builds")

type definition struct {
	provider    Provider
	executables []string
	directory   string
	environment string
	filename    string
	events      []string
}

var definitions = []definition{
	{
		provider:    ProviderClaude,
		executables: []string{"claude", "claude-code"},
		directory:   ".claude",
		environment: "CLAUDE_CONFIG_DIR",
		filename:    "settings.json",
		events: []string{
			"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
			"PostToolUseFailure", "PermissionRequest", "Notification", "Elicitation",
			"ElicitationResult", "PreCompact", "PostCompact", "Stop", "StopFailure", "SessionEnd",
		},
	},
	{
		provider:    ProviderCodex,
		executables: []string{"codex"},
		directory:   ".codex",
		environment: "CODEX_HOME",
		filename:    "hooks.json",
		events: []string{
			"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
			"PermissionRequest", "PreCompact", "PostCompact", "Stop", "SessionEnd",
		},
	},
}

var findExecutable = exec.LookPath
var userHomeDirectory = os.UserHomeDir
var findRomtyExecutable = os.Executable

func (p Provider) DisplayName() string {
	switch p {
	case ProviderClaude:
		return "Claude Code"
	case ProviderCodex:
		return "Codex"
	default:
		return string(p)
	}
}

func Detect() []Status {
	statuses := make([]Status, 0, len(definitions))
	for _, value := range definitions {
		if !available(value.executables) {
			statuses = append(statuses, Status{Provider: value.provider, State: StateUnavailable})
			continue
		}
		if !version.IsRelease() {
			statuses = append(statuses, Status{Provider: value.provider, State: StateDevelopment})
			continue
		}
		statuses = append(statuses, inspect(value))
	}
	return statuses
}

func Pending(statuses []Status) []Provider {
	providers := make([]Provider, 0, len(statuses))
	for _, status := range statuses {
		if status.State == StateMissing || status.State == StateOutdated {
			providers = append(providers, status.Provider)
		}
	}
	return providers
}

func Install(providers []Provider) ([]Result, error) {
	if len(providers) > 0 && !version.IsRelease() {
		return nil, ErrDevelopmentBuild
	}
	results := make([]Result, 0, len(providers))
	var failures []error
	for _, provider := range providers {
		value, ok := findDefinition(provider)
		if !ok {
			failures = append(failures, fmt.Errorf("unsupported hook provider %q", provider))
			continue
		}
		result, err := install(value)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s hooks: %w", provider.DisplayName(), err))
			continue
		}
		results = append(results, result)
	}
	return results, errors.Join(failures...)
}

func inspect(value definition) Status {
	path, err := configurationPath(value)
	if err != nil {
		return Status{Provider: value.provider, State: StateInvalid, Err: err}
	}
	data, exists, err := readConfiguration(path)
	if err != nil {
		return Status{Provider: value.provider, State: StateInvalid, Path: path, Err: err}
	}
	if !exists {
		return Status{Provider: value.provider, State: StateMissing, Path: path}
	}
	command, err := desiredCommand(value.provider)
	if err != nil {
		return Status{Provider: value.provider, State: StateInvalid, Path: path, Err: err}
	}
	_, owned, changed, err := normalize(data, value, command)
	if err != nil {
		return Status{Provider: value.provider, State: StateInvalid, Path: path, Err: err}
	}
	state := StateCurrent
	if owned == 0 {
		state = StateMissing
	} else if changed {
		state = StateOutdated
	}
	return Status{Provider: value.provider, State: state, Path: path}
}

func install(value definition) (Result, error) {
	path, err := configurationPath(value)
	if err != nil {
		return Result{}, err
	}
	data, exists, err := readConfiguration(path)
	if err != nil {
		return Result{}, err
	}
	if !exists {
		data = []byte("{}")
	}
	command, err := desiredCommand(value.provider)
	if err != nil {
		return Result{}, err
	}
	document, owned, changed, err := normalize(data, value, command)
	if err != nil {
		return Result{}, err
	}
	action := ActionUnchanged
	if changed {
		target, err := writablePath(path)
		if err != nil {
			return Result{}, err
		}
		if err := jsonfile.Write(target, document); err != nil {
			return Result{}, err
		}
		if owned == 0 {
			action = ActionInstalled
		} else {
			action = ActionUpdated
		}
	}
	return Result{Provider: value.provider, Action: action, Path: path}, nil
}

func normalize(data []byte, value definition, command string) (map[string]any, int, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		return nil, 0, false, fmt.Errorf("decode %s: %w", value.filename, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, 0, false, fmt.Errorf("decode %s: multiple JSON values", value.filename)
		}
		return nil, 0, false, fmt.Errorf("decode %s: %w", value.filename, err)
	}
	if document == nil {
		return nil, 0, false, fmt.Errorf("decode %s: top level must be an object", value.filename)
	}

	hooks, present := document["hooks"]
	if !present {
		hooks = map[string]any{}
		document["hooks"] = hooks
	}
	hookMap, ok := hooks.(map[string]any)
	if !ok {
		return nil, 0, false, fmt.Errorf("decode %s: hooks must be an object", value.filename)
	}

	desired := make(map[string]struct{}, len(value.events))
	for _, event := range value.events {
		desired[event] = struct{}{}
	}
	owned := 0
	changed := !present
	kept := make(map[string]bool, len(desired))
	for event, rawGroups := range hookMap {
		groups, ok := rawGroups.([]any)
		if !ok {
			if _, required := desired[event]; required {
				return nil, 0, false, fmt.Errorf("decode %s: hooks.%s must be an array", value.filename, event)
			}
			continue
		}
		normalized, eventOwned, eventChanged, err := normalizeGroups(groups, event, value.provider, command, desired, kept)
		if err != nil {
			return nil, 0, false, fmt.Errorf("decode %s: hooks.%s %w", value.filename, event, err)
		}
		owned += eventOwned
		if eventChanged {
			changed = true
			if len(normalized) == 0 {
				delete(hookMap, event)
			} else {
				hookMap[event] = normalized
			}
		}
	}
	for _, event := range value.events {
		if kept[event] {
			continue
		}
		hookMap[event] = appendHookGroup(hookMap[event], command)
		changed = true
	}
	return document, owned, changed, nil
}

func normalizeGroups(groups []any, event string, provider Provider, command string, desired map[string]struct{}, kept map[string]bool) ([]any, int, bool, error) {
	result := make([]any, 0, len(groups))
	owned := 0
	changed := false
	_, required := desired[event]
	for _, rawGroup := range groups {
		group, ok := rawGroup.(map[string]any)
		if !ok {
			if required {
				return nil, 0, false, errors.New("entries must be objects")
			}
			result = append(result, rawGroup)
			continue
		}
		rawHandlers, ok := group["hooks"]
		if !ok {
			if required {
				return nil, 0, false, errors.New("entries must contain a hooks array")
			}
			result = append(result, rawGroup)
			continue
		}
		handlers, ok := rawHandlers.([]any)
		if !ok {
			if required {
				return nil, 0, false, errors.New("entry hooks must be an array")
			}
			result = append(result, rawGroup)
			continue
		}
		all := matcherMatchesAll(group)
		normalizedHandlers := make([]any, 0, len(handlers))
		groupChanged := false
		for _, rawHandler := range handlers {
			handler, ok := rawHandler.(map[string]any)
			if !ok {
				if required {
					return nil, 0, false, errors.New("handlers must be objects")
				}
				normalizedHandlers = append(normalizedHandlers, rawHandler)
				continue
			}
			if !romtyHandler(handler, provider, command) {
				normalizedHandlers = append(normalizedHandlers, rawHandler)
				continue
			}
			owned++
			if !required || !all || kept[event] {
				groupChanged = true
				continue
			}
			if normalizeHandler(handler, command) {
				groupChanged = true
			}
			kept[event] = true
			normalizedHandlers = append(normalizedHandlers, handler)
		}
		if groupChanged {
			changed = true
			if len(normalizedHandlers) == 0 {
				continue
			}
			group["hooks"] = normalizedHandlers
		}
		result = append(result, group)
	}
	return result, owned, changed, nil
}

func appendHookGroup(raw any, command string) []any {
	groups, _ := raw.([]any)
	return append(groups, map[string]any{
		"hooks": []any{newHandler(command)},
	})
}

func matcherMatchesAll(group map[string]any) bool {
	matcher, present := group["matcher"]
	if !present {
		return true
	}
	value, ok := matcher.(string)
	return ok && (value == "" || value == "*")
}

func romtyHandler(handler map[string]any, provider Provider, desired string) bool {
	typeName, _ := handler["type"].(string)
	command, _ := handler["command"].(string)
	if typeName != "command" {
		return false
	}
	if arguments, ok := handler["args"].([]any); ok && len(arguments) >= 2 {
		operation, operationOK := arguments[0].(string)
		argumentProvider, providerOK := arguments[1].(string)
		if operationOK && providerOK && operation == "hook" && argumentProvider == string(provider) &&
			filepath.Base(command) == "romty" {
			return true
		}
	}
	if command == desired || command == "romty hook "+string(provider) {
		return true
	}
	suffix := " hook " + string(provider)
	if !strings.HasSuffix(command, suffix) {
		return false
	}
	executable := strings.TrimSpace(strings.TrimSuffix(command, suffix))
	if len(executable) >= 2 && (executable[0] == '\'' && executable[len(executable)-1] == '\'' ||
		executable[0] == '"' && executable[len(executable)-1] == '"') {
		executable = executable[1 : len(executable)-1]
	}
	return filepath.Base(executable) == "romty"
}

func normalizeHandler(handler map[string]any, command string) bool {
	changed := false
	for _, field := range []string{"args", "if", "async", "asyncRewake", "shell"} {
		if _, present := handler[field]; present {
			delete(handler, field)
			changed = true
		}
	}
	if handler["command"] != command {
		handler["command"] = command
		changed = true
	}
	if timeout, ok := handler["timeout"].(json.Number); !ok || timeout.String() != "1" {
		handler["timeout"] = json.Number("1")
		changed = true
	}
	return changed
}

func newHandler(command string) map[string]any {
	return map[string]any{
		"type":    "command",
		"command": command,
		"timeout": json.Number("1"),
	}
}

func desiredCommand(provider Provider) (string, error) {
	executable, err := findRomtyExecutable()
	if err != nil {
		return "", fmt.Errorf("resolve romty executable: %w", err)
	}
	absolute, err := filepath.Abs(executable)
	if err != nil {
		return "", fmt.Errorf("resolve romty executable path: %w", err)
	}
	quoted := "'" + strings.ReplaceAll(absolute, "'", `'"'"'`) + "'"
	return quoted + " hook " + string(provider), nil
}

func available(names []string) bool {
	for _, name := range names {
		if _, err := findExecutable(name); err == nil {
			return true
		}
	}
	return false
}

func findDefinition(provider Provider) (definition, bool) {
	for _, value := range definitions {
		if value.provider == provider {
			return value, true
		}
	}
	return definition{}, false
}

// ConfigDirectory is where a provider keeps its files: the directory its own
// environment variable names, or the default under the user's home. romty reads
// more out of it than the hook settings, and where to look must not be worked
// out in two places that can disagree.
func ConfigDirectory(provider Provider) (string, error) {
	value, ok := findDefinition(provider)
	if !ok {
		return "", fmt.Errorf("unknown agent provider %q", provider)
	}
	return providerDirectory(value)
}

func providerDirectory(value definition) (string, error) {
	directory := os.Getenv(value.environment)
	if directory == "" {
		home, err := userHomeDirectory()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		directory = filepath.Join(home, value.directory)
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("resolve %s directory: %w", value.provider, err)
	}
	return absolute, nil
}

func configurationPath(value definition) (string, error) {
	directory, err := providerDirectory(value)
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, value.filename), nil
}

func readConfiguration(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	return data, true, nil
}

func writablePath(path string) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", filepath.Base(path), err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("%s is not a regular file", path)
		}
		return path, nil
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s symlink: %w", filepath.Base(path), err)
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("inspect %s target: %w", filepath.Base(path), err)
	}
	if !targetInfo.Mode().IsRegular() {
		return "", fmt.Errorf("%s target is not a regular file", path)
	}
	return target, nil
}
