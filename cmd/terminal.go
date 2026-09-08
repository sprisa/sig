package cmd

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/muesli/cancelreader"
	"golang.org/x/term"
)

func readTerminalKey(ctx context.Context, input *os.File, output io.Writer) (key string, err error) {
	if ctx.Err() != nil {
		return "", fail("cancelled", "login cancelled")
	}
	reader, err := cancelreader.NewReader(input)
	if err != nil {
		return "", fail("credentials", "could not initialize terminal input")
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			key, err = "", fail("credentials", "could not close terminal input")
		}
	}()
	state, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		return "", fail("credentials", "could not configure terminal input")
	}
	defer func() {
		if restoreErr := term.Restore(int(input.Fd()), state); restoreErr != nil {
			key, err = "", fail("credentials", "could not restore terminal settings")
		}
	}()

	// Cancel reads on external signals as well as handling Ctrl-C typed in raw mode.
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		reader.Cancel()
		close(done)
	})
	defer func() {
		if !stop() {
			<-done
		}
	}()
	terminal := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{reader, output}, "")
	key, err = terminal.ReadPassword("SigNoz service-account key: ")
	if ctx.Err() != nil || errors.Is(err, io.EOF) || errors.Is(err, cancelreader.ErrCanceled) {
		return "", fail("cancelled", "login cancelled")
	}
	if err != nil && !errors.Is(err, term.ErrPasteIndicator) {
		return "", fail("credentials", "could not read API key from terminal")
	}
	return key, nil
}
