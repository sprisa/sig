package cmd

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/sprisa/sig/config"
	"github.com/sprisa/sig/signoz"
	"github.com/urfave/cli/v3"
)

type connection struct {
	Client                              *signoz.Client
	Context, Endpoint, CredentialSource string
}

func (a *app) store() (config.Store, error) {
	dir := a.ConfigDir
	if dir == "" {
		dir = a.Getenv("SIG_CONFIG_DIR")
	}
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return config.Store{}, fail("config", "cannot locate configuration directory; set SIG_CONFIG_DIR")
		}
		dir = filepath.Join(base, "sig")
	}
	return config.Store{Dir: dir, Credentials: a.Credentials}, nil
}

func selected(c *cli.Command, cfg *config.Config) (string, error) {
	name := cfg.CurrentContext
	if c.IsSet("context") {
		name = c.String("context")
	}
	if !config.ValidName(name) {
		return "", fail("usage", "context names must be 1-64 letters, digits, dots, underscores, or hyphens, starting with a letter or digit")
	}
	return name, nil
}

func (a *app) connect(c *cli.Command) (connection, error) {
	store, err := a.store()
	if err != nil {
		return connection{}, err
	}
	cfg, err := store.Load()
	if err != nil {
		return connection{}, err
	}
	name, err := selected(c, cfg)
	if err != nil {
		return connection{}, err
	}
	entry, exists := cfg.Contexts[name]
	urlOverride, key := a.Getenv("SIGNOZ_URL"), a.Getenv("SIGNOZ_API_KEY")
	if c.IsSet("context") && !exists && (urlOverride == "" || key == "") {
		return connection{}, fail("config", "context does not exist; run sig auth login --context NAME --url URL")
	}
	endpoint := entry.URL
	if urlOverride != "" {
		endpoint = urlOverride
	}
	if endpoint == "" {
		return connection{}, fail("config", "no endpoint configured; run sig auth login --url URL or set SIGNOZ_URL and SIGNOZ_API_KEY")
	}
	endpoint, err = signoz.NormalizeURL(endpoint)
	if err != nil {
		return connection{}, err
	}
	source := "environment"
	if key == "" {
		if endpoint != entry.URL {
			return connection{}, fail("authentication", "SIGNOZ_URL changes the stored endpoint; supply SIGNOZ_API_KEY too, or log in to that endpoint")
		}
		if entry.Credential == "" {
			return connection{}, fail("authentication", "no stored credential; run sig auth login or set SIGNOZ_API_KEY")
		}
		key, err = a.Credentials.Get(entry.Credential)
		if errors.Is(err, config.ErrCredentialNotFound) {
			return connection{}, fail("authentication", "stored credential is missing; run sig auth login")
		}
		if err != nil {
			return connection{}, fail("credentials", "cannot access the OS keychain; unlock it or supply SIGNOZ_API_KEY")
		}
		source = "keychain"
	}
	client, err := signoz.New(endpoint, key, c.Duration("timeout"))
	return connection{Client: client, Context: name, Endpoint: endpoint, CredentialSource: source}, err
}
