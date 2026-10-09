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
- Show a Task with its current revision, then start, complete, or reopen it
  with revision protection and idempotent requests.
- Discuss work in a full-screen terminal client with typed Task, action,
  approval, clarification, evidence, and activity widgets.
- Use concise, script-friendly output and predictable exit codes.
- Configure and authorize the Assign remote MCP server in Codex without sharing
  the CLI credential.
- Generate Bash, Zsh, and Fish completion without installing extra tooling.

```sh
# Find your work
assign
assign search "release notes"
assign discuss

# Keep context close
assign workspace switch another-workspace
assign project switch PRO
assign project tasks
assign project open PRO
assign document show release-plan
assign document url release-plan

# Make a safe workflow change
assign task show PRO-123                 # prints the Task, including Revision: 7
assign task done PRO-123 --revision 7
assign task done PRO-123 --revision "$(assign task show PRO-123 --field revision)"

# Set up the separately scoped Assign MCP connection in Codex
assign mcp setup codex
```

`assign discuss` opens the current Workspace's canonical Discuss thread in an
interactive terminal. It requires a TTY. Availability follows the Workspace
plan: full access includes the composer, read-only access keeps history
visible, and preview access shows the upgrade boundary without loading private
history.

The Go CLI keeps ownership of the credential. The bundled terminal renderer
talks to a short-lived loopback broker and never receives the access or refresh
token. Installers include the renderer, so Node.js and Bun are not runtime
requirements. CLI sessions created before the Discuss scope was introduced
must run `assign login` once. A personal API token needs the
`assign:discuss` scope.

Responses stream into the canonical transcript and reconnect with server-issued
cursors. Task, source, and saved-work widgets print exact Assign Web URLs when
the server provides a trusted identity and version; opening them rechecks your
current Workspace access.

## Initialization and configuration (Upcoming)

After signing in, select a Project for your current Workspace:

```sh
assign init PRO
assign task related PRO-123 --compact
```

`init` validates the Project and saves the same local selection as `project switch`.
The selection belongs to the current API host and Workspace. `task related` is an
alias for `task context`: it returns the bounded Task packet, including readable
related Tasks, and reports truncation or omissions on stderr.

Inspect or save an API host for future commands:

```sh
assign config show
assign config set host https://api.assign.so
assign config unset host
```

An explicit `--host` overrides the saved setting for one command. The default is
`https://api.assign.so`. Settings contain only the HTTPS host; credentials stay in
their existing credential store. `ASSIGN_CONFIG_FILE` overrides the settings-file
location. These commands are implemented locally and await a CLI release.

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

Build and test the bundled terminal renderer:

```sh
cd terminal
npm ci
npm run typecheck
npm test
npm run build:all
```

Release archives target macOS, Linux, and Windows on both `amd64` and `arm64`.

## Loop workflow primitives (upcoming)

Read one bounded Task page and pass selected context to your external work runner:

```sh
assign project tasks PRO --limit 50 --format json
assign task context PRO-123 --compact
assign task create "Implement one acceptance gate" --project PRO --field code
printf '%s\n' 'Prepared checks passed; parent integration remains open.' | assign task comment PRO-123 -
```

Task pages accept an opaque `--cursor`, `--limit` (1–100) and scalar `--field code`,
`next_cursor` or `has_more`. The command-specific JSON format includes both Tasks
and continuation under `assign.cli.task-page.v1`. Context defaults to 16 KiB;
`--full` uses 64 KiB. Read stderr for omitted or truncated context.

`done` lets the server pick the completing Status. When a Project has several
Statuses that count as completed, the server answers `completion_target_required`
and the CLI exits 5 naming that rule; complete the Task in the web app or MCP
with an explicit `target_status_id`. A stale revision is reported separately.

Create/comment/start/done/reopen accept `--idempotency-key` for an identical retry.
Keep the original arguments and revision when reconciling an uncertain write;
a newly decided operation needs a new key. Descriptions/comments accept `-` for
nonempty UTF-8 stdin, capped at 256 KiB plus the existing encoded-payload limit.

The external host owns Agent execution, run limits and cancellation. These CLI
primitives do not automatically link subtasks, select Milestones, infer readiness
or complete parents. They are implemented locally and have not been released.

### Milestone reads (implemented locally, awaiting qualification/release)

`assign milestone show FRS-M1` reads one authorized Milestone and canonical Task progress using the existing bearer transport. Use `--field revision` for a scalar or `--json` for the canonical projection; these flags are mutually exclusive. The Milestone number is decimal text, with ASCII case normalization and strict signed-64-bit bounds. Missing/inconsistent reference or progress data fails rather than showing false zero. This is read-only; it does not launch or complete work. See the [owning contract](../architecture/core/milestone-references.md#cli-credential-and-show-parity--asg-209-continuation-2026-09-28) and [command guide](../docs/cli/commands.md). No tests/builds/CI were run under the current Core/Web gate.

`assign milestone tasks FRS-M1` reads one checked member-Task page through the existing canonical Workspace Task query. Use explicit `--limit`/`--cursor`, `--format json` for scope/revision/coverage or `--field code|next_cursor|coverage`. Partial pages retain coverage; stale or scan-limit results exit nonzero, including when there is no next cursor. This is membership selection across all non-archived statuses, not runnable-work or completion proof. [Owning contract](../architecture/core/milestone-references.md#bounded-cli-membership-selection--asg-209-continuation-2026-09-28). Implemented locally, unverified, awaiting release; no checks ran under the full Core/Web gate.

`assign task context FRS-12 --milestone FRS-M1 --compact` adds canonical membership/revision observations before and after the bounded context fetch. Archived, mismatched, changed or inaccessible scope fails before output. Successful Markdown records the observed scope/revisions; the header and context together retain the 16/64 KiB cap. Five application reads do not constitute an atomic claim or complete dependency snapshot. [Owning contract](../architecture/core/milestone-references.md#milestone-scoped-task-context--asg-209-continuation-2026-09-28). Implemented locally, unverified, awaiting qualification/release.
