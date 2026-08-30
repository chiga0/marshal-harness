//go:build !darwin || !arm64

package processsupervisor

import "context"

// InheritedInvocationKind always fails on platforms without the darwin
// fresh-start mechanics: no inherited invocation exists there.
func InheritedInvocationKind() (string, error) {
	return "", ErrInvalid
}

func RunInheritedMain(_ context.Context) error {
	return ErrInvalid
}
