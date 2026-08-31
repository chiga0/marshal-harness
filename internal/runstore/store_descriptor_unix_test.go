//go:build unix

package runstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDescriptorBoundAcquireExistingUsesHeldStateRootAfterReplacement(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, ".marshal")
	seed, err := New(root).Acquire("run:held")
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Release(); err != nil {
		t.Fatal(err)
	}
	stateRoot, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewFromStateRootDescriptor(stateRoot)
	if err != nil {
		stateRoot.Close()
		t.Fatal(err)
	}
	defer store.Close()
	defer stateRoot.Close()

	oldRoot := filepath.Join(parent, ".marshal-old")
	if err := os.Rename(root, oldRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "runs", "run:held"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runs", "run:held", "lease.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	lease, err := store.AcquireExisting("run:held")
	if err != nil {
		t.Fatalf("held StateRoot descriptor was not authoritative: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "runs", "run:held", "lease.lock.owner")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement StateRoot was mutated by descriptor-bound acquisition: %v", err)
	}
	if _, err := os.Stat(filepath.Join(oldRoot, "runs", "run:held", "lease.lock.owner")); err != nil {
		t.Fatalf("held StateRoot did not receive existing-only owner record: %v", err)
	}
}

func TestDescriptorBoundAcquireExistingIsExistingOnly(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, ".marshal")
	if err := os.MkdirAll(filepath.Join(root, "runs"), 0o700); err != nil {
		t.Fatal(err)
	}
	stateRoot, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer stateRoot.Close()
	store, err := NewFromStateRootDescriptor(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.AcquireExisting("run:missing"); err == nil {
		t.Fatal("descriptor-bound AcquireExisting created an unknown Run")
	}
	if _, err := os.Stat(filepath.Join(root, "runs", "run:missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unknown Run path was created: %v", err)
	}
}
