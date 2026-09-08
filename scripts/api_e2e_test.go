//go:build e2e

package scripts

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sprisa/sig/signoz"
)

// This deliberately constructs requests independently of the production client
// and compares decoded JSON with UseNumber, including the complete log rows.
func TestLiveAPIParity(t *testing.T) {
	endpoint, key := os.Getenv("SIGNOZ_URL"), os.Getenv("SIGNOZ_API_KEY")
	if endpoint == "" || key == "" {
		t.Fatal("live parity tests require SIGNOZ_URL and SIGNOZ_API_KEY")
	}
	endpoint, err := signoz.NormalizeURL(endpoint)
	if err != nil {
		t.Fatal("invalid live API URL")
	}
	startText, endText := os.Getenv("SIG_E2E_START"), os.Getenv("SIG_E2E_END")
	end := time.Now().UTC().Truncate(time.Millisecond)
	start := end.Add(-time.Hour)
	if startText != "" || endText != "" {
		start, err = time.Parse(time.RFC3339Nano, startText)
		if err != nil {
			t.Fatal("SIG_E2E_START must be an RFC3339 timestamp")
		}
		end, err = time.Parse(time.RFC3339Nano, endText)
		if err != nil {
			t.Fatal("SIG_E2E_END must be an RFC3339 timestamp")
		}
	}
	start, end = start.UTC().Truncate(time.Millisecond), end.UTC().Truncate(time.Millisecond)
	if !start.Before(end) {
		t.Fatal("comparison start must precede end")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "sig.exe")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = ".."
	if output, err := build.CombinedOutput(); err != nil {
		// Compiler diagnostics concern source code, not API responses.
		t.Fatalf("build failed: %v\n%s", err, output)
	}
	decode := func(t *testing.T, b []byte) any {
		t.Helper()
		var value any
		decoder := json.NewDecoder(bytes.NewReader(b))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			t.Fatal("invalid JSON; response contents suppressed")
		}
		if decoder.Decode(new(any)) != io.EOF {
			t.Fatal("unexpected trailing JSON output")
		}
		return value
	}
	field := func(value any, names ...string) any {
		for _, name := range names {
			object, ok := value.(map[string]any)
			if !ok {
				return nil
			}
			value = object[name]
		}
		return value
	}
	run := func(t *testing.T, credential string, expectedExit int, args ...string) any {
		t.Helper()
		command := exec.Command(binary, args...)
		for _, item := range os.Environ() {
			if !strings.HasPrefix(item, "SIGNOZ_") && !strings.HasPrefix(item, "SIG_CONFIG_DIR=") {
				command.Env = append(command.Env, item)
			}
		}
		command.Env = append(command.Env, "SIGNOZ_URL="+endpoint, "SIGNOZ_API_KEY="+credential, "SIG_CONFIG_DIR="+filepath.Join(dir, "config"))
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		code := 0
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatal("could not run CLI")
			}
			code = exit.ExitCode()
		}
		if code != expectedExit {
			t.Fatalf("CLI exit %d, expected %d; response contents suppressed", code, expectedExit)
		}
		if bytes.Contains(stdout.Bytes(), []byte(key)) || bytes.Contains(stderr.Bytes(), []byte(key)) {
			t.Fatal("credential appeared in CLI output")
		}
		if code == 0 {
			if stderr.Len() != 0 {
				t.Fatal("successful CLI request wrote stderr")
			}
			return decode(t, stdout.Bytes())
		}
		if stdout.Len() != 0 {
			t.Fatal("failed CLI request wrote stdout")
		}
		return decode(t, stderr.Bytes())
	}
	direct := func(t *testing.T, method, path, credential string, body any) (int, any) {
		t.Helper()
		var input io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatal("could not encode direct request")
			}
			input = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, endpoint+path, input)
		if err != nil {
			t.Fatal("could not construct direct request")
		}
		req.Header.Set("Accept", "application/json")
		if credential != "" {
			req.Header.Set("SIGNOZ-API-KEY", credential)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal("direct API request failed; details suppressed")
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
		if err != nil || len(b) > 16<<20 {
			t.Fatal("could not read bounded direct response")
		}
		return resp.StatusCode, decode(t, b)
	}
	query := func(signal string, limit, offset int, where string) any {
		order := []any{map[string]any{"key": map[string]string{"name": "timestamp"}, "direction": "desc"}}
		ties := []string{"id"}
		if signal == "traces" {
			ties = []string{"trace_id", "span_id"}
		}
		for _, name := range ties {
			order = append(order, map[string]any{"key": map[string]string{"name": name}, "direction": "desc"})
		}
		spec := map[string]any{
			"name": "A", "signal": signal, "offset": offset, "limit": limit, "order": order,
		}
		if where != "" {
			spec["filter"] = map[string]string{"expression": where}
		}
		return map[string]any{
			"start": start.UnixMilli(), "end": end.UnixMilli(), "requestType": "raw",
			"compositeQuery": map[string]any{"queries": []any{map[string]any{"type": "builder_query", "spec": spec}}},
		}
	}
	t.Run("identity", func(t *testing.T) {
		status, raw := direct(t, "GET", "/api/v1/service_accounts/me", key, nil)
		got := run(t, key, 0, "auth", "status")
		if status != 200 || !reflect.DeepEqual(field(got, "data", "identity"), field(raw, "data")) {
			t.Fatal("CLI identity differs from direct API")
		}
	})
	for _, test := range []struct {
		name, where string
		limit       int
	}{
		{"limit-one", os.Getenv("SIG_E2E_WHERE"), 1},
		{"limit-five", os.Getenv("SIG_E2E_WHERE"), 5},
		{"limit-twenty-five", os.Getenv("SIG_E2E_WHERE"), 25},
		{"error-filter", "severity_text = 'ERROR'", 5},
		{"empty-result", "severity_text = 'sig-e2e-no-match'", 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, raw := direct(t, "POST", "/api/v5/query_range", key, query("logs", test.limit, 0, test.where))
			if status != 200 {
				t.Fatalf("direct query returned HTTP %d", status)
			}
			got := run(t, key, 0, "logs", "search", "--start", start.Format(time.RFC3339Nano), "--end", end.Format(time.RFC3339Nano), "--limit", strconv.Itoa(test.limit), "--where", test.where)
			results, ok := field(raw, "data", "data", "results").([]any)
			if !ok || len(results) != 1 {
				t.Fatal("unexpected direct query shape")
			}
			rows := field(results[0], "rows")
			if rows == nil {
				rows = []any{}
			}
			if !reflect.DeepEqual(field(got, "data"), rows) {
				t.Fatal("CLI rows or numeric values differ from direct API; use a fixed window with stable data")
			}
			if field(got, "meta", "next_cursor") != field(results[0], "nextCursor") || !reflect.DeepEqual(field(got, "meta", "warning"), field(raw, "data", "warning")) {
				t.Fatal("CLI cursor or warning differs from direct API")
			}
			if strings.HasPrefix(test.name, "limit-") && (field(got, "meta", "returned") == json.Number("0") || field(got, "meta", "warning") != nil) {
				t.Fatal("comparison needs nonempty, unqualified results; configure a known-data window and filter")
			}
		})
	}
	t.Run("invalid-key", func(t *testing.T) {
		status, _ := direct(t, "GET", "/api/v1/service_accounts/me", "invalid-test-key", nil)
		got := run(t, "invalid-test-key", 3, "auth", "status")
		if status != 401 || field(got, "error", "http_status") != json.Number("401") || field(got, "error", "code") != "authentication" {
			t.Fatal("invalid-key rejection differs from direct API")
		}
	})
	t.Run("invalid-filter", func(t *testing.T) {
		status, _ := direct(t, "POST", "/api/v5/query_range", key, query("logs", 1, 0, "severity_text ="))
		got := run(t, key, 6, "logs", "search", "--start", start.Format(time.RFC3339Nano), "--end", end.Format(time.RFC3339Nano), "--limit", "1", "--where", "severity_text =")
		if status != 400 || field(got, "error", "http_status") != json.Number("400") || field(got, "error", "code") != "invalid_query" {
			t.Fatal("invalid-filter rejection differs from direct API")
		}
	})
	rowsOf := func(t *testing.T, raw any) []any {
		t.Helper()
		results, ok := field(raw, "data", "data", "results").([]any)
		if !ok || len(results) != 1 {
			t.Fatal("unexpected direct search response")
		}
		rows, ok := field(results[0], "rows").([]any)
		if !ok && field(results[0], "rows") != nil {
			t.Fatal("unexpected direct row array")
		}
		if rows == nil {
			rows = []any{}
		}
		return rows
	}
	traceID := strings.ToLower(os.Getenv("SIG_E2E_TRACE_ID"))
	if traceID != "" {
		_, err := hex.DecodeString(traceID)
		if len(traceID) != 32 || err != nil || strings.Trim(traceID, "0") == "" {
			t.Fatal("SIG_E2E_TRACE_ID must be a nonzero 32-character hexadecimal ID")
		}
	}
	for _, signal := range []string{"logs", "traces"} {
		t.Run(signal+"-pagination", func(t *testing.T) {
			status, raw := direct(t, "POST", "/api/v5/query_range", key, query(signal, 4, 0, ""))
			if status != 200 {
				t.Fatalf("direct search returned HTTP %d", status)
			}
			got := run(t, key, 0, signal, "search", "--start", start.Format(time.RFC3339Nano), "--end", end.Format(time.RFC3339Nano), "--limit", "2", "--pages", "2")
			rows := rowsOf(t, raw)
			if !reflect.DeepEqual(field(got, "data"), rows) {
				t.Fatal("offset pages differ from a single direct query")
			}
			if signal == "traces" && len(rows) > 0 && traceID == "" {
				traceID, _ = field(rows[0], "data", "trace_id").(string)
			}
			token, _ := field(got, "meta", "next_page_token").(string)
			if len(rows) < 4 {
				t.Log("fewer than four records; continuation check is not applicable")
				return
			}
			if token == "" {
				t.Fatal("full pages did not return a continuation token")
			}
			_, raw = direct(t, "POST", "/api/v5/query_range", key, query(signal, 2, 4, ""))
			got = run(t, key, 0, signal, "search", "--page-token", token)
			if !reflect.DeepEqual(field(got, "data"), rowsOf(t, raw)) {
				t.Fatal("resumed page differs from direct offset query")
			}
		})
	}
	t.Run("trace-detail", func(t *testing.T) {
		if traceID == "" {
			t.Skip("no trace ID available in the comparison window")
		}
		status, raw := direct(t, "POST", "/api/v4/traces/"+traceID+"/waterfall", key, map[string]any{"selectedSpanId": "", "uncollapsedSpans": []string{}})
		got := run(t, key, 0, "traces", "get", traceID)
		if status != 200 || !reflect.DeepEqual(field(got, "data"), field(raw, "data")) {
			t.Fatal("trace waterfall differs from direct API")
		}
	})
	t.Run("trace-not-found", func(t *testing.T) {
		idBytes := make([]byte, 16)
		if _, err := rand.Read(idBytes); err != nil {
			t.Fatal("could not generate synthetic trace ID")
		}
		id := hex.EncodeToString(idBytes)
		status, _ := direct(t, "POST", "/api/v4/traces/"+id+"/waterfall", key, map[string]any{"selectedSpanId": "", "uncollapsedSpans": []string{}})
		got := run(t, key, 6, "traces", "get", id)
		if status != 404 || field(got, "error", "http_status") != json.Number("404") {
			t.Fatal("trace-not-found status differs from direct API")
		}
	})
	var metricName string
	t.Run("metric-list", func(t *testing.T) {
		params := url.Values{"start": {strconv.FormatInt(start.UnixMilli(), 10)}, "end": {strconv.FormatInt(end.UnixMilli(), 10)}, "limit": {"5"}}
		status, raw := direct(t, "GET", "/api/v2/metrics?"+params.Encode(), key, nil)
		got := run(t, key, 0, "metrics", "list", "--start", start.Format(time.RFC3339Nano), "--end", end.Format(time.RFC3339Nano), "--limit", "5")
		metrics := field(raw, "data", "metrics")
		if metrics == nil {
			metrics = []any{}
		}
		if status != 200 || !reflect.DeepEqual(field(got, "data"), metrics) {
			t.Fatal("metric list differs from direct API")
		}
		if entries, ok := metrics.([]any); ok && len(entries) > 0 {
			metricName, _ = field(entries[0], "metricName").(string)
		}
	})
	for _, signal := range []string{"logs", "traces", "metrics"} {
		for _, action := range []string{"fields", "values"} {
			t.Run(signal+"-"+action, func(t *testing.T) {
				params := url.Values{"signal": {signal}, "startUnixMilli": {strconv.FormatInt(start.UnixMilli(), 10)}, "endUnixMilli": {strconv.FormatInt(end.UnixMilli(), 10)}, "limit": {"5"}}
				args := []string{signal, action, "--start", start.Format(time.RFC3339Nano), "--end", end.Format(time.RFC3339Nano), "--limit", "5"}
				path := "/api/v1/fields/keys"
				if action == "values" {
					path = "/api/v1/fields/values"
					params.Set("name", "service.name")
					args = append(args, "service.name")
				}
				if signal == "metrics" && metricName != "" {
					params.Set("metricName", metricName)
					args = append(args, "--metric", metricName)
				}
				status, raw := direct(t, "GET", path+"?"+params.Encode(), key, nil)
				got := run(t, key, 0, args...)
				if status != 200 || !reflect.DeepEqual(discoverySetView(field(got, "data")), discoverySetView(field(raw, "data"))) {
					t.Fatal("discovery differs from direct API")
				}
			})
		}
	}
	t.Run("services", func(t *testing.T) {
		params := url.Values{"signal": {"logs"}, "startUnixMilli": {strconv.FormatInt(start.UnixMilli(), 10)}, "endUnixMilli": {strconv.FormatInt(end.UnixMilli(), 10)}, "limit": {"5"}, "fieldContext": {"resource"}, "name": {"service.name"}}
		status, raw := direct(t, "GET", "/api/v1/fields/values?"+params.Encode(), key, nil)
		got := run(t, key, 0, "services", "list", "--signal", "logs", "--start", start.Format(time.RFC3339Nano), "--end", end.Format(time.RFC3339Nano), "--limit", "5")
		if status != 200 || !reflect.DeepEqual(discoverySetView(field(got, "data")), discoverySetView(field(raw, "data"))) {
			t.Fatal("service discovery differs from direct API")
		}
	})
	shortEnd := start.Add(5 * time.Minute)
	if shortEnd.After(end) {
		shortEnd = end
	}
	t.Run("promql", func(t *testing.T) {
		payload := map[string]any{"noCache": true, "start": start.UnixMilli(), "end": shortEnd.UnixMilli(), "requestType": "time_series", "compositeQuery": map[string]any{"queries": []any{map[string]any{"type": "promql", "spec": map[string]any{"name": "A", "query": "vector(1)", "step": 60}}}}}
		status, raw := direct(t, "POST", "/api/v5/query_range", key, payload)
		got := run(t, key, 0, "metrics", "query", "vector(1)", "--start", start.Format(time.RFC3339Nano), "--end", shortEnd.Format(time.RFC3339Nano), "--step", "1m", "--no-cache")
		if status != 200 || !reflect.DeepEqual(field(got, "data", "data"), field(raw, "data", "data")) {
			t.Fatal("PromQL values differ from direct API")
		}
	})
	t.Run("metric-series", func(t *testing.T) {
		if metricName == "" {
			t.Skip("no metric name available in the comparison window")
		}
		expression := "count({__name__=" + strconv.Quote(metricName) + "})"
		payload := map[string]any{"noCache": true, "start": start.UnixMilli(), "end": shortEnd.UnixMilli(), "requestType": "time_series", "compositeQuery": map[string]any{"queries": []any{map[string]any{"type": "promql", "spec": map[string]any{"name": "A", "query": expression, "step": 60}}}}}
		status, raw := direct(t, "POST", "/api/v5/query_range", key, payload)
		got := run(t, key, 0, "metrics", "query", expression, "--start", start.Format(time.RFC3339Nano), "--end", shortEnd.Format(time.RFC3339Nano), "--step", "1m", "--no-cache")
		if status != 200 || !reflect.DeepEqual(field(got, "data", "data"), field(raw, "data", "data")) {
			t.Fatal("metric series differs from direct API")
		}
	})
	nativePayload := map[string]any{"start": start.UnixMilli(), "end": shortEnd.UnixMilli(), "requestType": "scalar", "compositeQuery": map[string]any{"queries": []any{map[string]any{"type": "builder_query", "spec": map[string]any{"name": "A", "signal": "logs", "aggregations": []any{map[string]string{"expression": "count()"}}}}}}}
	payloadBytes, _ := json.Marshal(nativePayload)
	queryFile := filepath.Join(dir, "query.json")
	if err := os.WriteFile(queryFile, payloadBytes, 0600); err != nil {
		t.Fatal("could not write temporary query file")
	}
	t.Run("native-query", func(t *testing.T) {
		status, raw := direct(t, "POST", "/api/v5/query_range", key, nativePayload)
		got := run(t, key, 0, "query", "run", "--file", queryFile)
		if status != 200 || !reflect.DeepEqual(field(got, "data", "data"), field(raw, "data", "data")) {
			t.Fatal("native scalar result differs from direct API")
		}
	})
	t.Run("query-preview", func(t *testing.T) {
		status, raw := direct(t, "POST", "/api/v5/query_range/preview?verbose=false", key, nativePayload)
		got := run(t, key, 0, "query", "preview", "--file", queryFile)
		if status != 200 || field(got, "data", "compositeQuery", "A", "valid") != true || field(raw, "data", "compositeQuery", "A", "valid") != true {
			t.Fatal("preview did not validate the native query")
		}
	})
	t.Run("agent-schema", func(t *testing.T) {
		got := run(t, key, 0, "agent", "schema", "query", "run")
		if field(got, "data", "command", "policy", "effect") != "server_defined" {
			t.Fatal("agent schema misclassified native query safety")
		}
	})
}

// Discovery value collections have no ordering guarantee. Compare their members
// without dropping completeness or other metadata, and leave CLI output intact.
func discoverySetView(data any) any {
	object, ok := data.(map[string]any)
	if !ok {
		return data
	}
	result := map[string]any{}
	for name, value := range object {
		result[name] = value
	}
	values, ok := object["values"].(map[string]any)
	if !ok {
		return result
	}
	copyValues := map[string]any{}
	for name, value := range values {
		if items, ok := value.([]any); ok {
			encoded := make([]string, 0, len(items))
			for _, item := range items {
				b, _ := json.Marshal(item)
				encoded = append(encoded, string(b))
			}
			sort.Strings(encoded)
			copyValues[name] = encoded
		} else {
			copyValues[name] = value
		}
	}
	result["values"] = copyValues
	return result
}
