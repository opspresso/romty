# Agent status hooks

romty identifies foreground Claude Code and Codex processes without configuration, and reads a phase back from what the agent last drew. Hooks replace that reading with the agent's own report:

| Marker | Meaning |
|---|---|
| `●` | Agent detected with an unknown phase |
| `◐` `◓` `◑` `◒` | Thinking, running a tool, planning, compacting, or running background work |
| `○` | Idle and ready for the next prompt |
| `▲` | Waiting for user input |
| `■` | Waiting for permission approval |
| `★` | Stopped with an error |

The hook command reads JSON from standard input and sends only the tab ID, provider, session ID, event name, tool name, notification type, permission mode, and whether background work remains. It does not send or retain prompts, transcripts, tool inputs, tool outputs, or assistant messages. It writes nothing to standard output or standard error and exits successfully when it is outside a romty tab, the daemon is unavailable, or the running daemon predates hook support.

## Without hooks

An agent that has no romty hook installed still reports a phase, read from the last 4 KiB of its terminal output and the window title it set. An approval prompt names the choices it accepts and a generating agent says how to interrupt it, so those phrases stand in for a hook. Only `working`, `waiting for input`, and `waiting for permission` are recognised this way; `thinking`, `planning`, `compacting`, `idle`, and `error` need a hook.

The newest phrase in the output wins, so an agent that answered a prompt and went back to work reports work again. The window title is consulted only when the output says nothing, because a title is sticky and can outlive the state it named. A hook always wins over both, and no phase is guessed for a tab whose agent has drawn nothing recognisable.

## Install or update

When the TUI starts, romty looks for `claude`, `claude-code`, and `codex` on `PATH`. If a detected agent has missing or outdated romty hooks, the TUI opens a confirmation dialog. Press `Enter` to install or update every listed provider, or `Esc` to leave the files unchanged for that run.

Hook installation is available only from a tagged release binary, including binaries installed from a tagged Go module. Development binaries produced by local `go run`, `go build`, or `go install` commands neither offer installation in the TUI nor write hook settings through `romty hooks`. This prevents temporary Go build-cache paths from becoming persistent hook commands.

Run the same installation directly without opening the TUI:

```sh
romty hooks
```

The command reports `installed`, `updated`, or `current` for each detected provider and `not found` for unavailable providers. It writes:

- Claude Code user hooks to `${CLAUDE_CONFIG_DIR:-~/.claude}/settings.json`
- Codex user hooks to `${CODEX_HOME:-~/.codex}/hooks.json`

Installation structurally merges JSON instead of replacing the document. Existing settings, unrelated hooks, and unknown fields remain. romty normalizes only command handlers that invoke `romty hook claude` or `romty hook codex`, removes obsolete duplicates, and adds any missing lifecycle events. Writes are atomic and preserve a settings-file symlink by updating its target. Malformed JSON or an incompatible `hooks` value is reported and left unchanged.

Installed handlers use the absolute path of the current romty executable so an untrusted working directory or modified `PATH` cannot substitute another command. Re-run `romty hooks` after moving a manually installed romty binary; the installer updates an old executable path.

Claude Code can disable all hooks with `disableAllHooks`, and Codex can set `[features].hooks = false`. romty does not override either explicit opt-out. Codex hooks are otherwise enabled by default. See the official [Claude Code hooks reference](https://code.claude.com/docs/en/hooks) and [Codex hooks documentation](https://learn.chatgpt.com/docs/hooks) for precedence and policy controls.

Claude Code applies direct user-settings edits automatically, subject to its workspace trust rules. Codex requires review and trust for a new or changed non-managed hook; open `/hooks` in Codex after installation. Restart an already running agent session if it does not pick up the new hook configuration.

## Verify

Start Claude Code or Codex in a newly created romty tab and submit a prompt. The marker should animate through `◐` `◓` `◑` `◒`, then settle on `○` when the agent is ready for another prompt. An input request should use `▲`, a permission request should use `■`, and a stopped error should use `★`. `romty list` reports the same phase as `claude/idle`, `codex/waiting_approval`, and similar values.

Optional embedded sound alerts use these same phase transitions. Enable them in the `F3` Config dialog; `d` controls completed work, `b` controls waiting for input or approval, and `s` tests the done sound.
