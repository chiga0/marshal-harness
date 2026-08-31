//go:build !darwin

package processsupervisor

// ExpectedProcessTerminal is unavailable outside the Darwin preview profile.
func ExpectedProcessTerminal(ProcessIdentity) (bool, error) { return false, ErrUnavailable }
