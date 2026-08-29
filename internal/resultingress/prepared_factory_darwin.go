//go:build darwin && arm64

package resultingress

import (
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

// SealPi0843DarwinPreparedExecutionStore is the sole S1 fresh-start
// composition. The production factory calls it only after OpenOwner, then this
// constructor observes the current fixed Marshal image and retains the exact
// owner-private control root. It returns a new prepared-capable view over the
// same held ledger backend; no mechanics callback or caller-supplied Core
// observation is accepted.
func SealPi0843DarwinPreparedExecutionStore(store *DurableStore, fixedMarshalPath string, ownerPrivateControlRoot *os.File) (*DurableStore, error) {
	if store == nil || store.heldFiles == nil || store.preparedDarwin != nil {
		return nil, ErrPreparedExecutionUnavailable
	}
	if ownerPrivateControlRoot == nil || !filepath.IsAbs(fixedMarshalPath) || filepath.Clean(fixedMarshalPath) != fixedMarshalPath {
		return nil, ErrPreparedExecutionUnavailable
	}
	core, err := processsupervisor.ObserveCurrentCore(fixedMarshalPath)
	if err != nil {
		return nil, ErrPreparedExecutionUnavailable
	}
	rootIdentity, err := processsupervisor.ObserveHeldControlDirectory(ownerPrivateControlRoot)
	if err != nil || !filepath.IsAbs(rootIdentity.CanonicalPath) || filepath.Clean(rootIdentity.CanonicalPath) != rootIdentity.CanonicalPath || rootIdentity.UID != uint32(os.Geteuid()) || rootIdentity.Mode&0o777 != 0o700 {
		return nil, ErrPreparedExecutionUnavailable
	}
	fd, err := unix.Dup(int(ownerPrivateControlRoot.Fd()))
	if err != nil {
		return nil, ErrPreparedExecutionUnavailable
	}
	unix.CloseOnExec(fd)
	retained := os.NewFile(uintptr(fd), "marshal-prepared-control-root")
	if retained == nil {
		_ = unix.Close(fd)
		return nil, ErrPreparedExecutionUnavailable
	}
	retainedIdentity, err := processsupervisor.ObserveHeldControlDirectory(retained)
	if err != nil || retainedIdentity != rootIdentity {
		_ = retained.Close()
		return nil, ErrPreparedExecutionUnavailable
	}
	sealed := &DurableStore{
		dir:          store.dir,
		nextSequence: 1,
		clock:        store.clock,
		heldFiles:    store.heldFiles,
		preparedDarwin: &preparedDarwinExecutionProfile{
			fixedMarshalPath: fixedMarshalPath,
			core:             core,
			controlRoot:      retained,
			controlIdentity:  rootIdentity,
		},
	}
	return sealed, nil
}
