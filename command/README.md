# TypeScript CLI feasibility scaffold

Use Bun `1.4.3+c6da4a4d3` and run these commands from `command/`.

```sh
bun install --frozen-lockfile
bun run test:local
```

This runs strict type checking and the local tests, then builds `dist/assign` and `dist/assign-fixture`. Local builds and prepared tests require no signing certificate or notarization. On macOS the App Sandbox replay signs its native helpers ad hoc with `codesign --sign -` to attach the sandbox entitlements. Follow `.codex/qualification-evidence/cli-2043-20261111/README.md` from the Assign workspace for that probe. Publication runs the platform signing workflow.

The ordinary build compiles `src/entry.ts` to `dist/assign` (`assign.exe` on Windows). `ASSIGN_BUILD_VERSION`, `ASSIGN_BUILD_COMMIT` and `ASSIGN_BUILD_DATE` are optional build-time inputs. `version` skips Commander. Other scaffold commands load the parser and settings lazily. The existing `terminal/` package owns React, Ink and the renderer. Native feasibility embeds the built renderer as a file asset and loads it only in renderer mode.

The configuration scaffold uses the existing Go v1 settings file and its host precedence. It leaves credentials in the OS vault. Roll back by running the retained Go binary against the same settings file. The scaffold and Go both reject files outside the accepted v1 shape.

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
