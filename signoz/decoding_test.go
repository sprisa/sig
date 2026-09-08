package signoz

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDirectSearchEnvelopeValidation(t *testing.T) {
	for _, body := range []string{
		`null`, `[]`, `{"status":"success"}`, `{"status":"success","data":null}`,
		`{"status":"error","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":[]}]}}}`,
		`{"status":"success","data":{"type":"raw"}}`,
		`{"status":"success","data":{"type":"raw","data":{"results":null}}}`,
		`{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":[]},{"queryName":"B","rows":[]}]}}}`,
		`{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":true}]}}}`,
		`{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":[]}]}}} {}`,
	} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, body)
		})
		_, err := c.Search(context.Background(), SearchRequest{Window: Window{Start: time.Unix(1, 0), End: time.Unix(2, 0)}, Signal: Logs, Limit: 100})
		assertCode(t, err, "invalid_response")
	}
}

func TestDirectRowsRetainUnknownFieldsAndOwnership(t *testing.T) {
	row := `{"timestamp":"2026-01-01T00:00:01.123456789Z","data":{"body":"<tag>& text","integer":9007199254740993,"decimal":1.234567890123456789,"nested":{"future":[true,null,"unicode-\u2603"]}},"futureRowField":{"enabled":true}}`
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"success","data":{"type":"raw","futureEnvelopeField":true,"data":{"results":[{"queryName":"A","rows":[%s],"nextCursor":"opaque"}]},"warning":{"message":"synthetic","futureWarningField":9007199254740993}}}`, row)
	})
	r := SearchRequest{Window: Window{Start: time.Unix(1, 0), End: time.Unix(2, 0)}, Signal: Logs, Limit: 1}
	first, err := c.Search(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Search(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Rows) != 1 || string(first.Rows[0]) != row || !strings.Contains(string(first.Warning), "9007199254740993") || first.NextCursor != "opaque" {
		t.Fatal("direct decoding changed row content or metadata")
	}
	second.Rows[0][0] = ' '
	if string(first.Rows[0]) != row {
		t.Fatal("responses share mutable row storage")
	}
	b, err := json.Marshal(struct {
		Data []json.RawMessage `json:"data"`
	}{first.Rows})
	if err != nil || !strings.Contains(string(b), "1.234567890123456789") || !strings.Contains(string(b), "9007199254740993") {
		t.Fatal("output lost numeric precision")
	}
}

func TestRequiredNullableRows(t *testing.T) {
	for _, test := range []struct {
		body               string
		present, nilValues bool
	}{
		{`{}`, false, true}, {`{"rows":null}`, true, true}, {`{"rows":[]}`, true, false},
	} {
		var wire struct{ Rows nullableRows }
		if err := json.Unmarshal([]byte(test.body), &wire); err != nil {
			t.Fatal(err)
		}
		if wire.Rows.Present != test.present || (wire.Rows.Values == nil) != test.nilValues {
			t.Fatal("lost absent/null/empty distinction")
		}
	}
}

func TestNullTraceSpansPreservePublicResult(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"success","data":{"spans":null,"hasMore":false,"hasMissingSpans":false,"futureField":9007199254740993}}`)
	})
	result, err := c.Trace(context.Background(), TraceRequest{ID: "0123456789abcdef0123456789abcdef"})
	if err != nil || result.Spans != nil || !strings.Contains(string(result.Raw), `"spans":null`) || !strings.Contains(string(result.Raw), "9007199254740993") {
		t.Fatalf("trace result changed: %v", err)
	}
}

func TestNullMetricListNormalizesToEmpty(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"success","data":{"metrics":null}}`)
	})
	result, err := c.MetricsList(context.Background(), MetricsListRequest{Window: Window{Start: time.Unix(1, 0), End: time.Unix(2, 0)}, Limit: 5})
	if err != nil || result.Metrics == nil || len(result.Metrics) != 0 {
		t.Fatalf("metric list changed: %v", err)
	}
}
