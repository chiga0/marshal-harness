package productionruntime

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
)

func TestProductionLocalIdentityPersistsDispatchAndReusesIngress(t *testing.T) {
	store := runstore.New(t.TempDir())
	lease, err := store.Acquire("run:production-local-identity")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	entry := productionLocalObservation(t, "activation-a", time.Unix(10, 0).UTC())
	fresh := productionLocalObservation(t, "activation-a", time.Unix(11, 0).UTC())
	ledger := &CompositionLedger{
		runLease: lease, entryLocalSelfIdentity: &entry,
		observeLocalSelfIdentity: func() (selfidentity.LocalSelfIdentityObservationV2, error) { return fresh, nil },
	}
	attemptID := "attempt:production-local-identity"
	if err := ledger.persistLocalDispatchObservation(attemptID); err != nil {
		t.Fatalf("persist dispatch: %v", err)
	}
	dispatchDigest := fresh.ObservationDigest
	fresh = productionLocalObservation(t, "activation-a", time.Unix(12, 0).UTC())
	if got, err := ledger.localDispatchObservationDigest(attemptID); err != nil || got != dispatchDigest {
		t.Fatalf("dispatch digest=%q want=%q err=%v", got, dispatchDigest, err)
	}
	directory, err := runstore.OpenDirectoryUnderLease(lease, "attempts", attemptID)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	fresh = productionLocalObservation(t, "activation-a", time.Unix(13, 0).UTC())
	gotDispatch, ingressDigest, err := ledger.localIngressObservationDigests(directory, false)
	if err != nil || gotDispatch != dispatchDigest || ingressDigest != fresh.ObservationDigest {
		t.Fatalf("ingress digests=(%q,%q) err=%v", gotDispatch, ingressDigest, err)
	}
	firstIngressDigest := ingressDigest
	fresh = productionLocalObservation(t, "activation-a", time.Unix(14, 0).UTC())
	gotDispatch, ingressDigest, err = ledger.localIngressObservationDigests(directory, true)
	if err != nil || gotDispatch != dispatchDigest || ingressDigest != firstIngressDigest {
		t.Fatalf("committed replay rebound ingress=(%q,%q) err=%v", gotDispatch, ingressDigest, err)
	}
	fresh = productionLocalObservation(t, "activation-b", time.Unix(15, 0).UTC())
	if _, _, err := ledger.localIngressObservationDigests(directory, true); err == nil {
		t.Fatal("cross-activation ingress observation was accepted")
	}
}

func productionLocalObservation(t *testing.T, activation string, observedAt time.Time) selfidentity.LocalSelfIdentityObservationV2 {
	t.Helper()
	observation := selfidentity.LocalSelfIdentityObservationV2{
		SchemaVersion: selfidentity.ObservationSchema, ActivationDigest: canonical.DigestBytes([]byte(activation)),
		ProcessID: 42, ProcessExecutablePath: "/fixed/bin/marshal",
		RepositoryIdentity: canonical.DigestBytes([]byte("repository")), CanonicalRepositoryRoot: "/fixed/repository",
		CurrentPathObject: selfidentity.CurrentPathObjectV2{
			CanonicalPath: "/fixed/bin/marshal", Device: "1", Inode: "2", Size: 3,
			RawSHA256: canonical.DigestBytes([]byte("marshal")), PathRechecked: true, ObservationKind: "darwin-current-path-fd-object",
		},
		SourceHead: strings.Repeat("a", 40), SelfProfile: selfidentity.LocalProfile,
		ObservedAt: observedAt.Format(time.RFC3339), Status: "pass", ReasonCode: selfidentity.ReasonObserved,
	}
	observation.IdentitySubjectDigest = productionLocalJSONDigest(t, map[string]any{
		"activationDigest": observation.ActivationDigest, "repositoryIdentity": observation.RepositoryIdentity,
		"canonicalRepositoryRoot": observation.CanonicalRepositoryRoot, "canonicalExecutablePath": observation.CurrentPathObject.CanonicalPath,
		"size":      observation.CurrentPathObject.Size,
		"rawSHA256": observation.CurrentPathObject.RawSHA256, "sourceHead": observation.SourceHead, "selfProfile": observation.SelfProfile,
	})
	observation.ObservationDigest = productionLocalJSONDigest(t, map[string]any{
		"schemaVersion": observation.SchemaVersion, "activationDigest": observation.ActivationDigest,
		"processId": observation.ProcessID, "processExecutablePath": observation.ProcessExecutablePath,
		"repositoryIdentity": observation.RepositoryIdentity, "canonicalRepositoryRoot": observation.CanonicalRepositoryRoot,
		"currentPathObject": observation.CurrentPathObject, "sourceHead": observation.SourceHead, "selfProfile": observation.SelfProfile,
		"observedAt": observation.ObservedAt, "status": observation.Status, "reasonCode": observation.ReasonCode,
		"identitySubjectDigest": observation.IdentitySubjectDigest,
	})
	if err := selfidentity.ValidateObservation(observation); err != nil {
		t.Fatal(err)
	}
	return observation
}

func productionLocalJSONDigest(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := canonical.DigestJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
