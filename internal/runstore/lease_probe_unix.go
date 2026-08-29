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

	"github.com/chiga0/marshal-harness/internal/domain"
	"golang.org/x/sys/unix"
)

func appendRegularAt(runFD int, name string, data []byte) error {
	fd, err := unix.Openat(runFD, name, unix.O_WRONLY|unix.O_APPEND|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		fd, err = unix.Openat(runFD, name, unix.O_WRONLY|unix.O_APPEND|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0o600)
	}
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return errors.New("authority journal is not a single-link regular file")
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func readRegularAt(directoryFD int, name string, limit int64) ([]byte, error) {
	fd, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return nil, errors.New("authority record is not a single-link regular file")
	}
	if limit > 0 && stat.Size > limit {
		return nil, fmt.Errorf("authority record exceeds %d bytes", limit)
	}
	reader := io.Reader(file)
	if limit > 0 {
		reader = io.LimitReader(file, limit+1)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if limit > 0 && int64(len(data)) > limit {
		return nil, fmt.Errorf("authority record exceeds %d bytes", limit)
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return nil, err
	}
	if after.Dev != stat.Dev || after.Ino != stat.Ino || after.Size != stat.Size || after.Mode != stat.Mode || after.Nlink != stat.Nlink {
		return nil, errors.New("authority record identity changed while reading")
	}
	return data, nil
}

func readEventsAt(runFD int) ([]domain.RunEvent, bool, error) {
	data, err := readRegularAt(runFD, "events.jsonl", 0)
	if err != nil {
		return nil, false, err
	}
	return decodeEvents(data)
}

func appendRegularInDirectoryAt(runFD int, directory, name string, data []byte) error {
	directoryFD, err := openDirectoryAt(runFD, directory, true)
	if err != nil {
		return err
	}
	defer unix.Close(directoryFD)
	if err := appendRegularAt(directoryFD, name, data); err != nil {
		return err
	}
	return unix.Fsync(directoryFD)
}

func openDirectoryAt(parentFD int, name string, create bool) (int, error) {
	if create {
		if err := unix.Mkdirat(parentFD, name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return -1, err
		}
	}
	return unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
}

func writeSnapshotAt(runFD int, data []byte) error {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	temporary := ".state-" + hex.EncodeToString(token[:]) + ".tmp"
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
	if err := unix.Renameat(runFD, temporary, runFD, "state.json"); err != nil {
		return err
	}
	return unix.Fsync(runFD)
}

func acquireLeaseFile(root, runID string) (*os.File, *os.File, uint64, uint64, bool, error) {
	return acquireLeaseFileMode(root, runID, true)
}

func acquireExistingLeaseFile(root, runID string) (*os.File, *os.File, uint64, uint64, bool, error) {
	return acquireLeaseFileMode(root, runID, false)
}

func acquireLeaseFileMode(root, runID string, createLock bool) (*os.File, *os.File, uint64, uint64, bool, error) {
	rootFD, runsFD, runFD, err := openRunAuthority(root, runID)
	if err != nil {
		return nil, nil, 0, 0, false, err
	}
	defer unix.Close(rootFD)
	defer unix.Close(runsFD)
	defer unix.Close(runFD)
	created := false
	leaseFD, err := unix.Openat(runFD, "lease.lock", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if createLock && errors.Is(err, unix.ENOENT) {
		leaseFD, err = unix.Openat(runFD, "lease.lock", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0o600)
		created = err == nil
	}
	if err != nil {
		return nil, nil, 0, 0, false, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(leaseFD, &stat); err != nil {
		unix.Close(leaseFD)
		return nil, nil, 0, 0, false, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		unix.Close(leaseFD)
		return nil, nil, 0, 0, false, errors.New("lease lock descriptor is not a single-link regular file")
	}
	if err := unix.Flock(leaseFD, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		unix.Close(leaseFD)
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, nil, 0, 0, false, nil
		}
		return nil, nil, 0, 0, false, err
	}
	if !created {
		owner, err := readLeaseOwnerAt(runFD)
		if err != nil && !errors.Is(err, errLegacyLeaseOwner) {
			unix.Flock(leaseFD, unix.LOCK_UN)
			unix.Close(leaseFD)
			return nil, nil, 0, 0, false, fmt.Errorf("validate existing lease owner: %w", err)
		}
		if err == nil && (owner.Device != uint64(stat.Dev) || owner.Inode != uint64(stat.Ino)) {
			unix.Flock(leaseFD, unix.LOCK_UN)
			unix.Close(leaseFD)
			return nil, nil, 0, 0, false, errors.New("existing lease owner does not bind the opened lock descriptor")
		}
	}
	boundRunFD, err := unix.Dup(runFD)
	if err != nil {
		unix.Flock(leaseFD, unix.LOCK_UN)
		unix.Close(leaseFD)
		return nil, nil, 0, 0, false, err
	}
	unix.CloseOnExec(boundRunFD)
	return os.NewFile(uintptr(leaseFD), "lease.lock"), os.NewFile(uintptr(boundRunFD), "run-authority"), uint64(stat.Dev), uint64(stat.Ino), true, nil
}

// validateAcquiredLeaseCurrent reopens the canonical Run pathname after an
// existing-only caller has obtained the lock and immediately before it writes
// a new owner record. This prevents a rename/replacement window from causing
// owner bytes to be installed in a detached or newly substituted Run object.
func validateAcquiredLeaseCurrent(root, runID string, boundRun, heldLease *os.File, device, inode uint64) error {
	if boundRun == nil || heldLease == nil {
		return errors.New("existing Run acquisition lacks bound descriptors")
	}
	rootFD, runsFD, currentRunFD, err := openRunAuthority(root, runID)
	if err != nil {
		return err
	}
	defer unix.Close(rootFD)
	defer unix.Close(runsFD)
	defer unix.Close(currentRunFD)

	var boundRunStat, currentRunStat unix.Stat_t
	if err := unix.Fstat(int(boundRun.Fd()), &boundRunStat); err != nil {
		return err
	}
	if err := unix.Fstat(currentRunFD, &currentRunStat); err != nil {
		return err
	}
	if boundRunStat.Dev != currentRunStat.Dev || boundRunStat.Ino != currentRunStat.Ino {
		return errors.New("run authority pathname no longer binds the acquired directory")
	}

	currentLeaseFD, err := unix.Openat(currentRunFD, "lease.lock", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer unix.Close(currentLeaseFD)
	var currentLeaseStat, heldLeaseStat unix.Stat_t
	if err := unix.Fstat(currentLeaseFD, &currentLeaseStat); err != nil {
		return err
	}
	if err := unix.Fstat(int(heldLease.Fd()), &heldLeaseStat); err != nil {
		return err
	}
	if currentLeaseStat.Mode&unix.S_IFMT != unix.S_IFREG || currentLeaseStat.Nlink != 1 {
		return errors.New("canonical lease is not a single-link regular file")
	}
	if uint64(currentLeaseStat.Dev) != device || uint64(currentLeaseStat.Ino) != inode || uint64(heldLeaseStat.Dev) != device || uint64(heldLeaseStat.Ino) != inode {
		return errors.New("lease pathname no longer binds the acquired descriptor")
	}
	owner, err := readLeaseOwnerAt(currentRunFD)
	if err != nil && !errors.Is(err, errLegacyLeaseOwner) {
		return fmt.Errorf("validate existing lease owner: %w", err)
	}
	if err == nil && (owner.Device != device || owner.Inode != inode) {
		return errors.New("existing lease owner no longer binds the acquired descriptor")
	}
	return nil
}

func releaseLeaseFile(file *os.File) error {
	if file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
	return errors.Join(unlockErr, file.Close())
}

func writeLeaseOwnerAt(runFD int, data []byte) error {
	if _, err := readLeaseOwnerAt(runFD); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, errLegacyLeaseOwner) {
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
		return leaseOwnerRecord{}, errLegacyLeaseOwner
	}
	return owner, nil
}

// OpenRunAuthority opens the current canonical run pathname, validates that
// exact directory descriptor against the held lease and owner records, and
// returns it without reopening by pathname. CAS reads and mutations must use
// this returned descriptor for the entire operation.
func OpenRunAuthority(lease *Lease) (*os.File, error) {
	if lease == nil || lease.runDir == nil || lease.file == nil || !lease.held {
		return nil, errors.New("lease lacks bound authority descriptors")
	}
	rootFD, runsFD, currentRunFD, err := openRunAuthority(lease.root, lease.runID)
	if err != nil {
		return nil, err
	}
	unix.Close(rootFD)
	unix.Close(runsFD)
	var boundRun, currentRun unix.Stat_t
	if err := unix.Fstat(int(lease.runDir.Fd()), &boundRun); err != nil {
		unix.Close(currentRunFD)
		return nil, err
	}
	if err := unix.Fstat(currentRunFD, &currentRun); err != nil {
		unix.Close(currentRunFD)
		return nil, err
	}
	if boundRun.Dev != currentRun.Dev || boundRun.Ino != currentRun.Ino {
		unix.Close(currentRunFD)
		return nil, errors.New("run authority pathname no longer binds the held directory")
	}
	if err := leaseDescriptorAuthoritativeAt(lease, currentRunFD); err != nil {
		unix.Close(currentRunFD)
		return nil, err
	}
	return os.NewFile(uintptr(currentRunFD), "canonical-run-authority"), nil
}

func leaseDescriptorAuthoritativeAt(lease *Lease, runFD int) error {
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

// DupRunDirectory returns a close-on-exec duplicate of the directory
// descriptor bound by the held lease. Callers use it for descriptor-relative
// attempt persistence without reopening the mutable run pathname.
func DupRunDirectory(lease *Lease) (*os.File, error) {
	return OpenRunAuthority(lease)
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
func probeLeaseHeld(root, runID string) (held bool, resultErr error) {
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
			// A held lock on the exact canonical descriptor is authoritative.
			// Do not read lease.lock.owner here: its atomic handoff can unlink
			// the previously opened record while the successor owner holds this
			// lock, which must remain a live (not unknown/dead) observation.
			return true, nil
		}
		return false, fmt.Errorf("probe run lease: %w", err)
	}
	defer func() {
		if err := unix.Flock(leaseFD, unix.LOCK_UN); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("release run lease probe: %w", err))
		}
	}()
	// Once the probe lock is held, owner installation cannot race this read.
	// The free-lock result is valid only when the durable owner record still
	// binds the exact descriptor that was probed.
	owner, err := readLeaseOwnerAt(runFD)
	if err != nil {
		return false, fmt.Errorf("inspect run lease owner: %w", err)
	}
	if owner.Device == 0 || owner.Inode == 0 || owner.Device != uint64(stat.Dev) || owner.Inode != uint64(stat.Ino) {
		return false, errors.New("inspect run lease: owner identity does not bind the opened lock descriptor")
	}
	return false, nil
}
