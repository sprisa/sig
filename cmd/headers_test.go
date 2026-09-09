package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCustomHeadersWithLoginAndKeychain(t *testing.T) {
	h := newHarness(t)
	const secret = "synthetic-proxy-secret"
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("CF-Access-Client-Secret") != secret || r.Header.Get("Authorization") != "Bearer synthetic:token" {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, secret)
			return
		}
		if r.Header.Get("SIGNOZ-API-KEY") != "synthetic-api-key" {
			t.Error("proxy auth replaced service-account auth")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			io.WriteString(w, `{"status":"success","data":{"id":"synthetic"}}`)
			return
		}
		io.WriteString(w, `{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":[]}]}}}`)
	}))
	defer s.Close()
	h.env["SIGNOZ_URL"], h.env["SIGNOZ_API_KEY"] = s.URL, "synthetic-api-key"
	h.env["SIGNOZ_CUSTOM_HEADERS"] = "CF-Access-Client-Secret:" + secret + ",Authorization:Bearer synthetic:token"
	for _, args := range [][]string{{"auth", "login"}, {"auth", "status"}, {"logs", "search"}, {"config", "get-contexts"}} {
		if got := h.run(t, "", 0, args...); strings.Contains(got, secret) {
			t.Fatal("custom header exposed in output")
		}
	}
	data, err := os.ReadFile(filepath.Join(h.opts.ConfigDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || strings.Contains(string(data), "CUSTOM_HEADERS") || strings.Contains(string(data), "CF-Access") {
		t.Fatal("proxy headers persisted in config")
	}
	for _, value := range h.keys.keys {
		if value != "synthetic-api-key" {
			t.Fatal("proxy headers persisted in keychain")
		}
	}
	delete(h.env, "SIGNOZ_API_KEY")
	if got := h.run(t, "", 0, "auth", "status"); !strings.Contains(got, `"credential_source":"keychain"`) {
		t.Fatal("headers do not work with stored API key")
	}
	delete(h.env, "SIGNOZ_CUSTOM_HEADERS")
	if got := h.run(t, "", 4, "auth", "status"); strings.Contains(got, secret) {
		t.Fatal("proxy response body exposed")
	}
	h.env["SIGNOZ_CUSTOM_HEADERS"] = "X-Test:" + secret + ",invalid-" + secret
	before := calls.Load()
	for _, args := range [][]string{{"auth", "status"}, {"logs", "search"}} {
		if got := h.run(t, "", 2, args...); strings.Contains(got, secret) {
			t.Fatal("parse error exposed input")
		}
	}
	if calls.Load() != before {
		t.Fatal("invalid custom headers reached the API")
	}
}

func TestCustomHeadersDoNotBypassEndpointBinding(t *testing.T) {
	h := newHarness(t)
	s := identityServer(t)
	h.run(t, "synthetic-api-key", 0, "auth", "login", "--url", s.URL, "--key-stdin")
	var reached atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Store(true) }))
	defer other.Close()
	h.env["SIGNOZ_URL"] = other.URL
	h.env["SIGNOZ_CUSTOM_HEADERS"] = "Authorization:Bearer synthetic-proxy-secret"
	h.run(t, "", 3, "auth", "status")
	if reached.Load() {
		t.Fatal("custom headers bypassed stored-key endpoint binding")
	}
}

func TestInvalidCustomHeadersDoNotChangeLoginState(t *testing.T) {
	h := newHarness(t)
	s := identityServer(t)
	h.run(t, "synthetic-api-key", 0, "auth", "login", "--url", s.URL, "--key-stdin")
	before, err := os.ReadFile(filepath.Join(h.opts.ConfigDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	h.env["SIGNOZ_CUSTOM_HEADERS"] = "SIGNOZ-API-KEY:synthetic-proxy-secret"
	got := h.run(t, "replacement-api-key", 2, "auth", "login", "--key-stdin")
	if strings.Contains(got, "synthetic-proxy-secret") {
		t.Fatal("invalid header exposed")
	}
	after, err := os.ReadFile(filepath.Join(h.opts.ConfigDir, "config.json"))
	if err != nil || string(before) != string(after) {
		t.Fatal("invalid headers modified configuration")
	}
	if len(h.keys.keys) != 1 {
		t.Fatal("invalid headers changed credentials")
	}
	for _, key := range h.keys.keys {
		if key != "synthetic-api-key" {
			t.Fatal("invalid headers replaced a key")
		}
	}
}

func TestCustomHeadersDoNotAffectOfflineCommands(t *testing.T) {
	h := newHarness(t)
	h.env["SIGNOZ_CUSTOM_HEADERS"] = "invalid-synthetic-proxy-secret"
	for _, args := range [][]string{{"version"}, {"agent", "schema"}, {"agent", "recipes", "workflow"}, {"config", "get-contexts"}} {
		got := h.run(t, "", 0, args...)
		if strings.Contains(got, "invalid-synthetic-proxy-secret") {
			t.Fatal("offline command exposed environment value")
		}
		if args[0] == "agent" && args[1] == "schema" && !strings.Contains(got, "SIGNOZ_CUSTOM_HEADERS") {
			t.Fatal("schema omitted environment name")
		}
	}
}
