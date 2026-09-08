package signoz

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestSupportedOperationsRejectWrongShapes(t *testing.T) {
	w := Window{Start: time.Unix(1, 0), End: time.Unix(2, 0)}
	q, err := ParseQuery([]byte(`{"start":1000,"end":2000,"requestType":"scalar","compositeQuery":{"queries":[{}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, body string
		call       func(*Client) error
	}{
		{"promql empty object", `{}`, func(c *Client) error {
			_, e := c.MetricsQuery(context.Background(), MetricsQueryRequest{Window: w, Expression: "vector(1)", Step: time.Second})
			return e
		}},
		{"wrong query kind", `{"type":"time_series","data":{"results":[]}}`, func(c *Client) error { _, e := c.Query(context.Background(), q); return e }},
		{"missing result array", `{"type":"scalar","data":{}}`, func(c *Client) error { _, e := c.Query(context.Background(), q); return e }},
		{"preview missing verdict", `{"compositeQuery":{"A":{}}}`, func(c *Client) error { _, e := c.Preview(context.Background(), q, false); return e }},
		{"trace missing flags", `{"spans":[]}`, func(c *Client) error {
			_, e := c.Trace(context.Background(), TraceRequest{ID: "0123456789abcdef0123456789abcdef"})
			return e
		}},
		{"metric array required", `{"metrics":{}}`, func(c *Client) error {
			_, e := c.MetricsList(context.Background(), MetricsListRequest{Window: w, Limit: 5})
			return e
		}},
		{"discovery keys required", `{"complete":true}`, func(c *Client) error {
			_, e := c.FieldKeys(context.Background(), FieldKeysRequest{Window: w, Signal: Logs, Limit: 5})
			return e
		}},
		{"typed values required", `{"complete":true,"values":{"numberValues":[true]}}`, func(c *Client) error {
			_, e := c.FieldValues(context.Background(), FieldValuesRequest{FieldKeysRequest: FieldKeysRequest{Window: w, Signal: Logs, Limit: 5}, Name: "example"})
			return e
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"status":"success","data":%s}`, test.body)
			})
			assertCode(t, test.call(c), "invalid_response")
		})
	}
}

func TestWarningsDominateContinuation(t *testing.T) {
	r := SearchRequest{Window: Window{Start: time.Unix(1, 0), End: time.Unix(2, 0)}, Signal: Logs, Limit: 1}
	calls := 0
	result, err := collectPages(context.Background(), r, PageBudget{Pages: 3}, func(context.Context, SearchRequest) (SearchPage, error) {
		calls++
		page := SearchPage{Rows: []json.RawMessage{json.RawMessage(`{"data":{}}`)}, NextCursor: "unsafe-cursor"}
		if calls == 2 {
			page.Warning = json.RawMessage(`{"message":"synthetic warning"}`)
		}
		return page, nil
	})
	if err != nil || calls != 2 || result.Completeness != "unknown" || len(result.Warnings) != 1 {
		t.Fatalf("warning state lost: %v", err)
	}
}
