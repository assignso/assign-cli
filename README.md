# Assign CLI

Native command-line client for Assign 3.0. Product behavior and delivery order
are owned by [CLI-001](../architecture/integrations/cli.md) and
[ROADMAP-0012](../architecture/roadmap/developer-cli.md).

This repository currently contains the release foundation, not the complete
terminal product. It implements version reporting, Bash/Zsh/Fish completion,
HTTPS host validation, redacted local diagnostics, stable initial exit codes,
and macOS/Linux/Windows release builds. API-backed Home, authentication, and
resource commands remain gated on their accepted backend and OpenAPI contracts.

## Development

```sh
go test ./...
go build -o assign .
./assign version
ASSIGN_TOKEN=test ./assign doctor
```

Generate completion without installing extra tooling:

```sh
./assign completion zsh > _assign
```

## Release packaging

GoReleaser builds native `darwin`, `linux`, and `windows` archives for `amd64`
and `arm64`, then publishes checksums with a tagged GitHub release. Run a local
snapshot without publishing with:

```sh
goreleaser release --snapshot --clean
```

Homebrew publication is intentionally not enabled yet. Assign is closed source,
so `homebrew/core` is not an eligible route; the release needs a public binary
artifact decision and an `assignso/homebrew-tap` repository before a cask can be
published. The first usable path will be `brew install assignso/tap/assign`;
after the tap is present, `brew install assign` also works for tapped users.
