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
	Path     string
	Device   uint64
	Inode    uint64
	Mode     uint32
	UID      uint32
	GID      uint32
	Mutation ownerMutationIdentity
}

type ownerParentIdentity struct {
	Path   string
	Device uint64
	Inode  uint64
	Mode   uint32
	UID    uint32
	GID    uint32
}

type ownerMutationIdentity struct {
	ChangeSeconds int64
	ChangeNanos   int64
	BirthSeconds  int64
	BirthNanos    int64
	Generation    uint32
}

type ownerFileIdentity struct {
	Object    ownerLockIdentity
	Mode      uint32
	UID       uint32
	GID       uint32
	LinkCount uint64
	Size      int64
	Mutation  ownerMutationIdentity
}

// darwinRepositoryOwnerPhysicalLock owns the duplicate of the factory-held
// owner-directory descriptor and the locked entry. No security-sensitive
// operation reopens the directory by pathname.
type darwinRepositoryOwnerPhysicalLock struct {
	mu             sync.Mutex
	parent         *os.File
	parentID       ownerParentIdentity
	directory      *os.File
	directoryID    ownerDirectoryIdentity
	directoryName  string
	file           *os.File
	name           string
	lockIdentity   ownerFileIdentity
	runtimeClaimed bool
	closed         bool
}

// darwinRepositoryOwnerScopeLock is the Phase A type. Keeping it distinct
// from darwinRepositoryOwnerLock makes it impossible to pass a scope-only
// lock to an API requiring CurrentOwnerLockVerifier.
type darwinRepositoryOwnerScopeLock struct {
	mu             sync.Mutex
	physical       *darwinRepositoryOwnerPhysicalLock
	ownerScope     resultingress.ControlOwnerScope
	transitionUsed bool
	closed         bool
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

// repositoryOwnerTransitionFailureKind is an internal, closed diagnostic
// vocabulary. Public callers still receive only ReasonOwnerNotCurrent; tests
// and internal diagnostics can distinguish the failed trust boundary without
// exposing a pathname, inode or raw I/O error.
type repositoryOwnerTransitionFailureKind string

const (
	repositoryOwnerFailureOwnerIdentityDrift repositoryOwnerTransitionFailureKind = "owner-identity-drift"
	repositoryOwnerFailureIngressIdentityIO  repositoryOwnerTransitionFailureKind = "ingress-identity-or-io"
	repositoryOwnerFailureReplayConflict     repositoryOwnerTransitionFailureKind = "owner-replay-conflict"
)

type repositoryOwnerTransitionError struct {
	kind repositoryOwnerTransitionFailureKind
}

func (err *repositoryOwnerTransitionError) Error() string {
	return application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent).Error()
}

func (err *repositoryOwnerTransitionError) Unwrap() error {
	return application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
}

func newRepositoryOwnerTransitionError(kind repositoryOwnerTransitionFailureKind) error {
	return &repositoryOwnerTransitionError{kind: kind}
}

func repositoryOwnerTransitionKind(err error) (repositoryOwnerTransitionFailureKind, bool) {
	var transition *repositoryOwnerTransitionError
	if !errors.As(err, &transition) || transition == nil {
		return "", false
	}
	return transition.kind, true
}

func repositoryOwnerTransitionLabel(err error) (string, bool) {
	kind, classified := repositoryOwnerTransitionKind(err)
	return string(kind), classified
}

func ownerReplayFailure(err error) error {
	if errors.Is(err, resultingress.ErrDurableReplayConflict) || errors.Is(err, resultingress.ErrControlOwnerConflict) || errors.Is(err, resultingress.ErrControlOwnerUnknown) || errors.Is(err, resultingress.ErrControlOwnerNotCurrent) {
		return newRepositoryOwnerTransitionError(repositoryOwnerFailureReplayConflict)
	}
	return newRepositoryOwnerTransitionError(repositoryOwnerFailureIngressIdentityIO)
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
	preCreateDirectoryID, err := observeOwnerDirectory(directoryFD)
	if err != nil {
		_ = directory.Close()
		return nil, err
	}
	parentFD, err := unix.Openat(directoryFD, "..", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		_ = directory.Close()
		return nil, application.NewError("repository-owner-lock", application.ReasonOwnerUnavailable)
	}
	parent := os.NewFile(uintptr(parentFD), "marshal-repository-owner-parent")
	if parent == nil {
		_ = unix.Close(parentFD)
		_ = directory.Close()
		return nil, application.NewError("repository-owner-lock", application.ReasonOwnerUnavailable)
	}
	parentID, err := observeOwnerParent(parentFD)
	directoryName := filepath.Base(preCreateDirectoryID.Path)
	if err != nil || directoryName == "." || directoryName == string(filepath.Separator) || filepath.Join(parentID.Path, directoryName) != preCreateDirectoryID.Path || validateOwnerDirectory(parentFD, directoryFD, directoryName, parentID, preCreateDirectoryID) != nil {
		_ = parent.Close()
		_ = directory.Close()
		return nil, application.NewError("repository-owner-lock", application.ReasonOwnerUnavailable)
	}
	fileFD, created, err := openOwnerFile(directoryFD, name)
	if err != nil {
		_ = parent.Close()
		_ = directory.Close()
		return nil, err
	}
	file := os.NewFile(uintptr(fileFD), name)
	if file == nil {
		_ = unix.Close(fileFD)
		_ = parent.Close()
		_ = directory.Close()
		return nil, application.NewError("repository-owner-lock", application.ReasonOwnerUnavailable)
	}
	if err := unix.Flock(fileFD, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		_ = parent.Close()
		_ = directory.Close()
		return nil, application.NewError("repository-owner-lock", application.ReasonOwnerUnavailable)
	}
	// A newly created coordination entry must be durable before its file and
	// containing-directory mutation identities are frozen.  On APFS, observing
	// those identities immediately after O_CREAT and only syncing unrelated
	// authority files later can make the first current-owner recheck see the
	// creation metadata settle as an apparent hostile drift.  This mirrors the
	// ResultIngress held-file boundary: sync the new leaf first, then its parent,
	// and only then attest the stable current names.  Existing entries are
	// already durable and are left untouched.
	if created && (unix.Fsync(fileFD) != nil || unix.Fsync(directoryFD) != nil) {
		_ = unix.Flock(fileFD, unix.LOCK_UN)
		_ = file.Close()
		_ = parent.Close()
		_ = directory.Close()
		return nil, application.NewError("repository-owner-lock", application.ReasonOwnerUnavailable)
	}
	identity, err := observeOwnerFile(directoryFD, fileFD, name)
	if err != nil {
		_ = unix.Flock(fileFD, unix.LOCK_UN)
		_ = file.Close()
		_ = parent.Close()
		_ = directory.Close()
		return nil, err
	}
	// Creating a fresh lock entry legitimately changes the owner directory.
	// Freeze directory mutation evidence only after the entry is stable.
	directoryID, err := observeOwnerDirectory(directoryFD)
	if err != nil || !sameOwnerDirectoryObject(preCreateDirectoryID, directoryID) || validateOwnerDirectory(parentFD, directoryFD, directoryName, parentID, directoryID) != nil {
		_ = unix.Flock(fileFD, unix.LOCK_UN)
		_ = file.Close()
		_ = parent.Close()
		_ = directory.Close()
		return nil, application.NewError("repository-owner-lock", application.ReasonOwnerUnavailable)
	}
	physical := &darwinRepositoryOwnerPhysicalLock{parent: parent, parentID: parentID, directory: directory, directoryID: directoryID, directoryName: directoryName, file: file, name: name, lockIdentity: identity}
	return &darwinRepositoryOwnerScopeLock{physical: physical, ownerScope: scope}, nil
}

func openOwnerFile(directoryFD int, name string) (int, bool, error) {
	fd, err := unix.Openat(directoryFD, name, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	created := false
	if errors.Is(err, unix.ENOENT) {
		fd, err = unix.Openat(directoryFD, name, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0o600)
		created = err == nil
	}
	if err != nil {
		return -1, false, application.NewError("repository-owner-lock", application.ReasonOwnerUnavailable)
	}
	return fd, created, nil
}

func mutationIdentity(stat unix.Stat_t) ownerMutationIdentity {
	return ownerMutationIdentity{ChangeSeconds: stat.Ctim.Sec, ChangeNanos: stat.Ctim.Nsec, BirthSeconds: stat.Btim.Sec, BirthNanos: stat.Btim.Nsec, Generation: stat.Gen}
}

func observeOwnerFile(directoryFD, fileFD int, name string) (ownerFileIdentity, error) {
	var held, named unix.Stat_t
	if unix.Fstat(fileFD, &held) != nil || unix.Fstatat(directoryFD, name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || held.Dev != named.Dev || held.Ino != named.Ino || held.Mode != named.Mode || held.Uid != named.Uid || held.Gid != named.Gid || held.Nlink != named.Nlink || held.Size != named.Size || held.Ctim != named.Ctim || held.Btim != named.Btim || held.Gen != named.Gen || held.Mode&unix.S_IFMT != unix.S_IFREG || held.Mode&0o777 != 0o600 || held.Uid != uint32(os.Getuid()) || held.Gid != uint32(os.Getgid()) || held.Nlink != 1 || held.Size != 0 {
		return ownerFileIdentity{}, application.NewError("repository-owner-lock", application.ReasonOwnerUnavailable)
	}
	return ownerFileIdentity{Object: ownerLockIdentity{Device: uint64(held.Dev), Inode: held.Ino}, Mode: uint32(held.Mode), UID: held.Uid, GID: held.Gid, LinkCount: uint64(held.Nlink), Size: held.Size, Mutation: mutationIdentity(held)}, nil
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
	return ownerDirectoryIdentity{Path: path, Device: uint64(stat.Dev), Inode: stat.Ino, Mode: uint32(stat.Mode), UID: stat.Uid, GID: stat.Gid, Mutation: mutationIdentity(stat)}, nil
}

func observeOwnerParent(fd int) (ownerParentIdentity, error) {
	path, err := descriptorCurrentPath(fd)
	var stat unix.Stat_t
	if err != nil || unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Getuid()) || stat.Mode&0o777 != 0o700 || stat.Nlink < 2 {
		return ownerParentIdentity{}, application.NewError("repository-owner-lock", application.ReasonOwnerUnavailable)
	}
	return ownerParentIdentity{Path: path, Device: uint64(stat.Dev), Inode: stat.Ino, Mode: uint32(stat.Mode), UID: stat.Uid, GID: stat.Gid}, nil
}

func sameOwnerDirectoryObject(left, right ownerDirectoryIdentity) bool {
	return left.Path == right.Path && left.Device == right.Device && left.Inode == right.Inode && left.Mode == right.Mode && left.UID == right.UID && left.GID == right.GID
}

func namedOwnerDirectoryMatches(stat unix.Stat_t, identity ownerDirectoryIdentity) bool {
	return uint64(stat.Dev) == identity.Device && stat.Ino == identity.Inode && uint32(stat.Mode) == identity.Mode && stat.Uid == identity.UID && stat.Gid == identity.GID && stat.Mode&unix.S_IFMT == unix.S_IFDIR && stat.Mode&0o777 == 0o700 && stat.Nlink >= 2 && mutationIdentity(stat) == identity.Mutation
}

func validateOwnerDirectory(parentFD, heldFD int, name string, parentIdentity ownerParentIdentity, identity ownerDirectoryIdentity) error {
	parent, err := observeOwnerParent(parentFD)
	held, heldErr := observeOwnerDirectory(heldFD)
	var named unix.Stat_t
	if err != nil || heldErr != nil || parent != parentIdentity || held != identity || filepath.Base(identity.Path) != name || filepath.Join(parentIdentity.Path, name) != identity.Path || unix.Fstatat(parentFD, name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || !namedOwnerDirectoryMatches(named, identity) {
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
	return lock.physical.lockIdentity.Object
}

func (lock *darwinRepositoryOwnerScopeLock) acquireAndBind(ctx context.Context, store *resultingress.DurableStore, candidate resultingress.ControlOwnerAcquisition) (repositoryOwnerLock, resultingress.ControlOwnerState, resultingress.ControlOwnerAcquisition, error) {
	if lock == nil || ctx == nil || store == nil || validateCompositionAcquisitionCandidate(candidate) != nil || candidate.Scope != lock.ownerScope || candidate.OwnerUID != uint32(os.Getuid()) || candidate.OwnerGID != uint32(os.Getgid()) || candidate.OwnerProcess.PID != os.Getpid() {
		return nil, resultingress.ControlOwnerState{}, resultingress.ControlOwnerAcquisition{}, application.NewError("repository-owner-lock", application.ReasonInvalidRequest)
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.closed || lock.physical == nil || lock.transitionUsed {
		return nil, resultingress.ControlOwnerState{}, resultingress.ControlOwnerAcquisition{}, application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	}
	// The transition is creation-once even on a response-loss or replay
	// failure. A caller must close Phase A and let a fresh owner observe the
	// durable ledger; retrying here could mint a sibling acquisition.
	lock.transitionUsed = true
	physical := lock.physical
	callbackEntered := false
	var bound repositoryOwnerLock
	var replayed resultingress.ControlOwnerState
	err := physical.withHeld(ctx, false, func() error {
		callbackEntered = true
		prior, found, replayErr := store.OpenOwner(candidate.Scope)
		if replayErr != nil {
			return ownerReplayFailure(replayErr)
		}
		expectedEpoch, expectedFactDigest := uint64(0), ""
		if found {
			expectedEpoch, expectedFactDigest = prior.Acquisition.OwnerEpoch, prior.FactDigest
		}
		nextEpoch := expectedEpoch + 1
		if candidate.OwnerEpoch != 0 && candidate.OwnerEpoch != nextEpoch {
			return newRepositoryOwnerTransitionError(repositoryOwnerFailureReplayConflict)
		}
		candidate.OwnerEpoch = nextEpoch

		// The provisional verifier never leaves this stack frame and can
		// authorize exactly this direct AcquireOwner call for exactly this
		// candidate. The physical owner lock is already outermost, so ledger
		// acquisition cannot invert owner→ledger lock ordering.
		verifier := &darwinProvisionalOwnerVerifier{candidate: candidate}
		result, acquireErr := store.AcquireOwner(ctx, verifier, expectedEpoch, expectedFactDigest, candidate)
		if acquireErr != nil {
			if ctx.Err() != nil {
				return application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
			}
			return ownerReplayFailure(acquireErr)
		}
		if !result.Appended || result.State.Acquisition != candidate || result.State.PreviousFactDigest != expectedFactDigest || !validCurrentOwnerReplay(result.State, candidate) {
			return newRepositoryOwnerTransitionError(repositoryOwnerFailureReplayConflict)
		}
		replayed, found, replayErr = store.OpenOwner(lock.ownerScope)
		if replayErr != nil {
			return ownerReplayFailure(replayErr)
		}
		if !found || replayed != result.State || replayed.PreviousFactDigest != expectedFactDigest || !validCurrentOwnerReplay(replayed, candidate) {
			return newRepositoryOwnerTransitionError(repositoryOwnerFailureReplayConflict)
		}
		// An external pathname actor is not serialized by physical.mu. Recheck
		// once more after ledger replay and before transferring Phase A into the
		// acquisition-bound verifier.
		if err := physical.revalidateLocked(); err != nil {
			return newRepositoryOwnerTransitionError(repositoryOwnerFailureOwnerIdentityDrift)
		}
		lock.physical = nil
		bound = &darwinRepositoryOwnerLock{physical: physical, ownerAcquisition: candidate}
		return nil
	})
	if err != nil {
		if !callbackEntered {
			if ctx.Err() != nil {
				err = application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
			} else {
				err = newRepositoryOwnerTransitionError(repositoryOwnerFailureOwnerIdentityDrift)
			}
		} else if _, typed := repositoryOwnerTransitionKind(err); !typed && ctx.Err() == nil {
			err = newRepositoryOwnerTransitionError(repositoryOwnerFailureIngressIdentityIO)
		}
		return nil, resultingress.ControlOwnerState{}, resultingress.ControlOwnerAcquisition{}, err
	}
	return bound, replayed, candidate, nil
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
	if physical == nil || physical.closed || physical.parent == nil || physical.file == nil || physical.directory == nil {
		return application.NewError("repository-owner-lock", application.ReasonOwnerNotCurrent)
	}
	if err := validateOwnerDirectory(int(physical.parent.Fd()), int(physical.directory.Fd()), physical.directoryName, physical.parentID, physical.directoryID); err != nil {
		return err
	}
	identity, err := observeOwnerFile(int(physical.directory.Fd()), int(physical.file.Fd()), physical.name)
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
	if physical.parent != nil {
		if physical.parent.Close() != nil {
			failed = true
		}
		physical.parent = nil
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
	return lock.physical.lockIdentity.Object
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
