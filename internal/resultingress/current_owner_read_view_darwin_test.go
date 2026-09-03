//go:build darwin

package resultingress

import (
	"os"
	"testing"
)

func TestCurrentOwnerReadViewDoesNotCreateMissingAuthorityFiles(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	handle, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if view, err := OpenDarwinCurrentOwnerReadView(handle); err == nil || view != nil {
		t.Fatalf("missing authority files admitted: view=%v err=%v", view, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("read view created authority files: %+v", entries)
	}
}
