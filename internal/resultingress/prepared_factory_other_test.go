//go:build !darwin || !arm64

package resultingress

import (
	"errors"
	"os"
	"testing"
)

func TestDarwinResultIngressFactoryFailsClosedOffProfile(t *testing.T) {
	directory := t.TempDir()
	held, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if _, err := OpenDarwinResultIngressStore(held); !errors.Is(err, ErrPreparedExecutionUnavailable) {
		t.Fatalf("OpenDarwinResultIngressStore err=%v, want unavailable", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("off-profile factory changed authority directory: %v", entries)
	}
}
