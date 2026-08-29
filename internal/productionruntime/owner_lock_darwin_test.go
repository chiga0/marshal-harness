//go:build darwin && arm64

package productionruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

func currentProcessAcquisition() resultingress.ControlOwnerAcquisition {
	value := testAcquisition()
	value.OwnerUID = uint32(os.Getuid())
	value.OwnerGID = uint32(os.Getgid())
	value.OwnerProcess.PID = os.Getpid()
	value.OwnerProcess.SessionID = os.Getpid()
	value.OwnerProcess.ProcessGroupID = os.Getpid()
	value.OwnerBinary.UID = uint32(os.Getuid())
	value.OwnerBinary.GID = uint32(os.Getgid())
	return value
}

func TestRepositoryOwnerLockRejectsCompetitorAndPathABA(t *testing.T) {
	// O_NOFOLLOW_ANY intentionally rejects a symlink in any path component.
	// GitHub-hosted macOS runners may place testing.T.TempDir below a symlinked
	// runner workspace, so use Darwin's real private temporary root. This keeps
	// the production path boundary intact while exercising the lock semantics.
	root, err := os.MkdirTemp("/private/tmp", "marshal-owner-lock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove owner lock root: %v", err)
		}
	})
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	// Darwin inherits the parent directory group. /private/tmp is commonly
	// group wheel, but the production owner-file identity is intentionally
	// bound to the ordinary user's effective group.
	if err := os.Chown(root, -1, os.Getgid()); err != nil {
		t.Fatalf("set owner lock root group: %v", err)
	}
	acquisition := currentProcessAcquisition()
	first, err := openRepositoryOwnerLock(root, acquisition)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	called := false
	err = first.WithCurrentOwnerLock(context.Background(), acquisition, func() error { called = true; return nil })
	if !application.HasReason(err, application.ReasonOwnerNotCurrent) || called {
		t.Fatalf("unclaimed lock err=%v called=%t", err, called)
	}
	if err := first.claimRuntime(); err != nil {
		t.Fatal(err)
	}
	if _, err := openRepositoryOwnerLock(root, acquisition); !application.HasReason(err, application.ReasonOwnerUnavailable) {
		t.Fatalf("second lock err=%v", err)
	}
	concrete := first.(*darwinRepositoryOwnerLock)
	if err := os.Rename(filepath.Join(root, concrete.name), filepath.Join(root, "replaced.lock")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, concrete.name), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	called = false
	err = first.WithCurrentOwnerLock(context.Background(), acquisition, func() error { called = true; return nil })
	if !application.HasReason(err, application.ReasonOwnerNotCurrent) || called {
		t.Fatalf("ABA err=%v called=%t", err, called)
	}
}
