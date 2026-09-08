package cmd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCursorWithoutRowsIsNotComplete(t *testing.T) {
	h := newHarness(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":[],"nextCursor":"unusable"}]}}}`)
	}))
	defer s.Close()
	h.env["SIGNOZ_URL"], h.env["SIGNOZ_API_KEY"] = s.URL, "synthetic-api-key"
	got := h.run(t, "", 0, "logs", "search")
	if strings.Contains(got, `"completeness":"complete"`) {
		t.Fatal("unusable continuation was reported as complete")
	}
}

func TestGenericStreamCancellation(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := readBoundedInput(ctx, r, 100); done <- err }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation or explicit unsupported-input error")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("cancellation left a generic stream blocked")
	}
}
