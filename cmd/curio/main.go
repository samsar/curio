package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/samsar/curio/internal/cli"
)

func main() {
	// ctrl-c or SIGTERM cancels the command's context, so a command that
	// waits (import --follow, add --wait) stops cleanly. After the first
	// signal the default handling is back, and a second one kills.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	context.AfterFunc(ctx, stop)
	code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop() // os.Exit skips deferred calls
	os.Exit(code)
}
