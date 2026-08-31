package dispatch

import (
	"fmt"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/provider"
)

// MintLeaseInput carries the exact claimed-lease content. The caller owns the
// identity decisions (registration, capability snapshot, attestation); this
// constructor only derives the deterministic seal fields (fencing token,
// digest) and the time windows, then fails closed unless the sealed lease
// passes the full production validation.
type MintLeaseInput struct {
	LeaseId                          string
	AuthorityNamespaceId             authority.AuthorityNamespaceId
	SecurityDomainId                 authority.SecurityDomainId
	RegistrationId                   string
	ProviderCapabilitySnapshotDigest string
	ConformanceEvidenceDigests       []string
	Attestation                      provider.Attestation
	TaskId                           string
	RunId                            string
	AttemptId                        string
	AllocationId                     string
	Generation                       int64
	// Now anchors CreatedAt; AckDeadline is Now+AckDelay and ExpiresAt is
	// Now+Lifetime. Both must stay positive so every window is future-bound.
	Now      time.Time
	AckDelay time.Duration
	Lifetime time.Duration
}

// MintClaimedLease is the single production entry point for creating a
// claimed DispatchLease. It seals the lease through the same deterministic
// path as replay and rejects any content the production validation would
// refuse, so a minted lease never needs post-mint repair.
func MintClaimedLease(input MintLeaseInput) (DispatchLease, error) {
	if input.Now.IsZero() || input.AckDelay <= 0 || input.Lifetime <= 0 || input.AckDelay >= input.Lifetime {
		return DispatchLease{}, fmt.Errorf("dispatch: mint requires a positive future-bounded time window")
	}
	lease := DispatchLease{
		LeaseId:                          input.LeaseId,
		AuthorityNamespaceId:             input.AuthorityNamespaceId,
		SecurityDomainId:                 input.SecurityDomainId,
		RegistrationId:                   input.RegistrationId,
		ProviderCapabilitySnapshotDigest: input.ProviderCapabilitySnapshotDigest,
		ConformanceEvidenceDigests:       append([]string(nil), input.ConformanceEvidenceDigests...),
		Attestation:                      input.Attestation,
		TaskId:                           input.TaskId,
		RunId:                            input.RunId,
		AttemptId:                        input.AttemptId,
		AllocationId:                     input.AllocationId,
		Generation:                       input.Generation,
		AckDeadlineAt:                    input.Now.UTC().Add(input.AckDelay).Format(time.RFC3339Nano),
		ExpiresAt:                        input.Now.UTC().Add(input.Lifetime).Format(time.RFC3339Nano),
		LeaseState:                       LeaseStateClaimed,
		CreatedAt:                        input.Now.UTC().Format(time.RFC3339Nano),
	}
	if err := sealLease(&lease); err != nil {
		return DispatchLease{}, err
	}
	if err := lease.Validate(); err != nil {
		return DispatchLease{}, err
	}
	return lease, nil
}
