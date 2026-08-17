//go:build darwin || linux

package runstore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func acquireLeaseFile(root, runID string) (*os.File, uint64, uint64, bool, error) {
	rootFD, runsFD, runFD, err := openRunAuthority(root, runID)
	if err != nil {
		return nil, 0, 0, false, err
	}
	defer unix.Close(rootFD)
	defer unix.Close(runsFD)
	defer unix.Close(runFD)
	created := false
	leaseFD, err := unix.Openat(runFD, "lease.lock", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		leaseFD, err = unix.Openat(runFD, "lease.lock", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0o600)
		created = err == nil
	}
	if err != nil {
		return nil, 0, 0, false, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(leaseFD, &stat); err != nil {
		unix.Close(leaseFD)
		return nil, 0, 0, false, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		unix.Close(leaseFD)
		return nil, 0, 0, false, errors.New("lease lock descriptor is not a single-link regular file")
	}
	if err := unix.Flock(leaseFD, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		unix.Close(leaseFD)
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, 0, 0, false, nil
		}
		return nil, 0, 0, false, err
	}
	if !created {
		owner, err := readLeaseOwnerAt(runFD)
		if err != nil {
			unix.Flock(leaseFD, unix.LOCK_UN)
			unix.Close(leaseFD)
			return nil, 0, 0, false, fmt.Errorf("validate existing lease owner: %w", err)
		}
		if owner.Device != uint64(stat.Dev) || owner.Inode != uint64(stat.Ino) {
			unix.Flock(leaseFD, unix.LOCK_UN)
			unix.Close(leaseFD)
			return nil, 0, 0, false, errors.New("existing lease owner does not bind the opened lock descriptor")
		}
	}
	return os.NewFile(uintptr(leaseFD), "lease.lock"), uint64(stat.Dev), uint64(stat.Ino), true, nil
}

func releaseLeaseFile(file *os.File) error {
	if file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
	return errors.Join(unlockErr, file.Close())
}

func writeLeaseOwner(root, runID string, data []byte) error {
	rootFD, runsFD, runFD, err := openRunAuthority(root, runID)
	if err != nil {
		return err
	}
	defer unix.Close(rootFD)
	defer unix.Close(runsFD)
	defer unix.Close(runFD)
	if _, err := readLeaseOwnerAt(runFD); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	temporary := ".lease.lock.owner-" + hex.EncodeToString(random[:]) + ".pending"
	fd, err := unix.Openat(runFD, temporary, unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), temporary)
	defer func() {
		file.Close()
		_ = unix.Unlinkat(runFD, temporary, 0)
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := unix.Renameat(runFD, temporary, runFD, "lease.lock.owner"); err != nil {
		return err
	}
	return unix.Fsync(runFD)
}

func readLeaseOwnerAt(runFD int) (leaseOwnerRecord, error) {
	ownerFD, err := unix.Openat(runFD, "lease.lock.owner", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return leaseOwnerRecord{}, os.ErrNotExist
		}
		return leaseOwnerRecord{}, err
	}
	ownerFile := os.NewFile(uintptr(ownerFD), "lease.lock.owner")
	defer ownerFile.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(ownerFD, &stat); err != nil {
		return leaseOwnerRecord{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return leaseOwnerRecord{}, errors.New("lease owner descriptor is not a single-link regular file")
	}
	var owner leaseOwnerRecord
	if err := json.NewDecoder(io.LimitReader(ownerFile, 16<<10)).Decode(&owner); err != nil {
		return leaseOwnerRecord{}, err
	}
	if owner.Device == 0 || owner.Inode == 0 {
		return leaseOwnerRecord{}, errors.New("lease owner record lacks descriptor identity")
	}
	return owner, nil
}

func leaseStillAuthoritative(lease *Lease) error {
	rootFD, runsFD, runFD, err := openRunAuthority(lease.root, lease.runID)
	if err != nil {
		return err
	}
	defer unix.Close(rootFD)
	defer unix.Close(runsFD)
	defer unix.Close(runFD)
	currentFD, err := unix.Openat(runFD, "lease.lock", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer unix.Close(currentFD)
	var current, held unix.Stat_t
	if err := unix.Fstat(currentFD, &current); err != nil {
		return err
	}
	if err := unix.Fstat(int(lease.file.Fd()), &held); err != nil {
		return err
	}
	if uint64(current.Dev) != lease.dev || uint64(current.Ino) != lease.inode || uint64(held.Dev) != lease.dev || uint64(held.Ino) != lease.inode {
		return errors.New("lease pathname no longer binds the held descriptor")
	}
	owner, err := readLeaseOwnerAt(runFD)
	if err != nil {
		return err
	}
	if owner.Device != lease.dev || owner.Inode != lease.inode {
		return errors.New("lease owner no longer binds the held descriptor")
	}
	return nil
}

func openRunAuthority(root, runID string) (int, int, int, error) {
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, -1, -1, fmt.Errorf("inspect state root: %w", err)
	}
	runsFD, err := unix.Openat(rootFD, "runs", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		unix.Close(rootFD)
		return -1, -1, -1, fmt.Errorf("inspect runs directory: %w", err)
	}
	runFD, err := unix.Openat(runsFD, runID, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		unix.Close(runsFD)
		unix.Close(rootFD)
		return -1, -1, -1, fmt.Errorf("inspect run directory: %w", err)
	}
	return rootFD, runsFD, runFD, nil
}

// probeLeaseHeld walks the authority directory through no-follow directory
// descriptors, opens lease.lock relative to the bound run directory, and
// probes the lock on that same descriptor. This removes the Lstat -> reopen
// pathname window in which an attacker could replace either a directory or
// the lock inode (ABA).
func probeLeaseHeld(root, runID string) (bool, error) {
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
	owner, err := readLeaseOwnerAt(runFD)
	if err != nil {
		return false, fmt.Errorf("inspect run lease owner: %w", err)
	}
	if owner.Device == 0 || owner.Inode == 0 || owner.Device != uint64(stat.Dev) || owner.Inode != uint64(stat.Ino) {
		return false, errors.New("inspect run lease: owner identity does not bind the opened lock descriptor")
	}
	if err := unix.Flock(leaseFD, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return true, nil
		}
		return false, fmt.Errorf("probe run lease: %w", err)
	}
	if err := unix.Flock(leaseFD, unix.LOCK_UN); err != nil {
		return false, fmt.Errorf("release run lease probe: %w", err)
	}
	return false, nil
}
