package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/chiga0/marshal-harness/internal/cli"
	"github.com/chiga0/marshal-harness/internal/stablegotest"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if handled, code := stablegotest.MaybeRun(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}
	os.Exit(cli.RunContext(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
