# Interface

## Workspace tree and terminal pane

The left pane shows roots, their direct child workspaces, Git state, and one marker for each running terminal tab. The right pane contains the selected tab rail and embedded terminal. The divider arrow points toward the focused pane.

Claude Code markers are orange and Codex markers are blue. Foreground process detection supplies the color without configuration; [agent status hooks](agent-hooks.md) add the phase:

| Marker | Meaning |
|---|---|
| `●` | Agent detected with an unknown phase |
| `◐` `◓` `◑` `◒` | Thinking, working, planning, compacting, or running background work |
| `○` | Idle and ready for another prompt |
| `▲` | Waiting for user input |
| `■` | Waiting for permission approval |
| `★` | Stopped with an error |

Git metadata is rendered as `(branch*) ↑N ↓N`. `*` means the worktree has tracked or untracked changes, `!` replaces it for conflicts, and the arrows count commits ahead of or behind the upstream. A detached HEAD appears as `(@abcdef0)`.

romty refreshes local Git state every 10 seconds. It fetches remote-tracking refs in the background at startup and every 5 minutes. `F5` refreshes local state immediately and starts another background fetch; fetches never prompt for credentials and time out without blocking the interface.

Press `Ctrl`+`Shift`+`G` to open Git actions for a workspace. From the workspace pane it targets the row under the cursor; from the terminal pane or scrollback it targets the open terminal workspace. The menu provides `Status`, `Fetch`, `Pull`, and `Push`. `Pull` uses `--ff-only` so it never creates a merge commit. Commands run without interactive credential prompts, and their output or error remains in a scrollable result modal. Press `Enter` from a result to return to the action menu.

## Keyboard shortcuts

`F1` through `F7` work in both panes and with regular modals open. The root path prompt and startup hook confirmation own their input until submitted or cancelled. Press `F1` for the compact in-app reference.

### Core

| Scope | Key | Action |
|---|---|---|
| Both panes | `F1` | Open help |
| Both panes | `F2` | Add a root |
| Both panes | `F3` | Open config |
| Both panes | `F4` | Close the TUI and leave sessions running |
| Both panes | `F5` | Refresh |
| Both panes | `F6` | Enter or leave scrollback |
| Both panes | `F7` | Switch pane |
| Workspace | `F8` | Delete the selected workspace or forget the selected root after confirmation |
| Workspace | `F9` | Stop the daemon and running shells after confirmation |

### Navigation

| Scope | Key | Action |
|---|---|---|
| Workspace | `↑`/`↓` | Select a root or workspace |
| Workspace | `←`/`→` | Select a terminal tab or `+` |
| Workspace | `Enter` | Open the selection |
| Workspace | `Tab` | Focus the terminal |
| Terminal | `Ctrl+\` | Focus the workspace and refresh it |
| Both panes | `Ctrl`+`Shift`+`T` | Create and open a terminal tab |
| Both panes | `Ctrl`+`Shift`+`G` | Open Git actions for the contextual workspace |
| Both panes | `Ctrl`+`Shift`+`←`/`→` | Switch terminal tab |
| Both panes | `Ctrl`+`Shift`+`↑`/`↓` | Switch to a workspace with a running terminal |

Workspace aliases are `?` Help, `a` Add root, `,` Config, `q` or `Ctrl+C` Quit, `r` Refresh, `s` Scrollback, `d` Remove selection, `t` Stop daemon, and `i` About. `j`/`k`, `h`/`l`, `Ctrl+B`/`Ctrl+F`, and `g`/`G` mirror the corresponding arrow, page, and end keys where applicable.

### Contextual modes

| Mode | Key | Action |
|---|---|---|
| Picker | `↑`/`↓`, `PgUp`/`PgDn`, `Home`/`End` | Move through directories |
| Picker | `→`/`←` | Open a directory or go to its parent |
| Picker | `Enter` / `/` / `Esc` | Add the selection / type a path / close |
| Terminal / scrollback | `Shift`+`PgUp`/`PgDn` | Enter scrollback or move one page |
| Scrollback | `↑`/`↓`, `PgUp`/`PgDn`, `Home`/`End`, Wheel | Move through history |
| Scrollback | `F6`, `F7`, `Ctrl+\`, `Esc`, `q`, `s` | Leave scrollback |
| Help | `↑`/`↓`, `PgUp`/`PgDn`, `Home`/`End` | Scroll the reference |
| Config | `←`/`→`, `[`/`]` | Resize the workspace pane |
| Git actions | `↑`/`↓`, `Enter` | Select and run an action |
| Git result | `↑`/`↓`, `PgUp`/`PgDn`, `Home`/`End` | Scroll command output |
| Modal or prompt | `Esc` | Cancel or skip |
| Confirmation | `Enter` | Confirm the requested action |

## Roots, workspaces, and tabs

`F2` opens a picker on the home directory. The first row is the open directory itself, shown as `.`, so `Enter` always adds the highlighted row. Files and dot-directories are omitted; `/` opens a path prompt. Directory reads run in the background so a slow mount does not block the TUI or its terminals.

The `+` key is not a shortcut. Select the `+` tab with `←`/`→` and press `Enter`, or use `Ctrl`+`Shift`+`T`. From the workspace pane the shortcut uses the workspace under the cursor; from the terminal pane or scrollback it uses the open terminal workspace. The switch shortcuts wrap at both ends.

`F7` switches panes. `Tab` enters the terminal and `Ctrl+\` returns to the workspace; pressing `Ctrl+\` again opens scrollback. In the terminal pane only global shortcuts are captured. Other keyboard and paste input, including `F8`, `F9`, and ordinary `Ctrl` combinations, is forwarded to the PTY.

romty discovers direct child directories only. Press `F5` after a command adds or removes a child. An unreadable root is marked `✗` and listed without workspaces; other roots remain usable.

## Scrollback and mouse

romty keeps 10,000 scrollback lines for each terminal. Scrollback fills the width so native terminal selection copies output without the workspace tree. New output does not move a historical view; leaving returns to the live screen.

Full-screen applications such as `vim`, `less`, and Claude Code use an alternate screen with no romty history. In that mode `Shift`+`PgUp`/`PgDn` is forwarded as plain `PgUp`/`PgDn` so the application can page itself.

The mouse stays with the host terminal for native selection. Set `mouse_passthrough` in `config.json` to let applications receive it instead:

```json
{ "mouse_passthrough": true }
```

With passthrough enabled, the application mouse mode is mirrored until it exits or scrollback opens. Native selection then uses the terminal bypass modifier: `Option` on macOS or `Shift` elsewhere. If the terminal does not support alternate scroll, use the keyboard to navigate scrollback.

## Destructive actions and config

`F8` or `d` acts on the highlighted row. Deleting a workspace recursively removes all of its contents. Forgetting a root removes it only from romty. Both actions terminate terminal sessions under the selected item.

`F9` or `t` asks before stopping the daemon because this terminates every running shell. Once confirmed, shutdown cannot be cancelled.

The workspace pane width is saved automatically in `config.json`. It is constrained to 18 through 40 columns and may shrink further when the terminal is narrow.
