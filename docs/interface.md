# Interface

This guide describes the romty TUI: its panes, agent markers, Git views, keyboard shortcuts, scrollback, mouse behavior, and settings. Press `F1` in romty for the in-app key reference, and press `/` in it to search the shortcuts by name or key. `Enter` keeps the filter; `Esc` clears it, and a second `Esc` closes Help.

## Workspace tree and terminal pane

The workspace pane on the left shows the workspace tree: roots, their direct child workspaces, Git state, and one marker for each running terminal tab. A workspace that is a Git repository takes two rows, its name and its Git state; any other workspace takes one row. The terminal pane on the right contains the tab row and the embedded terminal. The divider arrow points toward the focused pane.

The workspace pane title shows the machine's hostname without its domain next to `romty`, for example `romty · build-01`. The terminal window or tab title uses the same text. When agents need you, both titles add their count, for example `romty · build-01 · ▲2 ■1`; see [Agents that need you](#agents-that-need-you).

Before the first root is added, the terminal pane explains how to start and the shortcut rail names `F2` only.

A release build checks GitHub for the newest romty release when the dashboard starts and every 12 hours after that. When a newer release exists, the workspace pane title shows `romty ↑`, and About (`i`) names the new version. A failed check, for example when the machine is offline or rate limited, keeps the previous result and shows no error. Development builds do not check.

Below 80 columns the workspace pane takes half the screen from the terminal, so focusing the terminal hides it and gives the terminal the full width. `Ctrl`+`/` or `F7` brings it back and moves the focus with it. The layout and terminal use the actual reported width even below 40 columns. At 80 columns and above both panes stay on screen, and focus never resizes the terminal. A terminal that speaks the Kitty keyboard protocol reports `Ctrl`+`/` as itself; every other terminal, including phone SSH clients, sends it as `Ctrl`+`_`, and romty accepts both.

## Agent and service markers

Each running terminal tab has one marker in the workspace tree and the same marker before its name in the tab rail. A tab without a detected agent or service shows a neutral `●` in the text color. Claude Code markers are terracotta, Codex markers are blue, OpenCode markers are violet, Kiro markers are pink, and Node.js service markers are green. None of them uses romty's teal accent, so a marker stays distinct on the current and selected rows. Foreground process detection supplies the color. When a terminal wrapper, such as the `kiro-cli-term` that Kiro CLI shell integration starts, runs the shell in a nested terminal, romty uses the foreground job of the nested terminal. It stops at the first job that runs an agent. [Native Codex status and agent hooks](agent-hooks.md) supply lifecycle phases; Codex and Claude Code remain unknown without a reporting channel.

Node.js services show a green `●` while `node`, `nodejs`, `npm`, `pnpm`, `yarn`, or `next-server` runs in the terminal's foreground process group. The status rail shows `Node.js running`, and `romty list` shows `node`. Node.js does not use agent lifecycle phases or completion sounds. When the service exits and the shell regains the foreground, the marker returns to neutral. If an AI agent shares the group with a Node.js launcher, its agent color takes priority.

The status rail shows the label for the open terminal's agent. `romty list` prints the phase after the agent name, for example `claude/working`.

| Marker | Status rail | `romty list` phase | Meaning |
|---|---|---|---|
| `●` | Status unavailable | none; agent name only | Agent detected, but no lifecycle report |
| `◐` `◓` `◑` `◒` | Thinking, Working, Planning, Compacting, or Background work | `thinking`, `working`, `planning`, `compacting`, `background` | Active work |
| `○` | Ready | `idle` | Waiting for a prompt |
| `○` | Stopped · completion unconfirmed | `stopped` | The response stopped without confirmed completion |
| `✓` | Completed | `completed` | Confirmed completion |
| `□` | Interrupted | `interrupted` | Interrupted |
| `▲` | Needs your answer | `waiting_input` | Waiting for user input |
| `■` | Needs your approval | `waiting_approval` | Waiting for permission approval |
| `★` | Failed | `error` | Stopped with an error |

If a marker stays `●`, see [Status stays unavailable](agent-hooks.md#status-stays-unavailable).

### Agents that need you

An agent that waits for input (`▲`) or approval (`■`), or that failed (`★`), is drawn in reverse so it stands out from busy agents. Their count is shown in an amber badge at the right end of the workspace pane title and in the window title. When the narrow layout hides the workspace pane, the shortcut rail shows the count instead, for example `▲1 waiting`.

romty sends a desktop notification when an agent finishes, fails, or starts waiting, for example `romty Claude Code: Needs your answer · api / 1`. It uses the OSC 9 escape sequence, which travels to the terminal on your own device, so the notification also reaches you over SSH. iTerm2, WezTerm, Ghostty, kitty, and Windows Terminal show it; a terminal without OSC 9 support ignores it. Several transitions in one poll share one notification. The first agent poll after startup does not notify. Press `n` in Config, or set `mute_notifications` in `config.json`, to turn notifications off.

While the workspace pane has focus, the shortcut rail spells out the tabs of the workspace under the cursor, for example `1: Claude Code · Working  server: Node.js  zsh: shell`.

Press `a` in the workspace pane, or `Ctrl`+`Shift`+`A` in either pane, to open the next terminal whose agent is waiting for input or approval. While an agent waits, the shortcut rail offers this key first. It walks those terminals in the order the tree draws them and wraps at the end, so repeated presses cycle every agent that stopped to ask something. When the only waiting agent is the terminal already open, the keyboard moves to it rather than reattaching. When nothing is waiting, the status bar says so and the open terminal is left alone.

## Git state and actions

Git metadata is rendered as `(branch*) ↑N ↓N`. `*` means the worktree has tracked or untracked changes, `!` replaces it for conflicts, and the arrows count commits ahead of or behind the upstream. A detached HEAD appears as `(@abcdef0)`.

romty refreshes local Git state every 10 seconds. It fetches remote-tracking refs in the background at startup and every 5 minutes. `F5` refreshes local state immediately and starts another background fetch; fetches never prompt for credentials and time out without blocking the interface.

Press `Ctrl`+`Shift`+`G` to open Git actions for a workspace. From the workspace pane it targets the row under the cursor; from the terminal pane or scrollback it targets the open terminal workspace. The menu provides `Status`, `Fetch`, `Pull`, and `Push`.

- `Pull` uses `--ff-only` so it never creates a merge commit.
- Commands run without interactive credential prompts, and their output or error remains in a scrollable result modal.
- Captured Git output is limited to 8 MiB; exceeding it stops the command and reports an error.
- The output is coloured: porcelain status letters take the same green, amber, and red the file view gives them, and a diffstat's `+` and `-` are split into additions and removals.
- Press `Enter` from a result to return to the action menu, or click `×` in its title bar to close it.

## File views

### Changed files

Press `Ctrl`+`Shift`+`F` to toggle the changed-file view for the same contextual workspace.

The left pane shows staged, unstaged, and untracked files as a directory tree. Added and untracked files are green, modified and renamed files are amber, and deleted or conflicted files are red.

The right pane shows the diff of the selected file:

- It syntax-highlights recognised code files and falls back to plain text for other content or unusually large diffs.
- Added and removed rows use green and red background tints without replacing syntax colors.
- A file with both staged and unstaged edits shows both sections, and an untracked text file is compared with an empty file.
- Diff previews disable external diff and text-conversion commands and treat selected filenames literally.

Press `↑`/`↓` to select a file. Use `Ctrl`+`↑`/`↓`, the mouse wheel, or the paging keys to read its diff. Press `F6` to switch between inline and split layouts; the last layout is restored when the view or romty is opened again. Press `F5` to reload the worktree.

### All files

The workspace action menu also provides `All files`. This view is also available for workspaces that are not Git repositories.

- It recursively lists every regular file in the workspace, including ignored and dot-prefixed files such as `.env`, while skipping dot-prefixed directories such as `.git`.
- Each level lists directories before files, with each group sorted by name, and directories start collapsed.
- Symlinks are listed neither as files nor as traversable directories.
- The selected file opens read-only with syntax highlighting when its format and size are supported.
- File previews are limited to 8 MiB; larger files report an error.
- Path resolution stays inside the workspace even if a parent directory changes after listing.

### Directory rows

Both file views allow directory rows to take the cursor. Press `Enter` to toggle the selected directory, `←` to collapse it, or `→` to expand it. Click a directory to select and toggle it, click a file to select and open it, or use the wheel over the tree to move its selection. Collapsed directories remain collapsed when `F5` reloads the view.

## Keyboard shortcuts

`F1` through `F7` work in both panes and with regular modals open. Each `Ctrl`+`Shift`+letter shortcut also has a single letter in the workspace pane. A terminal without the Kitty keyboard protocol, such as macOS Terminal and many phone SSH clients, sends `Ctrl`+`Shift`+`A` as the same byte as `Ctrl`+`A`, so the shell receives it. In such a terminal, press `Ctrl`+`/` to move to the workspace pane and then press the letter. Alternatives listed beside them are contextual; file view assigns its own action to `F5`, and changed-file view also assigns `F6`. The root path modal and the tab name modal own keyboard and paste input until submitted or cancelled, including `F4`. After `Enter`, the modal stays open until the daemon answers. If the daemon rejects the value, the modal keeps the typed text and shows the reason inside it. Clicks reach only the modal's `×` and action buttons, not the dashboard behind it. While daemon shutdown, hook installation, a Git command, or a modal submit is running, only `F4` is accepted. Press `F1` for the in-app reference.

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
| `F8` | Open actions for the selected workspace or root |
| `F9` | Stop the daemon and running shells after confirmation |
| `i` | Open About |
| `Tab` | Focus the terminal |
| `Space` | Collapse or expand the selected root, or collapse the root of the selected workspace |

### Switch

| Key | Action |
|---|---|
| `Ctrl`+`Shift`+`T`, or `t` in the workspace pane | Create and open a terminal tab |
| `Ctrl`+`Shift`+`G`, or `g` in the workspace pane | Open Git actions for the contextual workspace |
| `Ctrl`+`Shift`+`F`, or `f` in the workspace pane | Toggle changed files and Git diff |
| `Ctrl`+`Shift`+`←`/`→` | Switch terminal tab |
| `Ctrl`+`Shift`+`↑`/`↓` | Switch to a workspace with a running terminal |
| `Ctrl`+`Shift`+`A`, or `a` in the workspace pane | Open the next terminal whose agent is waiting |

### Move

| Key | Action | Applies to |
|---|---|---|
| `↑`/`↓` | Move one item or line | Workspace, file list, picker, Config, Help, Git, scrollback |
| `k`/`j` | Move one item or line | Workspace, file list, picker, Config, Help, Git |
| `←`/`→` or `h`/`l` | Select a tab, open a picker child/parent, or collapse/expand a file tree directory | Workspace, picker, file view |
| `PgUp`/`PgDn` or `Ctrl+B`/`Ctrl+F` | Move one page | Picker, Help, Git result, file view |
| `Home`/`End` or `g`/`G` | Move to the first or last item/line | Picker, Help, Git result, file view |
| `Shift`+`PgUp`/`PgDn` | Enter scrollback or move one page | Workspace, terminal, scrollback |
| Mouse wheel | Scroll | Help, Git result, scrollback, file view, terminal |

### File view

| Key | Action |
|---|---|
| `Enter` | Toggle the selected directory |
| `←`/`→` or `h`/`l` | Collapse or expand the selected directory |
| `F6` | Toggle inline and split layouts for changed files |
| `Ctrl`+`↑`/`↓` | Move through the content one line at a time |

### Context

| Key | Action |
|---|---|
| `Enter` | Open, run, return, submit, confirm, or toggle a Config setting in the workspace, picker, Config, Git views, and input modals |
| `Esc` | Close a modal or file view, cancel an input modal, or leave scrollback |
| `Backspace` | Erase the last typed character in a text field or the scrollback search |
| `←`/`→` or `[`/`]` | Resize the workspace pane in Config |
| `/` | Find in scrollback, search Help, or type a path in the root picker |
| `n`/`N` | Move to the next or previous scrollback match |
| `p` | Toggle mouse passthrough in Config |
| `m` | Toggle the scrollback mouse in Config |
| `d`/`b` | Toggle the done and waiting sounds in Config |
| `n` | Toggle desktop notifications in Config |

## Roots, workspaces, and tabs

`F2` opens a picker on the home directory. The first row is the open directory itself, shown as `.`, so `Enter` always adds the highlighted row. Files and dot-directories are omitted; `/` opens the `Root path` modal for a typed or pasted path. An absolute path or a path that starts with `~` is used as typed; any other path is relative to the directory the picker shows. `Esc` in the modal returns to the picker. Directory reads run in the background so a slow mount does not block the TUI or its terminals.

Tabs have a wider click target and an `×` at the right edge. Clicking the label opens the tab; clicking `×` asks for confirmation, then terminates its shell and removes it. Double-clicking the label opens the `Rename tab` modal with the current name. Edit it and press `Enter` to rename the tab, or press `Esc` to keep the old name. An empty or rejected name keeps the modal open and shows the reason inside it. A name is 1 to 32 characters after surrounding spaces are removed and cannot contain control characters. Renaming keeps the shell running, and the daemon saves the new name. The `+` key is not a shortcut. Select the wider `+` tab with `←`/`→` and press `Enter`, or use `Ctrl`+`Shift`+`T`. From the workspace pane the shortcut uses the workspace under the cursor; from the terminal pane or scrollback it uses the open terminal workspace. The switch shortcuts wrap at both ends.

### Opening a workspace without a terminal

Selecting a root or workspace that has no running terminal, with `Enter` or a click, starts a new terminal at once when the workspace has no closed sessions. When it has closed sessions, a menu titled `Open a session in <workspace>` opens in the middle of the terminal pane instead, and the pane shows nothing else until you choose:

- `New session` starts a new terminal tab.
- `Reopen <tab> · <agent> · <time>` reopens one of the workspace's last three closed sessions, newest first. The agent is shown when one was running at close. The time shows the date as well when the session closed before today.

Use `↑`/`↓` and `Enter`, or click a choice. Press `Esc` or click outside the menu to cancel. A reopened tab replays the closed tab's output behind a `── reopened from a closed tab ──` marker and takes back its name when no running tab uses it. When an agent was running at close, its resume command is typed at the prompt, as after `romty stop`; see [Stopping and resuming](runtime.md#stopping-and-resuming). A reopened session leaves the list.

Closing a tab from its `×` or from the workspace actions saves it to this list. A shell that exits on its own is not saved. `New tab`, `Ctrl`+`Shift`+`T`, and the `+` tab still create a tab directly.

When you move the cursor to a workspace, the tab cursor goes to the tab you last opened there, so `Enter` returns to it. While the cursor is on a workspace other than the open one, the tab row shows that workspace's tabs above the open terminal, and the end of the row says `tabs of <workspace> · Enter opens`. When the tabs do not fit in the row, the longest names are shortened so every tab and the `+` tab stay visible.

Pane focus and scrollback toggles are reversible. In the terminal pane only Global and Switch shortcuts are captured. Other keyboard and paste input, including `F8`, `F9`, and ordinary `Ctrl` combinations, is forwarded to the PTY.

The dashboard chrome is mouse-aware. Click a root or workspace to select and open it (or to choose a session when it has no running terminal), click a terminal tab or `+` to activate it, and use the wheel over the workspace tree to scroll its viewport without moving the cursor or keyboard focus. Arrow-key cursor movement scrolls the viewport only when the selected row would otherwise leave it. Drag the vertical divider to resize the workspace pane; the terminal resizes live and the final width is saved when the button is released.

Mouse targets identify themselves before activation. Workspace, tab, picker, Git, Config, and dialog-action targets gain a muted background on hover, while the draggable divider changes to the accent color. Keyboard selection keeps the stronger accent treatment and is never replaced by hover.

### Modals

Modals are drawn over the dashboard rather than in place of it: the workspace tree and the terminal stay on screen around the box, redrawn in one receding tone so the box in front is what asks to be read.

- The box takes the width its content needs, up to a cap. Config is given half the screen because it is read rather than answered.
- The workspace action palette is the exception: it is anchored on the row it belongs to and leaves the tree around it as it was.
- Modal title bars provide a clickable `×` that performs the same close or cancel action as `Esc`. The button is hidden while a Git command, daemon shutdown, or hook installation is running.
- Confirmation dialogs also render their Enter and Esc actions inside the box and accept clicks on those actions.
- Modal boxes fit narrow terminal widths, and hidden portions of clipped actions are not clickable.
- Paste input while a modal is open does not reach the terminal behind it.

Longer operations such as opening a terminal, reading a directory, running a Git action, stopping the daemon, or installing hooks show a moving one-cell activity marker. The marker stops scheduling frames when no visible work or agent animation remains.

Modal content is pointer-aware as well. Click a directory in the root picker to open it, click a Git action to run it, and use the wheel over a completed Git result to read longer output. Config rows toggle when clicked and the row clicked becomes the one the cursor is on; use the wheel over the pane-width row for one-column adjustments.

romty discovers direct child directories only and skips names that start with `.`. Press `F5` after a command adds or removes a child. An unreadable root is marked `✗` and listed without workspaces; other roots remain usable.

### Collapsed roots

Press `Space` in the workspace pane, or click the `▾` arrow of a root, to collapse the root. A collapsed root shows `▸` and hides its workspaces. Press `Space` on it or click `▸` to expand it again. Clicking the root name still opens the root. romty saves the collapsed roots to `collapsed_roots` in `config.json`, so they stay collapsed after a restart.

- The root row shows the markers of the running tabs in its hidden workspaces.
- When the open terminal belongs to a hidden workspace, the root row shows the current-workspace mark. `Ctrl`+`/`, `F7`, and the terminal restored at startup put the cursor on the root and keep it collapsed.
- `Ctrl`+`Shift`+`↑`/`↓` and `Ctrl`+`Shift`+`A` can open a hidden workspace. They expand its root first, so the cursor lands on that workspace.

## Scrollback and mouse

romty keeps 10,000 scrollback lines for each terminal. Scrollback fills the width so native terminal selection copies output without the workspace tree. New output does not move a historical view. `Shift`+`PgUp`/`PgDn` and the mouse wheel continue browsing history; other terminal input, including paste, returns to the live screen and is forwarded without dropping the first input.

Press `/` in scrollback to find text in the retained output. Typed and pasted text edits the search query. The query is applied when `Enter` confirms it, matching without regard to case and ignoring the colours around a phrase. The view moves to the newest match first, and `n` and `N` walk towards older and newer matches, wrapping at both ends. The status bar carries the query and the position among the matches. Leaving scrollback forgets the search.

Full-screen applications such as `vim`, `less`, and Claude Code use an alternate screen with no romty history. In that mode `Shift`+`PgUp`/`PgDn` is forwarded as plain `PgUp`/`PgDn` so the application can page itself, and the wheel goes to the application as well: as mouse reports when it asked for the mouse, and otherwise as three cursor keys per notch, which is what a terminal sends for alternate scroll. This does not depend on `mouse_passthrough`.

On the live main screen, the mouse wheel enters scrollback and scrolls three lines per notch. Scrolling up with no history to show reports why in the status bar, so scrolling up with no response at all means the terminal is not sending wheel events and its mouse reporting setting needs checking. Because romty captures the live mouse, native selection uses the terminal bypass modifier: `Option` on macOS or `Shift` elsewhere. Scrollback releases mouse capture, so plain click-drag selects the full-width terminal output. File view captures the wheel to move through its tree or scroll its content, according to the pane under the pointer.

Press `p` in Config, or set `mouse_passthrough` in `config.json`, to let applications receive the mouse when they request it instead:

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

`F8` opens a compact action menu beside the highlighted row; right-clicking a workspace or root opens the same menu at the pointer. The menu offers terminal creation, closing the selected tab, read-only access to all files, changed-file review, Git status/fetch/pull/push, and removal directly when they apply. Closing a tab asks before it terminates that shell. Deleting a workspace recursively removes all of its contents, so the confirmation accepts `Enter` only after you type the workspace's name exactly. Forgetting a root removes it only from romty and confirms with `Enter`. Both removal actions terminate terminal sessions under the selected item.

`F9` asks before stopping the daemon because this terminates every running shell. Once confirmed, shutdown cannot be cancelled.

The last opened workspace and terminal tab, workspace pane width, collapsed roots, and inline or split diff layout are saved automatically in `config.json`. Reopening romty reconnects to that tab when it is still running; a removed workspace or stopped tab falls back to the workspace list. The pane width is constrained to 18 through 40 columns and may shrink further when the terminal is narrow.

Config lists each setting as a name, its value, and the key that runs it. Walk the list with `↑`/`↓` and run the row under the cursor with `Enter`; each row's own key still works from anywhere in the modal.

Desktop notifications are on by default; see [Agents that need you](#agents-that-need-you). Sound alerts are off by default. Open Config with `F3`, press `d` to toggle the embedded done sound when an agent reports confirmed completion or failure, or `b` to toggle the embedded waiting sound when an agent begins waiting for input or approval. Turning a sound on plays it once, so you can hear it and confirm that the host can play it. A response that stopped without confirmed completion, an interruption, and an unavailable status do not play the done sound; see [agent status hooks](agent-hooks.md). A stable state does not play repeatedly, and the first agent snapshot after startup stays silent. Playback uses the first supported audio tool on the host running romty, so an SSH session plays on the remote host rather than the connecting device; a host without a supported player remains silent.
