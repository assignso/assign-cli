# Changelog

## Unreleased

- Upcoming: Milestone Task selection reads the canonical checked page, including short or empty pages with a continuation. Milestone show and selection accept `--workspace-id` for personal and service credentials. Scoped Task context uses exact Milestone reads and drops the packet if Task or Milestone revisions change.

- Upcoming: `start`/`done`/`reopen` now report lifecycle 409s (`completion_target_required`, `workflow_status_unavailable`, `lifecycle_migration_pending`, `task_inactive`) with their own message and exit code 5 instead of a stale-revision hint; plain `revision_conflict` and servers without lifecycle codes keep the existing text. `done` accepts `--target-status-id <uuid>` for an explicit completing Status; without it the server uses the single applicable completing Status or answers `completion_target_required`. `start` and `reopen` keep the server's in-progress adapter. Source is unverified and unpublished.

- Upcoming: add `init <project-code>`, local `config show/set host/unset host`, and the `task related` alias for bounded Task context. Explicit `--host` overrides the saved HTTPS host; Project selection remains scoped to the host and Workspace.

- Upcoming: add explicit `activity groups` and `activity children <group-id>` JSON page reads with scope, day/range, filters and opaque continuation. These commands use existing API read credentials and server-provided counts/coverage. Source is unverified and unpublished.
