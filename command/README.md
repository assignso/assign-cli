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

Rollback: stop using the candidate and run the retained Go binary/release from `assignso/assign-cli` with the same `ASSIGN_CONFIG_FILE` and `ASSIGN_CONTEXT_FILE` overrides (or unchanged default directories). No migration or copy is required: settings stay schema 1 and context stays `{schema:1,projects:{"<host>\n<workspace>":"<project>"}}`. The configuration/context adapters never modify the Go binary or credential vault. Do not delete context to roll back. Settings intentionally reject unrelated fields (including tokens); unrelated context partition keys are preserved. Native Windows filesystem/ACL qualification remains owned by the rewrite platform gate; local path-shape tests do not prove native ACL behavior.

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

## macOS credential adapter (internal candidate)

`src/credentials-macos.ts` provides read/write/delete via the pinned Bun 1.4.3
`secrets` API. The `MacOSSecrets` port allows fixtures without native access;
`nativeMacOSCredentialStore()` lazily binds that API on macOS only. The caller
must validate/select the host and run this code only in the credential-owning
process. Do not import it into the renderer. It adds no login command, IPC
route, environment override or plaintext fallback.

The service is `Assign CLI`; the account is the exact host string, unchanged
from Go. The stored JSON uses schema 1, `access_token`, `refresh_token`, and
RFC3339 `expires_at`, preserving subsecond text. Workspace binding remains an
authentication-layer responsibility; this adapter cannot broaden a grant.
Missing reads produce `not-found`; missing deletes succeed. Corrupt data produces
`invalid`. Native failures (including locked, denied and unavailable stores)
produce a fixed `unavailable` error without native messages or causes. Callers
must never log successful values. Operations use memory/native APIs, never shell
commands, arguments, environment credentials or configuration files.

The dependency decision is to reuse the runtime, with no added native addon,
FFI ABI, dynamic library or package install hook. The installed 1.4.3 Bun types
expose the exact get/set/delete signatures, and the compiled probe verifies the
native binding is present. [Bun's official secrets documentation](https://bun.com/docs/runtime/secrets)
describes macOS Keychain Services, service/name keys and potential first-use
permission prompts. The API is experimental; retain the exact runtime pin and
repeat qualification before upgrading. Documentation for newer Bun versions
does not establish compatibility of a new runtime.

Deterministic checks from `command/`:

```sh
bun run typecheck
bun test fixtures/credentials.test.ts
bun scripts/build.ts --credential-fixture
./dist/assign-credential-fixture
```

The compiled probe binds the real API without invoking it, then performs all
operations on injected prepared storage. It emits only a fixed success line.
These checks prove compilation and adapter behavior, not native vault access.

Native qualification (2043; installed signing 1811 and migration 1987) must record
macOS version/architecture/minimum supported OS, Bun revision, artifact hash,
`codesign -dv --verbose=4` identifier/team/requirements/entitlements for the
credential helper and renderer launcher, and the installed path. Local compiled
probes use ad-hoc signatures, not a Developer ID identity. Bun alone is not the
renderer isolation boundary: the selected App Sandbox launcher, separate native
credential identity and authenticated private broker must prevent the renderer
from invoking this adapter or reading its items/files/helper. Integrate this port
inside that credential owner before release; the factory alone grants no isolation.

Using a disposable test account/keychain and dummy credentials, qualify create,
read, update, delete, missing, denied access, locked store, unavailable store and
existing Go-item compatibility. Record each outcome and whether macOS prompts on
first access, unlock, denial/cancellation, identity or install-path changes. Do
not assume silent access, auto-approve prompts, change the user's search list or
inspect saved user credentials. Confirm denial does not trigger a plaintext
fallback. Record item cleanup and restored test state. Native access controls,
Data Protection Keychain choice, installed signing/notarization, migration and
the full platform/process matrix remain unverified by the prepared fixtures.

## Linux credential adapter (internal candidate)

`src/credentials-linux.ts` uses the private `native/secret-service.c` libsecret
transport. Unlike Bun.secrets, it queries the existing Go attributes `service=Assign CLI`
and `username=<exact host>` without matching a new schema name. Schema-1 JSON and
host partitions reuse the existing codec. Only the credential-owning process may
instantiate it. Pass the absolute packaged helper path to
`nativeLinuxCredentialStore`; do not resolve it through PATH or expose it to the renderer.

Linux builds require a C11 compiler, pkg-config and libsecret-1 development headers.
The build script compiles `dist/assign-secret-service`; Linux archives/installers
must include this executable beside the command, owned and writable only by the
same trusted installer/user as the command. Runtime requires libsecret-1.so.0 and
its GLib/GIO dependencies, a reachable user D-Bus session bus, an implementation of
org.freedesktop.secrets (for example GNOME Keyring), and an existing unlocked default
collection for writes. The helper does not create or unlock collections or prompt.
No sudo/system-bus substitute is supported. Both binaries must target the package's
Linux architecture; this build script is native-host only.

The helper receives NUL-delimited fields over stdin, never token arguments. It
returns fixed exit statuses: 4 absent, 10 missing session service, 11 locked,
12 denied, 13 otherwise unavailable. Native stderr is discarded; the adapter
returns only fixed diagnostics. Reads/deletes fail closed for locked matching
items, writes fail closed for a locked default collection. Missing entries and
repeated deletion remain distinct from service failure. A racing lock may fail
with unavailable rather than an interactive unlock. Each helper call has a
15-second process deadline.

Headless systems often have no session bus/service: installation does not imply
secure storage is usable. No plaintext fallback is implemented by this adapter;
it does not read/write the Go file fallback or honor ASSIGN_CREDENTIALS_FILE.
ASSIGN_TOKEN remains the separate noninteractive authentication path and must
bypass vault construction in command composition. This slice adds no login or
token resolver and does not change the serving Go CLI's file-fallback behavior.

`bun test fixtures/credentials-linux.test.ts` uses a fake service only, covering
read/write/delete, exact partitions and unavailable conditions without touching
a user vault. Native Linux compilation, service races, installed packaging and
renderer denial remain host qualification; macOS fake-store tests do not prove them.
