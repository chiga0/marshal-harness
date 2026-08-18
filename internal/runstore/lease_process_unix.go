//go:build darwin || linux

package runstore

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func probeLeaseOwnerProcessAlive(root, runID string) (bool, error) {
	rootFD, runsFD, runFD, err := openRunAuthority(root, runID)
	if err != nil {
		return false, err
	}
	defer unix.Close(rootFD)
	defer unix.Close(runsFD)
	defer unix.Close(runFD)
	owner, err := readLeaseOwnerAt(runFD)
	if err != nil {
		return false, fmt.Errorf("inspect run lease owner: %w", err)
	}
	if owner.PID <= 1 {
		return false, errors.New("inspect run lease owner: invalid pid")
	}
	if err := unix.Kill(owner.PID, 0); err == nil || errors.Is(err, unix.EPERM) {
		return true, nil
	} else if errors.Is(err, unix.ESRCH) {
		return false, nil
	} else {
		return false, fmt.Errorf("probe run lease owner process: %w", err)
	}
}
