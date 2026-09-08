package cmd

import (
	"context"
	"embed"

	"github.com/urfave/cli/v3"
)

//go:embed recipes/*.md
var recipeFiles embed.FS

var recipeCatalog = []struct {
	Topic   string `json:"topic"`
	Summary string `json:"summary"`
}{
	{"workflow", "Discover, aggregate, inspect, correlate, and check completeness"},
	{"logs", "Log fields, contexts, body filters, and timestamp units"},
	{"traces", "Spans versus traces, latency units, and log correlation"},
	{"aggregations", "Counts, percentiles, grouping, time buckets, and top-N limits"},
	{"metrics", "Metric metadata, PromQL, and native formula pitfalls"},
}

func (a *app) recipesCommand() *cli.Command {
	return operation(&cli.Command{Name: "recipes", Usage: "List offline query recipes or read one topic", ArgsUsage: "[TOPIC]", Action: func(_ context.Context, c *cli.Command) error {
		if c.NArg() == 0 {
			return a.emit(recipeCatalog, nil)
		}
		if c.NArg() != 1 {
			return fail("usage", "recipes accepts at most one topic; run sig agent recipes")
		}
		for _, entry := range recipeCatalog {
			if entry.Topic != c.Args().First() {
				continue
			}
			content, err := recipeFiles.ReadFile("recipes/" + entry.Topic + ".md")
			if err != nil {
				return fail("internal", "could not load bundled recipe")
			}
			return a.emit(struct {
				Topic   string `json:"topic"`
				Content string `json:"content"`
			}{entry.Topic, string(content)}, nil)
		}
		return fail("usage", "unknown recipe topic; run sig agent recipes")
	}}, commandPolicy{Effect: "local_read", Result: "array_or_object"})
}
