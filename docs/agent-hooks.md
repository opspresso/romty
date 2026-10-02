# Agent status hooks

romty identifies foreground Claude Code, Codex, and OpenCode processes. Lifecycle events determine what those processes are doing; terminal output and silence do not establish Codex or Claude Code completion.

These states describe the agent runtime. A question written only in ordinary response text has no structured input-request event, and finishing a response is not independent verification that a build or deployment succeeded. romty does not classify the prose to invent those outcomes.

| Marker | Meaning |
|---|---|
| `●` | Agent detected; lifecycle status unavailable |
| `◐` `◓` `◑` `◒` | Thinking, running tools, planning, compacting, or continuing background work |
| `○` | Ready, or response stopped without confirmed final completion; the status rail distinguishes them |
| `✓` | Confirmed completion |
| `□` | Interrupted |
| `▲` | Needs your answer |
| `■` | Needs your approval |
| `★` | Failed |

## Codex native status

Run `codex`, `codex resume`, and other Codex commands with their usual arguments. The command, executable, `PATH`, working directory, authentication, and execution policy stay under Codex's control.

The existing `romty hooks` setup adds `thread-id` to `tui.terminal_title` in `${CODEX_HOME:-~/.codex}/config.toml`. It preserves existing title items, comments and other settings. An explicitly empty title list stays disabled. Restart Codex after setup to pick up the identity field. If a profile or command-line override replaces the title items, include `thread-id` in that selection too.

The TUI emits this identifier in its window title; romty already tracks titles without displaying them. Codex 0.160.0 shortens that title item, so romty resolves it only when it uniquely matches a loaded session. It never selects a session by working directory, transcript recency, or a guessed task description.

romty polls the local app-server's read-only APIs: `thread/loaded/list`, `thread/read`, `thread/turns/list` with `itemsView: notLoaded`, and `thread/goal/get`. Runtime flags identify pending questions and approvals. The latest turn metadata distinguishes completion, interruption, and failure. An active goal stays in background work between turns; a paused or blocked goal does not count as completed. No prompts, commands, approval decisions, resumes, or daemon-start requests are sent by the status reader.

The API and title metadata are verified against Codex 0.160.0. A missing identity, ambiguous match, unavailable server, or unsupported API produces an unavailable state rather than a guessed completion. Standalone sessions can use correctly bound hooks when the shared server cannot report them. The reader uses the `CODEX_HOME` inherited when the terminal tab was created.

Codex's shared app-server can inherit `ROMTY_TAB_ID` from the terminal that originally started it. Codex hooks therefore route by their session ID and the TUI's title identity, not that inherited tab ID. This also works when the shared server started outside romty and has no tab ID at all.

See the official [Codex app-server API](https://learn.chatgpt.com/docs/app-server) and [Codex hooks](https://learn.chatgpt.com/docs/hooks).

## Claude Code hooks

Install and trust the hooks described below. `UserPromptSubmit` begins work. `AskUserQuestion`, `PermissionRequest`, and MCP elicitation identify separate user interactions. Tool-use IDs keep an unanswered question visible while other parallel tools finish. Session, prompt/turn, and subagent IDs prevent unrelated events from changing the main agent's state.

`Stop` means the response reached its stop hooks. Another hook can continue the same turn, so romty shows `stopped` without a completion sound until a definitive completion notification arrives. Claude Code's `idle_prompt` notification confirms that it finished responding; Claude sends it after about 60 seconds only if the user has not typed. `StopFailure` reports failure. Scheduled future cron wakeups alone do not mean work is currently running.

Claude Code's public hooks do not expose every final transition immediately: for example, a user interrupt need not emit `Stop`, and a blocking Stop hook can continue work. romty does not claim that this hook-only path proves immediate final task completion. Without hooks it shows status unavailable. See the [Claude Code hooks reference](https://code.claude.com/docs/en/hooks).

The hook command sends only tab, provider, session, turn/prompt, subagent and tool-use identifiers, event name, tool name, notification type, permission mode, and whether background work remains. It does not send or retain prompts, transcripts, tool inputs, tool outputs, or assistant messages. It is silent outside a romty tab or when the daemon is unavailable.

OpenCode uses its plugin's lifecycle events. Its terminal-output fallback remains an estimate and never confirms completion.

## Token and cost readings

A hooked Claude Code session also reports what it has spent. romty reads the counters Claude Code writes to its own transcript under `${CLAUDE_CONFIG_DIR:-~/.claude}/projects` and shows them on the rail above the status row while that terminal is open: the tokens the newest request carried into the model, and the session cost the agent totalled.

Both are the agent's own numbers. romty never estimates them, and never converts them to a share of a context window — a transcript records no window size, so a percentage could only come from a table of model limits that would go stale as models change. A tab shows no reading when the transcript cannot be read.

The reading needs the session identifier, which only a hook reports: two tabs running an agent in the same directory cannot otherwise be told apart. Codex and OpenCode record their counters differently and are not read yet.

## Install or update

When the TUI starts, romty looks for `claude`, `claude-code`, `codex`, and `opencode` on `PATH`. If a detected agent has missing or outdated romty hooks, the TUI opens a confirmation dialog. Press `Enter` to install or update every listed provider, or `Esc` to leave the files unchanged for that run.

Hook installation is available only from a tagged release binary, including binaries installed from a tagged Go module. Development binaries produced by local `go run`, `go build`, or `go install` commands neither offer installation in the TUI nor write hook settings through `romty hooks`. This prevents temporary Go build-cache paths from becoming persistent hook commands.

Run the same installation directly without opening the TUI:

```sh
romty hooks
```

The command reports `installed`, `updated`, or `current` for each detected provider and `not found` for unavailable providers. It writes:

- Claude Code user hooks to `${CLAUDE_CONFIG_DIR:-~/.claude}/settings.json`
- Codex user hooks to `${CODEX_HOME:-~/.codex}/hooks.json`, and the `thread-id` title item to `tui.terminal_title` in `config.toml` in the same directory
- an OpenCode plugin to `${OPENCODE_CONFIG_DIR:-~/.config/opencode}/plugins/romty.js`

Claude Code and Codex are configured by structurally merging JSON instead of replacing the document. Existing settings, unrelated hooks, and unknown fields remain. romty normalizes only command handlers that invoke `romty hook claude` or `romty hook codex`, removes obsolete duplicates, and adds any missing lifecycle events. Malformed JSON or an incompatible `hooks` value is reported and left unchanged.

OpenCode has no JSON hook settings; its plugin API is the extension point. romty generates the whole plugin file, which bridges OpenCode's lifecycle events to `romty hook opencode`. A `plugins/romty.js` that romty did not write is refused rather than overwritten. All writes are atomic and preserve a settings-file symlink by updating its target.

Installed handlers use the absolute path of the current romty executable so an untrusted working directory or modified `PATH` cannot substitute another command. Re-run `romty hooks` after moving a manually installed romty binary; the installer updates an old executable path.

Claude Code can disable all hooks with `disableAllHooks`, and Codex can set `[features].hooks = false`. romty does not override either explicit opt-out. Codex hooks are otherwise enabled by default. See the official [Claude Code hooks reference](https://code.claude.com/docs/en/hooks) and [Codex hooks documentation](https://learn.chatgpt.com/docs/hooks) for precedence and policy controls.

Claude Code applies direct user-settings edits automatically, subject to its workspace trust rules. Codex requires review and trust for a new or changed non-managed hook; open `/hooks` in Codex after installation. OpenCode loads plugins only at startup, so restart it to pick up the plugin. Restart an already running agent session if it does not pick up the new hook configuration.

## Verify

Start `codex`, `claude`, or `opencode` normally in a new tab after setup. Submit a prompt, answer a question, approve a tool, and interrupt a separate turn. Check both the tab marker and the status rail. `romty list` reports the same phase, including `working`, `waiting_input`, `waiting_approval`, `stopped`, `completed`, `interrupted`, and `unknown`.

Completion sounds require confirmed `completed` or `error` transitions. `idle`, provisional `stopped`, interruption, and unavailable status do not play them. Enable alerts in the `F3` Config dialog; `d` controls completion alerts, `b` controls input/approval alerts, and `s` tests the done sound.
