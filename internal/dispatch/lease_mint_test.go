package dispatch

import (
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/provider"
)

func TestMintClaimedLeaseProducesValidSealedLease(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	lease, err := MintClaimedLease(MintLeaseInput{
		LeaseId:                          strings.Repeat("a", 64),
		AuthorityNamespaceId:             testAuthorityNamespace(),
		SecurityDomainId:                 testSecurityDomain(),
		RegistrationId:                   "registration-1",
		ProviderCapabilitySnapshotDigest: fixedDigest("snapshot-1"),
		ConformanceEvidenceDigests:       []string{},
		Attestation:                      testAttestation(),
		TaskId:                           "task-1",
		RunId:                            "run-1",
		AttemptId:                        "attempt-1",
		AllocationId:                     "allocation-1",
		Generation:                       1,
		Now:                              now,
		AckDelay:                         15 * time.Minute,
		Lifetime:                         2 * time.Hour,
	})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if lease.LeaseState != LeaseStateClaimed {
		t.Fatalf("minted state=%q", lease.LeaseState)
	}
	if lease.CreatedAt != "2026-08-30T12:00:00Z" {
		t.Fatalf("createdAt=%q", lease.CreatedAt)
	}
	if lease.AckDeadlineAt != "2026-08-30T12:15:00Z" || lease.ExpiresAt != "2026-08-30T14:00:00Z" {
		t.Fatalf("windows ack=%q expires=%q", lease.AckDeadlineAt, lease.ExpiresAt)
	}
	// The minted lease must satisfy the production guard paths verbatim.
	if err := lease.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := ValidateLeaseFencing(lease, 1, lease.FencingToken); err != nil {
		t.Fatalf("fencing: %v", err)
	}
}

func TestMintClaimedLeaseRejectsInvalidContentAndWindows(t *testing.T) {
	base := MintLeaseInput{
		AuthorityNamespaceId: testAuthorityNamespace(),
		SecurityDomainId:     testSecurityDomain(),
		RegistrationId:       "registration-1",
		TaskId:               "task-1",
		RunId:                "run-1",
		AttemptId:            "attempt-1",
		AllocationId:         "allocation-1",
		Generation:           1,
		Now:                  time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
		AckDelay:             15 * time.Minute,
		Lifetime:             2 * time.Hour,
	}
	if _, err := MintClaimedLease(base); err == nil {
		t.Fatal("empty lease id accepted")
	}
	base.LeaseId = strings.Repeat("a", 64)
	inverted := base
	inverted.AckDelay, inverted.Lifetime = 2*time.Hour, 15*time.Minute
	if _, err := MintClaimedLease(inverted); err == nil {
		t.Fatal("inverted time window accepted")
	}
	zero := base
	zero.Now = time.Time{}
	if _, err := MintClaimedLease(zero); err == nil {
		t.Fatal("zero clock accepted")
	}
	noAttestation := base
	noAttestation.Attestation = provider.Attestation{}
	if _, err := MintClaimedLease(noAttestation); err == nil {
		t.Fatal("missing attestation accepted")
	}
	if _, err := MintClaimedLease(base); err == nil {
		t.Fatal("missing capability snapshot digest accepted")
	}
}

var _ = authority.AuthorityNamespaceId{}
