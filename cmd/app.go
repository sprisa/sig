package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
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
	cmd := a.command()
	err := cmd.Run(ctx, args)
	if err == nil {
		return 0
	}
	var apiErr *signoz.Error
	if !errors.As(err, &apiErr) {
		apiErr = &signoz.Error{Code: "internal", Message: "command failed"}
	}
	_ = json.NewEncoder(a.ErrOut).Encode(map[string]any{"error": apiErr})
	switch apiErr.Code {
	case "usage":
		return 2
	case "authentication":
		return 3
	case "permission":
		return 4
	case "network", "timeout", "cancelled":
		return 5
	case "config", "credentials":
		return 7
	default:
		return 6
	}
}

func fail(code, message string) error { return &signoz.Error{Code: code, Message: message} }

func (a *app) emit(data any, meta any) error {
	result := map[string]any{"data": data}
	if meta != nil {
		result["meta"] = meta
	}
	if err := json.NewEncoder(a.Out).Encode(result); err != nil {
		return fail("output", "could not write JSON output")
	}
	return nil
}

func noArgs(cmd *cli.Command) error {
	if cmd.NArg() != 0 {
		return fail("usage", "unexpected arguments; see --help")
	}
	return nil
}

func (a *app) command() *cli.Command {
	root := &cli.Command{
		Name: "sig", Usage: "Query SigNoz with JSON output",
		Reader: a.In, Writer: a.Out, ErrWriter: a.ErrOut,
		HideVersion: true, HideHelpCommand: true,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "context", Usage: "Use a named context instead of the current context"},
			&cli.DurationFlag{Name: "timeout", Value: 30 * time.Second, Usage: "HTTP request timeout"},
		},
		Commands: []*cli.Command{
			{Name: "auth", Usage: "Manage local service-account credentials", Commands: []*cli.Command{
				{Name: "login", Usage: "Validate and securely store a service-account key", Flags: []cli.Flag{
					&cli.StringFlag{Name: "url", Usage: "Reachable SigNoz base URL"},
					&cli.BoolFlag{Name: "key-stdin", Usage: "Read the API key from stdin instead of prompting"},
				}, Action: a.login},
				{Name: "status", Usage: "Validate the effective credential and report identity", Action: a.status},
				{Name: "logout", Usage: "Remove the stored credential, without revoking the server key", Action: a.logout},
			}},
			{Name: "config", Usage: "Manage optional named contexts", Commands: []*cli.Command{
				{Name: "get-contexts", Usage: "List configured contexts (never credentials)", Action: a.getContexts},
				{Name: "current-context", Usage: "Show the configured current context", Action: a.currentContext},
				{Name: "use-context", Usage: "Select a context", ArgsUsage: "NAME", Action: a.useContext},
				{Name: "delete-context", Usage: "Delete a context and its stored credential", ArgsUsage: "NAME", Action: a.deleteContext},
			}},
			{Name: "logs", Usage: "Query log records", Commands: []*cli.Command{
				{Name: "search", Usage: "Retrieve one bounded page of newest logs", Flags: []cli.Flag{
					&cli.StringFlag{Name: "where", Usage: "Native SigNoz filter expression"},
					&cli.DurationFlag{Name: "since", Value: 15 * time.Minute, Usage: "Lookback from the end time (mutually exclusive with --start)"},
					&cli.StringFlag{Name: "start", Usage: "Inclusive start time in RFC3339"},
					&cli.StringFlag{Name: "end", Usage: "End time in RFC3339 (default: now)"},
					&cli.IntFlag{Name: "limit", Value: 100, Usage: "Maximum records, from 1 to 10000"},
				}, Action: a.logs},
			}},
			{Name: "version", Usage: "Print the CLI version as JSON", Action: func(_ context.Context, cmd *cli.Command) error {
				if err := noArgs(cmd); err != nil {
					return err
				}
				return a.emit(map[string]string{"version": Version}, nil)
			}},
		},
	}
	_ = root.Walk(func(cmd *cli.Command) error {
		cmd.ExitErrHandler = func(context.Context, *cli.Command, error) {}
		cmd.OnUsageError = func(context.Context, *cli.Command, error, bool) error {
			return fail("usage", "invalid command arguments or flags; see --help")
		}
		if cmd.Action == nil {
			cmd.Action = func(ctx context.Context, c *cli.Command) error {
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

func (a *app) load() (string, *config.Config, error) {
	dir := a.ConfigDir
	if dir == "" {
		dir = a.Getenv("SIG_CONFIG_DIR")
	}
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", nil, fail("config", "cannot locate configuration directory; set SIG_CONFIG_DIR")
		}
		dir = filepath.Join(base, "sig")
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return "", nil, fail("config", "cannot read configuration; check config.json and its permissions")
	}
	return dir, cfg, nil
}

func save(dir string, cfg *config.Config) error {
	if err := config.Save(dir, cfg); err != nil {
		return fail("config", "cannot save configuration; check directory permissions")
	}
	return nil
}

func selected(cmd *cli.Command, cfg *config.Config) (string, error) {
	name := cfg.CurrentContext
	if cmd.IsSet("context") {
		name = cmd.String("context")
	}
	if !config.ValidName(name) {
		return "", fail("usage", "context names must be 1-64 letters, digits, dots, underscores, or hyphens, starting with a letter or digit")
	}
	return name, nil
}

func (a *app) connect(cmd *cli.Command) (*signoz.Client, string, string, string, error) {
	_, cfg, err := a.load()
	if err != nil {
		return nil, "", "", "", err
	}
	name, err := selected(cmd, cfg)
	if err != nil {
		return nil, "", "", "", err
	}
	c, exists := cfg.Contexts[name]
	if cmd.IsSet("context") && !exists && (a.Getenv("SIGNOZ_URL") == "" || a.Getenv("SIGNOZ_API_KEY") == "") {
		return nil, "", "", "", fail("config", "context does not exist; run sig auth login --context NAME --url URL")
	}
	endpoint := c.URL
	if override := a.Getenv("SIGNOZ_URL"); override != "" {
		endpoint = override
	}
	if endpoint == "" {
		return nil, "", "", "", fail("config", "no endpoint configured; run sig auth login --url URL or set SIGNOZ_URL and SIGNOZ_API_KEY")
	}
	endpoint, err = signoz.NormalizeURL(endpoint)
	if err != nil {
		return nil, "", "", "", err
	}
	key, source := a.Getenv("SIGNOZ_API_KEY"), "environment"
	if key == "" {
		if endpoint != c.URL {
			return nil, "", "", "", fail("authentication", "SIGNOZ_URL changes the stored endpoint; supply SIGNOZ_API_KEY too, or log in to that endpoint")
		}
		if c.Credential == "" {
			return nil, "", "", "", fail("authentication", "no stored credential; run sig auth login or set SIGNOZ_API_KEY")
		}
		key, err = a.Credentials.Get(c.Credential)
		if errors.Is(err, config.ErrCredentialNotFound) {
			return nil, "", "", "", fail("authentication", "stored credential is missing; run sig auth login")
		}
		if err != nil {
			return nil, "", "", "", fail("credentials", "cannot access the OS keychain; unlock it or supply SIGNOZ_API_KEY")
		}
		source = "keychain"
	}
	client, err := signoz.New(endpoint, key, cmd.Duration("timeout"))
	return client, name, endpoint, source, err
}

func (a *app) status(ctx context.Context, cmd *cli.Command) error {
	if err := noArgs(cmd); err != nil {
		return err
	}
	client, name, endpoint, source, err := a.connect(cmd)
	if err != nil {
		return err
	}
	identity, err := client.Me(ctx)
	if err != nil {
		return err
	}
	return a.emit(map[string]any{"context": name, "url": endpoint, "authenticated": true, "credential_source": source, "identity": identity}, nil)
}

func (a *app) getContexts(_ context.Context, cmd *cli.Command) error {
	if err := noArgs(cmd); err != nil {
		return err
	}
	_, cfg, err := a.load()
	if err != nil {
		return err
	}
	type entry struct {
		Name                 string `json:"name"`
		URL                  string `json:"url"`
		Current              bool   `json:"current"`
		CredentialConfigured bool   `json:"credential_configured"`
	}
	names := make([]string, 0, len(cfg.Contexts))
	for name := range cfg.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]entry, 0, len(names))
	for _, name := range names {
		c := cfg.Contexts[name]
		entries = append(entries, entry{name, c.URL, name == cfg.CurrentContext, c.Credential != ""})
	}
	return a.emit(entries, nil)
}

func (a *app) currentContext(_ context.Context, cmd *cli.Command) error {
	if err := noArgs(cmd); err != nil {
		return err
	}
	_, cfg, err := a.load()
	if err != nil {
		return err
	}
	_, exists := cfg.Contexts[cfg.CurrentContext]
	return a.emit(map[string]any{"name": cfg.CurrentContext, "configured": exists}, nil)
}

func (a *app) useContext(_ context.Context, cmd *cli.Command) error {
	if cmd.NArg() != 1 {
		return fail("usage", "use-context requires one context name")
	}
	dir, cfg, err := a.load()
	if err != nil {
		return err
	}
	name := cmd.Args().First()
	if _, ok := cfg.Contexts[name]; !ok {
		return fail("config", "context does not exist")
	}
	cfg.CurrentContext = name
	if err := save(dir, cfg); err != nil {
		return err
	}
	return a.emit(map[string]string{"current_context": name}, nil)
}

func (a *app) deleteContext(_ context.Context, cmd *cli.Command) error {
	if cmd.NArg() != 1 {
		return fail("usage", "delete-context requires one context name")
	}
	dir, cfg, err := a.load()
	if err != nil {
		return err
	}
	name := cmd.Args().First()
	c, ok := cfg.Contexts[name]
	if !ok {
		return fail("config", "context does not exist")
	}
	if cfg.CurrentContext == name && len(cfg.Contexts) > 1 {
		return fail("config", "select another context before deleting the current context")
	}
	if c.Credential != "" {
		if err := a.Credentials.Delete(c.Credential); err != nil {
			return fail("credentials", "cannot remove credential from the OS keychain; context was not deleted")
		}
	}
	delete(cfg.Contexts, name)
	if len(cfg.Contexts) == 0 {
		cfg.CurrentContext = "default"
	}
	if err := save(dir, cfg); err != nil {
		return err
	}
	return a.emit(map[string]any{"context": name, "deleted": true}, nil)
}

func (a *app) logs(ctx context.Context, cmd *cli.Command) error {
	if err := noArgs(cmd); err != nil {
		return err
	}
	end := a.Now().UTC().Truncate(time.Millisecond)
	if cmd.IsSet("end") {
		parsed, err := time.Parse(time.RFC3339Nano, cmd.String("end"))
		if err != nil {
			return fail("usage", "--end must be an RFC3339 timestamp with a timezone")
		}
		end = parsed.UTC().Truncate(time.Millisecond)
	}
	if cmd.IsSet("start") && cmd.IsSet("since") {
		return fail("usage", "--start and --since are mutually exclusive")
	}
	if cmd.Duration("since") <= 0 {
		return fail("usage", "--since must be positive")
	}
	start := end.Add(-cmd.Duration("since")).Truncate(time.Millisecond)
	if cmd.IsSet("start") {
		parsed, err := time.Parse(time.RFC3339Nano, cmd.String("start"))
		if err != nil {
			return fail("usage", "--start must be an RFC3339 timestamp with a timezone")
		}
		start = parsed.UTC().Truncate(time.Millisecond)
	}
	if !start.Before(end) {
		return fail("usage", "start must precede end by at least one millisecond")
	}
	limit := cmd.Int("limit")
	if limit < 1 || limit > 10000 {
		return fail("usage", "--limit must be between 1 and 10000")
	}
	client, name, _, _, err := a.connect(cmd)
	if err != nil {
		return err
	}
	result, err := client.SearchLogs(ctx, signoz.LogOptions{Start: start, End: end, Limit: limit, Where: cmd.String("where")})
	if err != nil {
		return err
	}
	return a.emit(result.Rows, map[string]any{
		"schema_version": "1", "context": name, "signal": "logs", "start": start, "end": end,
		"returned": len(result.Rows), "limit": limit, "next_cursor": result.NextCursor,
		"completeness": result.Completeness, "warning": result.Warning,
	})
}
