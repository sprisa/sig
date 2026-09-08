//go:build e2e

package scripts

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestLiveAPIParity(t *testing.T) {
	h := newLiveHarness(t)
	t.Run("identity", func(t *testing.T) {
		raw := h.api(t, "GET", "/api/v1/service_accounts/me", h.key, nil, 200)
		got := h.cli(t, h.key, 0, "auth", "status")
		requireEqual(t, requireField(t, got.Data, "identity"), raw.Data)
	})
	for _, test := range []struct {
		name, where string
		limit       int
	}{{"limit-one", os.Getenv("SIG_E2E_WHERE"), 1}, {"limit-five", os.Getenv("SIG_E2E_WHERE"), 5}, {"limit-twenty-five", os.Getenv("SIG_E2E_WHERE"), 25}, {"error-filter", "severity_text = 'ERROR'", 5}, {"empty-result", "severity_text = 'sig-e2e-no-match'", 5}} {
		t.Run(test.name, func(t *testing.T) { checkLogQuery(t, h, test.limit, test.where) })
	}
	for _, signal := range []string{"logs", "traces"} {
		t.Run(signal+"-pagination", func(t *testing.T) { checkPagination(t, h, signal) })
	}
	t.Run("trace-detail", func(t *testing.T) {
		id := h.traceFixture(t)
		raw := h.api(t, "POST", "/api/v4/traces/"+id+"/waterfall", h.key, map[string]any{"selectedSpanId": "", "uncollapsedSpans": []string{}}, 200)
		got := h.cli(t, h.key, 0, "traces", "get", id)
		requireArray(t, requireField(t, got.Data, "spans"))
		requireEqual(t, got.Data, raw.Data)
	})
	t.Run("trace-not-found", func(t *testing.T) {
		idBytes := make([]byte, 16)
		if _, err := rand.Read(idBytes); err != nil {
			t.Fatal(err)
		}
		id := hex.EncodeToString(idBytes)
		h.api(t, "POST", "/api/v4/traces/"+id+"/waterfall", h.key, map[string]any{"selectedSpanId": "", "uncollapsedSpans": []string{}}, 404)
		requireEqual(t, h.cli(t, h.key, 6, "traces", "get", id).Error.HTTPStatus, 404)
	})
	t.Run("metric-list", func(t *testing.T) {
		raw := h.api(t, "GET", "/api/v2/metrics?"+h.metricParams(5).Encode(), h.key, nil, 200)
		got := h.cli(t, h.key, 0, append([]string{"metrics", "list", "--limit", "5"}, h.bounds()...)...)
		metrics := requireField(t, raw.Data, "metrics")
		if metrics == nil {
			metrics = []any{}
		}
		requireArray(t, got.Data)
		requireEqual(t, got.Data, metrics)
	})
	for _, signal := range []string{"logs", "traces", "metrics"} {
		for _, action := range []string{"fields", "values"} {
			t.Run(signal+"-"+action, func(t *testing.T) { checkDiscovery(t, h, signal, action) })
		}
	}
	t.Run("services", func(t *testing.T) {
		p := h.fieldParams("logs")
		p.Set("name", "service.name")
		p.Set("fieldContext", "resource")
		raw := h.api(t, "GET", "/api/v1/fields/values?"+p.Encode(), h.key, nil, 200)
		got := h.cli(t, h.key, 0, append([]string{"services", "list", "--signal", "logs", "--limit", "5"}, h.bounds()...)...)
		requireField(t, got.Data, "values")
		requireEqual(t, discoverySetView(got.Data), discoverySetView(raw.Data))
	})
	t.Run("promql", func(t *testing.T) { checkPromQL(t, h, "vector(1)") })
	t.Run("metric-series", func(t *testing.T) { checkPromQL(t, h, "count({__name__="+strconv.Quote(h.metricFixture(t))+"})") })
	t.Run("native-query", func(t *testing.T) { checkNativeQuery(t, h, false) })
	t.Run("query-preview", func(t *testing.T) { checkNativeQuery(t, h, true) })
	t.Run("invalid-key", func(t *testing.T) {
		h.api(t, "GET", "/api/v1/service_accounts/me", "invalid-test-key", nil, 401)
		got := h.cli(t, "invalid-test-key", 3, "auth", "status")
		requireEqual(t, got.Error.HTTPStatus, 401)
		requireEqual(t, got.Error.Code, "authentication")
	})
	t.Run("invalid-filter", func(t *testing.T) {
		h.api(t, "POST", "/api/v5/query_range", h.key, h.searchBody("logs", 1, 0, "severity_text ="), 400)
		got := h.cli(t, h.key, 6, append([]string{"logs", "search", "--limit", "1", "--where", "severity_text ="}, h.bounds()...)...)
		requireEqual(t, got.Error.HTTPStatus, 400)
		requireEqual(t, got.Error.Code, "invalid_query")
	})
	t.Run("agent-schema", func(t *testing.T) {
		got := h.cli(t, h.key, 0, "agent", "schema", "query", "run")
		requireEqual(t, requireField(t, got.Data, "command", "policy", "effect"), "server_defined")
	})
}

func checkLogQuery(t *testing.T, h *liveHarness, limit int, where string) {
	raw := h.api(t, "POST", "/api/v5/query_range", h.key, h.searchBody("logs", limit, 0, where), 200)
	got := h.cli(t, h.key, 0, append([]string{"logs", "search", "--limit", strconv.Itoa(limit), "--where", where}, h.bounds()...)...)
	rows := searchRows(t, raw)
	requireEqual(t, requireArray(t, got.Data), rows)
	if where == os.Getenv("SIG_E2E_WHERE") && len(rows) == 0 {
		t.Fatal("comparison requires known log data")
	}
	result := queryResults(t, raw, "raw")[0]
	requireEqual(t, requireField(t, got.Meta, "next_cursor"), requireField(t, result, "nextCursor"))
}

func checkPagination(t *testing.T, h *liveHarness, signal string) {
	raw := h.api(t, "POST", "/api/v5/query_range", h.key, h.searchBody(signal, 4, 0, ""), 200)
	got := h.cli(t, h.key, 0, append([]string{signal, "search", "--limit", "2", "--pages", "2"}, h.bounds()...)...)
	rows := searchRows(t, raw)
	requireEqual(t, requireArray(t, got.Data), rows)
	if len(rows) < 4 {
		t.Skip("continuation needs at least four records in the comparison window")
	}
	token, ok := requireField(t, got.Meta, "next_page_token").(string)
	if !ok || token == "" {
		t.Fatal("missing continuation token")
	}
	raw = h.api(t, "POST", "/api/v5/query_range", h.key, h.searchBody(signal, 2, 4, ""), 200)
	got = h.cli(t, h.key, 0, signal, "search", "--page-token", token)
	requireEqual(t, requireArray(t, got.Data), searchRows(t, raw))
}

func checkDiscovery(t *testing.T, h *liveHarness, signal, action string) {
	p := h.fieldParams(signal)
	args := append([]string{signal, action, "--limit", "5"}, h.bounds()...)
	path, member := "/api/v1/fields/keys", "keys"
	if action == "values" {
		path, member = "/api/v1/fields/values", "values"
		p.Set("name", "service.name")
		args = append(args, "service.name")
	}
	if signal == "metrics" {
		metric := h.metricFixture(t)
		p.Set("metricName", metric)
		args = append(args, "--metric", metric)
	}
	raw := h.api(t, "GET", path+"?"+p.Encode(), h.key, nil, 200)
	got := h.cli(t, h.key, 0, args...)
	requireField(t, got.Data, member)
	requireField(t, raw.Data, member)
	requireEqual(t, requireField(t, got.Data, "complete"), requireField(t, raw.Data, "complete"))
	requireEqual(t, discoverySetView(got.Data), discoverySetView(raw.Data))
}

func checkPromQL(t *testing.T, h *liveHarness, expression string) {
	end := h.start.Add(5 * time.Minute)
	if end.After(h.end) {
		end = h.end
	}
	payload := map[string]any{"noCache": true, "start": h.start.UnixMilli(), "end": end.UnixMilli(), "requestType": "time_series", "compositeQuery": map[string]any{"queries": []any{map[string]any{"type": "promql", "spec": map[string]any{"name": "A", "query": expression, "step": 60}}}}}
	raw := h.api(t, "POST", "/api/v5/query_range", h.key, payload, 200)
	got := h.cli(t, h.key, 0, "metrics", "query", expression, "--start", h.start.Format(time.RFC3339Nano), "--end", end.Format(time.RFC3339Nano), "--step", "1m", "--no-cache")
	actual, expected := queryResults(t, got, "time_series"), queryResults(t, raw, "time_series")
	if len(actual) != 1 {
		t.Fatal("PromQL must return one query result")
	}
	aggregations := requireArray(t, requireField(t, actual[0], "aggregations"))
	if expression == "vector(1)" {
		if len(aggregations) == 0 {
			t.Fatal("constant PromQL returned no aggregation")
		}
		series := requireArray(t, requireField(t, aggregations[0], "series"))
		if len(series) == 0 {
			t.Fatal("constant PromQL returned no series")
		}
		if len(requireArray(t, requireField(t, series[0], "values"))) == 0 {
			t.Fatal("constant PromQL returned no points")
		}
	}
	requireEqual(t, actual, expected)
}

func checkNativeQuery(t *testing.T, h *liveHarness, preview bool) {
	end := h.start.Add(5 * time.Minute)
	if end.After(h.end) {
		end = h.end
	}
	payload := map[string]any{"start": h.start.UnixMilli(), "end": end.UnixMilli(), "requestType": "scalar", "compositeQuery": map[string]any{"queries": []any{map[string]any{"type": "builder_query", "spec": map[string]any{"name": "A", "signal": "logs", "aggregations": []any{map[string]string{"expression": "count()"}}}}}}}
	b, _ := json.Marshal(payload)
	file := filepath.Join(t.TempDir(), "query.json")
	if err := os.WriteFile(file, b, 0600); err != nil {
		t.Fatal("cannot write query fixture")
	}
	if preview {
		raw := h.api(t, "POST", "/api/v5/query_range/preview?verbose=false", h.key, payload, 200)
		got := h.cli(t, h.key, 0, "query", "preview", "--file", file)
		requireEqual(t, requireField(t, got.Data, "compositeQuery", "A", "valid"), true)
		requireEqual(t, requireField(t, raw.Data, "compositeQuery", "A", "valid"), true)
		return
	}
	raw := h.api(t, "POST", "/api/v5/query_range", h.key, payload, 200)
	got := h.cli(t, h.key, 0, "query", "run", "--file", file)
	actual, expected := queryResults(t, got, "scalar"), queryResults(t, raw, "scalar")
	if len(actual) != 1 {
		t.Fatal("scalar query must return one result")
	}
	requireArray(t, requireField(t, actual[0], "columns"))
	requireArray(t, requireField(t, actual[0], "data"))
	requireEqual(t, actual, expected)
}
