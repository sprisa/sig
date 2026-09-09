package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/sprisa/sig/signoz"
	"github.com/urfave/cli/v3"
	"golang.org/x/term"
)

func (a *app) authCommand() *cli.Command {
	return &cli.Command{Name: "auth", Usage: "Manage local service-account credentials", Commands: []*cli.Command{
		operation(&cli.Command{Name: "login", Usage: "Validate and securely store a service-account key", Flags: []cli.Flag{&cli.StringFlag{Name: "url", Usage: "Reachable SigNoz base URL"}, &cli.BoolFlag{Name: "key-stdin", Usage: "Read the API key from stdin instead of prompting"}}, Action: a.login}, commandPolicy{Effect: "local_write", Authentication: true, Result: "object"}),
		operation(&cli.Command{Name: "status", Usage: "Validate the effective credential and report identity", Action: a.status}, commandPolicy{Effect: "read", Authentication: true, Result: "object"}),
		operation(&cli.Command{Name: "logout", Usage: "Remove the stored credential, without revoking the server key", Action: a.logout}, commandPolicy{Effect: "local_write", Result: "object"}),
	}}
}

func (a *app) login(ctx context.Context, cmd *cli.Command) error {
	if err := noArgs(cmd); err != nil {
		return err
	}
	store, err := a.store()
	if err != nil {
		return err
	}
	cfg, err := store.Load()
	if err != nil {
		return err
	}
	name, err := selected(cmd, cfg)
	if err != nil {
		return err
	}
	previous := cfg.Contexts[name]
	endpoint := previous.URL
	if value := a.Getenv("SIGNOZ_URL"); value != "" {
		endpoint = value
	}
	if cmd.IsSet("url") {
		endpoint = cmd.String("url")
	}
	if endpoint == "" {
		return fail("usage", "provide --url or SIGNOZ_URL for the first login")
	}
	endpoint, err = signoz.NormalizeURL(endpoint)
	if err != nil {
		return err
	}
	if cmd.Duration("timeout") <= 0 {
		return fail("usage", "--timeout must be positive")
	}
	key := a.Getenv("SIGNOZ_API_KEY")
	if cmd.Bool("key-stdin") {
		b, err := readBoundedInput(ctx, a.In, 65536)
		if err != nil {
			var inputErr *signoz.Error
			if errors.As(err, &inputErr) && inputErr.Code == "cancelled" {
				return err
			}
			return fail("credentials", "could not read API key from stdin (maximum 64 KiB)")
		}
		key = strings.TrimSpace(string(b))
	} else if key == "" {
		f, ok := a.In.(*os.File)
		if !ok || !term.IsTerminal(int(f.Fd())) {
			return fail("authentication", "noninteractive login requires --key-stdin or SIGNOZ_API_KEY")
		}
		value, err := readTerminalKey(ctx, f, a.ErrOut)
		_, _ = fmt.Fprintln(a.ErrOut)
		if err != nil {
			return err
		}
		key = strings.TrimSpace(value)
	}
	client, err := signoz.NewWithHeaders(endpoint, key, cmd.Duration("timeout"), a.Getenv("SIGNOZ_CUSTOM_HEADERS"))
	if err != nil {
		return err
	}
	if _, err := client.Me(ctx); err != nil {
		return err
	}
	if err := store.Replace(ctx, name, endpoint, key); err != nil {
		return err
	}
	return a.emit(map[string]any{"context": name, "url": endpoint, "authenticated": true, "credential_source": "keychain"}, nil)
}

func (a *app) logout(ctx context.Context, cmd *cli.Command) error {
	if err := noArgs(cmd); err != nil {
		return err
	}
	store, err := a.store()
	if err != nil {
		return err
	}
	cfg, err := store.Load()
	if err != nil {
		return err
	}
	name, err := selected(cmd, cfg)
	if err != nil {
		return err
	}
	if err := store.Logout(ctx, name); err != nil {
		return err
	}
	return a.emit(map[string]any{"context": name, "credential_removed": true, "environment_key_present": a.Getenv("SIGNOZ_API_KEY") != "", "server_key_revoked": false}, nil)
}

func (a *app) status(ctx context.Context, c *cli.Command) error {
	if err := noArgs(c); err != nil {
		return err
	}
	conn, err := a.connect(c)
	if err != nil {
		return err
	}
	identity, err := conn.Client.Me(ctx)
	if err != nil {
		return err
	}
	return a.emit(map[string]any{"context": conn.Context, "url": conn.Endpoint, "authenticated": true, "credential_source": conn.CredentialSource, "identity": identity.Raw}, nil)
}
