package signoz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 16 << 20

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"http_status,omitempty"`
}

func (e *Error) Error() string { return e.Message }

type Client struct {
	base *url.URL
	key  string
	http *http.Client
}

func NormalizeURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", &Error{Code: "usage", Message: "URL must be absolute, without credentials, query parameters, or a fragment"}
	}
	ip := net.ParseIP(u.Hostname())
	loopback := strings.EqualFold(u.Hostname(), "localhost") || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return "", &Error{Code: "usage", Message: "URL must use HTTPS; HTTP is allowed only for loopback endpoints"}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	return u.String(), nil
}

func New(rawURL, key string, timeout time.Duration) (*Client, error) {
	normalized, err := NormalizeURL(rawURL)
	if err != nil {
		return nil, err
	}
	if key == "" || strings.ContainsAny(key, "\r\n") {
		return nil, &Error{Code: "authentication", Message: "a valid service-account key is required; run sig auth login or set SIGNOZ_API_KEY"}
	}
	if timeout <= 0 {
		return nil, &Error{Code: "usage", Message: "timeout must be positive"}
	}
	u, _ := url.Parse(normalized)
	return &Client{base: u, key: key, http: &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *Client) request(ctx context.Context, method, path string, body any, params url.Values) (json.RawMessage, error) {
	var input io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, &Error{Code: "usage", Message: "could not encode request"}
		}
		input = bytes.NewReader(b)
	}
	u := *c.base
	u.Path += path
	if u.RawPath != "" {
		u.RawPath += path
	}
	u.RawQuery = params.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), input)
	if err != nil {
		return nil, &Error{Code: "usage", Message: "could not construct request"}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("SIGNOZ-API-KEY", c.key)
	req.Header.Set("User-Agent", "sig")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, transportError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		e := &Error{Code: "api", Message: "API request failed", Status: resp.StatusCode}
		switch {
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			e.Code, e.Message = "authentication", "endpoint redirected the request; configure a directly accessible API URL or arrange proxy access outside sig"
		case resp.StatusCode == 401:
			e.Code, e.Message = "authentication", "authentication rejected by SigNoz or an access proxy; check the key and endpoint access"
		case resp.StatusCode == 403:
			e.Code, e.Message = "permission", "access denied by SigNoz or an access proxy"
		case resp.StatusCode == 400:
			e.Code, e.Message = "invalid_query", "API rejected the request; check filter syntax and API compatibility"
		case resp.StatusCode == 404:
			e.Message = "API resource or endpoint not found; check the identifier, base URL, and supported SigNoz version"
		case resp.StatusCode == 429:
			e.Code, e.Message = "rate_limited", "API rate limit reached; wait before retrying"
		}
		return nil, e
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json")) {
		return nil, &Error{Code: "invalid_response", Message: "expected JSON from the API; the endpoint may be serving a proxy login page"}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, transportError(ctx, err)
	}
	if len(b) > maxResponseBytes {
		return nil, &Error{Code: "response_too_large", Message: "API response exceeds 16 MiB; reduce the query range or limit"}
	}
	var envelope struct {
		Status string          `json:"status"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &envelope); err != nil || envelope.Status != "success" || len(envelope.Data) == 0 || bytes.Equal(envelope.Data, []byte("null")) {
		return nil, &Error{Code: "invalid_response", Message: "API returned an unsuccessful or unexpected JSON response"}
	}
	return envelope.Data, nil
}

func transportError(ctx context.Context, err error) error {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return &Error{Code: "timeout", Message: "API request timed out; narrow the query or increase --timeout"}
	}
	if ctx.Err() != nil {
		return &Error{Code: "cancelled", Message: "API request cancelled"}
	}
	// Transport errors can contain URLs or proxy credentials; do not echo them.
	return &Error{Code: "network", Message: "could not reach the API; check network access, the URL, and TLS trust"}
}

func (c *Client) Me(ctx context.Context) (json.RawMessage, error) {
	data, err := c.request(ctx, http.MethodGet, "/api/v1/service_accounts/me", nil, nil)
	if err != nil {
		return nil, err
	}
	var identity struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &identity); err != nil || identity.ID == "" {
		return nil, &Error{Code: "invalid_response", Message: "API did not return a service-account identity"}
	}
	return data, nil
}

type SearchOptions struct {
	Start, End time.Time
	Limit      int
	Where      string
	Offset     int
}

type SearchResult struct {
	Rows         []json.RawMessage
	NextCursor   string
	Warning      json.RawMessage
	Completeness string
}

func (c *Client) SearchLogs(ctx context.Context, opts SearchOptions) (SearchResult, error) {
	return c.search(ctx, "logs", opts)
}

func (c *Client) SearchTraces(ctx context.Context, opts SearchOptions) (SearchResult, error) {
	return c.search(ctx, "traces", opts)
}

func (c *Client) search(ctx context.Context, signal string, opts SearchOptions) (SearchResult, error) {
	if err := ValidateWindow(opts.Start, opts.End); err != nil {
		return SearchResult{}, err
	}
	if opts.Limit < 1 || opts.Limit > 10000 || opts.Offset < 0 || opts.Offset > 1000000 {
		return SearchResult{}, &Error{Code: "usage", Message: "search requires a limit between 1 and 10000 and an offset between 0 and 1000000"}
	}
	order := []any{map[string]any{"key": map[string]string{"name": "timestamp"}, "direction": "desc"}}
	ties := []string{"id"}
	if signal == "traces" {
		ties = []string{"trace_id", "span_id"}
	}
	for _, name := range ties {
		order = append(order, map[string]any{"key": map[string]string{"name": name}, "direction": "desc"})
	}
	spec := map[string]any{
		"name": "A", "signal": signal, "limit": opts.Limit, "offset": opts.Offset, "order": order,
	}
	if opts.Where != "" {
		spec["filter"] = map[string]string{"expression": opts.Where}
	}
	data, err := c.request(ctx, http.MethodPost, "/api/v5/query_range", map[string]any{
		"schemaVersion": "v1", "start": opts.Start.UnixMilli(), "end": opts.End.UnixMilli(), "requestType": "raw",
		"compositeQuery": map[string]any{"queries": []any{map[string]any{"type": "builder_query", "spec": spec}}},
	}, nil)
	if err != nil {
		return SearchResult{}, err
	}
	var response struct {
		Type string `json:"type"`
		Data struct {
			Results []struct {
				QueryName  string          `json:"queryName"`
				Rows       json.RawMessage `json:"rows"`
				NextCursor string          `json:"nextCursor"`
			} `json:"results"`
		} `json:"data"`
		Warning json.RawMessage `json:"warning"`
	}
	if err := json.Unmarshal(data, &response); err != nil || response.Type != "raw" || len(response.Data.Results) != 1 || response.Data.Results[0].QueryName != "A" {
		return SearchResult{}, &Error{Code: "invalid_response", Message: "unexpected search response; check SigNoz API compatibility"}
	}
	raw := response.Data.Results[0]
	var rows []json.RawMessage
	if err := json.Unmarshal(raw.Rows, &rows); err != nil {
		return SearchResult{}, &Error{Code: "invalid_response", Message: "API did not return a row array"}
	}
	if rows == nil {
		rows = []json.RawMessage{}
	}
	completeness := "complete"
	if len(rows) >= opts.Limit {
		completeness = "unknown"
	}
	if len(response.Warning) > 0 && string(response.Warning) != "null" {
		completeness = "unknown"
	}
	if raw.NextCursor != "" {
		completeness = "more_available"
	}
	if len(rows) > opts.Limit {
		rows = rows[:opts.Limit]
		raw.NextCursor = "" // The server cursor would skip rows discarded locally.
		completeness = "more_available"
	}
	return SearchResult{Rows: rows, NextCursor: raw.NextCursor, Warning: response.Warning, Completeness: completeness}, nil
}
