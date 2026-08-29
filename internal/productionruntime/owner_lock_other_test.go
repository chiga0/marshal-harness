//go:build !darwin || !arm64

package productionruntime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chiga0/marshal-harness/internal/application"
)

func TestRepositoryOwnerLockIsUnavailableBeforeFilesystemMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "must-not-exist")
	if _, err := openRepositoryOwnerLock(root, testAcquisition()); !application.HasReason(err, application.ReasonPlatformProfileUnavailable) {
		t.Fatalf("reason=%v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("non-Darwin owner lock mutated filesystem: %v", err)
	}
}
