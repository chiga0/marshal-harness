//go:build !unix

package runstore

import (
	"os"
	"testing"
)

func TestDescriptorBoundRunStoreUnavailableOutsideUnix(t *testing.T) {
	root, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := NewFromStateRootDescriptor(root); err == nil {
		t.Fatal("descriptor-bound RunStore unexpectedly available outside Unix")
	}
}
