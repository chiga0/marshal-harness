//go:build !darwin

package provider

import (
	"errors"
	"testing"
)

func TestDarwinHeldRegistrationStoreFailsClosedOffDarwin(t *testing.T) {
	store, err := OpenDarwinRegistrationStore(nil)
	if store != nil {
		t.Fatal("non-Darwin constructor returned a registration store")
	}
	if !errors.Is(err, ErrHeldRegistrationUnavailable) {
		t.Fatalf("expected ErrHeldRegistrationUnavailable, got %v", err)
	}
}
