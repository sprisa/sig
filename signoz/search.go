package signoz

import (
	"context"
	"encoding/json"
	"net/http"
)

type searchSpec struct {
	Name   string        `json:"name"`
	Signal Signal        `json:"signal"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
	Order  []searchOrder `json:"order"`
	Filter *searchFilter `json:"filter,omitempty"`
}
type searchFilter struct {
	Expression string `json:"expression"`
}
type searchOrder struct {
	Key       searchKey `json:"key"`
	Direction string    `json:"direction"`
}
type searchKey struct {
	Name string `json:"name"`
}

// SearchPage contains API facts only. The collector owns continuation and
// completeness, including the upstream cursor's loss of timestamp ties.
type SearchPage struct {
	Rows       []json.RawMessage
	NextCursor string
	Warning    json.RawMessage
	Truncated  bool
}

func (c *Client) Search(ctx context.Context, r SearchRequest) (SearchPage, error) {
	if err := r.Validate(); err != nil {
		return SearchPage{}, err
	}
	keys := []string{"timestamp", "id"}
	if r.Signal == Traces {
		keys = []string{"timestamp", "trace_id", "span_id"}
	}
	spec := searchSpec{Name: "A", Signal: r.Signal, Limit: r.Limit, Offset: r.Offset}
	for _, key := range keys {
		spec.Order = append(spec.Order, searchOrder{Key: searchKey{Name: key}, Direction: "desc"})
	}
	if r.Where != "" {
		spec.Filter = &searchFilter{Expression: r.Where}
	}
	data, err := c.request(ctx, http.MethodPost, "/api/v5/query_range", queryRequest{
		SchemaVersion: "v1", Start: r.Start.UnixMilli(), End: r.End.UnixMilli(), RequestType: "raw",
		CompositeQuery: compositeQuery{Queries: []queryEnvelope{{Type: "builder_query", Spec: spec}}},
	}, nil)
	if err != nil {
		return SearchPage{}, err
	}
	result, err := decodeQueryResult(data, "raw")
	if err != nil {
		return SearchPage{}, err
	}
	if len(result.Results) != 1 {
		return SearchPage{}, invalidResponse("search must return exactly one query result")
	}
	var wire struct {
		QueryName  string
		Rows       json.RawMessage
		NextCursor string
	}
	var rows []json.RawMessage
	if json.Unmarshal(result.Results[0], &wire) != nil || wire.QueryName != "A" || json.Unmarshal(wire.Rows, &rows) != nil {
		return SearchPage{}, invalidResponse("unexpected search row array or query name")
	}
	if rows == nil {
		rows = []json.RawMessage{}
	}
	page := SearchPage{Rows: rows, NextCursor: wire.NextCursor, Warning: result.Warning}
	if string(page.Warning) == "null" {
		page.Warning = nil
	}
	if len(rows) > r.Limit {
		page.Rows = rows[:r.Limit]
		page.NextCursor = ""
		page.Truncated = true
	}
	return page, nil
}

type PageBudget struct{ Pages int }

func (b PageBudget) Validate(limit int) error {
	if limit < 1 || limit > MaxSearchRows || b.Pages < 1 || b.Pages > 100 || b.Pages > MaxSearchRows/limit {
		return usage("limit must be 1-10000, pages 1-100, and total requested rows at most 10000")
	}
	return nil
}

type SearchResult struct {
	Rows         []json.RawMessage
	Pages        int
	NextOffset   *int
	NextCursor   string
	Warnings     []json.RawMessage
	Completeness string
}

// CollectPages keeps the resolved window immutable and returns no partial
// success on error. Callers supply a context deadline for the whole collection.
func (c *Client) CollectPages(ctx context.Context, r SearchRequest, b PageBudget) (SearchResult, error) {
	return collectPages(ctx, r, b, c.Search)
}

func collectPages(ctx context.Context, r SearchRequest, b PageBudget, fetch func(context.Context, SearchRequest) (SearchPage, error)) (SearchResult, error) {
	if err := r.Validate(); err != nil {
		return SearchResult{}, err
	}
	if err := b.Validate(r.Limit); err != nil {
		return SearchResult{}, err
	}
	result := SearchResult{Rows: []json.RawMessage{}, Warnings: []json.RawMessage{}, Completeness: "unknown"}
	totalBytes := 0
	for result.Pages < b.Pages {
		page, err := fetch(ctx, r)
		if err != nil {
			return SearchResult{}, err
		}
		for _, row := range page.Rows {
			totalBytes += len(row)
		}
		totalBytes += 2 * len(page.Warning)
		if totalBytes > maxResponseBytes {
			return SearchResult{}, &Error{Code: "response_too_large", Message: "combined search results exceed 16 MiB; reduce limit or pages"}
		}
		result.Rows = append(result.Rows, page.Rows...)
		result.Pages++
		result.NextCursor = page.NextCursor
		if len(page.Warning) > 0 {
			result.Warnings = append(result.Warnings, page.Warning)
		}
		filled := len(page.Rows) == r.Limit
		r.Offset += len(page.Rows)
		result.NextOffset = nil
		if filled && r.Offset <= MaxSearchOffset {
			next := r.Offset
			result.NextOffset = &next
		}
		// Warnings take precedence over continuation hints. A cursor without a
		// full page is not proof of exhaustion and cannot be followed safely.
		if len(result.Warnings) > 0 {
			result.Completeness = "unknown"
			break
		}
		if !filled {
			if page.NextCursor == "" && !page.Truncated {
				result.Completeness = "complete"
			}
			break
		}
		if page.NextCursor != "" || page.Truncated {
			result.Completeness = "more_available"
		} else {
			result.Completeness = "unknown"
		}
		if result.NextOffset == nil {
			break
		}
	}
	return result, nil
}
