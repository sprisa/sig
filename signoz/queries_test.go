package signoz

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTraceSearchRequest(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Start, End     int64
			CompositeQuery struct {
				Queries []struct {
					Spec struct {
						Signal        string
						Offset, Limit int
						Order         []struct {
							Key       struct{ Name string }
							Direction string
						}
					}
				}
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		spec := body.CompositeQuery.Queries[0].Spec
		if spec.Signal != "traces" || spec.Offset != 4 || spec.Limit != 2 || len(spec.Order) != 3 || spec.Order[1].Key.Name != "trace_id" || spec.Order[2].Key.Name != "span_id" {
			t.Error("incorrect deterministic trace search")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":[]}]}}}`)
	})
	_, err := c.Search(context.Background(), SearchRequest{Window: Window{Start: time.Unix(1, 0), End: time.Unix(2, 0)}, Signal: Traces, Limit: 2, Offset: 4})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTraceDetailRequest(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/traces/0123456789abcdef0123456789abcdef/waterfall" || r.Method != "POST" {
			t.Error("wrong trace detail route")
		}
		var body struct {
			Selected string   `json:"selectedSpanId"`
			Expanded []string `json:"uncollapsedSpans"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Selected != "0123456789abcdef" || len(body.Expanded) != 1 || body.Expanded[0] != "fedcba9876543210" {
			t.Error("wrong trace detail body")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"success","data":{"spans":[],"hasMore":true,"hasMissingSpans":true,"totalSpansCount":9007199254740993}}`)
	})
	expanded := []string{"FEDCBA9876543210"}
	data, err := c.Trace(context.Background(), TraceRequest{ID: "0123456789ABCDEF0123456789ABCDEF", SelectedSpan: "0123456789ABCDEF", ExpandedSpans: expanded})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data.Raw), "9007199254740993") || expanded[0] != "FEDCBA9876543210" || data.Completeness() != "partial" {
		t.Error("trace precision or caller input changed")
	}
	for _, id := range []string{"../auth", strings.Repeat("0", 32), "short", strings.Repeat("z", 32)} {
		_, err := c.Trace(context.Background(), TraceRequest{ID: id})
		assertCode(t, err, "usage")
	}
}

func TestPromQLRequest(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Start, End     int64
			RequestType    string
			NoCache        bool
			CompositeQuery struct {
				Queries []struct {
					Type string
					Spec struct {
						Name, Query string
						Step        int
					}
				}
			}
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatal("decode")
		}
		q := body.CompositeQuery.Queries[0]
		if body.Start != 1000 || body.End != 121000 || !body.NoCache || body.RequestType != "time_series" || q.Type != "promql" || q.Spec.Query != "vector(1)" || q.Spec.Step != 60 {
			t.Error("incorrect PromQL request")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"success","data":{"type":"time_series","data":{"results":[{"queryName":"A","aggregations":[]}]},"warning":{"message":"synthetic warning"}}}`)
	})
	data, err := c.MetricsQuery(context.Background(), MetricsQueryRequest{Window: Window{Start: time.Unix(1, 0), End: time.Unix(121, 0)}, Expression: "vector(1)", Step: time.Minute, NoCache: true})
	if err != nil || !strings.Contains(string(data.Raw), "synthetic warning") {
		t.Fatalf("PromQL result: %v", err)
	}
	for _, step := range []time.Duration{0, -time.Second, time.Millisecond, 1500 * time.Millisecond} {
		_, err := c.MetricsQuery(context.Background(), MetricsQueryRequest{Window: Window{Start: time.Unix(1, 0), End: time.Unix(121, 0)}, Expression: "vector(1)", Step: step})
		assertCode(t, err, "usage")
	}
	_, err = c.MetricsQuery(context.Background(), MetricsQueryRequest{Window: Window{Start: time.Unix(1, 0), End: time.Unix(12000, 0)}, Expression: "vector(1)", Step: time.Second})
	assertCode(t, err, "usage")
}

func TestDiscoveryQueryEncoding(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method != "GET" || r.URL.Path != "/api/v1/fields/values" || q.Get("signal") != "metrics" || q.Get("name") != "service.name" || q.Get("metricName") != "provider/requests.total" || q.Get("searchText") != "api & jobs" || q.Get("existingQuery") != "service.name = 'checkout'" || q.Get("startUnixMilli") != "1000" || q.Get("endUnixMilli") != "2000" {
			t.Error("incorrect discovery query encoding")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"success","data":{"values":{"numberValues":[9007199254740993]},"complete":false}}`)
	})
	data, err := c.FieldValues(context.Background(), FieldValuesRequest{FieldKeysRequest: FieldKeysRequest{Window: Window{Start: time.Unix(1, 0), End: time.Unix(2, 0)}, Signal: Metrics, Limit: 50, MetricName: "provider/requests.total", Search: "api & jobs"}, Name: "service.name", Where: "service.name = 'checkout'"})
	if err != nil || !strings.Contains(string(data.Raw), "9007199254740993") {
		t.Fatalf("discovery failed: %v", err)
	}
}

func TestMetricsListRequest(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/metrics" || r.URL.Query().Get("start") != "1000" || r.URL.Query().Get("end") != "2000" || r.URL.Query().Get("searchText") != "cpu & memory" || r.URL.Query().Get("limit") != "5" {
			t.Error("wrong metric listing parameters")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"success","data":{"metrics":[]}}`)
	})
	_, err := c.MetricsList(context.Background(), MetricsListRequest{Window: Window{Start: time.Unix(1, 0), End: time.Unix(2, 0)}, Limit: 5, Search: "cpu & memory"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeQueryAndPreview(t *testing.T) {
	payload := json.RawMessage(`{"start":1000,"end":2000,"requestType":"scalar","compositeQuery":{"queries":[{"type":"builder_query","spec":{"name":"A","signal":"logs","aggregations":[{"expression":"count()"}]}}]},"variables":{"large":{"type":"custom","value":9007199254740993}}}`)
	for _, preview := range []bool{false, true} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			if string(b) != string(payload) {
				t.Error("native JSON payload was translated or lost precision")
			}
			if preview && (r.URL.Path != "/api/v5/query_range/preview" || r.URL.Query().Get("verbose") != "false") {
				t.Error("incorrect preview endpoint/options")
			}
			if !preview && (r.URL.Path != "/api/v5/query_range" || r.URL.RawQuery != "") {
				t.Error("incorrect run endpoint")
			}
			w.Header().Set("Content-Type", "application/json")
			if preview {
				io.WriteString(w, `{"status":"success","data":{"compositeQuery":{"A":{"valid":false,"error":{"message":"synthetic error"}}}}}`)
			} else {
				io.WriteString(w, `{"status":"success","data":{"type":"scalar","data":{"results":[{"queryName":"A","columns":[],"data":[[9007199254740993]]}]}}}`)
			}
		})
		query, err := ParseQuery(payload)
		if err != nil {
			t.Fatal(err)
		}
		if preview {
			data, err := c.Preview(context.Background(), query, false)
			if err != nil || data.Queries["A"].Valid || !strings.Contains(string(data.Raw), `"valid":false`) {
				t.Fatalf("preview verdict lost: %v", err)
			}
		} else {
			data, err := c.Query(context.Background(), query)
			if err != nil || data.Type != "scalar" || !strings.Contains(string(data.Raw), "9007199254740993") {
				t.Fatalf("scalar result lost: %v", err)
			}
		}
	}
	for _, payload := range []string{`[]`, `{}`, `null`, `{"start":0,"end":2000}`, `{"start":1000,"end":2000,"requestType":"raw_stream","compositeQuery":{"queries":[{}]}}`, strings.Repeat(" ", MaxQueryBytes+1)} {
		_, err := ParseQuery([]byte(payload))
		assertCode(t, err, "usage")
	}
}
