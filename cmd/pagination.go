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

type pageToken struct {
	Version  int    `json:"v"`
	Endpoint string `json:"endpoint"`
	Signal   string `json:"signal"`
	Start    int64  `json:"start"`
	End      int64  `json:"end"`
	Where    string `json:"where"`
	Limit    int    `json:"limit"`
	Offset   int    `json:"offset"`
}

func endpointHash(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:])
}

func (a *app) search(ctx context.Context, cmd *cli.Command, signal string) error {
	if err := noArgs(cmd); err != nil {
		return err
	}
	var token pageToken
	if cmd.IsSet("page-token") {
		for _, flag := range []string{"since", "start", "end", "where", "limit", "offset"} {
			if cmd.IsSet(flag) {
				return fail("usage", "--page-token cannot be combined with query flags; only --pages and global flags may change")
			}
		}
		raw := cmd.String("page-token")
		if len(raw) > 65536 {
			return fail("usage", "page token is too large")
		}
		b, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(b, &token) != nil || token.Version != 1 || token.Signal != signal || len(token.Endpoint) != 64 {
			return fail("usage", "invalid page token or wrong signal")
		}
	} else {
		start, end, err := a.timeWindow(cmd)
		if err != nil {
			return err
		}
		if cmd.Int("offset") != 0 && (!cmd.IsSet("start") || !cmd.IsSet("end")) {
			return fail("usage", "nonzero --offset requires explicit --start and --end; prefer --page-token")
		}
		token = pageToken{Version: 1, Signal: signal, Start: start.UnixMilli(), End: end.UnixMilli(), Where: cmd.String("where"), Limit: cmd.Int("limit"), Offset: cmd.Int("offset")}
	}
	start, end := time.UnixMilli(token.Start).UTC(), time.UnixMilli(token.End).UTC()
	if err := signoz.ValidateWindow(start, end); err != nil {
		return err
	}
	pages := cmd.Int("pages")
	if token.Limit < 1 || token.Limit > 10000 || pages < 1 || pages > 100 || pages > 10000/token.Limit || token.Offset < 0 || token.Offset > 1000000 {
		return fail("usage", "limit must be 1-10000, pages 1-100, total requested rows at most 10000, and offset 0-1000000")
	}
	client, name, endpoint, _, err := a.connect(cmd)
	if err != nil {
		return err
	}
	fingerprint := endpointHash(endpoint)
	if token.Endpoint != "" && token.Endpoint != fingerprint {
		return fail("usage", "page token belongs to another endpoint")
	}
	token.Endpoint = fingerprint
	ctx, cancel := context.WithTimeout(ctx, cmd.Duration("timeout"))
	defer cancel()
	rows := []json.RawMessage{}
	warnings := []json.RawMessage{}
	var last signoz.SearchResult
	initialOffset, fetched, bytes := token.Offset, 0, 0
	more := false
	for fetched < pages {
		opts := signoz.SearchOptions{Start: start, End: end, Limit: token.Limit, Offset: token.Offset, Where: token.Where}
		if signal == "logs" {
			last, err = client.SearchLogs(ctx, opts)
		} else {
			last, err = client.SearchTraces(ctx, opts)
		}
		if err != nil {
			return err
		}
		for _, row := range last.Rows {
			bytes += len(row)
		}
		bytes += 2 * len(last.Warning)
		if bytes > 16<<20 {
			return fail("response_too_large", "combined search results exceed 16 MiB; reduce --limit or --pages")
		}
		rows = append(rows, last.Rows...)
		fetched++
		if len(last.Warning) > 0 && string(last.Warning) != "null" {
			warnings = append(warnings, last.Warning)
		}
		token.Offset += len(last.Rows)
		more = len(last.Rows) == token.Limit
		if !more || len(warnings) > 0 || token.Offset > 1000000 {
			break
		}
	}
	next := ""
	if more && token.Offset <= 1000000 {
		b, err := json.Marshal(token)
		if err != nil {
			return fail("internal", "could not encode pagination state")
		}
		next = base64.RawURLEncoding.EncodeToString(b)
		if len(next) > 65536 {
			return fail("usage", "query is too large to encode a continuation token")
		}
	}
	complete := "complete"
	if more || len(warnings) > 0 {
		complete = "unknown"
	}
	if last.NextCursor != "" && more {
		complete = "more_available"
	}
	var warning json.RawMessage
	if len(warnings) > 0 {
		warning = warnings[len(warnings)-1]
	}
	return a.emit(rows, map[string]any{
		"schema_version": "1", "context": name, "signal": signal, "start": start, "end": end,
		"returned": len(rows), "limit": token.Limit, "offset": initialOffset, "pages": fetched,
		"next_page_token": next, "next_cursor": last.NextCursor, "pagination": "offset",
		"completeness": complete, "warning": warning, "warnings": warnings,
	})
}
