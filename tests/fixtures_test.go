package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type scenario struct {
	name                   string
	rows, bodyBytes, pages int
}

var scenarios = []scenario{
	{"default", 100, 512, 1},
	{"one_meg", 1000, 1024, 1},
	{"near_cap", 10000, 1450, 1},
	{"ten_pages", 1000, 1024, 10},
}

func logResponse(count, bodyBytes, offset int) []byte {
	var b bytes.Buffer
	b.WriteString(`{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":[`)
	body := strings.Repeat("x", bodyBytes)
	for i := range count {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"timestamp":"2026-01-01T00:00:01Z","data":{"id":"row-%06d","body":"%s","severity_text":"INFO","service.name":"synthetic-service","sequence":9007199254740993}}`, offset+i, body)
	}
	b.WriteString(`],"nextCursor":"synthetic-cursor"}]}}}`)
	return b.Bytes()
}

// Fixtures are allocated before benchmark timing. The server exercises the real
// client over loopback; benchmark allocations include server/request overhead.
func fixtureServer(t testing.TB, s scenario) (*httptest.Server, int) {
	t.Helper()
	pages := make([][]byte, s.pages)
	total := 0
	for i := range pages {
		pages[i] = logResponse(s.rows, s.bodyBytes, i*s.rows)
		total += len(pages[i])
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v5/query_range" || r.Header.Get("SIGNOZ-API-KEY") != "synthetic-key" {
			http.Error(w, "unexpected synthetic request", http.StatusBadRequest)
			return
		}
		var request struct {
			CompositeQuery struct {
				Queries []struct{ Spec struct{ Limit, Offset int } }
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.CompositeQuery.Queries) != 1 {
			http.Error(w, "invalid synthetic request", http.StatusBadRequest)
			return
		}
		spec := request.CompositeQuery.Queries[0].Spec
		if spec.Limit != s.rows || spec.Offset < 0 || spec.Offset%s.rows != 0 || spec.Offset/s.rows >= len(pages) {
			http.Error(w, "unexpected page bounds", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pages[spec.Offset/s.rows])
	}))
	t.Cleanup(server.Close)
	return server, total
}
