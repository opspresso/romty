# romty

> romty = roam + tty

romty is a terminal workspace manager that keeps shell sessions running while its TUI is closed or disconnected. It combines a workspace navigator, terminal tabs, and persistent PTY sessions in one process-light interface.

Website: [romty.dev](https://romty.dev)

## Features

- Keep terminal sessions alive after leaving the TUI.
- Organize projects as roots and their direct child directories.
- Open multiple terminal tabs for a root or workspace.
- Reconnect to a session with up to 8 MiB of terminal history.
- Use native terminal mouse selection and copy behavior.
- Adapt colors to light and dark terminal backgrounds.
- Control the interface with IME-independent function keys.
- Stop the daemon and all running shells through a confirmed TUI action or CLI command.
- Prevent nested romty sessions.

## Requirements

- macOS or Linux
- Go 1.25 or later when building from source

## Installation

Install the Homebrew formula:

```sh
brew install opspresso/tap/romty
```

Or install the latest version with Go:

```sh
go install github.com/opspresso/romty/cmd/romty@latest
```

To build a local checkout:

```sh
go build -o build/romty ./cmd/romty
```

## Quick start

Start romty:

```sh
romty
```

The daemon starts automatically on the first run. Press `F2` to open a picker on your home directory, walk to a directory with `→` and `←`, and press `Enter` to add it. romty displays the root and its direct child directories as a workspace tree:

```text
▾ projects
├─ api
└─ web
```

Use `↑`/`↓` to choose a root or workspace. Use `←`/`→` to choose an existing terminal tab or the `+` tab, then press `Enter` to confirm. Selecting a workspace without an open tab creates one automatically.

Both roots and workspaces can host terminal tabs. A root tab starts its shell in the root directory; a workspace tab starts in that direct child directory.

To stop the daemon and every running terminal session from outside the TUI:

```sh
romty stop
```

## Interface

The left pane shows the workspace tree, which scrolls with the selection when the tree is taller than the pane. A `●` after an item represents one running terminal tab. The right pane contains the tab rail and the embedded terminal. Accent colors distinguish the current selection, active tab, and focused pane; the arrow beside the vertical divider points to the active pane.

The status bar shows `F1` through `F7` in both panes and adds navigation keys for the active pane. Press `F1` in either pane to see every shortcut, including aliases hidden from the status bar.

### Adding a root

`F2`, or `a` in the workspace pane, opens a picker on the home directory listing the directories inside it.

| Key | Action |
|---|---|
| `↑`/`↓`, `k`/`j` | Move between directories |
| `→`, `l` | Open the highlighted directory |
| `←`, `h` | Go to the parent directory; the cursor lands on the directory just left |
| `Enter` | Add the highlighted directory as a root |
| `/` | Type a path instead |
| `Esc` | Close the picker |

Files and dot-directories are left out, and a linked directory is listed as the directory it points at. A directory with no subdirectories to highlight adds itself, so walking into an empty one is not a dead end. The picker always opens on the home directory rather than where it was left, so `F2` lands somewhere predictable; `/` reaches everything else, including a path that is faster to paste than to walk to.

### Global keys

These function keys work from either pane, with a modal open, and with non-English keyboard input modes. The root input prompt is the one exception: while it is open every key belongs to the prompt, so a typed path is never discarded by a function key.

| Key | Action |
|---|---|
| `F1` | Open Help |
| `F2` | Open the root directory picker |
| `F3` | Open Config |
| `F4` | Quit romty |
| `F5` | Refresh roots, workspaces, and sessions |
| `F6` | Enter or leave scrollback for the open terminal |
| `F7` | Switch between the workspace and terminal panes |
| `Shift`+`PgUp`/`PgDn` | Enter scrollback and move one page at a time |

The row stops at `F7`. A full-screen program binds the whole function key row — `htop` puts Kill on `F9` and answers it with `Enter`, which is the same `Enter` that would confirm stopping the daemon — so romty takes `F1` through `F7` from the shell and no more. `F8` and `F9` belong to the workspace pane, below.

### Workspace pane

| Key | Action |
|---|---|
| `F1`, `?` | Open Help |
| `F2`, `a` | Open the root directory picker |
| `F3`, `,` | Open Config |
| `F4`, `q` | Quit romty |
| `F5`, `r` | Refresh roots, workspaces, and sessions |
| `F6`, `s` | Enter or leave scrollback for the open terminal |
| `F7`, `Tab` | Focus the active terminal |
| `F8`, `d` | Ask for confirmation, then forget the selected root; terminals under it keep running |
| `F9`, `t` | Ask for confirmation, then stop the daemon and all running terminal sessions |
| `i` | Open About, which names the version this build is |
| `↑`/`↓`, `j`/`k` | Move between roots and workspaces |
| `←`/`→`, `h`/`l` | Move between terminal tabs and the `+` tab |
| `Enter` | Open the selected tab or create the selected `+` tab |
| `Ctrl+C` | Quit romty |

The `+` key itself is not a shortcut. Select the `+` tab with `←`/`→` and confirm it with `Enter`.

### Terminal pane

| Key | Action |
|---|---|
| `F7`, `Ctrl+\` | Focus the workspace pane and refresh the workspace tree |
| `Ctrl+\` `Ctrl+\` | Pressing it a second time, from the workspace pane, opens the terminal's scrollback |

`F7` moves in both directions, which `Tab` and `Ctrl+\` each did one way only. It is also the way out when `Ctrl+\` never arrives: an application or the desktop environment can claim that chord as a global hotkey — 1Password does, and a Windows host running romty over WSL sees it taken before the terminal does — and romty cannot receive a key the system intercepted first.

Except for `F1` through `F7` and `Ctrl+\`, keyboard and paste input is forwarded to the PTY — `F8` and `F9` included, so a full-screen program keeps the keys it binds — along with keys held with `Shift`, `Ctrl` or `Meta` such as `Ctrl`+`←` for word-wise movement. Mouse tracking remains disabled so the host terminal can select and copy displayed text normally.

### Scrollback

romty keeps the last 10,000 lines that scrolled off each terminal. Enter scrollback with `F6`, `s`, `Shift`+`PgUp`, or a second `Ctrl+\`, and leave it with `F6`, `s`, `Ctrl+\`, `Esc`, or `q`. New output does not move the view while you are scrolled back; leaving returns to the live screen.

Full-screen applications such as `vim`, `less`, and Claude Code switch the terminal to its alternate screen, which keeps no history — the application owns every row and scrolls its own content. romty says so instead of opening scrollback, because the history from before the application started is not what you asked for. `Shift`+`PgUp`/`PgDn` reaches such an application as a plain `PgUp`/`PgDn` so its own paging still works.

### Mouse

The mouse belongs to the host terminal, which is what keeps its click-drag selection and copy working over romty. Applications that want the mouse themselves — Claude Code, `htop`, `vim` with `set mouse=a` — therefore do not receive it by default, and scroll with `PgUp`/`PgDn` or `Ctrl+U`/`Ctrl+D` instead.

Set `mouse_passthrough` in `config.json` — see [Data and configuration](#data-and-configuration) for where that lives — to hand the mouse to those applications while they run:

```json
{ "mouse_passthrough": true }
```

romty then mirrors whatever mouse mode the application asks for, and returns the mouse to the terminal as soon as the application exits or scrollback opens. This is the same trade as `set -g mouse on` in tmux: the wheel and clicks reach the application, and the terminal's drag selection needs its bypass modifier — `Option` on macOS, `Shift` elsewhere — for as long as the application is running.

| Key | Action |
|---|---|
| Wheel | Scroll, through the terminal's own alternate scroll |
| `↑`/`↓`, `j`/`k` | Scroll one line |
| `PgUp`/`PgDn`, `Ctrl+B`/`Ctrl+F` | Scroll one page |
| `Home`/`End`, `g`/`G` | Jump to the oldest retained line or back to the live screen |
| `F6`, `s`, `Ctrl+\`, `Esc`, `q` | Leave scrollback for the terminal |
| `F7` | Leave scrollback for the workspace pane |

Scrollback hides the workspace pane and draws the terminal across the full width. That is what makes the text selectable: in the split layout every row of the host terminal holds the workspace tree, a divider, and terminal output on one line, so dragging across several lines copies the tree along with them. With one pane on screen, a plain drag selects terminal output and nothing else.

romty never asks the terminal for mouse events here. The xterm protocol has no wheel-only reporting mode, so requesting the wheel would take drag selection away — the very thing scrollback exists to give you. The wheel still scrolls because terminals in the alternate screen send it as arrow keys, which scrollback already handles. Selection and copying stay with the terminal, exactly as they behave outside romty.

If your terminal has alternate scroll turned off, the wheel does nothing here and the keys above still work.

While scrollback is open it owns the keyboard, so navigation and terminal input resume only after you leave it. Leaving focuses the terminal, which is what the full-width view was already showing, so `Ctrl+\` cycles terminal → workspace → scrollback → terminal. `F7` is the exception: it switches panes, so it leaves scrollback for the workspace pane. A terminal whose shell has exited is not focused; the workspace pane keeps the keyboard instead.

### Modals and prompts

| Key | Action |
|---|---|
| `Esc` | Close a modal or cancel root input |
| `←`/`→`, `[`/`]` | Adjust the workspace pane width in Config |
| `↑`/`↓`, `j`/`k` | Scroll Help when the terminal is too short to show every shortcut |
| `Enter` | Confirm the root removal in the `F8` modal, or the daemon shutdown in the `F9` modal |

The configured workspace pane width is stored automatically and constrained to 18 through 40 columns, subject to the available terminal width.

## Workspace refresh

romty discovers only direct child directories of each root. If a command such as `git clone` adds a directory, or a directory is removed, press `F5`.

A root romty cannot read — unmounted, deleted, or with its permissions changed — is marked `✗` and listed with no directories. The other roots are unaffected, and `F8` or `d` forgets it. Returning from the terminal pane with `Ctrl+\` also refreshes the tree.

## Session lifecycle

The TUI communicates with a detached local daemon over a Unix socket. The socket and the directory holding it are created private to your user, which is the only thing separating another local process from every shell romty owns: connecting to it is enough to read and write any terminal, start new ones, or stop the daemon. The daemon owns the shell PTYs, so closing the TUI or its host terminal does not terminate running sessions. Reopening romty reconnects to those sessions and replays their buffered output.

New terminals start with the environment and `$SHELL` of the romty that asked for them, not the daemon's. The daemon may have been started days earlier from a different login session, so its own environment is not the one you are working in.

Pressing `F9` opens a confirmation modal because stopping the daemon terminates every running shell. Once `Enter` confirms it the shutdown cannot be cancelled, so the modal stays until the daemon reports back. The `romty stop` command performs the same shutdown directly and is intended for explicit use from outside a romty terminal; stopping a daemon that is not running succeeds without output.

Reattaching replays the recorded output so the screen comes back as it was, preceded by the terminal modes the shell has set. Modes are sticky — a shell turns bracketed paste on once and never mentions it again — so a session long enough to fill the recording would otherwise lose them, and a shell that cannot tell pasted text from typed text runs a multi-line paste line by line. Tracking them separately from the recording keeps the answer independent of how much of it is left. Terminal queries are dropped from that replay: a query is an exchange that already finished, and a terminal emulator answering it a second time would send the reply to a shell that asked nothing, where it lands on the command line as typed text. Live queries are still answered normally.

A terminal whose connection drops is reconnected automatically. Repeated drops are retried more slowly each time and then left alone, with a message, rather than reconnecting in a loop; press `Enter` on the tab to try again.

When a shell exits, its tab is removed from the daemon state. If you were in that terminal, romty moves to the tab that took its place in the same workspace, or to the workspace pane when it held the last one. A shell that exits in the background leaves the workspace tree where it is. If the daemon stops or the operating system restarts, roots and workspace metadata remain available, but stale terminal tabs are discarded because their PTY processes can no longer be reattached.

The daemon writes what it is doing, and every failure it cannot report to a client, to `daemon.log` — see [Data and configuration](#data-and-configuration) for where that lives. That is the place to look when romty will not start or a session behaves oddly, because the daemon runs detached with nobody watching its output.

romty refuses to start inside one of its own terminal sessions to avoid nesting the TUI.

## Data and configuration

romty stores its files in `romty` under the user config directory — `~/Library/Application Support` on macOS, and `$XDG_CONFIG_HOME` or `~/.config` on Linux:

| File | Purpose |
|---|---|
| `state.json` | Roots, workspaces, and terminal tab metadata |
| `config.json` | TUI settings such as workspace pane width |
| `daemon.sock` | Local client-daemon Unix socket |
| `daemon.log` | Detached daemon output |

Set `ROMTY_HOME` to use a different directory, which is useful for development or isolated testing:

```sh
ROMTY_HOME=/tmp/romty-dev romty
```

## Architecture

```text
romty TUI
   ├─ workspace tree
   └─ terminal tabs and VT renderer
            │
       Unix socket
            │
       romty daemon
            ├─ state store
            └─ PTY sessions
                 └─ shell process
```

The daemon keeps up to 8 MiB of output history per session. The client replays that history through its VT emulator when attaching, then continues streaming output into the terminal pane.

romty manages local shells only. It does not provide remote or SSH session management.

## Development

Run the standard checks:

```sh
go vet ./...
go test ./...
go build ./...
```

Run an isolated development instance:

```sh
ROMTY_HOME=/tmp/romty-dev go run ./cmd/romty
```

## Releases

Pushing a version tag such as `v0.2.0` runs the release workflow. It validates the project, publishes macOS and Linux archives for amd64 and arm64 to GitHub Releases, and updates the `romty` formula in `opspresso/homebrew-tap`.
