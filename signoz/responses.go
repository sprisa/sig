package signoz

import (
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	"encoding/json/v2"
)

// Raw preserves fields not interpreted by the CLI, including large JSON numbers.
// The typed views own validation and metadata; callers never reparse envelopes.
type Identity struct {
	ID  string
	Raw jsontext.Value
}
type QueryResult struct {
	Type    string
	Results []jsontext.Value
	Warning jsontext.Value
	Raw     jsontext.Value
}
type PreviewVerdict struct {
	Valid bool
	Error jsontext.Value
}
type PreviewResult struct {
	Queries map[string]PreviewVerdict
	Raw     jsontext.Value
}
type TraceResult struct {
	Spans                    []jsontext.Value
	HasMore, HasMissingSpans bool
	Raw                      jsontext.Value
}
type MetricsListResult struct{ Metrics []jsontext.Value }
type FieldKey struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	Unit          string `json:"unit"`
	Signal        string `json:"signal"`
	FieldContext  string `json:"fieldContext"`
	FieldDataType string `json:"fieldDataType"`
}
type FieldValues struct {
	StringValues  []string        `json:"stringValues"`
	NumberValues  []jsonv1.Number `json:"numberValues"`
	BoolValues    []bool          `json:"boolValues"`
	RelatedValues []string        `json:"relatedValues"`
}
type FieldKeysResult struct {
	Keys     map[string][]FieldKey
	Complete bool
	Raw      jsontext.Value
}
type FieldValuesResult struct {
	Values   FieldValues
	Complete bool
	Raw      jsontext.Value
}

func invalidResponse(message string) error { return &Error{Code: "invalid_response", Message: message} }

// Track a required array's presence without copying the entire array into a
// RawMessage. Explicit null remains distinct from an absent field; endpoints
// decide whether to normalize null to an empty slice. Each row owns its bytes.
type nullableRows struct {
	Values  []jsontext.Value
	Present bool
}

func (r *nullableRows) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	var rows []jsontext.Value
	if err := json.UnmarshalDecode(dec, &rows); err != nil {
		return err
	}
	r.Values, r.Present = rows, true
	return nil
}

func decodeQueryResult(data jsontext.Value, kind string) (QueryResult, error) {
	var wire struct {
		Type string `json:"type"`
		Data struct {
			Results []jsontext.Value `json:"results"`
		} `json:"data"`
		Warning jsontext.Value `json:"warning"`
	}
	if json.Unmarshal(data, &wire) != nil || wire.Type != kind || wire.Data.Results == nil {
		return QueryResult{}, invalidResponse("unexpected query result type or missing result array")
	}
	return QueryResult{Type: wire.Type, Results: wire.Data.Results, Warning: wire.Warning, Raw: data}, nil
}

func decodePreview(data jsontext.Value) (PreviewResult, error) {
	var wire struct {
		CompositeQuery map[string]struct {
			Valid *bool          `json:"valid"`
			Error jsontext.Value `json:"error"`
		} `json:"compositeQuery"`
	}
	if json.Unmarshal(data, &wire) != nil || wire.CompositeQuery == nil {
		return PreviewResult{}, invalidResponse("preview is missing query verdicts")
	}
	result := PreviewResult{Raw: data, Queries: map[string]PreviewVerdict{}}
	for name, verdict := range wire.CompositeQuery {
		if verdict.Valid == nil {
			return PreviewResult{}, invalidResponse("preview is missing a validity verdict")
		}
		result.Queries[name] = PreviewVerdict{Valid: *verdict.Valid, Error: verdict.Error}
	}
	return result, nil
}

func (r TraceResult) Completeness() string {
	if r.HasMore || r.HasMissingSpans {
		return "partial"
	}
	return "complete"
}
func discoveryCompleteness(complete bool) string {
	if complete {
		return "complete"
	}
	return "unknown"
}
func (r FieldKeysResult) Completeness() string   { return discoveryCompleteness(r.Complete) }
func (r FieldValuesResult) Completeness() string { return discoveryCompleteness(r.Complete) }
