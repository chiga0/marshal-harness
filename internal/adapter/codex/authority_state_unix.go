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
	"reflect"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

const (
	consumerStateName      = "state.json"
	consumerBootstrapName  = "bootstrap.json"
	consumerLockName       = "state.lock"
	consumerStateLimit     = 24 << 10
	consumerBootstrapLimit = 8 << 10
)

type directoryIdentity struct {
	device uint64
	inode  uint64
}

// CodexBootstrapRootIdentityV1 is deployment-owned trust input. It is passed
// across the store construction boundary rather than derived from authority
// payloads that an attacker could replace together.
type CodexBootstrapRootIdentityV1 struct {
	AuthorityNamespace  string
	RootKeyID           string
	RootAlgorithm       string
	RootPublicKey       string
	RootPublicKeyDigest string
	TrustRootGeneration uint64
}

func (root CodexBootstrapRootIdentityV1) validate() error {
	publicKey, err := decodeEd25519Public(root.RootPublicKey)
	if !validID(root.AuthorityNamespace) || !validID(root.RootKeyID) || root.RootAlgorithm != "Ed25519" || err != nil || canonical.DigestBytes(publicKey) != root.RootPublicKeyDigest || !validGeneration(root.TrustRootGeneration) {
		return errors.New("codex deployment bootstrap root identity is invalid")
	}
	return nil
}

func (root CodexBootstrapRootIdentityV1) matches(pin CodexActiveRootPinV1) bool {
	return root.AuthorityNamespace == pin.AuthorityNamespace &&
		root.RootKeyID == pin.RootKeyID &&
		root.RootAlgorithm == pin.RootAlgorithm &&
		root.RootPublicKey == pin.RootPublicKey &&
		root.RootPublicKeyDigest == pin.RootPublicKeyDigest &&
		root.TrustRootGeneration == pin.TrustRootGeneration
}

type CodexConsumerAuthorityStore struct {
	stateRoot     *os.File
	authorityRoot *os.File
	bootstrapRoot CodexBootstrapRootIdentityV1
}

// OpenCodexConsumerAuthorityStore pins both roots through nofollow dirfds and
// rejects aliases and either direction of nesting before any state is read.
func OpenCodexConsumerAuthorityStore(stateRoot, authorityRoot string, bootstrapRoot CodexBootstrapRootIdentityV1) (*CodexConsumerAuthorityStore, error) {
	if err := bootstrapRoot.validate(); err != nil {
		return nil, newAuthorityFailure("constructor", "codex_trust_root_invalid", "Codex deployment bootstrap root identity is invalid", AuthorityFailureDetails{}, err, authorityNow())
	}
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
	return &CodexConsumerAuthorityStore{stateRoot: state, authorityRoot: authority, bootstrapRoot: bootstrapRoot}, nil
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
	bootstrap, bootstrapExists, bootstrapErr := readConsumerBootstrap(int(store.stateRoot.Fd()))
	if bootstrapErr != nil || !bootstrapExists {
		return CodexConsumerAuthorityStateV1{}, false, newAuthorityFailure("constructor", "codex_fence_invalid", "Codex consumer bootstrap is unavailable", AuthorityFailureDetails{}, bootstrapErr, authorityNow())
	}
	bootstrapDigest, _ := BootstrapDigest(bootstrap)
	if bootstrapDigest != state.Fence.BootstrapDigest || bootstrap.AuthorityNamespace != state.Fence.AuthorityNamespace || bootstrap.BootstrapID != state.Fence.BootstrapID || bootstrap.HostIdentityDigest != state.Fence.HostIdentityDigest {
		return CodexConsumerAuthorityStateV1{}, false, newAuthorityFailure("constructor", "codex_fence_invalid", "Codex consumer bootstrap differs from committed state", AuthorityFailureDetails{}, nil, authorityNow())
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

// CommitBootstrap establishes the first trust state from a durable consumer
// bootstrap and an initial keyset signed by the explicitly pinned root. A
// config cannot self-select the initial root.
func (store *CodexConsumerAuthorityStore) CommitBootstrap(bootstrap CodexConsumerBootstrapV1, hostIdentity LinuxHostIdentityV1, next CodexConsumerAuthorityStateV1, initialKeyset SignedEnvelopeV1, now time.Time) error {
	if store == nil || store.stateRoot == nil {
		return newAuthorityFailure("constructor", "codex_fence_invalid", "Codex consumer authority store is unavailable", AuthorityFailureDetails{}, nil, authorityNow())
	}
	lock, err := lockConsumerState(int(store.stateRoot.Fd()))
	if err != nil {
		return err
	}
	defer unlockConsumerState(lock)
	if _, exists, err := readConsumerState(int(store.stateRoot.Fd())); err != nil || exists {
		return newAuthorityFailure("constructor", "codex_trust_root_invalid", "Codex consumer bootstrap may only be committed once", AuthorityFailureDetails{}, err, authorityNow())
	}
	committedBootstrap, bootstrapExists, bootstrapErr := readConsumerBootstrap(int(store.stateRoot.Fd()))
	if bootstrapErr != nil || bootstrapExists && committedBootstrap != bootstrap {
		return newAuthorityFailure("constructor", "codex_trust_root_invalid", "Codex consumer bootstrap identity already differs", AuthorityFailureDetails{}, bootstrapErr, authorityNow())
	}
	bootstrapDigest, err := BootstrapDigest(bootstrap)
	hostErr := ValidateBootstrapHostIdentity(bootstrap, hostIdentity)
	stateErr := next.Validate()
	if err == nil {
		err = hostErr
	}
	if err == nil {
		err = stateErr
	}
	if err != nil || bootstrapDigest != next.Fence.BootstrapDigest || bootstrap.AuthorityNamespace != next.Fence.AuthorityNamespace || bootstrap.BootstrapID != next.Fence.BootstrapID || bootstrap.HostIdentityDigest != next.Fence.HostIdentityDigest {
		return newAuthorityFailure("constructor", "codex_trust_root_invalid", "Codex consumer bootstrap binding is invalid", AuthorityFailureDetails{}, err, authorityNow())
	}
	if !store.bootstrapRoot.matches(next.ActiveRootPin) {
		return newAuthorityFailure("constructor", "codex_trust_root_invalid", "Codex initial root differs from the deployment bootstrap root", AuthorityFailureDetails{}, nil, authorityNow())
	}
	if err := VerifyInitialKeyset(next.ActiveRootPin, initialKeyset, now); err != nil {
		return newAuthorityFailure("constructor", "codex_trust_root_invalid", "Codex initial root proof is invalid", AuthorityFailureDetails{}, err, authorityNow())
	}
	if !bootstrapExists {
		if err := writeConsumerBootstrap(int(store.stateRoot.Fd()), bootstrap); err != nil {
			return err
		}
	}
	return writeConsumerState(int(store.stateRoot.Fd()), next, nil)
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
	if !exists {
		return newAuthorityFailure("constructor", "codex_trust_root_invalid", "Codex initial root requires consumer bootstrap proof", AuthorityFailureDetails{}, nil, authorityNow())
	}
	bootstrap, bootstrapExists, bootstrapErr := readConsumerBootstrap(int(store.stateRoot.Fd()))
	bootstrapDigest := ""
	if bootstrapExists && bootstrapErr == nil {
		bootstrapDigest, bootstrapErr = BootstrapDigest(bootstrap)
	}
	if bootstrapErr != nil || !bootstrapExists || bootstrapDigest != current.Fence.BootstrapDigest || bootstrapDigest != next.Fence.BootstrapDigest || bootstrap.BootstrapID != current.Fence.BootstrapID || bootstrap.HostIdentityDigest != current.Fence.HostIdentityDigest {
		return newAuthorityFailure("constructor", "codex_fence_invalid", "Codex consumer bootstrap binding is unavailable", AuthorityFailureDetails{}, bootstrapErr, authorityNow())
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
	if stateAuthorityIdentityEqual(current, next) {
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

func readConsumerBootstrap(directoryFD int) (CodexConsumerBootstrapV1, bool, error) {
	fd, err := unix.Openat(directoryFD, consumerBootstrapName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return CodexConsumerBootstrapV1{}, false, nil
	}
	if err != nil {
		return CodexConsumerBootstrapV1{}, false, newAuthorityFailure("constructor", "codex_fence_invalid", "Codex consumer bootstrap is unavailable", AuthorityFailureDetails{}, err, authorityNow())
	}
	file := os.NewFile(uintptr(fd), consumerBootstrapName)
	defer file.Close()
	if err := validatePrivateRegularFD(fd); err != nil {
		return CodexConsumerBootstrapV1{}, false, newAuthorityFailure("constructor", "codex_fence_invalid", "Codex consumer bootstrap is unsafe", AuthorityFailureDetails{}, err, authorityNow())
	}
	data, err := io.ReadAll(io.LimitReader(file, consumerBootstrapLimit+1))
	if err != nil || len(data) > consumerBootstrapLimit {
		return CodexConsumerBootstrapV1{}, false, newAuthorityFailure("constructor", "codex_fence_invalid", "Codex consumer bootstrap is unreadable", AuthorityFailureDetails{}, err, authorityNow())
	}
	data = bytes.TrimSuffix(data, []byte{'\n'})
	var bootstrap CodexConsumerBootstrapV1
	if err := decodeClosed(data, consumerBootstrapLimit, &bootstrap); err != nil || bootstrap.Validate() != nil {
		return CodexConsumerBootstrapV1{}, false, newAuthorityFailure("constructor", "codex_fence_invalid", "Codex consumer bootstrap is invalid", AuthorityFailureDetails{}, err, authorityNow())
	}
	return bootstrap, true, nil
}

func writeConsumerBootstrap(directoryFD int, bootstrap CodexConsumerBootstrapV1) error {
	if err := bootstrap.Validate(); err != nil {
		return newAuthorityFailure("constructor", "codex_fence_invalid", "Codex consumer bootstrap is invalid", AuthorityFailureDetails{}, err, authorityNow())
	}
	raw, err := json.Marshal(bootstrap)
	if err != nil {
		return err
	}
	data, err := canonical.JSON(raw)
	if err != nil || len(data)+1 > consumerBootstrapLimit {
		return newAuthorityFailure("constructor", "codex_fence_invalid", "Codex consumer bootstrap is invalid", AuthorityFailureDetails{}, err, authorityNow())
	}
	data = append(data, '\n')
	token := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, token); err != nil {
		return newAuthorityFailure("constructor", "codex_fence_commit_failed", "Codex consumer bootstrap transaction id is unavailable", AuthorityFailureDetails{}, err, authorityNow())
	}
	temporary := "bootstrap." + hex.EncodeToString(token) + ".tmp"
	fd, err := unix.Openat(directoryFD, temporary, unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		return newAuthorityFailure("constructor", "codex_fence_commit_failed", "Codex consumer bootstrap could not be staged", AuthorityFailureDetails{}, err, authorityNow())
	}
	file := os.NewFile(uintptr(fd), temporary)
	defer func() { _ = file.Close(); _ = unix.Unlinkat(directoryFD, temporary, 0) }()
	if _, err := file.Write(data); err != nil {
		return newAuthorityFailure("constructor", "codex_fence_commit_failed", "Codex consumer bootstrap could not be written", AuthorityFailureDetails{}, err, authorityNow())
	}
	if err := file.Sync(); err != nil {
		return newAuthorityFailure("constructor", "codex_fence_commit_failed", "Codex consumer bootstrap could not be synchronized", AuthorityFailureDetails{}, err, authorityNow())
	}
	if err := file.Close(); err != nil {
		return newAuthorityFailure("constructor", "codex_fence_commit_failed", "Codex consumer bootstrap could not be closed", AuthorityFailureDetails{}, err, authorityNow())
	}
	if err := unix.Renameat(directoryFD, temporary, directoryFD, consumerBootstrapName); err != nil {
		return newAuthorityFailure("constructor", "codex_fence_commit_failed", "Codex consumer bootstrap could not be committed", AuthorityFailureDetails{}, err, authorityNow())
	}
	if err := unix.Fsync(directoryFD); err != nil {
		return newAuthorityFailure("constructor", "codex_fence_commit_failed", "Codex consumer bootstrap directory could not be synchronized", AuthorityFailureDetails{}, err, authorityNow())
	}
	return nil
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

type heldLaunchPath struct {
	role          string
	path          string
	file          *os.File
	identity      MountObjectIdentityV1
	ancestorChain []MountObjectIdentityV1
}

type HeldLaunchRoots struct {
	mountNamespaceDevice uint64
	mountNamespaceInode  uint64
	paths                [6]heldLaunchPath
}

func OpenHeldLaunchRoots(authorityRoot, fenceRoot, worktree, controlRoot string) (*HeldLaunchRoots, error) {
	rootPaths := []string{authorityRoot, fenceRoot, worktree, controlRoot}
	roles := fixedRootRoles[:4]
	roots := &HeldLaunchRoots{}
	fail := func(code string, err error) (*HeldLaunchRoots, error) {
		_ = roots.Close()
		return nil, newAuthorityFailure("launch", code, "Codex held launch root topology is invalid", AuthorityFailureDetails{}, err, authorityNow())
	}
	for index, path := range rootPaths {
		file, _, err := openNoSymlinkDirectory(path)
		if err != nil {
			return fail("codex_path_invalid", err)
		}
		roots.paths[index] = heldLaunchPath{role: roles[index], path: path, file: file}
	}
	for index, name := range []string{"input", "output"} {
		fd, err := unix.Openat(int(roots.paths[3].file.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return fail("codex_path_invalid", err)
		}
		roots.paths[index+4] = heldLaunchPath{role: fixedRootRoles[index+4], path: filepath.Join(controlRoot, name), file: os.NewFile(uintptr(fd), name)}
	}
	for left := 0; left < 4; left++ {
		for right := left + 1; right < 4; right++ {
			overlap, err := heldDirectoriesOverlap(int(roots.paths[left].file.Fd()), int(roots.paths[right].file.Fd()))
			if err != nil || overlap {
				return fail("codex_path_topology_conflict", err)
			}
		}
	}
	inputOutputOverlap, err := heldDirectoriesOverlap(int(roots.paths[4].file.Fd()), int(roots.paths[5].file.Fd()))
	if err != nil || inputOutputOverlap {
		return fail("codex_path_topology_conflict", err)
	}
	namespaceDevice, namespaceInode, err := heldMountNamespaceIdentity()
	if err != nil {
		return fail("codex_mount_identity_unsupported", err)
	}
	roots.mountNamespaceDevice, roots.mountNamespaceInode = namespaceDevice, namespaceInode
	for index := range roots.paths {
		identity, err := mountObjectIdentityForFD(int(roots.paths[index].file.Fd()), roots.paths[index].role, nil)
		if err != nil {
			return fail("codex_mount_identity_unsupported", err)
		}
		chain, err := mountAncestorChain(int(roots.paths[index].file.Fd()), roots.paths[index].role+"Ancestor")
		if err != nil {
			return fail("codex_mount_identity_unsupported", err)
		}
		roots.paths[index].identity, roots.paths[index].ancestorChain = identity, chain
	}
	if roots.paths[4].identity.Mode&0o222 != 0 {
		return fail("codex_path_permission_invalid", errors.New("control input is writable"))
	}
	return roots, nil
}

func mountAncestorChain(directoryFD int, role string) ([]MountObjectIdentityV1, error) {
	current, err := unix.Openat(directoryFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(current) }()
	var result []MountObjectIdentityV1
	for len(result) < 256 {
		identity, err := mountObjectIdentityForFD(current, role, nil)
		if err != nil {
			return nil, err
		}
		result = append(result, identity)
		parent, err := unix.Openat(current, "..", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return nil, err
		}
		parentIdentity, err := mountObjectIdentityForFD(parent, role, nil)
		if err != nil {
			_ = unix.Close(parent)
			return nil, err
		}
		if identity.DeviceMajor == parentIdentity.DeviceMajor && identity.DeviceMinor == parentIdentity.DeviceMinor && identity.Inode == parentIdentity.Inode && identity.MountIDUnique == parentIdentity.MountIDUnique {
			_ = unix.Close(parent)
			return result, nil
		}
		_ = unix.Close(current)
		current = parent
	}
	return nil, errors.New("codex mount ancestor chain exceeds limit")
}

func (roots *HeldLaunchRoots) Snapshot(phase string, executables []TopologyObjectV1) (TopologySnapshotV1, error) {
	if roots == nil {
		return TopologySnapshotV1{}, errors.New("codex held launch roots are unavailable")
	}
	if err := roots.Verify(); err != nil {
		return TopologySnapshotV1{}, err
	}
	fixed := make([]TopologyObjectV1, len(roots.paths))
	for index := range roots.paths {
		fixed[index] = TopologyObjectV1{Identity: roots.paths[index].identity, AncestorChain: append([]MountObjectIdentityV1(nil), roots.paths[index].ancestorChain...)}
	}
	snapshot := TopologySnapshotV1{SchemaVersion: "marshal.codex.topology-snapshot.v1", MountNamespaceDevice: roots.mountNamespaceDevice, MountNamespaceInode: roots.mountNamespaceInode, Phase: phase, FixedRoots: fixed, Executables: executables}
	return snapshot, snapshot.Validate()
}

func (roots *HeldLaunchRoots) Verify() error {
	if roots == nil {
		return errors.New("codex held launch roots are unavailable")
	}
	namespaceDevice, namespaceInode, err := heldMountNamespaceIdentity()
	if err != nil || namespaceDevice != roots.mountNamespaceDevice || namespaceInode != roots.mountNamespaceInode {
		return errors.New("codex held mount namespace identity changed")
	}
	for index := range roots.paths {
		held := &roots.paths[index]
		identity, err := mountObjectIdentityForFD(int(held.file.Fd()), held.role, nil)
		if err != nil || identity != held.identity {
			return errors.New("codex held root identity changed")
		}
		chain, err := mountAncestorChain(int(held.file.Fd()), held.role+"Ancestor")
		if err != nil || !reflect.DeepEqual(chain, held.ancestorChain) {
			return errors.New("codex held root ancestry changed")
		}
		reopened, _, err := openNoSymlinkDirectory(held.path)
		if err != nil {
			return errors.New("codex held root pathname changed")
		}
		reopenedIdentity, reopenErr := mountObjectIdentityForFD(int(reopened.Fd()), held.role, nil)
		_ = reopened.Close()
		if reopenErr != nil || reopenedIdentity != held.identity {
			return errors.New("codex held root pathname identity changed")
		}
	}
	for left := 0; left < 4; left++ {
		for right := left + 1; right < 4; right++ {
			overlap, err := heldDirectoriesOverlap(int(roots.paths[left].file.Fd()), int(roots.paths[right].file.Fd()))
			if err != nil || overlap {
				return errors.New("codex held launch roots overlap")
			}
		}
	}
	if roots.paths[4].identity.Mode&0o222 != 0 {
		return errors.New("codex held control input became writable")
	}
	return nil
}

func (roots *HeldLaunchRoots) Close() error {
	if roots == nil {
		return nil
	}
	var result error
	for index := range roots.paths {
		if roots.paths[index].file != nil {
			if err := roots.paths[index].file.Close(); result == nil {
				result = err
			}
		}
	}
	return result
}

var authorityNow = func() time.Time { return time.Now().UTC() }
