# Agent status hooks

romty identifies foreground Claude Code and Codex processes without configuration. Hooks add the phase behind the colored tab marker:

| Marker | Meaning |
|---|---|
| `●` | Thinking, running a tool, planning, compacting, background work, or an unknown phase |
| `○` | Idle and ready for the next prompt |
| `◉` | Waiting for user input or permission, or stopped with an error |

The hook command reads JSON from standard input and sends only the tab ID, provider, session ID, event name, tool name, notification type, permission mode, and whether background work remains. It does not send or retain prompts, transcripts, tool inputs, tool outputs, or assistant messages. It writes nothing to standard output or standard error and exits successfully when it is outside a romty tab, the daemon is unavailable, or the running daemon predates hook support.

Hooks are synchronous to preserve event order. They use romty's existing private Unix socket and require no additional service. A tab created by an older daemon does not have `ROMTY_TAB_ID`; restart romty and create a new tab after upgrading before testing the configuration. `romty stop` also terminates every running shell, so save work before restarting the daemon.

## Claude Code

Merge the following `hooks` object into `~/.claude/settings.json`. If the file already contains hooks, add the entries without replacing them.

```json
{
  "hooks": {
    "SessionStart": [{ "hooks": [{ "type": "command", "command": "romty hook claude", "timeout": 1 }] }],
    "UserPromptSubmit": [{ "hooks": [{ "type": "command", "command": "romty hook claude", "timeout": 1 }] }],
    "PreToolUse": [{ "hooks": [{ "type": "command", "command": "romty hook claude", "timeout": 1 }] }],
    "PostToolUse": [{ "hooks": [{ "type": "command", "command": "romty hook claude", "timeout": 1 }] }],
    "PostToolUseFailure": [{ "hooks": [{ "type": "command", "command": "romty hook claude", "timeout": 1 }] }],
    "PermissionRequest": [{ "hooks": [{ "type": "command", "command": "romty hook claude", "timeout": 1 }] }],
    "Notification": [{ "hooks": [{ "type": "command", "command": "romty hook claude", "timeout": 1 }] }],
    "Elicitation": [{ "hooks": [{ "type": "command", "command": "romty hook claude", "timeout": 1 }] }],
    "ElicitationResult": [{ "hooks": [{ "type": "command", "command": "romty hook claude", "timeout": 1 }] }],
    "PreCompact": [{ "hooks": [{ "type": "command", "command": "romty hook claude", "timeout": 1 }] }],
    "PostCompact": [{ "hooks": [{ "type": "command", "command": "romty hook claude", "timeout": 1 }] }],
    "Stop": [{ "hooks": [{ "type": "command", "command": "romty hook claude", "timeout": 1 }] }],
    "StopFailure": [{ "hooks": [{ "type": "command", "command": "romty hook claude", "timeout": 1 }] }],
    "SessionEnd": [{ "hooks": [{ "type": "command", "command": "romty hook claude", "timeout": 1 }] }]
  }
}
```

Restart Claude Code after changing settings. Project and local settings can override or add hooks; use Claude Code's `/hooks` view if a hook does not appear.

## Codex

Enable hooks in `~/.codex/config.toml`:

```toml
[features]
hooks = true
```

Then create or merge the following entries into `~/.codex/hooks.json`:

```json
{
  "hooks": {
    "SessionStart": [{ "hooks": [{ "type": "command", "command": "romty hook codex", "timeout": 1 }] }],
    "UserPromptSubmit": [{ "hooks": [{ "type": "command", "command": "romty hook codex", "timeout": 1 }] }],
    "PreToolUse": [{ "hooks": [{ "type": "command", "command": "romty hook codex", "timeout": 1 }] }],
    "PostToolUse": [{ "hooks": [{ "type": "command", "command": "romty hook codex", "timeout": 1 }] }],
    "PermissionRequest": [{ "hooks": [{ "type": "command", "command": "romty hook codex", "timeout": 1 }] }],
    "PreCompact": [{ "hooks": [{ "type": "command", "command": "romty hook codex", "timeout": 1 }] }],
    "PostCompact": [{ "hooks": [{ "type": "command", "command": "romty hook codex", "timeout": 1 }] }],
    "Stop": [{ "hooks": [{ "type": "command", "command": "romty hook codex", "timeout": 1 }] }],
    "SessionEnd": [{ "hooks": [{ "type": "command", "command": "romty hook codex", "timeout": 1 }] }]
  }
}
```

Restart Codex after changing the files and approve the hook when Codex asks whether to trust it. Use Codex's `/hooks` view to inspect the loaded configuration.

## Verify

Start Claude Code or Codex in a newly created romty tab, submit a prompt, and leave the agent waiting for another prompt. The marker should move from `●` to `○`; a permission or input request should use `◉`. `romty list` reports the same phase as `claude/idle`, `codex/waiting_approval`, and similar values.
