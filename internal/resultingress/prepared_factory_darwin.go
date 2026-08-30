//go:build darwin && arm64

package resultingress

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
	"golang.org/x/sys/unix"
)

// OpenDarwinResultIngressStore binds the RB1 ledger and its coordination file
// to one already-held directory. It performs no Core observation and exposes
// no prepared-execution mechanics, so ADR 0066 Phase B can OpenOwner first.
func OpenDarwinResultIngressStore(ledgerDir *os.File) (*DurableStore, error) {
	files, err := openHeldDarwinAuthorityFiles(ledgerDir)
	if err != nil {
		return nil, err
	}
	return &DurableStore{dir: files.directoryID.CanonicalPath, nextSequence: 1, clock: time.Now, heldFiles: files}, nil
}

// SealPi0844DarwinPreparedExecutionStore is the sole S1 fresh-start
// composition. It consumes the same descriptor-backed store in place while an
// exact current-owner verifier is held, observes the current fixed Marshal
// image inside that authority window, and retains the owner-private control
// root. No second writable view, mechanics callback or caller-supplied Core
// observation is accepted.
func SealPi0844DarwinPreparedExecutionStore(ctx context.Context, store *DurableStore, verifier CurrentOwnerLockVerifier, binding CurrentOwnerBinding, fixedMarshalPath string, ownerPrivateControlRoot *os.File) (*DurableStore, error) {
	if ctx == nil || store == nil || store.heldFiles == nil || store.closed.Load() || binding.Validate() != nil {
		return nil, ErrPreparedExecutionUnavailable
	}
	if ownerPrivateControlRoot == nil || !filepath.IsAbs(fixedMarshalPath) || filepath.Clean(fixedMarshalPath) != fixedMarshalPath {
		return nil, ErrPreparedExecutionUnavailable
	}
	if err := store.WithCurrentOwner(ctx, verifier, binding, func(owner ControlOwnerState) error {
		core, err := processsupervisor.ObserveCurrentCore(fixedMarshalPath)
		if err != nil || owner.Acquisition.OwnerUID != core.UID || owner.Acquisition.OwnerGID != core.GID || owner.Acquisition.OwnerProcess != core.Process || owner.Acquisition.OwnerBinary != core.Binary {
			return ErrPreparedExecutionUnavailable
		}
		rootIdentity, err := processsupervisor.ObserveHeldControlDirectory(ownerPrivateControlRoot)
		if err != nil || !filepath.IsAbs(rootIdentity.CanonicalPath) || filepath.Clean(rootIdentity.CanonicalPath) != rootIdentity.CanonicalPath || rootIdentity.UID != uint32(os.Geteuid()) || rootIdentity.Mode&0o777 != 0o700 {
			return ErrPreparedExecutionUnavailable
		}
		fd, err := unix.Dup(int(ownerPrivateControlRoot.Fd()))
		if err != nil {
			return ErrPreparedExecutionUnavailable
		}
		unix.CloseOnExec(fd)
		retained := os.NewFile(uintptr(fd), "marshal-prepared-control-root")
		if retained == nil {
			_ = unix.Close(fd)
			return ErrPreparedExecutionUnavailable
		}
		retainedIdentity, err := processsupervisor.ObserveHeldControlDirectory(retained)
		if err != nil || retainedIdentity != rootIdentity {
			_ = retained.Close()
			return ErrPreparedExecutionUnavailable
		}
		store.mu.Lock()
		defer store.mu.Unlock()
		if store.closed.Load() || store.preparedDarwin != nil {
			_ = retained.Close()
			return ErrPreparedExecutionUnavailable
		}
		store.preparedDarwin = &preparedDarwinExecutionProfile{fixedMarshalPath: fixedMarshalPath, core: core, controlRoot: retained, controlIdentity: rootIdentity}
		return nil
	}); err != nil {
		return nil, err
	}
	return store, nil
}
