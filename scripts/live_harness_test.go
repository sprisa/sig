//go:build e2e

package scripts

import (
	"bytes"
	"context"
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

type liveHarness struct {
	endpoint, key, binary, dir string
	start, end                 time.Time
	customHeaders              string
	headers                    http.Header
}
type liveResponse struct {
	Status string
	Data   any
	Meta   any
	Error  struct {
		Code       string
		HTTPStatus int `json:"http_status"`
	}
}

func newLiveHarness(t *testing.T) *liveHarness {
	t.Helper()
	h := &liveHarness{endpoint: os.Getenv("SIGNOZ_URL"), key: os.Getenv("SIGNOZ_API_KEY"), dir: t.TempDir()}
	if h.endpoint == "" || h.key == "" {
		t.Fatal("live tests require SIGNOZ_URL and SIGNOZ_API_KEY")
	}
	var err error
	h.endpoint, err = signoz.NormalizeURL(h.endpoint)
	if err != nil {
		t.Fatal("invalid API URL")
	}
	h.customHeaders = os.Getenv("SIGNOZ_CUSTOM_HEADERS")
	h.headers, err = signoz.ParseCustomHeaders(h.customHeaders)
	if err != nil {
		t.Fatal("invalid SIGNOZ_CUSTOM_HEADERS; contents suppressed")
	}
	h.end = time.Now().UTC().Truncate(time.Millisecond)
	h.start = h.end.Add(-time.Hour)
	if os.Getenv("SIG_E2E_START") != "" || os.Getenv("SIG_E2E_END") != "" {
		h.start, err = time.Parse(time.RFC3339Nano, os.Getenv("SIG_E2E_START"))
		if err != nil {
			t.Fatal("invalid SIG_E2E_START")
		}
		h.end, err = time.Parse(time.RFC3339Nano, os.Getenv("SIG_E2E_END"))
		if err != nil {
			t.Fatal("invalid SIG_E2E_END")
		}
	}
	h.start, h.end = h.start.UTC().Truncate(time.Millisecond), h.end.UTC().Truncate(time.Millisecond)
	if !h.start.Before(h.end) {
		t.Fatal("comparison start must precede end")
	}
	h.binary = filepath.Join(h.dir, "sig.exe")
	build := exec.Command("go", "build", "-o", h.binary, ".")
	build.Dir = ".."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, output)
	}
	return h
}

func decodeLive(t *testing.T, b []byte) liveResponse {
	t.Helper()
	var result liveResponse
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if d.Decode(&result) != nil || d.Decode(new(any)) != io.EOF {
		t.Fatal("invalid JSON response; contents suppressed")
	}
	return result
}

func requireField(t *testing.T, value any, names ...string) any {
	t.Helper()
	for _, name := range names {
		object, ok := value.(map[string]any)
		if !ok {
			t.Fatal("expected an object while inspecting a required field")
		}
		value, ok = object[name]
		if !ok {
			t.Fatalf("missing required field %q", name)
		}
	}
	return value
}

func requireArray(t *testing.T, value any) []any {
	t.Helper()
	a, ok := value.([]any)
	if !ok {
		t.Fatal("expected an array")
	}
	return a
}

func requireEqual(t *testing.T, a, b any) {
	t.Helper()
	if !reflect.DeepEqual(a, b) {
		t.Fatal("CLI and direct API results differ; contents suppressed")
	}
}

func (h *liveHarness) cli(t *testing.T, key string, exit int, args ...string) liveResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, h.binary, args...)
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "SIGNOZ_") && !strings.HasPrefix(item, "SIG_CONFIG_DIR=") {
			c.Env = append(c.Env, item)
		}
	}
	c.Env = append(c.Env, "SIGNOZ_URL="+h.endpoint, "SIGNOZ_API_KEY="+key, "SIGNOZ_CUSTOM_HEADERS="+h.customHeaders, "SIG_CONFIG_DIR="+filepath.Join(h.dir, "config"))
	var out, stderr bytes.Buffer
	c.Stdout = &out
	c.Stderr = &stderr
	err := c.Run()
	code := 0
	if err != nil {
		var e *exec.ExitError
		if !errors.As(err, &e) {
			t.Fatal("could not run CLI")
		}
		code = e.ExitCode()
	}
	if code != exit {
		t.Fatalf("CLI exit %d, expected %d; contents suppressed", code, exit)
	}
	if bytes.Contains(out.Bytes(), []byte(h.key)) || bytes.Contains(stderr.Bytes(), []byte(h.key)) {
		t.Fatal("credential appeared in output")
	}
	if code == 0 {
		if stderr.Len() != 0 {
			t.Fatal("successful command wrote stderr")
		}
		result := decodeLive(t, out.Bytes())
		if result.Data == nil {
			t.Fatal("successful command omitted data")
		}
		return result
	}
	if out.Len() != 0 {
		t.Fatal("failed command wrote stdout")
	}
	result := decodeLive(t, stderr.Bytes())
	if result.Error.Code == "" {
		t.Fatal("failed command omitted error code")
	}
	return result
}

func (h *liveHarness) api(t *testing.T, method, path, key string, body any, status int) liveResponse {
	t.Helper()
	var input io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal("cannot encode direct request")
		}
		input = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.endpoint+path, input)
	if err != nil {
		t.Fatal("invalid direct request")
	}
	if h.headers != nil {
		req.Header = h.headers.Clone()
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("SIGNOZ-API-KEY", key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal("direct request failed; details suppressed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != status {
		t.Fatalf("direct HTTP %d, expected %d", resp.StatusCode, status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil || len(b) > 16<<20 {
		t.Fatal("invalid direct response size")
	}
	result := decodeLive(t, b)
	if status == 200 && (result.Status != "success" || result.Data == nil) {
		t.Fatal("direct API omitted success data")
	}
	return result
}

func (h *liveHarness) bounds() []string {
	return []string{"--start", h.start.Format(time.RFC3339Nano), "--end", h.end.Format(time.RFC3339Nano)}
}
func (h *liveHarness) metricParams(limit int) url.Values {
	return url.Values{"start": {strconv.FormatInt(h.start.UnixMilli(), 10)}, "end": {strconv.FormatInt(h.end.UnixMilli(), 10)}, "limit": {strconv.Itoa(limit)}}
}
func (h *liveHarness) fieldParams(signal string) url.Values {
	return url.Values{"signal": {signal}, "startUnixMilli": {strconv.FormatInt(h.start.UnixMilli(), 10)}, "endUnixMilli": {strconv.FormatInt(h.end.UnixMilli(), 10)}, "limit": {"5"}}
}

// Request construction stays independent of production wire structs.
func (h *liveHarness) searchBody(signal string, limit, offset int, where string) any {
	keys := []string{"timestamp", "id"}
	if signal == "traces" {
		keys = []string{"timestamp", "trace_id", "span_id"}
	}
	order := []any{}
	for _, name := range keys {
		order = append(order, map[string]any{"key": map[string]string{"name": name}, "direction": "desc"})
	}
	spec := map[string]any{"name": "A", "signal": signal, "limit": limit, "offset": offset, "order": order}
	if where != "" {
		spec["filter"] = map[string]string{"expression": where}
	}
	return map[string]any{"start": h.start.UnixMilli(), "end": h.end.UnixMilli(), "requestType": "raw", "compositeQuery": map[string]any{"queries": []any{map[string]any{"type": "builder_query", "spec": spec}}}}
}

func queryResults(t *testing.T, r liveResponse, kind string) []any {
	t.Helper()
	requireEqual(t, requireField(t, r.Data, "type"), kind)
	return requireArray(t, requireField(t, r.Data, "data", "results"))
}

func searchRows(t *testing.T, r liveResponse) []any {
	t.Helper()
	results := queryResults(t, r, "raw")
	if len(results) != 1 {
		t.Fatal("search requires one result")
	}
	rows := requireField(t, results[0], "rows")
	if rows == nil {
		return []any{}
	}
	return requireArray(t, rows)
}

func (h *liveHarness) traceFixture(t *testing.T) string {
	t.Helper()
	id := os.Getenv("SIG_E2E_TRACE_ID")
	if id == "" {
		rows := searchRows(t, h.api(t, "POST", "/api/v5/query_range", h.key, h.searchBody("traces", 1, 0, ""), 200))
		if len(rows) == 0 {
			t.Skip("no trace fixture in comparison window; set SIG_E2E_TRACE_ID")
		}
		var ok bool
		id, ok = requireField(t, rows[0], "data", "trace_id").(string)
		if !ok {
			t.Fatal("trace ID must be a string")
		}
	}
	if b, err := hex.DecodeString(id); err != nil || len(b) != 16 || strings.Trim(id, "0") == "" {
		t.Fatal("invalid trace fixture ID")
	}
	return strings.ToLower(id)
}

func (h *liveHarness) metricFixture(t *testing.T) string {
	t.Helper()
	r := h.api(t, "GET", "/api/v2/metrics?"+h.metricParams(1).Encode(), h.key, nil, 200)
	value := requireField(t, r.Data, "metrics")
	if value == nil {
		t.Skip("no metric fixture in comparison window")
	}
	metrics := requireArray(t, value)
	if len(metrics) == 0 {
		t.Skip("no metric fixture in comparison window")
	}
	name, ok := requireField(t, metrics[0], "metricName").(string)
	if !ok || name == "" {
		t.Fatal("missing metric name")
	}
	return name
}

// Only value collection order is unspecified. All other fields stay comparable.
func discoverySetView(data any) any {
	object, ok := data.(map[string]any)
	if !ok {
		return data
	}
	result := map[string]any{}
	for k, v := range object {
		result[k] = v
	}
	values, ok := object["values"].(map[string]any)
	if !ok {
		return result
	}
	copyValues := map[string]any{}
	for name, value := range values {
		if items, ok := value.([]any); ok {
			encoded := make([]string, 0, len(items))
			for _, v := range items {
				b, _ := json.Marshal(v)
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
