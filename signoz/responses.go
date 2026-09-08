package signoz

import "encoding/json"

// Raw preserves fields not interpreted by the CLI, including large JSON numbers.
// The typed views own validation and metadata; callers never reparse envelopes.
type Identity struct {
	ID  string
	Raw json.RawMessage
}
type QueryResult struct {
	Type    string
	Results []json.RawMessage
	Warning json.RawMessage
	Raw     json.RawMessage
}
type PreviewVerdict struct {
	Valid bool
	Error json.RawMessage
}
type PreviewResult struct {
	Queries map[string]PreviewVerdict
	Raw     json.RawMessage
}
type TraceResult struct {
	Spans                    []json.RawMessage
	HasMore, HasMissingSpans bool
	Raw                      json.RawMessage
}
type MetricsListResult struct{ Metrics []json.RawMessage }
type FieldKey struct{ Name, Description, Unit, Signal, FieldContext, FieldDataType string }
type FieldValues struct {
	StringValues  []string      `json:"stringValues"`
	NumberValues  []json.Number `json:"numberValues"`
	BoolValues    []bool        `json:"boolValues"`
	RelatedValues []string      `json:"relatedValues"`
}
type FieldKeysResult struct {
	Keys     map[string][]FieldKey
	Complete bool
	Raw      json.RawMessage
}
type FieldValuesResult struct {
	Values   FieldValues
	Complete bool
	Raw      json.RawMessage
}

func invalidResponse(message string) error { return &Error{Code: "invalid_response", Message: message} }

func decodeQueryResult(data json.RawMessage, kind string) (QueryResult, error) {
	var wire struct {
		Type    string
		Data    struct{ Results json.RawMessage }
		Warning json.RawMessage
	}
	var results []json.RawMessage
	if json.Unmarshal(data, &wire) != nil || wire.Type != kind || json.Unmarshal(wire.Data.Results, &results) != nil || results == nil {
		return QueryResult{}, invalidResponse("unexpected query result type or missing result array")
	}
	return QueryResult{Type: wire.Type, Results: results, Warning: wire.Warning, Raw: data}, nil
}

func decodePreview(data json.RawMessage) (PreviewResult, error) {
	var wire struct {
		CompositeQuery map[string]struct {
			Valid *bool
			Error json.RawMessage
		}
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
