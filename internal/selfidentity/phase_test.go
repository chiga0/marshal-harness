package selfidentity

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

func TestPhaseObservationPersistenceRejectsReplaySymlinkParentSwapAndABA(t *testing.T) {
	root := t.TempDir()
	observation := phaseTestObservation(t, "2026-08-27T10:00:00Z")

	t.Run("exact replay only", func(t *testing.T) {
		directory := filepath.Join(root, "exact")
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := PersistPhaseObservation(directory, "phase.json", observation); err != nil {
			t.Fatal(err)
		}
		if _, err := PersistPhaseObservation(directory, "phase.json", observation); err != nil {
			t.Fatalf("exact replay: %v", err)
		}
		later := phaseTestObservation(t, "2026-08-27T10:00:01Z")
		if _, err := PersistPhaseObservation(directory, "phase.json", later); err == nil {
			t.Fatal("different observation replay was accepted")
		}
		crossed := later
		var err error
		crossed.ActivationDigest = canonical.DigestBytes([]byte("replacement activation"))
		crossed.IdentitySubjectDigest, err = digestIdentitySubject(crossed)
		if err != nil {
			t.Fatal(err)
		}
		crossed.ObservationDigest, err = digestObservation(crossed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := PersistPhaseObservation(directory, "phase.json", crossed); err == nil {
			t.Fatal("cross-identity replacement was accepted")
		}
	})

	t.Run("symlink and nonregular", func(t *testing.T) {
		directory := filepath.Join(root, "symlink")
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(directory, "target")
		if err := os.WriteFile(target, []byte("{}"), 0o400); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("target", filepath.Join(directory, "phase.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := PersistPhaseObservation(directory, "phase.json", observation); err == nil {
			t.Fatal("symlink phase record was accepted")
		}
		if _, err := ReadPhaseObservation(directory); err == nil {
			t.Fatal("directory phase record was accepted")
		}
	})

	t.Run("hardlink alias", func(t *testing.T) {
		directory := filepath.Join(root, "hardlink")
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := PersistPhaseObservation(directory, "phase.json", observation); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(filepath.Join(directory, "phase.json"), filepath.Join(directory, "alias.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadPhaseObservation(filepath.Join(directory, "phase.json")); err == nil {
			t.Fatal("hardlinked phase evidence was accepted")
		}
	})

	t.Run("parent swap", func(t *testing.T) {
		directory := filepath.Join(root, "parent")
		held := filepath.Join(root, "parent-held")
		replacement := filepath.Join(root, "parent-replacement")
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(replacement, 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := persistPhaseObservation(directory, "phase.json", observation, func() {
			if renameErr := os.Rename(directory, held); renameErr != nil {
				t.Fatal(renameErr)
			}
			if linkErr := os.Symlink(replacement, directory); linkErr != nil {
				t.Fatal(linkErr)
			}
		}, nil)
		if err == nil {
			t.Fatal("parent pathname swap was accepted")
		}
	})

	t.Run("interrupted install is not visible", func(t *testing.T) {
		directory := filepath.Join(root, "interrupted")
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		stop := errors.New("simulated stop before link")
		_, err := persistPhaseObservation(directory, "phase.json", observation, nil, func() error {
			if _, statErr := os.Lstat(filepath.Join(directory, "phase.json")); !os.IsNotExist(statErr) {
				t.Fatalf("partial final record became visible: %v", statErr)
			}
			return stop
		})
		if !errors.Is(err, stop) {
			t.Fatalf("interrupted install error = %v", err)
		}
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 0 {
			t.Fatalf("interrupted install left files: entries=%v err=%v", entries, err)
		}
	})

	t.Run("record ABA", func(t *testing.T) {
		directory := filepath.Join(root, "aba")
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := PersistPhaseObservation(directory, "phase.json", observation); err != nil {
			t.Fatal(err)
		}
		directoryFD, err := unix.Open(directory, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer unix.Close(directoryFD)
		raw, err := json.Marshal(observation)
		if err != nil {
			t.Fatal(err)
		}
		raw, err = canonical.JSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		_, err = readPhaseObservationAt(directoryFD, "phase.json", func() {
			if renameErr := os.Rename(filepath.Join(directory, "phase.json"), filepath.Join(directory, "phase.old")); renameErr != nil {
				t.Fatal(renameErr)
			}
			if writeErr := os.WriteFile(filepath.Join(directory, "phase.json"), raw, 0o400); writeErr != nil {
				t.Fatal(writeErr)
			}
		})
		if err == nil {
			t.Fatal("record ABA replacement was accepted")
		}
	})
}

func TestClosedVerificationAndReviewBindingsRejectTampering(t *testing.T) {
	dispatch := phaseTestObservation(t, "2026-08-27T10:00:00Z")
	ingress := phaseTestObservation(t, "2026-08-27T10:00:01Z")
	verification := phaseTestObservation(t, "2026-08-27T10:00:02Z")
	applicability, err := ApplicabilityForObservation(dispatch)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := BuildVerificationBinding("attempt-1", applicability, dispatch, ingress, verification)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateVerificationBinding(binding, "attempt-1", applicability, dispatch, ingress, verification); err != nil {
		t.Fatal(err)
	}
	tampered := binding
	tampered.IngressObservationDigest = canonical.DigestBytes([]byte("tampered"))
	if err := ValidateVerificationBinding(tampered, "attempt-1", applicability, dispatch, ingress, verification); err == nil {
		t.Fatal("tampered verification binding was accepted")
	}
	review := phaseTestObservation(t, "2026-08-27T10:00:03Z")
	reviewBinding, err := BuildReviewBinding("attempt-1", 1, binding, review)
	if err != nil {
		t.Fatal(err)
	}
	if reviewBinding.VerificationObservationDigest != verification.ObservationDigest || reviewBinding.ReviewObservationDigest != review.ObservationDigest {
		t.Fatalf("review binding = %+v", reviewBinding)
	}
	if err := ValidateReviewBindingProjection(reviewBinding, "attempt-1", 1, binding); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*LocalReviewBindingV2){
		"attempt":       func(value *LocalReviewBindingV2) { value.AttemptID = "attempt-2" },
		"round":         func(value *LocalReviewBindingV2) { value.ReviewRound = 2 },
		"profile":       func(value *LocalReviewBindingV2) { value.SelfProfile = "managed" },
		"applicability": func(value *LocalReviewBindingV2) { value.Applicability.Publication = "remote" },
	} {
		t.Run(name, func(t *testing.T) {
			forged := reviewBinding
			mutate(&forged)
			if err := ValidateReviewBindingProjection(forged, "attempt-1", 1, binding); err == nil {
				t.Fatal("paired forged review projection was accepted")
			}
		})
	}
}

func TestPortableSubjectAllowsFreshHostObjectObservation(t *testing.T) {
	first := phaseTestObservation(t, "2026-08-27T10:00:00Z")
	second := first
	second.ProcessID = 456
	second.CurrentPathObject.Device = "91"
	second.CurrentPathObject.Inode = "92"
	second.ObservedAt = "2026-08-27T10:00:01Z"
	var err error
	second.IdentitySubjectDigest, err = digestIdentitySubject(second)
	if err != nil {
		t.Fatal(err)
	}
	second.ObservationDigest, err = digestObservation(second)
	if err != nil {
		t.Fatal(err)
	}
	if first.IdentitySubjectDigest != second.IdentitySubjectDigest {
		t.Fatalf("host-local object numbers changed portable subject: first=%s second=%s", first.IdentitySubjectDigest, second.IdentitySubjectDigest)
	}
	if first.ObservationDigest == second.ObservationDigest {
		t.Fatal("fresh host-local observation did not receive a distinct observation digest")
	}
	if err := SameSubject(first, second); err != nil {
		t.Fatalf("same portable subject was rejected across host-local observations: %v", err)
	}
}

func TestObservationV2RejectsHostLocalShapeForgery(t *testing.T) {
	base := phaseTestObservation(t, "2026-08-27T10:00:00Z")
	tests := map[string]func(*LocalSelfIdentityObservationV2){
		"process path":      func(value *LocalSelfIdentityObservationV2) { value.ProcessExecutablePath = "/other/marshal" },
		"repository digest": func(value *LocalSelfIdentityObservationV2) { value.RepositoryIdentity = "repository" },
		"observation kind":  func(value *LocalSelfIdentityObservationV2) { value.CurrentPathObject.ObservationKind = "portable" },
		"source head":       func(value *LocalSelfIdentityObservationV2) { value.SourceHead = "head" },
		"timestamp":         func(value *LocalSelfIdentityObservationV2) { value.ObservedAt = "2026-08-27T10:00:00+00:00" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			forged := base
			mutate(&forged)
			if err := ValidateObservation(forged); err == nil {
				t.Fatal("forged host-local observation was accepted")
			}
		})
	}
}

func phaseTestObservation(t *testing.T, observedAt string) LocalSelfIdentityObservationV2 {
	t.Helper()
	digest := canonical.DigestBytes([]byte("object"))
	observation := LocalSelfIdentityObservationV2{
		SchemaVersion: ObservationSchema, ActivationDigest: canonical.DigestBytes([]byte("activation")),
		ProcessID: 123, ProcessExecutablePath: "/fixed/marshal",
		RepositoryIdentity: canonical.DigestBytes([]byte("repository")), CanonicalRepositoryRoot: "/repository",
		CurrentPathObject: CurrentPathObjectV2{CanonicalPath: "/fixed/marshal", Device: "1", Inode: "2", Size: 6, RawSHA256: digest, PathRechecked: true, ObservationKind: "darwin-current-path-fd-object"},
		SourceHead:        testSourceHead, SelfProfile: LocalProfile, ObservedAt: observedAt,
		Status: "pass", ReasonCode: ReasonObserved,
	}
	var err error
	observation.IdentitySubjectDigest, err = digestIdentitySubject(observation)
	if err != nil {
		t.Fatal(err)
	}
	observation.ObservationDigest, err = digestObservation(observation)
	if err != nil {
		t.Fatal(err)
	}
	return observation
}
