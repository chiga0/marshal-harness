//go:build !darwin && !linux

package runstore

import "errors"

func probeLeaseOwnerProcessAlive(root, runID string) (bool, error) {
	return false, errors.New("lease owner process probe unsupported on this platform")
}
