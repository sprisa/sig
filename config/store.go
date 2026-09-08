package config

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/gofrs/flock"
)

type Error struct {
	Code, Message string
	Cause         error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }
func storeError(code, message string, cause error) error {
	return &Error{Code: code, Message: message, Cause: cause}
}

// Store owns read-modify-write operations across configuration and credentials.
// Pending references form a write-ahead cleanup journal, never storing secrets.
type Store struct {
	Dir         string
	Credentials Credentials
}

func (s Store) Load() (*Config, error) {
	c, err := Load(s.Dir)
	if err != nil {
		return nil, storeError("config", "cannot read configuration; check config.json and its permissions", err)
	}
	return c, nil
}

func (s Store) save(c *Config) error {
	if err := Save(s.Dir, c); err != nil {
		return storeError("config", "cannot save configuration; pending credential cleanup can be retried after storage is repaired", err)
	}
	return nil
}

func (s Store) mutate(ctx context.Context, update func(*Config) error) error {
	if s.Credentials == nil {
		return storeError("credentials", "credential storage is required for context mutations", nil)
	}
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return storeError("config", "cannot create configuration directory", err)
	}
	lock := flock.New(filepath.Join(s.Dir, "config.lock"), flock.SetPermissions(0600))
	defer lock.Close()
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(lockCtx, 20*time.Millisecond)
	if !locked || err != nil {
		if ctx.Err() != nil {
			return storeError("cancelled", "configuration update cancelled", ctx.Err())
		}
		return storeError("config", "configuration is busy or cannot be locked; retry the update", err)
	}
	c, err := s.Load()
	if err != nil {
		return err
	}
	if err := s.cleanup(c); err != nil {
		return err
	}
	return update(c)
}

func (s Store) cleanup(c *Config) error {
	if len(c.PendingCredentials) == 0 {
		return nil
	}
	for _, id := range c.PendingCredentials {
		if err := s.Credentials.Delete(id); err != nil {
			return storeError("credentials", "credential cleanup is pending; unlock the keychain and retry a context or credential update", err)
		}
	}
	c.PendingCredentials = nil
	return s.save(c)
}

// scheduleDelete is called after detaching a reference. Shared references in
// manually edited configurations remain usable until the last context detaches.
func (c *Config) scheduleDelete(id string) {
	if id == "" {
		return
	}
	for _, entry := range c.Contexts {
		if entry.Credential == id {
			return
		}
	}
	if !slices.Contains(c.PendingCredentials, id) {
		c.PendingCredentials = append(c.PendingCredentials, id)
	}
}

func (s Store) Replace(ctx context.Context, name, url, key string) error {
	if !ValidName(name) || url == "" || key == "" {
		return storeError("config", "invalid context or credential", nil)
	}
	return s.mutate(ctx, func(c *Config) error {
		idBytes := make([]byte, 16)
		if _, err := rand.Read(idBytes); err != nil {
			return storeError("credentials", "cannot create a credential reference", err)
		}
		id := hex.EncodeToString(idBytes)
		// Persist the reference before a keychain write can partially succeed.
		c.PendingCredentials = append(c.PendingCredentials, id)
		if err := s.save(c); err != nil {
			return err
		}
		if err := s.Credentials.Set(id, key); err != nil {
			_ = s.cleanup(c) // A failed cleanup remains journaled on disk.
			return storeError("credentials", "cannot store key in the OS keychain; use environment credentials for headless operation", err)
		}
		next := *c
		next.Contexts = maps.Clone(c.Contexts)
		next.PendingCredentials = slices.Delete(slices.Clone(c.PendingCredentials), len(c.PendingCredentials)-1, len(c.PendingCredentials))
		old := next.Contexts[name].Credential
		if len(next.Contexts) == 0 {
			next.CurrentContext = name
		}
		next.Contexts[name] = Context{URL: url, Credential: id}
		next.scheduleDelete(old)
		if err := s.save(&next); err != nil {
			_ = s.cleanup(c)
			return err
		}
		return s.cleanup(&next)
	})
}

func (s Store) Logout(ctx context.Context, name string) error {
	return s.mutate(ctx, func(c *Config) error {
		entry, ok := c.Contexts[name]
		if !ok || entry.Credential == "" {
			return nil
		}
		old := entry.Credential
		entry.Credential = ""
		c.Contexts[name] = entry
		c.scheduleDelete(old)
		if err := s.save(c); err != nil {
			return err
		}
		return s.cleanup(c)
	})
}

func (s Store) Use(ctx context.Context, name string) error {
	return s.mutate(ctx, func(c *Config) error {
		if _, ok := c.Contexts[name]; !ok {
			return storeError("config", "context does not exist", nil)
		}
		c.CurrentContext = name
		return s.save(c)
	})
}

func (s Store) Delete(ctx context.Context, name string) error {
	return s.mutate(ctx, func(c *Config) error {
		entry, ok := c.Contexts[name]
		if !ok {
			return storeError("config", "context does not exist", nil)
		}
		if c.CurrentContext == name && len(c.Contexts) > 1 {
			return storeError("config", "select another context before deleting the current context", nil)
		}
		delete(c.Contexts, name)
		if len(c.Contexts) == 0 {
			c.CurrentContext = "default"
		}
		c.scheduleDelete(entry.Credential)
		if err := s.save(c); err != nil {
			return err
		}
		return s.cleanup(c)
	})
}
