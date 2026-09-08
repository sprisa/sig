package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sprisa/sig/config"
	"github.com/sprisa/sig/signoz"
	"github.com/urfave/cli/v3"
	"golang.org/x/term"
)

func (a *app) login(ctx context.Context, cmd *cli.Command) error {
	if err := noArgs(cmd); err != nil {
		return err
	}
	dir, cfg, err := a.load()
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
		b, err := io.ReadAll(io.LimitReader(a.In, 65537))
		if err != nil || len(b) > 65536 {
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
	client, err := signoz.New(endpoint, key, cmd.Duration("timeout"))
	if err != nil {
		return err
	}
	if _, err := client.Me(ctx); err != nil {
		return err
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return fail("credentials", "could not generate credential identifier")
	}
	id := hex.EncodeToString(idBytes)
	if err := a.Credentials.Set(id, key); err != nil {
		return fail("credentials", "cannot store key in the OS keychain; use SIGNOZ_URL and SIGNOZ_API_KEY without login for headless operation")
	}
	if len(cfg.Contexts) == 0 {
		cfg.CurrentContext = name
	}
	cfg.Contexts[name] = config.Context{URL: endpoint, Credential: id}
	if err := save(dir, cfg); err != nil {
		if cleanupErr := a.Credentials.Delete(id); cleanupErr != nil {
			return fail("credentials", "configuration save failed and the new keychain entry could not be removed")
		}
		return err
	}
	if previous.Credential != "" {
		if err := a.Credentials.Delete(previous.Credential); err != nil {
			return fail("credentials", "login saved, but the previous keychain entry could not be removed")
		}
	}
	return a.emit(map[string]any{"context": name, "url": endpoint, "authenticated": true, "credential_source": "keychain"}, nil)
}

func (a *app) logout(_ context.Context, cmd *cli.Command) error {
	if err := noArgs(cmd); err != nil {
		return err
	}
	dir, cfg, err := a.load()
	if err != nil {
		return err
	}
	name, err := selected(cmd, cfg)
	if err != nil {
		return err
	}
	c, exists := cfg.Contexts[name]
	if exists && c.Credential != "" {
		if err := a.Credentials.Delete(c.Credential); err != nil {
			return fail("credentials", "cannot remove credential from the OS keychain")
		}
		c.Credential = ""
		cfg.Contexts[name] = c
		if err := save(dir, cfg); err != nil {
			return err
		}
	}
	return a.emit(map[string]any{"context": name, "credential_removed": true, "environment_key_present": a.Getenv("SIGNOZ_API_KEY") != "", "server_key_revoked": false}, nil)
}
