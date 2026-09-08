# sig

A JSON-first SigNoz CLI for humans, scripts, and AI agents. Built with Go and
[urfave/cli v3](https://cli.urfave.org/v3/).

The initial implementation supports service-account authentication, optional named
contexts, and bounded log searches. Traces, metrics, field discovery, and advanced
query commands are not implemented yet. The API client targets SigNoz v0.132.0's
service-account and v5 query contracts; other releases are not yet verified.

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
sends one builder query to `POST /api/v5/query_range`, ordered by timestamp and ID
descending. Defaults are a 15-minute lookback, 100 records, and a 30-second HTTP
timeout. Override the timeout with `--timeout 60s`.

`--since` accepts Go durations such as `30m` or `24h`, not `1d`. `--start` and
`--since` are mutually exclusive. `--end` defaults to now; timestamps require a
timezone and are normalized to millisecond precision. Limits must be 1-10000.

Search retrieves one page only. It does not follow cursors or retry requests.
Upstream cursors are preserved as metadata for inspection; cursor input and
automatic pagination are not yet supported. A full page without a cursor is
marked `unknown`, not assumed complete. Warnings are preserved. Responses larger
than 16 MiB are rejected; narrow the query or reduce the limit.

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
    "next_cursor": "",
    "completeness": "complete",
    "warning": null
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
```

Tests use synthetic fixtures, local HTTP test servers, and an in-memory credential
store. They require no SigNoz instance, cluster access, or OS keychain. Bash workflow
tests require Bash and `jq`; Unix terminal regression tests require `expect` and
use synthetic input in a pseudo-terminal. These optional tests report skips when
their tools are unavailable. Native Windows terminal behavior needs separate
operator verification. Live compatibility and real keychain integration require
the opt-in checks below.

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

This builds a temporary CLI binary and compares service-account identity, complete
log rows (preserving JSON numeric precision), cursors, warnings, limits, filters,
and authentication/query error statuses. It uses environment credentials only,
does not access the keychain, and does not print response contents. The `e2e` Go
build tag keeps these tests out of ordinary test runs, and caching is disabled for
the live task. Choose a stable historical window so separate requests see the same
data; the unfiltered or `SIG_E2E_WHERE` comparisons must not be empty.

Never commit deployment URLs, credentials, real telemetry, or captured production
responses. Examples and fixtures must use placeholder endpoints and synthetic data.
