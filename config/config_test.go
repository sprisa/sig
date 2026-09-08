package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestConfigRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sig")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentContext != "default" || len(cfg.Contexts) != 0 {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("loading defaults must not create files")
	}
	cfg.Contexts["default"] = Context{URL: "https://signoz.example.com", Credential: "opaque-reference"}
	if err := Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, cfg) {
		t.Fatalf("got %#v, want %#v", got, cfg)
	}
	if runtime.GOOS != "windows" {
		for path, want := range map[string]os.FileMode{dir: 0700, filepath.Join(dir, "config.json"): 0600} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != want {
				t.Errorf("%s mode: %o, want %o", path, info.Mode().Perm(), want)
			}
		}
	}
	cfg.Contexts["staging"] = Context{URL: "https://staging.example.com"}
	cfg.CurrentContext = "staging"
	if err := Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	got, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, cfg) {
		t.Fatal("replacement did not persist")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.json" {
		t.Fatal("temporary files were not cleaned up")
	}
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	for _, contents := range []string{
		`{`, `null`, `{}`, `{"current_context":"default","contexts":{}} {}`,
		`{"current_context":"default","contexts":{},"api_key":"synthetic-secret"}`,
		`{"current_context":"missing","contexts":{"default":{"url":"https://signoz.example.com"}}}`,
		`{"current_context":"../bad","contexts":{}}`, strings.Repeat(" ", (1<<20)+1),
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); err == nil {
			t.Error("expected invalid configuration to fail")
		}
	}
}

func TestContextNames(t *testing.T) {
	for _, name := range []string{"default", "staging-eu", "Work_1", "dev.local"} {
		if !ValidName(name) {
			t.Errorf("rejected %q", name)
		}
	}
	for _, name := range []string{"", "../secret", "-bad", "two words", strings.Repeat("a", 65)} {
		if ValidName(name) {
			t.Errorf("accepted %q", name)
		}
	}
}
