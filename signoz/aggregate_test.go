package signoz

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAggregateWireAndResults(t *testing.T) {
	for _, signal := range []Signal{Logs, Traces} {
		for _, step := range []time.Duration{0, time.Minute} {
			t.Run(string(signal)+"/"+step.String(), func(t *testing.T) {
				kind := "scalar"
				if step > 0 {
					kind = "time_series"
				}
				c := testClient(t, func(w http.ResponseWriter, req *http.Request) {
					if req.Method != "POST" || req.URL.Path != "/api/v5/query_range" {
						t.Error("wrong endpoint")
					}
					var wire struct {
						Start          int64  `json:"start"`
						End            int64  `json:"end"`
						RequestType    string `json:"requestType"`
						NoCache        bool   `json:"noCache"`
						CompositeQuery struct {
							Queries []struct {
								Type string `json:"type"`
								Spec struct {
									Signal       Signal `json:"signal"`
									Name         string `json:"name"`
									Limit        int    `json:"limit"`
									Step         *int64 `json:"stepInterval"`
									Aggregations []struct {
										Expression string `json:"expression"`
									} `json:"aggregations"`
									Order []struct {
										Key struct {
											Name string `json:"name"`
										} `json:"key"`
										Direction string `json:"direction"`
									} `json:"order"`
									GroupBy []struct {
										Name    string `json:"name"`
										Signal  Signal `json:"signal"`
										Context string `json:"fieldContext"`
									} `json:"groupBy"`
									Filter struct {
										Expression string `json:"expression"`
									} `json:"filter"`
								} `json:"spec"`
							} `json:"queries"`
						} `json:"compositeQuery"`
					}
					if err := json.UnmarshalRead(req.Body, &wire); err != nil {
						t.Error(err)
						return
					}
					if len(wire.CompositeQuery.Queries) != 1 {
						t.Error("expected one query")
						return
					}
					q := wire.CompositeQuery.Queries[0]
					s := q.Spec
					if wire.Start != 1000 || wire.End != 121000 || wire.RequestType != kind || !wire.NoCache || q.Type != "builder_query" || s.Signal != signal || s.Name != "A" || s.Limit != 5 {
						t.Error("wrong request metadata")
					}
					if len(s.Aggregations) != 1 || s.Aggregations[0].Expression != "count()" || len(s.Order) != 1 || s.Order[0].Key.Name != "count()" || s.Order[0].Direction != "asc" {
						t.Error("aggregation or order changed")
					}
					if len(s.GroupBy) != 3 {
						t.Error("missing grouping")
						return
					}
					if s.GroupBy[0].Name != "service.name" || s.GroupBy[0].Context != "" || s.GroupBy[1].Name != "service.name" || s.GroupBy[1].Context != "resource" || s.GroupBy[2].Name != "http.method" || s.GroupBy[2].Context != "attribute" || s.GroupBy[2].Signal != signal {
						t.Error("field context was guessed or lost")
					}
					if s.Filter.Expression != "service.name = 'synthetic'" || (s.Step == nil) != (step == 0) {
						t.Error("filter or bucket presence changed")
					}
					if s.Step != nil && *s.Step != 60 {
						t.Error("step is not in seconds")
					}
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, `{"status":"success","data":{"type":"`+kind+`","data":{"results":[{"queryName":"A","future":9007199254740993,"value":1.234567890123456789}]},"warning":{"message":"synthetic warning"}}}`)
				})
				result, err := c.Aggregate(context.Background(), AggregateRequest{Window: Window{Start: time.Unix(1, 0), End: time.Unix(121, 0)}, Signal: signal, Aggregation: "count()", GroupBy: []string{"service.name", "resource.service.name", "attribute.http.method"}, Where: "service.name = 'synthetic'", Limit: 5, Order: "asc", Step: step, NoCache: true})
				if err != nil {
					t.Fatal(err)
				}
				if result.Type != kind || !strings.Contains(string(result.Raw), "9007199254740993") || !strings.Contains(string(result.Results[0]), "1.234567890123456789") || !strings.Contains(string(result.Warning), "synthetic warning") {
					t.Fatal("lost response content")
				}
			})
		}
	}
}

func TestAggregateValidation(t *testing.T) {
	valid := AggregateRequest{Window: Window{Start: time.Unix(1, 0), End: time.Unix(3601, 0)}, Signal: Logs, Aggregation: "count()", Limit: 100, Order: "desc"}
	for _, expression := range []string{"count()", "rate()", "count_distinct(trace_id)", "avg(body.latency)", "sum(attribute.bytes)", "min(duration_nano)", "max(duration_nano)", "p50(duration_nano)", "p75(duration_nano)", "p90(duration_nano)", "p95(duration_nano)", "p99(duration_nano)"} {
		r := valid
		r.Aggregation = expression
		if err := r.Validate(); err != nil {
			t.Fatalf("%s rejected: %v", expression, err)
		}
	}
	for _, expression := range []string{"", "count", "avg()", "count(body)", "rate(body)", "sum(a,b)", "p100(duration_nano)", "count();SELECT 1", "avg(a+b)", "sum(a.)", "sum(a..b)"} {
		r := valid
		r.Aggregation = expression
		assertCode(t, r.Validate(), "usage")
	}
	for _, change := range []func(*AggregateRequest){
		func(r *AggregateRequest) { r.Signal = Metrics },
		func(r *AggregateRequest) { r.End = r.Start },
		func(r *AggregateRequest) { r.Limit = 0 },
		func(r *AggregateRequest) { r.Limit = 10001 },
		func(r *AggregateRequest) { r.Order = "sideways" },
		func(r *AggregateRequest) { r.Step = -time.Second },
		func(r *AggregateRequest) { r.Step = time.Millisecond },
		func(r *AggregateRequest) { r.Step = time.Second; r.End = r.Start.Add(4 * time.Hour) },
		func(r *AggregateRequest) { r.GroupBy = []string{""} },
		func(r *AggregateRequest) { r.GroupBy = []string{"service.name", "service.name"} },
		func(r *AggregateRequest) { r.GroupBy = []string{"sum(a)"} },
		func(r *AggregateRequest) { r.GroupBy = make([]string, 17) },
		func(r *AggregateRequest) { r.Where = strings.Repeat("x", MaxAggregateTextBytes) },
	} {
		r := valid
		change(&r)
		c := testClient(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid aggregation reached API") })
		_, err := c.Aggregate(context.Background(), r)
		assertCode(t, err, "usage")
	}
}

func TestAggregateTextBudget(t *testing.T) {
	r := AggregateRequest{Window: Window{Start: time.Unix(1, 0), End: time.Unix(2, 0)}, Signal: Logs, Aggregation: "count()", Limit: 1, Order: "desc"}
	for _, fragment := range []string{"x", `"`, `\`, "\u00e9"} {
		t.Run(fragment, func(t *testing.T) {
			// Include a plausible filter shell; the budget measures input bytes,
			// not JSON escape expansion, code points, or the generated envelope.
			prefix, suffix := "body CONTAINS '", "'"
			remaining := MaxAggregateTextBytes - len(r.Aggregation) - len(prefix) - len(suffix)
			r.Where = prefix + strings.Repeat(fragment, remaining/len(fragment)) + strings.Repeat("x", remaining%len(fragment)) + suffix
			if err := r.Validate(); err != nil {
				t.Fatalf("exact text budget rejected: %v", err)
			}
			encoded, err := json.Marshal(struct {
				Expression string `json:"expression"`
			}{r.Where})
			if err != nil {
				t.Fatal(err)
			}
			if (fragment == `"` || fragment == `\`) && len(encoded) <= MaxAggregateTextBytes {
				t.Fatal("fixture did not exercise JSON escape expansion")
			}
			r.Where += "x"
			err = r.Validate()
			assertCode(t, err, "usage")
			if err.Error() != "combined aggregation and filter input text exceeds 1 MiB" {
				t.Fatal("size error describes the wrong boundary")
			}
			r.Where = r.Where[:len(r.Where)-2]
			if err := r.Validate(); err != nil {
				t.Fatalf("below text budget rejected: %v", err)
			}
		})
	}
}

func TestAggregateRejectsWrongResults(t *testing.T) {
	for _, data := range []string{`{"type":"raw","data":{"results":[]}}`, `{"type":"scalar","data":{}}`, `{"type":"scalar","data":{"results":null}}`} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"status":"success","data":`+data+`}`)
		})
		_, err := c.Aggregate(context.Background(), AggregateRequest{Window: Window{Start: time.Unix(1, 0), End: time.Unix(2, 0)}, Signal: Logs, Aggregation: "count()", Limit: 1, Order: "desc"})
		assertCode(t, err, "invalid_response")
	}
}
