<div align="center">
  <h1>sig</h1>
  <p><strong>A SigNoz CLI built for AI agents.</strong></p>
  <p>Query logs, traces, and metrics with structured JSON output.</p>
  <p>
    <a href="#quick-start"><strong>Quick start</strong></a> ·
    <a href="#everyday-queries"><strong>Usage</strong></a> ·
    <a href="#ai-agents"><strong>AI agents</strong></a> ·
    <a href="#learn-more"><strong>Documentation</strong></a>
  </p>
  <p>
    <a href="https://github.com/sprisa/sig/actions/workflows/ci.yml"><img src="https://github.com/sprisa/sig/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
    <a href="LICENSE"><img src="https://img.shields.io/badge/license-MPL%202.0-blue" alt="License: MPL 2.0"></a>
  </p>
</div>

**sig** is an independent CLI for [SigNoz](https://signoz.io). Find the logs behind
an error, inspect a slow trace, or query a metric without leaving your workflow.
It connects directly to your SigNoz API and returns JSON that works equally well
in a shell pipeline or an agent's tool call.

- **Investigate across signals.** Search logs and spans, fetch trace waterfalls,
  calculate statistics, and run PromQL.
- **Discover as you go.** Find field names, observed values, and available metrics
  before building a query.
- **Bring your agent.** Machine-readable commands and offline query recipes are
  built in. No MCP server is needed.

## Install

### CLI

```sh
go install github.com/sprisa/sig@latest
```

Requires [Go](https://go.dev/dl/) 1.27.1 or newer. Make sure your Go binary directory
is on `PATH`, then check the installation:

```sh
sig version
```

[Installation help](docs/CONFIGURATION.md#installation) ·
[Build from source](docs/CONTRIBUTING.md#get-started)

### Agent skill

Install the skill for your AI agent with [skills](https://skills.sh/docs/cli)
(requires Node.js and `npx`):

```sh
npx skills add sprisa/sig
```

The skill includes CLI installation instructions, so you can start here even if
you haven't installed `sig` yet. It also teaches the investigation workflow and
when to load the bundled query recipes.

## Quick start

You'll need your SigNoz URL and a
[service-account API key](https://signoz.io/docs/manage/administrator-guide/iam/service-accounts/).
Replace the example URL with your instance:

```sh
sig auth login --url https://signoz.example.com
sig auth status
sig logs search --since 15m
```

Login prompts for the key and saves it in your OS keychain. Your first login
creates the `default` context, so subsequent commands already know where to connect.

Using CI, a headless machine, or an existing MCP configuration? You can supply
`SIGNOZ_URL`, `SIGNOZ_API_KEY`, and optional `SIGNOZ_CUSTOM_HEADERS` through the
environment instead. See [authentication and configuration](docs/CONFIGURATION.md).

## Everyday queries

### Find the logs you need

```sh
sig logs search --where "body CONTAINS 'timeout'" --since 1h --limit 20
```

Not sure what to filter on? Discover the fields and values in your instance:

```sh
sig logs fields --search service --since 1h
sig logs values severity_text --field-context log --since 1h
```

[Log query recipes →](cmd/recipes/logs.md)

### Get the big picture

Count matching records or group them into a trend instead of downloading every row:

```sh
sig logs aggregate --aggregation 'count()' --since 1h
sig logs aggregate --aggregation 'count()' --group-by severity_text --step 1m --since 1h
```

[Aggregation recipes →](cmd/recipes/aggregations.md)

### Follow a trace

Search returns spans. Use a trace ID from the results to retrieve its waterfall:

```sh
sig traces search --where "has_error = true" --since 1h --limit 20
sig traces get "$TRACE_ID"
```

[Trace query recipes →](cmd/recipes/traces.md)

### Query your metrics

Discover a metric name, then query it with PromQL. Replace the example expression
with one that matches your instrumentation:

```sh
sig metrics list --search http --since 1h
sig metrics query 'sum(rate(http_requests_total[5m]))' --since 1h --step 1m
```

[Metric query recipes →](cmd/recipes/metrics.md)

<details>
<summary><strong>Need a fixed window, more results, or a native query?</strong></summary>

Use explicit bounds for an incident window (replace these example times):

```sh
sig logs search --start 2026-01-01T12:00:00Z --end 2026-01-01T13:00:00Z
```

Fetch a few pages or resume with `meta.next_page_token` from a previous response:

```sh
sig logs search --since 1h --limit 100 --pages 3
sig logs search --page-token "$PAGE_TOKEN"
```

For a native SigNoz v5 query you've saved to a file:

```sh
sig query preview --file query.json
sig query run --file query.json
```

[Query behavior, limits, and native examples →](docs/QUERYING.md)

</details>

## JSON that fits your workflow

Successful commands write `{data, meta?}` to stdout. Errors write `{error}` to
stderr and return a nonzero exit code. Help is plain text.

For example, use `jq` to inspect the metadata for a search:

```sh
sig logs search --since 1h --limit 20 | jq '.meta'
```

Check warnings and completeness before treating a result as exhaustive.
[Output shapes, pagination, and exit codes](docs/QUERYING.md#output-and-errors)
explain what each command returns.

## AI agents

Use the [skill install command](#agent-skill) above, or add
[`skills/sig/SKILL.md`](skills/sig/SKILL.md) through your agent's usual skill
mechanism. The skill points to focused recipes instead of loading a full manual
on every request.

Agents can also discover the installed interface directly:

```sh
sig agent schema logs aggregate
sig agent recipes
sig agent recipes workflow
```

Schema and recipes work offline. Everyone gets the same JSON interface; no
agent-mode flag or environment detection is required.

## Learn more

| Looking for… | Start here |
| --- | --- |
| Headless auth, multiple contexts, or proxy headers | [Configuration](docs/CONFIGURATION.md) |
| Time windows, pagination, output contracts, or native JSON queries | [Querying](docs/QUERYING.md) |
| Field contexts, units, and investigation techniques | [Query recipes](cmd/recipes/workflow.md) |
| Credential handling and the trust model | [Security](docs/SECURITY.md) |
| Building, testing, architecture, or contributing | [Contributing](docs/CONTRIBUTING.md) |

Run `sig --help` or `sig <command> --help` for command details.

**Compatibility:** sig targets SigNoz v0.132.0's APIs. Compatibility with other
versions needs verification. See [compatibility notes](docs/QUERYING.md#compatibility).

Licensed under [MPL 2.0](LICENSE).
