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

## CLI commands

| Command | Action |
|---|---|
| `romty` | Start or reconnect to the TUI |
| `romty status` | Show daemon, protocol, root, workspace, session, and agent counts |
| `romty version` | Show the client version |
| `romty help` | Show command-line help |
| `romty doctor` | Check runtime permissions, JSON files, the shell, and daemon compatibility |
| `romty list` | List roots, workspaces, and running terminal sessions |
| `romty stop` | Stop the daemon and every running terminal session |

`status`, `doctor`, and `list` are read-only and never start the daemon or create the runtime directory. A missing runtime directory, state file, or config file is valid before the first run. `doctor` exits with an error when it finds an invalid runtime, malformed JSON, an unavailable shell, an unsafe socket, or an incompatible daemon protocol.

`--version` and `-v` are aliases for `version`; `--help` and `-h` are aliases for `help`.
Command output uses color when connected to a terminal, stays plain when redirected or piped, and honors `NO_COLOR`.

## Interface

The left pane shows the workspace tree, which scrolls with the selection when the tree is taller than the pane. A `●` after an item represents one running terminal tab. A dot turns Claude orange while Claude Code is in the foreground and Codex blue while Codex is in the foreground; other terminals keep the normal row color. The right pane contains the tab rail and the embedded terminal. Accent colors distinguish the current selection, active tab, and focused pane; the arrow beside the vertical divider points to the active pane.

A `↓N` marker means a Git workspace is `N` commits behind its configured upstream and can pull remote-tracking commits. romty checks local remote-tracking refs every 10 seconds, fetches them in the background at startup and every 5 minutes, and fetches immediately when you press `F5`. Fetches never prompt for credentials and time out without blocking the interface.

The status bar shows the primary actions and the keys for the active pane. Press `F1` for the compact in-app reference.

### Keyboard shortcuts

`F1` through `F7` work in both panes and with a modal open. The root path prompt is the exception: it owns every key until it is submitted or cancelled.

#### Core

| Scope | Key | Action |
|---|---|---|
| Both panes | `F1` | Help |
| Both panes | `F2` | Add a root |
| Both panes | `F3` | Config |
| Both panes | `F4` | Quit romty |
| Both panes | `F5` | Refresh |
| Both panes | `F6` | Enter or leave scrollback |
| Both panes | `F7` | Switch pane |
| Workspace | `F8` | Delete the selected workspace or forget the selected root after confirmation |
| Workspace | `F9` | Stop the daemon and running shells after confirmation |

#### Navigation

| Scope | Key | Action |
|---|---|---|
| Workspace | `↑`/`↓` | Select a root or workspace |
| Workspace | `←`/`→` | Select a terminal tab or `+` |
| Workspace | `Enter` | Open the selection |
| Workspace | `Tab` | Focus the terminal |
| Terminal | `Ctrl+\` | Focus the workspace and refresh it |
| Both panes | `Ctrl`+`Shift`+`T` | Create and open a terminal tab |
| Both panes | `Ctrl`+`Shift`+`←`/`→` | Switch terminal tab |
| Both panes | `Ctrl`+`Shift`+`↑`/`↓` | Switch to a workspace with a running terminal |

Workspace aliases are `?` Help, `a` Add root, `,` Config, `q` or `Ctrl+C` Quit, `r` Refresh, `s` Scrollback, `d` Remove selection, `t` Stop daemon, and `i` About. `j`/`k`, `h`/`l`, `Ctrl+B`/`Ctrl+F`, and `g`/`G` mirror the corresponding arrow, page, and end keys where applicable.

#### Contextual modes

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
| Modal or prompt | `Esc` | Cancel |
| Confirmation | `Enter` | Confirm selection removal or daemon shutdown |

### Adding a root

`F2` opens a picker on the home directory. The first row is the open directory itself, shown as `.`, so `Enter` always adds the highlighted row. Files and dot-directories are omitted; `/` opens the path prompt for a directory that is faster to type or paste.

Directory reads run in the background, so a slow network mount does not block the TUI or its terminals.

### Switching panes and sessions

The `+` key is not a shortcut. Select the `+` tab with `←`/`→` and press `Enter`, or press `Ctrl`+`Shift`+`T` to create and open a tab directly. From the workspace pane, the shortcut uses the workspace under the cursor; from the terminal pane or scrollback, it uses the open terminal's workspace. `Ctrl`+`Shift`+the arrow keys switch existing tabs or workspaces immediately and wrap at both ends.

`F7` switches panes in either direction. `Tab` enters the terminal and `Ctrl+\` returns to the workspace; pressing `Ctrl+\` again opens scrollback. The function-key fallback matters when the desktop intercepts `Ctrl+\` before romty receives it.

In the terminal pane, only the global shortcuts above are captured. Other keyboard and paste input, including `F8`, `F9`, and ordinary `Ctrl` combinations, is forwarded to the PTY.

### Scrollback and mouse

romty keeps the last 10,000 lines that scrolled off each terminal. Scrollback fills the width so native terminal selection copies terminal output without the workspace tree. New output does not move the historical view; leaving returns to the live screen.

Full-screen applications such as `vim`, `less`, and Claude Code use an alternate screen with no history. In that mode `Shift`+`PgUp`/`PgDn` is forwarded as plain `PgUp`/`PgDn` so the application can page itself.

The mouse stays with the host terminal for native selection. Set `mouse_passthrough` in `config.json` to let applications receive it instead:

```json
{ "mouse_passthrough": true }
```

With passthrough enabled, the application's mouse mode is mirrored until it exits or scrollback opens. Native selection then uses the terminal's bypass modifier: `Option` on macOS or `Shift` elsewhere. If the terminal does not support alternate scroll, the wheel does nothing in scrollback and the keyboard controls still work.

### Modals and config

`Esc` cancels a modal or path prompt; `Enter` confirms destructive actions. The configured workspace pane width is saved automatically and constrained to 18 through 40 columns, subject to the available terminal width.

`F8` or `d` acts on the highlighted row. A workspace is deleted recursively with all of its contents. A root is only forgotten by romty; its directory stays on disk. Both actions terminate every terminal session under the selected item.

## Workspace refresh

romty discovers only direct child directories of each root. If a command such as `git clone` adds a directory, or a directory is removed, press `F5`.

A root romty cannot read — unmounted, deleted, or with its permissions changed — is marked `✗` and listed with no directories. The other roots are unaffected, and `F8` or `d` forgets it. Returning from the terminal pane with `Ctrl+\` also refreshes the tree.

## Session lifecycle

The TUI communicates with a detached local daemon over a Unix socket. The socket and the directory holding it are created private to your user, which is the only thing separating another local process from every shell romty owns: connecting to it is enough to read and write any terminal, start new ones, or stop the daemon. The daemon owns the shell PTYs, so closing the TUI or its host terminal does not terminate running sessions. Reopening romty reconnects to those sessions and replays their buffered output.

New terminals start with the environment and `$SHELL` of the romty that asked for them, not the daemon's. The daemon may have been started days earlier from a different login session, so its own environment is not the one you are working in.

Pressing `F9` opens a confirmation modal because stopping the daemon terminates every running shell. Once `Enter` confirms it the shutdown cannot be cancelled, so the modal stays until the daemon reports back. The `romty stop` command performs the same shutdown directly and is intended for explicit use from outside a romty terminal; stopping a daemon that is not running succeeds without output.

The daemon outlives the romty binary, so `brew upgrade` or `go install` can leave different client and daemon versions running together. A version-exempt ping advertises each side's supported protocol range and capabilities, and ordinary requests use the highest common revision. Protocols 1 through 5 are supported: older peers keep their existing operations while newer features degrade independently. In particular, agent status requires protocol 2, ordered snapshot revisions require 3, workspace removal requires 4, and the bounded initial replay requires 5. Protocol 0 predates an explicit compatibility contract and is reported as unsupported. Shutdown remains available even when no ordinary revision overlaps, and every message that reports a mismatch ends by naming that remedy: run `romty stop` and start romty again.

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
| `daemon.sock.lock` | Held by the running daemon, so only one owns the socket |
| `daemon.log` | Detached daemon output |

Set `ROMTY_HOME` to use a different directory, which is useful for development or isolated testing:

```sh
ROMTY_HOME=/tmp/romty-dev romty
```

The directory must be owned by the current user and cannot be a symbolic link. romty narrows it to mode `0700` and refuses a daemon socket or log that could expose the client environment or terminal stream to another local user.

Keep it shallow. `daemon.sock` lives inside it, and a unix socket path has a hard ceiling in the kernel — 104 bytes on macOS, 108 on Linux. romty refuses a directory that would put the socket past it and says so, rather than leaving `bind: invalid argument` to explain itself.

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

The daemon keeps up to 8 MiB of output history per session. The client restores that history through its VT emulator before displaying the attached terminal, then continues streaming live output into the pane.

romty manages local shells only. It does not provide remote or SSH session management.

## Development

Run the standard checks:

```sh
go vet ./...
go test -race ./...
go build ./...
```

A session runs several goroutines around one emulator, so the race detector is the check that matters most here, and it is the one CI gates on.

### Protocol compatibility

Keep existing field meanings and stream framing stable within their protocol revision. Additive actions and optional JSON fields do not require a new revision; advertise independently usable behavior as a capability, and ignore unknown fields and capabilities. Raise `MinimumVersion` only when retaining an older wire contract is unsafe. Breaking field semantics or framing require a new revision and a preserved adapter for every revision still inside the supported range.

Run an isolated development instance:

```sh
ROMTY_HOME=/tmp/romty-dev go run ./cmd/romty
```

## Releases

Pushing a version tag such as `v0.2.0` runs the release workflow. It validates the project, publishes macOS and Linux archives for amd64 and arm64 to GitHub Releases, and updates the `romty` formula in `opspresso/homebrew-tap`.
