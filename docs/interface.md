# Interface

## Workspace tree and terminal pane

The left pane shows roots, their direct child workspaces, Git state, and one marker for each running terminal tab. The right pane contains the selected tab rail and embedded terminal. The divider arrow points toward the focused pane.

Below 80 columns the workspace pane takes half the screen from the terminal, so focusing the terminal hides it and gives the terminal the full width. `Ctrl`+`/` or `F7` brings it back and moves the focus with it. At 80 columns and above both panes stay on screen, and focus never resizes the terminal. A terminal that speaks the Kitty keyboard protocol reports `Ctrl`+`/` as itself; every other terminal, including phone SSH clients, sends it as `Ctrl`+`_`, and romty accepts both.

Claude Code markers are orange and Codex markers are blue. Foreground process detection supplies the color without configuration; [agent status hooks](agent-hooks.md) add the phase:

| Marker | Meaning |
|---|---|
| `●` | Agent detected with an unknown phase |
| `◐` `◓` `◑` `◒` | Thinking, working, planning, compacting, or running background work |
| `○` | Idle and ready for another prompt |
| `▲` | Waiting for user input |
| `■` | Waiting for permission approval |
| `★` | Stopped with an error |

Press `Ctrl`+`Shift`+`A` to open the next terminal whose agent is waiting for input or approval. It walks those terminals in the order the tree draws them and wraps at the end, so repeated presses cycle every agent that stopped to ask something. When the only waiting agent is the terminal already open, the keyboard moves to it rather than reattaching. When nothing is waiting, the status bar says so and the open terminal is left alone.

Git metadata is rendered as `(branch*) ↑N ↓N`. `*` means the worktree has tracked or untracked changes, `!` replaces it for conflicts, and the arrows count commits ahead of or behind the upstream. A detached HEAD appears as `(@abcdef0)`.

romty refreshes local Git state every 10 seconds. It fetches remote-tracking refs in the background at startup and every 5 minutes. `F5` refreshes local state immediately and starts another background fetch; fetches never prompt for credentials and time out without blocking the interface.

Press `Ctrl`+`Shift`+`G` to open Git actions for a workspace. From the workspace pane it targets the row under the cursor; from the terminal pane or scrollback it targets the open terminal workspace. The menu provides `Status`, `Fetch`, `Pull`, and `Push`. `Pull` uses `--ff-only` so it never creates a merge commit. Commands run without interactive credential prompts, and their output or error remains in a scrollable result modal. Press `Enter` from a result to return to the action menu.

Press `Ctrl`+`Shift`+`F` to toggle the file view for the same contextual workspace. Its left pane shows staged, unstaged, and untracked files as a directory tree. Added and untracked files are green, modified and renamed files are amber, and deleted or conflicted files are red. The right pane syntax-highlights recognised code files and falls back to plain text for other content or unusually large diffs. Added and removed rows use green and red background tints without replacing syntax colors. Press `F6` to switch between inline and split layouts; the last layout is restored when the view or romty is opened again. A file with both staged and unstaged edits shows both sections, and an untracked text file is compared with an empty file. Press `↑`/`↓` to select a file, use `Ctrl`+`↑`/`↓`, the mouse wheel, or the paging keys to read its diff, and press `F5` to reload the worktree.

## Keyboard shortcuts

`F1` through `F7` work in both panes and with regular modals open. Alternatives listed beside them are contextual; file view also assigns its own actions to `F5` and `F6`. The root path prompt owns its input until submitted or cancelled. While daemon shutdown, hook installation, or a Git command is running, only `F4` is accepted. Press `F1` for the in-app reference.

### Global

| Key | Action |
|---|---|
| `F1` or `?` | Open Help |
| `F2` | Add a root |
| `F3` or `,` | Open Config |
| `F4` or `Ctrl+C` | Close the TUI and leave sessions running |
| `F5` | Refresh workspaces and Git state, or reload file view |
| `F6` or `Ctrl`+`Shift`+`\` | Enter or leave scrollback |
| `F7` or `Ctrl`+`/` | Toggle pane focus |

### Workspace

| Key | Action |
|---|---|
| `F8` | Delete the selected workspace or forget the selected root after confirmation |
| `F9` | Stop the daemon and running shells after confirmation |
| `i` | Open About |
| `Tab` | Focus the terminal |

### Switch

| Key | Action |
|---|---|
| `Ctrl`+`Shift`+`T` | Create and open a terminal tab |
| `Ctrl`+`Shift`+`G` | Open Git actions for the contextual workspace |
| `Ctrl`+`Shift`+`F` | Toggle changed files and Git diff |
| `Ctrl`+`Shift`+`←`/`→` | Switch terminal tab |
| `Ctrl`+`Shift`+`↑`/`↓` | Switch to a workspace with a running terminal |
| `Ctrl`+`Shift`+`A` | Open the next terminal whose agent is waiting |

### Move

| Key | Action | Applies to |
|---|---|---|
| `↑`/`↓` | Move one item or line | Workspace, file list, picker, Help, Git, scrollback |
| `k`/`j` | Move one item or line | Workspace, file list, picker, Help, Git |
| `←`/`→` or `h`/`l` | Select a tab, or open a picker child/parent | Workspace, picker |
| `PgUp`/`PgDn` or `Ctrl+B`/`Ctrl+F` | Move one page | Picker, Help, Git result, file diff |
| `Home`/`End` or `g`/`G` | Move to the first or last item/line | Picker, Help, Git result, file diff |
| `Shift`+`PgUp`/`PgDn` | Enter scrollback or move one page | Workspace, terminal, scrollback |
| Mouse wheel | Scroll | Help, scrollback, file diff, terminal |

### File diff

| Key | Action |
|---|---|
| `F6` | Toggle inline and split layouts |
| `Ctrl`+`↑`/`↓` | Move through the diff one line at a time |

### Context

| Key | Action |
|---|---|
| `Enter` | Open, run, return, submit, or confirm in the workspace, picker, Git views, and prompts |
| `Esc` | Close a modal or file view, cancel a prompt, or leave scrollback |
| `/` | Type a path in the root picker |
| `Backspace` | Erase a path character |
| `←`/`→` or `[`/`]` | Resize the workspace pane in Config |
| `m` | Toggle the scrollback mouse in Config |
| `d`/`b` | Toggle the done and waiting sounds in Config |
| `s` | Play the done sound in Config |

## Roots, workspaces, and tabs

`F2` opens a picker on the home directory. The first row is the open directory itself, shown as `.`, so `Enter` always adds the highlighted row. Files and dot-directories are omitted; `/` opens a path prompt. Directory reads run in the background so a slow mount does not block the TUI or its terminals.

The `+` key is not a shortcut. Select the `+` tab with `←`/`→` and press `Enter`, or use `Ctrl`+`Shift`+`T`. From the workspace pane the shortcut uses the workspace under the cursor; from the terminal pane or scrollback it uses the open terminal workspace. The switch shortcuts wrap at both ends.

Pane focus and scrollback toggles are reversible. In the terminal pane only Global and Switch shortcuts are captured. Other keyboard and paste input, including `F8`, `F9`, and ordinary `Ctrl` combinations, is forwarded to the PTY.

The dashboard chrome is mouse-aware. Click a root or workspace to select and open it, click a terminal tab or `+` to activate it, and use the wheel over the workspace tree to move its cursor without opening anything. Drag the vertical divider to resize the workspace pane; the terminal resizes live and the final width is saved when the button is released.

Mouse targets identify themselves before activation. Workspace, tab, picker, Git, Config, and dialog-action targets gain a muted background on hover, while the draggable divider changes to the accent color. Keyboard selection keeps the stronger accent treatment and is never replaced by hover.

Confirmation dialogs render their Enter and Esc actions inside the box and accept clicks on those actions. Longer operations such as opening a terminal, reading a directory, running a Git action, stopping the daemon, or installing hooks show a moving one-cell activity marker; the marker stops scheduling frames when no visible work or agent animation remains.

Modal content is pointer-aware as well. Click a directory in the root picker to open it, click a Git action to run it, and use the wheel over a completed Git result to read longer output. Config rows toggle when clicked; use the wheel over the pane-width row for one-column adjustments.

romty discovers direct child directories only. Press `F5` after a command adds or removes a child. An unreadable root is marked `✗` and listed without workspaces; other roots remain usable.

## Scrollback and mouse

romty keeps 10,000 scrollback lines for each terminal. Scrollback fills the width so native terminal selection copies output without the workspace tree. New output does not move a historical view. `Shift`+`PgUp`/`PgDn` and the mouse wheel continue browsing history; other terminal input, including paste, returns to the live screen and is forwarded without dropping the first input.

Full-screen applications such as `vim`, `less`, and Claude Code use an alternate screen with no romty history. In that mode `Shift`+`PgUp`/`PgDn` is forwarded as plain `PgUp`/`PgDn` so the application can page itself, and the wheel goes to the application as well: as mouse reports when it asked for the mouse, and otherwise as three cursor keys per notch, which is what a terminal sends for alternate scroll. This does not depend on `mouse_passthrough`.

On the live main screen, the mouse wheel enters scrollback and scrolls three lines per notch. Scrolling up with no history to show reports why in the status bar, so scrolling up with no response at all means the terminal is not sending wheel events and its mouse reporting setting needs checking. Because romty captures the live mouse, native selection uses the terminal bypass modifier: `Option` on macOS or `Shift` elsewhere. Scrollback releases mouse capture, so plain click-drag selects the full-width terminal output. File view captures the wheel to scroll its diff.

Set `mouse_passthrough` in `config.json` to let applications receive the mouse when they request it instead:

```json
{ "mouse_passthrough": true }
```

With passthrough enabled, the application mouse mode is mirrored until it exits or scrollback opens.

Scrollback normally reads the wheel as the arrow keys the terminal's alternate scroll sends. A terminal without alternate scroll — many phone SSH clients, and some desktop ones — sends nothing there, leaving scrollback scrollable by keyboard only. Press `m` in Config, or set `scrollback_mouse`, to have romty keep the mouse in scrollback instead:

```json
{ "scrollback_mouse": true }
```

The wheel then scrolls scrollback on any terminal that reports the mouse, at the cost of the plain click-drag selection scrollback otherwise offers; the bypass modifier still selects.

## Destructive actions and config

`F8` acts on the highlighted row. Deleting a workspace recursively removes all of its contents. Forgetting a root removes it only from romty. Both actions terminate terminal sessions under the selected item.

`F9` asks before stopping the daemon because this terminates every running shell. Once confirmed, shutdown cannot be cancelled.

The last opened workspace and terminal tab, workspace pane width, and inline or split diff layout are saved automatically in `config.json`. Reopening romty reconnects to that tab when it is still running; a removed workspace or stopped tab falls back to the workspace list. The pane width is constrained to 18 through 40 columns and may shrink further when the terminal is narrow.

Sound alerts are off by default. Open Config with `F3`, press `d` to toggle the embedded done sound when an agent finishes active work, `b` to toggle the embedded waiting sound when an agent begins waiting for input or approval, and `s` to test the done sound. A stable state does not play repeatedly, and the first agent snapshot after startup stays silent. Playback uses the first supported audio tool on the host running romty, so an SSH session plays on the remote host rather than the connecting device; a host without a supported player remains silent.
