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

## Private worker registration binding

Import `createWorkerRegistrationClient` from `src/worker-registration/index.ts`.
It exposes registration, list, show, receipt recovery, policy read and policy
update (including pause, resume and profile revocation). Supply the selected
HTTPS API origin, an interactive Assign CLI OAuth token callback and an optional
fetch implementation. Methods return raw responses. The module exports success
and problem types by operation and keeps HTTP status and Retry-After available
to the caller. Preserve the original enrollment/profile intents for receipt
reconciliation and use the saved revision for each new policy edit. Core owns
authorization, schema validation and the owner quota.

Run these commands in `command/` with Bun 1.4.3:

```sh
# Generate from the checked CLI-targeted input, with no sibling checkouts
bun scripts/generate-worker-registration.ts generate
bun scripts/generate-worker-registration.ts check
bun test fixtures/worker-registration.test.ts

# Refresh the approved projection from the owning workspace sources
bun scripts/generate-worker-registration.ts project
bun scripts/generate-worker-registration.ts check-source
```

The projection records source file digests. The generated module records the
projection and generator digests. Keep the projection, generator and binding in
the private candidate. The public export requires independent approval. The
standalone device credential lifecycle currently has Core service methods and
requires a canonical CLI transport before a client can call it.

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

## Windows credential adapter (internal candidate)

`src/credentials-windows.ts` reuses the schema-1 codec and exact host account
partition. Its private C helper calls `CredReadW`, `CredWriteW`, `CredDeleteW`
and `CredFree` in system Advapi32, using SDK types rather than FFI layouts.
Generic targets are `Assign CLI:<exact host>` and UserName is the exact host,
matching go-keyring v0.2.8 and wincred v1.2.3. Blobs remain raw UTF-8 JSON bytes,
not UTF-16. `CRED_PERSIST_LOCAL_MACHINE` persists across logon sessions for the
same user on this device and does not roam. Logout deletes that exact generic
item; absent deletion succeeds. No migration copy or re-key is required; Go can
read the same schema after rollback. Never instantiate this port in the renderer.

The alternative Bun.secrets API in pinned Bun 1.4.3+c6da4a4d3 uses a slash target,
so it cannot preserve the existing Go identity. This helper adds a private native
packaging artifact but avoids an addon dependency and architecture-specific FFI
layout. Build with the Windows SDK and MSVC matching native Bun x64 or arm64:

```sh
bun scripts/build-windows-helper.ts
bun test fixtures/credentials-windows.test.ts fixtures/credentials-windows-native.test.ts
```

The ordinary Windows build also builds the helper; ship
`dist/assign-credential-manager.exe` alongside the command. The package must pass
its trusted absolute drive path to `nativeWindowsCredentialStore`. No PATH lookup,
PowerShell, shell interpolation or credential-bearing argv/environment is used.
Only the operation is an argument; bounded NUL-delimited stdin carries identifiers
and payload. The helper erases its buffers, frees native read allocations and
returns fixed codes: missing read 4, denied 12, other failures 13. The parent
suppresses native stderr and maps failures to content-free errors. Reads missing
from the vault use `not-found`; malformed schema uses `invalid`.

Local deterministic fixtures are prepared port evidence only. The explicit
native selector skips on other hosts. Run the commands above separately on native
Windows x64 and arm64 with the pinned Bun; record architecture, compiler identity,
pass/fail/skip counts and helper hash. Cross-compilation or emulation cannot close
native acceptance. Additional native checks must inspect target, UserName, blob
bytes and persistence through an independent Go/Win32 reader and verify denied
access with the actual renderer token. These native receipts are outstanding;
no installed-package, signing, renderer isolation or migration qualification is
claimed by local fixtures.

Sources: [Microsoft CredWriteW](https://learn.microsoft.com/en-us/windows/win32/api/wincred/nf-wincred-credwritew),
[Go adapter v0.2.8](https://github.com/zalando/go-keyring/blob/v0.2.8/keyring_windows.go),
and the workspace native feasibility packet's Existing credential identities section.

## Native feasibility jobs (2042)

`fixtures/native-jobs.json` defines six individually scheduled native host jobs.
It is preparation only: no workflow automatically allocates a paid runner. On each
host build ordinary and credential fixtures with the pinned Bun, stage the SDK
with `scripts/stage-sdk-fixture.py`, then run:

```sh
python3 fixtures/native-packet.py --job macos-arm64 --execution native \
  --ordinary dist/assign --vault-fixture dist/assign-credential-fixture \
  --sdk-fixture /path/to/sdk-stage/candidate/dist/assign-sdk-fixture \
  --sdk-identity /path/to/sdk-stage/sdk-identity.json \
  --scratch /path/to/scratch --receipt /path/to/packet.json
```

Build the dummy credential fixture with `bun scripts/build.ts --credential-fixture`.
It injects a documented in-memory vault transport and performs save/load/delete/
absent assertions; it never opens the user's vault. This is compiled adapter
feasibility evidence, not native secure-store or renderer denial qualification.
The SDK binary uses prepared fetch responses, never a provider transport.

Linux and Windows must additionally supply `--deny-network` as a JSON argv array
whose first element is the absolute approved native wrapper executable. The
wrapper must deny all descendant network access; IPv4/IPv6 negative controls run
before any fixture. Windows inputs must resolve to approved `.exe` files; `.cmd`
and `.bat` shims fail. No vendor executable is invoked by this packet. Vendor
sandbox, authentication and coding behavior remain unverified. Installer/launcher
and shim-to-native resolution cases remain with 1983.

Record `--execution emulated` when applicable; a passing emulated packet cannot
close a native host gate. For missing hosts use `--execution unavailable` with
scratch/receipt paths: it emits a failing receipt, never a skip/pass. Operator
attestation must include the actual host architecture (including Rosetta/WOW
emulation) and source/toolchain identities beside the hashed-binary receipt.
The runner records OS/release, machine, input hashes, SDK identity and assertions;
the adjacent `.commands.json` records individual prepared command results.
No aggregate six-target qualification is implied by one packet passing.

Orchestrator development check: `python3 fixtures/native-packet.test.py`.

Output projection (`CLI-TS-14`): `src/output.ts` owns compact deterministic Go-compatible JSON, scalar/list streams, terminal control/bidi sanitation and the 65,536-code-point Document excerpt. `src/richtext.ts` consumes canonical JSON without an editor model or browser dependency; unknown nodes/marks fail explicitly. Simple tables become GFM; complex tables retain compact lexical JSON in an inert `assign-table` fence. Code fences grow around embedded backticks. Output never adds ANSI or pads Unicode labels by UTF-16 length.

Run `bun test fixtures/output.test.ts fixtures/command.test.ts` and `bun run typecheck`. `node ../scripts/generate-command-richtext.mjs --check` checks the frozen CLI generated Core source hashes and generated projection limits. Deliberate source upgrades use the existing canonical generator first, then this generator and Go differential fixtures. Recreate the offline oracle from the CLI root with `go run ./command/fixtures/go-output > command/fixtures/richtext-go.json`; the checked fixture pins exact canonical values independently of JavaScript object insertion order. No API call or private application import is involved.

### Frozen shell grammar

`src/shell-contract.json` captures the retained Go registration tree, positional
arity errors, help, aliases and Bash/Zsh/Fish scripts. Regenerate from the CLI
repository (constructors only; no remote actions):

```sh
ASSIGN_SHELL_SNAPSHOT=../../command/src/shell-contract.json go test ./internal/cli -run '^TestShellSnapshot$' -count=1
```

`src/shell.ts` consumes this snapshot without loading credentials, Discuss or Go.
Command actions continue to belong to their individual port cohorts. The shell
selector is `bun test fixtures/shell.test.ts fixtures/command.test.ts fixtures/config.test.ts`.
The native packet includes shell generation and an argv case containing spaces,
quotes, backslashes and Unicode; execution on Windows retains its native owner.
The frozen negative cases cover arity, unknown/ambiguous words, typed flags,
mutual exclusion and version flags. `fixtures/shell-harness.py` runs the same
cases against a supplied native candidate and records its hash; it also checks
completion scripts and quoted Unicode argv. Native platform qualification
remains with the native packet and release owners.


## Private worker Task receipts

The private `src/worker-task-claims` client is generated from Core's v1
OpenAPI fragment. The `operations.ts` adapter maps HTTP and transport
failures to CLI exit statuses and carries the original operation ID through
recovery. The worker supervisor persists the exact intent before calling
`apply`. After an uncertain result, call that operation's `reconcile` with
the saved input. A missing receipt requires reconciliation of the Task, run
and local process before recovery. Keep active tokens and receipts private.

Use `bun scripts/generate-worker-registration.ts project worker-task-claims`
from this directory to refresh the projection from the workspace. Use
`generate worker-task-claims` to regenerate the binding. The matching
`check-source worker-task-claims` and `check worker-task-claims` modes check
the source and binding. The prepared `fixtures/worker-task-claims.test.ts`
selector runs in CW-07.

### Private external worker selection

The foreground supervisor calls `createExternalWorkerSelection` after
confirming the enrolled device connection and acquiring its local run slot.
Provide the supervisor's durable intent journal. Clear a claim intent only
after definite refusal or completed run reconciliation. A claimed selection
carries the current receipt for preparation. On a reconciliation-required
result, resolve the original saved intent and any native process state before
launch or further selection. Use the returned idle delay between passes.

The private `evidence` operation accepts an allowed status event and the
current run fence. A blocked event requires a bounded reason code. Persist
its exact input before dispatch and use the matching receipt operation after
uncertainty. Keep its token, workspace
binding and local process information in the supervisor. Core records only
worker/run references and the bounded event in the Task comment.

The prepared selection selector is `bun test fixtures/worker-selection.test.ts`
in the CW-07 batch.
