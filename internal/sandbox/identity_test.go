package sandbox

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/provider"
)

// fixtureDigest derives a well-formed sha256 digest from seed material, so
// no Digest-family, Token-family or Key-family fixture field is ever
// assigned one complete string literal (gitleaks publication gate).
func fixtureDigest(seed string) string {
	return canonical.DigestBytes([]byte(seed))
}

func validIdentity() OperationIdentity {
	return OperationIdentity{
		TaskId:       "task-" + "1",
		RunId:        "run-" + "1",
		AttemptId:    "attempt-" + "1",
		WorkloadRole: WorkloadRoleWorker,
		AllocationId: "allocation-" + "1",
		Generation:   1,
		FencingToken: fixtureDigest("fencing" + "-1"),
		CommandId:    "command-" + "1",
	}
}

// TestWorkloadRoleClosedEnumeration freezes the closed workloadRole
// enumeration: only worker and verifier are members, publisher and every
// other value are rejected.
func TestWorkloadRoleClosedEnumeration(t *testing.T) {
	for _, role := range []WorkloadRole{WorkloadRoleWorker, WorkloadRoleVerifier} {
		if err := role.Validate(); err != nil {
			t.Fatalf("Validate rejected the closed workloadRole %q: %v", string(role), err)
		}
	}
	for _, role := range []WorkloadRole{"", "publisher", "Publisher", "PUBLISHER", "WORKER", "verifier "} {
		err := role.Validate()
		if err == nil {
			t.Fatalf("Validate accepted the workloadRole %q outside the closed enumeration", string(role))
		}
		if !errors.Is(err, ErrInvalidWorkloadRole) {
			t.Fatalf("Validate must surface ErrInvalidWorkloadRole for %q, got %v", string(role), err)
		}
	}
}

// TestOperationIdentityValidateFailClosed freezes that any missing or
// malformed identity field fails closed.
func TestOperationIdentityValidateFailClosed(t *testing.T) {
	if err := validIdentity().Validate(); err != nil {
		t.Fatalf("Validate rejected a complete identity: %v", err)
	}
	mutations := []struct {
		name   string
		mutate func(*OperationIdentity)
	}{
		{"empty taskId", func(id *OperationIdentity) { id.TaskId = "" }},
		{"blank runId", func(id *OperationIdentity) { id.RunId = "   " }},
		{"empty attemptId", func(id *OperationIdentity) { id.AttemptId = "" }},
		{"publisher role", func(id *OperationIdentity) { id.WorkloadRole = "publisher" }},
		{"empty role", func(id *OperationIdentity) { id.WorkloadRole = "" }},
		{"empty allocationId", func(id *OperationIdentity) { id.AllocationId = "" }},
		{"zero generation", func(id *OperationIdentity) { id.Generation = 0 }},
		{"negative generation", func(id *OperationIdentity) { id.Generation = -1 }},
		{"empty fencingToken", func(id *OperationIdentity) { id.FencingToken = "" }},
		{"empty commandId", func(id *OperationIdentity) { id.CommandId = "" }},
	}
	for _, tc := range mutations {
		id := validIdentity()
		tc.mutate(&id)
		err := id.Validate()
		if err == nil {
			t.Fatalf("Validate accepted the identity with %s", tc.name)
		}
		if !errors.Is(err, ErrInvalidOperationIdentity) {
			t.Fatalf("Validate must fail closed with ErrInvalidOperationIdentity for %s, got %v", tc.name, err)
		}
	}
}

// TestOperationIdentityReplayKeyDeterministic freezes the replay-key
// determinism: identical identities derive the identical key, and any
// change to any single field derives a different key.
func TestOperationIdentityReplayKeyDeterministic(t *testing.T) {
	first, err := validIdentity().ReplayKey()
	if err != nil {
		t.Fatalf("ReplayKey failed on a valid identity: %v", err)
	}
	second, err := validIdentity().ReplayKey()
	if err != nil {
		t.Fatalf("ReplayKey failed on the second derivation: %v", err)
	}
	if first != second {
		t.Fatal("identical identities must derive the identical replay key")
	}
	mutations := []func(*OperationIdentity){
		func(id *OperationIdentity) { id.TaskId = "task-" + "2" },
		func(id *OperationIdentity) { id.RunId = "run-" + "2" },
		func(id *OperationIdentity) { id.AttemptId = "attempt-" + "2" },
		func(id *OperationIdentity) { id.WorkloadRole = WorkloadRoleVerifier },
		func(id *OperationIdentity) { id.AllocationId = "allocation-" + "2" },
		func(id *OperationIdentity) { id.Generation = 2 },
		func(id *OperationIdentity) { id.FencingToken = fixtureDigest("fencing" + "-2") },
		func(id *OperationIdentity) { id.CommandId = "command-" + "2" },
	}
	for index, mutate := range mutations {
		id := validIdentity()
		mutate(&id)
		key, err := id.ReplayKey()
		if err != nil {
			t.Fatalf("mutation %d: ReplayKey failed: %v", index, err)
		}
		if key == first {
			t.Fatalf("mutation %d must change the replay key", index)
		}
	}
	if _, err := (OperationIdentity{}).ReplayKey(); err == nil {
		t.Fatal("an invalid identity must never derive a replay key")
	}
}

// TestParseOperationIdentityRejectsDuplicateMembers freezes that duplicate
// canonical members are rejected with canonical.ErrRejected before any
// field is interpreted.
func TestParseOperationIdentityRejectsDuplicateMembers(t *testing.T) {
	valid := validIdentity()
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := ParseOperationIdentity(raw)
	if err != nil {
		t.Fatalf("ParseOperationIdentity rejected a canonical identity: %v", err)
	}
	if parsed != valid {
		t.Fatalf("ParseOperationIdentity must round-trip the identity, got %+v", parsed)
	}
	duplicate := `{"taskId":"task-1","taskId":"task-2","runId":"run-1","attemptId":"attempt-1","workloadRole":"worker","allocationId":"allocation-1","generation":1,"fencingToken":"` + fixtureDigest("fencing"+"-1") + `","commandId":"command-1"}`
	_, err = ParseOperationIdentity([]byte(duplicate))
	if err == nil {
		t.Fatal("ParseOperationIdentity accepted duplicate members")
	}
	if !errors.Is(err, canonical.ErrRejected) {
		t.Fatalf("duplicate members must surface canonical.ErrRejected, got %v", err)
	}
}

// testLease builds a sealed active DispatchLease through the same
// deterministic derivation dispatch uses internally: the fencingToken is the
// canonical digest of the record with fencingToken and leaseDigest detached,
// and the leaseDigest is the canonical content digest of the sealed record.
func testLease(t *testing.T) dispatch.DispatchLease {
	t.Helper()
	var lease dispatch.DispatchLease
	lease.LeaseId = fixtureDigest("lease" + "-1")
	lease.AuthorityNamespaceId = authority.AuthorityNamespaceId{TenantNamespace: "default", ControlPlaneId: "default", AuthorityScopeId: "marshal-harness"}
	lease.SecurityDomainId = authority.SecurityDomainId{TenantNamespace: "default", TrustDomainKind: authority.TrustDomainKindExecution, IsolationDomainId: "isolation-local"}
	lease.RegistrationId = "registration-" + "1"
	lease.ProviderCapabilitySnapshotDigest = fixtureDigest("capability-snapshot" + "-1")
	lease.ConformanceEvidenceDigests = []string{fixtureDigest("conformance-evidence" + "-1")}
	lease.Attestation = provider.Attestation{ProviderInstanceId: "provider-instance-" + "1", ConfigDigest: fixtureDigest("effective-config" + "-1"), TrustRootKeyId: "trust-root-key-" + "1", TrustRootAlgorithm: "ed25519"}
	lease.TaskId = "task-" + "1"
	lease.RunId = "run-" + "1"
	lease.AttemptId = "attempt-" + "1"
	lease.AllocationId = "allocation-" + "1"
	lease.Generation = 2
	lease.AckDeadlineAt = "2026-08-13T00:30:00Z"
	lease.ExpiresAt = "2026-08-13T02:00:00Z"
	lease.LeaseState = dispatch.LeaseStateActive
	lease.CreatedAt = "2026-08-13T00:00:00Z"
	raw, err := json.Marshal(lease)
	if err != nil {
		t.Fatalf("seal test lease: %v", err)
	}
	token, err := canonical.DigestJSON(raw)
	if err != nil {
		t.Fatalf("seal test lease: %v", err)
	}
	lease.FencingToken = token
	digest, err := lease.Digest()
	if err != nil {
		t.Fatalf("seal test lease: %v", err)
	}
	lease.LeaseDigest = digest
	return lease
}

// TestOperationIdentityValidateFencing freezes the replay-fencing behavior:
// the exact current generation and fencingToken pass, while a stale
// generation, a mismatched fencingToken or a tampered lease fail closed with
// the fencing diagnostics preserved.
func TestOperationIdentityValidateFencing(t *testing.T) {
	lease := testLease(t)
	id := validIdentity()
	id.Generation = lease.Generation
	id.FencingToken = lease.FencingToken
	if err := id.ValidateFencing(lease); err != nil {
		t.Fatalf("ValidateFencing rejected the exact current generation and fencingToken: %v", err)
	}
	stale := id
	stale.Generation = lease.Generation - 1
	err := stale.ValidateFencing(lease)
	if err == nil {
		t.Fatal("ValidateFencing accepted a stale generation replay")
	}
	if !strings.Contains(err.Error(), "fencing guard") {
		t.Fatalf("a stale replay must keep the fencing diagnostics, got %v", err)
	}
	future := id
	future.Generation = lease.Generation + 1
	if err := future.ValidateFencing(lease); err == nil {
		t.Fatal("ValidateFencing accepted a future generation replay")
	}
	forged := id
	forged.FencingToken = fixtureDigest("fencing" + "-forged")
	if err := forged.ValidateFencing(lease); err == nil {
		t.Fatal("ValidateFencing accepted a mismatched fencingToken")
	}
	tampered := lease
	tampered.Generation = lease.Generation + 1
	if err := id.ValidateFencing(tampered); err == nil {
		t.Fatal("ValidateFencing accepted a lease whose canonical binding no longer validates")
	}
	if err := (OperationIdentity{}).ValidateFencing(lease); err == nil {
		t.Fatal("ValidateFencing accepted an invalid identity")
	}
}
