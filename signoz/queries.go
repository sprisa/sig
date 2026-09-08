package signoz

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const MaxQueryBytes = 1 << 20

func ValidateWindow(start, end time.Time) error {
	if start.UnixMilli() <= 0 || start.UnixMilli() >= end.UnixMilli() || start.Year() > 9999 || end.Year() > 9999 {
		return &Error{Code: "usage", Message: "start and end must be positive timestamps, with start at least one millisecond before end"}
	}
	return nil
}

func ValidateQuery(payload json.RawMessage) error {
	if len(payload) > MaxQueryBytes {
		return &Error{Code: "usage", Message: "query file exceeds 1 MiB"}
	}
	var request struct {
		Start          int64  `json:"start"`
		End            int64  `json:"end"`
		RequestType    string `json:"requestType"`
		CompositeQuery struct {
			Queries []json.RawMessage `json:"queries"`
		} `json:"compositeQuery"`
	}
	if err := json.Unmarshal(payload, &request); err != nil || request.Start <= 0 || request.End <= request.Start || len(request.CompositeQuery.Queries) == 0 {
		return &Error{Code: "usage", Message: "query must be one v5 JSON object with positive millisecond start/end bounds and compositeQuery.queries"}
	}
	if err := ValidateWindow(time.UnixMilli(request.Start), time.UnixMilli(request.End)); err != nil {
		return err
	}
	switch request.RequestType {
	case "raw", "scalar", "time_series", "trace":
	default:
		return &Error{Code: "usage", Message: "query requestType must be raw, scalar, time_series, or trace; streaming is not supported"}
	}
	return nil
}

// Query sends a native v5 request without translating its expressions or numbers.
// SQL authorization and execution safety remain the server's responsibility.
func (c *Client) Query(ctx context.Context, payload json.RawMessage, preview, verbose bool) (json.RawMessage, error) {
	if err := ValidateQuery(payload); err != nil {
		return nil, err
	}
	path := "/api/v5/query_range"
	var params url.Values
	if preview {
		path += "/preview"
		params = url.Values{"verbose": {strconv.FormatBool(verbose)}}
	}
	data, err := c.request(ctx, http.MethodPost, path, payload, params)
	if err != nil {
		return nil, err
	}
	if _, err := Object(data); err != nil {
		return nil, err
	}
	return data, nil
}

func (c *Client) MetricsQuery(ctx context.Context, expression string, start, end time.Time, step time.Duration, noCache bool) (json.RawMessage, error) {
	if err := ValidateWindow(start, end); err != nil {
		return nil, err
	}
	if strings.TrimSpace(expression) == "" || step < time.Second || step%time.Second != 0 {
		return nil, &Error{Code: "usage", Message: "PromQL is required and --step must be a positive whole number of seconds"}
	}
	if (end.UnixMilli()-start.UnixMilli())/step.Milliseconds()+1 > 11000 {
		return nil, &Error{Code: "usage", Message: "query exceeds 11000 points per series; increase --step or narrow the time window"}
	}
	payload, _ := json.Marshal(map[string]any{
		"schemaVersion": "v1", "start": start.UnixMilli(), "end": end.UnixMilli(), "requestType": "time_series",
		"noCache":        noCache,
		"compositeQuery": map[string]any{"queries": []any{map[string]any{"type": "promql", "spec": map[string]any{"name": "A", "query": expression, "step": int64(step / time.Second)}}}},
	})
	return c.Query(ctx, payload, false, false)
}

func validID(id string, size int) bool {
	if len(id) != size || strings.Trim(id, "0") == "" {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func (c *Client) Trace(ctx context.Context, id, selected string, expanded []string) (json.RawMessage, error) {
	if !validID(id, 32) || (selected != "" && !validID(selected, 16)) {
		return nil, &Error{Code: "usage", Message: "trace ID must be 32 hexadecimal characters and span IDs 16, and cannot be all zero"}
	}
	normalized := make([]string, len(expanded))
	for i, span := range expanded {
		if !validID(span, 16) {
			return nil, &Error{Code: "usage", Message: "expanded span IDs must be nonzero 16-character hexadecimal IDs"}
		}
		normalized[i] = strings.ToLower(span)
	}
	return c.request(ctx, http.MethodPost, "/api/v4/traces/"+strings.ToLower(id)+"/waterfall", map[string]any{"selectedSpanId": strings.ToLower(selected), "uncollapsedSpans": normalized}, nil)
}

type DiscoveryOptions struct {
	Start, End                                              time.Time
	Limit                                                   int
	Search, Name, Where, FieldContext, DataType, MetricName string
}

func (c *Client) Fields(ctx context.Context, signal string, values bool, opts DiscoveryOptions) (json.RawMessage, error) {
	if err := ValidateWindow(opts.Start, opts.End); err != nil {
		return nil, err
	}
	if signal != "logs" && signal != "traces" && signal != "metrics" {
		return nil, &Error{Code: "usage", Message: "discovery signal must be logs, traces, or metrics"}
	}
	if opts.Limit < 1 || opts.Limit > 1000 || (values && opts.Name == "") {
		return nil, &Error{Code: "usage", Message: "discovery needs a limit between 1 and 1000 and values requires a field name"}
	}
	params := url.Values{
		"signal": {signal}, "startUnixMilli": {strconv.FormatInt(opts.Start.UnixMilli(), 10)},
		"endUnixMilli": {strconv.FormatInt(opts.End.UnixMilli(), 10)}, "limit": {strconv.Itoa(opts.Limit)},
	}
	for key, value := range map[string]string{"searchText": opts.Search, "fieldContext": opts.FieldContext, "fieldDataType": opts.DataType, "metricName": opts.MetricName} {
		if value != "" {
			params.Set(key, value)
		}
	}
	path := "/api/v1/fields/keys"
	if values {
		path = "/api/v1/fields/values"
		params.Set("name", opts.Name)
		if opts.Where != "" {
			params.Set("existingQuery", opts.Where)
		}
	}
	data, err := c.request(ctx, http.MethodGet, path, nil, params)
	if err != nil {
		return nil, err
	}
	var result struct {
		Complete *bool           `json:"complete"`
		Keys     json.RawMessage `json:"keys"`
		Values   json.RawMessage `json:"values"`
	}
	if json.Unmarshal(data, &result) != nil || result.Complete == nil || (!values && len(result.Keys) == 0) || (values && len(result.Values) == 0) {
		return nil, &Error{Code: "invalid_response", Message: "API returned an unexpected field-discovery response"}
	}
	return data, nil
}

func (c *Client) MetricsList(ctx context.Context, start, end time.Time, limit int, search string) (json.RawMessage, error) {
	if err := ValidateWindow(start, end); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 5000 {
		return nil, &Error{Code: "usage", Message: "metric list limit must be between 1 and 5000"}
	}
	params := url.Values{"start": {strconv.FormatInt(start.UnixMilli(), 10)}, "end": {strconv.FormatInt(end.UnixMilli(), 10)}, "limit": {strconv.Itoa(limit)}}
	if search != "" {
		params.Set("searchText", search)
	}
	return c.request(ctx, http.MethodGet, "/api/v2/metrics", nil, params)
}

// Object is used to inspect metadata without converting large numbers to floats.
func Object(data json.RawMessage) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil || bytes.Equal(data, []byte("null")) {
		return nil, &Error{Code: "invalid_response", Message: "API response data must be a JSON object"}
	}
	return object, nil
}
