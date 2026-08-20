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

Tagged releases publish public binary archives and a cask to
[`assignso/homebrew-tap`](https://github.com/assignso/homebrew-tap). The release
workflow requires a fine-grained `HOMEBREW_TAP_GITHUB_TOKEN` Actions secret with
contents write access to that repository.

Install the current preview with:

```sh
brew install assignso/tap/assign
```

After the tap is present, `brew install assign` also resolves the cask for
tapped users.

The `v0.1.0-preview.1` macOS binaries are not Developer ID signed or notarized.
Homebrew can install them, but macOS may block execution. Treat this as a
packaging preview rather than a supported production release.
