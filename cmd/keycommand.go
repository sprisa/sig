package cmd

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// maxCommandKeyBytes bounds a key command's output like --key-stdin bounds piped keys.
const maxCommandKeyBytes = 65536

type limitedBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxCommandKeyBytes {
		b.overflow = true
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

// commandKey runs SIGNOZ_API_KEY_COMMAND through the platform shell and returns its trimmed
// standard output as the API key. The helper's stderr is discarded so sig's own stderr stays
// one JSON error; errors name only the exit status, never the helper's output.
func commandKey(command string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", command)
	}
	var out limitedBuffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		switch {
		case ctx.Err() != nil:
			return "", fail("timeout", "SIGNOZ_API_KEY_COMMAND timed out; run it directly to see why, or increase --timeout")
		case errors.As(err, &exitErr):
			return "", fail("authentication", "SIGNOZ_API_KEY_COMMAND failed; run it directly to see its error")
		default:
			return "", fail("authentication", "SIGNOZ_API_KEY_COMMAND could not be started")
		}
	}
	if out.overflow {
		return "", fail("authentication", "SIGNOZ_API_KEY_COMMAND printed more than 64 KiB")
	}
	key := strings.TrimSpace(out.String())
	if key == "" {
		return "", fail("authentication", "SIGNOZ_API_KEY_COMMAND printed no key")
	}
	return key, nil
}
