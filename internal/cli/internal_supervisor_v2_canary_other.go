//go:build !darwin

package cli

import (
	"context"
	"fmt"
	"io"
)

func runInternalProcessSupervisorV2Canary(_ context.Context, args []string, _, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "--attestation-ready" {
		fmt.Fprintln(stderr, "supervisor-v2-canary-handshake-invalid")
		return ExitUsage
	}
	fmt.Fprintln(stderr, "supervisor-v2-canary-unavailable")
	return ExitUnavailable
}
