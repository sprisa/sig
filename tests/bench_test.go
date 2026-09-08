package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/sprisa/sig/cmd"
	"github.com/sprisa/sig/signoz"
)

// These two implementations are experiments, not substitutes for the production
// benchmark below. Keep the nested-raw baseline fixed when optimizing the client.
func decodeNestedRaw(b []byte) ([]json.RawMessage, error) {
	var envelope struct {
		Status string
		Data   json.RawMessage
	}
	if err := json.Unmarshal(b, &envelope); err != nil {
		return nil, err
	}
	var result struct {
		Type string
		Data struct{ Results json.RawMessage }
	}
	if err := json.Unmarshal(envelope.Data, &result); err != nil {
		return nil, err
	}
	var results []json.RawMessage
	if err := json.Unmarshal(result.Data.Results, &results); err != nil {
		return nil, err
	}
	if envelope.Status != "success" || result.Type != "raw" || len(results) != 1 {
		return nil, fmt.Errorf("unexpected response")
	}
	var raw struct {
		QueryName string
		Rows      json.RawMessage
	}
	if err := json.Unmarshal(results[0], &raw); err != nil {
		return nil, err
	}
	if raw.QueryName != "A" {
		return nil, fmt.Errorf("unexpected query name")
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(raw.Rows, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func decodeDirectTyped(b []byte) ([]json.RawMessage, error) {
	var response struct {
		Status string
		Data   struct {
			Type string
			Data struct {
				Results []struct {
					QueryName  string
					Rows       []json.RawMessage
					NextCursor string
				}
			}
			Warning json.RawMessage
		}
	}
	if err := json.Unmarshal(b, &response); err != nil {
		return nil, err
	}
	if response.Status != "success" || response.Data.Type != "raw" || len(response.Data.Data.Results) != 1 || response.Data.Data.Results[0].QueryName != "A" {
		return nil, fmt.Errorf("unexpected response")
	}
	return response.Data.Data.Results[0].Rows, nil
}

var decodeSink []json.RawMessage

func BenchmarkDecodeStrategies(b *testing.B) {
	for _, s := range scenarios {
		if s.pages != 1 {
			continue
		}
		data := logResponse(s.rows, s.bodyBytes, 0)
		for _, method := range []struct {
			name   string
			decode func([]byte) ([]json.RawMessage, error)
		}{
			{"nested_raw", decodeNestedRaw}, {"direct_typed", decodeDirectTyped},
		} {
			b.Run(s.name+"/"+method.name, func(b *testing.B) {
				b.Cleanup(func() { decodeSink = nil })
				b.ReportAllocs()
				b.SetBytes(int64(len(data)))
				for b.Loop() {
					rows, err := method.decode(data)
					if err != nil || len(rows) != s.rows {
						b.Fatal("decode failed")
					}
					decodeSink = rows
				}
			})
		}
	}
}

func BenchmarkSearchAndOutput(b *testing.B) {
	for _, s := range scenarios {
		b.Run(s.name, func(b *testing.B) {
			server, total := fixtureServer(b, s)
			client, err := signoz.New(server.URL, "synthetic-key", 30*time.Second)
			if err != nil {
				b.Fatal(err)
			}
			request := searchRequest(s.rows)
			b.ReportAllocs()
			b.SetBytes(int64(total))
			for b.Loop() {
				result, err := client.CollectPages(context.Background(), request, signoz.PageBudget{Pages: s.pages})
				if err != nil || len(result.Rows) != s.rows*s.pages {
					b.Fatalf("search failed: %v", err)
				}
				// Match the output envelope and encoder used by the CLI, without
				// terminal, process startup, credential lookup, or disk costs.
				output := struct {
					Data any `json:"data"`
					Meta any `json:"meta"`
				}{
					result.Rows, map[string]any{"returned": len(result.Rows), "pages": result.Pages, "completeness": result.Completeness},
				}
				if err := jsonv2.MarshalEncode(jsontext.NewEncoder(io.Discard), output, jsonv2.FormatNilSliceAsNull(true), jsonv2.FormatNilMapAsNull(true)); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "pipelines/s")
			b.ReportMetric(float64(b.N*s.pages)/b.Elapsed().Seconds(), "requests/s")
		})
	}
}

func BenchmarkEncodeRawRows(b *testing.B) {
	for _, s := range scenarios {
		if s.pages != 1 {
			continue
		}
		data := logResponse(s.rows, s.bodyBytes, 0)
		rows, err := decodeDirectTyped(data)
		if err != nil {
			b.Fatal(err)
		}
		output := struct {
			Data any `json:"data"`
		}{rows}
		b.Run(s.name+"/v1", func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if err := json.NewEncoder(io.Discard).Encode(output); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(s.name+"/v2_stream", func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if err := jsonv2.MarshalEncode(jsontext.NewEncoder(io.Discard), output); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Exercise the actual command output path as well as the isolated pipeline above.
func BenchmarkCLIStreaming(b *testing.B) {
	for _, s := range scenarios {
		b.Run(s.name, func(b *testing.B) {
			server, total := fixtureServer(b, s)
			opts := cmd.Options{Out: io.Discard, ErrOut: io.Discard, ConfigDir: b.TempDir(), Getenv: func(name string) string {
				switch name {
				case "SIGNOZ_URL":
					return server.URL
				case "SIGNOZ_API_KEY":
					return "synthetic-key"
				}
				return ""
			}}
			args := []string{"sig", "logs", "search", "--limit", strconv.Itoa(s.rows), "--pages", strconv.Itoa(s.pages), "--start", "2026-01-01T00:00:00Z", "--end", "2026-01-01T00:01:00Z"}
			b.ReportAllocs()
			b.SetBytes(int64(total))
			for b.Loop() {
				if code := cmd.Run(context.Background(), args, opts); code != 0 {
					b.Fatalf("CLI exited %d", code)
				}
			}
		})
	}
}

func searchRequest(limit int) signoz.SearchRequest {
	return signoz.SearchRequest{Window: signoz.Window{Start: time.Unix(1767225600, 0), End: time.Unix(1767225660, 0)}, Signal: signoz.Logs, Limit: limit}
}

func TestDecodeStrategiesPreserveRows(t *testing.T) {
	data := logResponse(100, 512, 0)
	a, err := decodeNestedRaw(data)
	if err != nil {
		t.Fatal(err)
	}
	b, err := decodeDirectTyped(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 100 || len(a) != len(b) {
		t.Fatal("different row counts")
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			t.Fatal("row content or numeric precision changed")
		}
	}
}
