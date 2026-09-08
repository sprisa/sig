package signoz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := New(server.URL, "synthetic-api-key", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func assertCode(t *testing.T, err error, want string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != want {
		t.Fatalf("got %v, want error code %q", err, want)
	}
}

func TestNormalizeURL(t *testing.T) {
	for _, raw := range []string{"https://signoz.example.com", "https://signoz.example.com/base/", "http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		if _, err := NormalizeURL(raw); err != nil {
			t.Errorf("rejected %s: %v", raw, err)
		}
	}
	for _, raw := range []string{"", "/api", "signoz.example.com", "http://signoz.example.com", "https://user:secret@signoz.example.com", "https://signoz.example.com?token=secret", "https://signoz.example.com?", "https://signoz.example.com/#secret", "ftp://localhost", "https://%zz"} {
		if _, err := NormalizeURL(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	if _, err := New("https://signoz.example.com", "key\r\ninjected: header", time.Second); err == nil {
		t.Fatal("accepted invalid key")
	}
	if _, err := New("https://signoz.example.com", "key", 0); err == nil {
		t.Fatal("accepted unbounded timeout")
	}
}

func TestMeAndBasePath(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/observability/api/v1/service_accounts/me" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("SIGNOZ-API-KEY") != "synthetic-api-key" || r.Header.Get("Accept") != "application/json" {
			t.Error("missing API headers")
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("unexpected bearer credential")
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		io.WriteString(w, `{"status":"success","data":{"id":"account-1","name":"cli-reader"}}`)
	})
	c.base.Path = "/observability"
	got, err := c.Me(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"id":"account-1","name":"cli-reader"}` {
		t.Fatalf("unexpected identity: %s", got)
	}
}

func TestSearchLogsRequestAndPrecision(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v5/query_range" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("missing JSON content type")
		}
		var got any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		var want any
		json.Unmarshal([]byte(`{"schemaVersion":"v1","start":1000,"end":2000,"requestType":"raw","compositeQuery":{"queries":[{"type":"builder_query","spec":{"name":"A","signal":"logs","filter":{"expression":"service.name = 'checkout'"},"limit":100,"offset":0,"order":[{"key":{"name":"timestamp"},"direction":"desc"},{"key":{"name":"id"},"direction":"desc"}]}}]}}`), &want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("unexpected query: %#v", got)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":[{"timestamp":"2026-01-01T00:00:00Z","data":{"duration_nano":9007199254740993}}],"nextCursor":"opaque-cursor"}]},"warning":{"message":"Synthetic partial result"}}}`)
	})
	got, err := c.SearchLogs(context.Background(), SearchOptions{Start: time.UnixMilli(1000), End: time.UnixMilli(2000), Limit: 100, Where: "service.name = 'checkout'"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 1 || !strings.Contains(string(got.Rows[0]), "9007199254740993") {
		t.Fatalf("precision lost: %#v", got)
	}
	if got.Completeness != "more_available" || got.NextCursor != "opaque-cursor" || !strings.Contains(string(got.Warning), "Synthetic") {
		t.Fatalf("metadata lost: %#v", got)
	}
}

func TestLogCompleteness(t *testing.T) {
	for _, tt := range []struct {
		name, rows, cursor, warning, want string
		limit, count                      int
	}{
		{"empty", `[]`, "", "", "complete", 100, 0},
		{"null", `null`, "", "", "complete", 100, 0},
		{"short", `[{}]`, "", "", "complete", 100, 1},
		{"full", `[{}]`, "", "", "unknown", 1, 1},
		{"cursor", `[]`, "next", "", "more_available", 100, 0},
		{"warning", `[]`, "", `,"warning":{"message":"incomplete"}`, "unknown", 100, 0},
		{"over-limit", `[{},{}]`, "", "", "more_available", 1, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":%s,"nextCursor":%q}]}%s}}`, tt.rows, tt.cursor, tt.warning)
			})
			got, err := c.SearchLogs(context.Background(), SearchOptions{Start: time.Unix(1, 0), End: time.Unix(2, 0), Limit: tt.limit})
			if err != nil {
				t.Fatal(err)
			}
			if got.Completeness != tt.want || len(got.Rows) != tt.count || got.Rows == nil {
				t.Fatalf("unexpected result: %#v", got)
			}
		})
	}
}

func TestHTTPFailuresDoNotEchoBodies(t *testing.T) {
	for _, tt := range []struct {
		status int
		code   string
	}{
		{400, "invalid_query"}, {401, "authentication"}, {403, "permission"}, {404, "api"}, {429, "rate_limited"}, {500, "api"},
	} {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				io.WriteString(w, "synthetic-api-key private upstream contents")
			})
			_, err := c.Me(context.Background())
			assertCode(t, err, tt.code)
			if strings.Contains(err.Error(), "synthetic-api-key") || strings.Contains(err.Error(), "private upstream") {
				t.Fatal("upstream contents leaked")
			}
		})
	}
}

func TestRedirectNeverForwardsCredential(t *testing.T) {
	var requests atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer destination.Close()
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) })
	_, err := c.Me(context.Background())
	assertCode(t, err, "authentication")
	if requests.Load() != 0 {
		t.Fatal("followed redirect")
	}
}

func TestMalformedResponses(t *testing.T) {
	for _, tt := range []struct{ contentType, body string }{
		{"text/html", "<html>login</html>"}, {"application/json", "not JSON"},
		{"application/json", `{"status":"error","error":{"message":"synthetic-api-key"}}`},
		{"application/json", `{"status":"success","data":null}`},
		{"application/json", `{"status":"success"}`},
		{"application/json", `{"status":"success","data":{}}`},
		{"application/json", `{"status":"success","data":[]}`},
		{"application/json", `{"status":"success","data":{}} {}`},
	} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", tt.contentType)
			io.WriteString(w, tt.body)
		})
		_, err := c.Me(context.Background())
		assertCode(t, err, "invalid_response")
	}
}

func TestTimeoutCancellationAndResponseLimit(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
		c.http.Timeout = 20 * time.Millisecond
		_, err := c.Me(context.Background())
		assertCode(t, err, "timeout")
	})
	t.Run("cancelled", func(t *testing.T) {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := c.Me(ctx)
		assertCode(t, err, "cancelled")
	})
	t.Run("response limit", func(t *testing.T) {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, strings.Repeat(" ", maxResponseBytes+1))
		})
		_, err := c.Me(context.Background())
		assertCode(t, err, "response_too_large")
	})
}

func TestLogValidation(t *testing.T) {
	c, err := New("https://signoz.example.com", "synthetic-api-key", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, opts := range []SearchOptions{
		{Start: time.Unix(2, 0), End: time.Unix(1, 0), Limit: 100},
		{Start: time.Unix(1, 0), End: time.Unix(2, 0), Limit: 0},
		{Start: time.Unix(1, 0), End: time.Unix(2, 0), Limit: 10001},
	} {
		_, err := c.SearchLogs(context.Background(), opts)
		assertCode(t, err, "usage")
	}
}

func TestMalformedLogResponses(t *testing.T) {
	for _, data := range []string{
		`{}`, `{"type":"scalar","data":{"results":[]}}`,
		`{"type":"raw","data":{"results":[]}}`,
		`{"type":"raw","data":{"results":[{"queryName":"B","rows":[]}]}}`,
		`{"type":"raw","data":{"results":[{"queryName":"A"}]}}`,
		`{"type":"raw","data":{"results":[{"queryName":"A","rows":{}}]}}`,
	} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"status":"success","data":%s}`, data)
		})
		_, err := c.SearchLogs(context.Background(), SearchOptions{Start: time.Unix(1, 0), End: time.Unix(2, 0), Limit: 100})
		assertCode(t, err, "invalid_response")
	}
}

func TestTLSValidationIsEnforced(t *testing.T) {
	var reached atomic.Bool
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	c, err := New(server.URL, "synthetic-api-key", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Me(context.Background())
	assertCode(t, err, "network")
	if reached.Load() {
		t.Fatal("sent a credential to a server with an untrusted certificate")
	}
}

func TestCancellationReachesServer(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(stopped)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.Me(ctx)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not reach the test server")
	}
	cancel()
	assertCode(t, <-done, "cancelled")
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("request cancellation did not reach the server")
	}
}

func TestTimeoutWhileReadingResponseBody(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"success","data":`)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	c.http.Timeout = 50 * time.Millisecond
	_, err := c.Me(context.Background())
	assertCode(t, err, "timeout")
}

func TestEncodedBasePathAndSameOriginRedirect(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.EscapedPath() != "/space%20name/api/v1/service_accounts/me" {
			t.Error("encoded base path was not preserved")
		}
		http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
	})
	client, err := New(c.base.String()+"/space%20name/", "synthetic-api-key", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Me(context.Background())
	assertCode(t, err, "authentication")
	if calls.Load() != 1 {
		t.Fatal("followed a same-origin redirect")
	}
}

func FuzzNormalizeURL(f *testing.F) {
	for _, seed := range []string{
		"https://signoz.example.com", "https://signoz.example.com/space%20name/",
		"http://127.0.0.1:8080", "http://[::1]:8080", "http://localhost:8080",
		"https://user:synthetic@signoz.example.com?key=synthetic", "", "https://%zz",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		normalized, err := NormalizeURL(input)
		if err != nil {
			return
		}
		u, err := url.Parse(normalized)
		if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			t.Fatal("accepted unsafe or unparsable URL")
		}
		if u.Scheme != "https" {
			ip := net.ParseIP(u.Hostname())
			if u.Scheme != "http" || (!strings.EqualFold(u.Hostname(), "localhost") && (ip == nil || !ip.IsLoopback())) {
				t.Fatal("accepted non-loopback plaintext URL")
			}
		}
		second, err := NormalizeURL(normalized)
		if err != nil || second != normalized {
			t.Fatal("normalization is not idempotent")
		}
	})
}
