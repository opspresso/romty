# Getting started

## Requirements

- macOS or Linux
- Go 1.25 or later when building from source

romty manages shells local to the machine where it runs; it does not provide remote or SSH session management. To keep sessions on an always-on machine, connect to that host with SSH and run romty there. See [Remote access over SSH](runtime.md#remote-access-over-ssh).

## Install

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

## First run

Start romty:

```sh
romty
```

The daemon starts automatically. Press `F2` to open a directory picker, select a directory, and press `Enter` to add it as a root. romty lists the root and each direct child directory as workspaces:

```text
▾ projects
  - api
    (main)
  - web
    (main*) ↓2
```

Use `↑`/`↓` to select a root or workspace. Use `←`/`→` to select an existing terminal tab or the `+` tab, then press `Enter`. Selecting a workspace without an open tab creates one automatically.

Both roots and workspaces can host terminal tabs. A root tab starts in the root directory; a workspace tab starts in that direct child directory. Closing the TUI leaves all shells running so a later `romty` can reconnect.

See the [interface guide](interface.md) for the complete key map.

## CLI commands

| Command | Action |
|---|---|
| `romty` | Start or reconnect to the TUI |
| `romty status` | Show daemon, protocol, root, workspace, session, and agent counts |
| `romty version` | Show the client version |
| `romty help` | Show command-line help |
| `romty doctor` | Check runtime permissions, JSON files, the shell, and daemon compatibility |
| `romty hooks` | Detect Claude Code and Codex, then install or update their status hooks |
| `romty list` | List roots, workspaces, and running terminal sessions |
| `romty stop` | Stop the daemon and every running terminal session |

`status`, `doctor`, and `list` are read-only. They do not start the daemon or create the runtime directory. A missing runtime directory, state file, or config file is valid before the first run. `doctor` returns an error for an invalid runtime, malformed JSON, unavailable shell, unsafe socket, or incompatible daemon protocol.

`--version` and `-v` are aliases for `version`; `--help` and `-h` are aliases for `help`. Command output uses color on a terminal, stays plain when redirected or piped, and honors `NO_COLOR`.

`romty stop` is intended for explicit use outside a romty terminal. It stops the daemon and every shell it owns; stopping an unavailable daemon succeeds without output. Before the shells go, the daemon saves each running tab's recorded output and agent session under `resume/` in the runtime directory. The next daemon offers a saved snapshot back through the first tab created in its workspace: the old output replays behind a `── restored from the previous romty session ──` marker, and when Claude Code or Codex was running, the matching `claude --resume` or `codex resume` command is typed at the prompt — Enter continues the conversation, Ctrl+C declines. Unconsumed snapshots are discarded after seven days.
