# Querying

[← README](../README.md) · [Configuration](CONFIGURATION.md) · [Query recipes](../cmd/recipes/workflow.md)

Use `sig <command> --help` for flags or `sig agent schema COMMAND SUBCOMMAND` for
the installed machine-readable contract. This guide explains behavior shared
across commands; the recipes cover field syntax and investigation techniques.

## Command guide

| Task | Commands | Read more |
| --- | --- | --- |
| Inspect log records | `logs search` | [Logs](../cmd/recipes/logs.md) |
| Inspect spans and waterfalls | `traces search`, `traces get` | [Traces](../cmd/recipes/traces.md) |
| Calculate counts, percentiles, and grouped trends | `logs aggregate`, `traces aggregate` | [Aggregations](../cmd/recipes/aggregations.md) |
| Discover metrics or evaluate PromQL | `metrics list`, `metrics query` | [Metrics](../cmd/recipes/metrics.md) |
| Discover fields and values | `logs/traces/metrics fields`, `logs/traces/metrics values`, `services list` | [Discovery](#discovery) |
| Execute or preview native v5 JSON | `query run`, `query preview` | [Native queries](#native-queries) |
| Discover commands and offline guides | `agent schema`, `agent recipes` | [Agent interface](#agent-interface) |

## Time windows

The default window is the last 15 minutes, ending now. Use Go durations such as
`30m` or `24h` for `--since` (not `1d`), or RFC3339 timestamps for explicit bounds:

```sh
sig logs search --since 30m --limit 100
sig logs search --start 2026-01-01T12:00:00Z --end 2026-01-01T13:00:00Z --limit 500
```

`--start` and `--since` are mutually exclusive. `--end` can be combined with
either. Explicit timestamps need a timezone and are normalized to millisecond
precision. The default HTTP timeout is 30 seconds; override it with, for example,
`sig --timeout 60s logs search --since 1h`.

## Search and pagination

Log and span searches default to 100 records and one page. Request more pages or
resume from `meta.next_page_token`:

```sh
sig logs search --since 1h --limit 100 --pages 3
sig traces search --since 1h --limit 50 --pages 2
sig logs search --page-token "$PAGE_TOKEN"
```

- `--limit` is per page, from 1 to 10000. `--pages` is from 1 to 100.
  Their product cannot exceed 10000 rows.
- The timeout covers the whole search, not each page independently. Combined
  rows and warning content are capped at 16 MiB.
- Warnings stop automatic paging and remain in the result. A failed later page
  produces an error rather than a successful partial collection.
- A page token cannot be combined with `--since`, `--start`, `--end`, `--where`,
  `--limit`, or `--offset`. You can change `--pages`, the timeout, or the context
  provided it still resolves to the same endpoint.
- Manual `--offset` supports up to 1000000 and requires explicit start/end bounds.

### How continuation works

Each page uses a v5 builder query with a fixed window and an offset. Logs sort by
timestamp and ID descending; spans sort by timestamp, trace ID, and span ID.
The token retains the bounds, filter, page size, offset, and endpoint fingerprint.

Use **`next_page_token`, not `next_cursor`**. On the targeted SigNoz version, the
native cursor contains only a millisecond timestamp and can skip timestamp ties.
It is retained in output for transparency.

Frozen bounds are not a database snapshot: ingestion and retention can still
shift offset pages. A full page may have a continuation token even when the next
page is empty; sig avoids an extra probe request just to establish exhaustion.
See [token handling](SECURITY.md#telemetry-and-continuation-tokens) for privacy details.

## Trace waterfalls

`traces search` returns spans, not distinct traces. Fetch a waterfall with an ID
from those results, and optionally select or expand a span:

```sh
sig traces get "$TRACE_ID"
sig traces get "$TRACE_ID" --span "$SPAN_ID" --expand "$SPAN_ID"
```

Repeat `--expand` to expand more than one span.

Trace IDs are nonzero 32-character hexadecimal strings; span IDs are nonzero
16-character hexadecimal strings. Waterfalls can be windowed or incomplete.
Inspect `hasMore`, `hasMissingSpans`, and `meta.completeness`; success does not
mean every span is present. Waterfall timestamps are milliseconds and durations
remain nanoseconds. See the [trace recipe](../cmd/recipes/traces.md) for correlation
and aggregation semantics.

## Discovery

```sh
sig services list --signal traces --since 1h
sig logs fields --search k8s --since 1h
sig logs values service.name --since 1h
sig traces values service.name --where "has_error = true"
sig metrics fields --metric system.cpu.utilization
sig metrics values host.name --metric system.cpu.utilization
```

The metric and attribute names above are examples. `fields` returns descriptors
including type and context; `values` returns string, number, and boolean
collections. Use `--field-context`, `--data-type`, `--search`, and limits to narrow
results. Service listing discovers resource `service.name` values in one signal,
not an APM health inventory.

Discovery preserves the API's `complete` field but has no supported continuation
mechanism. The targeted SigNoz release rounds its start bound down to a six-hour
boundary, so results are not an exact-window or exhaustive inventory.

Metric listing supports 1-5000 names and metadata records. Its API supplies no
continuation token or completeness flag, so sig reports `unknown` completeness.

## Metrics and caching

PromQL queries use the v5 time-series API. `--step` must be a whole-second duration,
with at most 11000 points per series. There is no implied series-cardinality limit;
HTTP timeout and response-size bounds still apply.

SigNoz controls caching. Use `--no-cache` when comparing reproducible results: the
tested server can return different boundary points on cold versus warm requests.
sig preserves those results and leaves caching enabled by default. Native JSON
queries can also set `"noCache": true`.

See the [metrics recipe](../cmd/recipes/metrics.md) for metric types and formula
pitfalls. Dotted OTel names can also be selected with a PromQL label selector such
as `{__name__="system.cpu.utilization"}`; use names observed in your instance.

## Native queries

For multi-query requests, formulas, metric-builder queries, or SQL beyond the
dedicated commands, supply native SigNoz v5 JSON. Start from the repository's
[log-count example](../examples/log-count.json) or
[metric-builder example](../examples/metric-builder.json), adjusting names,
millisecond time bounds, limits, and ordering for your task:

```sh
# From a checkout, after editing the synthetic example's bounds and fields:
sig query preview --file examples/log-count.json
sig query run --file examples/log-count.json
sig query run --file - < examples/metric-builder.json
```

Files or stdin must contain one JSON object of at most 1 MiB, with positive
millisecond `start`/`end`, `requestType` (`raw`, `scalar`, `time_series`, or `trace`),
and `compositeQuery.queries`. Streaming API requests are not supported. The backend
validates the full schema; other native fields pass through without translating
expressions or rounding numbers.

The v5 response is preserved inside CLI `data`; results are at `.data.data.results`.
Native requests get no automatic pagination or client-side row limiting. Specify
the appropriate limits and ordering in the payload. Timeout and response-size
bounds still apply.

Preview returns per-query `valid` and `error` verdicts. Exit 0 means the preview
operation succeeded, not that every query is valid. `--verbose` requests additional
analysis; even nonverbose preview can contact ClickHouse. Native execution is
governed by backend permissions, not a CLI read-only sandbox. See
[native query security](SECURITY.md#native-queries-and-agent-use).

## Output and errors

Success is newline-terminated JSON on stdout: `{data, meta?}`. Errors are JSON on
stderr: `{error: {code, message, http_status?}}`, with a nonzero exit code. Help is
plain text. The interface is the same for people, scripts, and agents.

Log rows retain `{timestamp, data}`. Search metadata includes counts, bounds,
pagination state, warnings, and `complete`, `unknown`, or `more_available`
completeness. Trace waterfalls use `complete` or `partial`. Aggregation completeness
remains `unknown`. These describe the response, not the completeness of ingestion.

Warning locations differ by command. The
[workflow recipe's result-contract table](../cmd/recipes/workflow.md#result-contracts)
is the canonical reference.

<details>
<summary><strong>Example: an empty log search</strong></summary>

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

</details>

| Exit code | Meaning |
| --- | --- |
| 0 | Success |
| 2 | Invalid arguments, query bounds, or custom-header configuration |
| 3 | Missing/rejected authentication or an API redirect |
| 4 | Permission denied |
| 5 | Network failure, timeout, or cancellation |
| 6 | API, response, output, or internal failure |
| 7 | Configuration or credential-storage failure |

Unknown telemetry fields and numeric representations are preserved. Whitespace,
string escaping, and object-key order are not stable output contracts. API and
native-input parsing rejects invalid UTF-8 and duplicate object names; interpreted
field names are case-sensitive.

All requested search pages finish before output starts. An output write failure
can still leave partial stdout, so check the process exit status before consuming
results. The 16 MiB response/content limits are not heap or backend scan-cost limits.

## Agent interface

`agent schema [COMMAND [SUBCOMMAND]]` describes flags, defaults, arguments, effects,
query languages, pagination modes, and output conventions. It is command discovery,
not a full JSON Schema for all upstream data. `agent recipes [TOPIC]` lists guides
or returns one as Markdown in `data.content`.

Both work offline without loading credentials. Use the
[portable skill](../skills/sig/SKILL.md) to teach your agent which guide to read.

## Compatibility

The client targets SigNoz v0.132.0's identity, v5 query/preview, v4 waterfall,
field-discovery, and metric-listing APIs. Other releases need independent
verification; newer MCP examples may use routes unavailable on that target.
Persisted configuration and continuation tokens retain their existing
serialization rather than adopting the stricter API JSON parser.

## Troubleshooting

| Symptom | Next step |
| --- | --- |
| An empty result | Check the window, signal, field names, and observed values before drawing a conclusion |
| An invalid filter | Use `fields` and `values` to confirm spelling, type, and context; consult the signal's recipe |
| A timeout | Narrow the window or grouping, or explicitly increase `--timeout` |
| An oversized response | Reduce page/group limits or narrow the query; increase time-series step when appropriate |
| A full page with a continuation token | Resume using `next_page_token`; the next page can legitimately be empty |
| Different grouped time-series ordering | Series order may vary; compare labels and values rather than array position |

For connection and credential problems, see [Configuration](CONFIGURATION.md#troubleshooting).
