# sig

A JSON-first SigNoz CLI for humans, scripts, and AI agents. Built with Go and
[urfave/cli v3](https://cli.urfave.org/v3/).

The v1 command surface covers service-account authentication, contexts, paginated
log and span search, trace retrieval, PromQL, telemetry discovery, native JSON
queries, and machine-readable command discovery. The client targets SigNoz
v0.132.0's APIs; API compatibility with other releases must be verified separately.

## Install

Use the Go version specified in `go.mod`. From a checkout:

```sh
go install .
```

Or build a local binary:

```sh
go build -o bin/sig .
```

## Quick Start

Provide a reachable SigNoz URL and a key generated for a SigNoz service account.
`sig` does not provision networking, start tunnels, or authenticate to an external
OAuth proxy. If your endpoint has another access layer, arrange access outside
the CLI. HTTPS is required except for HTTP endpoints on localhost or a loopback IP.

```sh
# Prompts for the key without echoing it. Creates the default context.
sig auth login --url https://signoz.example.com

sig auth status
sig logs search --since 15m
sig logs search --where "service.name = 'checkout' AND severity_text = 'ERROR'" --since 1h
```

Login checks `GET /api/v1/service_accounts/me` before saving credentials. An
identity check does not prove permission to query logs. Give the account the
appropriate SigNoz role; the CLI does not grant or modify server permissions.

## Credentials

Persistent credentials use the OS keychain: macOS Keychain, Windows Credential
Manager, or Linux Secret Service. There is no plaintext fallback. Linux desktop
storage requires an available, unlocked Secret Service provider and D-Bus session.

For headless use, inject `SIGNOZ_URL` and `SIGNOZ_API_KEY` through your secret
manager or execution environment, then run commands directly without login:

```sh
# SIGNOZ_URL and SIGNOZ_API_KEY are already injected by the environment.
sig auth status
sig logs search
```

This does not write configuration or access the keychain. Environment variables
can be inherited by child processes; handle them as secrets.

`auth login --key-stdin` supports importing a key from a pipe into the keychain.
Noninteractive login requires that flag or `SIGNOZ_API_KEY`. Keys are never
accepted through a `--api-key` argument. Only explicit interactive login prompts;
query commands never prompt or open a browser.

Interactive login restores terminal settings on success or cancellation. Ctrl-C,
Ctrl-D on an empty line, SIGINT, and SIGTERM cancel the key prompt with exit code 5.

```sh
sig auth logout
```

Logout removes the selected context's stored key, not its URL, and does not revoke
the server-side key. An injected `SIGNOZ_API_KEY` still works after local logout.
Revoke keys in SigNoz when needed.

## Contexts

Most users only need `default`. A context holds a URL and an opaque keychain
reference, never the API key itself.

```sh
sig auth login --context staging --url https://staging.example.com
sig --context staging logs search --since 15m

sig config get-contexts
sig config current-context
sig config use-context staging
sig config delete-context old-instance
```

The first login selects its context (`default` unless explicitly named).
Additional named logins do not change the current context. Login without
`--context` updates the current context. To delete the current context while others
exist, select another first. Deleting the last context resets the selection to
an unconfigured `default`.

Configuration lives in `sig/config.json` under Go's `os.UserConfigDir()`:

| Platform | Typical location |
| --- | --- |
| macOS | `~/Library/Application Support/sig/config.json` |
| Linux | `$XDG_CONFIG_HOME/sig/config.json` or `~/.config/sig/config.json` |
| Windows | `%AppData%\sig\config.json` |

Override the directory with `SIG_CONFIG_DIR`. Configuration writes are atomic;
new directories and files use owner-only permissions on Unix. Context management
is intended for one writer at a time, not concurrent configuration updates.

Resolution rules:

- `--context` overrides the configured current context.
- `SIGNOZ_URL` overrides the selected context URL for that invocation.
- `SIGNOZ_API_KEY` overrides its stored credential without consulting the keychain.
- A different `SIGNOZ_URL` requires `SIGNOZ_API_KEY` too: stored credentials are
  never silently sent to a replacement endpoint.
- During login, `--url` takes precedence over `SIGNOZ_URL`, then the context URL.
- During login, `--key-stdin` takes precedence over `SIGNOZ_API_KEY`, then the prompt.

## Log Queries

```sh
sig logs search --where "severity_text = 'ERROR'" --since 30m --limit 100

sig logs search \
  --start 2026-01-01T12:00:00Z \
  --end 2026-01-01T13:00:00Z \
  --limit 500
```

Filters use native SigNoz expression syntax, not a new CLI query language. A search
sends a builder query per page to `POST /api/v5/query_range`, ordered by timestamp and ID
descending. Defaults are a 15-minute lookback, 100 records, and a 30-second HTTP
timeout. Override the timeout with `--timeout 60s`.

`--since` accepts Go durations such as `30m` or `24h`, not `1d`. `--start` and
`--since` are mutually exclusive. `--end` defaults to now; timestamps require a
timezone and are normalized to millisecond precision. Limits must be 1-10000.

### Pagination

Both log and span searches support bounded multiple-page retrieval:

```sh
sig logs search --since 1h --limit 100 --pages 3
sig traces search --since 1h --limit 50 --pages 2

# Resume with the next_page_token from the preceding JSON response.
sig logs search --page-token "$PAGE_TOKEN"
```

`--limit` is the per-page size. `--pages` defaults to 1 and is capped at 100;
`limit * pages` cannot exceed 10000 rows. The timeout applies to the entire search,
not independently to every page. Combined rows and warning content are capped at
16 MiB. A failed later page produces an error, not silently successful partial
output. Warnings stop automatic paging and are preserved.

A continuation token retains the resolved start/end, filter, page size, offset,
and an endpoint fingerprint. It is not a credential, but its filter can contain
sensitive information: treat it like query output. It cannot be combined with
`--since`, `--start`, `--end`, `--where`, `--limit`, or `--offset`. You can change
`--pages`, the timeout, or the context, provided the endpoint stays the same.

The CLI uses offset pagination with deterministic timestamp/ID ordering. SigNoz
v0.132.0's native cursor contains only a millisecond timestamp and can skip records
sharing that timestamp. The original `next_cursor` is retained for transparency,
but **resume with `next_page_token`, not the native cursor**. Tokens are unsigned
query state, not an authorization mechanism. The selected context still supplies
authentication.

Manual `--offset` is available up to 1000000 and requires explicit `--start` and
`--end`. Frozen bounds are not a database snapshot: ingestion, retention, or
migration can still change subsequent offset pages. Full pages may have a next
token even if the following page is empty; this avoids an extra probe request.

## Traces

```sh
sig traces search --where "service.name = 'checkout' AND has_error = true" --since 1h
sig traces get "$TRACE_ID"
sig traces get "$TRACE_ID" --span "$SPAN_ID" --expand "$SPAN_ID"
```

Search returns **spans**, not distinct traces. Its filter syntax and pagination
match log search. Ties are ordered by trace ID and span ID. Use field discovery
to find attributes, or filter `parent_span_id = ''` when you need root spans.

`traces get` uses the waterfall API. Trace IDs must be nonzero 32-character hex
strings; span IDs must be nonzero 16-character hex strings. The response preserves
`hasMore`, `hasMissingSpans`, and expansion state, with `meta.completeness` set to
`partial` when appropriate. Large traces can be windowed; the CLI does not claim
that a waterfall contains every span. Returned waterfall timestamps are
milliseconds; durations remain nanoseconds.

## Metrics

```sh
sig metrics list --search cpu --since 1h
sig metrics query 'sum(rate(http_requests_total[5m]))' --since 1h --step 1m
sig metrics query 'vector(1)' --since 5m --step 30s
sig metrics query 'vector(1)' --since 5m --step 30s --no-cache
```

PromQL uses the v5 time-series API. Steps must be whole seconds, with at most 11000
points per series. Response size and HTTP timeouts also apply; there is no implied
series-cardinality limit. Metric names are deployment-specific; discover them
before constructing a query. OTel names with dots can be selected using a label
selector such as `{__name__="system.cpu.utilization"}`.

Caching is controlled by SigNoz. Use `--no-cache` when comparing reproducible
results: the tested server can include different boundary points on cold versus
warm cache requests. The CLI does not silently rewrite those results or disable
the cache by default. Native JSON queries can also set `"noCache": true`.

Metric listing returns names and metadata, with a limit of 1-5000. The upstream
listing has no continuation token or completeness flag, so the CLI reports its
completeness as `unknown` rather than claiming a complete inventory.

## Discovery

```sh
sig services list --signal traces --since 1h
sig logs fields --search k8s --since 1h
sig logs values service.name --since 1h
sig traces fields
sig traces values service.name --where "has_error = true"
sig metrics fields --metric system.cpu.utilization
sig metrics values host.name --metric system.cpu.utilization
```

`fields` returns field descriptors grouped by the API, including type and context.
`values` returns typed string/number/bool value collections. `--field-context`,
`--data-type`, `--search`, and bounded limits help disambiguate names. Service
listing is resource `service.name` value discovery in the selected signal.

The API's `complete` field is retained. Discovery has no supported continuation
mechanism, and this SigNoz release rounds its start bound down to a six-hour
boundary. Do not interpret discovery as an exact-window or exhaustive inventory.

## Native Queries

"Advanced queries" means **native SigNoz v5 JSON**, not another query language.
Use this for aggregations, grouped counts, metric builder queries, formulas,
joins, or SQL that cannot be expressed through the search/PromQL flags.

```sh
# Adjust the synthetic example's millisecond start/end bounds before running.
sig query run --file examples/log-count.json
sig query preview --file examples/log-count.json
sig query run --file - < examples/metric-builder.json
```

Files or stdin must contain one JSON object of at most 1 MiB, with positive epoch
millisecond `start` and `end`, a supported `requestType` (`raw`, `scalar`,
`time_series`, or `trace`), and `compositeQuery.queries`. Other native fields pass
through without translating expressions or rounding numbers. Server validation
governs the full query schema. Streaming requests are not supported.

Query execution and metric queries preserve the v5 response inside CLI `data`,
including native statistics and warnings. Results are at `.data.data.results`.
No automatic pagination or client-side row limiting is added to native requests;
specify appropriate bounds/limits in the payload. HTTP timeout and response-size
limits still apply.

**Native SQL is not a read-only sandbox.** SigNoz and its database permissions
govern execution. `query run` is marked `server_defined` in the agent schema,
rather than incorrectly declaring every JSON query read-only.

Preview returns per-query `valid` and `error` verdicts. Exit 0 means the preview
operation succeeded, not that every query is valid. Preview may contact ClickHouse
even without `--verbose`; the flag enables additional analysis. It is not an
offline validator or a guarantee that executing the query will succeed.

## Agent Schema

```sh
sig agent schema
sig agent schema logs search
sig agent schema query run
```

The schema is a command-discovery document generated from the command tree. It
includes typed flags and defaults, required flags, positional usage, explicit
operation safety, query languages, pagination modes, and output/exit conventions.
It is not a complete JSON Schema for every upstream SigNoz payload.

Schema generation is local: it does not load contexts, access the keychain, or
query the server. It never includes invocation-specific credentials or URLs.
Adding a command without explicit safety metadata fails schema generation and
its regression test, rather than guessing safety from the command name.

## Output And Errors

Command results are JSON on stdout. Errors are JSON on stderr with a nonzero exit
code. Help is plain text, and an interactive key prompt is written to stderr.
There is no agent detection, alternate table mode, or environment-dependent
output envelope. `sig version` returns the CLI version as JSON.

A synthetic log response:

```json
{
  "data": [],
  "meta": {
    "schema_version": "1",
    "context": "default",
    "signal": "logs",
    "start": "2026-01-01T12:00:00Z",
    "end": "2026-01-01T12:15:00Z",
    "returned": 0,
    "limit": 100,
    "offset": 0,
    "pages": 1,
    "pagination": "offset",
    "next_page_token": "",
    "next_cursor": "",
    "completeness": "complete",
    "warning": null,
    "warnings": []
  }
}
```

Log rows retain the API's `{timestamp, data}` structure and JSON numeric precision.
Completeness is `complete`, `unknown`, or `more_available`, based on the returned
page and warning metadata; it is not a guarantee that ingestion itself is complete.

```json
{"error":{"code":"authentication","message":"authentication rejected by SigNoz or an access proxy; check the key and endpoint access","http_status":401}}
```

| Exit code | Meaning |
| --- | --- |
| 0 | Success |
| 2 | Invalid arguments or query bounds |
| 3 | Missing or rejected authentication, or an API redirect |
| 4 | Permission denied |
| 5 | Network failure, timeout, or cancellation |
| 6 | API, response, output, or internal failure |
| 7 | Configuration or credential-storage failure |

API redirects are never followed, even to the same host. This prevents the custom
API-key header from being forwarded to a login page or another origin. TLS
verification is always enabled. Error output deliberately excludes arbitrary
upstream bodies, which can contain sensitive information or HTML login pages.

## Development

[Task](https://taskfile.dev/) is optional; all tasks wrap standard Go commands.

```sh
task build
task test
task test:cover
task test:repeat # shuffled, race-enabled repetitions
task check       # formatting check, go vet, and race-enabled tests
task vuln        # opt-in Go vulnerability scan; requires network access
task fmt
task tidy
task install
task build VERSION=v1.0.0-rc.1
```

Tests use synthetic fixtures, local HTTP test servers, and an in-memory credential
store. They require no SigNoz instance, cluster access, or OS keychain. Bash workflow
tests require Bash and `jq`; Unix terminal regression tests require `expect` and
use synthetic input in a pseudo-terminal. These optional tests report skips when
their tools are unavailable. Native Windows terminal behavior needs separate
operator verification. Live compatibility and real keychain integration require
the opt-in checks below.

CI is configured to run formatting, vet, and race-enabled tests on Linux, macOS, and Windows. It
does not have live credentials or run the opt-in suites. Versioned module installs
report the module version; local builds report `dev` unless stamped through the
Taskfile `VERSION` variable. No version tag or release publication is automatic.

### Live Smoke Tests

With `SIGNOZ_URL` and `SIGNOZ_API_KEY` already exported by your environment:

```sh
task test:e2e
# Or: bash scripts/e2e.sh
```

This opt-in Bash script runs the CLI with `go run .`. It requires Go, `jq`, an
accessible OS keychain, and a reachable SigNoz API. It is not part of `task test`
or `task check` and does not configure networking or external proxy authentication.

It checks login, keychain and environment authentication, bounded recent logs,
native error filtering, nonempty fixed-window retrieval, timestamp bounds and
ordering, invalid-key rejection, and local logout. The negative test accounts for
`go run` wrapping the application's exit code. All server operations are read-only.

For comparison with known logs in the UI, optionally export:

| Variable | Purpose |
| --- | --- |
| `SIG_E2E_START` and `SIG_E2E_END` | An RFC3339 comparison window; provide both or neither. Defaults to the last hour. |
| `SIG_E2E_WHERE` | Native filter for the comparison query, such as a service filter. |
| `SIG_E2E_EXPECT_ID` | A known log ID that must appear among the five newest matching records. |
| `SIG_E2E_TRACE_ID` | Optional known trace ID for `task test:api` waterfall verification when the comparison window has no spans. |

The comparison query must return at least one record without a warning. The error
filter query may legitimately return no records. Without an expected ID, the
script reports that UI content/ID comparison remains manual.

The script overrides `SIG_CONFIG_DIR` with a private temporary directory and
creates an isolated keychain entry. Your existing contexts and credentials are
not changed. On exit it removes its keychain entry and temporary files. If
credential cleanup fails, it retains the temporary configuration and reports
its location so logout can be retried. Responses are temporarily stored with
owner-only permissions outside the repository; telemetry, identities, URLs, and
keys are not printed. Do not run the script with shell tracing enabled.

For independent comparisons between the CLI and direct HTTP requests, use the
same exported credentials and fixed comparison window:

```sh
task test:api
```

This builds a temporary CLI binary and compares identity, log/span search and
pagination, trace waterfalls when data is available, metric listing and PromQL,
discovery, native queries, preview, and error statuses. JSON numeric precision is
preserved. Discovery value collections are compared without assuming order, and
PromQL comparisons bypass the server cache. It uses environment credentials only,
does not access the keychain, and does not print response contents. The `e2e` Go
build tag keeps these tests out of ordinary test runs, and caching is disabled for
the live task. Choose a stable historical window so separate requests see the same
data; the unfiltered or `SIG_E2E_WHERE` comparisons must not be empty.

Never commit deployment URLs, credentials, real telemetry, or captured production
responses. Examples and fixtures must use placeholder endpoints and synthetic data.
