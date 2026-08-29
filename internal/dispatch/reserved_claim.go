package dispatch

import (
	"errors"
	"fmt"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/provider"
)

// ErrReservedClaimNotFound is the only result that authorizes the fresh
// reserved-claim path to mint a DispatchLease. Every other lookup failure is
// structural or durable-state corruption and must fail closed.
var ErrReservedClaimNotFound = errors.New("dispatch: reserved claim not found")

// ReservedClaimRequest is the ADR 0069 dispatch admission request. The
// reservation fact digest, RunID and reserved AttemptID form the durable
// lookup key; the nested Claim is the exact frozen dispatch input. RunId and
// AttemptId are repeated deliberately so a sibling or substituted Claim can
// never hide behind a valid reservation key.
type ReservedClaimRequest struct {
	ReservationFactDigest string       `json:"reservationFactDigest"`
	RunId                 string       `json:"runId"`
	ReservedAttemptId     string       `json:"reservedAttemptId"`
	Claim                 ClaimRequest `json:"claim"`
}

// ReservedClaimResult is the durable response of ClaimReserved. It carries
// every stable value needed to assemble the full Attempt identity. Replays
// return these exact persisted records; they never derive a new CreatedAt,
// generation, fencing token, lease digest, or typed-edge digest.
type ReservedClaimResult struct {
	ReservationFactDigest string                             `json:"reservationFactDigest"`
	RunId                 string                             `json:"runId"`
	ReservedAttemptId     string                             `json:"reservedAttemptId"`
	ClaimInputDigest      string                             `json:"claimInputDigest"`
	Lease                 DispatchLease                      `json:"lease"`
	ResultCapability      authority.DispatchResultCapability `json:"resultCapability"`
}

// reservedClaimInput is the canonical request identity persisted with the
// claim. Registration is the exact durable registration observed before the
// lookup; Claim binds capability snapshot, evidence records, requirements,
// target, deadlines and all workload identifiers. The caller clock is
// intentionally absent from this identity.
type reservedClaimInput struct {
	ReservationFactDigest string                        `json:"reservationFactDigest"`
	RunId                 string                        `json:"runId"`
	ReservedAttemptId     string                        `json:"reservedAttemptId"`
	Registration          provider.ProviderRegistration `json:"registration"`
	Claim                 ClaimRequest                  `json:"claim"`
}

// reservedClaimFact is a new fact type rather than an extension of the
// legacy lease-claimed fact. That keeps historical strict decoding and replay
// byte-compatible while giving fresh ADR 0069 claims a closed durable replay
// record.
type reservedClaimFact struct {
	FactType         string                             `json:"factType"`
	Sequence         int64                              `json:"sequence"`
	Input            reservedClaimInput                 `json:"input"`
	ClaimInputDigest string                             `json:"claimInputDigest"`
	Lease            DispatchLease                      `json:"lease"`
	ResultCapability authority.DispatchResultCapability `json:"resultCapability"`
	Digest           string                             `json:"digest"`
}

func (fact reservedClaimFact) factDigest() string           { return fact.Digest }
func (fact *reservedClaimFact) setFactDigest(digest string) { fact.Digest = digest }

func (input reservedClaimInput) key() string {
	return input.ReservationFactDigest + "\x00" + input.RunId + "\x00" + input.ReservedAttemptId
}

func (input reservedClaimInput) digest() (string, error) {
	return canonicalDigestOf(input)
}

func (input reservedClaimInput) validate() error {
	if err := requireSHA256Digest("reservationFactDigest", input.ReservationFactDigest); err != nil {
		return err
	}
	if err := requireText("reserved claim runId", input.RunId); err != nil {
		return err
	}
	if err := requireText("reservedAttemptId", input.ReservedAttemptId); err != nil {
		return err
	}
	if input.Claim.RunId != input.RunId {
		return fmt.Errorf("dispatch: reserved claim runId does not match claim.runId")
	}
	if input.Claim.AttemptId != input.ReservedAttemptId {
		return fmt.Errorf("dispatch: reservedAttemptId does not match claim.attemptId")
	}
	if err := input.Registration.Validate(); err != nil {
		return fmt.Errorf("dispatch: reserved claim registration: %w", err)
	}
	if input.Registration.LifecycleState != provider.LifecycleStateActive {
		return fmt.Errorf("dispatch: reserved claim registration must record active lifecycleState")
	}
	if !input.Claim.AuthorityNamespaceId.Equal(input.Registration.AuthorityNamespaceId) {
		return fmt.Errorf("dispatch: reserved claim authorityNamespaceId does not match registration")
	}
	if input.Claim.RegistrationId != input.Registration.RegistrationId {
		return fmt.Errorf("dispatch: reserved claim registrationId does not match registration")
	}
	if err := input.Claim.Snapshot.ValidateAgainstRegistration(input.Registration); err != nil {
		return fmt.Errorf("dispatch: reserved claim snapshot: %w", err)
	}
	if input.Claim.Snapshot.SnapshotState != provider.SnapshotStateActive {
		return fmt.Errorf("dispatch: reserved claim snapshot must record active snapshotState")
	}
	covered := make(map[string]struct{}, len(input.Claim.Evidences))
	for index, evidence := range input.Claim.Evidences {
		if err := evidence.ValidateAgainstRegistration(input.Registration); err != nil {
			return fmt.Errorf("dispatch: reserved claim evidences[%d]: %w", index, err)
		}
		if err := evidence.ValidateAgainstSnapshot(input.Claim.Snapshot); err != nil {
			return fmt.Errorf("dispatch: reserved claim evidences[%d]: %w", index, err)
		}
		if evidence.EvidenceState != provider.EvidenceStateValid {
			return fmt.Errorf("dispatch: reserved claim evidences[%d] must record valid evidenceState", index)
		}
		if _, duplicate := covered[evidence.EvidenceDigest]; duplicate {
			return fmt.Errorf("dispatch: reserved claim evidence set contains duplicate digest %s", evidence.EvidenceDigest)
		}
		covered[evidence.EvidenceDigest] = struct{}{}
	}
	if len(covered) != len(input.Claim.Snapshot.ConformanceEvidenceDigests) {
		return fmt.Errorf("dispatch: reserved claim evidence records do not cover the snapshot closed set")
	}
	for _, digest := range input.Claim.Snapshot.ConformanceEvidenceDigests {
		if _, ok := covered[digest]; !ok {
			return fmt.Errorf("dispatch: reserved claim evidence records do not cover snapshot digest %s", digest)
		}
	}
	if _, err := domain.ParseAccessMode(string(input.Claim.Requirements.AccessMode)); err != nil {
		return fmt.Errorf("dispatch: reserved claim requirements: %w", err)
	}
	if _, err := domain.ParseAssuranceLevel(string(input.Claim.Requirements.MinimumAssuranceLevel)); err != nil {
		return fmt.Errorf("dispatch: reserved claim requirements: %w", err)
	}
	if input.Claim.Requirements.APIVersion != domain.APIVersionV1Alpha1 || input.Claim.Requirements.Kind != domain.KindSandboxRequirements {
		return fmt.Errorf("dispatch: reserved claim requirements carry an unsupported apiVersion/kind")
	}
	if err := input.Claim.TargetActor.Validate(); err != nil {
		return fmt.Errorf("dispatch: reserved claim targetActor: %w", err)
	}
	for _, field := range []struct{ name, value string }{
		{"taskId", input.Claim.TaskId},
		{"allocationId", input.Claim.AllocationId},
	} {
		if err := requireText(field.name, field.value); err != nil {
			return err
		}
	}
	if err := requireRFC3339("ackDeadlineAt", input.Claim.AckDeadlineAt); err != nil {
		return err
	}
	return requireRFC3339("expiresAt", input.Claim.ExpiresAt)
}

func (fact reservedClaimFact) validate() error {
	if fact.FactType != leaseFactTypeReservedClaimed {
		return fmt.Errorf("dispatch: reserved claim fact carries factType %q", fact.FactType)
	}
	if err := fact.Input.validate(); err != nil {
		return err
	}
	inputDigest, err := fact.Input.digest()
	if err != nil {
		return err
	}
	if fact.ClaimInputDigest != inputDigest {
		return fmt.Errorf("dispatch: reserved claim input digest does not match canonical input bytes")
	}
	if err := fact.Lease.Validate(); err != nil {
		return fmt.Errorf("dispatch: reserved claim lease: %w", err)
	}
	if fact.Lease.LeaseState != LeaseStateClaimed || fact.Lease.Generation != 1 {
		return fmt.Errorf("dispatch: reserved claim lease must start claimed at generation 1")
	}
	expectedFencingToken, err := fencingTokenOf(fact.Lease)
	if err != nil {
		return fmt.Errorf("dispatch: reserved claim lease fencing derivation: %w", err)
	}
	if fact.Lease.FencingToken != expectedFencingToken {
		return fmt.Errorf("dispatch: reserved claim lease fencingToken does not match its deterministic generation binding")
	}
	if !fact.Lease.AuthorityNamespaceId.Equal(fact.Input.Registration.AuthorityNamespaceId) ||
		!fact.Lease.SecurityDomainId.Equal(fact.Input.Registration.SecurityDomainId) ||
		fact.Lease.RegistrationId != fact.Input.Registration.RegistrationId ||
		!fact.Lease.Attestation.Equal(fact.Input.Registration.Attestation) ||
		fact.Lease.ProviderCapabilitySnapshotDigest != fact.Input.Claim.Snapshot.ProviderCapabilitySnapshotDigest ||
		!digestSetsEqual(fact.Lease.ConformanceEvidenceDigests, fact.Input.Claim.Snapshot.ConformanceEvidenceDigests) ||
		fact.Lease.TaskId != fact.Input.Claim.TaskId || fact.Lease.RunId != fact.Input.RunId ||
		fact.Lease.AttemptId != fact.Input.ReservedAttemptId || fact.Lease.AllocationId != fact.Input.Claim.AllocationId ||
		fact.Lease.AckDeadlineAt != fact.Input.Claim.AckDeadlineAt || fact.Lease.ExpiresAt != fact.Input.Claim.ExpiresAt {
		return fmt.Errorf("dispatch: reserved claim lease does not exactly bind the canonical claim input")
	}
	if err := fact.ResultCapability.Validate(); err != nil {
		return fmt.Errorf("dispatch: reserved claim result capability: %w", err)
	}
	if !fact.ResultCapability.Issuer.Equal(fact.Lease.AuthorityNamespaceId) ||
		!fact.ResultCapability.SourceActor.Equal(fact.Lease.SecurityDomainId) ||
		!fact.ResultCapability.TargetActor.Equal(fact.Input.Claim.TargetActor) ||
		fact.ResultCapability.Operation != authority.DispatchResultOperationAccept ||
		fact.ResultCapability.BoundAttemptId != fact.Lease.AttemptId ||
		fact.ResultCapability.BoundAllocationId != fact.Lease.AllocationId ||
		fact.ResultCapability.Expiry != fact.Lease.ExpiresAt ||
		fact.ResultCapability.Generation != 1 || fact.ResultCapability.RevocationGeneration != 0 {
		return fmt.Errorf("dispatch: reserved claim result capability does not exactly bind the lease identity")
	}
	return nil
}

func (fact reservedClaimFact) result() ReservedClaimResult {
	return ReservedClaimResult{
		ReservationFactDigest: fact.Input.ReservationFactDigest,
		RunId:                 fact.Input.RunId,
		ReservedAttemptId:     fact.Input.ReservedAttemptId,
		ClaimInputDigest:      fact.ClaimInputDigest,
		Lease:                 fact.Lease,
		ResultCapability:      fact.ResultCapability,
	}
}

// lookupOrCreateReservedClaim is the atomic durable lookup-before-claim
// seam. create is invoked only for deterministic not-found while l.mu is
// held, so concurrent callers sharing the production ledger cannot mint
// siblings. Exact input replay returns the persisted response; any key reuse
// with different canonical bytes fails closed.
func (l *LeaseLedger) lookupOrCreateReservedClaim(input reservedClaimInput, create func() (DispatchLease, authority.DispatchResultCapability, error)) (ReservedClaimResult, bool, error) {
	if err := l.requireBound(); err != nil {
		return ReservedClaimResult{}, false, err
	}
	if err := input.validate(); err != nil {
		return ReservedClaimResult{}, false, err
	}
	inputDigest, err := input.digest()
	if err != nil {
		return ReservedClaimResult{}, false, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	key := input.key()
	if existing, ok := l.reservedClaims[key]; ok {
		if existing.ClaimInputDigest != inputDigest {
			return ReservedClaimResult{}, false, fmt.Errorf("%w: reserved claim key was replayed with different canonical input bytes", ErrLeaseConflict)
		}
		current, ok := l.leases[existing.Lease.LeaseId]
		if !ok {
			return ReservedClaimResult{}, false, fmt.Errorf("%w: reserved claim lease is missing from the current ledger index", ErrLeaseConflict)
		}
		if current.LeaseDigest != existing.Lease.LeaseDigest || current.Generation != existing.Lease.Generation || current.FencingToken != existing.Lease.FencingToken || current.LeaseState != existing.Lease.LeaseState {
			return ReservedClaimResult{}, false, fmt.Errorf("%w: reserved claim current generation/fencing/state no longer matches the original response", ErrLeaseConflict)
		}
		return existing.result(), true, nil
	}
	if existing, ok := l.reservationBindings[input.ReservationFactDigest]; ok {
		return ReservedClaimResult{}, false, fmt.Errorf("%w: reservation fact digest already binds reserved claim %s", ErrLeaseConflict, existing)
	}
	binding := claimBindingKey(input.RunId, input.ReservedAttemptId)
	if existing, ok := l.reservedAttemptBindings[binding]; ok {
		return ReservedClaimResult{}, false, fmt.Errorf("%w: Run/Attempt tuple already binds reserved claim %s", ErrLeaseConflict, existing)
	}
	if existing, ok := l.activeBindings[binding]; ok {
		return ReservedClaimResult{}, false, fmt.Errorf("%w: Run/Attempt tuple already carries legacy or sibling lease %s", ErrLeaseConflict, existing)
	}
	if source, ok := l.closedAttemptBindings[binding]; ok {
		return ReservedClaimResult{}, false, fmt.Errorf("%w: Run/Attempt tuple is terminal in Attempt authority projection %s", ErrLeaseConflict, source)
	}
	for _, historical := range l.leases {
		if historical.RunId == input.RunId && historical.AttemptId == input.ReservedAttemptId {
			return ReservedClaimResult{}, false, fmt.Errorf("%w: Run/Attempt tuple already exists in legacy lease history and fresh reserved claim cannot adopt it", ErrLeaseConflict)
		}
	}
	if create == nil {
		return ReservedClaimResult{}, false, fmt.Errorf("%w: %s", ErrReservedClaimNotFound, key)
	}
	lease, edge, err := create()
	if err != nil {
		return ReservedClaimResult{}, false, err
	}
	fact := reservedClaimFact{
		FactType: leaseFactTypeReservedClaimed, Sequence: l.nextSequence,
		Input: input, ClaimInputDigest: inputDigest, Lease: lease, ResultCapability: edge,
	}
	if err := fact.validate(); err != nil {
		return ReservedClaimResult{}, false, err
	}
	if err := l.requireReservedClaimable(fact); err != nil {
		return ReservedClaimResult{}, false, err
	}
	if err := l.requireClaimable(lease); err != nil {
		return ReservedClaimResult{}, false, err
	}
	if err := l.appendFactLine(&fact); err != nil {
		return ReservedClaimResult{}, false, err
	}
	l.indexReservedClaim(fact)
	return fact.result(), false, nil
}

func (l *LeaseLedger) requireReservedClaimable(fact reservedClaimFact) error {
	key := fact.Input.key()
	if existing, ok := l.reservationBindings[fact.Input.ReservationFactDigest]; ok && existing != key {
		return fmt.Errorf("%w: reservation fact digest already binds a different Run/Attempt tuple", ErrLeaseConflict)
	}
	binding := claimBindingKey(fact.Input.RunId, fact.Input.ReservedAttemptId)
	if existing, ok := l.reservedAttemptBindings[binding]; ok && existing != key {
		return fmt.Errorf("%w: reserved Run/Attempt tuple already binds a sibling reservation", ErrLeaseConflict)
	}
	if existing, ok := l.reservedClaims[key]; ok {
		if existing.ClaimInputDigest == fact.ClaimInputDigest {
			return fmt.Errorf("%w: reserved claim already exists and must be replayed, never appended", ErrLeaseConflict)
		}
		return fmt.Errorf("%w: reserved claim key carries conflicting canonical input bytes", ErrLeaseConflict)
	}
	return nil
}

func (l *LeaseLedger) indexReservedClaim(fact reservedClaimFact) {
	key := fact.Input.key()
	l.reservedClaims[key] = fact
	l.reservationBindings[fact.Input.ReservationFactDigest] = key
	l.reservedAttemptBindings[claimBindingKey(fact.Input.RunId, fact.Input.ReservedAttemptId)] = key
	l.leases[fact.Lease.LeaseId] = fact.Lease
	l.activeBindings[claimBindingKey(fact.Lease.RunId, fact.Lease.AttemptId)] = fact.Lease.LeaseId
	l.nextSequence++
}

func (l *LeaseLedger) applyReservedClaimFact(line []byte) error {
	var fact reservedClaimFact
	if err := decodeLeaseFact(line, &fact); err != nil {
		return err
	}
	if err := verifyFactDigest(&fact); err != nil {
		return err
	}
	if err := fact.validate(); err != nil {
		return err
	}
	if err := l.requireReservedClaimable(fact); err != nil {
		return err
	}
	if err := l.requireClaimable(fact.Lease); err != nil {
		return err
	}
	l.indexReservedClaim(fact)
	return nil
}

// ClaimReserved is the fresh ADR 0069 production entry point. It first
// obtains the exact current durable registration and performs an atomic
// reserved-key lookup. Only deterministic absence invokes the original
// match/claim calculation. Exact replay restores and returns the persisted
// lease and result capability without reading now into the idempotency
// identity; conflicts never fall back to legacy Claim or mint a sibling.
func (m *Matcher) ClaimReserved(request ReservedClaimRequest, now time.Time) (ReservedClaimResult, error) {
	if m == nil || m.store == nil {
		return ReservedClaimResult{}, fmt.Errorf("dispatch: reserved claim precondition: the registration store is not bound to a durable ledger directory: %w", provider.ErrMemoryOnlyRegistration)
	}
	if m.edgeRuntime == nil {
		return ReservedClaimResult{}, fmt.Errorf("dispatch: reserved claim precondition: the typed-edge runtime is not bound")
	}
	if m.leaseLedger == nil {
		return ReservedClaimResult{}, fmt.Errorf("dispatch: reserved claim precondition: the durable lease ledger is not bound; fresh claims never fall back to legacy Claim")
	}
	stored, err := m.store.Get(request.Claim.RegistrationId)
	if err != nil {
		return ReservedClaimResult{}, fmt.Errorf("dispatch: reserved claim registration lookup: %w", err)
	}
	if !m.edgeRuntime.Issuer().Equal(stored.AuthorityNamespaceId) {
		return ReservedClaimResult{}, fmt.Errorf("%w: typed-edge runtime issuer does not match the canonical registration authority", ErrLeaseConflict)
	}
	input := reservedClaimInput{
		ReservationFactDigest: request.ReservationFactDigest,
		RunId:                 request.RunId, ReservedAttemptId: request.ReservedAttemptId,
		Registration: stored, Claim: request.Claim,
	}
	if err := input.validate(); err != nil {
		return ReservedClaimResult{}, fmt.Errorf("dispatch: reserved claim input rejected: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	result, _, err := m.leaseLedger.lookupOrCreateReservedClaim(input, func() (DispatchLease, authority.DispatchResultCapability, error) {
		if m.issuedLeases == nil {
			m.issuedLeases = map[string]string{}
		}
		binding := claimBindingKey(request.RunId, request.ReservedAttemptId)
		if existing, taken := m.issuedLeases[binding]; taken {
			return DispatchLease{}, authority.DispatchResultCapability{}, fmt.Errorf("%w: fresh reserved claim encountered existing lease %s for the Run/Attempt binding; legacy or sibling claims are never adopted", ErrLeaseConflict, existing)
		}
		return m.buildReservedClaimCandidate(stored, request.Claim, now)
	})
	if err != nil {
		return ReservedClaimResult{}, err
	}
	if err := m.reissueReservedCapability(result, input, now); err != nil {
		return ReservedClaimResult{}, err
	}
	if m.issuedLeases == nil {
		m.issuedLeases = map[string]string{}
	}
	if m.issuedResultCapabilities == nil {
		m.issuedResultCapabilities = map[string]authority.DispatchResultCapability{}
	}
	m.issuedLeases[claimBindingKey(request.RunId, request.ReservedAttemptId)] = result.Lease.LeaseId
	m.issuedResultCapabilities[result.Lease.LeaseId] = result.ResultCapability
	return result, nil
}

// buildReservedClaimCandidate is the side-effect-free form of Claim. It
// deterministically seals both response records but does not issue the edge
// into the runtime. ClaimReserved first fsyncs those exact bytes and only
// then restores the runtime projection; an append or response failure can
// therefore be recovered without an unrecorded hidden lease binding.
func (m *Matcher) buildReservedClaimCandidate(stored provider.ProviderRegistration, request ClaimRequest, now time.Time) (DispatchLease, authority.DispatchResultCapability, error) {
	if stored.LifecycleState != provider.LifecycleStateActive {
		return DispatchLease{}, authority.DispatchResultCapability{}, fmt.Errorf("dispatch: reserved claim: stored registration carries lifecycleState %q", stored.LifecycleState)
	}
	if !request.AuthorityNamespaceId.Equal(stored.AuthorityNamespaceId) || request.RegistrationId != stored.RegistrationId {
		return DispatchLease{}, authority.DispatchResultCapability{}, fmt.Errorf("dispatch: reserved claim: registration identity no longer matches the canonical input")
	}
	if request.Snapshot.RegistrationId != stored.RegistrationId || !request.Snapshot.Attestation.Equal(stored.Attestation) {
		return DispatchLease{}, authority.DispatchResultCapability{}, fmt.Errorf("dispatch: reserved claim: snapshot no longer aligns with the canonical registration")
	}
	if err := m.Match(stored, request.Snapshot, request.Evidences, request.Requirements, now); err != nil {
		return DispatchLease{}, authority.DispatchResultCapability{}, fmt.Errorf("dispatch: reserved claim: %w", err)
	}

	evidenceDigests := append([]string(nil), request.Snapshot.ConformanceEvidenceDigests...)
	lease := DispatchLease{
		AuthorityNamespaceId: stored.AuthorityNamespaceId, SecurityDomainId: stored.SecurityDomainId,
		RegistrationId:                   stored.RegistrationId,
		ProviderCapabilitySnapshotDigest: request.Snapshot.ProviderCapabilitySnapshotDigest,
		ConformanceEvidenceDigests:       evidenceDigests, Attestation: stored.Attestation,
		TaskId: request.TaskId, RunId: request.RunId, AttemptId: request.AttemptId, AllocationId: request.AllocationId,
		Generation: 1, AckDeadlineAt: request.AckDeadlineAt, ExpiresAt: request.ExpiresAt,
		LeaseState: LeaseStateClaimed, CreatedAt: now.UTC().Format(time.RFC3339),
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
		TaskId:                           request.TaskId, RunId: request.RunId, AttemptId: request.AttemptId,
		AllocationId: request.AllocationId, Generation: 1,
	}
	leaseID, err := canonicalDigestOf(binding)
	if err != nil {
		return DispatchLease{}, authority.DispatchResultCapability{}, fmt.Errorf("dispatch: reserved claim leaseId derivation: %w", err)
	}
	lease.LeaseId = leaseID
	if err := sealLease(&lease); err != nil {
		return DispatchLease{}, authority.DispatchResultCapability{}, fmt.Errorf("dispatch: reserved claim lease seal: %w", err)
	}
	edge := authority.DispatchResultCapability{
		Issuer: stored.AuthorityNamespaceId, SourceActor: stored.SecurityDomainId, TargetActor: request.TargetActor,
		Operation: authority.DispatchResultOperationAccept, BoundAttemptId: request.AttemptId,
		BoundAllocationId: request.AllocationId, Expiry: request.ExpiresAt, Generation: 1,
	}
	edgeDigest, err := edge.Digest()
	if err != nil {
		return DispatchLease{}, authority.DispatchResultCapability{}, fmt.Errorf("dispatch: reserved claim typed-edge digest: %w", err)
	}
	edge.EdgeDigest = edgeDigest
	if err := edge.Validate(); err != nil {
		return DispatchLease{}, authority.DispatchResultCapability{}, fmt.Errorf("dispatch: reserved claim typed-edge validation: %w", err)
	}
	preview, _, err := m.edgeRuntime.PreviewDispatchResultCapability(reservedDispatchResultIssuance(lease, request.TargetActor))
	if err != nil {
		return DispatchLease{}, authority.DispatchResultCapability{}, fmt.Errorf("dispatch: reserved claim typed-edge preflight: %w", err)
	}
	if preview != edge {
		return DispatchLease{}, authority.DispatchResultCapability{}, fmt.Errorf("%w: typed-edge preflight did not reproduce the exact unrevoked capability", ErrLeaseConflict)
	}
	return lease, edge, nil
}

func reservedDispatchResultIssuance(lease DispatchLease, target authority.SecurityDomainId) authority.DispatchResultIssuance {
	return authority.DispatchResultIssuance{
		SourceActor: lease.SecurityDomainId, TargetActor: target,
		Operation:      authority.DispatchResultOperationAccept,
		BoundAttemptId: lease.AttemptId, BoundAllocationId: lease.AllocationId,
		Expiry: lease.ExpiresAt,
		LeaseBinding: authority.EdgeLeaseBinding{
			LeaseId: lease.LeaseId, AttemptId: lease.AttemptId, AllocationId: lease.AllocationId,
			Generation: lease.Generation, FencingToken: lease.FencingToken,
		},
	}
}

// reissueReservedCapability restores the typed-edge runtime projection from
// a durable exact replay and verifies that deterministic issuance reproduces
// the persisted edge byte-for-byte. Current eligibility is deliberately not
// decided here; callers must still invoke Revalidate before downstream work.
func (m *Matcher) reissueReservedCapability(fact ReservedClaimResult, input reservedClaimInput, now time.Time) error {
	edge, err := m.edgeRuntime.IssueDispatchResultCapability(reservedDispatchResultIssuance(fact.Lease, input.Claim.TargetActor), now)
	if err != nil {
		return fmt.Errorf("dispatch: reserved claim replay typed-edge issuance: %w", err)
	}
	if edge != fact.ResultCapability {
		return fmt.Errorf("%w: reserved claim typed-edge replay did not reproduce the durable capability", ErrLeaseConflict)
	}
	return nil
}
