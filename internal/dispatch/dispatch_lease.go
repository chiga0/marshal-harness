package dispatch

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/provider"
)

// LeaseState is the closed enumeration of DispatchLease states. Matching is
// case sensitive.
type LeaseState string

// Closed states of a DispatchLease.
const (
	LeaseStateOffered   LeaseState = "offered"
	LeaseStateClaimed   LeaseState = "claimed"
	LeaseStateActive    LeaseState = "active"
	LeaseStateExpired   LeaseState = "expired"
	LeaseStateCancelled LeaseState = "cancelled"
)

// Validate rejects every value outside the closed enumeration.
func (state LeaseState) Validate() error {
	switch state {
	case LeaseStateOffered, LeaseStateClaimed, LeaseStateActive, LeaseStateExpired, LeaseStateCancelled:
		return nil
	default:
		return fmt.Errorf("dispatch: unknown leaseState %q", string(state))
	}
}

// IsTerminal reports whether the state ends the lease: expired and cancelled
// leases never return to any in-flight state.
func (state LeaseState) IsTerminal() bool {
	return state == LeaseStateExpired || state == LeaseStateCancelled
}

// CancelReason is the closed enumeration of machine-readable reasons that end
// a DispatchLease. Matching is case sensitive.
type CancelReason string

// Closed cancel reasons of a DispatchLease.
const (
	CancelReasonSecurityCriticalRevoke   CancelReason = "security-critical-revoke"
	CancelReasonRegistrationExpired      CancelReason = "registration-expired"
	CancelReasonRegistrationIncompatible CancelReason = "registration-incompatible"
	CancelReasonSnapshotSuperseded       CancelReason = "snapshot-superseded"
	CancelReasonSnapshotExpired          CancelReason = "snapshot-expired"
	CancelReasonEvidenceRevoked          CancelReason = "evidence-revoked"
	CancelReasonEvidenceExpired          CancelReason = "evidence-expired"
	CancelReasonDeadlineExceeded         CancelReason = "deadline-exceeded"
)

// Validate rejects every value outside the closed enumeration. The empty
// string is not a member; it is only legal as the absence marker on a lease
// that was not cancelled, enforced by DispatchLease.validateContent.
func (reason CancelReason) Validate() error {
	switch reason {
	case CancelReasonSecurityCriticalRevoke, CancelReasonRegistrationExpired, CancelReasonRegistrationIncompatible, CancelReasonSnapshotSuperseded, CancelReasonSnapshotExpired, CancelReasonEvidenceRevoked, CancelReasonEvidenceExpired, CancelReasonDeadlineExceeded:
		return nil
	default:
		return fmt.Errorf("dispatch: unknown cancelReason %q", string(reason))
	}
}

// DispatchLease is the dispatch-protocol lease record issued by Matcher.Claim
// (ADR 0018 §5/§6/§7, ADR 0017 §7). It binds both key spaces — the authority
// owner authority.AuthorityNamespaceId copied from the durable provider
// registration and the actor authority.SecurityDomainId carried as provenance
// only — together with the registration attestation chain, the immutable
// capability snapshot digest and the closed conformanceEvidenceDigests set.
// The leaseDigest is the canonical content digest of the record with the
// digest itself detached: any rewrite of a bound reference, digest or token
// fails validation, and cancel or expiry only ever produces a new generation
// record, never a rewrite of the original.
type DispatchLease struct {
	LeaseId                          string                         `json:"leaseId"`
	AuthorityNamespaceId             authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	SecurityDomainId                 authority.SecurityDomainId     `json:"securityDomainId"`
	RegistrationId                   string                         `json:"registrationId"`
	ProviderCapabilitySnapshotDigest string                         `json:"providerCapabilitySnapshotDigest"`
	ConformanceEvidenceDigests       []string                       `json:"conformanceEvidenceDigests"`
	Attestation                      provider.Attestation           `json:"attestation"`
	TaskId                           string                         `json:"taskId"`
	RunId                            string                         `json:"runId"`
	AttemptId                        string                         `json:"attemptId"`
	AllocationId                     string                         `json:"allocationId"`
	Generation                       int64                          `json:"generation"`
	FencingToken                     string                         `json:"fencingToken"`
	AckDeadlineAt                    string                         `json:"ackDeadlineAt"`
	ExpiresAt                        string                         `json:"expiresAt"`
	LeaseState                       LeaseState                     `json:"leaseState"`
	CancelReason                     CancelReason                   `json:"cancelReason"`
	CreatedAt                        string                         `json:"createdAt"`
	LeaseDigest                      string                         `json:"leaseDigest"`
}

// Validate fails closed on any missing or malformed field and verifies that
// leaseDigest equals the canonical content digest of the record, so a
// tampered record or a memory-only record without its canonical binding
// never validates.
func (lease DispatchLease) Validate() error {
	if err := lease.validateContent(); err != nil {
		return err
	}
	computed, err := lease.Digest()
	if err != nil {
		return err
	}
	if lease.LeaseDigest != computed {
		return fmt.Errorf("dispatch: leaseDigest does not match the canonical content digest")
	}
	return nil
}

// Digest returns the canonical content digest of the lease: RFC 8785 JCS over
// all content fields with leaseDigest detached. Identical field values always
// yield the identical digest, and member order in any transport encoding
// never changes it.
func (lease DispatchLease) Digest() (string, error) {
	if err := lease.validateContent(); err != nil {
		return "", err
	}
	detached := lease
	detached.LeaseDigest = ""
	return canonicalDigestOf(detached)
}

// Cancel produces the next-generation record carrying leaseState cancelled
// and the closed cancelReason. The receiver is never rewritten: all bound
// references, digests and deadlines stay identical, while the generation bump
// derives a fresh fencingToken and leaseDigest. Only an in-flight lease can
// be cancelled; expired and cancelled leases are terminal.
func (lease DispatchLease) Cancel(reason CancelReason) (DispatchLease, error) {
	if err := lease.Validate(); err != nil {
		return DispatchLease{}, fmt.Errorf("dispatch: cancel: %w", err)
	}
	if err := reason.Validate(); err != nil {
		return DispatchLease{}, fmt.Errorf("dispatch: cancel: %w", err)
	}
	if lease.LeaseState.IsTerminal() {
		return DispatchLease{}, fmt.Errorf("dispatch: cancel: a %s lease is terminal and can never be cancelled", string(lease.LeaseState))
	}
	next := lease
	next.LeaseState = LeaseStateCancelled
	next.CancelReason = reason
	next.Generation = lease.Generation + 1
	if err := sealLease(&next); err != nil {
		return DispatchLease{}, fmt.Errorf("dispatch: cancel: %w", err)
	}
	return next, nil
}

// Expire produces the next-generation record carrying leaseState expired
// without any cancelReason. The receiver is never rewritten. now must not be
// before the lease createdAt, and only an in-flight lease can expire;
// expired and cancelled leases are terminal.
func (lease DispatchLease) Expire(now time.Time) (DispatchLease, error) {
	if err := lease.Validate(); err != nil {
		return DispatchLease{}, fmt.Errorf("dispatch: expire: %w", err)
	}
	createdAt, err := time.Parse(time.RFC3339, lease.CreatedAt)
	if err != nil {
		return DispatchLease{}, fmt.Errorf("dispatch: expire: createdAt: %w", err)
	}
	if now.Before(createdAt) {
		return DispatchLease{}, fmt.Errorf("dispatch: expire: now is before createdAt %q", lease.CreatedAt)
	}
	if lease.LeaseState.IsTerminal() {
		return DispatchLease{}, fmt.Errorf("dispatch: expire: a %s lease is terminal and can never be expired", string(lease.LeaseState))
	}
	next := lease
	next.LeaseState = LeaseStateExpired
	next.CancelReason = ""
	next.Generation = lease.Generation + 1
	if err := sealLease(&next); err != nil {
		return DispatchLease{}, fmt.Errorf("dispatch: expire: %w", err)
	}
	return next, nil
}

// ValidateLeaseFencing is the isolated fencing adjudication point: generation
// and fencingToken must equal the current values of the lease exactly. Any
// stale or future generation, any mismatched or empty fencingToken, and any
// lease whose canonical binding no longer validates, fail closed.
func ValidateLeaseFencing(lease DispatchLease, generation int64, fencingToken string) error {
	if err := lease.Validate(); err != nil {
		return fmt.Errorf("dispatch: fencing guard rejected the lease: %w", err)
	}
	if generation != lease.Generation {
		return fmt.Errorf("dispatch: fencing guard rejected stale generation %d: the lease carries generation %d", generation, lease.Generation)
	}
	if fencingToken != lease.FencingToken {
		return fmt.Errorf("dispatch: fencing guard rejected a fencingToken that does not match the lease generation %d", lease.Generation)
	}
	return nil
}

// fencingTokenOf derives the fencing token deterministically from the lease
// content with the fencingToken and leaseDigest bindings detached: identical
// content always yields the identical token, and no random source or clock
// read participates.
func fencingTokenOf(lease DispatchLease) (string, error) {
	detached := lease
	detached.FencingToken = ""
	detached.LeaseDigest = ""
	return canonicalDigestOf(detached)
}

// sealLease derives the fencingToken first and the leaseDigest second in
// place, so the digest binds the derived token together with every content
// field.
func sealLease(lease *DispatchLease) error {
	lease.FencingToken = ""
	lease.LeaseDigest = ""
	fencingToken, err := fencingTokenOf(*lease)
	if err != nil {
		return err
	}
	lease.FencingToken = fencingToken
	digest, err := lease.Digest()
	if err != nil {
		return err
	}
	lease.LeaseDigest = digest
	return nil
}

// validateContent checks every content field except the leaseDigest binding
// itself.
func (lease DispatchLease) validateContent() error {
	if err := requireText("leaseId", lease.LeaseId); err != nil {
		return err
	}
	if err := lease.AuthorityNamespaceId.Validate(); err != nil {
		return err
	}
	if err := lease.SecurityDomainId.Validate(); err != nil {
		return err
	}
	if err := requireText("registrationId", lease.RegistrationId); err != nil {
		return err
	}
	if err := requireSHA256Digest("providerCapabilitySnapshotDigest", lease.ProviderCapabilitySnapshotDigest); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(lease.ConformanceEvidenceDigests))
	for index, digest := range lease.ConformanceEvidenceDigests {
		field := fmt.Sprintf("conformanceEvidenceDigests[%d]", index)
		if err := requireSHA256Digest(field, digest); err != nil {
			return err
		}
		if _, duplicate := seen[digest]; duplicate {
			return fmt.Errorf("dispatch: conformanceEvidenceDigests must be a closed set without duplicates")
		}
		seen[digest] = struct{}{}
	}
	if err := lease.Attestation.Validate(); err != nil {
		return err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"taskId", lease.TaskId},
		{"runId", lease.RunId},
		{"attemptId", lease.AttemptId},
		{"allocationId", lease.AllocationId},
	} {
		if err := requireText(field.name, field.value); err != nil {
			return err
		}
	}
	if lease.Generation < 1 {
		return fmt.Errorf("dispatch: generation must be a positive integer")
	}
	if err := requireSHA256Digest("fencingToken", lease.FencingToken); err != nil {
		return err
	}
	if err := requireRFC3339("ackDeadlineAt", lease.AckDeadlineAt); err != nil {
		return err
	}
	if err := requireRFC3339("expiresAt", lease.ExpiresAt); err != nil {
		return err
	}
	if err := requireRFC3339("createdAt", lease.CreatedAt); err != nil {
		return err
	}
	if err := lease.LeaseState.Validate(); err != nil {
		return err
	}
	if lease.LeaseState == LeaseStateCancelled {
		if err := lease.CancelReason.Validate(); err != nil {
			return fmt.Errorf("dispatch: a cancelled lease must carry a closed cancelReason: %w", err)
		}
		return nil
	}
	if lease.CancelReason != "" {
		return fmt.Errorf("dispatch: cancelReason must stay empty outside the cancelled leaseState")
	}
	return nil
}

// requireText fails closed on empty or blank values.
func requireText(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("dispatch: %s must be a non-empty string", field)
	}
	return nil
}

// requireSHA256Digest fails closed unless the value is a full lowercase hex
// sha256 digest with the sha256: prefix.
func requireSHA256Digest(field, value string) error {
	if err := requireText(field, value); err != nil {
		return err
	}
	if !strings.HasPrefix(value, provider.DigestPrefix) {
		return fmt.Errorf("dispatch: %s must carry the %s digest prefix", field, provider.DigestPrefix)
	}
	hexPart := strings.TrimPrefix(value, provider.DigestPrefix)
	if len(hexPart) != 64 {
		return fmt.Errorf("dispatch: %s must be a 64 character sha256 hex digest", field)
	}
	for _, r := range hexPart {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return fmt.Errorf("dispatch: %s must be lowercase hex", field)
		}
	}
	return nil
}

// requireRFC3339 fails closed unless the value parses as RFC 3339.
func requireRFC3339(field, value string) error {
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return fmt.Errorf("dispatch: %s must be an RFC 3339 timestamp", field)
	}
	return nil
}

// canonicalDigestOf marshals value, canonicalizes it under RFC 8785 JCS and
// returns the sha256 digest of the canonical bytes. Member order in the
// input never changes the digest.
func canonicalDigestOf(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("dispatch: canonical marshal: %w", err)
	}
	canonicalized, err := canonical.JSON(raw)
	if err != nil {
		return "", fmt.Errorf("dispatch: canonical digest: %w", err)
	}
	return canonical.DigestBytes(canonicalized), nil
}
