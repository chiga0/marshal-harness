//go:build darwin || linux

package runstore

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func probeLeaseOwnerProcessAlive(root, runID string) (alive bool, resultErr error) {
	rootFD, runsFD, runFD, err := openRunAuthority(root, runID)
	if err != nil {
		return false, err
	}
	defer unix.Close(rootFD)
	defer unix.Close(runsFD)
	defer unix.Close(runFD)
	leaseFD, err := unix.Openat(runFD, "lease.lock", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return false, fmt.Errorf("inspect run lease: %w", err)
	}
	defer unix.Close(leaseFD)
	var stat unix.Stat_t
	if err := unix.Fstat(leaseFD, &stat); err != nil {
		return false, fmt.Errorf("fstat run lease: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return false, errors.New("inspect run lease: lock descriptor is not a single-link regular file")
	}
	if err := unix.Flock(leaseFD, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			// The exact canonical lease descriptor is held. That authority is
			// stronger than the advisory PID and also covers the short Acquire
			// window before its successor owner record is atomically installed.
			return true, nil
		}
		return false, fmt.Errorf("probe run lease: %w", err)
	}
	defer func() {
		if err := unix.Flock(leaseFD, unix.LOCK_UN); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("release run lease owner probe: %w", err))
		}
	}()
	owner, err := readLeaseOwnerAt(runFD)
	if err != nil {
		return false, fmt.Errorf("inspect run lease owner: %w", err)
	}
	if owner.Device != uint64(stat.Dev) || owner.Inode != uint64(stat.Ino) {
		return false, errors.New("inspect run lease: owner identity does not bind the opened lock descriptor")
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
