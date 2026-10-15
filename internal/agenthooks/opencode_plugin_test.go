package agenthooks

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/opspresso/romty/internal/testutil"
)

// pluginHarness drives the generated plugin through OpenCode 2's setup() and
// OpenCode 1's server() with fake contexts. Each report reaches a fake romty
// executable, which appends its input to a log.
const pluginHarness = `
import plugin from %q
const api = process.argv[2]
if (api === "2") {
  const hooks = {}
  let push
  const queue = []
  const ctx = {
    tool: { hook: async (name, fn) => { hooks[name] = fn } },
    event: { subscribe: () => ({ [Symbol.asyncIterator]: () => ({ next: () => queue.length ? Promise.resolve({ value: queue.shift(), done: false }) : new Promise((resolve) => { push = resolve }) }) }) },
  }
  const emit = (type, data) => push ? (push({ value: { type, data }, done: false }), push = undefined) : queue.push({ type, data })
  const cleanup = await plugin.setup(ctx)
  const tick = () => new Promise((resolve) => setTimeout(resolve, 20))
  emit("session.created", { sessionID: "ses-1" }); await tick()
  emit("session.created", { sessionID: "child", parentID: "ses-1" }); await tick()
  emit("session.execution.started", { sessionID: "ses-1" }); await tick()
  await hooks["execute.before"]({ sessionID: "ses-1", tool: "shell", id: "call-1" }); await tick()
  emit("permission.asked", { sessionID: "ses-1", tool: { callID: "call-1" } }); await tick()
  await hooks["execute.before"]({ sessionID: "child", tool: "read", id: "call-2" }); await tick()
  await hooks["execute.after"]({ sessionID: "ses-1", tool: "shell", id: "call-1", status: "error" }); await tick()
  emit("session.execution.interrupted", { sessionID: "ses-1" }); await tick()
  emit("session.execution.succeeded", { sessionID: "ses-1" }); await tick()
  cleanup()
} else {
  const hooks = await plugin.server()
  await hooks["chat.message"]({ sessionID: "ses-1" })
  await hooks["tool.execute.before"]({ sessionID: "ses-1", tool: "task", callID: "call-1" })
  await hooks.event({ event: { type: "permission.asked", properties: { sessionID: "ses-1", callID: "call-1" } } })
  await hooks.event({ event: { type: "session.created", properties: { info: { id: "child", parentID: "ses-1" } } } })
  await hooks["chat.message"]({ sessionID: "child" })
  await hooks["tool.execute.before"]({ sessionID: "child", tool: "read", callID: "call-2" })
  await hooks.event({ event: { type: "session.idle", properties: { sessionID: "child" } } })
  await hooks.event({ event: { type: "session.deleted", properties: { info: { id: "child", parentID: "ses-1" } } } })
  await hooks.event({ event: { type: "session.idle", properties: { sessionID: "ses-1" } } })
}
// Reporters receive their input over a pipe; stay alive until each one wrote.
const { readdirSync } = await import("node:fs")
for (let waited = 0; waited < 10000 && readdirSync(process.argv[3]).length < Number(process.argv[4]); waited += 20) {
  await new Promise((resolve) => setTimeout(resolve, 20))
}
`

func runPluginHarness(t *testing.T, api string, want int) []map[string]string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to load the generated plugin")
	}
	directory := t.TempDir()
	reportsDirectory := filepath.Join(directory, "reports")
	if err := os.Mkdir(reportsDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	romty := filepath.Join(directory, "romty")
	// One file per report, renamed into place once complete: detached
	// reporters run concurrently.
	script := "#!/bin/sh\ncat > '" + directory + "'/partial.$$ && mv '" + directory + "'/partial.$$ '" + reportsDirectory + "'/report.$$\n"
	if err := os.WriteFile(romty, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	testutil.Set(t, &findRomtyExecutable, func() (string, error) { return romty, nil })
	contents, err := pluginContents(definition{})
	if err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(directory, "romty.mjs")
	harness := filepath.Join(directory, "harness.mjs")
	if err := os.WriteFile(plugin, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(harness, []byte(strings.Replace(pluginHarness, "%q", strconvQuote(plugin), 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(node, harness, api, reportsDirectory, strconv.Itoa(want))
	command.Env = append(os.Environ(), "ROMTY_TAB_ID=tab-1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("node harness error = %v\n%s", err, output)
	}
	files, _ := filepath.Glob(filepath.Join(reportsDirectory, "report.*"))
	var reports []map[string]string
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var report map[string]string
		if err := json.Unmarshal(data, &report); err != nil {
			t.Fatalf("report %q: %v", data, err)
		}
		reports = append(reports, report)
	}
	return reports
}

func strconvQuote(value string) string {
	quoted, _ := json.Marshal(value)
	return string(quoted)
}

func hookEvents(reports []map[string]string) []string {
	var events []string
	for _, report := range reports {
		events = append(events, report["hook_event_name"])
	}
	return events
}

// OpenCode 2 reports one execution outcome per turn and tool call IDs, and a
// child session's events belong to its parent's subagent tool.
func TestOpenCodePluginReportsOpenCode2Lifecycle(t *testing.T) {
	want := []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PermissionRequest", "PostToolUseFailure", "Interrupt", "Stop"}
	reports := runPluginHarness(t, "2", len(want))
	got := hookEvents(reports)
	// Detached reporters can finish out of order; the set and the tool IDs matter.
	if !reflect.DeepEqual(sorted(got), sorted(want)) {
		t.Fatalf("OpenCode 2 events = %v, want %v", got, want)
	}
	for _, report := range reports {
		if report["session_id"] != "ses-1" {
			t.Fatalf("child session reported: %v", report)
		}
		switch report["hook_event_name"] {
		case "PreToolUse", "PermissionRequest", "PostToolUseFailure":
			if report["tool_use_id"] != "call-1" {
				t.Fatalf("%s tool_use_id = %q, want call-1", report["hook_event_name"], report["tool_use_id"])
			}
		}
	}
}

// OpenCode 1 reports tool call IDs, and a child session cannot rebind the tab
// or end its parent's turn.
func TestOpenCodePluginReportsOpenCode1Hooks(t *testing.T) {
	want := []string{"UserPromptSubmit", "PreToolUse", "PermissionRequest", "Stop"}
	reports := runPluginHarness(t, "1", len(want))
	got := hookEvents(reports)
	if !reflect.DeepEqual(sorted(got), sorted(want)) {
		t.Fatalf("OpenCode 1 events = %v, want %v", got, want)
	}
	for _, report := range reports {
		if report["session_id"] != "ses-1" {
			t.Fatalf("child session reported: %v", report)
		}
		switch report["hook_event_name"] {
		case "PreToolUse", "PermissionRequest":
			if report["tool_use_id"] != "call-1" {
				t.Fatalf("%s tool_use_id = %q, want call-1", report["hook_event_name"], report["tool_use_id"])
			}
		}
	}
}

func sorted(values []string) []string {
	return slices.Sorted(slices.Values(values))
}
