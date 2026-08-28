//go:build darwin

package allocationcontrol

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSameStatTreatsAPFSDirectoryAccountingAsNonAuthoritative(t *testing.T) {
	directory := unix.Stat_t{Dev: 1, Ino: 2, Mode: unix.S_IFDIR | 0o700, Uid: 501, Gid: 20, Size: 64, Nlink: 2}
	changedAccounting := directory
	changedAccounting.Size = 160
	changedAccounting.Nlink = 1
	if !sameStat(directory, changedAccounting) {
		t.Fatal("directory accounting drift replaced stable object identity")
	}
	replaced := changedAccounting
	replaced.Ino++
	if sameStat(directory, replaced) {
		t.Fatal("directory inode replacement was accepted")
	}
	ownerDrift := changedAccounting
	ownerDrift.Uid++
	if sameStat(directory, ownerDrift) {
		t.Fatal("directory owner drift was accepted")
	}
}

func TestSameStatKeepsRegularFileSizeAndLinkCountAuthoritative(t *testing.T) {
	regular := unix.Stat_t{Dev: 1, Ino: 2, Mode: unix.S_IFREG | 0o600, Uid: 501, Gid: 20, Size: 64, Nlink: 1}
	sizeDrift := regular
	sizeDrift.Size++
	if sameStat(regular, sizeDrift) {
		t.Fatal("regular file size drift was accepted")
	}
	linkDrift := regular
	linkDrift.Nlink++
	if sameStat(regular, linkDrift) {
		t.Fatal("regular file link-count drift was accepted")
	}
}

// canonicalTempDir resolves macOS runner aliases such as /var -> /private/var.
// OpenStore intentionally rejects symlinked path components, so its fixtures
// must pass the physical directory rather than the runner-provided alias.
func canonicalTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestStoreCreatesOnlyOwnerBoundDurableNamespace(t *testing.T) {
	root := canonicalTempDir(t)
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
		root := canonicalTempDir(t)
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
		root := canonicalTempDir(t)
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
	root := canonicalTempDir(t)
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
	root := canonicalTempDir(t)
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
	root := canonicalTempDir(t)
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
	root := canonicalTempDir(t)
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
