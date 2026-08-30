package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/chiga0/marshal-harness/internal/cli"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Inherited fixed-image invocations (supervisor / launch child) are
	// dispatched before any CLI work: the role is proven by the inherited
	// bootstrap descriptor type, never by argv or environment.
	if _, kindErr := processsupervisor.InheritedInvocationKind(); kindErr == nil {
		if runErr := processsupervisor.RunInheritedMain(ctx); runErr != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(cli.RunContext(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
