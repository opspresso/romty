# Development

## Build and test

Build the command from a local checkout:

```sh
go build -o build/romty ./cmd/romty
```

CI runs only before a release: the release workflow calls `ci.yml` first, and pull requests and pushes do not start it. Run it by hand with `gh workflow run ci.yml --ref <branch>` when a branch needs the hosted macOS and Linux checks. Before merging, run the same checks locally:

```sh
test -z "$(gofmt -s -l .)"
go vet ./...
go test -race ./...
go build ./...
go install golang.org/x/vuln/cmd/govulncheck@v1.7.0
govulncheck ./...
```

The race detector is required because each terminal session coordinates several goroutines around one emulator.

Run an isolated development instance without using normal romty state:

```sh
ROMTY_HOME=/tmp/romty-dev go run ./cmd/romty
```

## Code layout

`cmd/romty` parses the command line and starts the TUI, the daemon, or a CLI command. Everything else is under `internal/`. Dependencies point from the top of this table down; a package does not import a package above it.

| Layer | Packages | Responsibility |
|---|---|---|
| Entry point | `cmd/romty` | Command line, `doctor`, `status`, `list`, `hooks`, `service`, and the hook command agents run |
| Presentation | `ui` | Bubble Tea dashboard: layout, keys, mouse, modals, and the embedded terminal |
| Application | `daemon`, `client` | The daemon that owns sessions, state, the change stream and the shared Git reads, and the client that talks to it |
| Wire contract | `protocol` | Framed messages, protocol revisions, and capabilities |
| Domain | `model` | Roots, workspaces, tabs, agent phases, snapshots, change events, Git states, and the domain errors `ErrDaemonUnavailable`, `ErrNeedsNewerDaemon` and `RefusedError` |
| Infrastructure | `agenthooks`, `codexstate`, `gitrepo`, `jsonfile`, `logfile`, `paths`, `procinfo`, `service`, `sound`, `state`, `transcript`, `update`, `workspacefs`, `display`, `version` | Files, processes, Git, the daemon log, the login service, agent integrations, and the network |

Keep side effects such as running `git`, reading workspace files, or calling GitHub in an infrastructure package. The `ui` package decides what to show and when; it does not run commands or read the filesystem itself.

`ui` reaches the daemon only through its `Backend` interface and knows only `model` types. It does not import `client` or `protocol`: the client adapter turns a socket failure into `model.ErrDaemonUnavailable` and a refusal into `model.RefusedError`, so the dashboard can be tested with a fake backend.

Inside `ui`, `dashboard_state.go` groups the dashboard's fields by the feature that owns them (the daemon link, alerts, the tree, tabs, the terminal, modals, the status bar, and Git views), and each feature's code and tests live in files named after it, such as `tabs.go`, `daemon_link.go` and `branch_picker.go`.

## Sound assets

The original notification recordings are `internal/sound/assets/done.mp3` and `waiting.mp3`. Runtime playback embeds their PCM WAV derivatives from the same directory so macOS and common Linux audio tools can play them without an in-process decoder. Regenerate a derivative without changing the original:

```sh
ffmpeg -y -i internal/sound/assets/waiting.mp3 -c:a pcm_s16le -ar 44100 -ac 1 internal/sound/assets/waiting.wav
```

## Protocol compatibility

Keep field meanings and stream framing stable within a protocol revision. Additive actions and optional JSON fields do not require a new revision. Advertise independently usable behavior as a capability and ignore unknown fields and capabilities.

Raise `MinimumVersion` only when retaining an older wire contract is unsafe. Breaking field semantics or framing require a new revision and an adapter for every revision still in the supported range.

Protocols 1 through 10 are currently supported. Foreground agent detection requires protocol 2, ordered snapshot revisions require 3, workspace removal requires 4, bounded initial replay plus hook-derived agent phases require 5, closing an individual tab requires 6, renaming a tab requires 7, reopening a closed tab requires 8, moving a tab requires 9, and the change stream (`subscribe`) with `refresh_git` requires 10. A TUI connected to a daemon without protocol 10 polls it instead of subscribing. The ping reply also carries the daemon's build (`daemon_version`), which a TUI compares with its own to say that an upgraded daemon needs a restart.

## Website and documentation

The static website source is in `pages/`. Keep product and contributor guides in `docs/`; do not copy the website into that directory. Relative website asset paths must remain valid from `pages/index.html`.

`.github/workflows/pages.yml` uploads `pages/` and deploys it through GitHub Pages whenever website files change on `main`. The repository Pages source must be set to GitHub Actions, not the legacy `main:/docs` branch source.

## Releases

Pushing a version tag such as `v0.2.0` runs the release workflow. It runs the full CI on macOS and Linux (tests, release contract and vulnerability check) first, publishes macOS and Linux archives for amd64 and arm64 to GitHub Releases, and updates the `romty` formula in `opspresso/homebrew-tap`.
