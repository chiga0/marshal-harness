package runstore

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestListExistingRunIDsIsSortedAndCreationFree(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	if got, err := store.ListExistingRunIDs(); err != nil || len(got) != 0 {
		t.Fatalf("empty list = %v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "runs")); !os.IsNotExist(err) {
		t.Fatalf("listing created runs directory: %v", err)
	}
	for _, runID := range []string{"run-z", "run-a"} {
		if err := os.MkdirAll(filepath.Join(root, "runs", runID), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := store.ListExistingRunIDs(); err != nil || !reflect.DeepEqual(got, []string{"run-a", "run-z"}) {
		t.Fatalf("sorted list = %v, %v", got, err)
	}
}

func TestListExistingRunIDsRejectsNonRunEntry(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "runs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runs", "unexpected"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(root).ListExistingRunIDs(); err == nil {
		t.Fatal("listing accepted a non-directory authority entry")
	}
}

func TestListExistingRunIDsUsesHeldStateRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("descriptor-bound store is unavailable on windows")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "runs", "run-held"), 0o700); err != nil {
		t.Fatal(err)
	}
	handle, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	store, err := NewFromStateRootDescriptor(handle)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if got, err := store.ListExistingRunIDs(); err != nil || !reflect.DeepEqual(got, []string{"run-held"}) {
		t.Fatalf("held list = %v, %v", got, err)
	}
}
