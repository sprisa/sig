package cmd

import (
	"context"
	"io"
	"os"
	"runtime/debug"
	"time"

	"github.com/sprisa/sig/config"
	"github.com/sprisa/sig/signoz"
	"github.com/urfave/cli/v3"
)

var Version = "dev"

type Options struct {
	In          io.Reader
	Out, ErrOut io.Writer
	ConfigDir   string
	Getenv      func(string) string
	Credentials config.Credentials
	Now         func() time.Time
}
type app struct{ Options }

func Run(ctx context.Context, args []string, opts Options) int {
	if opts.In == nil {
		opts.In = os.Stdin
	}
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.ErrOut == nil {
		opts.ErrOut = os.Stderr
	}
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.Credentials == nil {
		opts.Credentials = config.Keyring{}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	a := &app{opts}
	if err := a.command().Run(ctx, args); err != nil {
		return a.report(err)
	}
	return 0
}

func (a *app) command() *cli.Command {
	root := &cli.Command{
		Name: "sig", Usage: "Query SigNoz with JSON output", Reader: a.In, Writer: a.Out, ErrWriter: a.ErrOut,
		HideVersion: true, HideHelpCommand: true,
		Flags: []cli.Flag{&cli.StringFlag{Name: "context", Usage: "Use a named context instead of the current context"}, &cli.DurationFlag{Name: "timeout", Value: 30 * time.Second, Usage: "HTTP request timeout"}},
		Commands: []*cli.Command{
			a.authCommand(), a.contextCommand(), a.signalCommand(signoz.Logs), a.signalCommand(signoz.Traces), a.metricsCommand(), a.servicesCommand(), a.queryCommand(),
			{Name: "agent", Usage: "Machine-readable command discovery and offline query recipes", Commands: []*cli.Command{operation(&cli.Command{Name: "schema", Usage: "Describe commands, flags, safety, and output contracts as JSON", ArgsUsage: "[COMMAND [SUBCOMMAND]]", Action: a.schema}, commandPolicy{Effect: "local_read", Result: "object"}), a.recipesCommand()}},
			operation(&cli.Command{Name: "version", Usage: "Print the CLI version as JSON", Action: func(_ context.Context, c *cli.Command) error {
				if err := noArgs(c); err != nil {
					return err
				}
				return a.emit(map[string]string{"version": buildVersion()}, nil)
			}}, commandPolicy{Effect: "local_read", Result: "object"}),
		},
	}
	_ = root.Walk(func(c *cli.Command) error {
		c.ExitErrHandler = func(context.Context, *cli.Command, error) {}
		c.OnUsageError = func(context.Context, *cli.Command, error, bool) error {
			return fail("usage", "invalid command arguments or flags; see --help")
		}
		if c.Action == nil {
			c.Action = func(_ context.Context, c *cli.Command) error {
				if err := noArgs(c); err != nil {
					return err
				}
				return cli.ShowSubcommandHelp(c)
			}
		}
		return nil
	})
	return root
}

func noArgs(c *cli.Command) error {
	if c.NArg() != 0 {
		return fail("usage", "unexpected arguments; see --help")
	}
	return nil
}
func buildVersion() string {
	if Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return Version
}
