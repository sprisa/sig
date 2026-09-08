# Trace Query Recipes

`traces search` returns spans, not distinct traces. `count()` over traces counts
spans; use `count_distinct(trace_id)` when the question asks for trace count.

```sh
sig traces fields --field-context span --since 1h
sig traces fields --field-context resource --since 1h
sig traces search --where "has_error = true AND duration_nano >= 500000000" --since 1h --limit 20
sig traces aggregate --aggregation 'count_distinct(trace_id)' --since 1h
sig traces aggregate --aggregation 'p99(duration_nano)' --group-by resource.service.name --since 1h
```

Use canonical fields such as `trace_id`, `span_id`, `parent_span_id`, `name`,
`duration_nano`, and `has_error`. Duration is in nanoseconds: 500 ms is 500000000
ns. A percentile of span durations is not automatically an end-to-end request
latency percentile. Filter to the relevant operation/span population first.

Discover a trace ID from spans or a log record, then fetch it:

```sh
sig traces get 4bf92f3577b34da6a3ce929d0e0e4736
sig logs search --where "trace_id = '4bf92f3577b34da6a3ce929d0e0e4736'" --since 1h --limit 20
```

The ID above is illustrative, not a fixture in your tenant. Missing trace-linked
logs can mean absent correlation fields or a wrong window, not no activity.

`traces get` is a waterfall view. Preserve `hasMore` and `hasMissingSpans`; a
successful request can still be partial. `--span` selects a span and repeatable
`--expand` expands particular span windows. Neither flag proves that every span
has arrived or remains retained.

For reproducible searches use explicit RFC3339 bounds and continuation tokens.
Ordering breaks timestamp ties with trace/span IDs, but offset pagination is not
a snapshot and can change when ingestion or retention changes the dataset.
