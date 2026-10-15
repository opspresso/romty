// Package agenthooks installs the status hooks that let an agent tell romty
// what it is doing. It either edits an agent's own settings file in place,
// keeping every hook it did not write, or generates a provider's plugin file
// whole, and refuses to install from a build that has no stable path to name as
// the hook command.
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
	ProviderClaude   Provider = "claude"
	ProviderCodex    Provider = "codex"
	ProviderOpenCode Provider = "opencode"
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

// definition describes where a provider keeps the file that reports its state,
// and what shape that file takes. Claude Code and Codex merge romty command
// handlers into a JSON settings document; OpenCode loads a JavaScript plugin
// from a plugins directory, so its file is generated whole instead.
type definition struct {
	provider    Provider
	executables []string
	directory   string
	environment string
	// filename is the JSON settings file the command handlers are merged into.
	filename string
	events   []string
	// plugin reports that the provider is configured with a generated plugin
	// file rather than merged JSON handlers.
	plugin     bool
	pluginFile string
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
			"PostToolUseFailure", "PermissionRequest", "PermissionDenied", "Notification", "Elicitation",
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
			"PermissionRequest", "PreCompact", "PostCompact", "Stop", "Interrupt", "SessionEnd",
		},
	},
	{
		provider:    ProviderOpenCode,
		executables: []string{"opencode"},
		directory:   ".config/opencode",
		environment: "OPENCODE_CONFIG_DIR",
		plugin:      true,
		pluginFile:  "plugins/romty.js",
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
	case ProviderOpenCode:
		return "OpenCode"
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

// plan is what bringing one provider's hooks up to date would do: the state its
// files are in now and the writes that would make them current. Detect reports
// the state and Install performs the writes, so the two cannot disagree.
type plan struct {
	path   string
	state  State
	writes []fileWrite
}

type fileWrite struct {
	path string
	data []byte
}

func planFor(value definition) (plan, error) {
	if value.plugin {
		return planPlugin(value)
	}
	// Both files are read and validated before either one is written.
	var titleWrite *fileWrite
	if value.provider == ProviderCodex {
		titlePath, data, changed, err := codexTitleConfiguration()
		if err != nil {
			return plan{path: titlePath}, err
		}
		if changed {
			titleWrite = &fileWrite{path: titlePath, data: data}
		}
	}
	path, err := configurationPath(value)
	if err != nil {
		return plan{}, err
	}
	result := plan{path: path}
	data, exists, err := readConfiguration(path)
	if err != nil {
		return result, err
	}
	if !exists {
		data = []byte("{}")
	}
	command, err := desiredCommand(value.provider)
	if err != nil {
		return result, err
	}
	document, owned, changed, err := normalize(data, value, command)
	if err != nil {
		return result, err
	}
	switch {
	case owned == 0:
		result.state = StateMissing
	case changed || titleWrite != nil:
		result.state = StateOutdated
	default:
		result.state = StateCurrent
	}
	if changed {
		encoded, err := json.MarshalIndent(document, "", "  ")
		if err != nil {
			return result, fmt.Errorf("encode %s: %w", value.filename, err)
		}
		result.writes = append(result.writes, fileWrite{path: path, data: append(encoded, '\n')})
	}
	if titleWrite != nil {
		result.writes = append(result.writes, *titleWrite)
	}
	return result, nil
}

func inspect(value definition) Status {
	current, err := planFor(value)
	if err != nil {
		return Status{Provider: value.provider, State: StateInvalid, Path: current.path, Err: err}
	}
	return Status{Provider: value.provider, State: current.state, Path: current.path}
}

func install(value definition) (Result, error) {
	current, err := planFor(value)
	if err != nil {
		return Result{}, err
	}
	// Every target is resolved before the first write, so a refused symlink
	// leaves both files as they were.
	targets := make([]string, len(current.writes))
	for index, write := range current.writes {
		if targets[index], err = writablePath(write.path); err != nil {
			return Result{}, err
		}
	}
	for index, write := range current.writes {
		if err := jsonfile.WriteBytes(targets[index], write.data); err != nil {
			return Result{}, err
		}
	}
	action := ActionUnchanged
	switch current.state {
	case StateMissing:
		action = ActionInstalled
	case StateOutdated:
		action = ActionUpdated
	}
	return Result{Provider: value.provider, Action: action, Path: current.path}, nil
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
	absolute, err := romtyPath()
	if err != nil {
		return "", err
	}
	quoted := "'" + strings.ReplaceAll(absolute, "'", `'"'"'`) + "'"
	return quoted + " hook " + string(provider), nil
}

// romtyPath is the absolute path of the running romty executable, which both a
// command handler and the OpenCode plugin name so an untrusted working
// directory or modified PATH cannot substitute another command.
func romtyPath() (string, error) {
	executable, err := findRomtyExecutable()
	if err != nil {
		return "", fmt.Errorf("resolve romty executable: %w", err)
	}
	absolute, err := filepath.Abs(executable)
	if err != nil {
		return "", fmt.Errorf("resolve romty executable path: %w", err)
	}
	return absolute, nil
}

// pluginMarker identifies a plugin file as romty's own. A user's unrelated
// plugins/romty.js must never be overwritten, and an edited one must be.
const pluginMarker = "romty-opencode-plugin: 1"

// openCodePluginTemplate bridges OpenCode's plugin events to `romty hook
// opencode`. OpenCode has no JSON hook settings; its plugin API is the
// extension point, so the file is generated whole rather than merged. The
// placeholder is the JSON-quoted romty path.
//
// One default export serves both plugin APIs: OpenCode 2 calls setup(),
// OpenCode 1.18.29 and later call server(). OpenCode 2 runs plugins in a
// server process, which is the shared background service unless OpenCode was
// started with --standalone; the daemon drops events from a process outside
// the tab's foreground job, so the service cannot report for a tab.
const openCodePluginTemplate = `// Code generated by romty. DO NOT EDIT.
// ` + pluginMarker + `
import { spawn } from "node:child_process"

const romty = %s

function report(event) {
  if (!process.env.ROMTY_TAB_ID || !event.session_id) return
  try {
    const child = spawn(romty, ["hook", "opencode"], {
      detached: true,
      stdio: ["pipe", "ignore", "ignore"],
    })
    child.on("error", () => {})
    child.stdin.on("error", () => {})
    child.stdin.end(JSON.stringify(event))
    child.unref()
  } catch {
    // A missing tab or daemon is not the agent's problem to report.
  }
}

// OpenCode 2 lifecycle events. A turn ends with exactly one execution outcome.
const sessionEvents = {
  "session.created": "SessionStart",
  "session.execution.started": "UserPromptSubmit",
  "session.retry.scheduled": "UserPromptSubmit",
  "session.compaction.started": "PreCompact",
  "session.compaction.ended": "PostCompact",
  "session.compaction.failed": "PostCompact",
  "session.execution.succeeded": "Stop",
  "session.execution.failed": "StopFailure",
  "session.execution.interrupted": "Interrupt",
  "session.deleted": "SessionEnd",
}

async function setup(ctx) {
  // Child sessions belong to the parent's subagent tool call.
  const children = new Set()
  const send = (sessionID, hook_event_name, fields = {}) => {
    if (!children.has(sessionID)) report({ session_id: sessionID || "", hook_event_name, ...fields })
  }
  await ctx.tool.hook("execute.before", (event) => {
    send(event.sessionID, "PreToolUse", { tool_name: event.tool || "", tool_use_id: event.id || "" })
  })
  await ctx.tool.hook("execute.after", (event) => {
    const hook = event.status === "error" ? "PostToolUseFailure" : "PostToolUse"
    send(event.sessionID, hook, { tool_name: event.tool || "", tool_use_id: event.id || "" })
  })
  const controller = new AbortController()
  void (async () => {
    for await (const event of ctx.event.subscribe({ signal: controller.signal })) {
      const data = event.data || {}
      if (event.type === "session.created" && data.parentID) children.add(data.sessionID)
      if (event.type === "permission.asked") {
        send(data.sessionID, "PermissionRequest", { tool_use_id: (data.tool && data.tool.callID) || "" })
      } else if (sessionEvents[event.type]) {
        send(data.sessionID, sessionEvents[event.type])
      }
      if (event.type === "session.deleted") children.delete(data.sessionID)
    }
  })().catch(() => {})
  return () => controller.abort()
}

// OpenCode 1 hooks. As in setup(), child sessions belong to the parent's
// subagent tool call: a child's SessionStart would otherwise rebind the tab
// to the child, and its idle Stop would end the parent's running turn.
async function server() {
  const children = new Set()
  const send = (sessionID, hook_event_name, fields = {}) => {
    if (!children.has(sessionID)) report({ session_id: sessionID || "", hook_event_name, ...fields })
  }
  return {
    event: async ({ event }) => {
      const properties = event.properties || {}
      const info = properties.info || {}
      switch (event.type) {
        case "session.created":
          if (info.parentID) children.add(info.id)
          send(info.id, "SessionStart")
          break
        case "session.idle":
          send(properties.sessionID, "Stop")
          break
        case "session.deleted":
          send(info.id, "SessionEnd")
          children.delete(info.id)
          break
        case "session.error":
          send(properties.sessionID, "StopFailure")
          break
        case "session.compacted":
          send(properties.sessionID, "PostCompact")
          break
        case "permission.updated":
        case "permission.asked":
          send(properties.sessionID, "PermissionRequest", { tool_use_id: properties.callID || (properties.tool && properties.tool.callID) || "" })
          break
      }
    },
    "chat.message": async (input) => {
      send(input.sessionID, "UserPromptSubmit")
    },
    "tool.execute.before": async (input) => {
      send(input.sessionID, "PreToolUse", { tool_name: input.tool || "", tool_use_id: input.callID || "" })
    },
    "tool.execute.after": async (input) => {
      send(input.sessionID, "PostToolUse", { tool_name: input.tool || "", tool_use_id: input.callID || "" })
    },
    "experimental.session.compacting": async (input) => {
      send(input.sessionID, "PreCompact")
    },
  }
}

export default { id: "romty", setup, server }
`

func pluginPath(value definition) (string, error) {
	directory, err := providerDirectory(value)
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, value.pluginFile), nil
}

func pluginContents(value definition) ([]byte, error) {
	absolute, err := romtyPath()
	if err != nil {
		return nil, err
	}
	quoted, err := json.Marshal(absolute)
	if err != nil {
		return nil, fmt.Errorf("encode romty path: %w", err)
	}
	return []byte(fmt.Sprintf(openCodePluginTemplate, quoted)), nil
}

func planPlugin(value definition) (plan, error) {
	path, err := pluginPath(value)
	if err != nil {
		return plan{}, err
	}
	result := plan{path: path}
	desired, err := pluginContents(value)
	if err != nil {
		return result, err
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		result.state = StateMissing
	case err != nil:
		return result, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	case !bytes.Contains(data, []byte(pluginMarker)):
		return result, fmt.Errorf("%s is not a romty-managed file", filepath.Base(path))
	case bytes.Equal(data, desired):
		result.state = StateCurrent
		return result, nil
	default:
		result.state = StateOutdated
	}
	result.writes = []fileWrite{{path: path, data: desired}}
	return result, nil
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
