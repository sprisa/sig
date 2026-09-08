# Metric Query Recipes

```sh
sig metrics list --search http --since 1h
sig metrics fields --metric http_requests_total --since 1h
sig metrics query 'sum(rate(http_requests_total[5m]))' --step 1m --since 1h
```

Metric names above are examples. Discover the actual catalog and labels. The
dedicated `metrics query` command accepts PromQL, not SigNoz builder aggregation
syntax. `logs/traces aggregate` is not a metric query interface.

Understand the metric type, temporality, monotonicity, and unit before choosing
an aggregation. Monotonic counters often need rate or increase; gauges need
level statistics such as average, min, max, or latest. Histograms need
histogram-aware quantiles, not an average of arbitrary bucket values. OTel
histograms and classic Prometheus `_bucket` series are not interchangeable.

Dotted OTel metric names must not be silently rewritten with underscores. On
SigNoz versions supporting Prometheus UTF-8 names, use a quoted selector such as
`{"http.server.request.duration"}`. Confirm support on your deployed version and
use discovered names exactly. Native metric-builder queries are another route.

For native builder queries, time aggregation (within a series), space
aggregation (across label dimensions), and scalar reduction are separate choices.
Use catalog metadata and the deployed API's rules; do not infer resource versus
metric attribute context from a naming prefix alone.

Formula input limits are applied before formula evaluation. Independently
selecting each input's top 100 groups can omit a group with a high error ratio.
Use aligned grouping and suitably bounded inputs, plus an explicit result limit
and order. Larger input limits still do not guarantee completeness at higher
cardinality. Never average already-computed percentiles to obtain a population
percentile.

The v5 wire field is `order`, not dashboard/editor `orderBy`. Native request
`start`/`end` are Unix milliseconds; builder `stepInterval` and PromQL `step` are
seconds. CLI `--step` accepts durations and performs the conversion. Top-N time
series groups are selected across the whole window, so short-lived spikes can
be omitted.

`query preview --file ...` can help check native payloads, but contacts the
backend and is not a read-only sandbox guarantee. Read every validity verdict.
Do not assume current MCP/dashboard/view examples work on our v0.132.0 target:
newer tools may require newer API versions. Cost Meter is a separate metric
source on supporting versions; do not assume ordinary metric listing includes it.
