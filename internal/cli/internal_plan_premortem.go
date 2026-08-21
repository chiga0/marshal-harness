package cli

import (
	"io"
	"os"

	"github.com/chiga0/marshal-harness/internal/planpremortem"
)

// runInternalPlanPremortemCheck keeps the plan checker inside the stable
// Marshal executable. The operator validator binds this executable's bytes,
// inode and running process identity before sending any input.

func runInternalPlanPremortemCheck(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var err error
	args, err = consumeStableAttestation(args, stdin)
	if err != nil {
		return ExitFailure
	}
	return planpremortem.Run(args, os.Getenv, stdout, stderr)
}
