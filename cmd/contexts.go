package cmd

import (
	"context"
	"sort"

	"github.com/urfave/cli/v3"
)

func (a *app) contextCommand() *cli.Command {
	return &cli.Command{Name: "config", Usage: "Manage optional named contexts", Commands: []*cli.Command{
		operation(&cli.Command{Name: "get-contexts", Usage: "List configured contexts (never credentials)", Action: a.getContexts}, commandPolicy{Effect: "local_read", Result: "array"}),
		operation(&cli.Command{Name: "current-context", Usage: "Show the configured current context", Action: a.currentContext}, commandPolicy{Effect: "local_read", Result: "object"}),
		operation(&cli.Command{Name: "use-context", Usage: "Select a context", ArgsUsage: "NAME", Action: a.useContext}, commandPolicy{Effect: "local_write", Result: "object"}),
		operation(&cli.Command{Name: "delete-context", Usage: "Delete a context and its stored credential", ArgsUsage: "NAME", Action: a.deleteContext}, commandPolicy{Effect: "local_write", Result: "object"}),
	}}
}

func (a *app) getContexts(_ context.Context, c *cli.Command) error {
	if err := noArgs(c); err != nil {
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
		v := cfg.Contexts[name]
		entries = append(entries, entry{name, v.URL, name == cfg.CurrentContext, v.Credential != ""})
	}
	return a.emit(entries, nil)
}

func (a *app) currentContext(_ context.Context, c *cli.Command) error {
	if err := noArgs(c); err != nil {
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
	_, exists := cfg.Contexts[cfg.CurrentContext]
	return a.emit(map[string]any{"name": cfg.CurrentContext, "configured": exists}, nil)
}

func (a *app) useContext(ctx context.Context, c *cli.Command) error {
	if c.NArg() != 1 {
		return fail("usage", "use-context requires one context name")
	}
	store, err := a.store()
	if err != nil {
		return err
	}
	name := c.Args().First()
	if err := store.Use(ctx, name); err != nil {
		return err
	}
	return a.emit(map[string]string{"current_context": name}, nil)
}

func (a *app) deleteContext(ctx context.Context, c *cli.Command) error {
	if c.NArg() != 1 {
		return fail("usage", "delete-context requires one context name")
	}
	store, err := a.store()
	if err != nil {
		return err
	}
	name := c.Args().First()
	if err := store.Delete(ctx, name); err != nil {
		return err
	}
	return a.emit(map[string]any{"context": name, "deleted": true}, nil)
}
