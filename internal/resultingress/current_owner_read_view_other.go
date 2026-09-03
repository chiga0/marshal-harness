//go:build !darwin

package resultingress

import "os"

func OpenDarwinCurrentOwnerReadView(*os.File) (*CurrentOwnerReadView, error) {
	return nil, ErrPreparedExecutionUnavailable
}
