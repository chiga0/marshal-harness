//go:build darwin

package allocationcontrol

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreCreatesOnlyOwnerBoundDurableNamespace(t *testing.T) {
	root := t.TempDir()
	scope := testStoreScope(t)
	store, err := OpenStore(root, scope)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scopeName, _ := scope.directoryName()
	for _, path := range []string{
		filepath.Join(root, storeDirectoryName),
		filepath.Join(root, storeDirectoryName, scopeName),
		filepath.Join(root, storeDirectoryName, scopeName, objectsDirectoryName),
	} {
		stat, err := os.Lstat(path)
		if err != nil || !stat.IsDir() || stat.Mode().Perm() != 0o700 {
			t.Fatal("allocation directory is not owner-only")
		}
	}
	journalPath := filepath.Join(root, storeDirectoryName, scopeName, JournalFileName)
	stat, err := os.Lstat(journalPath)
	if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm() != 0o600 {
		t.Fatal("journal is not an owner-only regular file")
	}
}

func TestStoreRejectsJournalSymlinkAndHardlink(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		scope := testStoreScope(t)
		scopeName, _ := scope.directoryName()
		base := filepath.Join(root, storeDirectoryName, scopeName)
		if err := os.MkdirAll(base, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(base, objectsDirectoryName), 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, "target")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(base, JournalFileName)); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenStore(root, scope); err == nil {
			t.Fatal("journal symlink was accepted")
		}
	})
	t.Run("hardlink", func(t *testing.T) {
		root := t.TempDir()
		scope := testStoreScope(t)
		scopeName, _ := scope.directoryName()
		store, err := OpenStore(root, scope)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		journal := filepath.Join(root, storeDirectoryName, scopeName, JournalFileName)
		if err := os.Link(journal, filepath.Join(root, "journal-hardlink")); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenStore(root, scope); !errors.Is(err, ErrFilesystemConflict) {
			t.Fatal("multiply-linked journal was accepted")
		}
	})
}

func TestStoreRejectsIntermediateRootSymlink(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(filepath.Join(real, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(filepath.Join(link, "child"), testStoreScope(t)); err == nil {
		t.Fatal("intermediate root symlink was followed")
	}
}

func TestImplementationContainsNoRecursiveDelete(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "Remove"+"All(") {
			t.Fatal("allocation controller contains a recursive delete path")
		}
	}
}

func TestStoreRejectsAuthorityFactsOutsideBoundScope(t *testing.T) {
	root := t.TempDir()
	scope := testStoreScope(t)
	store, err := OpenStore(root, scope)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fact := testCommittedFact(t, RecordProvisionIntent, "fact-1")
	fact.Binding.AllocationID = "other-allocation"
	if err := store.SyncAuthorityProjection([]CommittedAuthorityFact{fact}); !errors.Is(err, ErrAuthorityConflict) {
		t.Fatal("store accepted authority facts outside its constructor-bound scope")
	}
	if len(store.JournalRecords()) != 0 {
		t.Fatal("scope conflict still mutated the recovery journal")
	}
}

func TestDifferentAllocationScopesUseDistinctStableNamespaces(t *testing.T) {
	root := t.TempDir()
	first := testStoreScope(t)
	second := first
	second.AllocationID = "allocation-2"
	firstName, _ := first.directoryName()
	secondName, _ := second.directoryName()
	if firstName == secondName {
		t.Fatal("different allocation scopes derived the same namespace")
	}
	firstStore, err := OpenStore(root, first)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}
	secondStore, err := OpenStore(root, second)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	for _, name := range []string{firstName, secondName} {
		if stat, err := os.Lstat(filepath.Join(root, storeDirectoryName, name)); err != nil || !stat.IsDir() {
			t.Fatal("stable per-allocation namespace was not created")
		}
	}
}

func TestSameAllocationScopeHasSingleOpenStoreOwner(t *testing.T) {
	root := t.TempDir()
	scope := testStoreScope(t)
	first, err := OpenStore(root, scope)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := OpenStore(root, scope); second != nil || !errors.Is(err, ErrFilesystemConflict) {
		t.Fatal("second live Store owner acquired the same allocation journal")
	}
}
