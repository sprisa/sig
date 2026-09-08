package signoz

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (c *Client) Query(ctx context.Context, q NativeQuery) (QueryResult, error) {
	if q.kind == "" {
		return QueryResult{}, usage("native query must be parsed before execution")
	}
	data, err := request[jsontext.Value](ctx, c, http.MethodPost, "/api/v5/query_range", q.payload, nil)
	if err != nil {
		return QueryResult{}, err
	}
	return decodeQueryResult(data, q.kind)
}

func (c *Client) Preview(ctx context.Context, q NativeQuery, verbose bool) (PreviewResult, error) {
	if q.kind == "" {
		return PreviewResult{}, usage("native query must be parsed before preview")
	}
	data, err := request[jsontext.Value](ctx, c, http.MethodPost, "/api/v5/query_range/preview", q.payload, url.Values{"verbose": {strconv.FormatBool(verbose)}})
	if err != nil {
		return PreviewResult{}, err
	}
	return decodePreview(data)
}

func (c *Client) MetricsQuery(ctx context.Context, r MetricsQueryRequest) (QueryResult, error) {
	if err := r.Validate(); err != nil {
		return QueryResult{}, err
	}
	data, err := request[jsontext.Value](ctx, c, http.MethodPost, "/api/v5/query_range", queryRequest[promSpec]{
		SchemaVersion: "v1", Start: r.Start.UnixMilli(), End: r.End.UnixMilli(), RequestType: "time_series", NoCache: r.NoCache,
		CompositeQuery: compositeQuery[promSpec]{Queries: [1]queryEnvelope[promSpec]{{Type: "promql", Spec: promSpec{Name: "A", Query: r.Expression, Step: int64(r.Step / time.Second)}}}},
	}, nil)
	if err != nil {
		return QueryResult{}, err
	}
	return decodeQueryResult(data, "time_series")
}

func (c *Client) Trace(ctx context.Context, r TraceRequest) (TraceResult, error) {
	if err := r.Validate(); err != nil {
		return TraceResult{}, err
	}
	expanded := make([]string, len(r.ExpandedSpans))
	for i, id := range r.ExpandedSpans {
		expanded[i] = strings.ToLower(id)
	}
	body := struct {
		Selected string   `json:"selectedSpanId"`
		Expanded []string `json:"uncollapsedSpans"`
	}{strings.ToLower(r.SelectedSpan), expanded}
	data, err := request[jsontext.Value](ctx, c, http.MethodPost, "/api/v4/traces/"+strings.ToLower(r.ID)+"/waterfall", body, nil)
	if err != nil {
		return TraceResult{}, err
	}
	var wire struct {
		Spans           nullableRows `json:"spans"`
		HasMore         *bool        `json:"hasMore"`
		HasMissingSpans *bool        `json:"hasMissingSpans"`
	}
	if json.Unmarshal(data, &wire) != nil || !wire.Spans.Present || wire.HasMore == nil || wire.HasMissingSpans == nil {
		return TraceResult{}, invalidResponse("unexpected trace waterfall response")
	}
	return TraceResult{Raw: data, Spans: wire.Spans.Values, HasMore: *wire.HasMore, HasMissingSpans: *wire.HasMissingSpans}, nil
}

func (c *Client) MetricsList(ctx context.Context, r MetricsListRequest) (MetricsListResult, error) {
	if err := r.Validate(); err != nil {
		return MetricsListResult{}, err
	}
	params := url.Values{"start": {strconv.FormatInt(r.Start.UnixMilli(), 10)}, "end": {strconv.FormatInt(r.End.UnixMilli(), 10)}, "limit": {strconv.Itoa(r.Limit)}}
	if r.Search != "" {
		params.Set("searchText", r.Search)
	}
	wire, err := request[struct {
		Metrics nullableRows `json:"metrics"`
	}](ctx, c, http.MethodGet, "/api/v2/metrics", nil, params)
	if err != nil {
		return MetricsListResult{}, err
	}
	if !wire.Metrics.Present {
		return MetricsListResult{}, invalidResponse("metric listing did not contain an array")
	}
	metrics := wire.Metrics.Values
	if metrics == nil {
		metrics = []jsontext.Value{}
	}
	return MetricsListResult{Metrics: metrics}, nil
}

type queryRequest[S any] struct {
	SchemaVersion  string            `json:"schemaVersion"`
	Start          int64             `json:"start"`
	End            int64             `json:"end"`
	RequestType    string            `json:"requestType"`
	NoCache        bool              `json:"noCache,omitzero"`
	CompositeQuery compositeQuery[S] `json:"compositeQuery"`
}
type compositeQuery[S any] struct {
	Queries [1]queryEnvelope[S] `json:"queries"`
}
type queryEnvelope[S any] struct {
	Type string `json:"type"`
	Spec S      `json:"spec"`
}
type promSpec struct {
	Name  string `json:"name"`
	Query string `json:"query"`
	Step  int64  `json:"step"`
}
