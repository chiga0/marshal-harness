//go:build darwin

package launchidentity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestVerifyCurrentClosureIsReadOnlyAndClosesTemporaryDescriptors(t *testing.T) {
	closure, live, runtimePath, workingPath := currentNativeFixture(t)
	observed, err := VerifyCurrentClosure(closure, live)
	if err != nil {
		t.Fatalf("verify current closure: %v", err)
	}
	if observed.Runtime.CanonicalPath != runtimePath || observed.WorkingDirectory.CanonicalPath != workingPath || len(observed.LaunchMaterials) != 0 || len(observed.MaterialRoots) != 0 || observed.Pi0843IdentityDigest != "" {
		t.Fatalf("unexpected closed observation: %+v", observed)
	}
	if _, err := os.Stat(runtimePath); err != nil {
		t.Fatalf("runtime fixture disappeared: %v", err)
	}
}

func TestVerifyCurrentClosureRejectsAllocationLiveAndCWDDrift(t *testing.T) {
	closure, live, _, workingPath := currentNativeFixture(t)
	wrongLive := live
	wrongLive.Inode++
	if _, err := VerifyCurrentClosure(closure, wrongLive); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("allocation live drift error = %v", err)
	}
	oldPath := workingPath + ".old"
	if err := os.Rename(workingPath, oldPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(workingPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyCurrentClosure(closure, live); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("cwd replacement error = %v", err)
	}
}

func TestEnumerateCurrentRootRejectsSymlinkAndReturnsExactRoleRecords(t *testing.T) {
	root := t.TempDir()
	materialPath := filepath.Join(root, "entry")
	if err := os.WriteFile(materialPath, []byte("material"), 0o600); err != nil {
		t.Fatal(err)
	}
	rootFile, rootObject, err := openObject(root, unix.S_IFDIR, false)
	if err != nil {
		t.Fatal(err)
	}
	expected := MaterialRootV1{Name: "bundle", CanonicalPath: root, PackageRelative: "bundle", Object: rootObject}
	records, err := enumerateCurrentRoot(rootFile, expected)
	_ = rootFile.Close()
	if err != nil || len(records) != 1 || records[0].Role != "bundle/entry" || records[0].Object.CanonicalPath != materialPath {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	if err := os.Symlink(materialPath, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	rootFile, _, err = openObject(root, unix.S_IFDIR, false)
	if err != nil {
		t.Fatal(err)
	}
	defer rootFile.Close()
	if _, err := enumerateCurrentRoot(rootFile, expected); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("symlink enumeration error = %v", err)
	}
}

func currentNativeFixture(t *testing.T) (ClosureV1, LiveIdentity, string, string) {
	t.Helper()
	base := t.TempDir()
	runtimePath := filepath.Join(base, "runtime")
	workingPath := filepath.Join(base, "work")
	if err := os.WriteFile(runtimePath, []byte("runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(workingPath, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeFile, runtime, err := openObject(runtimePath, unix.S_IFREG, true)
	if err != nil {
		t.Fatal(err)
	}
	_ = runtimeFile.Close()
	workingFile, working, err := openObject(workingPath, unix.S_IFDIR, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = workingFile.Close()
	closure, err := Seal(SpecInput{
		RuntimeExecutable: runtime, ClosureProfileID: NativeProfile,
		MaterialRoots: []MaterialRootV1{}, LaunchMaterials: []LaunchMaterialV1{},
		Arguments: []string{runtimePath}, Environment: []string{}, WorkingDirectory: workingPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	return closure, LiveIdentityFromObject(working), runtimePath, workingPath
}
