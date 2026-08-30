# Runtime

## Remote access over SSH

romty can keep AI coding sessions on an always-on macOS or Linux machine while the operator moves between laptops or locations. Install and run romty on the machine that owns the repositories and compute. Reach that machine through a private network such as Tailscale, log in with SSH, and run `romty` inside the SSH session.

Closing SSH or exiting the TUI with `F4` leaves the daemon, PTYs, shells, and agent processes running on that machine. A later SSH login under the same Unix user can run `romty` again and reopen the live tabs with retained output. The host must remain powered on, and `romty stop` intentionally terminates every managed shell — saving each tab's output and agent session first, so the next daemon can replay the output and pre-type the agent's resume command in that workspace's next tabs.

```text
local laptop
    -> Tailscale or another private network
        -> SSH
            -> remote host: romty TUI
                -> private local Unix socket
                    -> romty daemon -> PTY -> AI coding agent
```

Tailscale and SSH are transport and login layers, not romty dependencies. romty does not listen on the tailnet or expose a TCP port; the TUI still reaches the daemon through the remote host's user-owned Unix socket. Normal SSH and host security practices remain responsible for remote access.

## Session lifecycle

The TUI communicates with a detached local daemon over a Unix socket. The daemon owns the shell PTYs, so closing the TUI or its host terminal does not terminate running sessions. Reopening romty reconnects and restores recent output before continuing with the live stream.

New terminals use the environment and `$SHELL` of the romty client that creates them. They do not inherit the potentially older environment of the detached daemon.

Each session retains up to 8 MiB of terminal output. Terminal modes are tracked separately so a replay remains usable after older output is trimmed. Completed terminal queries are omitted from a replay to prevent an unsolicited response from reaching the shell command line. Reconnect replay starts at the PTY dimensions that produced the latest output, then resizes the local emulator to the current pane; width-sensitive shell redraws therefore keep the wrapping and erase behavior they had when recorded.

A dropped terminal connection reconnects automatically with backoff. Press `Enter` on a disconnected tab to retry after automatic attempts stop. Closing a tab from its `×` or from the workspace actions terminates its shell and removes the tab, after a confirmation. When a shell exits on its own, its tab is also removed. If the daemon or operating system stops, roots and workspace metadata remain but stale PTY tabs are discarded.

More than one TUI can attach to the same terminal. Every client receives output, while the client that most recently sent input owns the PTY size. Resizing a background TUI does not disturb the active terminal; sending input promotes that client and applies its viewport first. If an attached client stops reading and exhausts its bounded output queue, only that client is disconnected.

The daemon reads a hooked Claude Code session's own transcript to report its token and cost counters. It reads at most the last mebibyte of that file, skips any record larger than 64 KiB, decodes only the counter fields, and re-reads a transcript only after it changes. Prompts, tool inputs, and assistant messages are never decoded or retained.

romty refuses to start inside one of its own terminal sessions.

## Data and configuration

romty stores data in a `romty` directory under the user configuration directory: `~/Library/Application Support` on macOS, and `$XDG_CONFIG_HOME` or `~/.config` on Linux.

| File | Purpose |
|---|---|
| `state.json` | Roots, workspaces, and terminal tab metadata |
| `config.json` | TUI settings such as the last workspace and tab, workspace pane width, mouse passthrough, and diff layout |
| `daemon.sock` | Local client-daemon Unix socket |
| `daemon.sock.lock` | Lock held by the running daemon |
| `daemon.log` | Detached daemon output and failures; archived before daemon startup after reaching 3 MiB |
| `daemon.log.1` | Previous daemon log, with one archive retained |

Set `ROMTY_HOME` to use an isolated location:

```sh
ROMTY_HOME=/tmp/romty-dev romty
```

Keep the path shallow. The `daemon.sock` path must remain below the operating system Unix socket limit: 104 bytes on macOS and 108 bytes on Linux.

## Security boundary

The runtime directory and Unix socket are private to the current user. Access to the socket is equivalent to control of every shell owned by romty: a connected process can read and write terminals, create sessions, or stop the daemon.

The runtime directory must be owned by the current user and cannot be a symbolic link. romty narrows it to mode `0700` and `daemon.log` to mode `0600`. It rejects a socket owned by another user or accessible by group or other users, and a log that is not a singly linked regular file owned by the current user.

Use `romty doctor` to check permissions, file formats, the shell, and daemon compatibility without starting the daemon. Inspect `daemon.log` when the detached daemon cannot report a failure to the TUI.

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

The client and daemon negotiate a supported protocol revision and independent capabilities. An older compatible peer keeps the features it understands. If no ordinary protocol overlaps, shutdown remains available and the error recommends `romty stop` before restarting with the new binary.
