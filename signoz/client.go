package signoz

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
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
	Status  int    `json:"http_status,omitzero"`
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

// Decode directly from the bounded body. Typed fields and jsontext.Values own
// their storage; nothing returned depends on a decoder's reusable buffer.
func request[T any](ctx context.Context, c *Client, method, path string, body any, params url.Values) (T, error) {
	var zero T
	resp, err := c.response(ctx, method, path, body, params)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	tooLarge := &Error{Code: "response_too_large", Message: "API response exceeds 16 MiB; reduce the query range or limit"}
	if resp.ContentLength > maxResponseBytes {
		return zero, tooLarge
	}
	bodyReader := &responseReader{Reader: resp.Body}
	limited := &io.LimitedReader{R: bodyReader, N: maxResponseBytes + 1}
	var envelope struct {
		Status string `json:"status"`
		Data   *T     `json:"data"`
	}
	decodeErr := json.UnmarshalRead(limited, &envelope)
	// Finish the bounded read even on a syntax error, so byte limits and body
	// transport failures retain precedence over sanitized parser diagnostics.
	if decodeErr != nil {
		_, _ = io.Copy(io.Discard, limited)
	}
	if bodyReader.err != nil {
		return zero, transportError(ctx, bodyReader.err)
	}
	if limited.N == 0 {
		return zero, tooLarge
	}
	if decodeErr != nil || envelope.Status != "success" || envelope.Data == nil {
		return zero, invalidResponse("API returned an unsuccessful or unexpected JSON response")
	}
	return *envelope.Data, nil
}

type responseReader struct {
	io.Reader
	err error
}

func (r *responseReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err != nil && err != io.EOF {
		r.err = err
	}
	return n, err
}

func (c *Client) response(ctx context.Context, method, path string, body any, params url.Values) (*http.Response, error) {
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
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
		resp.Body.Close()
		return nil, &Error{Code: "invalid_response", Message: "expected JSON from the API; the endpoint may be serving a proxy login page"}
	}
	return resp, nil
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

func (c *Client) Me(ctx context.Context) (Identity, error) {
	data, err := request[jsontext.Value](ctx, c, http.MethodGet, "/api/v1/service_accounts/me", nil, nil)
	if err != nil {
		return Identity{}, err
	}
	var identity struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(data, &identity) != nil || identity.ID == "" {
		return Identity{}, &Error{Code: "invalid_response", Message: "API did not return a service-account identity"}
	}
	return Identity{ID: identity.ID, Raw: data}, nil
}
