# TypeScript CLI feasibility scaffold

Use Bun `1.4.3+c6da4a4d3` and run these commands from `command/`.

```sh
bun install --frozen-lockfile
bun run test:local
```

This runs strict type checking and the local tests, then builds `dist/assign` and `dist/assign-fixture`. Local builds and prepared tests require no signing certificate or notarization. On macOS the App Sandbox replay signs its native helpers ad hoc with `codesign --sign -` to attach the sandbox entitlements. Follow `.codex/qualification-evidence/cli-2043-20261111/README.md` from the Assign workspace for that probe. Publication runs the platform signing workflow.

The ordinary build compiles `src/entry.ts` to `dist/assign` (`assign.exe` on Windows). `ASSIGN_BUILD_VERSION`, `ASSIGN_BUILD_COMMIT` and `ASSIGN_BUILD_DATE` are optional build-time inputs. `version` skips Commander. Other scaffold commands load the parser and settings lazily. The existing `terminal/` package owns React, Ink and the renderer. Native feasibility embeds the built renderer as a file asset and loads it only in renderer mode.

The configuration adapter uses the existing Go schema-1 settings file: `--host` overrides the stored host, then the default applies. `ASSIGN_CONFIG_FILE` changes the settings location, not host precedence. Configuration reads make no remote calls. Unsupported versions and malformed settings fail with exit 2; `config set host` and `config unset host` can repair malformed contents. Invalid explicit hosts fail before a command runs.

The Project context adapter reads and atomically updates the Go schema-1 `projects` map, keyed by HTTPS host, newline and credential-bound Workspace. Updating one selection preserves the other partitions. `ASSIGN_CONTEXT_FILE` overrides its location independently of settings. Default files live under `assign/` in APPDATA on Windows, `~/Library/Application Support` on macOS, or XDG_CONFIG_HOME / `~/.config` on Linux. Paths use native APIs, including drive letters, spaces and Unicode. These adapters accept only host/Workspace/Project inputs; credentials remain in the OS vault. Resource commands must obtain the authorized Workspace before saving a hint; a hint grants no permission.

Rollback: stop using the candidate and run the retained Go binary/release from `assignso/assign-cli` with the same `ASSIGN_CONFIG_FILE` and `ASSIGN_CONTEXT_FILE` overrides (or unchanged default directories). No migration or copy is required: settings stay schema 1 and context stays `{schema:1,projects:{"<host>\n<workspace>":"<project>"}}`. The TypeScript candidate never modifies the Go binary or credential vault. Do not delete context to roll back. Settings intentionally reject unrelated fields (including tokens); unrelated context partition keys are preserved. Native Windows filesystem/ACL qualification remains owned by the rewrite platform gate; local path-shape tests do not prove native ACL behavior.

## Prepared verification

Run the compiled Bun fixtures locally with Python and a writable scratch directory. Use `assign-fixture.exe` on Windows.

```sh
python3 fixtures/harness.py --candidate-only --candidate dist/assign-fixture \
  --scratch /path/to/scratch --receipt /path/to/scratch/commands.json
```

The receipt records `candidate-only`. Omit the flag for paired Go parity evidence.

Build the exact reviewed SDK from a checkout containing its pinned revision. This step acquires public build dependencies. The subsequent fixture runs are offline. The staging script archives the reviewed commit and packs the generated SDK, so it does not use uncommitted sibling files or add a workspace dependency to the ordinary executable.

```sh
python3 scripts/stage-sdk-fixture.py --sdk-checkout /path/to/typescript-sdk --output /path/to/new-sdk-fixture
python3 fixtures/harness.py --scratch /path/to/scratch --receipt /path/to/commands.json \
  --candidate /path/to/new-sdk-fixture/candidate/dist/assign-sdk-fixture
```

Use Python 3.10.18 or later and the pinned Bun. SDK staging also uses Node 24 and npm 11. For Go parity the harness builds a test-only overlay with Go `1.26.10`, `GOPROXY=off` and preinstalled baseline dependencies, or takes an existing probe via `--go-probe`. Outside macOS supply a host-native `--deny-network` wrapper. The harness checks IPv4 and IPv6 denial before executing fixtures. It starts children with isolated configuration, dummy credentials and a prepared request sequence. Use the `.exe` fixture on Windows.

| Selector | Producer | Evidence |
| --- | --- | --- |
| Prepared task read and malformed response | 1951 and 1953 | Request trace, decimal-string revision, streams and exit. The staged binary runs the reviewed SDK operation for the task list. |
| Mutation conflict and closed stdout pipe | 1951 | Request trace, idempotency header, streams and exit. |
| Unexpected request and unconsumed fixture | 1951 | Fail-closed exit and request trace. |
| Fixture child stdin, EOF and termination | 1952 and 2042 | Prepared bytes round-trip to stdout, then a separate child stays active until the harness terminates it. |

`fixtures/cases.json` fixes request IDs and response payloads. The JSON receipt records each selected assertion and pass/fail outcome. `dist/assign-fixture` uses the same harness with a direct prepared fetch. The staged SDK fixture, its archived source and the Go overlay are feasibility inputs.


Shared transport fixtures use an offline export of SDK revision `d12065d63d9dd327b2e42f14ada155822f31702e`:

```sh
ASSIGN_TEST_SDK_SOURCE=/absolute/path/to/pinned-sdk-export bun test fixtures/transport.test.ts
```

`src/transport.ts` accepts the public SDK recovery binding at composition time. Use its configuration with the SDK Configuration constructor, call raw operations, and decode with an operation-owned byte limit before validating the complete result and writing output. Cursor traversal remains the SDK's `iteratePages`; pass its signal through each raw request. Wrap the whole request/decode/validation operation with `sdkResult` and the caller's signal. Numeric tokens outside the safe range fail; decimal strings retain their exact spelling. Integer-valued fraction/exponent tokens are conservatively rejected to avoid rounding fractional revisions into integers. Generated model conversion must follow this boundary, never precede it.

The default SDK policy retries reads only. Supply a retry classification only from the canonical operation owner; the SDK retains the original key/body/preconditions. No credential refresh or operation resubmission is implicit. Negotiated capability metadata still needs a canonical producer contract; HTTP error handling alone does not establish that negotiation. Public SDK package/export approval and distribution remain separate from these private candidate checks.
