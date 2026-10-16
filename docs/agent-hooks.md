# Agent status hooks

romty identifies foreground Claude Code, Codex, OpenCode, and Kiro CLI processes in its terminal tabs. This guide explains how to connect each agent's lifecycle reports to its tab, how to verify the result, and what each report means.

Process detection alone gives a tab its agent color and the `●` marker. The phase behind every other marker comes from a reporting channel:

| Agent | Reporting channel | Setup |
|---|---|---|
| Codex | The local app-server's read-only APIs, with hooks for standalone sessions | `romty hooks`, then restart Codex and trust the hooks in `/hooks` |
| Claude Code | Hooks in its user settings | `romty hooks` |
| OpenCode | A generated plugin; OpenCode 2 needs `opencode --standalone` | `romty hooks`, then restart OpenCode |
| Kiro CLI | Generated global hook files (V3 engine) | `romty hooks`, then start a new Kiro session |

Terminal output and silence do not establish Codex or Claude Code completion. Without a reporting channel, their status is unavailable. The [agent marker table](interface.md#agent-and-service-markers) lists every marker with its status rail label and `romty list` phase.

## Install or update

When the TUI starts, romty looks for `claude`, `claude-code`, `codex`, `opencode`, and `kiro-cli` on `PATH`. If a detected agent has missing or outdated romty hooks, the TUI opens a confirmation dialog:

- Press `Enter` to install or update every listed provider.
- Press `Esc` to leave the files unchanged for that run. The status rail then shows `Status unavailable · run romty hooks` for a skipped provider's agent until romty restarts or the agent reports an event.

Run the same installation directly without opening the TUI:

```sh
romty hooks
```

The command reports `installed`, `updated`, or `current` for each detected provider and `not found` for unavailable providers. It writes:

- Claude Code user hooks to `${CLAUDE_CONFIG_DIR:-~/.claude}/settings.json`
- Codex user hooks to `${CODEX_HOME:-~/.codex}/hooks.json`, and the `thread-id` title item to `tui.terminal_title` in `config.toml` in the same directory
- an OpenCode plugin to `${OPENCODE_CONFIG_DIR:-~/.config/opencode}/plugins/romty.js`
- Kiro hook files to `${KIRO_HOME:-~/.kiro}/hooks/romty.json` and `romty-session-end.json`

After installation, each agent picks up the change differently:

- **Claude Code** applies direct user-settings edits automatically, subject to its workspace trust rules.
- **Codex** requires review and trust for a new or changed non-managed hook. Open `/hooks` in Codex after installation, and restart Codex to pick up the title identity field.
- **OpenCode** loads plugins only at startup. Restart it to pick up the plugin.
- **Kiro CLI** reads hook files when a session starts. Start a new session to pick up the files.

Restart an already running agent session if it does not pick up the new hook configuration.

### Release builds only

Hook installation is available only from a tagged release binary, including binaries installed from a tagged Go module. Development binaries produced by local `go run`, `go build`, or `go install` commands neither offer installation in the TUI nor write hook settings through `romty hooks`. This prevents temporary Go build-cache paths from becoming persistent hook commands.

### What the installer changes

Claude Code and Codex are configured by structurally merging JSON instead of replacing the document. Existing settings, unrelated hooks, and unknown fields remain. romty normalizes only command handlers that invoke `romty hook claude` or `romty hook codex`, removes obsolete duplicates, and adds any missing lifecycle events. Malformed JSON or an incompatible `hooks` value is reported and left unchanged.

OpenCode has no JSON hook settings; its plugin API is the extension point. romty generates the whole plugin file, which bridges OpenCode's lifecycle events to `romty hook opencode`. A `plugins/romty.js` that romty did not write is refused rather than overwritten.

Kiro CLI loads standalone hook files. romty generates both files whole; each hook runs `romty hook kiro`. A file at either path that romty did not write is refused rather than overwritten.

All writes are atomic and preserve a settings-file symlink by updating its target.

Installed handlers use the absolute path of the current romty executable so an untrusted working directory or modified `PATH` cannot substitute another command. Re-run `romty hooks` after moving a manually installed romty binary; the installer updates an old executable path.

### Opt-outs

Claude Code can disable all hooks with `disableAllHooks`, and Codex can set `[features].hooks = false`. romty does not override either explicit opt-out. Codex hooks are otherwise enabled by default. See the official [Claude Code hooks reference](https://code.claude.com/docs/en/hooks) and [Codex hooks documentation](https://learn.chatgpt.com/docs/hooks) for precedence and policy controls.

## Verify

1. Start `codex`, `claude`, `opencode`, or `kiro-cli` normally in a new tab after setup.
2. Submit a prompt, answer a question, approve a tool, and interrupt a separate turn.
3. Check both the tab marker and the status rail after each step.

`romty list` reports the same phase after the agent name, for example `claude/working`. The phases include `working`, `waiting_input`, `waiting_approval`, `stopped`, `completed`, and `interrupted`. An agent with unavailable status is listed by name only.

Completion sounds require confirmed `completed` or `error` transitions. `idle`, provisional `stopped`, interruption, and unavailable status do not play them. Enable alerts in the `F3` Config dialog; `d` controls completion alerts, `b` controls input/approval alerts, and `s` tests the done sound.

### Status stays unavailable

A `●` marker with `Status unavailable` on the status rail means romty detected the agent but received no lifecycle report. Check these causes:

- **Hooks are not installed.** Run `romty hooks` and confirm that the provider reports `installed`, `updated`, or `current`.
- **romty is a development build.** `romty hooks` reports `development build; skipped`. Install hooks from a tagged release binary.
- **The agent disabled hooks.** See [Opt-outs](#opt-outs).
- **The agent has not picked up the configuration.** Trust the hooks in Codex's `/hooks`, or restart the agent session.
- **Codex has no usable title identity.** See [Codex native status](#codex-native-status).

## How romty determines status

These states describe the agent runtime. A question written only in ordinary response text has no structured input-request event, and finishing a response is not independent verification that a build or deployment succeeded. romty does not classify the prose to invent those outcomes.

### Codex native status

Run `codex`, `codex resume`, and other Codex commands with their usual arguments. The command, executable, `PATH`, working directory, authentication, and execution policy stay under Codex's control.

`romty hooks` adds `thread-id` to `tui.terminal_title` in `${CODEX_HOME:-~/.codex}/config.toml`. When the list is absent, it installs `["thread-id", "activity", "thread-title", "project-name"]`. Existing title items, comments, and other settings are preserved. An explicitly empty title list stays disabled. If a profile or command-line override replaces the title items, include `thread-id` in that selection too.

Codex Doctor 0.162.1 and 0.163.0 report `thread-name` as unknown even though the TUI supports it. To avoid this diagnostic mismatch, `romty hooks` migrates only the exact older default `["thread-id", "activity", "thread-name", "project-name"]` to the new default. Custom selections keep `thread-name`. The two items differ for unnamed threads: `thread-name` is omitted, while `thread-title` falls back to the thread identifier.

The TUI emits this identifier in its window title; romty already tracks titles without displaying them. Codex 0.160.0 shortens that title item, so romty resolves it only when it uniquely matches a loaded session. It never selects a session by working directory, transcript recency, or a guessed task description.

romty polls the local app-server's read-only APIs: `thread/loaded/list`, `thread/read`, `thread/turns/list` with `itemsView: notLoaded`, and `thread/goal/get`.

- Runtime flags identify pending questions and approvals.
- The latest turn metadata distinguishes completion, interruption, and failure.
- An active goal stays in background work between turns. A paused or blocked goal does not count as completed.

The status reader sends no prompts, commands, approval decisions, resumes, or daemon-start requests.

The API and title metadata are verified against Codex 0.160.0. A missing identity, ambiguous match, unavailable server, or unsupported API produces an unavailable state rather than a guessed completion. Standalone sessions can use correctly bound hooks when the shared server cannot report them. The reader uses the `CODEX_HOME` inherited when the terminal tab was created.

Codex's shared app-server can inherit `ROMTY_TAB_ID` from the terminal that originally started it. Codex hooks therefore route by their session ID and the TUI's title identity, not that inherited tab ID. This also works when the shared server started outside romty and has no tab ID at all.

See the official [Codex app-server API](https://learn.chatgpt.com/docs/app-server) and [Codex hooks](https://learn.chatgpt.com/docs/hooks).

### Claude Code hooks

Hook events map to phases as follows:

- `UserPromptSubmit` begins work.
- `PreToolUse` marks a tool as running. `PostToolUse`, `PostToolUseFailure`, and `PermissionDenied` end it. While any tool still runs, the tab stays `working` (or `planning` in plan mode), even after a parallel tool ends.
- `AskUserQuestion`, `PermissionRequest`, and MCP elicitation identify separate user interactions. Tool-use IDs keep an unanswered question or approval visible while other parallel tools finish. An approval outranks a question, and a question outranks running work.
- `Stop` means the response reached its stop hooks. Another hook can continue the same turn, so romty shows `stopped` without a completion sound until a definitive completion notification arrives. A `Stop` with running background tasks shows `background`.
- The `idle_prompt` notification arrives about 60 seconds after Claude Code finished responding, and only if the user has not typed. After `stopped` or `background`, it confirms `completed`. While the turn is still running, it means the turn ended without a `Stop` hook, and romty shows `interrupted`.
- `StopFailure` reports failure.

Claude Code runs no hook when the user interrupts a turn or rejects a tool at a permission prompt. It writes a `[Request interrupted by user…]` record to the session transcript instead. romty already reads that transcript for [token and cost readings](#token-and-cost-readings). When the newest conversation record is that interrupt and it is newer than the last hook event, the running or waiting turn becomes `interrupted` at the next status refresh. A later prompt or response record cancels it.

Session, prompt/turn, and subagent IDs prevent unrelated events from changing the main agent's state. Scheduled future cron wakeups alone do not mean work is currently running.

The phase stays as reported until the next event: romty does not age a running turn to idle. A tool that the user approves at a permission prompt runs without a new hook event, so the tab shows `waiting_approval` until that tool's `PostToolUse`. A blocking Stop hook can continue work. See the [Claude Code hooks reference](https://code.claude.com/docs/en/hooks).

### OpenCode plugin

The generated plugin supports both plugin APIs from one default export. OpenCode 2 calls its `setup()`; OpenCode 1.18.29 and later call its `server()`.

OpenCode 2 events map to phases as follows:

- `session.execution.started` and `session.retry.scheduled` begin work.
- The `execute.before` and `execute.after` tool hooks start and end a tool call by its call ID. A failed tool reports `PostToolUseFailure`.
- `permission.asked` waits for approval.
- `session.compaction.started` compacts; `session.compaction.ended` or `session.compaction.failed` returns to work.
- Each turn ends with one outcome: `session.execution.succeeded` is `completed`, `session.execution.failed` is `error`, and `session.execution.interrupted` is `interrupted`.
- Child sessions (subagents) do not report; the parent's tool call represents them.

OpenCode 1 maps `chat.message` to `UserPromptSubmit`, `tool.execute.before` and `tool.execute.after` to the tool events with their `callID`, `session.idle` to `Stop`, and `session.error` to `StopFailure`. A child session announced by `session.created` with a `parentID` does not report, so it cannot take the tab from its parent or end the parent's turn.

OpenCode 2 runs plugins in a server process. By default this is the shared background service, which serves every terminal and can carry the `ROMTY_TAB_ID` of the terminal that started it. Each hook therefore reports the process groups above it, and romty accepts an OpenCode event only when the tab's foreground job is among them. Events from the shared service are dropped, so a tab running plain `opencode` shows the terminal-output estimate. Start OpenCode with `opencode --standalone` to run its server inside the tab and get lifecycle phases.

The terminal-output fallback remains an estimate and never confirms completion.

### Kiro CLI hooks

Kiro CLI 3, and Kiro CLI 2 started with `kiro-cli --v3`, run the global hook files. `romty.json` reports `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, and `Stop`. `romty-session-end.json` reports `SessionEnd` and is a separate file: Kiro IDE and the V2 engine do not list that trigger, so a surface that rejects it skips only that file.

Kiro runs hooks from its agent server, a child of the `kiro-cli` process. Each hook therefore reports the process groups above it, and romty accepts a Kiro event only when the tab's foreground job is among them and the hook does not run for a background job such as `kiro-cli &`. romty detects a Kiro tab by its `kiro-cli-chat` process, because Kiro CLI shell integration also runs `kiro-cli` helper commands and the `kiro-cli-term` wrapper in every shell. Kiro runs `Stop` only for a completed turn, so `Stop` is `completed`. Kiro CLI 3 runs `SessionStart` when the first prompt is submitted, so until then a new Kiro tab shows the terminal-output estimate.

A cancelled turn runs no hook. romty reads the session record that the hook's session ID names, and a newer cancellation there ends the turn as `interrupted`:

- V3 writes `${KIRO_HOME:-~/.kiro}/sessions/<workspace>/<session_id>/messages.jsonl`. A `turn_end` with `stopReason` `cancelled` dates the interrupt.
- V2 writes `${KIRO_HOME:-~/.kiro}/sessions/cli/<session_id>.jsonl`. A record that starts with `Response was interrupted by the user` or `Tool use was cancelled by the user` ends the turn. V2 records carry no time, so the file's modification time dates the interrupt.

Kiro CLI 2's default V2 engine runs no global hooks. To report V2 sessions, add hooks that run `romty hook kiro` for `agentSpawn`, `userPromptSubmit`, `preToolUse`, `postToolUse`, and `stop` to the custom agent you use, then start it with `kiro-cli --v2 chat --agent <name>`. Without hooks, a Kiro tab shows the terminal-output estimate.

## What a hook sends

The hook command sends only tab, provider, session, turn/prompt, subagent and tool-use identifiers, event name, tool name, notification type, permission mode, whether background work remains, for Claude Code the transcript file path, and, for OpenCode and Kiro, the process groups above the hook and whether it runs for a background job. It does not send or retain prompts, transcript contents, tool inputs, tool outputs, or assistant messages. It is silent outside a romty tab or when the daemon is unavailable.

## Token and cost readings

A hooked Claude Code session also reports what it has spent. romty reads the counters Claude Code writes to its own transcript, at the path the session's hook reports. romty reads the path only when it is a session transcript directly inside one project folder of `${CLAUDE_CONFIG_DIR:-~/.claude}/projects`. While that terminal is open, the rail above the status row shows two readings: the tokens the newest request carried into the model, and the session cost the agent totalled.

romty decodes only these counters, each record's type and time, and the text of a record that starts with Claude Code's interrupt marker. It does not decode prompts, tool inputs, or responses.

Both are the agent's own numbers. romty never estimates them, and never converts them to a share of a context window — a transcript records no window size, so a percentage could only come from a table of model limits that would go stale as models change. A tab shows no reading when the transcript cannot be read.

The reading needs the session identifier, which only a hook reports: two tabs running an agent in the same directory cannot otherwise be told apart. Codex and OpenCode record their counters differently and are not read yet.
