package scripts

import (
	"bytes"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"reflect"
	"slices"
	"testing"
)

type aggregationComparison struct {
	Fields map[string]jsontext.Value       `json:",embed"`
	Series comparisonArray[jsontext.Value] `json:"series"`
}

type aggregateComparison struct {
	QueryName    string                                 `json:"queryName"`
	Fields       map[string]jsontext.Value              `json:",embed"`
	Columns      comparisonArray[jsontext.Value]        `json:"columns"`
	Data         comparisonArray[jsontext.Value]        `json:"data"`
	Aggregations comparisonArray[aggregationComparison] `json:"aggregations"`
}

// Keep absence distinct from explicit null and empty arrays in the typed view.
type comparisonArray[T any] struct {
	Present bool
	Values  []T
}

func (a *comparisonArray[T]) UnmarshalJSONFrom(d *jsontext.Decoder) error {
	if err := json.UnmarshalDecode(d, &a.Values); err != nil {
		return err
	}
	a.Present = true
	return nil
}

// Build a separate comparison view. Only the order of series is ignored;
// duplicates, unknown fields, numeric text, and null/empty distinctions remain.
func aggregateComparisonView(results []any, timeSeries bool) ([]aggregateComparison, error) {
	invalid := errors.New("unexpected aggregation result structure; contents suppressed")
	if results == nil {
		return nil, invalid
	}
	// v1's deterministic map encoding gives opaque fields a stable representation
	// while preserving the json.Number values supplied by the independent harness.
	encoded, err := jsonv1.Marshal(results)
	if err != nil {
		return nil, invalid
	}
	var view []aggregateComparison
	if json.Unmarshal(encoded, &view) != nil {
		return nil, invalid
	}
	for _, result := range view {
		if result.QueryName == "" {
			return nil, invalid
		}
		if !timeSeries {
			if !result.Columns.Present || !result.Data.Present {
				return nil, invalid
			}
			continue
		}
		if !result.Aggregations.Present {
			return nil, invalid
		}
		for _, aggregation := range result.Aggregations.Values {
			if !aggregation.Series.Present {
				return nil, invalid
			}
			for _, item := range aggregation.Series.Values {
				if item.Kind() != '{' {
					return nil, invalid
				}
				var fields struct {
					Labels comparisonArray[jsontext.Value] `json:"labels"`
					Values comparisonArray[jsontext.Value] `json:"values"`
				}
				if json.Unmarshal(item, &fields) != nil || !fields.Labels.Present || !fields.Values.Present {
					return nil, invalid
				}
			}
			slices.SortFunc(aggregation.Series.Values, func(a, b jsontext.Value) int { return bytes.Compare(a, b) })
		}
	}
	return view, nil
}

func compareAggregateResults(actual, expected []any, timeSeries bool) error {
	a, err := aggregateComparisonView(actual, timeSeries)
	if err != nil {
		return err
	}
	b, err := aggregateComparisonView(expected, timeSeries)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(a, b) {
		return errors.New("CLI and direct API aggregation results differ; contents suppressed")
	}
	return nil
}

func TestAggregateComparison(t *testing.T) {
	const a = `{"labels":[{"name":"service.name","value":"synthetic-a"}],"values":[{"timestamp":1000,"value":9007199254740993}],"future":true}`
	const b = `{"labels":[],"values":[{"timestamp":1000,"value":1.234567890123456789}]}`
	wrap := func(series string) string {
		return `[{"queryName":"A","aggregations":[{"index":0,"series":` + series + `}]}]`
	}
	decode := func(text string) []any {
		t.Helper()
		var result []any
		d := jsonv1.NewDecoder(bytes.NewBufferString(text))
		d.UseNumber()
		if err := d.Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	for _, tc := range []struct {
		name, actual, expected string
		timeSeries, equal      bool
	}{
		{"empty results", `[]`, `[]`, true, true},
		{"null results", `null`, `null`, true, false},
		{"reordered", wrap("[" + a + "," + b + "]"), wrap("[" + b + "," + a + "]"), true, true},
		{"missing series", wrap("[" + a + "]"), wrap("[" + a + "," + b + "]"), true, false},
		{"duplicates differ", wrap("[" + a + "," + a + "," + b + "]"), wrap("[" + a + "," + b + "," + b + "]"), true, false},
		{"duplicates preserved", wrap("[" + a + "," + a + "," + b + "]"), wrap("[" + a + "," + b + "," + a + "]"), true, true},
		{"changed point", wrap("[" + a + "]"), wrap(`[{"labels":[{"name":"service.name","value":"synthetic-a"}],"values":[{"timestamp":1000,"value":9007199254740992}],"future":true}]`), true, false},
		{"changed decimal", wrap("[" + b + "]"), wrap(`[{"labels":[],"values":[{"timestamp":1000,"value":1.234567890123456788}]}]`), true, false},
		{"changed unknown field", wrap("[" + a + "]"), wrap(`[{"labels":[{"name":"service.name","value":"synthetic-a"}],"values":[{"timestamp":1000,"value":9007199254740993}],"future":false}]`), true, false},
		{"point order matters", wrap(`[{"labels":[],"values":[{"value":1},{"value":2}]}]`), wrap(`[{"labels":[],"values":[{"value":2},{"value":1}]}]`), true, false},
		{"numeric text matters", wrap(`[{"labels":[],"values":[{"value":-0}]}]`), wrap(`[{"labels":[],"values":[{"value":0}]}]`), true, false},
		{"null series", wrap("null"), wrap("null"), true, true},
		{"empty series", wrap("[]"), wrap("[]"), true, true},
		{"null versus empty series", wrap("null"), wrap("[]"), true, false},
		{"missing series member", `[{"queryName":"A","aggregations":[{}]}]`, wrap("null"), true, false},
		{"missing on both sides", `[{"queryName":"A","aggregations":[{}]}]`, `[{"queryName":"A","aggregations":[{}]}]`, true, false},
		{"wrong series shape", wrap(`{}`), wrap(`{}`), true, false},
		{"wrong series element", wrap(`[1]`), wrap(`[1]`), true, false},
		{"missing point collection", wrap(`[{"labels":[]}]`), wrap(`[{"labels":[]}]`), true, false},
		{"missing labels", wrap(`[{"values":[]}]`), wrap(`[{"values":[]}]`), true, false},
		{"null aggregations", `[{"queryName":"A","aggregations":null}]`, `[{"queryName":"A","aggregations":null}]`, true, true},
		{"result metadata differs", `[{"queryName":"A","aggregations":null,"future":1}]`, `[{"queryName":"A","aggregations":null,"future":2}]`, true, false},
		{"missing aggregations", `[{"queryName":"A"}]`, `[{"queryName":"A","aggregations":null}]`, true, false},
		{"null versus empty aggregations", `[{"queryName":"A","aggregations":null}]`, `[{"queryName":"A","aggregations":[]}]`, true, false},
		{"invalid query result", `[null]`, `[null]`, true, false},
		{"scalar", `[{"queryName":"A","columns":[],"data":[[9007199254740993]]}]`, `[{"queryName":"A","columns":[],"data":[[9007199254740993]]}]`, false, true},
		{"changed scalar", `[{"queryName":"A","columns":[],"data":[[9007199254740993]]}]`, `[{"queryName":"A","columns":[],"data":[[9007199254740992]]}]`, false, false},
		{"missing scalar data", `[{"queryName":"A","columns":[]}]`, `[{"queryName":"A","columns":[]}]`, false, false},
		{"null scalar data", `[{"queryName":"A","columns":[],"data":null}]`, `[{"queryName":"A","columns":[],"data":null}]`, false, true},
		{"null versus empty scalar data", `[{"queryName":"A","columns":[],"data":null}]`, `[{"queryName":"A","columns":[],"data":[]}]`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual, expected := decode(tc.actual), decode(tc.expected)
			beforeA, _ := jsonv1.Marshal(actual)
			beforeB, _ := jsonv1.Marshal(expected)
			err := compareAggregateResults(actual, expected, tc.timeSeries)
			if (err == nil) != tc.equal {
				t.Fatalf("unexpected comparison: %v", err)
			}
			afterA, _ := jsonv1.Marshal(actual)
			afterB, _ := jsonv1.Marshal(expected)
			if !bytes.Equal(beforeA, afterA) || !bytes.Equal(beforeB, afterB) {
				t.Fatal("comparison mutated its inputs")
			}
		})
	}
}
