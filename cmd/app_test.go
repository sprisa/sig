package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sprisa/sig/config"
)

type memoryCredentials struct {
	keys     map[string]string
	fail     bool
	reads    int
	onSet    func()
	onDelete func(string) error
}

func (m *memoryCredentials) Get(id string) (string, error) {
	m.reads++
	if m.fail {
		return "", errors.New("synthetic keychain failure")
	}
	key, ok := m.keys[id]
	if !ok {
		return "", config.ErrCredentialNotFound
	}
	return key, nil
}

func (m *memoryCredentials) Set(id, key string) error {
	if m.fail {
		return errors.New("synthetic keychain failure")
	}
	m.keys[id] = key
	if m.onSet != nil {
		m.onSet()
	}
	return nil
}

func (m *memoryCredentials) Delete(id string) error {
	if m.onDelete != nil {
		if err := m.onDelete(id); err != nil {
			return err
		}
	}
	if m.fail {
		return errors.New("synthetic keychain failure")
	}
	delete(m.keys, id)
	return nil
}

func TestRotationFailureKeepsCleanupReference(t *testing.T) {
	h := newHarness(t)
	s := identityServer(t)
	h.run(t, "synthetic-api-key", 0, "auth", "login", "--url", s.URL, "--key-stdin")
	cfg, err := config.Load(h.opts.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	old := cfg.Contexts["default"].Credential
	h.keys.onDelete = func(id string) error {
		if id == old {
			return errors.New("synthetic delete failure")
		}
		return nil
	}
	h.run(t, "synthetic-api-key", 7, "auth", "login", "--key-stdin")
	data, err := os.ReadFile(filepath.Join(h.opts.ConfigDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(old)) {
		t.Fatal("failed cleanup lost the old credential reference")
	}
	h.keys.onDelete = nil
	h.run(t, "", 0, "auth", "logout")
	if len(h.keys.keys) != 0 {
		t.Fatal("retry did not clean up all credentials")
	}
}

type harness struct {
	opts Options
	env  map[string]string
	keys *memoryCredentials
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{env: map[string]string{}, keys: &memoryCredentials{keys: map[string]string{}}}
	h.opts = Options{
		ConfigDir: filepath.Join(t.TempDir(), "sig"), Credentials: h.keys,
		Getenv: func(key string) string { return h.env[key] },
		Now:    func() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) },
	}
	return h
}

func (h *harness) run(t *testing.T, input string, wantExit int, args ...string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	opts := h.opts
	opts.In, opts.Out, opts.ErrOut = strings.NewReader(input), &out, &errOut
	code := Run(context.Background(), append([]string{"sig"}, args...), opts)
	if code != wantExit {
		t.Fatalf("%v exited %d, want %d; stdout=%s stderr=%s", args, code, wantExit, out.String(), errOut.String())
	}
	if code == 0 {
		if errOut.Len() != 0 {
			t.Fatalf("unexpected stderr: %s", errOut.String())
		}
		if !json.Valid(out.Bytes()) {
			t.Fatalf("output is not JSON: %s", out.String())
		}
		return out.String()
	}
	if out.Len() != 0 {
		t.Fatalf("error contaminated stdout: %s", out.String())
	}
	if !json.Valid(errOut.Bytes()) {
		t.Fatalf("error is not JSON: %s", errOut.String())
	}
	if strings.Contains(errOut.String(), "synthetic-api-key") {
		t.Fatal("error leaked key")
	}
	return errOut.String()
}

func identityServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("SIGNOZ-API-KEY") != "synthetic-api-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api/v1/service_accounts/me" || r.Method != "GET" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"success","data":{"id":"account-1","name":"cli-reader"}}`)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestDefaultLoginStatusLogout(t *testing.T) {
	h := newHarness(t)
	s := identityServer(t)
	got := h.run(t, "synthetic-api-key\n", 0, "auth", "login", "--url", s.URL, "--key-stdin")
	if !strings.Contains(got, `"context":"default"`) {
		t.Fatal(got)
	}
	if strings.Contains(got, "synthetic-api-key") {
		t.Fatal("login leaked key")
	}
	b, err := os.ReadFile(filepath.Join(h.opts.ConfigDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "synthetic-api-key") {
		t.Fatal("key written to config")
	}
	if len(h.keys.keys) != 1 {
		t.Fatal("key not stored")
	}
	got = h.run(t, "", 0, "auth", "status")
	if !strings.Contains(got, `"credential_source":"keychain"`) || !strings.Contains(got, "cli-reader") {
		t.Fatal(got)
	}
	h.run(t, "", 0, "auth", "logout")
	if len(h.keys.keys) != 0 {
		t.Fatal("logout retained key")
	}
	cfg, err := config.Load(h.opts.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Contexts["default"].URL != s.URL || cfg.Contexts["default"].Credential != "" {
		t.Fatal("logout did not preserve context without credential")
	}
	h.run(t, "", 3, "auth", "status")
	h.run(t, "", 0, "auth", "logout")
}

func TestContextSelection(t *testing.T) {
	h := newHarness(t)
	s := identityServer(t)
	h.env["SIGNOZ_API_KEY"] = "synthetic-api-key"
	h.run(t, "", 0, "auth", "login", "--url", s.URL)
	h.run(t, "", 0, "auth", "login", "--context", "staging", "--url", s.URL)
	got := h.run(t, "", 0, "config", "current-context")
	if !strings.Contains(got, `"name":"default"`) {
		t.Fatal("named login changed current context")
	}
	delete(h.env, "SIGNOZ_API_KEY")
	got = h.run(t, "", 0, "--context", "staging", "auth", "status")
	if !strings.Contains(got, `"context":"staging"`) {
		t.Fatal(got)
	}
	h.run(t, "", 0, "config", "use-context", "staging")
	got = h.run(t, "", 0, "auth", "status")
	if !strings.Contains(got, `"context":"staging"`) {
		t.Fatal(got)
	}
	got = h.run(t, "", 0, "config", "get-contexts")
	if strings.Contains(got, "synthetic-api-key") || strings.Contains(got, `"credential":`) {
		t.Fatal("listed credentials")
	}
	h.run(t, "", 7, "config", "delete-context", "staging")
	h.run(t, "", 0, "config", "delete-context", "default")
	h.run(t, "", 0, "config", "delete-context", "staging")
	if len(h.keys.keys) != 0 {
		t.Fatal("deletion retained credentials")
	}
	got = h.run(t, "", 0, "config", "current-context")
	if !strings.Contains(got, `"name":"default"`) || !strings.Contains(got, `"configured":false`) {
		t.Fatal(got)
	}
}

func TestEnvironmentOnlyAndOverrides(t *testing.T) {
	h := newHarness(t)
	s := identityServer(t)
	h.env["SIGNOZ_URL"], h.env["SIGNOZ_API_KEY"] = s.URL, "synthetic-api-key"
	h.keys.fail = true
	got := h.run(t, "", 0, "auth", "status")
	if !strings.Contains(got, `"credential_source":"environment"`) {
		t.Fatal(got)
	}
	if h.keys.reads != 0 {
		t.Fatal("environment auth accessed keychain")
	}
	if _, err := os.Stat(h.opts.ConfigDir); !os.IsNotExist(err) {
		t.Fatal("environment auth wrote config")
	}
	h.keys.fail = false
	h.run(t, "", 0, "auth", "login")
	delete(h.env, "SIGNOZ_API_KEY")
	h.env["SIGNOZ_URL"] = "https://other.example.com"
	got = h.run(t, "", 3, "auth", "status")
	if !strings.Contains(got, "supply SIGNOZ_API_KEY too") {
		t.Fatal("stored key was not bound to endpoint")
	}
	h.env["SIGNOZ_API_KEY"] = "synthetic-api-key"
	h.env["SIGNOZ_URL"] = s.URL
	h.keys.fail = true
	h.run(t, "", 0, "auth", "status")
}

func TestLoginFailuresPreserveCredentials(t *testing.T) {
	h := newHarness(t)
	s := identityServer(t)
	h.run(t, "synthetic-api-key", 0, "auth", "login", "--url", s.URL, "--key-stdin")
	original, err := os.ReadFile(filepath.Join(h.opts.ConfigDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	h.run(t, "invalid-key", 3, "auth", "login", "--key-stdin")
	h.keys.fail = true
	h.run(t, "synthetic-api-key", 7, "auth", "login", "--key-stdin")
	after, err := os.ReadFile(filepath.Join(h.opts.ConfigDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var beforeConfig, afterConfig config.Config
	if json.Unmarshal(original, &beforeConfig) != nil || json.Unmarshal(after, &afterConfig) != nil {
		t.Fatal("invalid configuration")
	}
	if beforeConfig.Contexts["default"] != afterConfig.Contexts["default"] || len(h.keys.keys) != 1 || len(afterConfig.PendingCredentials) != 1 {
		t.Fatal("failed login did not preserve the active credential and pending cleanup")
	}
	h.keys.fail = false
	h.run(t, "synthetic-api-key", 0, "auth", "login", "--key-stdin")
	if len(h.keys.keys) != 1 {
		t.Fatal("successful replacement left old key")
	}
}

func TestNoninteractiveAndUsageErrors(t *testing.T) {
	h := newHarness(t)
	h.run(t, "", 3, "auth", "login", "--url", "https://signoz.example.com")
	h.run(t, "", 7, "auth", "status")
	for _, args := range [][]string{
		{"missing"}, {"auth", "missing"}, {"--api-key", "synthetic-api-key"},
		{"logs", "search", "--limit", "bad"}, {"logs", "search", "--limit", "0"},
		{"logs", "search", "--limit", "10001"}, {"logs", "search", "--since", "0s"},
		{"logs", "search", "--since", "-1s"}, {"logs", "search", "--start", "not-a-time"},
		{"logs", "search", "--end", "not-a-time"},
		{"logs", "search", "--start", "2026-01-01T00:00:00Z", "--since", "1h"},
		{"logs", "search", "--start", "2026-01-02T00:00:00Z"},
		{"auth", "login", "--context", "../bad"}, {"config", "use-context"},
		{"config", "delete-context"}, {"auth", "status", "extra"},
	} {
		h.run(t, "", 2, args...)
	}
}

func TestLogsEndToEnd(t *testing.T) {
	h := newHarness(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v5/query_range" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body struct{ Start, End int64 }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.End != h.opts.Now().UnixMilli() || body.Start != h.opts.Now().Add(-15*time.Minute).UnixMilli() {
			t.Errorf("unexpected default range: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":[{"data":{"body":"Synthetic event","sequence":9007199254740993}}],"nextCursor":""}]}}}`)
	}))
	defer s.Close()
	h.env["SIGNOZ_URL"], h.env["SIGNOZ_API_KEY"] = s.URL, "synthetic-api-key"
	got := h.run(t, "", 0, "logs", "search")
	if !strings.Contains(got, "9007199254740993") || !strings.Contains(got, `"completeness":"complete"`) || !strings.Contains(got, `"returned":1`) {
		t.Fatal(got)
	}
}

func TestHelpAndVersion(t *testing.T) {
	h := newHarness(t)
	h.run(t, "", 0, "version")
	for _, args := range [][]string{{"sig", "--help"}, {"sig", "logs", "search", "--help"}, {"sig"}} {
		var out, errOut bytes.Buffer
		opts := h.opts
		opts.Out, opts.ErrOut = &out, &errOut
		if code := Run(context.Background(), args, opts); code != 0 {
			t.Fatalf("help exited %d", code)
		}
		if out.Len() == 0 || errOut.Len() != 0 {
			t.Fatal("unexpected help output")
		}
	}
}

func TestLoginSaveFailureRollsBackNewCredential(t *testing.T) {
	h := newHarness(t)
	s := identityServer(t)
	h.keys.onSet = func() {
		// Block atomic replacement after login has validated and stored its key.
		if err := os.Remove(filepath.Join(h.opts.ConfigDir, "config.json")); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(h.opts.ConfigDir, "config.json"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	h.run(t, "synthetic-api-key", 7, "auth", "login", "--url", s.URL, "--key-stdin")
	if len(h.keys.keys) != 0 {
		t.Fatal("failed configuration save left a new credential behind")
	}
}

func TestCredentialRemovalFailurePreservesCleanupReference(t *testing.T) {
	for _, command := range [][]string{{"auth", "logout"}, {"config", "delete-context", "default"}} {
		h := newHarness(t)
		s := identityServer(t)
		h.run(t, "synthetic-api-key", 0, "auth", "login", "--url", s.URL, "--key-stdin")
		before, err := os.ReadFile(filepath.Join(h.opts.ConfigDir, "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		h.keys.fail = true
		h.run(t, "", 7, command...)
		after, err := os.ReadFile(filepath.Join(h.opts.ConfigDir, "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		var initial, failed config.Config
		if json.Unmarshal(before, &initial) != nil || json.Unmarshal(after, &failed) != nil {
			t.Fatal("invalid configuration")
		}
		if len(failed.PendingCredentials) != 1 || failed.PendingCredentials[0] != initial.Contexts["default"].Credential || len(h.keys.keys) != 1 {
			t.Fatal("failed deletion lost cleanup state")
		}
		h.keys.fail = false
		h.run(t, "", 0, "auth", "logout")
		if len(h.keys.keys) != 0 {
			t.Fatal("pending deletion was not retried")
		}
	}
}

func TestMissingKeychainEntry(t *testing.T) {
	h := newHarness(t)
	s := identityServer(t)
	h.run(t, "synthetic-api-key", 0, "auth", "login", "--url", s.URL, "--key-stdin")
	clear(h.keys.keys)
	got := h.run(t, "", 3, "auth", "status")
	if !strings.Contains(got, "stored credential is missing") {
		t.Fatal("missing keychain entry was not distinguished from storage failure")
	}
}

func TestExplicitWindowNormalization(t *testing.T) {
	h := newHarness(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Start, End int64 }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		start := time.Date(2026, 1, 1, 12, 0, 0, 123000000, time.UTC)
		if body.Start != start.UnixMilli() || body.End != start.Add(time.Hour).UnixMilli() {
			t.Error("timezone offset or fractional timestamps were not normalized")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":[]}]}}}`)
	}))
	defer s.Close()
	h.env["SIGNOZ_URL"], h.env["SIGNOZ_API_KEY"] = s.URL, "synthetic-api-key"
	h.run(t, "", 0, "logs", "search", "--start", "2026-01-01T05:00:00.123456789-07:00", "--end", "2026-01-01T06:00:00.123456789-07:00")
	h.run(t, "", 0, "logs", "search", "--since", "1h", "--end", "2026-01-01T13:00:00.123456789Z")
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("synthetic write failure") }

func TestBrokenOutputReturnsStructuredError(t *testing.T) {
	h := newHarness(t)
	var stderr bytes.Buffer
	opts := h.opts
	opts.Out, opts.ErrOut = brokenWriter{}, &stderr
	if code := Run(context.Background(), []string{"sig", "version"}, opts); code != 6 {
		t.Fatalf("output failure exited %d, want 6", code)
	}
	if !json.Valid(stderr.Bytes()) || !strings.Contains(stderr.String(), `"code":"output"`) {
		t.Fatal("output failure did not return a structured error")
	}
}
