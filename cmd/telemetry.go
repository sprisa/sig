package cmd

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sprisa/sig/signoz"
	"github.com/urfave/cli/v3"
)

func windowFlags() []cli.Flag {
	return []cli.Flag{
		&cli.DurationFlag{Name: "since", Value: 15 * time.Minute, Usage: "Lookback from end (mutually exclusive with --start)"},
		&cli.StringFlag{Name: "start", Usage: "Start time in RFC3339 with timezone"},
		&cli.StringFlag{Name: "end", Usage: "End time in RFC3339 with timezone (default: now)"},
	}
}

func (a *app) signalCommand(signal string) *cli.Command {
	search := &cli.Command{Name: "search", Usage: "Search newest " + signal + " with bounded pagination", Action: func(ctx context.Context, cmd *cli.Command) error { return a.search(ctx, cmd, signal) }}
	search.Flags = append(windowFlags(),
		&cli.StringFlag{Name: "where", Usage: "Native SigNoz filter expression"},
		&cli.IntFlag{Name: "limit", Value: 100, Usage: "Rows per page, 1-10000"},
		&cli.IntFlag{Name: "pages", Value: 1, Usage: "Maximum pages to fetch, 1-100 (at most 10000 total rows)"},
		&cli.IntFlag{Name: "offset", Usage: "Rows to skip, 0-1000000; nonzero requires explicit --start and --end"},
		&cli.StringFlag{Name: "page-token", Usage: "Resume using meta.next_page_token; do not combine with query flags"},
	)
	if signal == "traces" {
		search.Description = "Returns spans, not distinct or complete traces. Use traces get TRACE_ID for a waterfall."
	}
	group := &cli.Command{Name: signal, Usage: "Query and discover " + signal, Commands: []*cli.Command{search, a.fieldsCommand(signal, false), a.fieldsCommand(signal, true)}}
	if signal == "traces" {
		group.Commands = append(group.Commands, &cli.Command{Name: "get", Usage: "Retrieve a trace waterfall, preserving partial-trace indicators", ArgsUsage: "TRACE_ID", Flags: []cli.Flag{
			&cli.StringFlag{Name: "span", Usage: "Select a span to center the trace window on"},
			&cli.StringSliceFlag{Name: "expand", Usage: "Span ID to expand (repeatable)"},
		}, Action: a.trace})
	}
	return group
}

func (a *app) fieldsCommand(signal string, values bool) *cli.Command {
	name, usage := "fields", "Discover field names, types, and contexts"
	limit := 100
	if values {
		name, usage, limit = "values", "Discover observed values for a field", 50
	}
	command := &cli.Command{Name: name, Usage: usage, Flags: append(windowFlags(),
		&cli.IntFlag{Name: "limit", Value: limit, Usage: "Maximum discovery results, 1-1000"},
		&cli.StringFlag{Name: "search", Usage: "Search field names or values"},
		&cli.StringFlag{Name: "field-context", Usage: "Field context, for example resource, attribute, log, or span"},
		&cli.StringFlag{Name: "data-type", Usage: "Field data type, for example string, number, or bool"},
	)}
	if signal == "metrics" {
		command.Flags = append(command.Flags, &cli.StringFlag{Name: "metric", Usage: "Metric name to discover attributes for"})
	}
	if values {
		command.ArgsUsage = "FIELD"
		command.Flags = append(command.Flags, &cli.StringFlag{Name: "where", Usage: "Existing native filter to constrain value discovery"})
	}
	command.Action = func(ctx context.Context, cmd *cli.Command) error { return a.discover(ctx, cmd, signal, values, "") }
	return command
}

func (a *app) servicesCommand() *cli.Command {
	list := &cli.Command{Name: "list", Usage: "Discover service.name values in the selected signal", Flags: append(windowFlags(),
		&cli.StringFlag{Name: "signal", Value: "traces", Usage: "Signal to inspect: logs, traces, or metrics"},
		&cli.IntFlag{Name: "limit", Value: 100, Usage: "Maximum services, 1-1000"},
		&cli.StringFlag{Name: "search", Usage: "Search service names"},
	), Action: func(ctx context.Context, cmd *cli.Command) error {
		return a.discover(ctx, cmd, cmd.String("signal"), true, "service.name")
	}}
	return &cli.Command{Name: "services", Usage: "Discover emitting services", Commands: []*cli.Command{list}}
}

func (a *app) metricsCommand() *cli.Command {
	return &cli.Command{Name: "metrics", Usage: "Query PromQL and discover metrics", Commands: []*cli.Command{
		{Name: "query", Usage: "Evaluate a PromQL range query", ArgsUsage: "PROMQL", Flags: append(windowFlags(), &cli.DurationFlag{Name: "step", Value: time.Minute, Usage: "Whole-second step (at most 11000 points per series)"}, &cli.BoolFlag{Name: "no-cache", Usage: "Bypass the server query cache"}), Action: a.metricsQuery},
		{Name: "list", Usage: "List metric names and metadata", Flags: append(windowFlags(), &cli.IntFlag{Name: "limit", Value: 100, Usage: "Maximum metrics, 1-5000"}, &cli.StringFlag{Name: "search", Usage: "Search metric names"}), Action: a.metricsList},
		a.fieldsCommand("metrics", false), a.fieldsCommand("metrics", true),
	}}
}

func (a *app) queryCommand() *cli.Command {
	group := &cli.Command{Name: "query", Usage: "Execute or preview native v5 JSON requests"}
	for _, name := range []string{"run", "preview"} {
		preview := name == "preview"
		command := &cli.Command{Name: name, Usage: "Submit a native SigNoz query JSON object", Description: "Supports builder queries, aggregations, formulas, PromQL, and SQL. Server permissions govern SQL execution. Input must include millisecond start/end bounds; streaming requests are not supported.", Flags: []cli.Flag{&cli.StringFlag{Name: "file", Required: true, Usage: "JSON file, or - for stdin (maximum 1 MiB)"}}}
		if preview {
			command.Flags = append(command.Flags, &cli.BoolFlag{Name: "verbose", Usage: "Include additional ClickHouse analysis; even nonverbose preview may query ClickHouse"})
		}
		command.Action = func(ctx context.Context, cmd *cli.Command) error { return a.nativeQuery(ctx, cmd, preview) }
		group.Commands = append(group.Commands, command)
	}
	return group
}

func (a *app) discover(ctx context.Context, cmd *cli.Command, signal string, values bool, fixedName string) error {
	if (values && fixedName == "" && cmd.NArg() != 1) || ((!values || fixedName != "") && cmd.NArg() != 0) {
		return fail("usage", "values requires one field name; other discovery commands take no positional arguments")
	}
	start, end, err := a.timeWindow(cmd)
	if err != nil {
		return err
	}
	name := fixedName
	if name == "" && values {
		name = cmd.Args().First()
	}
	fieldContext, datatype, metric, where := "", "", "", ""
	if fixedName == "" {
		fieldContext, datatype = cmd.String("field-context"), cmd.String("data-type")
		if signal == "metrics" {
			metric = cmd.String("metric")
		}
		if values {
			where = cmd.String("where")
		}
	} else {
		fieldContext = "resource"
	}
	if cmd.Int("limit") < 1 || cmd.Int("limit") > 1000 {
		return fail("usage", "discovery limit must be between 1 and 1000")
	}
	if signal != "logs" && signal != "traces" && signal != "metrics" {
		return fail("usage", "signal must be logs, traces, or metrics")
	}
	client, contextName, _, _, err := a.connect(cmd)
	if err != nil {
		return err
	}
	data, err := client.Fields(ctx, signal, values, signoz.DiscoveryOptions{Start: start, End: end, Limit: cmd.Int("limit"), Name: name, Search: cmd.String("search"), Where: where, FieldContext: fieldContext, DataType: datatype, MetricName: metric})
	if err != nil {
		return err
	}
	object, err := signoz.Object(data)
	if err != nil {
		return err
	}
	complete := "unknown"
	if string(object["complete"]) == "true" {
		complete = "complete"
	}
	return a.emit(data, map[string]any{"schema_version": "1", "context": contextName, "signal": signal, "start": start, "end": end, "completeness": complete})
}

func (a *app) trace(ctx context.Context, cmd *cli.Command) error {
	if cmd.NArg() != 1 {
		return fail("usage", "traces get requires one trace ID")
	}
	client, name, _, _, err := a.connect(cmd)
	if err != nil {
		return err
	}
	data, err := client.Trace(ctx, cmd.Args().First(), cmd.String("span"), cmd.StringSlice("expand"))
	if err != nil {
		return err
	}
	var info struct {
		Spans   json.RawMessage `json:"spans"`
		HasMore *bool           `json:"hasMore"`
		Missing *bool           `json:"hasMissingSpans"`
	}
	if json.Unmarshal(data, &info) != nil || info.HasMore == nil || info.Missing == nil || len(info.Spans) == 0 {
		return fail("invalid_response", "unexpected trace waterfall response")
	}
	var spans []json.RawMessage
	if json.Unmarshal(info.Spans, &spans) != nil {
		return fail("invalid_response", "trace waterfall did not contain a span array")
	}
	complete := "complete"
	if *info.HasMore || *info.Missing {
		complete = "partial"
	}
	return a.emit(data, map[string]any{"schema_version": "1", "context": name, "completeness": complete})
}

func (a *app) metricsQuery(ctx context.Context, cmd *cli.Command) error {
	if cmd.NArg() != 1 || strings.TrimSpace(cmd.Args().First()) == "" {
		return fail("usage", "metrics query requires one quoted PromQL expression")
	}
	start, end, err := a.timeWindow(cmd)
	if err != nil {
		return err
	}
	step := cmd.Duration("step")
	if step < time.Second || step%time.Second != 0 || (end.UnixMilli()-start.UnixMilli())/step.Milliseconds()+1 > 11000 {
		return fail("usage", "step must be whole seconds and produce at most 11000 points per series")
	}
	client, name, _, _, err := a.connect(cmd)
	if err != nil {
		return err
	}
	data, err := client.MetricsQuery(ctx, cmd.Args().First(), start, end, step, cmd.Bool("no-cache"))
	if err != nil {
		return err
	}
	return a.emit(data, map[string]any{"schema_version": "1", "context": name, "signal": "metrics", "start": start, "end": end, "step_seconds": int64(step / time.Second)})
}

func (a *app) metricsList(ctx context.Context, cmd *cli.Command) error {
	if err := noArgs(cmd); err != nil {
		return err
	}
	start, end, err := a.timeWindow(cmd)
	if err != nil {
		return err
	}
	if cmd.Int("limit") < 1 || cmd.Int("limit") > 5000 {
		return fail("usage", "metric list limit must be between 1 and 5000")
	}
	client, name, _, _, err := a.connect(cmd)
	if err != nil {
		return err
	}
	data, err := client.MetricsList(ctx, start, end, cmd.Int("limit"), cmd.String("search"))
	if err != nil {
		return err
	}
	var result struct {
		Metrics json.RawMessage `json:"metrics"`
	}
	if json.Unmarshal(data, &result) != nil || len(result.Metrics) == 0 {
		return fail("invalid_response", "unexpected metric listing response")
	}
	var metrics []json.RawMessage
	if json.Unmarshal(result.Metrics, &metrics) != nil {
		return fail("invalid_response", "metric listing did not contain an array")
	}
	if metrics == nil {
		metrics = []json.RawMessage{}
	}
	return a.emit(metrics, map[string]any{"schema_version": "1", "context": name, "start": start, "end": end, "limit": cmd.Int("limit"), "completeness": "unknown"})
}

func (a *app) nativeQuery(ctx context.Context, cmd *cli.Command, preview bool) error {
	if err := noArgs(cmd); err != nil {
		return err
	}
	var input io.Reader = a.In
	if cmd.String("file") != "-" {
		info, err := os.Stat(cmd.String("file"))
		if err != nil || !info.Mode().IsRegular() {
			return fail("usage", "query input must be a regular file; use --file - for streams")
		}
		f, err := os.Open(cmd.String("file"))
		if err != nil {
			return fail("usage", "cannot open query file")
		}
		defer f.Close()
		input = f
	}
	data, err := readBoundedInput(ctx, input, signoz.MaxQueryBytes)
	if err != nil {
		return err
	}
	if err := signoz.ValidateQuery(data); err != nil {
		return err
	}
	client, name, _, _, err := a.connect(cmd)
	if err != nil {
		return err
	}
	verbose := false
	if preview {
		verbose = cmd.Bool("verbose")
	}
	result, err := client.Query(ctx, data, preview, verbose)
	if err != nil {
		return err
	}
	return a.emit(result, map[string]any{"schema_version": "1", "context": name, "preview": preview})
}
