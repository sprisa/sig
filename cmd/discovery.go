package cmd

import (
	"context"
	"time"

	"github.com/sprisa/sig/signoz"
	"github.com/urfave/cli/v3"
)

func discoveryFlags(signal signoz.Signal, limit int) []cli.Flag {
	flags := append(windowFlags(), &cli.IntFlag{Name: "limit", Value: limit, Usage: "Maximum discovery results, 1-1000"}, &cli.StringFlag{Name: "search", Usage: "Search field names or values"}, &cli.StringFlag{Name: "field-context", Usage: "Field context, for example resource, attribute, log, or span"}, &cli.StringFlag{Name: "data-type", Usage: "Field data type, for example string, number, or bool"})
	if signal == signoz.Metrics {
		flags = append(flags, &cli.StringFlag{Name: "metric", Usage: "Metric name to discover attributes for"})
	}
	return flags
}

func (a *app) discoveryRequest(c *cli.Command, signal signoz.Signal) (signoz.FieldKeysRequest, error) {
	w, err := a.timeWindow(c)
	if err != nil {
		return signoz.FieldKeysRequest{}, err
	}
	r := signoz.FieldKeysRequest{Window: w, Signal: signal, Limit: c.Int("limit"), Search: c.String("search"), FieldContext: c.String("field-context"), DataType: c.String("data-type")}
	if signal == signoz.Metrics {
		r.MetricName = c.String("metric")
	}
	return r, r.Validate()
}

func (a *app) fieldKeysCommand(signal signoz.Signal) *cli.Command {
	return operation(&cli.Command{Name: "fields", Usage: "Discover field names, types, and contexts", Flags: discoveryFlags(signal, 100), Action: func(ctx context.Context, c *cli.Command) error {
		if err := noArgs(c); err != nil {
			return err
		}
		r, err := a.discoveryRequest(c, signal)
		if err != nil {
			return err
		}
		conn, err := a.connect(c)
		if err != nil {
			return err
		}
		result, err := conn.Client.FieldKeys(ctx, r)
		if err != nil {
			return err
		}
		return a.emit(result.Raw, discoveryMeta(conn.Context, r, result.Completeness()))
	}}, commandPolicy{Effect: "read", Authentication: true, Pagination: "bounded_discovery", Result: "object"})
}

func (a *app) fieldValuesCommand(signal signoz.Signal) *cli.Command {
	return operation(&cli.Command{Name: "values", Usage: "Discover observed values for a field", ArgsUsage: "FIELD", Flags: append(discoveryFlags(signal, 50), &cli.StringFlag{Name: "where", Usage: "Existing native filter to constrain value discovery"}), Action: func(ctx context.Context, c *cli.Command) error {
		if c.NArg() != 1 {
			return fail("usage", "values requires one field name")
		}
		scope, err := a.discoveryRequest(c, signal)
		if err != nil {
			return err
		}
		return a.fieldValues(ctx, c, signoz.FieldValuesRequest{FieldKeysRequest: scope, Name: c.Args().First(), Where: c.String("where")})
	}}, commandPolicy{Effect: "read", Authentication: true, QueryLanguage: "signoz_filter", Pagination: "bounded_discovery", Result: "object"})
}

func (a *app) servicesCommand() *cli.Command {
	list := operation(&cli.Command{Name: "list", Usage: "Discover service.name values in the selected signal", Flags: append(windowFlags(), &cli.StringFlag{Name: "signal", Value: "traces", Usage: "Signal to inspect: logs, traces, or metrics"}, &cli.IntFlag{Name: "limit", Value: 100, Usage: "Maximum services, 1-1000"}, &cli.StringFlag{Name: "search", Usage: "Search service names"}), Action: func(ctx context.Context, c *cli.Command) error {
		if err := noArgs(c); err != nil {
			return err
		}
		w, err := a.timeWindow(c)
		if err != nil {
			return err
		}
		return a.fieldValues(ctx, c, signoz.FieldValuesRequest{FieldKeysRequest: signoz.FieldKeysRequest{Window: w, Signal: signoz.Signal(c.String("signal")), Limit: c.Int("limit"), Search: c.String("search"), FieldContext: "resource"}, Name: "service.name"})
	}}, commandPolicy{Effect: "read", Authentication: true, Pagination: "bounded_discovery", Result: "object"})
	return &cli.Command{Name: "services", Usage: "Discover emitting services", Commands: []*cli.Command{list}}
}

func (a *app) fieldValues(ctx context.Context, c *cli.Command, r signoz.FieldValuesRequest) error {
	if err := r.Validate(); err != nil {
		return err
	}
	conn, err := a.connect(c)
	if err != nil {
		return err
	}
	result, err := conn.Client.FieldValues(ctx, r)
	if err != nil {
		return err
	}
	return a.emit(result.Raw, discoveryMeta(conn.Context, r.FieldKeysRequest, result.Completeness()))
}

func discoveryMeta(name string, r signoz.FieldKeysRequest, completeness string) any {
	return struct {
		SchemaVersion string        `json:"schema_version"`
		Context       string        `json:"context"`
		Signal        signoz.Signal `json:"signal"`
		Start         time.Time     `json:"start"`
		End           time.Time     `json:"end"`
		Completeness  string        `json:"completeness"`
	}{"1", name, r.Signal, r.Start, r.End, completeness}
}
