//go:build darwin

package processsupervisor

import (
	"errors"

	"golang.org/x/sys/unix"
)

// ExpectedProcessTerminal is a read-only exact-birth probe. It reports true
// only when the expected child is absent or its PID has been reused; a live
// exact process returns false and ambiguous observation fails closed.
func ExpectedProcessTerminal(expected ProcessIdentity) (bool, error) {
	if expected.validate() != nil {
		return false, ErrInvalid
	}
	observed, err := observeAnyProcessIdentity(expected.PID)
	if err == nil {
		return observed != expected, nil
	}
	if errors.Is(err, unix.ESRCH) {
		return true, nil
	}
	if killErr := unix.Kill(expected.PID, 0); errors.Is(killErr, unix.ESRCH) {
		return true, nil
	}
	return false, ErrIntervention
}
