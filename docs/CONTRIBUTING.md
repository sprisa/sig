# Contributing

[← README](../README.md) · [Query contracts](QUERYING.md) · [Security](SECURITY.md)

Thanks for helping improve sig. Keep changes focused, preserve the JSON interface,
and use synthetic examples that another contributor can reproduce.

## Get started

Clone the repository, then run these commands from the checkout:

```sh
git clone https://github.com/sprisa/sig.git
cd sig
go build -o bin/sig .
go test ./...
```

Use the Go version in [`go.mod`](../go.mod). [Task](https://taskfile.dev/) is optional;
the [`Taskfile`](../Taskfile.yml) wraps standard Go commands.

```sh
task build
task check
```

Ordinary tests use synthetic fixtures, local HTTP servers, and in-memory
credential stores. They need no live SigNoz instance, cluster, or OS keychain.
Bash workflow tests need Bash and `jq`; Unix terminal regression tests use
`expect` and synthetic input in a pseudo-terminal. These optional tests report
skips when their tools are unavailable. Native Windows console behavior needs
separate operator verification.

## Common tasks

| Task | Purpose |
| --- | --- |
| `task build` | Build `bin/sig` |
| `task install` | Install the checkout into the Go binary directory |
| `task fmt` | Format Go source |
| `task tidy` | Tidy module dependencies |
| `task test` | Run synthetic tests |
| `task test:cover` | Report test coverage |
| `task test:race` | Run tests with the race detector |
| `task test:repeat` | Repeat race-enabled tests with randomized order |
| `task test:proxy` | Compare a compiled CLI and direct API client behind a synthetic auth proxy |
| `task check` | Check formatting, vet, race tests, and the synthetic proxy integration |
| `task vuln` | Opt-in reachable Go vulnerability scan; requires network access |

Before submitting a change:

```sh
task check
go vet -tags=e2e ./...
```

For authentication, cancellation, pagination, or concurrent state changes, also
run `task test:repeat`. Add meaningful regression coverage for changed contracts;
avoid tests that only mirror internal implementation details.

CI runs formatting, vet, race tests, builds, and the synthetic proxy integration
on Linux, macOS, and Windows. It has no live credentials and does not run live
SigNoz scenarios. The proxy test uses the `e2e` build tag but supplies its own local
server and synthetic credentials.

## Repository structure

| Path | Responsibility |
| --- | --- |
| `cmd/` | Command definitions, flags, connection resolution, page tokens, and JSON output |
| `cmd/recipes/` | Canonical query guides embedded in the binary |
| `signoz/` | API transport, typed requests, validation, result views, and page collection |
| `config/` | Configuration snapshots, credential references, serialized mutations, and cleanup recovery |
| `scripts/` | Live-test harness, smoke workflow, and their synthetic tests |
| `tests/` | Synthetic performance workloads and resource measurements |
| `examples/` | Native query examples with placeholder data and bounds |
| `skills/sig/` | Portable agent workflow and routing to the query recipes |
| `docs/` | User reference, contributor guidance, and security model |

## Design boundaries

sig is built with Go and [urfave/cli v3](https://cli.urfave.org/v3/). Keep packages
at the repository root and keep behavior in its canonical owner:

- `signoz` owns request invariants and fixed API envelopes. It retains raw JSON
  for arbitrary telemetry and native results, preserving unknown fields and
  exact numbers. API decoding and CLI output use `encoding/json/v2` streaming APIs.
- `cmd` binds flags, resolves a named connection, encodes continuation tokens,
  and renders results. The API client does not read environment variables.
- Command effects and other discovery metadata are attached with `operation()`
  alongside the command definition, not maintained in a parallel path registry.
  A command without explicit policy metadata fails schema generation and its test.
- Configuration persistence and token serialization keep their existing formats.
  Changes to those contracts need compatibility tests, not an incidental parser swap.
- Dedicated queries remain bounded and explicit. Native `query run` preserves
  the caller's payload; it does not insert limits or rewrite expressions.
- Networking and proxy setup belong to the operator. Do not add automatic
  tunnels, credential provisioning, or agent-environment detection.

When adding commands, update their generated schema metadata, user-facing entry
points, and the relevant recipe. Keep detailed SigNoz semantics in recipes rather
than copying them into the README and skill. Keep synthetic request assertions
independent of production wire structs so contract mistakes do not verify themselves.

### Configuration mutations

`config.Store` owns serialized mutations and cleanup recovery. `config.Save` is
the low-level snapshot writer, not a replacement for the store's mutation flow.

Mutations take an OS-backed `config.lock`, reload the latest snapshot, and wait
at most five seconds for a competing writer. Reads do not take that lock.
Configuration writes are atomic; on Unix, new files and directories use
owner-only permissions.

`pending_credentials` is a cleanup journal of opaque keychain references.
Replacement records a new reference before storing a key and retains detached
references until deletion succeeds. A later mutation retries cleanup after an
interrupted operation or keychain failure. This is a recoverable two-store
protocol, not an atomic transaction across the filesystem and keychain.

A cleanup error can mean the configuration change already succeeded. Tests must
cover that state and retry recovery. Older configurations without the optional
journal remain readable; downgrading to a version that does not understand a
pending journal can break recovery.

### Input and output

Input handling distinguishes finite files/in-memory buffers from interruptible
streams. Injected `io.PipeReader` streams close on cancellation; OS streams use
deadlines or platform cancellation. Unsupported readers fail explicitly rather
than starting a background read that could be abandoned. Regular-file reads are
bounded but cannot guarantee interruption of a blocked filesystem call; console
interruption depends on the OS backend.

HTTP responses are decoded from bounded readers. Returned raw values own their
bytes. All requested search pages finish before output starts; output streams
without a whole-collection encoding buffer. Neither the byte cap nor streaming
eliminates allocation or bounds total heap usage. See the
[output contract](QUERYING.md#output-and-errors).

## Performance checks

```sh
task perf
task perf:profile
task perf:resources
```

These use synthetic workloads. [Performance documentation](../tests/README.md)
covers scenarios, task options, CPU/allocation profiles, and the distinction
between allocated bytes, retained heap, and peak RSS. Profiles go under ignored
`bin/perf/`. Keep performance tests in `tests/` rather than scattering benchmarks
into application packages, and compare equivalent toolchains and runtime settings.

## Live verification

Live checks are opt-in. The operator must supply a reachable `SIGNOZ_URL` and
`SIGNOZ_API_KEY`; tests do not create networking or seed telemetry. Optional
`SIGNOZ_CUSTOM_HEADERS` applies to both CLI and direct API requests.

### Keychain smoke test

```sh
task test:e2e
# Equivalent: bash scripts/e2e.sh
```

This uses `go run .` and requires Go, Bash, `jq`, an accessible OS keychain, and
the live API. It checks login, environment and keychain authentication, bounded
logs, error filtering, nonempty fixed-window retrieval, timestamp bounds and
ordering, invalid-key rejection, and local logout. All server operations are
read-only. The negative check accounts for `go run` wrapping application exit codes.

The script sets an isolated temporary `SIG_CONFIG_DIR` and creates a temporary
keychain reference. Existing contexts and credentials are not changed. It removes
temporary state on exit; if credential cleanup fails, it retains the temporary
configuration and reports its location for retry. Temporary responses use
owner-only permissions outside the repository. Do not enable shell tracing.

### Direct API parity

```sh
task test:api
```

This builds a temporary CLI and compares it with independently constructed HTTP
requests: identity, log/span search and pagination, scalar/time-series
aggregations, trace waterfalls, metrics, discovery, native queries, preview,
and error handling. It uses environment credentials without keychain access.
Response contents are suppressed in failure reports.

Comparisons preserve JSON numeric precision. Unordered discovery values and
aggregation series are compared without relying on array order. Execution
metadata can vary per request; comparisons focus on query results and warnings.
PromQL comparisons bypass the server cache. The pure aggregation comparator has
synthetic coverage for changed/duplicate series, malformed shapes, null/missing
collections, and input immutability.

| Variable | Purpose |
| --- | --- |
| `SIG_E2E_START`, `SIG_E2E_END` | RFC3339 comparison bounds; provide both or neither. Defaults to the last hour. |
| `SIG_E2E_WHERE` | Native filter for known comparison logs |
| `SIG_E2E_EXPECT_ID` | For smoke tests, a known log ID expected among the five newest matching records |
| `SIG_E2E_TRACE_ID` | Optional trace ID for parity waterfall checks when the comparison window has no spans |

Choose a stable historical window. Known-log comparisons must return data; the
smoke comparison also requires no warning. An error-filter query may legitimately
be empty. Without `SIG_E2E_EXPECT_ID`, the smoke test reports UI/ID comparison as
manual. Trace or metric fixture absence is an explicit skip, not positive coverage.
Missing required fields are failures, not equal missing values.

Live scenarios can be selected with Go's `-run` filter. Do not run all `e2e`-tagged
tests blindly on a machine without live configuration. `task test:proxy` selects
only the credential-free synthetic scenario and is safe for ordinary CI.

## Versioning and releases

[`version.txt`](../version.txt) is the source of truth. Use a bare semantic version
such as `0.0.1` or `0.1.0-rc.1`, without a `v` prefix. Task builds stamp the binary
with that version; Git tags use `v<VERSION>`. Direct `go build` defaults to `dev`,
and versioned `go install` reports the module version.

Releases run locally. Install GoReleaser v2, Node.js/npm, `jq`, and `gh`, then
authenticate with `npm login` and `gh auth login`. npm access must allow public
packages under `@sprisa`.

After committing code changes, the workflow is:

1. Bump `version.txt`.
2. Run `task publish`.

`publish` synchronizes the version in `npmreleaser.json`, commits just those two
release metadata files when changed, and creates an annotated tag. It then runs
these steps in order:

1. GoReleaser builds native archives and checksums without publishing them.
2. npmreleaser builds the platform packages; npm publishes them before the wrapper.
3. Git pushes the current branch and tag; `gh` creates the GitHub release with
   generated notes, archives, and checksums.

Prereleases go to npm's `next` tag and are marked as GitHub prereleases. Stable
versions go to `latest`. Publishing changes remote state; the build and dry-run
tasks below do not:

```sh
task release:check         # Validate configuration
task build:go              # Cross-build snapshot binaries
task release:snapshot      # Build snapshot archives and checksums
task build:npm             # Build npm packages, syncing the version from version.txt
task release:npm:dry-run   # Build and inspect npm packages without uploading
```

Both packagers target Linux, macOS, and Windows on amd64 and arm64. GoReleaser
outputs live in `dist/goreleaser/`; npm packages live in `dist/npm/`. The npm entry
package is `@sprisa/sig`, with a `sig` command and platform-specific optional
dependencies. Keep optional dependencies enabled when installing it.

The Taskfile normalizes npmreleaser 0.0.5's generated scoped-package `bin`/`files`
metadata and launcher permissions before packaging. No custom release script or
release CI workflow is needed. The individual `release:npm` and `release:github` tasks are also available
for manual operation. Publishing across registries is not atomic: if a step
fails, inspect which packages/releases exist before retrying; npm versions cannot
be overwritten.

Test API compatibility against the intended SigNoz version; a successful
cross-build alone does not establish native terminal, keychain, or backend
compatibility.

For issues and pull requests, use synthetic data and placeholder URLs. Follow
the [security guidance](SECURITY.md) when handling credentials or query output.
