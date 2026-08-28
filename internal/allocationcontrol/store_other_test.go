//go:build !darwin

package allocationcontrol

import (
	"errors"
	"testing"
)

func TestUnsupportedPlatformFailsBeforeOpeningStore(t *testing.T) {
	if store, err := OpenStore(t.TempDir(), testStoreScope(t)); store != nil || !errors.Is(err, ErrPlatformUnavailable) {
		t.Fatal("unsupported platform did not fail closed before mutation")
	}
}
