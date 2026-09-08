package cmd

import (
	"context"
	"time"

	"github.com/sprisa/sig/signoz"
	"github.com/urfave/cli/v3"
)

func (a *app) aggregateCommand(signal signoz.Signal) *cli.Command {
	return operation(&cli.Command{Name: "aggregate", Usage: "Compute bounded counts, percentiles, or grouped statistics",
		Description: "Returns a scalar aggregate per group, or a time series with --step. The group limit applies across the entire window, not independently per bucket. No pagination; completeness remains unknown. Discover field names and contexts first. See sig agent recipes aggregations.",
		Flags: append(windowFlags(),
			&cli.StringFlag{Name: "aggregation", Required: true, Usage: "One expression: count(), rate(), or count_distinct/avg/sum/min/max/p50/p75/p90/p95/p99(FIELD)"},
			&cli.StringSliceFlag{Name: "group-by", Usage: "Group field (repeatable or comma-separated); optional explicit resource./attribute./scope./log./span./body. prefix"},
			&cli.StringFlag{Name: "where", Usage: "Native SigNoz filter expression"},
			&cli.IntFlag{Name: "limit", Value: 100, Usage: "Maximum groups across the window, 1-10000"},
			&cli.StringFlag{Name: "order", Value: "desc", Usage: "Sort by the aggregation: asc or desc"},
			&cli.DurationFlag{Name: "step", Usage: "Enable time series with a whole-second bucket size, e.g. 1m (at most 11000 points per series)"},
			&cli.BoolFlag{Name: "no-cache", Usage: "Bypass the server query cache"},
		), Action: func(ctx context.Context, c *cli.Command) error {
			if err := noArgs(c); err != nil {
				return err
			}
			w, err := a.timeWindow(c)
			if err != nil {
				return err
			}
			if c.IsSet("step") && c.Duration("step") <= 0 {
				return fail("usage", "--step must be positive when supplied")
			}
			r := signoz.AggregateRequest{Window: w, Signal: signal, Aggregation: c.String("aggregation"), Where: c.String("where"), GroupBy: c.StringSlice("group-by"), Limit: c.Int("limit"), Order: c.String("order"), Step: c.Duration("step"), NoCache: c.Bool("no-cache")}
			if err := r.Validate(); err != nil {
				return err
			}
			conn, err := a.connect(c)
			if err != nil {
				return err
			}
			result, err := conn.Client.Aggregate(ctx, r)
			if err != nil {
				return err
			}
			return a.emit(result.Raw, struct {
				SchemaVersion string        `json:"schema_version"`
				Context       string        `json:"context"`
				Signal        signoz.Signal `json:"signal"`
				Start         time.Time     `json:"start"`
				End           time.Time     `json:"end"`
				Limit         int           `json:"limit"`
				StepSeconds   int64         `json:"step_seconds,omitzero"`
				Completeness  string        `json:"completeness"`
			}{"1", conn.Context, signal, w.Start, w.End, r.Limit, int64(r.Step / time.Second), "unknown"})
		}}, commandPolicy{Effect: "read", Authentication: true, QueryLanguage: "signoz_aggregation", Pagination: "bounded_groups", Result: "object"})
}
