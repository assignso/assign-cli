# Assign CLI

## Install

Homebrew is available for macOS and Linux:

```sh
brew install assignso/tap/assign
```

Signed stable releases also support a user-scoped install with no Homebrew,
Node.js, or package manager required:

```sh
# macOS or Linux
curl https://assign.so/install.sh | sh
```

```powershell
# Windows PowerShell
irm https://github.com/assignso/assign-cli/releases/latest/download/install.ps1 | iex
```

`https://assign.so/install.sh` is a small macOS/Linux wrapper that checks the
operating system and runs the GitHub Release installer. The release asset
remains the single source of installer logic and archive verification.

The installers select the right CPU archive, verify its SHA-256 checksum, and
refuse an unsigned macOS or Windows executable. They install only into a
user-owned directory.

> **Note:** The historical `0.1.0-preview.1` macOS build is not Developer ID
> signed or notarized, so macOS may block it. Use a signed stable release for a
> supported macOS installation.

Work with Assign without leaving your terminal. `assign` is a fast, native
command-line client for developers, keyboard-first teams, scripts, and local
automation.

```sh
assign login
assign
```

The default command shows your current work. Keep the browser for the moments
when it is useful—not for every Project, Task, or Document lookup.

## What you can do today

- Sign in securely through your browser with PKCE, then refresh credentials
  automatically through your operating system’s credential vault when available.
- See My Work, search Tasks and Documents, and move between Workspaces.
- List, select, and inspect Projects and their Tasks or Documents.
- Start, complete, or reopen a Task with revision protection and idempotent
  requests.
- Use concise, script-friendly output and predictable exit codes.
- Configure and authorize the Assign remote MCP server in Codex without sharing
  the CLI credential.
- Generate Bash, Zsh, and Fish completion without installing extra tooling.

```sh
# Find your work
assign
assign search "release notes"

# Keep context close
assign workspace switch another-workspace
assign project switch PRO
assign project tasks
assign document show release-plan

# Make a safe workflow change
assign task done PRO-123 --revision 7

# Set up the separately scoped Assign MCP connection in Codex
assign mcp setup codex
```

## Built for the shell

The CLI keeps data on stdout and diagnostics on stderr, so normal shell tools
stay useful. `ASSIGN_TOKEN` supports non-interactive use without placing a
secret in command history or arguments.

```sh
export ASSIGN_TOKEN='apt_...'
assign project list
assign search "on-call"
```

Aliases are short spellings of the same command—not a second command language:

```sh
assign ws list                 # workspace list
assign p tasks                 # project tasks
assign doc list --project PRO  # document list --project PRO
assign t done PRO-123 --revision 7
assign aliases
```

Run `assign <command> --help` for the complete options and aliases available in
your installed version.

## Updates

Check the release channel used by your installed version:

```sh
assign update check
```

For a direct installation, print the right installer command with:

```sh
assign update install
```

Homebrew-managed installations stay under Homebrew’s control:

```sh
brew upgrade --cask assign
```

The CLI never silently checks for or installs an update while you run normal
commands.

## Develop locally

```sh
go test ./...
go build -o assign .
./assign version
./assign login
ASSIGN_TOKEN=test ./assign doctor
```

Generate shell completion during development:

```sh
./assign completion zsh > _assign
```

Release archives target macOS, Linux, and Windows on both `amd64` and `arm64`.
