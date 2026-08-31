//go:build darwin

package processsupervisor

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/chiga0/marshal-harness/internal/launchidentity"
)

func TestSpawnSourceGateReturnsRoleKeyedExactHeldSet(t *testing.T) {
	payload, root, material := sourceGatePayload(t)
	files, specs, exactSetDigest, err := openSpawnObjects(payload)
	if err != nil {
		t.Fatalf("open exact source set: %v", err)
	}
	defer closeFiles(files...)
	if exactSetDigest == "" || !validDigest(exactSetDigest) || len(files) != len(specs) {
		t.Fatalf("files=%d specs=%d digest=%q", len(files), len(specs), exactSetDigest)
	}
	for index, file := range files {
		if err := verifyHeldObject(file, specs[index]); err != nil {
			t.Fatalf("held[%d] role=%q is not the admitted descriptor: %v", index, specs[index].Role, err)
		}
	}
	if specs[0].Role != "working-directory" || specs[1].Role != "runtime" || specs[2].CanonicalPath != root || specs[3].CanonicalPath != material {
		t.Fatalf("non-canonical held order: %+v", specs)
	}
}

func TestSpawnSourceGateRejectsExtraRoleAndRecordBeforeChildCreation(t *testing.T) {
	payload, root, _ := sourceGatePayload(t)
	if err := os.WriteFile(filepath.Join(root, "extra"), []byte("extra"), 0o600); err != nil {
		t.Fatal(err)
	}
	if files, _, _, err := openSpawnObjects(payload); !errors.Is(err, ErrConflict) || files != nil {
		t.Fatalf("extra material files=%v err=%v", files, err)
	}

	payload, _, _ = sourceGatePayload(t)
	payload.LaunchMaterials[0].Role = "bundle/wrong"
	if files, _, _, err := openSpawnObjects(payload); !errors.Is(err, ErrConflict) || files != nil {
		t.Fatalf("wrong role files=%v err=%v", files, err)
	}

	payload, _, _ = sourceGatePayload(t)
	wrongLive := *payload.AllocationLiveIdentity
	wrongLive.Inode++
	payload.AllocationLiveIdentity = &wrongLive
	if files, _, _, err := openSpawnObjects(payload); !errors.Is(err, ErrConflict) || files != nil {
		t.Fatalf("allocation live mismatch files=%v err=%v", files, err)
	}
}

func TestSpawnSourceGateRejectsPathReplacementAndDigestIsRoleKeyed(t *testing.T) {
	payload, root, material := sourceGatePayload(t)
	old := material + ".old"
	if err := os.Rename(material, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(material, []byte("material"), 0o600); err != nil {
		t.Fatal(err)
	}
	if files, _, _, err := openSpawnObjects(payload); !errors.Is(err, ErrConflict) || files != nil {
		t.Fatalf("material ABA files=%v err=%v", files, err)
	}

	payload, _, _ = sourceGatePayload(t)
	first := spawnObjects(payload)
	second := append([]HeldObjectSpec(nil), first...)
	for i, j := 0, len(second)-1; i < j; i, j = i+1, j-1 {
		second[i], second[j] = second[j], second[i]
	}
	left, err := digestHeldSet(first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := digestHeldSet(second)
	if err != nil || left != right {
		t.Fatalf("role-keyed digest left=%q right=%q err=%v", left, right, err)
	}
	_ = root
}

func TestProcessReportRequiresExactSetDigest(t *testing.T) {
	report := ProcessReport{State: "running", ObserverIdentity: "darwin-fixed-process-supervisor-v1", ObservedAt: fixtureObservedAt, Process: validBootstrap().Core.Process, RuntimeObjectDigest: digest("a"), WorkingObjectDigest: digest("b")}
	if err := ValidateProcessReport(report); err != nil {
		t.Fatalf("legacy report no longer replayable: %v", err)
	}
	if err := ValidateS1ProcessReport(report); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing exact-set digest accepted as S1: %v", err)
	}
	report.SourceGateRevision = SourceGateRevisionV1
	report.ExactSetDigest = "sha256:not-a-digest"
	if err := ValidateS1ProcessReport(report); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed exact-set digest accepted: %v", err)
	}
}

func TestLegacySpawnWireReplaysButCannotEnterFreshMechanics(t *testing.T) {
	payload := validSpawnPayload()
	payload.SourceGateRevision = ""
	payload.AllocationLiveIdentity = nil
	raw, err := canonicalValue(payload)
	if err != nil {
		t.Fatal(err)
	}
	var replay SpawnPayload
	if err := strictCanonicalDecode(raw, &replay); err != nil || replay.SourceGateRevision != "" || replay.AllocationLiveIdentity != nil {
		t.Fatalf("legacy wire no longer decodes byte-stably: err=%v payload=%+v", err, replay)
	}
	if err := validateSpawnPayload(replay); err != nil {
		t.Fatalf("legacy completed-replay payload rejected: %v", err)
	}
	if files, _, _, err := openSpawnObjects(replay); !errors.Is(err, ErrConflict) || files != nil {
		t.Fatalf("legacy payload entered fresh source gate: files=%v err=%v", files, err)
	}
}

func sourceGatePayload(t *testing.T) (SpawnPayload, string, string) {
	t.Helper()
	base := t.TempDir()
	// Held-object opens use O_NOFOLLOW_ANY, so the fixture must observe and
	// reopen one canonical base path (macOS temp dirs sit under /var ->
	// /private/var).
	if resolved, err := filepath.EvalSymlinks(base); err != nil {
		t.Fatal(err)
	} else {
		base = resolved
	}
	runtimePath := filepath.Join(base, "runtime")
	workingPath := filepath.Join(base, "work")
	rootPath := filepath.Join(base, "bundle")
	materialPath := filepath.Join(rootPath, "entry")
	if err := os.WriteFile(runtimePath, []byte("runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(workingPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(materialPath, []byte("material"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtimeFile, runtime, err := openObservedSpec("runtime", runtimePath, "regular")
	if err != nil {
		t.Fatal(err)
	}
	_ = runtimeFile.Close()
	workingFile, working, err := openObservedSpec("working-directory", workingPath, "directory")
	if err != nil {
		t.Fatal(err)
	}
	_ = workingFile.Close()
	rootFile, root, err := openObservedSpec("bundle", rootPath, "directory")
	if err != nil {
		t.Fatal(err)
	}
	_ = rootFile.Close()
	materialFile, material, err := openObservedSpec("bundle/entry", materialPath, "regular")
	if err != nil {
		t.Fatal(err)
	}
	_ = materialFile.Close()
	payload := SpawnPayload{
		Runtime: runtime, WorkingDirectory: working, ClosureProfileID: launchidentity.NativeProfile,
		SourceGateRevision:     SourceGateRevisionV1,
		AllocationLiveIdentity: &AllocationLiveIdentity{Device: working.Device, Inode: working.Inode, FileType: working.FileType, UID: working.UID, GID: working.GID, Mode: working.Mode, LinkCount: working.LinkCount, Size: working.Size},
		MaterialRoots:          []launchidentity.MaterialRootV1{{Name: "bundle", CanonicalPath: rootPath, PackageRelative: "bundle", Object: launchObject(root)}},
		LaunchMaterials:        []launchidentity.LaunchMaterialV1{{Role: "bundle/entry", Object: launchObject(material)}},
	}
	return payload, rootPath, materialPath
}
