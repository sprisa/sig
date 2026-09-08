package cmd

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v3"
)

func TestSearchPaginationFreezesWindowAndUsesOffsets(t *testing.T) {
	h := newHarness(t)
	var offsets []int
	var firstStart, firstEnd int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Start, End     int64
			CompositeQuery struct {
				Queries []struct {
					Spec struct {
						Offset, Limit int
						Cursor        string
						Signal        string
					}
				}
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		spec := body.CompositeQuery.Queries[0].Spec
		if len(offsets) == 0 {
			firstStart, firstEnd = body.Start, body.End
		}
		if body.Start != firstStart || body.End != firstEnd || spec.Cursor != "" {
			t.Error("pagination changed its window or used the unsafe native cursor")
		}
		offsets = append(offsets, spec.Offset)
		w.Header().Set("Content-Type", "application/json")
		rows := `[]`
		cursor := ""
		if spec.Offset < 4 {
			// All records share a millisecond: timestamp-only cursors would skip them.
			rows = fmt.Sprintf(`[{"timestamp":"2026-01-01T11:59:00Z","data":{"id":"row-%d"}},{"timestamp":"2026-01-01T11:59:00Z","data":{"id":"row-%d"}}]`, spec.Offset, spec.Offset+1)
			cursor = "unsafe-native-cursor"
		}
		fmt.Fprintf(w, `{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":%s,"nextCursor":%q}]}}}`, rows, cursor)
	}))
	defer s.Close()
	h.env["SIGNOZ_URL"], h.env["SIGNOZ_API_KEY"] = s.URL, "synthetic-api-key"
	got := h.run(t, "", 0, "logs", "search", "--limit", "2", "--pages", "2")
	var result struct {
		Data []json.RawMessage
		Meta struct {
			Next            string `json:"next_page_token"`
			Returned, Pages int
		}
	}
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Data) != 4 || result.Meta.Pages != 2 || result.Meta.Next == "" {
		t.Fatal("missing bounded multi-page results")
	}
	if len(offsets) != 2 || offsets[0] != 0 || offsets[1] != 2 {
		t.Fatalf("wrong offsets: %v", offsets)
	}
	if strings.Contains(result.Meta.Next, "synthetic-api-key") {
		t.Fatal("credential in token")
	}
	tokenBytes, err := base64.RawURLEncoding.DecodeString(result.Meta.Next)
	if err != nil || strings.Contains(string(tokenBytes), "synthetic-api-key") || strings.Contains(string(tokenBytes), s.URL) {
		t.Fatal("token leaked connection secrets")
	}
	got = h.run(t, "", 0, "logs", "search", "--page-token", result.Meta.Next)
	if !strings.Contains(got, `"returned":0`) || !strings.Contains(got, `"next_page_token":""`) || offsets[2] != 4 {
		t.Fatal("continuation did not terminate")
	}
	h.run(t, "", 2, "traces", "search", "--page-token", result.Meta.Next)
	h.run(t, "", 2, "logs", "search", "--page-token", result.Meta.Next, "--since", "1h")
	h.env["SIGNOZ_URL"] = "https://other.example.com"
	h.run(t, "", 2, "logs", "search", "--page-token", result.Meta.Next)
}

func TestPaginationFailuresDoNotEmitPartialResults(t *testing.T) {
	h := newHarness(t)
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":[{"data":{"id":"first"}}],"nextCursor":"native"}]}}}`)
	}))
	defer s.Close()
	h.env["SIGNOZ_URL"], h.env["SIGNOZ_API_KEY"] = s.URL, "synthetic-api-key"
	h.run(t, "", 6, "logs", "search", "--limit", "1", "--pages", "2")
}

func TestPaginationValidation(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{{"--pages", "0"}, {"--pages", "101"}, {"--limit", "10000", "--pages", "2"}, {"--offset", "1"}, {"--page-token", "invalid"}, {"--page-token", ""}, {"--offset", "-1"}} {
		h.run(t, "", 2, append([]string{"logs", "search"}, args...)...)
	}
}

func TestPaginationHasOneOverallDeadline(t *testing.T) {
	h := newHarness(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(70 * time.Millisecond):
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":[{"data":{"id":"example"}}]}]}}}`)
		case <-r.Context().Done():
		}
	}))
	defer s.Close()
	h.env["SIGNOZ_URL"], h.env["SIGNOZ_API_KEY"] = s.URL, "synthetic-api-key"
	got := h.run(t, "", 5, "logs", "search", "--limit", "1", "--pages", "2", "--timeout", "110ms")
	if !strings.Contains(got, `"code":"timeout"`) {
		t.Fatal("pagination did not enforce a whole-command timeout")
	}
}

func TestPaginationStopsOnWarnings(t *testing.T) {
	h := newHarness(t)
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":[{"data":{"id":"example"}}]}]},"warning":{"message":"synthetic warning"}}}`)
	}))
	defer s.Close()
	h.env["SIGNOZ_URL"], h.env["SIGNOZ_API_KEY"] = s.URL, "synthetic-api-key"
	got := h.run(t, "", 0, "logs", "search", "--limit", "1", "--pages", "2")
	if calls != 1 || !strings.Contains(got, `"completeness":"unknown"`) || !strings.Contains(got, "synthetic warning") {
		t.Fatal("pagination ignored warning")
	}
}

func TestNewCommandsEndToEnd(t *testing.T) {
	h := newHarness(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/fields/keys":
			io.WriteString(w, `{"status":"success","data":{"keys":{"resource":[{"name":"service.name","fieldContext":"resource","fieldDataType":"string"}]},"complete":false}}`)
		case "/api/v1/fields/values":
			if r.URL.Query().Get("name") != "service.name" {
				t.Error("missing field name")
			}
			io.WriteString(w, `{"status":"success","data":{"values":{"stringValues":["checkout"]},"complete":true}}`)
		case "/api/v2/metrics":
			io.WriteString(w, `{"status":"success","data":{"metrics":[{"metricName":"http.requests","type":"Sum"}]}}`)
		case "/api/v4/traces/0123456789abcdef0123456789abcdef/waterfall":
			io.WriteString(w, `{"status":"success","data":{"spans":[],"hasMore":true,"hasMissingSpans":false,"totalSpansCount":9007199254740993}}`)
		case "/api/v5/query_range":
			var body struct{ RequestType string }
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Error("invalid JSON")
			}
			if body.RequestType == "raw" {
				io.WriteString(w, `{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":[]}]}}}`)
			} else {
				io.WriteString(w, `{"status":"success","data":{"type":"time_series","data":{"results":[]},"warning":{"message":"synthetic warning"}}}`)
			}
		case "/api/v5/query_range/preview":
			if r.URL.Query().Get("verbose") != "false" {
				t.Error("preview defaults should not request extra analysis")
			}
			io.WriteString(w, `{"status":"success","data":{"compositeQuery":{"A":{"valid":false,"error":{"message":"synthetic"}}}}}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
	}))
	defer s.Close()
	h.env["SIGNOZ_URL"], h.env["SIGNOZ_API_KEY"] = s.URL, "synthetic-api-key"
	for _, signal := range []string{"logs", "traces", "metrics"} {
		got := h.run(t, "", 0, signal, "fields")
		if !strings.Contains(got, `"complete":false`) || !strings.Contains(got, `"completeness":"unknown"`) {
			t.Fatal("discovery completeness lost")
		}
		h.run(t, "", 0, signal, "values", "service.name")
	}
	h.run(t, "", 0, "services", "list", "--signal", "logs")
	h.run(t, "", 0, "metrics", "list")
	got := h.run(t, "", 0, "metrics", "query", "vector(1)")
	if !strings.Contains(got, "synthetic warning") {
		t.Fatal("metric warning lost")
	}
	got = h.run(t, "", 0, "traces", "get", "0123456789abcdef0123456789abcdef")
	if !strings.Contains(got, `"completeness":"partial"`) || !strings.Contains(got, "9007199254740993") {
		t.Fatal("trace completeness or precision lost")
	}
	h.run(t, "", 0, "traces", "search")
	payload := `{"start":1000,"end":2000,"requestType":"scalar","compositeQuery":{"queries":[{"type":"builder_query","spec":{"name":"A","signal":"logs","aggregations":[{"expression":"count()"}]}}]}}`
	h.run(t, payload, 0, "query", "run", "--file", "-")
	got = h.run(t, payload, 0, "query", "preview", "--file", "-")
	if !strings.Contains(got, `"valid":false`) {
		t.Fatal("preview verdict lost")
	}
	file := filepath.Join(t.TempDir(), "query.json")
	if err := os.WriteFile(file, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	h.run(t, "", 0, "query", "run", "--file", file)
	for _, args := range [][]string{{"traces", "get", "../auth"}, {"traces", "get"}, {"metrics", "query"}, {"metrics", "query", "vector(1)", "--step", "1ms"}, {"metrics", "query", "vector(1)", "--step", "1s", "--since", "24h"}, {"metrics", "list", "--limit", "5001"}, {"logs", "values"}, {"logs", "fields", "--limit", "0"}, {"services", "list", "--signal", "invalid"}, {"query", "run"}} {
		h.run(t, "", 2, args...)
	}
	h.run(t, "not-json", 2, "query", "run", "--file", "-")
	h.run(t, strings.Repeat(" ", (1<<20)+1), 2, "query", "run", "--file", "-")
}

func TestAgentSchemaCoverageAndPrivacy(t *testing.T) {
	h := newHarness(t)
	h.env["SIGNOZ_URL"], h.env["SIGNOZ_API_KEY"] = "https://private.example.com", "synthetic-api-key"
	got := h.run(t, "", 0, "--context", "private-context", "agent", "schema")
	for _, private := range []string{"private.example.com", "private-context", "synthetic-api-key"} {
		if strings.Contains(got, private) {
			t.Fatal("schema contains invocation data")
		}
	}
	var document struct {
		Data struct {
			Command commandSchema
			Globals []flagSchema `json:"global_flags"`
		}
	}
	if err := json.Unmarshal([]byte(got), &document); err != nil {
		t.Fatal(err)
	}
	count := 0
	var walk func(commandSchema)
	walk = func(c commandSchema) {
		if len(c.Commands) == 0 {
			count++
			if c.Policy == nil {
				t.Fatal("missing policy")
			}
		}
		for _, child := range c.Commands {
			walk(child)
		}
	}
	walk(document.Data.Command)
	if count != len(commandPolicies) {
		t.Fatalf("described %d leaves, policies %d", count, len(commandPolicies))
	}
	got = h.run(t, "", 0, "agent", "schema", "query", "run")
	if !strings.Contains(got, `"effect":"server_defined"`) || !strings.Contains(got, `"required":true`) {
		t.Fatal("native query safety/file requirement absent")
	}
	got = h.run(t, "", 0, "agent", "schema", "logs", "search")
	if !strings.Contains(got, `"pagination":"page_token"`) {
		t.Fatal("missing pagination contract")
	}
	h.run(t, "", 2, "agent", "schema", "unknown")
	_, err := describeCommand(&cli.Command{Name: "unknown"}, []string{"unknown"})
	if err == nil {
		t.Fatal("missing operation policy was silently inferred")
	}
}
