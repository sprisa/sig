package cmd

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/muesli/cancelreader"
	"golang.org/x/term"
)

func readBoundedInput(ctx context.Context, input io.Reader, limit int64) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, fail("cancelled", "input cancelled")
	}
	var cancel, release func()
	if file, ok := input.(*os.File); ok && file.SetReadDeadline(time.Time{}) == nil {
		cancel = func() { _ = file.SetReadDeadline(time.Now()) }
		release = func() { _ = file.SetReadDeadline(time.Time{}) }
	} else {
		if file, ok := input.(*os.File); ok {
			info, err := file.Stat()
			if err != nil {
				return nil, fail("usage", "cannot inspect input")
			}
			// Regular files cannot be registered with epoll. Windows' console
			// cancellation backend also cannot read redirected non-console input.
			if info.Mode().IsRegular() || ((runtime.GOOS == "windows" || info.Mode()&os.ModeCharDevice != 0) && !term.IsTerminal(int(file.Fd()))) {
				input = struct{ io.Reader }{input}
			}
		}
		reader, err := cancelreader.NewReader(input)
		if err != nil {
			return nil, fail("usage", "cannot initialize input reader")
		}
		input = reader
		cancel = func() { reader.Cancel() }
		release = func() { _ = reader.Close() }
	}
	defer release()
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { cancel(); close(done) })
	defer func() {
		if !stop() {
			<-done
		}
	}()
	data, err := io.ReadAll(io.LimitReader(input, limit+1))
	if ctx.Err() != nil || errors.Is(err, cancelreader.ErrCanceled) {
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
