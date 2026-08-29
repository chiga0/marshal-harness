//go:build darwin && arm64

package productionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"golang.org/x/sys/unix"
)

const (
	ownerLockNamePrefix = "production-runtime-owner-"
	ownerLockNameSuffix = ".lock"
	ownerPathBufferSize = 4096
)

type ownerDirectoryIdentity struct {
	Path   string
	Device uint64
	Inode  uint64
	Mode   uint32
	UID    uint32
	GID    uint32
}

// darwinRepositoryOwnerPhysicalLock owns the duplicate of the factory-held
// owner-directory descriptor and the locked entry. No security-sensitive
// operation reopens the directory by pathname.
type darwinRepositoryOwnerPhysicalLock struct {
	mu             sync.Mutex
	directory      *os.File
	directoryID    ownerDirectoryIdentity
	file           *os.File
	name           string
	lockIdentity   ownerLockIdentity
	runtimeClaimed bool
	closed         bool
}

// darwinRepositoryOwnerScopeLock is the Phase A type. Keeping it distinct
// from darwinRepositoryOwnerLock makes it impossible to pass a scope-only
// lock to an API requiring CurrentOwnerLockVerifier.
type darwinRepositoryOwnerScopeLock struct {
	mu               sync.Mutex
	physical         *darwinRepositoryOwnerPhysicalLock
	ownerScope       resultingress.ControlOwnerScope
	acquireIssued    bool
	acquireSucceeded bool
	ownerAcquisition resultingress.ControlOwnerAcquisition
	ownerStore       *resultingress.DurableStore
	appendedState    resultingress.ControlOwnerState
	bindIssued       bool
	closed           bool
}

type darwinProvisionalOwnerVerifier struct {
	mu        sync.Mutex
	candidate resultingress.ControlOwnerAcquisition
	invoked   bool
}

type darwinRepositoryOwnerLock struct {
	physical         *darwinRepositoryOwnerPhysicalLock
	ownerAcquisition resultingress.ControlOwnerAcquisition
}

func (lock *darwinRepositoryOwnerLock) claimRuntime() error {
	if lock == nil || lock.physical == nil {
		return application.NewError("production-runtime", application.ReasonOwnerUnavailable)
	}
	lock.physical.mu.Lock()
	defer lock.physical.mu.Unlock()
	if lock.physical.closed || lock.physical.runtimeClaimed {
		return application.NewError("production-runtime", application.ReasonOwnerUnavailable)
	}
	lock.physical.runtimeClaimed = true
	return nil
}

func (lock *darwinRepositoryOwnerLock) claimed() bool {
	if lock == nil || lock.physical == nil {
		return false
	}
	lock.physical.mu.Lock()
	defer lock.physical.mu.Unlock()
	return lock.physical.runtimeClaimed && !lock.physical.closed
}

func openRepositoryOwnerScopeLock(ownerDirectory *os.File, scope resultingress.ControlOwnerScope) (repositoryOwnerScopeLock, error) {
	if ownerDirectory == nil || scope.Validate() != nil {
		return nil, application.NewError("repository-owner-lock", application.ReasonInvalidRequest)
	}
	rawScope, err := json.Marshal(scope)
	if err != nil {
		return nil, application.NewError("repository-owner-lock", application.ReasonInvalidRequest)
	}
	scopeDigest, err := canonical.DigestJSON(rawScope)
	if err != nil {
		return nil, application.NewError("repository-owner-lock", application.ReasonInvalidRequest)
	}
	name := ownerLockNamePrefix + strings.TrimPrefix(scopeDigest, "sha256:") + ownerLockNameSuffix
	directoryFD, err := unix.Dup(int(ownerDirectory.Fd()))
	if err != nil {
		return nil, application.NewError("repository-owner-lock", application.ReasonOwnerUnavailable)
	}
	unix.CloseOnExec(directoryFD)
	directory := os.NewFile(uintptr(directoryFD), "marshal-repository-owner-directory")
	if directory == nil {
		_ = unix.Close(directoryFD)
		return nil, application.NewError("repository-owner-lock", application.ReasonOwnerUnavailable)
	}
	directoryID, err := observeOwnerDirectory(directoryFD)
	if err != nil {
		_ = directory.Close()
		return nil, err
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
	physical := &darwinRepositoryOwnerPhysicalLock{directory: directory, directoryID: directoryID, file: file, name: name, lockIdentity: identity}
	return &darwinRepositoryOwnerScopeLock{physical: physical, ownerScope: scope}, nil
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

func descriptorCurrentPath(fd int) (string, error) {
	buffer := make([]byte, ownerPathBufferSize)
	_, err := unix.FcntlInt(uintptr(fd), unix.F_GETPATH, int(uintptr(unsafe.Pointer(&buffer[0]))))
	if err != nil {
		return "", err
	}
	end := 0
	for end < len(buffer) && buffer[end] != 0 {
		end++
	}
	if end == 0 || end == len(buffer) {
		return "", errors.New("owner directory descriptor has no bounded current path")
	}
	path := string(buffer[:end])
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("owner directory descriptor current path is not canonical")
	}
	return path, nil
}

func observeOwnerDirectory(fd int) (ownerDirectoryIdentity, error) {
	path, err := descriptorCurrentPath(fd)
	var stat unix.Stat_t
	if err != nil || unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Getuid()) || stat.Gid != uint32(os.Getgid()) || stat.Mode&0o777 != 0o700 || stat.Nlink < 2 {
		return ownerDirectoryIdentity{}, application.NewError("repository-owner-lock", application.ReasonOwnerUnavailable)
	}
	return ownerDirectoryIdentity{Path: path, Device: uint64(stat.Dev), Inode: stat.Ino, Mode: uint32(stat.Mode), UID: stat.Uid, GID: stat.Gid}, nil
}

func validateOwnerDirectory(heldFD int, identity ownerDirectoryIdentity) error {
	path, err := descriptorCurrentPath(heldFD)
	var held unix.Stat_t
	if err != nil || unix.Fstat(heldFD, &held) != nil || path != identity.Path || uint64(held.Dev) != identity.Device || held.Ino != identity.Inode || uint32(held.Mode) != identity.Mode || held.Uid != identity.UID || held.Gid != identity.GID || held.Mode&unix.S_IFMT != unix.S_IFDIR || held.Mode&0o777 != 0o700 || held.Nlink < 2 {
		return application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	}
	return nil
}

func validCurrentOwnerReplay(state resultingress.ControlOwnerState, acquisition resultingress.ControlOwnerAcquisition) bool {
	if state.Acquisition != acquisition || acquisition.Validate() != nil || !profileDigestPattern.MatchString(state.FactDigest) {
		return false
	}
	if acquisition.OwnerEpoch == 1 {
		return state.PreviousFactDigest == ""
	}
	return profileDigestPattern.MatchString(state.PreviousFactDigest)
}

func (lock *darwinRepositoryOwnerScopeLock) scope() resultingress.ControlOwnerScope {
	if lock == nil {
		return resultingress.ControlOwnerScope{}
	}
	return lock.ownerScope
}

func (lock *darwinRepositoryOwnerScopeLock) identity() ownerLockIdentity {
	if lock == nil {
		return ownerLockIdentity{}
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.physical == nil {
		return ownerLockIdentity{}
	}
	return lock.physical.lockIdentity
}

func (lock *darwinRepositoryOwnerScopeLock) acquireOwner(ctx context.Context, store *resultingress.DurableStore, expectedEpoch uint64, expectedFactDigest string, candidate resultingress.ControlOwnerAcquisition) (resultingress.ControlOwnerAppendResult, error) {
	if lock == nil || ctx == nil || store == nil || candidate.Validate() != nil || candidate.Scope != lock.ownerScope || candidate.OwnerUID != uint32(os.Getuid()) || candidate.OwnerGID != uint32(os.Getgid()) || candidate.OwnerProcess.PID != os.Getpid() {
		return resultingress.ControlOwnerAppendResult{}, application.NewError("repository-owner-lock", application.ReasonInvalidRequest)
	}
	lock.mu.Lock()
	if lock.closed || lock.physical == nil || lock.acquireIssued || lock.bindIssued {
		lock.mu.Unlock()
		return resultingress.ControlOwnerAppendResult{}, application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	}
	lock.acquireIssued = true
	physical := lock.physical
	lock.mu.Unlock()

	var result resultingress.ControlOwnerAppendResult
	err := physical.withHeld(ctx, false, func() error {
		// The provisional verifier never leaves this stack frame and can
		// authorize exactly this direct AcquireOwner call for exactly this
		// candidate. The physical owner lock is already outermost, so ledger
		// acquisition cannot invert owner→ledger lock ordering.
		verifier := &darwinProvisionalOwnerVerifier{candidate: candidate}
		var acquireErr error
		result, acquireErr = store.AcquireOwner(ctx, verifier, expectedEpoch, expectedFactDigest, candidate)
		return acquireErr
	})
	if err == nil && (!result.Appended || result.State.Acquisition != candidate) {
		err = application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if err == nil && result.Appended && result.State.Acquisition == candidate && !lock.closed && lock.physical == physical {
		lock.acquireSucceeded = true
		lock.ownerAcquisition = candidate
		lock.ownerStore = store
		lock.appendedState = result.State
	}
	return result, err
}

func (lock *darwinRepositoryOwnerScopeLock) bindAcquisition(store *resultingress.DurableStore) (repositoryOwnerLock, error) {
	if lock == nil {
		return nil, application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.closed || lock.physical == nil || lock.bindIssued || !lock.acquireIssued || !lock.acquireSucceeded {
		return nil, application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	}
	// A bind attempt is creation-once even when a foreign store is supplied;
	// retrying a different object would turn Phase B into a setter.
	lock.bindIssued = true
	if store == nil || store != lock.ownerStore {
		return nil, application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	}
	var replayed resultingress.ControlOwnerState
	err := lock.physical.withHeld(context.Background(), false, func() error {
		var found bool
		var replayErr error
		replayed, found, replayErr = store.OpenOwner(lock.ownerScope)
		if replayErr != nil || !found || replayed != lock.appendedState || !validCurrentOwnerReplay(replayed, lock.ownerAcquisition) {
			return application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
		}
		return nil
	})
	if err != nil {
		return nil, application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	}
	physical := lock.physical
	lock.physical = nil
	return &darwinRepositoryOwnerLock{physical: physical, ownerAcquisition: lock.ownerAcquisition}, nil
}

func (verifier *darwinProvisionalOwnerVerifier) WithCurrentOwnerLock(ctx context.Context, acquisition resultingress.ControlOwnerAcquisition, fn func() error) error {
	if verifier == nil || fn == nil || acquisition != verifier.candidate {
		return application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	}
	verifier.mu.Lock()
	if verifier.invoked {
		verifier.mu.Unlock()
		return application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	}
	verifier.invoked = true
	verifier.mu.Unlock()
	select {
	case <-ctx.Done():
		return application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	default:
	}
	return fn()
}

func (physical *darwinRepositoryOwnerPhysicalLock) revalidateLocked() error {
	if physical == nil || physical.closed || physical.file == nil || physical.directory == nil {
		return application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	}
	if err := validateOwnerDirectory(int(physical.directory.Fd()), physical.directoryID); err != nil {
		return err
	}
	identity, err := validateOwnerFile(int(physical.directory.Fd()), int(physical.file.Fd()), physical.name)
	if err != nil || identity != physical.lockIdentity {
		return application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	}
	return nil
}

func (physical *darwinRepositoryOwnerPhysicalLock) withHeld(ctx context.Context, requireRuntime bool, fn func() error) error {
	if physical == nil || fn == nil {
		return application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	}
	physical.mu.Lock()
	defer physical.mu.Unlock()
	select {
	case <-ctx.Done():
		return application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	default:
	}
	if requireRuntime && !physical.runtimeClaimed {
		return application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	}
	if err := physical.revalidateLocked(); err != nil {
		return err
	}
	return fn()
}

func (physical *darwinRepositoryOwnerPhysicalLock) close() error {
	if physical == nil {
		return nil
	}
	physical.mu.Lock()
	defer physical.mu.Unlock()
	if physical.closed {
		return nil
	}
	physical.closed = true
	var failed bool
	if physical.file != nil {
		if unix.Flock(int(physical.file.Fd()), unix.LOCK_UN) != nil {
			failed = true
		}
		if physical.file.Close() != nil {
			failed = true
		}
		physical.file = nil
	}
	if physical.directory != nil {
		if physical.directory.Close() != nil {
			failed = true
		}
		physical.directory = nil
	}
	if failed {
		return application.NewError("repository-owner-lock", application.ReasonOwnerUnavailable)
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
	if lock == nil || lock.physical == nil {
		return ownerLockIdentity{}
	}
	return lock.physical.lockIdentity
}

func (lock *darwinRepositoryOwnerLock) WithCurrentOwnerLock(ctx context.Context, acquisition resultingress.ControlOwnerAcquisition, fn func() error) error {
	if lock == nil || lock.physical == nil || fn == nil || acquisition != lock.ownerAcquisition {
		return application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	}
	return lock.physical.withHeld(ctx, true, fn)
}

func (lock *darwinRepositoryOwnerLock) Close() error {
	if lock == nil || lock.physical == nil {
		return nil
	}
	return lock.physical.close()
}

func (lock *darwinRepositoryOwnerScopeLock) Close() error {
	if lock == nil {
		return nil
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.closed {
		return nil
	}
	lock.closed = true
	if lock.physical == nil {
		return nil
	}
	return lock.physical.close()
}
