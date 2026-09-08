package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/sprisa/sig/signoz"
	"github.com/urfave/cli/v3"
)

// This DTO is the persisted v1 token format, not mutable pagination state.
type pageToken struct {
	Version  int           `json:"v"`
	Endpoint string        `json:"endpoint"`
	Signal   signoz.Signal `json:"signal"`
	Start    int64         `json:"start"`
	End      int64         `json:"end"`
	Where    string        `json:"where"`
	Limit    int           `json:"limit"`
	Offset   int           `json:"offset"`
}

func endpointHash(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:])
}

func (a *app) searchRequest(c *cli.Command, signal signoz.Signal) (signoz.SearchRequest, string, error) {
	if c.IsSet("page-token") {
		for _, flag := range []string{"since", "start", "end", "where", "limit", "offset"} {
			if c.IsSet(flag) {
				return signoz.SearchRequest{}, "", fail("usage", "--page-token cannot be combined with query flags; only --pages and global flags may change")
			}
		}
		raw := c.String("page-token")
		if len(raw) > 65536 {
			return signoz.SearchRequest{}, "", fail("usage", "page token is too large")
		}
		b, err := base64.RawURLEncoding.DecodeString(raw)
		var token pageToken
		if err != nil || json.Unmarshal(b, &token) != nil || token.Version != 1 || token.Signal != signal || len(token.Endpoint) != 64 {
			return signoz.SearchRequest{}, "", fail("usage", "invalid page token or wrong signal")
		}
		r := signoz.SearchRequest{Window: signoz.Window{Start: time.UnixMilli(token.Start).UTC(), End: time.UnixMilli(token.End).UTC()}, Signal: signal, Where: token.Where, Limit: token.Limit, Offset: token.Offset}
		return r, token.Endpoint, r.Validate()
	}
	w, err := a.timeWindow(c)
	if err != nil {
		return signoz.SearchRequest{}, "", err
	}
	if c.Int("offset") != 0 && (!c.IsSet("start") || !c.IsSet("end")) {
		return signoz.SearchRequest{}, "", fail("usage", "nonzero --offset requires explicit --start and --end; prefer --page-token")
	}
	r := signoz.SearchRequest{Window: w, Signal: signal, Where: c.String("where"), Limit: c.Int("limit"), Offset: c.Int("offset")}
	return r, "", r.Validate()
}

func encodePageToken(r signoz.SearchRequest, endpoint string, offset *int) (string, error) {
	if offset == nil {
		return "", nil
	}
	token := pageToken{Version: 1, Endpoint: endpoint, Signal: r.Signal, Start: r.Start.UnixMilli(), End: r.End.UnixMilli(), Where: r.Where, Limit: r.Limit, Offset: *offset}
	b, err := json.Marshal(token)
	if err != nil {
		return "", fail("internal", "cannot encode pagination state")
	}
	encoded := base64.RawURLEncoding.EncodeToString(b)
	if len(encoded) > 65536 {
		return "", fail("usage", "query is too large to encode a continuation token")
	}
	return encoded, nil
}

func (a *app) search(ctx context.Context, c *cli.Command, signal signoz.Signal) error {
	if err := noArgs(c); err != nil {
		return err
	}
	r, binding, err := a.searchRequest(c, signal)
	if err != nil {
		return err
	}
	budget := signoz.PageBudget{Pages: c.Int("pages")}
	if err := budget.Validate(r.Limit); err != nil {
		return err
	}
	conn, err := a.connect(c)
	if err != nil {
		return err
	}
	fingerprint := endpointHash(conn.Endpoint)
	if binding != "" && binding != fingerprint {
		return fail("usage", "page token belongs to another endpoint")
	}
	ctx, cancel := context.WithTimeout(ctx, c.Duration("timeout"))
	defer cancel()
	result, err := conn.Client.CollectPages(ctx, r, budget)
	if err != nil {
		return err
	}
	next, err := encodePageToken(r, fingerprint, result.NextOffset)
	if err != nil {
		return err
	}
	var warning json.RawMessage
	if len(result.Warnings) > 0 {
		warning = result.Warnings[len(result.Warnings)-1]
	}
	return a.emit(result.Rows, struct {
		SchemaVersion string            `json:"schema_version"`
		Context       string            `json:"context"`
		Signal        signoz.Signal     `json:"signal"`
		Start         time.Time         `json:"start"`
		End           time.Time         `json:"end"`
		Returned      int               `json:"returned"`
		Limit         int               `json:"limit"`
		Offset        int               `json:"offset"`
		Pages         int               `json:"pages"`
		NextPageToken string            `json:"next_page_token"`
		NextCursor    string            `json:"next_cursor"`
		Pagination    string            `json:"pagination"`
		Completeness  string            `json:"completeness"`
		Warning       json.RawMessage   `json:"warning"`
		Warnings      []json.RawMessage `json:"warnings"`
	}{
		"1", conn.Context, signal, r.Start, r.End,
		len(result.Rows), r.Limit, r.Offset, result.Pages,
		next, result.NextCursor, "offset", result.Completeness, warning, result.Warnings,
	})
}
