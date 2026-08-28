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
	root := t.TempDir()
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
