package cmd

import (
	"context"
	"io"
	"os"

	"github.com/sprisa/sig/signoz"
	"github.com/urfave/cli/v3"
)

func (a *app) queryCommand() *cli.Command {
	fileFlag := func() cli.Flag {
		return &cli.StringFlag{Name: "file", Required: true, Usage: "JSON file, or - for stdin (maximum 1 MiB)"}
	}
	description := "Supports native builder queries, aggregations, formulas, PromQL, and SQL. Server permissions govern SQL execution. Input must include millisecond start/end bounds; streaming is not supported."
	return &cli.Command{Name: "query", Usage: "Execute or preview native v5 JSON requests", Commands: []*cli.Command{
		operation(&cli.Command{Name: "run", Usage: "Execute a native SigNoz query JSON object", Description: description, Flags: []cli.Flag{fileFlag()}, Action: a.runQuery}, commandPolicy{Effect: "server_defined", Authentication: true, QueryLanguage: "signoz_v5_json", Result: "object"}),
		operation(&cli.Command{Name: "preview", Usage: "Preview a native SigNoz query JSON object", Description: description, Flags: []cli.Flag{fileFlag(), &cli.BoolFlag{Name: "verbose", Usage: "Include additional ClickHouse analysis; even nonverbose preview may query ClickHouse"}}, Action: a.previewQuery}, commandPolicy{Effect: "read", Authentication: true, QueryLanguage: "signoz_v5_json", Result: "object"}),
	}}
}

func (a *app) readQuery(ctx context.Context, c *cli.Command) (signoz.NativeQuery, error) {
	if err := noArgs(c); err != nil {
		return signoz.NativeQuery{}, err
	}
	var input io.Reader = a.In
	if c.String("file") != "-" {
		info, err := os.Stat(c.String("file"))
		if err != nil || !info.Mode().IsRegular() {
			return signoz.NativeQuery{}, fail("usage", "query input must be a regular file; use --file - for streams")
		}
		f, err := os.Open(c.String("file"))
		if err != nil {
			return signoz.NativeQuery{}, fail("usage", "cannot open query file")
		}
		defer f.Close()
		input = f
	}
	data, err := readBoundedInput(ctx, input, signoz.MaxQueryBytes)
	if err != nil {
		return signoz.NativeQuery{}, err
	}
	return signoz.ParseQuery(data)
}

func (a *app) runQuery(ctx context.Context, c *cli.Command) error {
	q, err := a.readQuery(ctx, c)
	if err != nil {
		return err
	}
	conn, err := a.connect(c)
	if err != nil {
		return err
	}
	result, err := conn.Client.Query(ctx, q)
	if err != nil {
		return err
	}
	return a.emit(result.Raw, map[string]any{"schema_version": "1", "context": conn.Context, "preview": false})
}

func (a *app) previewQuery(ctx context.Context, c *cli.Command) error {
	q, err := a.readQuery(ctx, c)
	if err != nil {
		return err
	}
	conn, err := a.connect(c)
	if err != nil {
		return err
	}
	result, err := conn.Client.Preview(ctx, q, c.Bool("verbose"))
	if err != nil {
		return err
	}
	return a.emit(result.Raw, map[string]any{"schema_version": "1", "context": conn.Context, "preview": true})
}
