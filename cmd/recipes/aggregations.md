# Aggregation Recipes

Dedicated commands generate a single bounded v5 builder query. One aggregation
is required; the default is a scalar value per group over the entire window.

```sh
sig logs aggregate --aggregation 'count()' --since 1h
sig logs aggregate --aggregation 'count()' --group-by resource.service.name --where "severity_text = 'ERROR'" --limit 10 --since 1h
sig logs aggregate --aggregation 'count()' --group-by severity_text --step 1m --since 1h
sig traces aggregate --aggregation 'p99(duration_nano)' --group-by resource.service.name --step 1m --since 1h
sig traces aggregate --aggregation 'count_distinct(trace_id)' --since 1h
```

`count()` and `rate()` take no field. `count_distinct`, `avg`, `sum`, `min`, `max`,
`p50`, `p75`, `p90`, `p95`, and `p99` take one dotted field name. Quote the entire
expression for the shell. Numeric aggregates require a suitable numeric field;
the backend validates field existence and types. Counts and rates are over log
records or spans, not automatically user requests. `rate()` reports events per
second; it is not a percentage or a derivative of a gauge.

`--group-by` is repeatable or comma-separated (at most 16 fields, each at most
1024 bytes). Bare names are resolved by SigNoz. Explicit `resource.`, `attribute.`,
`scope.`, `log.`, `span.`,
or `body.` prefixes set field context. For example, `resource.service.name` sets
name `service.name` and context `resource`; sig does not guess context from a
`service.`, `k8s.`, or `http.` naming convention. Discover fields first.

`--order asc|desc` sorts by the aggregation, default descending. `--limit` bounds
groups from 1 to 10000, default 100, with no aggregation pagination.
`--step 1m` enables time series with 60-second buckets. Steps must be positive
whole seconds and permit at most 11000 points per series. Group count and bucket
count multiply; start small. Responses still have a 16 MiB byte cap, not a heap
or server-query-cost guarantee. `--no-cache` explicitly bypasses server caching.

The combined `--aggregation` and `--where` input text is limited to 1 MiB of UTF-8
bytes before JSON escaping. This is not a 1 MiB encoded-request limit: escaping,
group fields, repeated expressions, and envelope overhead can make the wire
request larger. Native query-file limits are a separate input contract.

For grouped time series, top-N selection is across the entire requested window,
not independently per bucket. A brief spike can be absent even when dominant in
one bucket. Narrow the window or filters, or deliberately increase the limit.
`meta.completeness` remains `unknown`; inspect `data.warning` and do not equate
successful execution with exhaustive results or complete ingestion.

For multiple aggregations, formulas, complex field syntax, or custom ordering,
use `query run` with native v5 JSON. Generated query flags do not rewrite a
native payload. See the metrics recipe for formula input-limit pitfalls.
