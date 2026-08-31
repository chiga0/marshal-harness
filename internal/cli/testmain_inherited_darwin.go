//go:build darwin && arm64

package cli

import (
	"context"
	"os"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

// inheritedTestEntry runs the inherited supervisor/launch-child loop when the
// test binary was re-executed by the sealed fresh-start mechanics. It returns
// false for normal test invocations.
func inheritedTestEntry() bool {
	if _, err := processsupervisor.InheritedInvocationKind(); err != nil {
		return false
	}
	if err := processsupervisor.RunInheritedMain(context.Background()); err != nil {
		os.Exit(1)
	}
	return true
}
