package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/sprisa/sig/cmd"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cmd.Run(ctx, os.Args, cmd.Options{})
	stop()
	os.Exit(code)
}
