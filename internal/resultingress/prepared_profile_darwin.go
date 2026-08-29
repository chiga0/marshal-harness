//go:build darwin && arm64

package resultingress

import (
	"os"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

// preparedDarwinExecutionProfile only exists in the platform graph that can
// construct and consume it. The common DurableStore owns it through the
// close-only interface so non-Darwin builds do not carry dormant authority
// fields or pretend the profile is constructible.
type preparedDarwinExecutionProfile struct {
	fixedMarshalPath string
	core             processsupervisor.CoreIdentity
	controlRoot      *os.File
	controlIdentity  processsupervisor.ControlDirectoryIdentity
}

func (profile *preparedDarwinExecutionProfile) close() error {
	if profile == nil || profile.controlRoot == nil {
		return nil
	}
	err := profile.controlRoot.Close()
	profile.controlRoot = nil
	return err
}
