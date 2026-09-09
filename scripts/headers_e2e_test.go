//go:build e2e

package scripts

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Exercises the independent direct-API and compiled-CLI sides of the live
// harness behind a synthetic authenticating proxy; no live credentials needed.
func TestProxyHeaderParity(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("SIGNOZ-API-KEY") != "synthetic-key" || r.Header.Get("Authorization") != "Bearer synthetic:proxy" || r.Header.Get("CF-Access-Client-Secret") != "synthetic-secret" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"success","data":{"id":"synthetic-identity"}}`)
	}))
	defer s.Close()
	t.Setenv("SIGNOZ_URL", s.URL)
	t.Setenv("SIGNOZ_API_KEY", "synthetic-key")
	t.Setenv("SIGNOZ_CUSTOM_HEADERS", "Authorization:Bearer synthetic:proxy,CF-Access-Client-Secret:synthetic-secret")
	t.Setenv("SIG_E2E_START", "")
	t.Setenv("SIG_E2E_END", "")
	h := newLiveHarness(t)
	raw := h.api(t, "GET", "/api/v1/service_accounts/me", h.key, nil, 200)
	got := h.cli(t, h.key, 0, "auth", "status")
	requireEqual(t, requireField(t, got.Data, "identity"), raw.Data)
	if calls.Load() != 2 {
		t.Fatal("expected direct and CLI requests through the proxy")
	}
}
