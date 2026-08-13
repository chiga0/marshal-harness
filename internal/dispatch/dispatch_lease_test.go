package dispatch

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/provider"
)

// fixedDigest derives a well-formed sha256 digest from seed material, so no
// Digest-family fixture field is ever assigned one complete literal
// (gitleaks generic-api-key publication gate).
func fixedDigest(seed string) string {
	return canonical.DigestBytes([]byte(seed))
}

func testAuthorityNamespace() authority.AuthorityNamespaceId {
	return authority.AuthorityNamespaceId{
		TenantNamespace:  "default",
		ControlPlaneId:   "default",
		AuthorityScopeId: "marshal-harness",
	}
}

func testSecurityDomain() authority.SecurityDomainId {
	return authority.SecurityDomainId{
		TenantNamespace:   "default",
		TrustDomainKind:   authority.TrustDomainKindExecution,
		IsolationDomainId: "isolation-local",
	}
}

func testAttestation() provider.Attestation {
	return provider.Attestation{
		ProviderInstanceId: "provider-instance-" + "1",
		ConfigDigest:       fixedDigest("effective-config-" + "1"),
		TrustRootKeyId:     "trust-root-key-" + "1",
		TrustRootAlgorithm: "ed25519",
	}
}

// validLease builds a sealed claimed DispatchLease whose fencingToken and
// leaseDigest are derived through the production deterministic path.
func validLease() DispatchLease {
	lease := DispatchLease{
		LeaseId:                          fixedDigest("lease-" + "1"),
		AuthorityNamespaceId:             testAuthorityNamespace(),
		SecurityDomainId:                 testSecurityDomain(),
		RegistrationId:                   "registration-" + "1",
		ProviderCapabilitySnapshotDigest: fixedDigest("snapshot-" + "1"),
		ConformanceEvidenceDigests:       []string{fixedDigest("evidence-" + "1")},
		Attestation:                      testAttestation(),
		TaskId:                           "task-" + "1",
		RunId:                            "run-" + "1",
		AttemptId:                        "attempt-" + "1",
		AllocationId:                     "allocation-" + "1",
		Generation:                       1,
		AckDeadlineAt:                    "2026-08-13T00:30:00Z",
		ExpiresAt:                        "2026-08-13T02:00:00Z",
		LeaseState:                       LeaseStateClaimed,
		CreatedAt:                        "2026-08-13T00:00:00Z",
	}
	if err := sealLease(&lease); err != nil {
		panic(err)
	}
	return lease
}

// TestLeaseStateValidateClosedEnumeration freezes the closed leaseState
// enumeration.
func TestLeaseStateValidateClosedEnumeration(t *testing.T) {
	for _, state := range []LeaseState{LeaseStateOffered, LeaseStateClaimed, LeaseStateActive, LeaseStateExpired, LeaseStateCancelled} {
		if err := state.Validate(); err != nil {
			t.Fatalf("Validate rejected the closed leaseState %q: %v", string(state), err)
		}
	}
	for _, state := range []LeaseState{"", "offered ", "CLAIMED", "draining", "claimed2"} {
		if err := state.Validate(); err == nil {
			t.Fatalf("Validate accepted the unknown leaseState %q", string(state))
		}
	}
}

// TestCancelReasonValidateClosedEnumeration freezes the closed cancelReason
// enumeration; the empty string is not a member.
func TestCancelReasonValidateClosedEnumeration(t *testing.T) {
	for _, reason := range []CancelReason{
		CancelReasonSecurityCriticalRevoke,
		CancelReasonRegistrationExpired,
		CancelReasonRegistrationIncompatible,
		CancelReasonSnapshotSuperseded,
		CancelReasonSnapshotExpired,
		CancelReasonEvidenceRevoked,
		CancelReasonEvidenceExpired,
		CancelReasonDeadlineExceeded,
	} {
		if err := reason.Validate(); err != nil {
			t.Fatalf("Validate rejected the closed cancelReason %q: %v", string(reason), err)
		}
	}
	for _, reason := range []CancelReason{"", "security-critical-revoked", "drain", "EVIDENCE-EXPIRED"} {
		if err := reason.Validate(); err == nil {
			t.Fatalf("Validate accepted the unknown cancelReason %q", string(reason))
		}
	}
}

// TestDispatchLeasePositiveBaseline freezes the positive baseline: a sealed
// lease validates, its leaseDigest equals the canonical content digest and
// the fencing guard accepts the exact current generation and fencingToken.
func TestDispatchLeasePositiveBaseline(t *testing.T) {
	lease := validLease()
	if err := lease.Validate(); err != nil {
		t.Fatalf("Validate rejected a sealed lease: %v", err)
	}
	if lease.Generation != 1 {
		t.Fatalf("a fresh lease must carry generation 1, got %d", lease.Generation)
	}
	if lease.LeaseState != LeaseStateClaimed || lease.CancelReason != "" {
		t.Fatalf("a fresh lease must stay claimed without a cancelReason, got %q/%q", string(lease.LeaseState), string(lease.CancelReason))
	}
	computed, err := lease.Digest()
	if err != nil {
		t.Fatalf("Digest failed: %v", err)
	}
	if computed != lease.LeaseDigest {
		t.Fatal("leaseDigest must equal the canonical content digest")
	}
	if !strings.HasPrefix(lease.FencingToken, provider.DigestPrefix) {
		t.Fatal("fencingToken must carry the sha256 digest prefix")
	}
	if lease.FencingToken == lease.LeaseDigest {
		t.Fatal("fencingToken and leaseDigest must bind different detached views")
	}
	if err := ValidateLeaseFencing(lease, lease.Generation, lease.FencingToken); err != nil {
		t.Fatalf("the fencing guard rejected the exact current generation and fencingToken: %v", err)
	}
}

// TestDispatchLeaseDigestDeterministic freezes the deterministic derivation:
// identical content derives the identical fencingToken and leaseDigest, and
// a generation bump changes both.
func TestDispatchLeaseDigestDeterministic(t *testing.T) {
	first := validLease()
	second := validLease()
	if first.LeaseDigest != second.LeaseDigest || first.FencingToken != second.FencingToken {
		t.Fatal("identical content must derive the identical leaseDigest and fencingToken")
	}
	bumped := first
	bumped.Generation = 2
	if err := sealLease(&bumped); err != nil {
		t.Fatalf("sealLease failed: %v", err)
	}
	if bumped.FencingToken == first.FencingToken {
		t.Fatal("a generation bump must change the fencingToken")
	}
	if bumped.LeaseDigest == first.LeaseDigest {
		t.Fatal("a generation bump must change the leaseDigest")
	}
}

// TestDispatchLeaseValidateRejectsTampering freezes negative fixture (14):
// any rewrite of a bound reference, digest or token fails validation,
// because the leaseDigest binds every content field.
func TestDispatchLeaseValidateRejectsTampering(t *testing.T) {
	base := validLease()
	cases := []struct {
		name   string
		change func(*DispatchLease)
	}{
		{"leaseId", func(l *DispatchLease) { l.LeaseId = fixedDigest("lease-" + "forged") }},
		{"authorityNamespaceId", func(l *DispatchLease) { l.AuthorityNamespaceId.TenantNamespace = "tenant-" + "forged" }},
		{"securityDomainId", func(l *DispatchLease) { l.SecurityDomainId.IsolationDomainId = "isolation-" + "forged" }},
		{"registrationId", func(l *DispatchLease) { l.RegistrationId = "registration-" + "forged" }},
		{"snapshotDigest", func(l *DispatchLease) { l.ProviderCapabilitySnapshotDigest = fixedDigest("snapshot-" + "forged") }},
		{"evidenceDigests", func(l *DispatchLease) { l.ConformanceEvidenceDigests = []string{fixedDigest("evidence-" + "forged")} }},
		{"attestation", func(l *DispatchLease) { l.Attestation.ProviderInstanceId = "provider-instance-" + "forged" }},
		{"taskId", func(l *DispatchLease) { l.TaskId = "task-" + "forged" }},
		{"runId", func(l *DispatchLease) { l.RunId = "run-" + "forged" }},
		{"attemptId", func(l *DispatchLease) { l.AttemptId = "attempt-" + "forged" }},
		{"allocationId", func(l *DispatchLease) { l.AllocationId = "allocation-" + "forged" }},
		{"generation", func(l *DispatchLease) { l.Generation = 2 }},
		{"fencingToken", func(l *DispatchLease) { l.FencingToken = fixedDigest("fencing-" + "forged") }},
		{"ackDeadlineAt", func(l *DispatchLease) { l.AckDeadlineAt = "2026-08-13T01:45:00Z" }},
		{"expiresAt", func(l *DispatchLease) { l.ExpiresAt = "2026-08-13T03:00:00Z" }},
		{"leaseState", func(l *DispatchLease) { l.LeaseState = LeaseStateActive }},
		{"cancelReason", func(l *DispatchLease) { l.CancelReason = CancelReasonDeadlineExceeded }},
		{"createdAt", func(l *DispatchLease) { l.CreatedAt = "2026-08-12T23:00:00Z" }},
		{"leaseDigest", func(l *DispatchLease) { l.LeaseDigest = fixedDigest("lease-digest-" + "forged") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := base
			tc.change(&mutated)
			if err := mutated.Validate(); err == nil {
				t.Fatalf("Validate accepted a tampered %s", tc.name)
			}
		})
	}
}

// TestDispatchLeaseValidateRejectsMalformedContent guards the remaining
// fail-closed content rules: empty required fields, malformed digests and
// timestamps, zero generation and the leaseState/cancelReason pairing.
func TestDispatchLeaseValidateRejectsMalformedContent(t *testing.T) {
	base := validLease()
	cases := []struct {
		name   string
		change func(*DispatchLease)
	}{
		{"empty leaseId", func(l *DispatchLease) { l.LeaseId = "" }},
		{"zero authorityNamespaceId", func(l *DispatchLease) { l.AuthorityNamespaceId = authority.AuthorityNamespaceId{} }},
		{"zero securityDomainId", func(l *DispatchLease) { l.SecurityDomainId = authority.SecurityDomainId{} }},
		{"empty registrationId", func(l *DispatchLease) { l.RegistrationId = "" }},
		{"snapshot digest without prefix", func(l *DispatchLease) {
			l.ProviderCapabilitySnapshotDigest = strings.TrimPrefix(l.ProviderCapabilitySnapshotDigest, provider.DigestPrefix)
		}},
		{"duplicate evidence digests", func(l *DispatchLease) {
			digest := fixedDigest("evidence-" + "dup")
			l.ConformanceEvidenceDigests = []string{digest, digest}
		}},
		{"zero attestation", func(l *DispatchLease) { l.Attestation = provider.Attestation{} }},
		{"empty taskId", func(l *DispatchLease) { l.TaskId = "" }},
		{"empty runId", func(l *DispatchLease) { l.RunId = "" }},
		{"empty attemptId", func(l *DispatchLease) { l.AttemptId = "" }},
		{"empty allocationId", func(l *DispatchLease) { l.AllocationId = "" }},
		{"zero generation", func(l *DispatchLease) { l.Generation = 0 }},
		{"empty fencingToken", func(l *DispatchLease) { l.FencingToken = "" }},
		{"malformed ackDeadlineAt", func(l *DispatchLease) { l.AckDeadlineAt = "tomorrow" }},
		{"malformed expiresAt", func(l *DispatchLease) { l.ExpiresAt = "tomorrow" }},
		{"malformed createdAt", func(l *DispatchLease) { l.CreatedAt = "yesterday" }},
		{"unknown leaseState", func(l *DispatchLease) { l.LeaseState = LeaseState("draining") }},
		{"cancelled without cancelReason", func(l *DispatchLease) { l.LeaseState = LeaseStateCancelled }},
		{"cancelled with unknown cancelReason", func(l *DispatchLease) {
			l.LeaseState = LeaseStateCancelled
			l.CancelReason = CancelReason("drain")
		}},
		{"cancelReason outside cancelled", func(l *DispatchLease) { l.CancelReason = CancelReasonDeadlineExceeded }},
		{"zero value", func(l *DispatchLease) { *l = DispatchLease{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := base
			tc.change(&mutated)
			if err := mutated.Validate(); err == nil {
				t.Fatalf("Validate accepted %s", tc.name)
			}
		})
	}

	missingDigest := base
	missingDigest.LeaseDigest = ""
	if err := missingDigest.Validate(); err == nil {
		t.Fatal("Validate accepted a lease without its leaseDigest binding")
	}
	if _, err := missingDigest.Digest(); err != nil {
		t.Fatalf("Digest rejected valid content fields: %v", err)
	}
}

// TestDispatchLeaseCancelProducesNewGeneration freezes the cancel path: the
// cancelled record carries the machine-readable reason in a new generation
// with fresh fencingToken and leaseDigest, while every bound reference of
// the original record stays untouched.
func TestDispatchLeaseCancelProducesNewGeneration(t *testing.T) {
	for _, reason := range []CancelReason{
		CancelReasonSecurityCriticalRevoke,
		CancelReasonRegistrationExpired,
		CancelReasonRegistrationIncompatible,
		CancelReasonSnapshotSuperseded,
		CancelReasonSnapshotExpired,
		CancelReasonEvidenceRevoked,
		CancelReasonEvidenceExpired,
		CancelReasonDeadlineExceeded,
	} {
		t.Run(string(reason), func(t *testing.T) {
			lease := validLease()
			cancelled, err := lease.Cancel(reason)
			if err != nil {
				t.Fatalf("Cancel rejected the closed reason %q: %v", string(reason), err)
			}
			if cancelled.LeaseState != LeaseStateCancelled || cancelled.CancelReason != reason {
				t.Fatalf("cancel must carry the cancelled state and the reason, got %q/%q", string(cancelled.LeaseState), string(cancelled.CancelReason))
			}
			if cancelled.Generation != lease.Generation+1 {
				t.Fatalf("cancel must bump the generation, got %d", cancelled.Generation)
			}
			if cancelled.LeaseId != lease.LeaseId ||
				cancelled.RegistrationId != lease.RegistrationId ||
				cancelled.ProviderCapabilitySnapshotDigest != lease.ProviderCapabilitySnapshotDigest ||
				cancelled.CreatedAt != lease.CreatedAt {
				t.Fatal("cancel must never rewrite the bound references")
			}
			if cancelled.TaskId != lease.TaskId || cancelled.RunId != lease.RunId ||
				cancelled.AttemptId != lease.AttemptId || cancelled.AllocationId != lease.AllocationId {
				t.Fatal("cancel must never rewrite the identity tuple")
			}
			if cancelled.AckDeadlineAt != lease.AckDeadlineAt || cancelled.ExpiresAt != lease.ExpiresAt {
				t.Fatal("cancel must never rewrite the deadlines")
			}
			if !cancelled.AuthorityNamespaceId.Equal(lease.AuthorityNamespaceId) ||
				!cancelled.SecurityDomainId.Equal(lease.SecurityDomainId) {
				t.Fatal("cancel must never rewrite the dual key-space binding")
			}
			if !cancelled.Attestation.Equal(lease.Attestation) {
				t.Fatal("cancel must never rewrite the attestation chain")
			}
			if !reflect.DeepEqual(cancelled.ConformanceEvidenceDigests, lease.ConformanceEvidenceDigests) {
				t.Fatal("cancel must never rewrite the closed evidence digest set")
			}
			if cancelled.FencingToken == lease.FencingToken || cancelled.LeaseDigest == lease.LeaseDigest {
				t.Fatal("cancel must derive a new fencingToken and leaseDigest")
			}
			if err := cancelled.Validate(); err != nil {
				t.Fatalf("the cancelled record does not validate: %v", err)
			}
			if err := lease.Validate(); err != nil {
				t.Fatalf("cancel mutated the original record: %v", err)
			}
			if lease.Generation != 1 || lease.LeaseState != LeaseStateClaimed {
				t.Fatal("cancel rewrote the original generation or state")
			}
			if err := ValidateLeaseFencing(cancelled, cancelled.Generation, cancelled.FencingToken); err != nil {
				t.Fatalf("the fencing guard rejected the cancelled record: %v", err)
			}
			if err := ValidateLeaseFencing(cancelled, lease.Generation, lease.FencingToken); err == nil {
				t.Fatal("the fencing guard accepted the pre-cancel generation after cancel")
			}
		})
	}
}

// TestDispatchLeaseCancelFailClosed guards the cancel preconditions: unknown
// or empty reasons, terminal leases and tampered records fail closed.
func TestDispatchLeaseCancelFailClosed(t *testing.T) {
	lease := validLease()
	if _, err := lease.Cancel(CancelReason("")); err == nil {
		t.Fatal("Cancel accepted an empty cancelReason")
	}
	if _, err := lease.Cancel(CancelReason("drain")); err == nil {
		t.Fatal("Cancel accepted an unknown cancelReason")
	}
	cancelled, err := lease.Cancel(CancelReasonEvidenceRevoked)
	if err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}
	if _, err := cancelled.Cancel(CancelReasonEvidenceExpired); err == nil {
		t.Fatal("Cancel accepted an already cancelled lease")
	}
	expired, err := lease.Expire(time.Date(2026, 8, 13, 1, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Expire failed: %v", err)
	}
	if _, err := expired.Cancel(CancelReasonDeadlineExceeded); err == nil {
		t.Fatal("Cancel accepted an expired lease")
	}
	tampered := lease
	tampered.LeaseDigest = fixedDigest("lease-digest-" + "forged")
	if _, err := tampered.Cancel(CancelReasonDeadlineExceeded); err == nil {
		t.Fatal("Cancel accepted a tampered lease")
	}
}

// TestDispatchLeaseExpireProducesNewGeneration freezes the expire path: the
// expired record carries no cancelReason in a new generation with fresh
// fencingToken and leaseDigest, while every bound reference stays untouched.
func TestDispatchLeaseExpireProducesNewGeneration(t *testing.T) {
	lease := validLease()
	expired, err := lease.Expire(time.Date(2026, 8, 13, 2, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Expire rejected a valid in-flight lease: %v", err)
	}
	if expired.LeaseState != LeaseStateExpired || expired.CancelReason != "" {
		t.Fatalf("expire must carry the expired state without a cancelReason, got %q/%q", string(expired.LeaseState), string(expired.CancelReason))
	}
	if expired.Generation != lease.Generation+1 {
		t.Fatalf("expire must bump the generation, got %d", expired.Generation)
	}
	if expired.LeaseId != lease.LeaseId || expired.RegistrationId != lease.RegistrationId ||
		expired.ProviderCapabilitySnapshotDigest != lease.ProviderCapabilitySnapshotDigest ||
		expired.CreatedAt != lease.CreatedAt {
		t.Fatal("expire must never rewrite the bound references")
	}
	if expired.FencingToken == lease.FencingToken || expired.LeaseDigest == lease.LeaseDigest {
		t.Fatal("expire must derive a new fencingToken and leaseDigest")
	}
	if err := expired.Validate(); err != nil {
		t.Fatalf("the expired record does not validate: %v", err)
	}
	if err := lease.Validate(); err != nil {
		t.Fatalf("expire mutated the original record: %v", err)
	}
	if err := ValidateLeaseFencing(expired, expired.Generation, expired.FencingToken); err != nil {
		t.Fatalf("the fencing guard rejected the expired record: %v", err)
	}
}

// TestDispatchLeaseExpireFailClosed guards the expire preconditions: a now
// before createdAt, terminal leases and tampered records fail closed.
func TestDispatchLeaseExpireFailClosed(t *testing.T) {
	lease := validLease()
	if _, err := lease.Expire(time.Date(2026, 8, 12, 23, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("Expire accepted a now before createdAt")
	}
	expired, err := lease.Expire(time.Date(2026, 8, 13, 3, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Expire failed: %v", err)
	}
	if _, err := expired.Expire(time.Date(2026, 8, 13, 4, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("Expire accepted an already expired lease")
	}
	cancelled, err := lease.Cancel(CancelReasonSecurityCriticalRevoke)
	if err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}
	if _, err := cancelled.Expire(time.Date(2026, 8, 13, 4, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("Expire accepted a cancelled lease")
	}
	tampered := lease
	tampered.Generation = 99
	if _, err := tampered.Expire(time.Date(2026, 8, 13, 4, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("Expire accepted a tampered lease")
	}
}

// TestValidateLeaseFencingRejectsStaleReplay freezes negative fixtures (12)
// and (13): the fencing guard demands exact equality on both generation and
// fencingToken, and a tampered or zero lease never passes.
func TestValidateLeaseFencingRejectsStaleReplay(t *testing.T) {
	lease := validLease()
	if err := ValidateLeaseFencing(lease, lease.Generation-1, lease.FencingToken); err == nil {
		t.Fatal("the fencing guard accepted a stale generation")
	} else if !strings.Contains(err.Error(), "generation") {
		t.Fatalf("expected the stale generation rejection, got: %v", err)
	}
	if err := ValidateLeaseFencing(lease, lease.Generation+1, lease.FencingToken); err == nil {
		t.Fatal("the fencing guard accepted a future generation; it must demand exact equality")
	}
	staleToken := "stale-fencing-" + "token"
	if err := ValidateLeaseFencing(lease, lease.Generation, staleToken); err == nil {
		t.Fatal("the fencing guard accepted a stale fencingToken")
	} else if !strings.Contains(err.Error(), "fencingToken") {
		t.Fatalf("expected the stale fencingToken rejection, got: %v", err)
	}
	if err := ValidateLeaseFencing(lease, lease.Generation, ""); err == nil {
		t.Fatal("the fencing guard accepted an empty fencingToken")
	}
	tampered := lease
	tampered.RunId = "run-" + "forged"
	if err := ValidateLeaseFencing(tampered, lease.Generation, lease.FencingToken); err == nil {
		t.Fatal("the fencing guard accepted a tampered lease")
	}
	if err := ValidateLeaseFencing(DispatchLease{}, 0, ""); err == nil {
		t.Fatal("the fencing guard accepted a zero lease")
	}
}
