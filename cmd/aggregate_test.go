package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAggregateCommand(t *testing.T) {
	for _, signal := range []string{"logs", "traces"} {
		t.Run(signal, func(t *testing.T) {
			h := newHarness(t)
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var wire struct {
					RequestType    string
					NoCache        bool
					CompositeQuery struct {
						Queries []struct {
							Spec struct {
								Signal       string
								Limit        int
								StepInterval int
								GroupBy      []struct{ Name, FieldContext string }
								Order        []struct{ Direction string }
							}
						}
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
					t.Error(err)
					return
				}
				if len(wire.CompositeQuery.Queries) != 1 {
					t.Error("missing query")
					return
				}
				spec := wire.CompositeQuery.Queries[0].Spec
				if wire.RequestType != "time_series" || !wire.NoCache || spec.Signal != signal || spec.Limit != 5 || spec.StepInterval != 60 || len(spec.GroupBy) != 2 || len(spec.Order) != 1 || spec.Order[0].Direction != "asc" {
					t.Error("CLI flags did not reach generated request")
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"status":"success","data":{"type":"time_series","data":{"results":[]},"warning":{"message":"synthetic"}}}`)
			}))
			defer s.Close()
			h.env["SIGNOZ_URL"], h.env["SIGNOZ_API_KEY"] = s.URL, "synthetic-key"
			got := h.run(t, "", 0, signal, "aggregate", "--aggregation", "count()", "--group-by", "resource.service.name,attribute.http.method", "--since", "1h", "--step", "1m", "--limit", "5", "--order", "asc", "--no-cache")
			if calls != 1 || !strings.Contains(got, `"completeness":"unknown"`) || !strings.Contains(got, `"step_seconds":60`) || !strings.Contains(got, `"warning":{"message":"synthetic"}`) {
				t.Fatal("aggregation metadata or warning lost")
			}
			for _, args := range [][]string{
				{}, {"--aggregation", "sum()"}, {"--aggregation", "count()", "--step", "0s"},
				{"--aggregation", "count()", "--step", "-1s"}, {"--aggregation", "count()", "--step", "1ms"},
				{"--aggregation", "count()", "--limit", "10001"}, {"--aggregation", "count()", "--order", "random"},
				{"--aggregation", "count()", "--pages", "2"}, {"--aggregation", "count()", "unexpected"},
			} {
				h.run(t, "", 2, append([]string{signal, "aggregate"}, args...)...)
			}
			if calls != 1 {
				t.Fatal("invalid arguments reached API")
			}
			schema := h.run(t, "", 0, "agent", "schema", signal, "aggregate")
			if !strings.Contains(schema, `"effect":"read"`) || !strings.Contains(schema, `"pagination":"bounded_groups"`) {
				t.Fatal("missing aggregation safety metadata")
			}
		})
	}
}

func TestAggregateCommandHTTPFailure(t *testing.T) {
	h := newHarness(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadRequest) }))
	defer s.Close()
	h.env["SIGNOZ_URL"], h.env["SIGNOZ_API_KEY"] = s.URL, "synthetic-key"
	h.run(t, "", 6, "logs", "aggregate", "--aggregation", "count()")
}
