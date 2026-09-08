package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
)

type inputSource struct {
	reader    io.Reader
	interrupt func()
	release   func()
}

// File and memory inputs are finite reads. Streams need a known interrupt
// mechanism: arbitrary io.Reader implementations are rejected, not disguised as
// cancellable readers or read by goroutines that can be abandoned.
func openInput(input io.Reader) (inputSource, error) {
	switch r := input.(type) {
	case *strings.Reader, *bytes.Reader, *bytes.Buffer:
		return inputSource{reader: r}, nil
	case *io.PipeReader:
		return inputSource{reader: r, interrupt: func() { _ = r.CloseWithError(context.Canceled) }}, nil
	case *os.File:
		info, err := r.Stat()
		if err != nil {
			return inputSource{}, fail("usage", "cannot inspect input")
		}
		if info.Mode().IsRegular() {
			return inputSource{reader: r}, nil
		}
		return openFileStream(r)
	default:
		return inputSource{}, fail("usage", "unsupported input reader; use a regular file, an in-memory reader, or a cancellable stream")
	}
}

// stop waits for an in-flight callback before the caller releases its resource.
func interruptOnCancel(ctx context.Context, interrupt func()) func() {
	if interrupt == nil {
		return func() {}
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { interrupt(); close(done) })
	return func() {
		if !stop() {
			<-done
		}
	}
}

func readBoundedInput(ctx context.Context, input io.Reader, limit int64) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, fail("cancelled", "input cancelled")
	}
	source, err := openInput(input)
	if err != nil {
		return nil, err
	}
	if source.release != nil {
		defer source.release()
	}
	defer interruptOnCancel(ctx, source.interrupt)()
	data, err := io.ReadAll(io.LimitReader(source.reader, limit+1))
	if ctx.Err() != nil {
		return nil, fail("cancelled", "input cancelled")
	}
	if err != nil {
		return nil, fail("usage", "could not read input")
	}
	if int64(len(data)) > limit {
		return nil, fail("usage", "input exceeds the allowed size")
	}
	return data, nil
}
