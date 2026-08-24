# Assign CLI

Native command-line client for Assign 3.0. Product behavior and delivery order
are owned by [CLI-001](../architecture/integrations/cli.md) and
[ROADMAP-0012](../architecture/roadmap/developer-cli.md).

This repository currently contains the release foundation and first API-backed
slice, not the complete terminal product. It implements version reporting,
Bash/Zsh/Fish completion, HTTPS host validation, redacted diagnostics, stable
exit codes, browser loopback-PKCE login, automatic refresh rotation, logout,
bounded My Work/search, Workspace switching, Project selection/list/show/task
and Document list/show navigation, revisioned Task actions, and
macOS/Linux/Windows release builds. Interactive credentials prefer the native
OS credential vault with a restricted atomic-file fallback. Full Task detail,
browser handoff, and the remaining release qualification are pending.

Command aliases are reviewed, documented shortcuts rather than a second command
language. Run `assign aliases` for the current table or use command help to see
aliases for one command. Examples include `assign find` for `assign search`,
`assign t done` for `assign task done`, and `assign diag` for `assign doctor`.
Resource aliases are `workspace/ws`, `project/p`, `task/t`, and
`document/doc`; subcommands also expose conservative shorthands such as
`list/ls`, `show/view`, and `switch/use/sw`.

Common navigation commands:

```sh
assign ws list
assign ws switch another-workspace
assign p list
assign p switch PRO
assign p tasks
assign p docs
assign doc list --project PRO
assign doc show release-plan
```

Workspace enumeration/switching requires interactive login because personal
API tokens are intentionally Workspace-bound. Project selection is a local
host-and-Workspace-partitioned hint; the server still authorizes every request.

## Development

```sh
go test ./...
go build -o assign .
./assign version
./assign login
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

The historical `v0.1.0-preview.1` macOS binaries are not Developer ID signed or notarized.
Homebrew can install them, but macOS may block execution. Treat this as a
packaging preview rather than a supported production release.

Coordinated releases use exact unprefixed Semantic Version tags such as
`1.0.0-rc.1`; the historical prefixed preview is not the naming convention for
future releases.
