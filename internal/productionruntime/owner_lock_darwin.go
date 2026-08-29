//go:build darwin && arm64

package productionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"golang.org/x/sys/unix"
)

type darwinRepositoryOwnerLock struct {
	mu               sync.Mutex
	directory        *os.File
	stateRoot        string
	directoryID      ownerLockIdentity
	file             *os.File
	name             string
	lockIdentity     ownerLockIdentity
	ownerAcquisition resultingress.ControlOwnerAcquisition
	runtimeClaimed   bool
	closed           bool
}

func (lock *darwinRepositoryOwnerLock) claimRuntime() error {
	if lock == nil {
		return application.NewError("production-runtime", application.ReasonOwnerUnavailable)
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.closed || lock.runtimeClaimed {
		return application.NewError("production-runtime", application.ReasonOwnerUnavailable)
	}
	lock.runtimeClaimed = true
	return nil
}

func (lock *darwinRepositoryOwnerLock) claimed() bool {
	if lock == nil {
		return false
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	return lock.runtimeClaimed && !lock.closed
}

func openRepositoryOwnerLock(stateRoot string, acquisition resultingress.ControlOwnerAcquisition) (repositoryOwnerLock, error) {
	if !cleanAbsolutePath(stateRoot) || acquisition.Validate() != nil || acquisition.OwnerUID != uint32(os.Getuid()) || acquisition.OwnerGID != uint32(os.Getgid()) || acquisition.OwnerProcess.PID != os.Getpid() {
		return nil, application.NewError("repository-owner-lock", application.ReasonInvalidRequest)
	}
	rawScope, err := json.Marshal(acquisition.Scope)
	if err != nil {
		return nil, application.NewError("repository-owner-lock", application.ReasonInvalidRequest)
	}
	scopeDigest, err := canonical.DigestJSON(rawScope)
	if err != nil {
		return nil, application.NewError("repository-owner-lock", application.ReasonInvalidRequest)
	}
	name := "production-runtime-owner-" + strings.TrimPrefix(scopeDigest, "sha256:") + ".lock"
	directoryFD, err := unix.Open(stateRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return nil, application.NewError("repository-owner-lock", application.ReasonOwnerUnavailable)
	}
	directory := os.NewFile(uintptr(directoryFD), stateRoot)
	if directory == nil {
		_ = unix.Close(directoryFD)
		return nil, application.NewError("repository-owner-lock", application.ReasonOwnerUnavailable)
	}
	var directoryStat unix.Stat_t
	if unix.Fstat(directoryFD, &directoryStat) != nil || directoryStat.Mode&unix.S_IFMT != unix.S_IFDIR || directoryStat.Uid != uint32(os.Getuid()) || directoryStat.Mode&0o022 != 0 {
		_ = directory.Close()
		return nil, application.NewError("repository-owner-lock", application.ReasonOwnerUnavailable)
	}
	fileFD, err := openOwnerFile(directoryFD, name)
	if err != nil {
		_ = directory.Close()
		return nil, err
	}
	file := os.NewFile(uintptr(fileFD), name)
	if file == nil {
		_ = unix.Close(fileFD)
		_ = directory.Close()
		return nil, application.NewError("repository-owner-lock", application.ReasonOwnerUnavailable)
	}
	if err := unix.Flock(fileFD, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		_ = directory.Close()
		return nil, application.NewError("repository-owner-lock", application.ReasonOwnerUnavailable)
	}
	identity, err := validateOwnerFile(directoryFD, fileFD, name)
	if err != nil {
		_ = unix.Flock(fileFD, unix.LOCK_UN)
		_ = file.Close()
		_ = directory.Close()
		return nil, err
	}
	return &darwinRepositoryOwnerLock{directory: directory, stateRoot: stateRoot, directoryID: ownerLockIdentity{Device: uint64(directoryStat.Dev), Inode: directoryStat.Ino}, file: file, name: name, lockIdentity: identity, ownerAcquisition: acquisition}, nil
}

func openOwnerFile(directoryFD int, name string) (int, error) {
	fd, err := unix.Openat(directoryFD, name, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		fd, err = unix.Openat(directoryFD, name, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0o600)
	}
	if err != nil {
		return -1, application.NewError("repository-owner-lock", application.ReasonOwnerUnavailable)
	}
	return fd, nil
}

func validateOwnerFile(directoryFD, fileFD int, name string) (ownerLockIdentity, error) {
	var held, named unix.Stat_t
	if unix.Fstat(fileFD, &held) != nil || unix.Fstatat(directoryFD, name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || held.Dev != named.Dev || held.Ino != named.Ino || held.Mode != named.Mode || held.Uid != named.Uid || held.Gid != named.Gid || held.Nlink != named.Nlink || held.Mode&unix.S_IFMT != unix.S_IFREG || held.Mode&0o777 != 0o600 || held.Uid != uint32(os.Getuid()) || held.Gid != uint32(os.Getgid()) || held.Nlink != 1 || held.Size != 0 {
		return ownerLockIdentity{}, application.NewError("repository-owner-lock", application.ReasonOwnerUnavailable)
	}
	return ownerLockIdentity{Device: uint64(held.Dev), Inode: held.Ino}, nil
}

func validateOwnerDirectory(stateRoot string, heldFD int, identity ownerLockIdentity) error {
	currentFD, err := unix.Open(stateRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	}
	defer unix.Close(currentFD) //nolint:errcheck -- validation result is already fixed
	var held, current unix.Stat_t
	if unix.Fstat(heldFD, &held) != nil || unix.Fstat(currentFD, &current) != nil || held.Dev != current.Dev || held.Ino != current.Ino || uint64(held.Dev) != identity.Device || held.Ino != identity.Inode || held.Mode&unix.S_IFMT != unix.S_IFDIR || held.Uid != uint32(os.Getuid()) || held.Mode&0o022 != 0 {
		return application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	}
	return nil
}

func (lock *darwinRepositoryOwnerLock) acquisition() resultingress.ControlOwnerAcquisition {
	if lock == nil {
		return resultingress.ControlOwnerAcquisition{}
	}
	return lock.ownerAcquisition
}

func (lock *darwinRepositoryOwnerLock) identity() ownerLockIdentity {
	if lock == nil {
		return ownerLockIdentity{}
	}
	return lock.lockIdentity
}

func (lock *darwinRepositoryOwnerLock) WithCurrentOwnerLock(ctx context.Context, acquisition resultingress.ControlOwnerAcquisition, fn func() error) error {
	if lock == nil || fn == nil || acquisition != lock.ownerAcquisition {
		return application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.closed || !lock.runtimeClaimed || lock.file == nil || lock.directory == nil {
		return application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	}
	select {
	case <-ctx.Done():
		return application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	default:
	}
	if err := validateOwnerDirectory(lock.stateRoot, int(lock.directory.Fd()), lock.directoryID); err != nil {
		return err
	}
	identity, err := validateOwnerFile(int(lock.directory.Fd()), int(lock.file.Fd()), lock.name)
	if err != nil || identity != lock.lockIdentity {
		return application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	}
	return fn()
}

func (lock *darwinRepositoryOwnerLock) Close() error {
	if lock == nil {
		return nil
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.closed {
		return nil
	}
	lock.closed = true
	var failed bool
	if lock.file != nil {
		if unix.Flock(int(lock.file.Fd()), unix.LOCK_UN) != nil {
			failed = true
		}
		if lock.file.Close() != nil {
			failed = true
		}
	}
	if lock.directory != nil && lock.directory.Close() != nil {
		failed = true
	}
	if failed {
		return application.NewError("repository-owner-lock", application.ReasonOwnerUnavailable)
	}
	return nil
}
