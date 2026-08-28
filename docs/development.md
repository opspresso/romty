# Development

## Build and test

Build the command from a local checkout:

```sh
go build -o build/romty ./cmd/romty
```

Run the same checks used by CI:

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

## Sound assets

The original notification recordings are `internal/sound/assets/done.mp3` and `waiting.mp3`. Runtime playback embeds their PCM WAV derivatives from the same directory so macOS and common Linux audio tools can play them without an in-process decoder. Regenerate a derivative without changing the original:

```sh
ffmpeg -y -i internal/sound/assets/waiting.mp3 -c:a pcm_s16le -ar 44100 -ac 1 internal/sound/assets/waiting.wav
```

## Protocol compatibility

Keep field meanings and stream framing stable within a protocol revision. Additive actions and optional JSON fields do not require a new revision. Advertise independently usable behavior as a capability and ignore unknown fields and capabilities.

Raise `MinimumVersion` only when retaining an older wire contract is unsafe. Breaking field semantics or framing require a new revision and an adapter for every revision still in the supported range.

Protocols 1 through 5 are currently supported. Foreground agent detection requires protocol 2, ordered snapshot revisions require 3, workspace removal requires 4, and bounded initial replay plus hook-derived agent phases require 5.

## Website and documentation

The static website source is in `pages/`. Keep product and contributor guides in `docs/`; do not copy the website into that directory. Relative website asset paths must remain valid from `pages/index.html`.

`.github/workflows/pages.yml` uploads `pages/` and deploys it through GitHub Pages whenever website files change on `main`. The repository Pages source must be set to GitHub Actions, not the legacy `main:/docs` branch source.

## Releases

Pushing a version tag such as `v0.2.0` runs the release workflow. It validates the project, publishes macOS and Linux archives for amd64 and arm64 to GitHub Releases, and updates the `romty` formula in `opspresso/homebrew-tap`.
