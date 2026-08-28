package launchidentity

import (
	"strings"
	"testing"
)

func TestEmptyCollectionsHaveOneCanonicalDigest(t *testing.T) {
	wantMaterials := "sha256:4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"
	for name, materials := range map[string][]LaunchMaterialV1{
		"nil":   nil,
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			digest, err := DigestMaterials(materials)
			if err != nil {
				t.Fatal(err)
			}
			if digest != wantMaterials {
				t.Fatalf("materials digest = %q, want %q", digest, wantMaterials)
			}
		})
	}

	base := nativeSpecInput()
	nilInput := base
	nilInput.MaterialRoots = nil
	nilInput.LaunchMaterials = nil
	nilInput.Environment = nil
	emptyInput := base
	emptyInput.MaterialRoots = []MaterialRootV1{}
	emptyInput.LaunchMaterials = []LaunchMaterialV1{}
	emptyInput.Environment = []string{}
	nilDigest, err := DigestSpec(nilInput)
	if err != nil {
		t.Fatal(err)
	}
	emptyDigest, err := DigestSpec(emptyInput)
	if err != nil {
		t.Fatal(err)
	}
	if nilDigest != emptyDigest {
		t.Fatalf("nil digest = %q, empty digest = %q", nilDigest, emptyDigest)
	}
}

func TestSealNativeClosurePreservesClosedEmptyArrays(t *testing.T) {
	for name, input := range map[string]SpecInput{
		"nil":   nativeSpecInput(),
		"empty": nativeEmptySpecInput(),
	} {
		t.Run(name, func(t *testing.T) {
			closure, err := Seal(input)
			if err != nil {
				t.Fatal(err)
			}
			if closure.MaterialRoots == nil || closure.LaunchMaterials == nil || closure.Environment == nil {
				t.Fatalf("closure retained a null collection: %+v", closure)
			}
			if err := closure.Validate(); err != nil {
				t.Fatalf("sealed closure did not validate: %v", err)
			}
			stored, err := closure.Stored()
			if err != nil {
				t.Fatal(err)
			}
			if stored.MaterialRootsJSON != "[]" || stored.LaunchMaterialsJSON != "[]" || stored.EnvironmentJSON != "[]" {
				t.Fatalf("stored arrays = roots:%s materials:%s env:%s", stored.MaterialRootsJSON, stored.LaunchMaterialsJSON, stored.EnvironmentJSON)
			}
		})
	}
}

func nativeSpecInput() SpecInput {
	return SpecInput{
		RuntimeExecutable: ObjectV1{
			CanonicalPath: "/fixed/workload",
			Device:        1,
			Inode:         3,
			FileType:      0o100000,
			Mode:          0o100700,
			UID:           501,
			GID:           20,
			Size:          42,
			LinkCount:     1,
			RawSHA256:     "sha256:" + strings.Repeat("a", 64),
		},
		ClosureProfileID: NativeProfile,
		Arguments:        []string{"/fixed/workload", "argument"},
		WorkingDirectory: "/fixed/worktree",
	}
}

func nativeEmptySpecInput() SpecInput {
	input := nativeSpecInput()
	input.MaterialRoots = []MaterialRootV1{}
	input.LaunchMaterials = []LaunchMaterialV1{}
	input.Environment = []string{}
	return input
}
