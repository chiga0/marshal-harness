//go:build darwin && arm64

package resultingress

import (
	"os"
	"path/filepath"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
	"golang.org/x/sys/unix"
)

// OpenPi0843DarwinResultIngressStore is the sole S1 fresh-start composition.
// It retains one exact owner-private control-root descriptor and one current
// fixed Marshal image; no mechanics interface or callback is configurable.
func OpenPi0843DarwinResultIngressStore(ledgerDir, fixedMarshalPath string, ownerPrivateControlRoot *os.File) (*DurableStore, error) {
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
	store, err := OpenResultIngressStore(ledgerDir)
	if err != nil {
		_ = retained.Close()
		return nil, err
	}
	store.preparedDarwin = &preparedDarwinExecutionProfile{fixedMarshalPath: fixedMarshalPath, core: core, controlRoot: retained, controlIdentity: rootIdentity}
	return store, nil
}
