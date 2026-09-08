package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sprisa/sig/signoz"
)

func TestBoundedRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(path, []byte(`{"example":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, err := readBoundedInput(context.Background(), file, 100)
	if err != nil || string(data) != `{"example":true}` {
		t.Fatalf("regular file input failed: %v", err)
	}
}

func TestBoundedInputCancellation(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if runtime.GOOS == "windows" && r.SetReadDeadline(time.Time{}) != nil {
		t.Skip("pipe read deadlines are unavailable on this Windows runtime")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := readBoundedInput(ctx, r, 100); done <- err }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		var e *signoz.Error
		if !errors.As(err, &e) || e.Code != "cancelled" {
			t.Fatalf("unexpected cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("input did not unblock after cancellation")
	}
	if _, err := w.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 2)
	if n, err := r.Read(buffer); err != nil || n != 2 || string(buffer) != "ok" {
		t.Fatal("cancellation closed input or left an expired deadline")
	}
}
