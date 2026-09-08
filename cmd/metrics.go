package cmd

import (
	"context"
	"time"

	"github.com/sprisa/sig/signoz"
	"github.com/urfave/cli/v3"
)

func (a *app) metricsCommand() *cli.Command {
	return &cli.Command{Name: "metrics", Usage: "Query PromQL and discover metrics", Commands: []*cli.Command{
		operation(&cli.Command{Name: "query", Usage: "Evaluate a PromQL range query", ArgsUsage: "PROMQL", Flags: append(windowFlags(), &cli.DurationFlag{Name: "step", Value: time.Minute, Usage: "Whole-second step (at most 11000 points per series)"}, &cli.BoolFlag{Name: "no-cache", Usage: "Bypass the server query cache"}), Action: a.metricsQuery}, commandPolicy{Effect: "read", Authentication: true, QueryLanguage: "promql", Result: "object"}),
		operation(&cli.Command{Name: "list", Usage: "List metric names and metadata", Flags: append(windowFlags(), &cli.IntFlag{Name: "limit", Value: 100, Usage: "Maximum metrics, 1-5000"}, &cli.StringFlag{Name: "search", Usage: "Search metric names"}), Action: a.metricsList}, commandPolicy{Effect: "read", Authentication: true, Pagination: "bounded_listing", Result: "array"}),
		a.fieldKeysCommand(signoz.Metrics), a.fieldValuesCommand(signoz.Metrics),
	}}
}

func (a *app) metricsQuery(ctx context.Context, c *cli.Command) error {
	if c.NArg() != 1 {
		return fail("usage", "metrics query requires one quoted PromQL expression")
	}
	w, err := a.timeWindow(c)
	if err != nil {
		return err
	}
	r := signoz.MetricsQueryRequest{Window: w, Expression: c.Args().First(), Step: c.Duration("step"), NoCache: c.Bool("no-cache")}
	if err := r.Validate(); err != nil {
		return err
	}
	conn, err := a.connect(c)
	if err != nil {
		return err
	}
	result, err := conn.Client.MetricsQuery(ctx, r)
	if err != nil {
		return err
	}
	return a.emit(result.Raw, map[string]any{"schema_version": "1", "context": conn.Context, "signal": "metrics", "start": w.Start, "end": w.End, "step_seconds": int64(r.Step / time.Second)})
}

func (a *app) metricsList(ctx context.Context, c *cli.Command) error {
	if err := noArgs(c); err != nil {
		return err
	}
	w, err := a.timeWindow(c)
	if err != nil {
		return err
	}
	r := signoz.MetricsListRequest{Window: w, Limit: c.Int("limit"), Search: c.String("search")}
	if err := r.Validate(); err != nil {
		return err
	}
	conn, err := a.connect(c)
	if err != nil {
		return err
	}
	result, err := conn.Client.MetricsList(ctx, r)
	if err != nil {
		return err
	}
	return a.emit(result.Metrics, map[string]any{"schema_version": "1", "context": conn.Context, "start": w.Start, "end": w.End, "limit": r.Limit, "completeness": "unknown"})
}
