//go:build !darwin || !arm64

package resultingress

import (
	"context"
	"os"
)

// OpenDarwinResultIngressStore never silently degrades outside the accepted
// Darwin arm64 ordinary-user profile.
func OpenDarwinResultIngressStore(_ *os.File) (*DurableStore, error) {
	return nil, ErrPreparedExecutionUnavailable
}

// SealPi0844DarwinPreparedExecutionStore never silently degrades outside the
// accepted Darwin arm64 ordinary-user profile.
func SealPi0844DarwinPreparedExecutionStore(_ context.Context, _ *DurableStore, _ CurrentOwnerLockVerifier, _ CurrentOwnerBinding, _ string, _ *os.File) (*DurableStore, error) {
	return nil, ErrPreparedExecutionUnavailable
}
