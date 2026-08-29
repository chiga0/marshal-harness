//go:build !darwin || !arm64

package resultingress

import "os"

// OpenPi0843DarwinResultIngressStore never silently degrades outside the
// accepted Darwin arm64 ordinary-user profile.
func OpenPi0843DarwinResultIngressStore(_ string, _ string, _ *os.File) (*DurableStore, error) {
	return nil, ErrPreparedExecutionUnavailable
}
