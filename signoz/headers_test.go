package signoz

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseCustomHeaders(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      http.Header
	}{
		{"unset", "", nil}, {"whitespace", " \t ", nil},
		{"MCP format", " CF-Access-Client-Id : synthetic.access , CF-Access-Client-Secret: synthetic-secret ", http.Header{"Cf-Access-Client-Id": {"synthetic.access"}, "Cf-Access-Client-Secret": {"synthetic-secret"}}},
		{"colon in value", "Authorization:Bearer synthetic:token", http.Header{"Authorization": {"Bearer synthetic:token"}}},
		{"empty value", "X-Feature:", http.Header{"X-Feature": {""}}},
		{"tabs and cookies", "X-Note: one\ttwo,Cookie:session=synthetic; mode=test", http.Header{"X-Note": {"one\ttwo"}, "Cookie": {"session=synthetic; mode=test"}}},
		{"token punctuation", "X_!#$%&'*+-.^`|~:ok", http.Header{http.CanonicalHeaderKey("X_!#$%&'*+-.^`|~"): {"ok"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseCustomHeaders(tc.raw)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("unexpected parsing result: %v", err)
			}
		})
	}
}

func TestRejectInvalidCustomHeaders(t *testing.T) {
	for _, raw := range []string{
		"private-marker", ":private-marker", "X Bad:private-marker", "X-\u00e9:private-marker",
		"X-Test:private-marker,", ",X-Test:private-marker", "X-Test:private-marker,,X-Other:yes",
		"X-Test:private-marker,x-test:second", "X-Test:private-marker,X-Test:second",
		"X-Test:private-marker\r\nInjected:yes", "\nX-Test:private-marker", "X-Test:private-marker\n",
		"X-Test:private-marker\x00", "X-Test:private-marker\x7f", "X-Test:private-marker\v",
		"X-Test:" + strings.Repeat("x", maxCustomHeaderBytes),
	} {
		headers, err := ParseCustomHeaders(raw)
		assertCode(t, err, "usage")
		if headers != nil || strings.Contains(err.Error(), "private-marker") {
			t.Fatal("invalid configuration exposed input or partial headers")
		}
	}
	for _, name := range []string{"SIGNOZ-API-KEY", "X-SigNoz-URL", "Host", "Accept", "Accept-Encoding", "Content-Type", "Content-Length", "User-Agent", "Connection", "Proxy-Connection", "Proxy-Authorization", "Transfer-Encoding", "Trailer", "TE", "Upgrade", "Expect"} {
		for _, variant := range []string{name, strings.ToLower(name), strings.ToUpper(name)} {
			_, err := NewWithHeaders("https://signoz.example.com", "synthetic-key", time.Second, variant+":private-marker")
			assertCode(t, err, "usage")
			if strings.Contains(err.Error(), "private-marker") {
				t.Fatal("reserved-header error exposed value")
			}
		}
	}
}

func TestCustomHeaderBounds(t *testing.T) {
	raw := "X-Test:" + strings.Repeat("x", maxCustomHeaderBytes-len("X-Test:"))
	if _, err := ParseCustomHeaders(raw); err != nil {
		t.Fatal("exact byte limit rejected")
	}
	_, err := ParseCustomHeaders(raw + "x")
	assertCode(t, err, "usage")
	var pairs []string
	for i := range maxCustomHeaders {
		pairs = append(pairs, fmt.Sprintf("X-Test-%d:value", i))
	}
	if _, err := ParseCustomHeaders(strings.Join(pairs, ",")); err != nil {
		t.Fatal("exact header count rejected")
	}
	pairs = append(pairs, "X-Extra:value")
	_, err = ParseCustomHeaders(strings.Join(pairs, ","))
	assertCode(t, err, "usage")
}

func TestCustomHeadersOnEveryRequest(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer synthetic:token" || r.Header.Get("Cf-Access-Client-Secret") != "synthetic-secret" || r.Header.Get("SIGNOZ-API-KEY") != "synthetic-key" || r.Header.Get("Accept") != "application/json" || r.UserAgent() != "sig" {
			t.Error("missing custom or CLI-owned header")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			io.WriteString(w, `{"status":"success","data":{"id":"synthetic"}}`)
		} else {
			if r.Header.Get("Content-Type") != "application/json" {
				t.Error("lost request content type")
			}
			io.WriteString(w, `{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":[{}]}]}}}`)
		}
	}))
	defer s.Close()
	c, err := NewWithHeaders(s.URL, "synthetic-key", time.Second, "Authorization:Bearer synthetic:token,CF-Access-Client-Secret:synthetic-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CollectPages(context.Background(), SearchRequest{Window: Window{Start: time.Unix(1, 0), End: time.Unix(2, 0)}, Signal: Logs, Limit: 1}, PageBudget{Pages: 2}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatal("pagination or identity request missing")
	}
}

func TestCustomHeadersAreRequestOwned(t *testing.T) {
	c, err := NewWithHeaders("https://signoz.example.com", "synthetic-key", time.Second, "Authorization:Bearer synthetic")
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = responseTransport(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer synthetic" {
			t.Error("headers shared across requests")
		}
		// Mutate both the value slice and the map to detect shallow copying.
		req.Header["Authorization"][0] = "changed"
		req.Header.Set("X-Mutated", "changed")
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"status":"success","data":{"id":"synthetic"}}`)), ContentLength: -1}, nil
	})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := c.Me(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if c.headers.Get("Authorization") != "Bearer synthetic" || c.headers.Get("X-Mutated") != "" {
		t.Fatal("request changed client headers")
	}
}

func TestCustomHeadersNeverFollowRedirects(t *testing.T) {
	for _, sameOrigin := range []bool{false, true} {
		t.Run(fmt.Sprint(sameOrigin), func(t *testing.T) {
			var forwarded atomic.Bool
			destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { forwarded.Store(true) }))
			defer destination.Close()
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/login" {
					forwarded.Store(true)
					return
				}
				location := destination.URL
				if sameOrigin {
					location = "/login"
				}
				http.Redirect(w, r, location, http.StatusTemporaryRedirect)
			}))
			defer source.Close()
			c, err := NewWithHeaders(source.URL, "synthetic-key", time.Second, "Authorization:Bearer synthetic,CF-Access-Client-Secret:synthetic-secret")
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Me(context.Background())
			assertCode(t, err, "authentication")
			if forwarded.Load() {
				t.Fatal("followed redirect with credentials")
			}
		})
	}
}

func TestCustomHeaderTransportErrorsAreSanitized(t *testing.T) {
	c, err := NewWithHeaders("https://signoz.example.com", "synthetic-key", time.Second, "Authorization:Bearer private-marker")
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = responseTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport diagnostic containing private-marker")
	})
	_, err = c.Me(context.Background())
	assertCode(t, err, "network")
	if strings.Contains(err.Error(), "private-marker") {
		t.Fatal("transport error exposed a proxy credential")
	}
}
