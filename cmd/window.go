package cmd

import (
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

func (a *app) timeWindow(c *cli.Command) (signoz.Window, error) {
	end := a.Now().UTC().Truncate(time.Millisecond)
	if c.IsSet("end") {
		parsed, err := time.Parse(time.RFC3339Nano, c.String("end"))
		if err != nil {
			return signoz.Window{}, fail("usage", "--end must be an RFC3339 timestamp with a timezone")
		}
		end = parsed.UTC().Truncate(time.Millisecond)
	}
	if c.IsSet("start") && c.IsSet("since") {
		return signoz.Window{}, fail("usage", "--start and --since are mutually exclusive")
	}
	if c.Duration("since") <= 0 {
		return signoz.Window{}, fail("usage", "--since must be positive")
	}
	start := end.Add(-c.Duration("since")).Truncate(time.Millisecond)
	if c.IsSet("start") {
		parsed, err := time.Parse(time.RFC3339Nano, c.String("start"))
		if err != nil {
			return signoz.Window{}, fail("usage", "--start must be an RFC3339 timestamp with a timezone")
		}
		start = parsed.UTC().Truncate(time.Millisecond)
	}
	w := signoz.Window{Start: start, End: end}
	return w, w.Validate()
}
