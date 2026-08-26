# Runtime

## Session lifecycle

The TUI communicates with a detached local daemon over a Unix socket. The daemon owns the shell PTYs, so closing the TUI or its host terminal does not terminate running sessions. Reopening romty reconnects and restores recent output before continuing with the live stream.

New terminals use the environment and `$SHELL` of the romty client that creates them. They do not inherit the potentially older environment of the detached daemon.

Each session retains up to 8 MiB of terminal output. Terminal modes are tracked separately so a replay remains usable after older output is trimmed. Completed terminal queries are omitted from a replay to prevent an unsolicited response from reaching the shell command line.

A dropped terminal connection reconnects automatically with backoff. Press `Enter` on a disconnected tab to retry after automatic attempts stop. When a shell exits, its tab is removed. If the daemon or operating system stops, roots and workspace metadata remain but stale PTY tabs are discarded.

romty refuses to start inside one of its own terminal sessions.

## Data and configuration

romty stores data in a `romty` directory under the user configuration directory: `~/Library/Application Support` on macOS, and `$XDG_CONFIG_HOME` or `~/.config` on Linux.

| File | Purpose |
|---|---|
| `state.json` | Roots, workspaces, and terminal tab metadata |
| `config.json` | TUI settings such as the last workspace and tab, workspace pane width, mouse passthrough, and diff layout |
| `daemon.sock` | Local client-daemon Unix socket |
| `daemon.sock.lock` | Lock held by the running daemon |
| `daemon.log` | Detached daemon output and failures |

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
