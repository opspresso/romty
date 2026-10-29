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

### SSH agent forwarding

An SSH login that forwards its agent gets a new `SSH_AUTH_SOCK` path on every login, and a shell keeps the path it started with. romty therefore gives its tabs a fixed path, `ssh-agent.sock` in the runtime directory, and each romty that connects points it at its own login's agent. A tab started yesterday reaches today's agent, so `git push` and commit signing keep working after a reconnect. When two logins are connected, the one that connected last wins. A login whose agent socket does not exist leaves the link on the previous agent.

### Keeping the machine and the daemon running

- **Sleep.** While an agent is busy (`◐`), the daemon holds the machine awake against idle sleep with `caffeinate -i` on macOS or `systemd-inhibit --what=idle:sleep` on Linux. The workspace pane title shows `☕` while an agent is busy and the setting is on; the mark comes from the TUI's own view, so it also shows when the daemon is older than v0.48.0 or the tool is missing, and the daemon logs why it cannot hold. The hold follows the agents within a TUI poll, or within 30 seconds with no TUI open, and ends with the daemon. It does not stop a sleep you start, a closed lid, or a shutdown. A tool that ends at once, for example without logind, is logged and not started again. Turn it off with `w` in Config or `"allow_sleep": true` in `config.json`.
- **Login and logout.** `romty service install` makes the daemon a login service and starts it: a LaunchAgent on macOS, or a systemd user unit on Linux (systemd 240 or later). Once the service is installed, every start of the daemon goes through it, including the start after `romty stop`. A daemon already running when you install the service stays outside it until it restarts; the command says so. On Linux, systemd-logind can end the processes of a login when it closes (`KillUserProcesses=yes`); a user unit is not part of a login, so it is not ended with it. To keep the daemon with nobody logged in, also run `loginctl enable-linger`; `romty service` reports whether lingering is on. `romty service uninstall` removes the service.
- **Upgrades.** Upgrading romty replaces the binary, but the running daemon stays the old build until it restarts. romty compares the daemon's build with its own and, when they differ, says so in the status bar, in About, and in `romty status`. Run `romty stop` and then `romty` to update it; `romty stop` saves each tab's output and agent session first, and with the service installed the new daemon starts as the service. The service runs the binary at the path it was installed from, so an upgrade that moves the binary needs `romty service install` again.

Tailscale and SSH are transport and login layers, not romty dependencies. romty does not listen on the tailnet or expose a TCP port; the TUI still reaches the daemon through the remote host's user-owned Unix socket. Normal SSH and host security practices remain responsible for remote access.

## Session lifecycle

The TUI communicates with a detached local daemon over a Unix socket. The daemon owns the shell PTYs, so closing the TUI or its host terminal does not terminate running sessions. Reopening romty reconnects and restores recent output before continuing with the live stream.

If `daemon.sock` is removed while the daemon is running, the daemon recreates it without restarting shells or disconnecting existing terminal streams. A client waits for this recovery while the daemon holds `daemon.sock.lock`. Never delete the lock to force another daemon to start: both daemons would then own the same state. If an older daemon cannot restore its missing socket, identify its process and send it `SIGTERM` before restarting romty. This orderly shutdown saves session snapshots but stops the managed shells and agents.

New terminals use the environment and `$SHELL` of the romty client that creates them. They do not inherit the potentially older environment of the detached daemon.

Each session retains up to 8 MiB of terminal output. When all sessions together hold more than 256 MiB, the daemon trims the quietest sessions to 1 MiB each; a trimmed session replays less output when it is reattached and keeps everything it prints after that. `romty status` shows the total under `recordings`. Terminal modes are tracked separately so a replay remains usable after older output is trimmed. Completed terminal queries are omitted from a replay to prevent an unsolicited response from reaching the shell command line. Reconnect replay starts at the PTY dimensions that produced the latest output, then resizes the local emulator to the current pane; width-sensitive shell redraws therefore keep the wrapping and erase behavior they had when recorded.

A dropped terminal connection reconnects automatically with backoff. Press `Enter` on a disconnected tab to retry after automatic attempts stop. Closing a tab from its `×` or from the workspace actions ends it and removes the tab, after a confirmation that names the agent running in it. The daemon sends `SIGHUP` to every process in the tab's terminal session, including jobs the shell started in the background, and sends `SIGKILL` to whatever still runs two seconds later. An agent uses the two seconds to save its session; a job that ignores `SIGHUP`, such as one started with `nohup`, does not outlive its tab. A process that started a session of its own, as daemons do, is not part of the tab and keeps running; the systemd unit uses `KillMode=process` so that this holds under the service too. A process the daemon may not signal, such as one run with `sudo`, is not ended and is not waited for. When the daemon stops, all tabs end this way at the same time. The daemon first saves the tab's output and agent session under `closed/`, so the tab can be reopened from the workspace; see [Opening a workspace without a terminal](interface.md#opening-a-workspace-without-a-terminal). When a shell exits on its own, its tab is also removed. If the daemon or operating system stops, roots and workspace metadata remain but stale PTY tabs are discarded.

More than one TUI can attach to the same terminal. Every client receives output, while the client that most recently sent input owns the PTY size. Resizing a background TUI does not disturb the active terminal; sending input promotes that client and applies its viewport first. If an attached client stops reading and exhausts its bounded output queue, only that client is disconnected.

Disconnecting a slow client never waits for terminal input, so output continues to drain even when the guest is busy writing. Input to a guest that stops reading is interrupted after two seconds and that attachment closes. Closing a terminal also interrupts pending PTY I/O, allowing tab and daemon shutdown to finish.

The daemon reads a hooked Claude Code session's own transcript to report its token and cost counters and to detect a user interrupt, which runs no hook. It reads at most the last mebibyte of that file, skips any record larger than 64 KiB, and re-reads a transcript only after it changes. It decodes the counter fields and each record's type and time. It decodes a record's message content only when the line contains Claude Code's interrupt marker, and keeps only the interrupt time. For a hooked Kiro CLI session, the daemon reads the session record under the Kiro home with the same bounds, only to detect a cancelled turn, which runs no Stop hook. Prompts, tool inputs, and assistant messages are not retained.

romty refuses to start inside one of its own terminal sessions.

Each open TUI subscribes to the daemon, which sends it the workspace tree, the agent statuses, and the Git states when they change. The daemon reads Git state once for all TUIs. It reads a repository when the repository's metadata changes (`HEAD`, `index`, `FETCH_HEAD`, `ORIG_HEAD`, or `packed-refs`), when one of its terminals printed since the last read, when the last read is a minute old, or when a TUI asks with `F5`. It fetches remote-tracking refs every five minutes. An edit to a tracked file that is not staged changes no metadata, so a repository whose terminals print nothing shows it within a minute. A daemon from before v0.49.0 has no subscription, and the TUI polls it as earlier versions did.

The daemon rotates `daemon.log` when it reaches 3 MiB, keeping one previous log as `daemon.log.1`, both at start and while it runs.

Agent status requests from several open TUIs share one scan of the process table for up to a second. An agent's own report and every change to a tab start a new scan.

## Stopping and resuming

`romty stop`, or `F9` in the TUI, stops the daemon and every shell it owns. Before the shells go, the daemon saves each running tab's recorded output and agent session under `resume/` in the runtime directory.

The next daemon offers a saved snapshot back through the first tab created in its workspace:

1. The old output replays behind a `── restored from the previous romty session ──` marker.
2. When Claude Code, Codex, OpenCode, or Kiro CLI was running in the tab's foreground at the stop, its resume command is typed at the prompt. An agent that had already exited is not resumed.
3. Press `Enter` to continue the conversation, or `Ctrl+C` to decline.

| Agent | With a saved session identifier | Without one |
|---|---|---|
| Claude Code | `claude --resume <id>` | `claude --continue` |
| Codex | `codex resume <id>` | `codex resume` |
| OpenCode | `opencode --session <id>` | `opencode --continue` |
| Kiro CLI | `kiro-cli --resume-id <id>` | `kiro-cli --resume` |

Without an identifier, the agent chooses its most recent session. Unconsumed snapshots are discarded after seven days, or after `output_retention_days` (1 to 30) in `config.json`.

### After a crash

`romty stop` is not the only way a daemon ends: it can crash, be killed, or lose power with the machine. Every five minutes the daemon therefore saves the session of each tab that runs an agent into `checkpoint/`, the same way a stop does; a tab that printed nothing and kept its agent session since its last checkpoint is not written again. The next daemon moves those checkpoints into `resume/` and offers them like a stop's snapshots; a stop's newer snapshot of the same tab is kept instead. A tab that closes, or whose shell ends, drops its checkpoint. Plain shells are not checkpointed.

### Saved output and privacy

A saved session holds the tab's recent terminal output, up to 8 MiB, which can include anything the terminal printed: a token, the contents of a `.env` file, or a password typed into a prompt that echoed it. The files are readable only by you (mode `0600`) and are not encrypted.

- On macOS the daemon excludes the runtime directory from Time Machine with `tmutil addexclusion` when it starts. On Linux, exclude the runtime directory (`~/.config/romty` by default) in your backup tool.
- Set `"save_output": "off"` in `config.json` to save no output. A reopened or resumed session then starts empty, still with its agent's resume command typed at the prompt.
- Set `output_retention_days` (1 to 30) to keep saved sessions for a number of days other than the default seven.
- A `config.json` romty cannot read saves no output; a field it cannot read falls back on its own, and `save_output` then falls back to `off`.

## Data and configuration

romty stores data in a `romty` directory under the user configuration directory: `~/Library/Application Support` on macOS, and `$XDG_CONFIG_HOME` or `~/.config` on Linux.

| File | Purpose |
|---|---|
| `state.json` | Roots, workspaces, and terminal tab metadata |
| `config.json` | TUI settings such as the last workspace and tab, workspace pane width, mouse passthrough, and diff layout |
| `resume/` | Output and agent session of each tab that was running at `romty stop`; unconsumed snapshots are discarded after `output_retention_days` (seven by default) |
| `closed/` | Output and agent session of the last three tabs closed in each workspace, for reopening; entries are discarded after `output_retention_days` |
| `checkpoint/` | The latest session of each running agent tab, so a crash leaves sessions to resume; the next daemon moves them into `resume/` |
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

The runtime directory and Unix socket are private to the current user. Access to the socket is equivalent to control of every shell owned by romty: a connected process can read and write terminals, create sessions, or stop the daemon. Besides the socket's mode, the daemon reads the user ID of each connecting process (`LOCAL_PEERCRED` on macOS, `SO_PEERCRED` on Linux) and refuses every user but its own, so a runtime directory on a shared or misconfigured mount does not open the shells to other users.

A program in a tab can copy text to your clipboard with OSC 52, which is how vim, tmux, and agents copy over SSH. romty passes on such a copy only; a request to read the clipboard is never answered, so a program cannot see what you copied elsewhere. Turn copying off with `c` in Config or `"block_clipboard": true`.

The runtime directory must be owned by the current user and cannot be a symbolic link. romty narrows it to mode `0700` and `daemon.log` to mode `0600`. It rejects a socket owned by another user or accessible by group or other users, and a log that is not a singly linked regular file owned by the current user.

Workspace deletion keeps the validated root directory open and removes only its direct child through that directory handle. Replacing the root path after validation cannot redirect deletion to another directory.

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
