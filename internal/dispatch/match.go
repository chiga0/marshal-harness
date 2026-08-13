package dispatch

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/provider"
)

// Capability keys that a ProviderCapabilitySnapshot must declare in its
// Capabilities map for dispatch match. A missing declaration or a value
// outside the closed domain enumerations fails closed.
const (
	capabilityKeyAccessMode            = "accessMode"
	capabilityKeyMinimumAssuranceLevel = "minimumAssuranceLevel"
)

// Matcher adjudicates capability match, claim and current-ledger recheck
// against the durable gate-4 registration ledger. Push and Pull dispatch
// topologies are expressed through the identical topology-agnostic
// Claim/Revalidate semantics; no transport participates.
type Matcher struct {
	store *provider.RegistrationStore

	// mu guards issuedLeases.
	mu sync.Mutex
	// issuedLeases is the in-memory unique-claim index keyed by
	// (runId, attemptId): the single-allocation invariant never reissues the
	// identical attempt, neither while its lease is active nor after that
	// lease lost eligibility; continuation requires a new attempt with a new
	// claim. Persisting this ledger is M9.
	issuedLeases map[string]string
}

// NewMatcher binds a Matcher to store, which must already be bound to a
// durable ledger directory. A nil or zero-value store keeps the Matcher
// constructible, but every Claim and Revalidate fails closed.
func NewMatcher(store *provider.RegistrationStore) *Matcher {
	return &Matcher{
		store:        store,
		issuedLeases: map[string]string{},
	}
}

// Match adjudicates whether the persisted snapshot and the closed evidence
// set satisfy requirements for the registered provider. The order is frozen:
// the gate-5 provider.EvaluateProviderEligibility combination is the single
// eligibility entry point; then the capabilities map must satisfy the
// requirements declarations fail closed; and when requirements demand the
// hardened assurance level every evidence must pass
// provider.IsHardenedEligible — never a silent downgrade to workspace-write.
func (m *Matcher) Match(registration provider.ProviderRegistration, snapshot provider.ProviderCapabilitySnapshot, evidences []provider.ConformanceEvidence, requirements domain.SandboxRequirements, now time.Time) error {
	if err := provider.EvaluateProviderEligibility(registration, snapshot, evidences, now); err != nil {
		return fmt.Errorf("dispatch: match: eligibility: %w", err)
	}
	if err := matchCapabilities(snapshot, requirements); err != nil {
		return fmt.Errorf("dispatch: match: %w", err)
	}
	if requirements.MinimumAssuranceLevel == domain.AssuranceLevelHardened {
		if len(evidences) == 0 {
			return fmt.Errorf("dispatch: match: hardened requirements demand conformance evidence; fail closed without downgrade to workspace-write")
		}
		for index, evidence := range evidences {
			if !provider.IsHardenedEligible(evidence, now) {
				return fmt.Errorf("dispatch: match: evidences[%d] is not hardened eligible; fail closed without downgrade to workspace-write", index)
			}
		}
	}
	return nil
}

// ClaimRequest carries one claim against the durable registration ledger.
type ClaimRequest struct {
	AuthorityNamespaceId authority.AuthorityNamespaceId      `json:"authorityNamespaceId"`
	RegistrationId       string                              `json:"registrationId"`
	Snapshot             provider.ProviderCapabilitySnapshot `json:"snapshot"`
	Evidences            []provider.ConformanceEvidence      `json:"evidences"`
	Requirements         domain.SandboxRequirements          `json:"requirements"`
	TaskId               string                              `json:"taskId"`
	RunId                string                              `json:"runId"`
	AttemptId            string                              `json:"attemptId"`
	AllocationId         string                              `json:"allocationId"`
	AckDeadlineAt        string                              `json:"ackDeadlineAt"`
	ExpiresAt            string                              `json:"expiresAt"`
}

// Claim issues one DispatchLease against the durable ledger. Every
// precondition fails closed in order and the error names the failing stage:
// the durable store binding, the registration lookup, the active lifecycle
// and identity alignment of the stored registration, the gate-5 match, the
// identity tuple and deadline fields, and the unique claim invariant. The
// issued lease binds the authorityNamespaceId and securityDomainId of the
// stored registration, copies the registration attestation and the closed
// conformanceEvidenceDigests set of the snapshot, starts at generation 1 and
// derives fencingToken and leaseDigest deterministically from the canonical
// content: no random source and no clock read beyond the injected now.
func (m *Matcher) Claim(request ClaimRequest, now time.Time) (DispatchLease, error) {
	if m == nil || m.store == nil {
		return DispatchLease{}, fmt.Errorf("dispatch: claim precondition: the registration store is not bound to a durable ledger directory: %w", provider.ErrMemoryOnlyRegistration)
	}
	stored, err := m.store.Get(request.RegistrationId)
	if err != nil {
		if errors.Is(err, provider.ErrMemoryOnlyRegistration) {
			return DispatchLease{}, fmt.Errorf("dispatch: claim precondition: the registration store is not bound to a durable ledger directory: %w", err)
		}
		return DispatchLease{}, fmt.Errorf("dispatch: claim precondition: registration lookup: %w", err)
	}
	if stored.LifecycleState != provider.LifecycleStateActive {
		return DispatchLease{}, fmt.Errorf("dispatch: claim precondition: stored registration carries lifecycleState %q, only %q can be claimed", string(stored.LifecycleState), string(provider.LifecycleStateActive))
	}
	if !request.AuthorityNamespaceId.Equal(stored.AuthorityNamespaceId) {
		return DispatchLease{}, fmt.Errorf("dispatch: claim precondition: request authorityNamespaceId does not align with the stored registration owner")
	}
	if request.Snapshot.RegistrationId != stored.RegistrationId {
		return DispatchLease{}, fmt.Errorf("dispatch: claim precondition: snapshot registrationId does not reference the stored registration")
	}
	if !request.Snapshot.Attestation.Equal(stored.Attestation) {
		return DispatchLease{}, fmt.Errorf("dispatch: claim precondition: snapshot attestation does not align with the stored registration attestation")
	}
	if err := m.Match(stored, request.Snapshot, request.Evidences, request.Requirements, now); err != nil {
		return DispatchLease{}, fmt.Errorf("dispatch: claim: %w", err)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"taskId", request.TaskId},
		{"runId", request.RunId},
		{"attemptId", request.AttemptId},
		{"allocationId", request.AllocationId},
	} {
		if err := requireText(field.name, field.value); err != nil {
			return DispatchLease{}, err
		}
	}
	if err := requireRFC3339("ackDeadlineAt", request.AckDeadlineAt); err != nil {
		return DispatchLease{}, err
	}
	if err := requireRFC3339("expiresAt", request.ExpiresAt); err != nil {
		return DispatchLease{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.issuedLeases == nil {
		m.issuedLeases = map[string]string{}
	}
	claimKey := request.RunId + "\x00" + request.AttemptId
	if existing, taken := m.issuedLeases[claimKey]; taken {
		return DispatchLease{}, fmt.Errorf("dispatch: claim rejected: (runId, attemptId) already carries lease %s; the single-allocation invariant never reissues the identical attempt, continue with a new attempt and a new claim", existing)
	}

	evidenceDigests := make([]string, len(request.Snapshot.ConformanceEvidenceDigests))
	copy(evidenceDigests, request.Snapshot.ConformanceEvidenceDigests)
	lease := DispatchLease{
		AuthorityNamespaceId:             stored.AuthorityNamespaceId,
		SecurityDomainId:                 stored.SecurityDomainId,
		RegistrationId:                   stored.RegistrationId,
		ProviderCapabilitySnapshotDigest: request.Snapshot.ProviderCapabilitySnapshotDigest,
		ConformanceEvidenceDigests:       evidenceDigests,
		Attestation:                      stored.Attestation,
		TaskId:                           request.TaskId,
		RunId:                            request.RunId,
		AttemptId:                        request.AttemptId,
		AllocationId:                     request.AllocationId,
		Generation:                       1,
		AckDeadlineAt:                    request.AckDeadlineAt,
		ExpiresAt:                        request.ExpiresAt,
		LeaseState:                       LeaseStateClaimed,
		CreatedAt:                        now.UTC().Format(time.RFC3339),
	}
	binding := struct {
		RegistrationId                   string `json:"registrationId"`
		ProviderCapabilitySnapshotDigest string `json:"providerCapabilitySnapshotDigest"`
		TaskId                           string `json:"taskId"`
		RunId                            string `json:"runId"`
		AttemptId                        string `json:"attemptId"`
		AllocationId                     string `json:"allocationId"`
		Generation                       int64  `json:"generation"`
	}{
		RegistrationId:                   stored.RegistrationId,
		ProviderCapabilitySnapshotDigest: request.Snapshot.ProviderCapabilitySnapshotDigest,
		TaskId:                           request.TaskId,
		RunId:                            request.RunId,
		AttemptId:                        request.AttemptId,
		AllocationId:                     request.AllocationId,
		Generation:                       1,
	}
	leaseId, err := canonicalDigestOf(binding)
	if err != nil {
		return DispatchLease{}, fmt.Errorf("dispatch: claim: leaseId derivation: %w", err)
	}
	lease.LeaseId = leaseId
	if err := sealLease(&lease); err != nil {
		return DispatchLease{}, fmt.Errorf("dispatch: claim: %w", err)
	}
	m.issuedLeases[claimKey] = lease.LeaseId
	return lease, nil
}

// Revalidate is the current-ledger recheck of an in-flight lease: it
// re-reads the durable ledger, verifies the lease bindings against the
// current snapshot and evidence set, and re-adjudicates eligibility. Any
// invalidation fails closed with the machine-readable CancelReason the
// caller must carry into Cancel plus a generation bump; continuation then
// requires a new attempt with a new claim, never an in-place renewal or a
// downgrade reuse.
func (m *Matcher) Revalidate(lease DispatchLease, snapshot provider.ProviderCapabilitySnapshot, evidences []provider.ConformanceEvidence, requirements domain.SandboxRequirements, now time.Time) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("dispatch: revalidate precondition: the registration store is not bound to a durable ledger directory: %w", provider.ErrMemoryOnlyRegistration)
	}
	if err := lease.Validate(); err != nil {
		return fmt.Errorf("dispatch: revalidate: lease rejected: %w", err)
	}
	if lease.LeaseState != LeaseStateClaimed && lease.LeaseState != LeaseStateActive {
		return fmt.Errorf("dispatch: revalidate: only an in-flight lease can be revalidated, got leaseState %q", string(lease.LeaseState))
	}
	stored, err := m.store.Get(lease.RegistrationId)
	if err != nil {
		return fmt.Errorf("dispatch: revalidate: current-ledger recheck: %w", err)
	}
	switch stored.LifecycleState {
	case provider.LifecycleStateRevoked:
		return cancelError(CancelReasonSecurityCriticalRevoke, fmt.Sprintf("registration %q was revoked after the claim; the lease loses eligibility immediately", lease.RegistrationId))
	case provider.LifecycleStateExpired:
		return cancelError(CancelReasonRegistrationExpired, fmt.Sprintf("registration %q expired after the claim", lease.RegistrationId))
	case provider.LifecycleStateActive:
	default:
		return cancelError(CancelReasonRegistrationIncompatible, fmt.Sprintf("registration %q carries lifecycleState %q", lease.RegistrationId, string(stored.LifecycleState)))
	}
	expiresAt, err := time.Parse(time.RFC3339, lease.ExpiresAt)
	if err != nil {
		return fmt.Errorf("dispatch: revalidate: expiresAt: %w", err)
	}
	if !now.Before(expiresAt) {
		return cancelError(CancelReasonDeadlineExceeded, fmt.Sprintf("now is not before expiresAt %q; the lease cannot be renewed in place", lease.ExpiresAt))
	}
	ackDeadlineAt, err := time.Parse(time.RFC3339, lease.AckDeadlineAt)
	if err != nil {
		return fmt.Errorf("dispatch: revalidate: ackDeadlineAt: %w", err)
	}
	if !now.Before(ackDeadlineAt) {
		return cancelError(CancelReasonDeadlineExceeded, fmt.Sprintf("now is not before ackDeadlineAt %q", lease.AckDeadlineAt))
	}
	if snapshot.RegistrationId != stored.RegistrationId ||
		snapshot.ProtocolVersion != stored.ProtocolVersion ||
		snapshot.ProviderType != stored.ProviderType ||
		snapshot.ProviderName != stored.ProviderName ||
		snapshot.ProviderVersion != stored.ProviderVersion ||
		snapshot.Scope != stored.Scope ||
		!snapshot.Attestation.Equal(stored.Attestation) {
		return cancelError(CancelReasonRegistrationIncompatible, "the current snapshot no longer aligns with the stored registration identity")
	}
	if !lease.AuthorityNamespaceId.Equal(stored.AuthorityNamespaceId) ||
		!lease.SecurityDomainId.Equal(stored.SecurityDomainId) ||
		!lease.Attestation.Equal(stored.Attestation) {
		return cancelError(CancelReasonRegistrationIncompatible, "the lease key space or attestation binding no longer matches the stored registration")
	}
	switch snapshot.SnapshotState {
	case provider.SnapshotStateActive:
	case provider.SnapshotStateSuperseded:
		return cancelError(CancelReasonSnapshotSuperseded, "the current snapshot is superseded")
	case provider.SnapshotStateExpired:
		return cancelError(CancelReasonSnapshotExpired, "the current snapshot is expired")
	default:
		return cancelError(CancelReasonSnapshotSuperseded, fmt.Sprintf("the current snapshot carries unknown snapshotState %q", string(snapshot.SnapshotState)))
	}
	currentDigest, err := snapshot.Digest()
	if err != nil {
		return cancelError(CancelReasonSnapshotSuperseded, fmt.Sprintf("the current snapshot no longer validates: %v", err))
	}
	if lease.ProviderCapabilitySnapshotDigest != currentDigest {
		return cancelError(CancelReasonSnapshotSuperseded, "the lease binds a different providerCapabilitySnapshotDigest than the current snapshot")
	}
	if !digestSetsEqual(lease.ConformanceEvidenceDigests, snapshot.ConformanceEvidenceDigests) {
		return cancelError(CancelReasonSnapshotSuperseded, "the closed conformanceEvidenceDigests set bound by the lease changed")
	}
	if err := revalidateEvidences(snapshot, evidences, now); err != nil {
		return err
	}
	if err := provider.EvaluateProviderEligibility(stored, snapshot, evidences, now); err != nil {
		return cancelError(CancelReasonRegistrationIncompatible, fmt.Sprintf("the combined eligibility gate rejected the current ledger: %v", err))
	}
	if err := m.Match(stored, snapshot, evidences, requirements, now); err != nil {
		return fmt.Errorf("dispatch: revalidate: %w", err)
	}
	return nil
}

// matchCapabilities verifies that the capabilities map declares both frozen
// keys and satisfies the requirements: the declared accessMode must cover
// the requested accessMode (workspace-write is the superset capability and
// satisfies read-only) and the declared minimumAssuranceLevel must be at
// least the requested level (hardened satisfies workspace-write). Missing
// declarations, unknown values and conflicts fail closed.
func matchCapabilities(snapshot provider.ProviderCapabilitySnapshot, requirements domain.SandboxRequirements) error {
	requiredMode, err := domain.ParseAccessMode(string(requirements.AccessMode))
	if err != nil {
		return fmt.Errorf("requirements accessMode rejected: %w", err)
	}
	requiredLevel, err := domain.ParseAssuranceLevel(string(requirements.MinimumAssuranceLevel))
	if err != nil {
		return fmt.Errorf("requirements minimumAssuranceLevel rejected: %w", err)
	}
	rawMode, present := snapshot.Capabilities[capabilityKeyAccessMode]
	if !present {
		return fmt.Errorf("capabilities must declare %q; a missing declaration fails closed", capabilityKeyAccessMode)
	}
	declaredMode, err := domain.ParseAccessMode(rawMode)
	if err != nil {
		return fmt.Errorf("capabilities %q declaration rejected: %w", capabilityKeyAccessMode, err)
	}
	if !accessModeSatisfies(declaredMode, requiredMode) {
		return fmt.Errorf("capabilities accessMode %q conflicts with the required accessMode %q", string(declaredMode), string(requiredMode))
	}
	rawLevel, present := snapshot.Capabilities[capabilityKeyMinimumAssuranceLevel]
	if !present {
		return fmt.Errorf("capabilities must declare %q; a missing declaration fails closed", capabilityKeyMinimumAssuranceLevel)
	}
	declaredLevel, err := domain.ParseAssuranceLevel(rawLevel)
	if err != nil {
		return fmt.Errorf("capabilities %q declaration rejected: %w", capabilityKeyMinimumAssuranceLevel, err)
	}
	if !assuranceLevelSatisfies(declaredLevel, requiredLevel) {
		return fmt.Errorf("capabilities minimumAssuranceLevel %q conflicts with the required minimumAssuranceLevel %q", string(declaredLevel), string(requiredLevel))
	}
	return nil
}

// accessModeSatisfies reports whether the declared accessMode covers the
// required one: workspace-write is the superset capability.
func accessModeSatisfies(declared, required domain.AccessMode) bool {
	if declared == required {
		return true
	}
	return declared == domain.AccessModeWorkspaceWrite
}

// assuranceLevelSatisfies reports whether the declared assurance level is at
// least the required one: hardened is the stronger isolation.
func assuranceLevelSatisfies(declared, required domain.AssuranceLevel) bool {
	if declared == required {
		return true
	}
	return declared == domain.AssuranceLevelHardened
}

// cancelError assembles the machine-readable revalidate failure: the closed
// cancelReason is embedded verbatim so the caller can cancel with the exact
// reason and bump the generation.
func cancelError(reason CancelReason, detail string) error {
	return fmt.Errorf("dispatch: revalidate fail closed: cancelReason %s: %s", string(reason), detail)
}

// digestSetsEqual reports whether both digest slices carry the identical
// set; both sides are validated elsewhere as duplicate-free closed sets.
func digestSetsEqual(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	seen := make(map[string]struct{}, len(first))
	for _, digest := range first {
		seen[digest] = struct{}{}
	}
	for _, digest := range second {
		if _, ok := seen[digest]; !ok {
			return false
		}
	}
	return true
}

// revalidateEvidences reconciles the provided evidence records with the
// closed digest set and fails closed with the exact evidence reason code:
// missing, extra or duplicate coverage and revoked records yield
// evidence-revoked; expired records and records past their validUntil yield
// evidence-expired.
func revalidateEvidences(snapshot provider.ProviderCapabilitySnapshot, evidences []provider.ConformanceEvidence, now time.Time) error {
	closed := make(map[string]struct{}, len(snapshot.ConformanceEvidenceDigests))
	for _, digest := range snapshot.ConformanceEvidenceDigests {
		closed[digest] = struct{}{}
	}
	covered := make(map[string]struct{}, len(evidences))
	for index, evidence := range evidences {
		if _, declared := closed[evidence.EvidenceDigest]; !declared {
			return cancelError(CancelReasonEvidenceRevoked, fmt.Sprintf("evidences[%d] carries evidenceDigest %s outside the closed set", index, evidence.EvidenceDigest))
		}
		if _, duplicate := covered[evidence.EvidenceDigest]; duplicate {
			return cancelError(CancelReasonEvidenceRevoked, fmt.Sprintf("evidenceDigest %s is covered more than once", evidence.EvidenceDigest))
		}
		covered[evidence.EvidenceDigest] = struct{}{}
	}
	for _, digest := range snapshot.ConformanceEvidenceDigests {
		evidence, present := evidenceByDigest(evidences, digest)
		if !present {
			return cancelError(CancelReasonEvidenceRevoked, fmt.Sprintf("the closed set declares %s but no evidence covers it any more", digest))
		}
		switch evidence.EvidenceState {
		case provider.EvidenceStateValid:
		case provider.EvidenceStateRevoked:
			return cancelError(CancelReasonEvidenceRevoked, fmt.Sprintf("evidence %s was revoked", digest))
		case provider.EvidenceStateExpired:
			return cancelError(CancelReasonEvidenceExpired, fmt.Sprintf("evidence %s expired", digest))
		default:
			return cancelError(CancelReasonEvidenceRevoked, fmt.Sprintf("evidence %s carries unknown evidenceState %q", digest, string(evidence.EvidenceState)))
		}
		if evidence.ValidUntil != "" {
			validUntil, err := time.Parse(time.RFC3339, evidence.ValidUntil)
			if err != nil {
				return cancelError(CancelReasonEvidenceExpired, fmt.Sprintf("evidence %s carries a malformed validUntil", digest))
			}
			if !now.Before(validUntil) {
				return cancelError(CancelReasonEvidenceExpired, fmt.Sprintf("evidence %s is past its validUntil %q", digest, evidence.ValidUntil))
			}
		}
	}
	return nil
}

// evidenceByDigest returns the evidence carrying digest, when present.
func evidenceByDigest(evidences []provider.ConformanceEvidence, digest string) (provider.ConformanceEvidence, bool) {
	for _, evidence := range evidences {
		if evidence.EvidenceDigest == digest {
			return evidence, true
		}
	}
	return provider.ConformanceEvidence{}, false
}
