package cmd

import (
	"context"

	"github.com/sprisa/sig/signoz"
	"github.com/urfave/cli/v3"
)

func (a *app) signalCommand(signal signoz.Signal) *cli.Command {
	search := operation(&cli.Command{Name: "search", Usage: "Search newest " + string(signal) + " with bounded pagination", Flags: append(windowFlags(),
		&cli.StringFlag{Name: "where", Usage: "Native SigNoz filter expression"},
		&cli.IntFlag{Name: "limit", Value: 100, Usage: "Rows per page, 1-10000"},
		&cli.IntFlag{Name: "pages", Value: 1, Usage: "Maximum pages, 1-100 (at most 10000 total rows)"},
		&cli.IntFlag{Name: "offset", Usage: "Rows to skip, 0-1000000; nonzero requires explicit --start and --end"},
		&cli.StringFlag{Name: "page-token", Usage: "Resume using meta.next_page_token; do not combine with query flags"},
	), Action: func(ctx context.Context, c *cli.Command) error { return a.search(ctx, c, signal) }}, commandPolicy{Effect: "read", Authentication: true, QueryLanguage: "signoz_filter", Pagination: "page_token", Result: "array"})
	group := &cli.Command{Name: string(signal), Usage: "Query and discover " + string(signal), Commands: []*cli.Command{search, a.aggregateCommand(signal), a.fieldKeysCommand(signal), a.fieldValuesCommand(signal)}}
	if signal == signoz.Traces {
		search.Description = "Returns spans, not distinct or complete traces. Use traces get TRACE_ID for a waterfall."
		group.Commands = append(group.Commands, operation(&cli.Command{Name: "get", Usage: "Retrieve a trace waterfall, preserving partial-trace indicators", ArgsUsage: "TRACE_ID", Flags: []cli.Flag{
			&cli.StringFlag{Name: "span", Usage: "Select a span to center the trace window on"}, &cli.StringSliceFlag{Name: "expand", Usage: "Span ID to expand (repeatable)"},
		}, Action: a.trace}, commandPolicy{Effect: "read", Authentication: true, Pagination: "span_window", Result: "object"}))
	}
	return group
}

func (a *app) trace(ctx context.Context, c *cli.Command) error {
	if c.NArg() != 1 {
		return fail("usage", "traces get requires one trace ID")
	}
	r := signoz.TraceRequest{ID: c.Args().First(), SelectedSpan: c.String("span"), ExpandedSpans: c.StringSlice("expand")}
	if err := r.Validate(); err != nil {
		return err
	}
	conn, err := a.connect(c)
	if err != nil {
		return err
	}
	result, err := conn.Client.Trace(ctx, r)
	if err != nil {
		return err
	}
	return a.emit(result.Raw, map[string]any{"schema_version": "1", "context": conn.Context, "completeness": result.Completeness()})
}
