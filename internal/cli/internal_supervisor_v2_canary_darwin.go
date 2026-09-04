//go:build darwin

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

const supervisorV2CanaryLimit = 30 * time.Second

// runInternalProcessSupervisorV2Canary is a hidden, bounded verification
// surface on the fixed Marshal image. It exercises only the dormant v2
// mechanics and grants no Core, repository, publication, or producer authority.
func runInternalProcessSupervisorV2Canary(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "--attestation-ready" {
		fmt.Fprintln(stderr, "supervisor-v2-canary-handshake-invalid")
		return ExitUsage
	}
	bounded, cancel := context.WithTimeout(ctx, supervisorV2CanaryLimit)
	defer cancel()
	result, err := processsupervisor.RunDormantV2FixedCanary(bounded)
	if err != nil {
		fmt.Fprintln(stderr, processsupervisor.DormantV2CanaryReason(err))
		return ExitFailure
	}
	raw, err := json.Marshal(result)
	if err != nil {
		fmt.Fprintln(stderr, "supervisor-v2-canary-encode-failed")
		return ExitFailure
	}
	raw, err = canonical.JSON(raw)
	if err != nil {
		fmt.Fprintln(stderr, "supervisor-v2-canary-encode-failed")
		return ExitFailure
	}
	if _, err := stdout.Write(append(raw, '\n')); err != nil {
		fmt.Fprintln(stderr, "supervisor-v2-canary-output-failed")
		return ExitFailure
	}
	return ExitOK
}
