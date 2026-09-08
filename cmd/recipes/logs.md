# Log Query Recipes

Discover before filtering:

```sh
sig logs fields --field-context resource --since 1h
sig logs fields --field-context attribute --since 1h
sig logs values severity_text --field-context log --since 1h
```

Log attributes are tenant-specific. Even `service.name` is present only when the
pipeline supplies it. Severity strings are observed values, not a fixed enum.
Use discovered spelling and case rather than assuming `ERROR` or `WARN` exists.

Filters are SigNoz expressions, not SQL queries or structured `{op,items}` JSON:

```sh
sig logs search --where "severity_text = 'ERROR' AND body CONTAINS 'timeout'" --since 1h --limit 20
sig logs search --where "(severity_text = 'ERROR' OR body CONTAINS 'panic') AND resource.service.name = 'checkout'" --since 1h --limit 20
```

Use parentheses for AND/OR precedence. `resource.<key>` and `attribute.<key>`
disambiguate attributes with the same name. Bare ambiguous fields may resolve
to a resource attribute and produce a backend warning; do not ignore it.

`body CONTAINS 'timeout'` searches rendered text. `body.error.code = 'E_TIMEOUT'`
addresses a nested JSON-body field and will not match a non-JSON body. Attribute
paths and JSON-body paths are different contexts. Use plain body search when
you do not know whether the message is JSON.

Common intrinsic fields include `timestamp`, `body`, `severity_text`,
`severity_number`, `trace_id`, `span_id`, and `id`. Discover resource and record
attributes instead of inferring their existence from these examples.

Prefer CLI window flags to timestamp filters. Native v5 top-level `start` and
`end` are Unix milliseconds; numeric comparisons against the `timestamp` column
use Unix nanoseconds. A millisecond value in that filter can silently select the
wrong range. CLI window flags handle the request-bound conversion for you.

Use `sig logs aggregate --aggregation 'count()' ...` for totals. `search --limit`
limits returned rows, not the population over which an aggregate is computed.
