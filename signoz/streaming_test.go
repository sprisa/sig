package signoz

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

type responseTransport func(*http.Request) (*http.Response, error)

func (f responseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error { b.closed = true; return nil }

func TestStreamingResponseBounds(t *testing.T) {
	valid := `{"status":"success","data":{"id":"synthetic"}}`
	exact := valid + strings.Repeat(" ", maxResponseBytes-len(valid))
	for _, tc := range []struct {
		name   string
		reader io.Reader
		length int64
		code   string
	}{
		{"known", strings.NewReader(valid), int64(len(valid)), ""},
		{"fragmented", iotest.OneByteReader(strings.NewReader(valid)), -1, ""},
		{"exact known", strings.NewReader(exact), maxResponseBytes, ""},
		{"exact unknown", strings.NewReader(exact), -1, ""},
		{"oversized advertised", strings.NewReader(valid), maxResponseBytes + 1, "response_too_large"},
		{"oversized unknown", io.MultiReader(strings.NewReader(exact), strings.NewReader(" ")), -1, "response_too_large"},
		{"underreported", io.MultiReader(strings.NewReader(exact), strings.NewReader(" ")), 1, "response_too_large"},
		{"trailing value", strings.NewReader(valid + " {}"), -1, "invalid_response"},
		{"truncated JSON", strings.NewReader(valid[:len(valid)-1]), -1, "invalid_response"},
		{"transport after JSON", io.MultiReader(strings.NewReader(valid), iotest.ErrReader(io.ErrUnexpectedEOF)), -1, "network"},
		{"transport after malformed", io.MultiReader(strings.NewReader("!"), iotest.ErrReader(errors.New("private transport detail"))), -1, "network"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackedBody{Reader: tc.reader}
			c, err := New("https://signoz.example.com", "synthetic-key", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			c.http.Transport = responseTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: body, ContentLength: tc.length}, nil
			})
			result, err := c.Me(context.Background())
			if tc.code != "" {
				assertCode(t, err, tc.code)
			} else if err != nil || result.ID != "synthetic" {
				t.Fatalf("unexpected result: %v", err)
			}
			if !body.closed {
				t.Fatal("response body was not closed")
			}
		})
	}
}

func TestStreamingStrictJSON(t *testing.T) {
	for _, row := range []string{
		`{"private-marker":1,"private-marker":2}`,
		`{"private-marker":01}`, `{"private-marker":NaN}`, `{"private-marker":+1}`,
		`{"private-marker":"\x00"}`, "{\"private-marker\":\"\xff\"}",
		strings.Repeat("[", 10001) + "0" + strings.Repeat("]", 10001),
	} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":[`+row+`]}]}}}`)
		})
		_, err := c.Search(context.Background(), SearchRequest{Window: Window{Start: time.Unix(1, 0), End: time.Unix(2, 0)}, Signal: Logs, Limit: 1})
		assertCode(t, err, "invalid_response")
		if strings.Contains(err.Error(), "private-marker") {
			t.Fatal("parser diagnostics leaked telemetry")
		}
	}
	for _, body := range []string{
		`{"status":"success","status":"success","data":{"id":"synthetic"}}`,
		`{"Status":"success","data":{"id":"synthetic"}}`,
		`{"status":"success","data":{"ID":"synthetic"}}`,
	} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, body)
		})
		_, err := c.Me(context.Background())
		assertCode(t, err, "invalid_response")
	}
}
