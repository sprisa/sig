package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

type testCredentials struct {
	mu         sync.Mutex
	keys       map[string]string
	failDelete bool
}

func (s *testCredentials) Get(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.keys[id]
	if !ok {
		return "", ErrCredentialNotFound
	}
	return v, nil
}
func (s *testCredentials) Set(id, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[id] = key
	return nil
}
func (s *testCredentials) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failDelete {
		return errors.New("synthetic delete failure")
	}
	delete(s.keys, id)
	return nil
}

func TestConcurrentContextUpdates(t *testing.T) {
	keys := &testCredentials{keys: map[string]string{}}
	dir := t.TempDir()
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := Store{Dir: dir, Credentials: keys}
			errors <- s.Replace(context.Background(), fmt.Sprintf("context-%d", i), "https://signoz.example.com", "synthetic-key")
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Contexts) != 8 || len(cfg.PendingCredentials) != 0 || len(keys.keys) != 8 {
		t.Fatal("concurrent updates lost contexts or left credentials behind")
	}
}

func TestPreparedCredentialRecovery(t *testing.T) {
	for _, stored := range []bool{false, true} {
		dir := t.TempDir()
		keys := &testCredentials{keys: map[string]string{}}
		if stored {
			keys.keys["prepared"] = "synthetic-key"
		}
		cfg := &Config{CurrentContext: "default", Contexts: map[string]Context{}, PendingCredentials: []string{"prepared"}}
		if err := Save(dir, cfg); err != nil {
			t.Fatal(err)
		}
		s := Store{Dir: dir, Credentials: keys}
		if err := s.Logout(context.Background(), "default"); err != nil {
			t.Fatal(err)
		}
		got, err := s.Load()
		if err != nil {
			t.Fatal(err)
		}
		if len(got.PendingCredentials) != 0 || len(keys.keys) != 0 {
			t.Fatal("interrupted preparation was not recovered")
		}
	}
}

func TestDetachedCredentialRecovery(t *testing.T) {
	keys := &testCredentials{keys: map[string]string{}}
	s := Store{Dir: t.TempDir(), Credentials: keys}
	if err := s.Replace(context.Background(), "default", "https://signoz.example.com", "synthetic-key"); err != nil {
		t.Fatal(err)
	}
	keys.failDelete = true
	if err := s.Logout(context.Background(), "default"); err == nil {
		t.Fatal("expected cleanup failure")
	}
	cfg, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Contexts["default"].Credential != "" || len(cfg.PendingCredentials) != 1 {
		t.Fatal("logout did not detach and journal the credential")
	}
	keys.failDelete = false
	if err := s.Logout(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	if len(keys.keys) != 0 {
		t.Fatal("retry left a credential behind")
	}
}

func TestSharedCredentialIsNotDeletedEarly(t *testing.T) {
	keys := &testCredentials{keys: map[string]string{"shared": "synthetic-key"}}
	s := Store{Dir: t.TempDir(), Credentials: keys}
	cfg := &Config{CurrentContext: "default", Contexts: map[string]Context{"default": {URL: "https://signoz.example.com", Credential: "shared"}, "second": {URL: "https://signoz.example.com", Credential: "shared"}}}
	if err := Save(s.Dir, cfg); err != nil {
		t.Fatal(err)
	}
	if err := s.Logout(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.Get("shared"); err != nil {
		t.Fatal("removed another context's credential")
	}
	if err := s.Delete(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.Get("shared"); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatal("last detached reference was not removed")
	}
}

func TestStoreLockAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{CurrentContext: "default", Contexts: map[string]Context{"default": {URL: "https://signoz.example.com"}, "second": {URL: "https://second.example.com"}}}
	if err := Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(filepath.Join(dir, "config.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	child := exec.Command(os.Args[0], "-test.run=^TestStoreLockChild$")
	child.Env = append(os.Environ(), "SIG_STORE_LOCK_TEST="+dir)
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("child failed: %v\n%s", err, out)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentContext != "default" {
		t.Fatal("child bypassed the configuration lock")
	}
}

func TestStoreLockChild(t *testing.T) {
	dir := os.Getenv("SIG_STORE_LOCK_TEST")
	if dir == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := (Store{Dir: dir, Credentials: &testCredentials{keys: map[string]string{}}}).Use(ctx, "second")
	var e *Error
	if !errors.As(err, &e) || e.Code != "cancelled" {
		t.Fatal("update did not respect the lock and context deadline")
	}
}
