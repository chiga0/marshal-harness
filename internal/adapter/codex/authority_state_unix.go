//go:build linux || darwin

package codex

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

const (
	consumerStateName  = "state.json"
	consumerLockName   = "state.lock"
	consumerStateLimit = 24 << 10
)

type directoryIdentity struct {
	device uint64
	inode  uint64
}

type CodexConsumerAuthorityStore struct {
	stateRoot     *os.File
	authorityRoot *os.File
}

// OpenCodexConsumerAuthorityStore pins both roots through nofollow dirfds and
// rejects aliases and either direction of nesting before any state is read.
func OpenCodexConsumerAuthorityStore(stateRoot, authorityRoot string) (*CodexConsumerAuthorityStore, error) {
	state, err := openPrivateDirectory(stateRoot)
	if err != nil {
		return nil, newAuthorityFailure("constructor", "codex_path_permission_invalid", "Codex consumer state root is not a private real directory", AuthorityFailureDetails{}, err, authorityNow())
	}
	authority, err := openPrivateDirectory(authorityRoot)
	if err != nil {
		state.Close()
		return nil, newAuthorityFailure("constructor", "codex_path_permission_invalid", "Codex authority root is not a private real directory", AuthorityFailureDetails{}, err, authorityNow())
	}
	overlaps, err := heldDirectoriesOverlap(int(state.Fd()), int(authority.Fd()))
	if err != nil || overlaps {
		state.Close()
		authority.Close()
		return nil, newAuthorityFailure("constructor", "codex_path_topology_conflict", "Codex authority and consumer state roots overlap", AuthorityFailureDetails{}, err, authorityNow())
	}
	return &CodexConsumerAuthorityStore{stateRoot: state, authorityRoot: authority}, nil
}

func (store *CodexConsumerAuthorityStore) Close() error {
	if store == nil {
		return nil
	}
	var result error
	if store.stateRoot != nil {
		result = store.stateRoot.Close()
	}
	if store.authorityRoot != nil {
		if err := store.authorityRoot.Close(); result == nil {
			result = err
		}
	}
	return result
}

// Recover re-reads the committed state, invokes current-authority validation,
// and fsyncs the state directory before returning eligibility. Calling this on
// every startup safely completes the rename-before-directory-fsync crash case.
func (store *CodexConsumerAuthorityStore) Recover(validateCurrentAuthority func(CodexConsumerAuthorityStateV1) error) (CodexConsumerAuthorityStateV1, bool, error) {
	if store == nil || store.stateRoot == nil || validateCurrentAuthority == nil {
		return CodexConsumerAuthorityStateV1{}, false, newAuthorityFailure("constructor", "codex_fence_invalid", "Codex consumer authority recovery is unavailable", AuthorityFailureDetails{}, nil, authorityNow())
	}
	lock, err := lockConsumerState(int(store.stateRoot.Fd()))
	if err != nil {
		return CodexConsumerAuthorityStateV1{}, false, err
	}
	defer unlockConsumerState(lock)
	state, exists, err := readConsumerState(int(store.stateRoot.Fd()))
	if err != nil || !exists {
		return state, exists, err
	}
	if err := validateCurrentAuthority(state); err != nil {
		return CodexConsumerAuthorityStateV1{}, false, newAuthorityFailure("constructor", "codex_fence_invalid", "Codex committed authority state cannot be recovered", AuthorityFailureDetails{AuthorityGeneration: state.Fence.AuthorityGeneration, TrustRootGeneration: state.Fence.TrustRootGeneration}, err, authorityNow())
	}
	if err := unix.Fsync(int(store.stateRoot.Fd())); err != nil {
		return CodexConsumerAuthorityStateV1{}, false, newAuthorityFailure("constructor", "codex_fence_commit_failed", "Codex consumer authority recovery could not be made durable", AuthorityFailureDetails{}, err, authorityNow())
	}
	return state, true, nil
}

func (store *CodexConsumerAuthorityStore) Commit(next CodexConsumerAuthorityStateV1) error {
	return store.commit(next, nil, nil, time.Time{})
}

// CommitRootRotation verifies old-root authorization and new-root possession
// while holding the same state lock used by the atomic pin+fence commit.
func (store *CodexConsumerAuthorityStore) CommitRootRotation(next CodexConsumerAuthorityStateV1, keyset SignedEnvelopeV1, now time.Time) error {
	return store.commit(next, nil, &keyset, now)
}

// CommitKeysetAdvance verifies a same-root keyset chain before atomically
// binding its digest into the active pin and generation fence.
func (store *CodexConsumerAuthorityStore) CommitKeysetAdvance(next CodexConsumerAuthorityStateV1, keyset SignedEnvelopeV1, now time.Time) error {
	return store.commit(next, nil, &keyset, now)
}

func (store *CodexConsumerAuthorityStore) commit(next CodexConsumerAuthorityStateV1, hook func(string) error, rotation *SignedEnvelopeV1, rotationTime time.Time) error {
	if store == nil || store.stateRoot == nil {
		return newAuthorityFailure("constructor", "codex_fence_invalid", "Codex consumer authority store is unavailable", AuthorityFailureDetails{}, nil, authorityNow())
	}
	lock, err := lockConsumerState(int(store.stateRoot.Fd()))
	if err != nil {
		return err
	}
	defer unlockConsumerState(lock)
	current, exists, err := readConsumerState(int(store.stateRoot.Fd()))
	if err != nil {
		return err
	}
	var currentPointer *CodexConsumerAuthorityStateV1
	if exists {
		currentPointer = &current
	}
	if err := ValidateStateAdvance(currentPointer, next); err != nil {
		code := "codex_authority_identity_conflict"
		if strings.Contains(err.Error(), "rollback") {
			code = "codex_authority_rollback"
		}
		return newAuthorityFailure("constructor", code, "Codex authority state advance was rejected", AuthorityFailureDetails{AuthorityGeneration: next.Fence.AuthorityGeneration, TrustRootGeneration: next.Fence.TrustRootGeneration}, err, authorityNow())
	}
	keysetChanged := exists && next.Fence.KeysetDigest != current.Fence.KeysetDigest
	if exists && next.Fence.TrustRootGeneration > current.Fence.TrustRootGeneration {
		if rotation == nil {
			return newAuthorityFailure("constructor", "codex_trust_root_invalid", "Codex root rotation proof is required", AuthorityFailureDetails{AuthorityGeneration: next.Fence.AuthorityGeneration, TrustRootGeneration: next.Fence.TrustRootGeneration}, nil, authorityNow())
		}
		verifiedPin, verifyErr := VerifyRootRotation(current.ActiveRootPin, *rotation, rotationTime)
		if verifyErr != nil || verifiedPin != next.ActiveRootPin {
			return newAuthorityFailure("constructor", "codex_trust_root_invalid", "Codex root rotation proof is invalid", AuthorityFailureDetails{AuthorityGeneration: next.Fence.AuthorityGeneration, TrustRootGeneration: next.Fence.TrustRootGeneration}, verifyErr, authorityNow())
		}
	} else if keysetChanged {
		if rotation == nil {
			return newAuthorityFailure("constructor", "codex_trust_root_invalid", "Codex keyset advance proof is required", AuthorityFailureDetails{AuthorityGeneration: next.Fence.AuthorityGeneration, TrustRootGeneration: next.Fence.TrustRootGeneration}, nil, authorityNow())
		}
		verifiedPin, verifyErr := VerifyKeysetAdvance(current.ActiveRootPin, *rotation, rotationTime)
		if verifyErr != nil || verifiedPin != next.ActiveRootPin {
			return newAuthorityFailure("constructor", "codex_trust_root_invalid", "Codex keyset advance proof is invalid", AuthorityFailureDetails{AuthorityGeneration: next.Fence.AuthorityGeneration, TrustRootGeneration: next.Fence.TrustRootGeneration}, verifyErr, authorityNow())
		}
	} else if rotation != nil {
		return newAuthorityFailure("constructor", "codex_trust_root_invalid", "Codex root rotation proof is unexpected", AuthorityFailureDetails{}, nil, authorityNow())
	}
	if exists && stateAuthorityIdentityEqual(current, next) {
		return nil
	}
	return writeConsumerState(int(store.stateRoot.Fd()), next, hook)
}

func stateAuthorityIdentityEqual(left, right CodexConsumerAuthorityStateV1) bool {
	return left.ActiveRootPin == right.ActiveRootPin && left.Fence.AuthorityNamespace == right.Fence.AuthorityNamespace &&
		left.Fence.AdapterID == right.Fence.AdapterID && left.Fence.BootstrapDigest == right.Fence.BootstrapDigest &&
		left.Fence.HostIdentityDigest == right.Fence.HostIdentityDigest && left.Fence.BootstrapID == right.Fence.BootstrapID &&
		left.Fence.TrustRootGeneration == right.Fence.TrustRootGeneration && left.Fence.AuthorityGeneration == right.Fence.AuthorityGeneration &&
		left.Fence.KeysetDigest == right.Fence.KeysetDigest && left.Fence.ConfigDigest == right.Fence.ConfigDigest &&
		left.Fence.RevocationSetDigest == right.Fence.RevocationSetDigest && left.Fence.CurrentEvidenceDigest == right.Fence.CurrentEvidenceDigest
}

func lockConsumerState(directoryFD int) (int, error) {
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	fd, err := unix.Openat(directoryFD, consumerLockName, flags, 0)
	if errors.Is(err, unix.ENOENT) {
		fd, err = unix.Openat(directoryFD, consumerLockName, flags|unix.O_CREAT|unix.O_EXCL, 0o600)
		if errors.Is(err, unix.EEXIST) {
			fd, err = unix.Openat(directoryFD, consumerLockName, flags, 0)
		}
	}
	if err != nil {
		return -1, newAuthorityFailure("constructor", "codex_fence_invalid", "Codex consumer authority lock is unavailable", AuthorityFailureDetails{}, err, authorityNow())
	}
	if err := validatePrivateRegularFD(fd); err != nil {
		unix.Close(fd)
		return -1, newAuthorityFailure("constructor", "codex_fence_invalid", "Codex consumer authority lock is unsafe", AuthorityFailureDetails{}, err, authorityNow())
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		unix.Close(fd)
		return -1, newAuthorityFailure("constructor", "codex_fence_lock_busy", "Codex consumer authority lock is busy", AuthorityFailureDetails{}, err, authorityNow())
	}
	return fd, nil
}

func unlockConsumerState(fd int) {
	if fd < 0 {
		return
	}
	_ = unix.Flock(fd, unix.LOCK_UN)
	_ = unix.Close(fd)
}

func readConsumerState(directoryFD int) (CodexConsumerAuthorityStateV1, bool, error) {
	fd, err := unix.Openat(directoryFD, consumerStateName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return CodexConsumerAuthorityStateV1{}, false, nil
	}
	if err != nil {
		return CodexConsumerAuthorityStateV1{}, false, newAuthorityFailure("constructor", "codex_fence_invalid", "Codex consumer authority state is unavailable", AuthorityFailureDetails{}, err, authorityNow())
	}
	file := os.NewFile(uintptr(fd), consumerStateName)
	defer file.Close()
	if err := validatePrivateRegularFD(fd); err != nil {
		return CodexConsumerAuthorityStateV1{}, false, newAuthorityFailure("constructor", "codex_fence_invalid", "Codex consumer authority state is unsafe", AuthorityFailureDetails{}, err, authorityNow())
	}
	data, err := io.ReadAll(io.LimitReader(file, consumerStateLimit+1))
	if err != nil || len(data) > consumerStateLimit {
		return CodexConsumerAuthorityStateV1{}, false, newAuthorityFailure("constructor", "codex_fence_invalid", "Codex consumer authority state is unreadable", AuthorityFailureDetails{}, err, authorityNow())
	}
	data = bytes.TrimSuffix(data, []byte{'\n'})
	var state CodexConsumerAuthorityStateV1
	if err := decodeClosed(data, consumerStateLimit, &state); err != nil || state.Validate() != nil {
		return CodexConsumerAuthorityStateV1{}, false, newAuthorityFailure("constructor", "codex_fence_invalid", "Codex consumer authority state is invalid", AuthorityFailureDetails{}, err, authorityNow())
	}
	return state, true, nil
}

func writeConsumerState(directoryFD int, state CodexConsumerAuthorityStateV1, hook func(string) error) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	data, err := canonical.JSON(raw)
	if err != nil || len(data)+1 > consumerStateLimit {
		return newAuthorityFailure("constructor", "codex_fence_invalid", "Codex consumer authority state is invalid", AuthorityFailureDetails{}, err, authorityNow())
	}
	data = append(data, '\n')
	token := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, token); err != nil {
		return newAuthorityFailure("constructor", "codex_fence_commit_failed", "Codex consumer authority transaction id is unavailable", AuthorityFailureDetails{}, err, authorityNow())
	}
	temporary := "state." + hex.EncodeToString(token) + ".tmp"
	fd, err := unix.Openat(directoryFD, temporary, unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		return newAuthorityFailure("constructor", "codex_fence_commit_failed", "Codex consumer authority state could not be staged", AuthorityFailureDetails{}, err, authorityNow())
	}
	file := os.NewFile(uintptr(fd), temporary)
	defer func() {
		_ = file.Close()
		_ = unix.Unlinkat(directoryFD, temporary, 0)
	}()
	if err := callStateHook(hook, "temp-created"); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return newAuthorityFailure("constructor", "codex_fence_commit_failed", "Codex consumer authority state could not be written", AuthorityFailureDetails{}, err, authorityNow())
	}
	if err := callStateHook(hook, "temp-written"); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return newAuthorityFailure("constructor", "codex_fence_commit_failed", "Codex consumer authority state could not be synchronized", AuthorityFailureDetails{}, err, authorityNow())
	}
	if err := callStateHook(hook, "temp-synced"); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return newAuthorityFailure("constructor", "codex_fence_commit_failed", "Codex consumer authority state could not be closed", AuthorityFailureDetails{}, err, authorityNow())
	}
	if err := unix.Renameat(directoryFD, temporary, directoryFD, consumerStateName); err != nil {
		return newAuthorityFailure("constructor", "codex_fence_commit_failed", "Codex consumer authority state could not be committed", AuthorityFailureDetails{}, err, authorityNow())
	}
	if err := callStateHook(hook, "state-renamed"); err != nil {
		return err
	}
	if err := unix.Fsync(directoryFD); err != nil {
		return newAuthorityFailure("constructor", "codex_fence_commit_failed", "Codex consumer authority directory could not be synchronized", AuthorityFailureDetails{}, err, authorityNow())
	}
	if err := callStateHook(hook, "directory-synced"); err != nil {
		return err
	}
	return nil
}

func callStateHook(hook func(string) error, phase string) error {
	if hook == nil {
		return nil
	}
	return hook(phase)
}

func validatePrivateRegularFD(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o7777 != 0o600 || stat.Nlink != 1 || (int(stat.Uid) != os.Geteuid() && stat.Uid != 0) {
		return errors.New("private authority file invariant failed")
	}
	return nil
}

func openPrivateDirectory(path string) (*os.File, error) {
	file, stat, err := openNoSymlinkDirectory(path)
	if err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o7777 != 0o700 || (int(stat.Uid) != os.Geteuid() && stat.Uid != 0) {
		file.Close()
		return nil, errors.New("private authority directory invariant failed")
	}
	return file, nil
}

func openNoSymlinkDirectory(path string) (*os.File, unix.Stat_t, error) {
	var zero unix.Stat_t
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return nil, zero, errors.New("authority directory path is invalid")
	}
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	current, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, zero, err
	}
	for _, part := range parts {
		next, openErr := unix.Openat(current, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(current)
		if openErr != nil {
			return nil, zero, openErr
		}
		current = next
	}
	var stat unix.Stat_t
	if err := unix.Fstat(current, &stat); err != nil {
		_ = unix.Close(current)
		return nil, zero, err
	}
	return os.NewFile(uintptr(current), filepath.Base(path)), stat, nil
}

func heldDirectoriesOverlap(leftFD, rightFD int) (bool, error) {
	left, err := statDirectory(leftFD)
	if err != nil {
		return false, err
	}
	right, err := statDirectory(rightFD)
	if err != nil {
		return false, err
	}
	contains, err := identityInAncestors(left, rightFD)
	if err != nil || contains {
		return contains, err
	}
	return identityInAncestors(right, leftFD)
}

func statDirectory(fd int) (directoryIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return directoryIdentity{}, errors.New("held root is not a directory")
	}
	return directoryIdentity{uint64(stat.Dev), uint64(stat.Ino)}, nil
}

func identityInAncestors(target directoryIdentity, descendantFD int) (bool, error) {
	current, err := unix.Openat(descendantFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return false, err
	}
	defer func() { _ = unix.Close(current) }()
	for {
		identity, err := statDirectory(current)
		if err != nil {
			return false, err
		}
		if identity == target {
			return true, nil
		}
		parent, err := unix.Openat(current, "..", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return false, err
		}
		parentIdentity, err := statDirectory(parent)
		if err != nil {
			_ = unix.Close(parent)
			return false, err
		}
		if parentIdentity == identity {
			_ = unix.Close(parent)
			return false, nil
		}
		_ = unix.Close(current)
		current = parent
	}
}

type HeldWorkRoots struct {
	worktree         *os.File
	controlRoot      *os.File
	worktreePath     string
	controlRootPath  string
	worktreeIdentity directoryIdentity
	controlIdentity  directoryIdentity
}

func OpenSeparatedWorkRoots(worktree, controlRoot string) (*HeldWorkRoots, error) {
	work, _, err := openNoSymlinkDirectory(worktree)
	if err != nil {
		return nil, newAuthorityFailure("launch", "codex_path_invalid", "Codex worktree root is invalid", AuthorityFailureDetails{}, err, authorityNow())
	}
	control, _, err := openNoSymlinkDirectory(controlRoot)
	if err != nil {
		work.Close()
		return nil, newAuthorityFailure("launch", "codex_path_invalid", "Codex control root is invalid", AuthorityFailureDetails{}, err, authorityNow())
	}
	overlap, err := heldDirectoriesOverlap(int(work.Fd()), int(control.Fd()))
	if err != nil || overlap {
		work.Close()
		control.Close()
		return nil, newAuthorityFailure("launch", "codex_path_topology_conflict", "Codex worktree and control roots overlap", AuthorityFailureDetails{}, err, authorityNow())
	}
	workIdentity, _ := statDirectory(int(work.Fd()))
	controlIdentity, _ := statDirectory(int(control.Fd()))
	return &HeldWorkRoots{worktree: work, controlRoot: control, worktreePath: worktree, controlRootPath: controlRoot, worktreeIdentity: workIdentity, controlIdentity: controlIdentity}, nil
}

func (roots *HeldWorkRoots) Verify() error {
	if roots == nil || roots.worktree == nil || roots.controlRoot == nil {
		return errors.New("codex held work roots are unavailable")
	}
	work, err := statDirectory(int(roots.worktree.Fd()))
	if err != nil || work != roots.worktreeIdentity {
		return errors.New("codex worktree held identity changed")
	}
	control, err := statDirectory(int(roots.controlRoot.Fd()))
	if err != nil || control != roots.controlIdentity {
		return errors.New("codex control root held identity changed")
	}
	overlap, err := heldDirectoriesOverlap(int(roots.worktree.Fd()), int(roots.controlRoot.Fd()))
	if err != nil || overlap {
		return errors.New("codex held work roots overlap")
	}
	currentWork, _, err := openNoSymlinkDirectory(roots.worktreePath)
	if err != nil {
		return errors.New("codex worktree path identity changed")
	}
	currentWorkIdentity, workErr := statDirectory(int(currentWork.Fd()))
	_ = currentWork.Close()
	if workErr != nil || currentWorkIdentity != roots.worktreeIdentity {
		return errors.New("codex worktree path identity changed")
	}
	currentControl, _, err := openNoSymlinkDirectory(roots.controlRootPath)
	if err != nil {
		return errors.New("codex control root path identity changed")
	}
	currentControlIdentity, controlErr := statDirectory(int(currentControl.Fd()))
	_ = currentControl.Close()
	if controlErr != nil || currentControlIdentity != roots.controlIdentity {
		return errors.New("codex control root path identity changed")
	}
	return nil
}

func (roots *HeldWorkRoots) Close() error {
	if roots == nil {
		return nil
	}
	var result error
	if roots.worktree != nil {
		result = roots.worktree.Close()
	}
	if roots.controlRoot != nil {
		if err := roots.controlRoot.Close(); result == nil {
			result = err
		}
	}
	return result
}

var authorityNow = func() time.Time { return time.Now().UTC() }
