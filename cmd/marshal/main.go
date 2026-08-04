package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/chiga0/marshal-harness/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(cli.RunContext(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
