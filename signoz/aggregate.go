package signoz

import (
	"context"
	"encoding/json/jsontext"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Dedicated aggregations accept one function and at most one field, not SQL or
// arbitrary expressions. More complex queries belong in the native query path.
var aggregationPattern = regexp.MustCompile(`^(count|rate|count_distinct|avg|sum|min|max|p50|p75|p90|p95|p99)\(([A-Za-z_][A-Za-z0-9_.]*)?\)$`)
var aggregateFieldPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z0-9_]+)*$`)

// MaxAggregateTextBytes bounds the combined aggregation and filter input text
// in UTF-8 bytes, before JSON escaping. It is not an encoded request-body limit.
const MaxAggregateTextBytes = 1 << 20

type AggregateRequest struct {
	Window
	Signal             Signal
	Aggregation, Where string
	GroupBy            []string
	Limit              int
	Order              string
	Step               time.Duration // Zero means scalar; positive means time_series.
	NoCache            bool
}

func (r AggregateRequest) Validate() error {
	if err := r.Window.Validate(); err != nil {
		return err
	}
	if r.Signal != Logs && r.Signal != Traces {
		return usage("aggregation signal must be logs or traces")
	}
	if len(r.Where)+len(r.Aggregation) > MaxAggregateTextBytes {
		return usage("combined aggregation and filter input text exceeds 1 MiB")
	}
	match := aggregationPattern.FindStringSubmatch(r.Aggregation)
	if match == nil {
		return usage("aggregation must be count(), rate(), or count_distinct/avg/sum/min/max/p50/p75/p90/p95/p99(FIELD); use query run for complex expressions")
	}
	withoutField := match[1] == "count" || match[1] == "rate"
	if withoutField != (match[2] == "") || (match[2] != "" && !aggregateFieldPattern.MatchString(match[2])) {
		return usage("count and rate take no field; other aggregations require one field name")
	}
	if r.Limit < 1 || r.Limit > MaxSearchRows {
		return usage("aggregation limit must be 1-10000 groups")
	}
	if r.Order != "asc" && r.Order != "desc" {
		return usage("aggregation order must be asc or desc")
	}
	if len(r.GroupBy) > 16 {
		return usage("aggregation supports at most 16 group-by fields")
	}
	seen := make(map[string]bool, len(r.GroupBy))
	for _, field := range r.GroupBy {
		if len(field) > 1024 || !aggregateFieldPattern.MatchString(field) || seen[field] {
			return usage("group-by fields must be distinct dotted field names; use query run for other field syntax")
		}
		seen[field] = true
	}
	if r.Step < 0 || r.Step%time.Second != 0 {
		return usage("aggregation step must be positive whole seconds when supplied")
	}
	if r.Step > 0 && (r.End.UnixMilli()-r.Start.UnixMilli())/r.Step.Milliseconds()+1 > 11000 {
		return usage("aggregation exceeds 11000 points per series; increase step or narrow the time window")
	}
	return nil
}

type aggregateField struct {
	Name         string `json:"name"`
	Signal       Signal `json:"signal"`
	FieldContext string `json:"fieldContext,omitzero"`
}

type aggregateSpec struct {
	Name         string           `json:"name"`
	Signal       Signal           `json:"signal"`
	Aggregations [1]searchFilter  `json:"aggregations"`
	GroupBy      []aggregateField `json:"groupBy,omitempty"`
	Order        [1]searchOrder   `json:"order"`
	Limit        int              `json:"limit"`
	StepInterval int64            `json:"stepInterval,omitzero"`
	Filter       *searchFilter    `json:"filter,omitempty"`
}

func (c *Client) Aggregate(ctx context.Context, r AggregateRequest) (QueryResult, error) {
	if err := r.Validate(); err != nil {
		return QueryResult{}, err
	}
	spec := aggregateSpec{Name: "A", Signal: r.Signal, Aggregations: [1]searchFilter{{Expression: r.Aggregation}},
		Order: [1]searchOrder{{Key: searchKey{Name: r.Aggregation}, Direction: r.Order}}, Limit: r.Limit, StepInterval: int64(r.Step / time.Second)}
	for _, name := range r.GroupBy {
		field := aggregateField{Name: name, Signal: r.Signal}
		// Only explicit context prefixes are interpreted. Bare fields are resolved
		// by SigNoz metadata, never guessed from service/k8s/http naming conventions.
		prefix, rest, found := strings.Cut(name, ".")
		if found {
			switch prefix {
			case "resource", "attribute", "scope", "log", "span", "body":
				field.Name, field.FieldContext = rest, prefix
			}
		}
		spec.GroupBy = append(spec.GroupBy, field)
	}
	if r.Where != "" {
		spec.Filter = &searchFilter{Expression: r.Where}
	}
	kind := "scalar"
	if r.Step > 0 {
		kind = "time_series"
	}
	body := queryRequest[aggregateSpec]{SchemaVersion: "v1", Start: r.Start.UnixMilli(), End: r.End.UnixMilli(), RequestType: kind, NoCache: r.NoCache,
		CompositeQuery: compositeQuery[aggregateSpec]{Queries: [1]queryEnvelope[aggregateSpec]{{Type: "builder_query", Spec: spec}}}}
	data, err := request[jsontext.Value](ctx, c, http.MethodPost, "/api/v5/query_range", body, nil)
	if err != nil {
		return QueryResult{}, err
	}
	return decodeQueryResult(data, kind)
}
