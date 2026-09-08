package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

type Context struct {
	URL        string `json:"url"`
	Credential string `json:"credential,omitempty"`
}

type Config struct {
	CurrentContext     string             `json:"current_context"`
	Contexts           map[string]Context `json:"contexts"`
	PendingCredentials []string           `json:"pending_credentials,omitempty"`
}

var contextName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

func ValidName(name string) bool { return contextName.MatchString(name) }

func Load(dir string) (*Config, error) {
	f, err := os.Open(filepath.Join(dir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return &Config{CurrentContext: "default", Contexts: map[string]Context{}}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 1<<20 {
		return nil, errors.New("configuration exceeds 1 MiB")
	}
	var c Config
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("unexpected trailing configuration data")
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) validate() error {
	if !ValidName(c.CurrentContext) || c.Contexts == nil {
		return errors.New("invalid context configuration")
	}
	if len(c.Contexts) > 0 {
		if _, ok := c.Contexts[c.CurrentContext]; !ok {
			return errors.New("current context does not exist")
		}
	}
	for name, ctx := range c.Contexts {
		if !ValidName(name) || ctx.URL == "" {
			return errors.New("invalid context configuration")
		}
	}
	seen := map[string]bool{}
	for _, id := range c.PendingCredentials {
		if id == "" || seen[id] {
			return errors.New("invalid pending credential references")
		}
		seen[id] = true
		for _, ctx := range c.Contexts {
			if ctx.Credential == id {
				return errors.New("pending credential is still active")
			}
		}
	}
	return nil
}

// Save replaces the file atomically so readers never observe a partial write.
func Save(dir string, c *Config) error {
	if err := c.validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if len(b)+1 > 1<<20 {
		return errors.New("configuration exceeds 1 MiB")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, "config.json"))
}
