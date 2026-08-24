# romty

> romty = roam + tty

romty is a terminal workspace manager that keeps shell sessions running while its TUI is closed or disconnected. It combines a workspace navigator, terminal tabs, and persistent PTY sessions in one process-light interface.

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
go install github.com/nalbam/romty/cmd/romty@latest
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

The daemon starts automatically on the first run. Press `F2`, enter a root directory, and press `Enter`. romty displays the root and its direct child directories as a workspace tree:

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

The status bar shows `F1` through `F7` in both panes and adds navigation keys for the active pane. Press `?` in the workspace pane to see every shortcut, including aliases hidden from the status bar.

### Global keys

These function keys work from either pane, with a modal open, and with non-English keyboard input modes. The root input prompt is the one exception: while it is open every key belongs to the prompt, so a typed path is never discarded by a function key.

| Key | Action |
|---|---|
| `F1` | Open About |
| `F2` | Add a root directory |
| `F3` | Open Config |
| `F4` | Quit romty |
| `F5` | Refresh roots, workspaces, and sessions |
| `F6` | Ask for confirmation, then stop the daemon and all running terminal sessions |
| `F7` | Enter or leave scrollback for the open terminal |
| `Shift`+`PgUp`/`PgDn` | Enter scrollback and move one page at a time |

### Workspace pane

| Key | Action |
|---|---|
| `↑`/`↓`, `j`/`k` | Move between roots and workspaces |
| `←`/`→`, `h`/`l` | Move between terminal tabs and the `+` tab |
| `Enter` | Open the selected tab or create the selected `+` tab |
| `Tab` | Focus the active terminal |
| `i` | Open About |
| `a` | Add a root directory |
| `,` | Open Config |
| `q` | Quit romty |
| `r` | Refresh roots, workspaces, and sessions |
| `?` | Open Help |
| `Ctrl+C` | Quit romty |

The `+` key itself is not a shortcut. Select the `+` tab with `←`/`→` and confirm it with `Enter`.

### Terminal pane

| Key | Action |
|---|---|
| `Ctrl+\` | Focus the workspace pane and refresh the workspace tree |
| `Ctrl+\` `Ctrl+\` | Pressing it a second time, from the workspace pane, opens the terminal's scrollback |

Except for the global function keys and `Ctrl+\`, keyboard and paste input is forwarded to the PTY. Mouse tracking remains disabled so the host terminal can select and copy displayed text normally.

### Scrollback

romty keeps the last 10,000 lines that scrolled off each terminal. Enter scrollback with `F7`, `Shift`+`PgUp`, or a second `Ctrl+\`, and leave it with `Esc`, `q`, `F7`, or `Ctrl+\`. New output does not move the view while you are scrolled back; leaving returns to the live screen.

Full-screen applications such as `vim`, `less`, and Claude Code switch the terminal to its alternate screen, which keeps no history — the application owns every row and scrolls its own content. romty says so instead of opening scrollback, because the history from before the application started is not what you asked for. `Shift`+`PgUp`/`PgDn` reaches such an application as a plain `PgUp`/`PgDn` so its own paging still works.

### Mouse

The mouse belongs to the host terminal, which is what keeps its click-drag selection and copy working over romty. Applications that want the mouse themselves — Claude Code, `htop`, `vim` with `set mouse=a` — therefore do not receive it by default, and scroll with `PgUp`/`PgDn` or `Ctrl+U`/`Ctrl+D` instead.

Set `mouse_passthrough` in `~/.config/romty/config.json` to hand the mouse to those applications while they run:

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
| `Esc`, `q` | Leave scrollback |

Scrollback hides the workspace pane and draws the terminal across the full width. That is what makes the text selectable: in the split layout every row of the host terminal holds the workspace tree, a divider, and terminal output on one line, so dragging across several lines copies the tree along with them. With one pane on screen, a plain drag selects terminal output and nothing else.

romty never asks the terminal for mouse events here. The xterm protocol has no wheel-only reporting mode, so requesting the wheel would take drag selection away — the very thing scrollback exists to give you. The wheel still scrolls because terminals in the alternate screen send it as arrow keys, which scrollback already handles. Selection and copying stay with the terminal, exactly as they behave outside romty.

If your terminal has alternate scroll turned off, the wheel does nothing here and the keys above still work.

While scrollback is open it owns the keyboard, so navigation and terminal input resume only after you leave it. Leaving focuses the terminal, which is what the full-width view was already showing, so `Ctrl+\` cycles terminal → workspace → scrollback → terminal. A terminal whose shell has exited is not focused; the workspace pane keeps the keyboard instead.

### Modals and prompts

| Key | Action |
|---|---|
| `Esc` | Close a modal or cancel root input |
| `←`/`→`, `[`/`]` | Adjust the workspace pane width in Config |
| `↑`/`↓`, `j`/`k` | Scroll Help when the terminal is too short to show every shortcut |
| `Enter` | Confirm the daemon shutdown in the `F6` modal |

The configured workspace pane width is stored automatically and constrained to 18 through 40 columns, subject to the available terminal width.

## Workspace refresh

romty discovers only direct child directories of each root. If a command such as `git clone` adds a directory, or a directory is removed, press `F5`. Returning from the terminal pane with `Ctrl+\` also refreshes the tree.

## Session lifecycle

The TUI communicates with a detached local daemon over a Unix socket. The daemon owns the shell PTYs, so closing the TUI or its host terminal does not terminate running sessions. Reopening romty reconnects to those sessions and replays their buffered output.

Pressing `F6` opens a confirmation modal because stopping the daemon terminates every running shell. Once `Enter` confirms it the shutdown cannot be cancelled, so the modal stays until the daemon reports back. The `romty stop` command performs the same shutdown directly and is intended for explicit use from outside a romty terminal; stopping a daemon that is not running succeeds without output.

When a shell exits, its tab is removed from the daemon state. If the daemon stops or the operating system restarts, roots and workspace metadata remain available, but stale terminal tabs are discarded because their PTY processes can no longer be reattached.

romty refuses to start inside one of its own terminal sessions to avoid nesting the TUI.

## Data and configuration

romty stores its files under `os.UserConfigDir()/romty`:

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
