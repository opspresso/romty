# romty

> romty = roam + tty

romty is a terminal workspace manager for macOS and Linux. A detached local daemon keeps shell sessions running while the TUI is closed, and the next TUI reconnects with the terminal output intact.

Website: [romty.dev](https://romty.dev)

## Why romty

AI coding agents often need more time and compute than a laptop can reliably provide. romty is designed to run on an always-on workstation at home or at the office, beside the repositories and tools the agents use. Connect to that machine over Tailscale and SSH, start romty and an agent, then disconnect without terminating the terminal session. The next SSH login can reopen the same romty workspace and continue from its retained output.

Tailscale and SSH provide access to the machine; romty does not expose a network service. Its client and daemon communicate through a private Unix socket on that host. Sessions survive SSH disconnects and TUI exits, but not a machine shutdown or an explicit `romty stop`.

```text
laptop -> Tailscale -> SSH -> always-on machine -> romty -> AI coding agent
```

## Features

- Organize roots and their direct child directories as a workspace tree.
- Keep multiple terminal tabs alive independently of the TUI.
- Restore up to 8 MiB of recent output, and browse and search 10,000 lines of scrollback.
- Open full-width scrollback for native terminal selection and copying, with light or dark colors.
- Show Claude Code and Codex working and waiting states from their own output, and every phase through optional hooks.
- Use function-key navigation that remains reliable with an active IME.

## Install

With Homebrew:

```sh
brew install opspresso/tap/romty
```

Or with Go 1.25 or later:

```sh
go install github.com/opspresso/romty/cmd/romty@latest
```

## Start

```sh
romty
```

Press `F2` to add a root directory. Use `↑`/`↓` to select a root or workspace, `←`/`→` to select a terminal tab or `+`, and `Enter` to open it. Closing the TUI with `F4` leaves its shell sessions running.

Use `romty stop` outside the TUI when you intend to stop the daemon and every running shell.

## Documentation

- [Getting started](docs/getting-started.md) — requirements, installation, first run, and CLI commands
- [Interface](docs/interface.md) — workspace behavior, keyboard shortcuts, scrollback, mouse, and Git state
- [Runtime](docs/runtime.md) — session persistence, local data, security boundaries, and architecture
- [Agent status hooks](docs/agent-hooks.md) — Claude Code and Codex state reporting and hook installation
- [Development](docs/development.md) — local checks, protocol compatibility, website deployment, and releases

The complete documentation index is in [docs/README.md](docs/README.md).
