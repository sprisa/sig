# Performance Checks

All workloads are synthetic. They require no SigNoz instance, Kubernetes access,
service-account credentials, or OS keychain. Production code is not replaced or
modified by the experiments.

```sh
task perf
task perf:throughput
task perf:decode
task perf:encode

task perf:profile PERF_CASE=near_cap PROFILE_TIME=10x
task perf:alloc
task perf:cpu

task perf:resources
```

Use `BENCH_TIME` and `BENCH_COUNT` to control benchmark duration and repetition:

```sh
task perf:throughput BENCH_TIME=1s BENCH_COUNT=5
task perf:resources RESOURCE_COUNT=5
```

## Workloads

| Case | Rows per page | Body bytes per row | Pages | Approximate response bytes |
| --- | --- | --- | --- | --- |
| `default` | 100 | 512 | 1 | 67 KB |
| `one_meg` | 1000 | 1024 | 1 | 1.18 MB |
| `near_cap` | 10000 | 1450 | 1 | 16.10 MB, below the 16 MiB cap |
| `ten_pages` | 1000 | 1024 | 10 | 11.84 MB total |

Each row includes a timestamp, unique synthetic ID, service name, severity, body,
and a number above JavaScript's exact integer range. The decoding comparison has
a correctness test that checks byte-identical rows and preserved numeric values.

## Measurements

**`perf:throughput`** exercises the actual client and page collector against a
loopback HTTP test server, then encodes the CLI-style output to `io.Discard`.
Fixture construction is outside benchmark timing. Counts include client and
in-process mock-server allocations, but not CLI startup, keychain access, disk
output, network latency, or SigNoz query execution. `MB/s` measures incoming JSON
bytes processed per second, not remote database throughput.
`pipelines/s` counts complete collection-and-output operations; `requests/s`
counts the pages fetched. These are serial loopback measurements, not a
concurrent load test or a throughput claim for a deployed SigNoz instance.

**`perf:decode`** compares two deliberately fixed experiments: nested raw-subtree
decoding and direct typed-envelope decoding that still preserves opaque raw rows.
Neither is a replacement for the production benchmark. They isolate parsing and
copying, excluding HTTP reads and output. The nested baseline should not be
updated to follow production optimizations.

**`perf:encode`** isolates encoding already-decoded raw rows. Encoder buffer pools
can be reused in a long-running benchmark, so low `B/op` does not mean a fresh CLI
process avoids allocating an output buffer.

**`perf:resources`** launches the compiled CLI under `/usr/bin/time`: `-l` on macOS
and GNU `-v` on Linux. The mock server lives outside the measured child process.
The child receives only a synthetic endpoint/key and an isolated configuration
directory, and its JSON output is discarded. macOS reports maximum RSS in bytes;
GNU time reports it in KiB. The first process launch can include OS executable
verification and cold-cache costs. No `go run` compilation is included in child
timing.

The retained-heap probe holds one API result across forced garbage collections.
It contrasts search rows with native results retaining both the raw response and
decoded result views. Its heap delta is approximate and can include residual HTTP
state; it is not peak RSS.

## Interpretation

- `ns/op`: time for one benchmark operation.
- `B/op`: total bytes allocated during that operation, including garbage later
  collected. It is not simultaneous memory usage.
- `allocs/op`: allocation count, independent of allocation size.
- Maximum RSS: peak resident memory of a fresh CLI process, including the Go
  runtime and temporary buffers.
- Retained heap: live Go objects remaining while a result is held, after GC.

The 16 MiB response/content cap is **not** a heap or RSS limit. HTTP buffering,
raw-subtree copies, typed views, page accumulation, and output encoding can all
overlap. Multiple CLI processes multiply the resident-memory cost.

The production search path now decodes the success envelope directly into typed
search metadata and owned raw rows, instead of copying each intermediate query
subtree. The `nested_raw` experiment intentionally retains the old strategy as a
comparison baseline. Native query and trace results still retain their complete
raw payloads where the public result contract requires them. HTTP reads and JSON
output buffering remain allocation costs; this is not a zero-copy implementation.

Do not compare race-enabled builds with normal benchmark runs. Keep Go version,
`GOEXPERIMENT`, `GOGC`, `GOMEMLIMIT`, architecture, and workload constant when
comparing revisions. The probes inherit Go tuning variables intentionally.

## Profiles

Profiles and their test executable are written under `bin/perf/`, already ignored
by Git. Keep the executable paired with its profiles. Profile totals can include
one-time fixture/setup allocations; use enough iterations to amortize setup.
`alloc_space` identifies allocation churn, while `inuse_space` describes only
objects alive when the profile was taken, not historical peak memory.

Use `task perf:alloc` or `task perf:cpu` for text reports. For interactive analysis:

```sh
go tool pprof -http=127.0.0.1:8081 bin/perf/bench.test bin/perf/alloc.pprof
```

Do not commit captured production telemetry or use it as a benchmark fixture.
These tasks are opt-in and do not impose noisy timing thresholds on CI.
