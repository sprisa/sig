package cmd

import (
	"os"
	"runtime"
	"time"

	"github.com/muesli/cancelreader"
	"golang.org/x/term"
)

// OS-specific capabilities belong here, not in the bounded JSON/key readers.
func openFileStream(file *os.File) (inputSource, error) {
	if file.SetReadDeadline(time.Time{}) == nil {
		return inputSource{reader: file, interrupt: func() { _ = file.SetReadDeadline(time.Now()) }, release: func() { _ = file.SetReadDeadline(time.Time{}) }}, nil
	}
	if runtime.GOOS == "windows" && file.Fd() != os.Stdin.Fd() {
		return inputSource{}, fail("usage", "this Windows stream has no supported interruption mechanism")
	}
	if !term.IsTerminal(int(file.Fd())) {
		info, err := file.Stat()
		if err != nil {
			return inputSource{}, fail("usage", "cannot inspect stream")
		}
		if info.Mode()&os.ModeCharDevice != 0 || runtime.GOOS == "windows" {
			return inputSource{}, fail("usage", "this stream cannot be interrupted; use a regular file or environment credentials")
		}
	}
	reader, err := cancelreader.NewReader(file)
	if err != nil {
		return inputSource{}, fail("usage", "cannot initialize interruptible input")
	}
	return inputSource{reader: reader, interrupt: func() { reader.Cancel() }, release: func() { _ = reader.Close() }}, nil
}
