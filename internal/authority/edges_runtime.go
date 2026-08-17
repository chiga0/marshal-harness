// Typed edge runtime (M9-b, ADR 0018 §3/§7): Core-exclusive issuance,
// revocation as an authority ledger fact and current-ledger recheck of every
// use for the three typed cross-domain edges frozen in edges.go.
//
// The record layer is not modified: the runtime issues, revokes and rechecks
// records strictly through their frozen Validate/Digest/ReplayKey/ValidAt
// semantics. Every use recheck consults the current authority ledger — edge
// active, digest aligned, unrevoked/unexpired, target actor still eligible,
// bound Attempt/allocation/lease still active — and fails closed with an
// audit record when any item is not satisfied. Derived tokens and handles
// are one-way references into this ledger and never a second authority: the
// runtime exposes no authorization decision from a handle alone.

package authority

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Sentinel errors exposed by the typed edge runtime. Every runtime-specific
// failure wraps exactly one of them, so fixtures can assert a fixed
// sentinel; failures of the frozen record layer keep wrapping the edges.go
// sentinels (ErrEdgePair, ErrEdgeOperation, ErrEdgeDigest, ErrEdgeRevoked,
// ErrEdgeExpired).
var (
	// ErrEdgeNotRecorded rejects a presented edge whose digest is absent
	// from the current authority ledger: an edge never issued by this Core
	// runtime, or issued under another authorityNamespaceId, can never
	// authorize a use.
	ErrEdgeNotRecorded = errors.New("authority: edge is not recorded in the current authority ledger")

	// ErrEdgeDiverged rejects a presented edge whose content no longer
	// matches the current authority ledger record recorded under its own
	// digest.
	ErrEdgeDiverged = errors.New("authority: presented edge diverges from the current authority ledger record")

	// ErrEdgeBindingMismatch rejects a use whose operation, source/target
	// identity, attempt/allocation, material, publication or lease binding
	// does not exactly match the authority ledger record of the edge.
	ErrEdgeBindingMismatch = errors.New("authority: edge use binding does not exactly match the authority ledger record")

	// ErrEdgeLeaseInactive rejects a use whose bound lease is no longer
	// active in the current dispatch ledger.
	ErrEdgeLeaseInactive = errors.New("authority: the lease bound to the edge is no longer active")

	// ErrEdgeTargetIneligible rejects a use whose target actor is no longer
	// eligible (registration/snapshot/evidence) in the current ledger.
	ErrEdgeTargetIneligible = errors.New("authority: the target actor bound to the edge is no longer eligible")

	// ErrEdgeResolverUnbound rejects any recheck before the current-ledger
	// resolvers are bound: a recheck that cannot consult the current ledger
	// fails closed.
	ErrEdgeResolverUnbound = errors.New("authority: edge recheck resolvers are not bound to the current ledger")
)

// EdgeKind is the closed enumeration of the three typed cross-domain edges.
// Matching is case sensitive.
type EdgeKind string

const (
	EdgeKindDispatchResultCapability EdgeKind = "dispatchResultCapability"
	EdgeKindMaterialAccessGrant      EdgeKind = "materialAccessGrant"
	EdgeKindPublicationAuthorization EdgeKind = "publicationAuthorization"
)

// EdgeAuditAction is the closed enumeration of audit facts the edge runtime
// records for every issuance, revocation and use decision.
type EdgeAuditAction string

const (
	EdgeAuditIssued             EdgeAuditAction = "edge-issued"
	EdgeAuditIssuanceMerged     EdgeAuditAction = "edge-issuance-merged"
	EdgeAuditIssuanceRejected   EdgeAuditAction = "edge-issuance-rejected"
	EdgeAuditRevoked            EdgeAuditAction = "edge-revoked"
	EdgeAuditRevocationRejected EdgeAuditAction = "edge-revocation-rejected"
	EdgeAuditUseAccepted        EdgeAuditAction = "edge-use-accepted"
	EdgeAuditUseReplayMerged    EdgeAuditAction = "edge-use-replay-merged"
	EdgeAuditUseRejected        EdgeAuditAction = "edge-use-rejected"
)

// EdgeAuditRecord is one append-only audit fact of the edge runtime. The
// audit trail is diagnostic authority; the ledger entries remain the
// authorization authority.
type EdgeAuditRecord struct {
	Sequence   int64           `json:"sequence"`
	Action     EdgeAuditAction `json:"action"`
	EdgeKind   EdgeKind        `json:"edgeKind"`
	EdgeDigest string          `json:"edgeDigest"`
	ReplayKey  string          `json:"replayKey,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	At         time.Time       `json:"at"`
}

// EdgeRevocationReason is the closed enumeration of revocation classes
// (ADR 0018 §6 failure-handling grades). Matching is case sensitive.
type EdgeRevocationReason string

const (
	// EdgeRevocationSecurityCritical is the security-critical revocation
	// class (credential compromise, protocol violation): immediate effect
	// with no drain window; the SecurityCriticalRevokeHook fires
	// synchronously after the revocation fact is recorded.
	EdgeRevocationSecurityCritical EdgeRevocationReason = "security-critical"

	// EdgeRevocationOrdinary is the planned/ordinary revocation class.
	EdgeRevocationOrdinary EdgeRevocationReason = "ordinary"
)

// Validate rejects every value outside the closed enumeration.
func (reason EdgeRevocationReason) Validate() error {
	switch reason {
	case EdgeRevocationSecurityCritical, EdgeRevocationOrdinary:
		return nil
	default:
		return fmt.Errorf("authority: unknown edge revocation reason %q", string(reason))
	}
}

// LeaseActiveResolver reports whether the lease bound to a dispatch edge is
// still active in the current dispatch ledger at exactly the recorded
// generation and fencingToken.
type LeaseActiveResolver interface {
	LeaseActive(leaseId string, generation int64, fencingToken string) (bool, error)
}

// TargetEligibilityResolver reports whether the target actor bound to an
// edge is still eligible in the current ledger: registration active,
// snapshot current and evidence valid.
type TargetEligibilityResolver interface {
	TargetEligible(target SecurityDomainId) (bool, error)
}

// SecurityCriticalRevokeHook is the immediate-effect seam for
// security-critical revocations (cancel + generation bump + kill, no drain
// window). It fires synchronously after the revocation fact is recorded in
// the ledger. A hook failure never rolls back the revocation fact —
// revocation is an authority ledger fact — the hook error is returned to the
// caller for escalation only. The hook is not re-fired by idempotent
// revocation replays.
type SecurityCriticalRevokeHook interface {
	OnSecurityCriticalRevoke(kind EdgeKind, edgeDigest string, at time.Time) error
}

// EdgeLeaseBinding binds one dispatch result capability to the exact
// DispatchLease identity carried at claim time: attempt, allocation,
// generation and fencingToken. The binding is recorded at issuance and every
// recheck requires the presented lease identity to match it exactly.
type EdgeLeaseBinding struct {
	LeaseId      string `json:"leaseId"`
	AttemptId    string `json:"attemptId"`
	AllocationId string `json:"allocationId"`
	Generation   int64  `json:"generation"`
	FencingToken string `json:"fencingToken"`
}

// Validate fails closed on any missing binding member.
func (binding EdgeLeaseBinding) Validate() error {
	if err := requireText("edgeLeaseBinding.leaseId", binding.LeaseId); err != nil {
		return err
	}
	if err := requireText("edgeLeaseBinding.attemptId", binding.AttemptId); err != nil {
		return err
	}
	if err := requireText("edgeLeaseBinding.allocationId", binding.AllocationId); err != nil {
		return err
	}
	if binding.Generation < 1 {
		return fmt.Errorf("authority: edgeLeaseBinding.generation must be a positive integer")
	}
	return requireDigest("edgeLeaseBinding.fencingToken", binding.FencingToken)
}

// MaterialAttemptBinding binds one material access grant to the attempt
// boundary it was issued under: the grant expiry never extends beyond the
// attempt boundary and the attempt/allocation identity must match every use.
type MaterialAttemptBinding struct {
	AttemptId       string    `json:"attemptId"`
	AllocationId    string    `json:"allocationId"`
	AttemptBoundary time.Time `json:"attemptBoundary"`
}

// PublicationDecisionBinding binds one publication authorization to the
// frozen publication-decision facts: the SideEffectIntent digest, the
// accepted ReviewDecision digest and the exact evidence digest. Every
// recheck requires all three digests to remain unchanged.
type PublicationDecisionBinding struct {
	SideEffectIntentDigest string `json:"sideEffectIntentDigest"`
	ReviewDecisionDigest   string `json:"reviewDecisionDigest"`
	EvidenceDigest         string `json:"evidenceDigest"`
}

// Validate fails closed on any missing digest member.
func (binding PublicationDecisionBinding) Validate() error {
	if err := requireDigest("publicationDecisionBinding.sideEffectIntentDigest", binding.SideEffectIntentDigest); err != nil {
		return err
	}
	if err := requireDigest("publicationDecisionBinding.reviewDecisionDigest", binding.ReviewDecisionDigest); err != nil {
		return err
	}
	return requireDigest("publicationDecisionBinding.evidenceDigest", binding.EvidenceDigest)
}

// DispatchResultIssuance requests one DispatchResultCapability. It
// deliberately carries no issuer field: the runtime stamps its own Core
// AuthorityNamespaceId, so issuance is mechanically Core-exclusive and a
// provider can never supply, forge or impersonate the issuer.
type DispatchResultIssuance struct {
	SourceActor       SecurityDomainId        `json:"sourceActor"`
	TargetActor       SecurityDomainId        `json:"targetActor"`
	Operation         DispatchResultOperation `json:"operation"`
	BoundAttemptId    string                  `json:"boundAttemptId"`
	BoundAllocationId string                  `json:"boundAllocationId"`
	Expiry            string                  `json:"expiry"`
	LeaseBinding      EdgeLeaseBinding        `json:"leaseBinding"`
}

func (request DispatchResultIssuance) validate() error {
	if err := request.SourceActor.Validate(); err != nil {
		return err
	}
	if err := request.TargetActor.Validate(); err != nil {
		return err
	}
	if err := request.Operation.Validate(); err != nil {
		return err
	}
	if err := requireText("dispatchResultIssuance.boundAttemptId", request.BoundAttemptId); err != nil {
		return err
	}
	if err := requireText("dispatchResultIssuance.boundAllocationId", request.BoundAllocationId); err != nil {
		return err
	}
	if request.Expiry == "" {
		return fmt.Errorf("authority: dispatchResultIssuance.expiry must be bounded by the lease expiry window and must not be empty")
	}
	if err := request.LeaseBinding.Validate(); err != nil {
		return err
	}
	if request.BoundAttemptId != request.LeaseBinding.AttemptId {
		return fmt.Errorf("authority: dispatchResultIssuance.boundAttemptId does not match the lease binding attemptId")
	}
	if request.BoundAllocationId != request.LeaseBinding.AllocationId {
		return fmt.Errorf("authority: dispatchResultIssuance.boundAllocationId does not match the lease binding allocationId")
	}
	return nil
}

// MaterialAccessIssuance requests one MaterialAccessGrant. It carries no
// issuer field; the runtime stamps its own Core AuthorityNamespaceId. The
// expiry must be bounded by the attempt boundary.
type MaterialAccessIssuance struct {
	SourceActor      SecurityDomainId        `json:"sourceActor"`
	TargetActor      SecurityDomainId        `json:"targetActor"`
	Operation        MaterialAccessOperation `json:"operation"`
	MaterialId       string                  `json:"materialId"`
	ScopeRestriction string                  `json:"scopeRestriction"`
	AttemptId        string                  `json:"attemptId"`
	AllocationId     string                  `json:"allocationId"`
	AttemptBoundary  time.Time               `json:"attemptBoundary"`
	Expiry           string                  `json:"expiry"`
}

func (request MaterialAccessIssuance) validate() error {
	if err := request.SourceActor.Validate(); err != nil {
		return err
	}
	if err := request.TargetActor.Validate(); err != nil {
		return err
	}
	if err := request.Operation.Validate(); err != nil {
		return err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"materialAccessIssuance.materialId", request.MaterialId},
		{"materialAccessIssuance.scopeRestriction", request.ScopeRestriction},
		{"materialAccessIssuance.attemptId", request.AttemptId},
		{"materialAccessIssuance.allocationId", request.AllocationId},
	} {
		if err := requireText(field.name, field.value); err != nil {
			return err
		}
	}
	if request.Expiry == "" {
		return fmt.Errorf("authority: materialAccessIssuance.expiry must be bounded by the attempt boundary and must not be empty")
	}
	if request.AttemptBoundary.IsZero() {
		return fmt.Errorf("authority: materialAccessIssuance.attemptBoundary must not be the zero time")
	}
	parsed, err := time.Parse(time.RFC3339, request.Expiry)
	if err != nil {
		return fmt.Errorf("authority: materialAccessIssuance.expiry must be an RFC 3339 timestamp")
	}
	if parsed.After(request.AttemptBoundary) {
		return fmt.Errorf("authority: materialAccessIssuance.expiry must not extend beyond the attempt boundary")
	}
	return nil
}

// PublicationIssuance requests one PublicationAuthorization. It carries no
// issuer field; the runtime stamps its own Core AuthorityNamespaceId. The
// decision binding freezes the SideEffectIntent/ReviewDecision/evidence
// digests the authorization was issued for; the publication decision-side
// issuance wiring that supplies these digests is delivered by the follow-up
// publication task against this frozen interface.
type PublicationIssuance struct {
	SourceActor            SecurityDomainId           `json:"sourceActor"`
	TargetActor            SecurityDomainId           `json:"targetActor"`
	Operation              PublicationOperation       `json:"operation"`
	BoundPublicationDigest string                     `json:"boundPublicationDigest"`
	ExpectedPrincipal      string                     `json:"expectedPrincipal"`
	DecisionBinding        PublicationDecisionBinding `json:"decisionBinding"`
	Expiry                 string                     `json:"expiry"`
}

func (request PublicationIssuance) validate() error {
	if err := request.SourceActor.Validate(); err != nil {
		return err
	}
	if err := request.TargetActor.Validate(); err != nil {
		return err
	}
	if err := request.Operation.Validate(); err != nil {
		return err
	}
	if err := requireDigest("publicationIssuance.boundPublicationDigest", request.BoundPublicationDigest); err != nil {
		return err
	}
	if err := requireText("publicationIssuance.expectedPrincipal", request.ExpectedPrincipal); err != nil {
		return err
	}
	if err := request.DecisionBinding.Validate(); err != nil {
		return err
	}
	if request.Expiry == "" {
		return fmt.Errorf("authority: publicationIssuance.expiry must be bounded by the publication window and must not be empty")
	}
	return nil
}

// DispatchResultUseRequest is one result-ingress use request against a
// DispatchResultCapability. RequestDigest is the canonical digest of the
// operation request summary; together with the edge reference it forms the
// canonical replay key.
type DispatchResultUseRequest struct {
	SourceActor   SecurityDomainId        `json:"sourceActor"`
	TargetActor   SecurityDomainId        `json:"targetActor"`
	Operation     DispatchResultOperation `json:"operation"`
	AttemptId     string                  `json:"attemptId"`
	AllocationId  string                  `json:"allocationId"`
	LeaseId       string                  `json:"leaseId"`
	Generation    int64                   `json:"generation"`
	FencingToken  string                  `json:"fencingToken"`
	RequestDigest string                  `json:"requestDigest"`
}

func (request DispatchResultUseRequest) validate() error {
	if err := request.SourceActor.Validate(); err != nil {
		return err
	}
	if err := request.TargetActor.Validate(); err != nil {
		return err
	}
	if err := request.Operation.Validate(); err != nil {
		return err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"dispatchResultUseRequest.attemptId", request.AttemptId},
		{"dispatchResultUseRequest.allocationId", request.AllocationId},
		{"dispatchResultUseRequest.leaseId", request.LeaseId},
	} {
		if err := requireText(field.name, field.value); err != nil {
			return err
		}
	}
	if request.Generation < 1 {
		return fmt.Errorf("authority: dispatchResultUseRequest.generation must be a positive integer")
	}
	if err := requireDigest("dispatchResultUseRequest.fencingToken", request.FencingToken); err != nil {
		return err
	}
	return requireDigest("dispatchResultUseRequest.requestDigest", request.RequestDigest)
}

// MaterialAccessUseRequest is one use request against a MaterialAccessGrant.
type MaterialAccessUseRequest struct {
	SourceActor   SecurityDomainId        `json:"sourceActor"`
	TargetActor   SecurityDomainId        `json:"targetActor"`
	Operation     MaterialAccessOperation `json:"operation"`
	MaterialId    string                  `json:"materialId"`
	AttemptId     string                  `json:"attemptId"`
	AllocationId  string                  `json:"allocationId"`
	RequestDigest string                  `json:"requestDigest"`
}

func (request MaterialAccessUseRequest) validate() error {
	if err := request.SourceActor.Validate(); err != nil {
		return err
	}
	if err := request.TargetActor.Validate(); err != nil {
		return err
	}
	if err := request.Operation.Validate(); err != nil {
		return err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"materialAccessUseRequest.materialId", request.MaterialId},
		{"materialAccessUseRequest.attemptId", request.AttemptId},
		{"materialAccessUseRequest.allocationId", request.AllocationId},
	} {
		if err := requireText(field.name, field.value); err != nil {
			return err
		}
	}
	return requireDigest("materialAccessUseRequest.requestDigest", request.RequestDigest)
}

// PublicationUseRequest is one use request against a PublicationAuthorization.
type PublicationUseRequest struct {
	SourceActor            SecurityDomainId     `json:"sourceActor"`
	TargetActor            SecurityDomainId     `json:"targetActor"`
	Operation              PublicationOperation `json:"operation"`
	PublicationDigest      string               `json:"publicationDigest"`
	ExpectedPrincipal      string               `json:"expectedPrincipal"`
	SideEffectIntentDigest string               `json:"sideEffectIntentDigest"`
	ReviewDecisionDigest   string               `json:"reviewDecisionDigest"`
	EvidenceDigest         string               `json:"evidenceDigest"`
	RequestDigest          string               `json:"requestDigest"`
}

func (request PublicationUseRequest) validate() error {
	if err := request.SourceActor.Validate(); err != nil {
		return err
	}
	if err := request.TargetActor.Validate(); err != nil {
		return err
	}
	if err := request.Operation.Validate(); err != nil {
		return err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"publicationUseRequest.publicationDigest", request.PublicationDigest},
		{"publicationUseRequest.sideEffectIntentDigest", request.SideEffectIntentDigest},
		{"publicationUseRequest.reviewDecisionDigest", request.ReviewDecisionDigest},
		{"publicationUseRequest.evidenceDigest", request.EvidenceDigest},
		{"publicationUseRequest.requestDigest", request.RequestDigest},
	} {
		if err := requireDigest(field.name, field.value); err != nil {
			return err
		}
	}
	if err := requireText("publicationUseRequest.expectedPrincipal", request.ExpectedPrincipal); err != nil {
		return err
	}
	return nil
}

// dispatchEdgeEntry is one authority ledger entry of a dispatch result
// capability: the current record (the issued record until revocation, the
// revoked successor afterwards) plus the lease binding recorded at issuance.
type dispatchEdgeEntry struct {
	Edge  DispatchResultCapability
	Lease EdgeLeaseBinding
}

// materialEdgeEntry is one authority ledger entry of a material access
// grant: the current record plus the attempt binding recorded at issuance.
type materialEdgeEntry struct {
	Grant   MaterialAccessGrant
	Binding MaterialAttemptBinding
}

// publicationEdgeEntry is one authority ledger entry of a publication
// authorization: the current record plus the decision binding recorded at
// issuance.
type publicationEdgeEntry struct {
	Authorization PublicationAuthorization
	Decision      PublicationDecisionBinding
}

// PublicationAuthorizationPersistence is the durable sink for publication
// authorization issuance and revocation successors. PersistenceID must be
// stable so repeated recovery wiring coalesces instead of accumulating
// duplicate callbacks. Persist runs before the in-memory ledger mutation;
// failure therefore fails closed and leaves the current ledger unchanged.
type PublicationAuthorizationPersistence interface {
	PersistenceID() string
	PersistPublicationAuthorization(ledgerKey string, authorization PublicationAuthorization, decision PublicationDecisionBinding) error
}

// EdgeRuntime is the issuance/revocation/current-ledger recheck runtime of
// the three typed cross-domain edges (ADR 0018 §3/§7). The issuer is always
// the Core AuthorityNamespaceId supplied at construction; individual
// issuance requests carry no issuer and can therefore never forge,
// impersonate or substitute it. Revocation replaces the current ledger
// record with a revoked successor fact; the issuance digest remains the
// stable ledger key so a revoked edge stays recoverable and every later use
// fails closed. Use replays coalesce idempotently on the canonical replay
// key (edge reference + operation request digest).
//
// The runtime is the in-process authority ledger seam of M9-b: durable
// sink-backed persistence switches in once the M9-a atomic sink lands; the
// issuance/revocation/recheck semantics frozen here do not change.
type EdgeRuntime struct {
	issuer AuthorityNamespaceId

	mu               sync.Mutex
	leaseResolver    LeaseActiveResolver
	targetResolver   TargetEligibilityResolver
	revokeHook       SecurityCriticalRevokeHook
	dispatchEdges    map[string]dispatchEdgeEntry
	materialEdges    map[string]materialEdgeEntry
	publicationEdges map[string]publicationEdgeEntry
	publicationSinks map[string]PublicationAuthorizationPersistence
	revokedAliases   map[string]string
	useReplays       map[string]struct{}
	audit            []EdgeAuditRecord
	nextAudit        int64
}

// NewEdgeRuntime constructs the edge runtime for one Core
// AuthorityNamespaceId. An invalid namespace fails closed: a runtime
// without a well-formed Core issuer can never issue.
func NewEdgeRuntime(coreNamespace AuthorityNamespaceId) (*EdgeRuntime, error) {
	if err := coreNamespace.Validate(); err != nil {
		return nil, fmt.Errorf("authority: edge runtime issuer must be a valid Core authorityNamespaceId: %w", err)
	}
	return &EdgeRuntime{
		issuer:           coreNamespace,
		dispatchEdges:    map[string]dispatchEdgeEntry{},
		materialEdges:    map[string]materialEdgeEntry{},
		publicationEdges: map[string]publicationEdgeEntry{},
		publicationSinks: map[string]PublicationAuthorizationPersistence{},
		revokedAliases:   map[string]string{},
		useReplays:       map[string]struct{}{},
		nextAudit:        1,
	}, nil
}

// BindPublicationAuthorizationPersistence binds a durable sink used by every
// later publication authorization issuance/revocation. Rebinding the same
// stable ID is idempotent and replaces only the equivalent recovery handle.
func (r *EdgeRuntime) BindPublicationAuthorizationPersistence(sink PublicationAuthorizationPersistence) error {
	if r == nil || sink == nil || sink.PersistenceID() == "" {
		return errors.New("authority: publication authorization persistence requires a stable sink")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.publicationSinks[sink.PersistenceID()] = sink
	return nil
}

func (r *EdgeRuntime) persistPublicationAuthorization(ledgerKey string, authorization PublicationAuthorization, decision PublicationDecisionBinding) error {
	for _, sink := range r.publicationSinks {
		if err := sink.PersistPublicationAuthorization(ledgerKey, authorization, decision); err != nil {
			return err
		}
	}
	return nil
}

// Issuer returns the Core AuthorityNamespaceId every issued edge carries.
func (r *EdgeRuntime) Issuer() AuthorityNamespaceId {
	if r == nil {
		return AuthorityNamespaceId{}
	}
	return r.issuer
}

// BindLeaseResolver binds the current dispatch-ledger resolver consulted by
// every dispatch-edge recheck. Rechecks fail closed while it is unbound.
func (r *EdgeRuntime) BindLeaseResolver(resolver LeaseActiveResolver) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.leaseResolver = resolver
}

// BindTargetEligibilityResolver binds the current-ledger resolver that
// re-adjudicates the target actor registration/snapshot/evidence
// eligibility on every recheck. Rechecks fail closed while it is unbound.
func (r *EdgeRuntime) BindTargetEligibilityResolver(resolver TargetEligibilityResolver) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.targetResolver = resolver
}

// BindSecurityCriticalRevokeHook binds the immediate-effect seam fired by
// security-critical revocations.
func (r *EdgeRuntime) BindSecurityCriticalRevokeHook(hook SecurityCriticalRevokeHook) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.revokeHook = hook
}

// appendAudit records one audit fact. The caller must hold r.mu.
func (r *EdgeRuntime) appendAudit(action EdgeAuditAction, kind EdgeKind, edgeDigest, replayKey, reason string, now time.Time) {
	r.audit = append(r.audit, EdgeAuditRecord{
		Sequence:   r.nextAudit,
		Action:     action,
		EdgeKind:   kind,
		EdgeDigest: edgeDigest,
		ReplayKey:  replayKey,
		Reason:     reason,
		At:         now.UTC(),
	})
	r.nextAudit++
}

// AuditTrail returns a copy of every audit fact in append order.
func (r *EdgeRuntime) AuditTrail() []EdgeAuditRecord {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	trail := make([]EdgeAuditRecord, len(r.audit))
	copy(trail, r.audit)
	return trail
}

// edgeUseReplayKey derives the canonical replay key of one edge use: the
// edge reference plus the operation request digest. Identical uses coalesce
// onto the identical key; any other request digest opens a distinct replay
// identity.
func edgeUseReplayKey(edgeDigest, requestDigest string) (string, error) {
	canonical, err := canonicalJSON(struct {
		EdgeDigest    string `json:"edgeDigest"`
		RequestDigest string `json:"requestDigest"`
	}{EdgeDigest: edgeDigest, RequestDigest: requestDigest})
	if err != nil {
		return "", fmt.Errorf("authority: edge use replay key: %w", err)
	}
	return digestBytes(canonical), nil
}

// IssueDispatchResultCapability issues one DispatchResultCapability under
// the Core issuer, sealed through the frozen record-layer Digest/Validate,
// and records it together with the lease binding in the authority ledger.
// Identical issuance replays coalesce idempotently and return the current
// ledger record (which may be the revoked successor).
func (r *EdgeRuntime) IssueDispatchResultCapability(request DispatchResultIssuance, now time.Time) (DispatchResultCapability, error) {
	if r == nil {
		return DispatchResultCapability{}, errors.New("authority: edge runtime is not initialized")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	kind := EdgeKindDispatchResultCapability
	if err := request.validate(); err != nil {
		wrapped := fmt.Errorf("authority: dispatchResultCapability: issuance rejected: %w", err)
		r.appendAudit(EdgeAuditIssuanceRejected, kind, "", "", wrapped.Error(), now)
		return DispatchResultCapability{}, wrapped
	}
	edge := DispatchResultCapability{
		Issuer:            r.issuer,
		SourceActor:       request.SourceActor,
		TargetActor:       request.TargetActor,
		Operation:         request.Operation,
		BoundAttemptId:    request.BoundAttemptId,
		BoundAllocationId: request.BoundAllocationId,
		Expiry:            request.Expiry,
		Generation:        1,
	}
	digest, err := edge.Digest()
	if err != nil {
		wrapped := fmt.Errorf("authority: dispatchResultCapability: issuance rejected: %w", err)
		r.appendAudit(EdgeAuditIssuanceRejected, kind, "", "", wrapped.Error(), now)
		return DispatchResultCapability{}, wrapped
	}
	edge.EdgeDigest = digest
	if err := edge.Validate(); err != nil {
		wrapped := fmt.Errorf("authority: dispatchResultCapability: issuance rejected: %w", err)
		r.appendAudit(EdgeAuditIssuanceRejected, kind, digest, "", wrapped.Error(), now)
		return DispatchResultCapability{}, wrapped
	}
	if existing, recorded := r.dispatchEdges[digest]; recorded {
		r.appendAudit(EdgeAuditIssuanceMerged, kind, digest, "", "", now)
		return existing.Edge, nil
	}
	r.dispatchEdges[digest] = dispatchEdgeEntry{Edge: edge, Lease: request.LeaseBinding}
	r.appendAudit(EdgeAuditIssued, kind, digest, "", "", now)
	return edge, nil
}

// IssueMaterialAccessGrant issues one MaterialAccessGrant under the Core
// issuer with an expiry bounded by the attempt boundary, and records it
// together with the attempt binding in the authority ledger. Identical
// issuance replays coalesce idempotently.
func (r *EdgeRuntime) IssueMaterialAccessGrant(request MaterialAccessIssuance, now time.Time) (MaterialAccessGrant, error) {
	if r == nil {
		return MaterialAccessGrant{}, errors.New("authority: edge runtime is not initialized")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	kind := EdgeKindMaterialAccessGrant
	if err := request.validate(); err != nil {
		wrapped := fmt.Errorf("authority: materialAccessGrant: issuance rejected: %w", err)
		r.appendAudit(EdgeAuditIssuanceRejected, kind, "", "", wrapped.Error(), now)
		return MaterialAccessGrant{}, wrapped
	}
	grant := MaterialAccessGrant{
		Issuer:           r.issuer,
		SourceActor:      request.SourceActor,
		TargetActor:      request.TargetActor,
		Operation:        request.Operation,
		MaterialId:       request.MaterialId,
		ScopeRestriction: request.ScopeRestriction,
		Expiry:           request.Expiry,
		Generation:       1,
	}
	digest, err := grant.Digest()
	if err != nil {
		wrapped := fmt.Errorf("authority: materialAccessGrant: issuance rejected: %w", err)
		r.appendAudit(EdgeAuditIssuanceRejected, kind, "", "", wrapped.Error(), now)
		return MaterialAccessGrant{}, wrapped
	}
	grant.EdgeDigest = digest
	if err := grant.Validate(); err != nil {
		wrapped := fmt.Errorf("authority: materialAccessGrant: issuance rejected: %w", err)
		r.appendAudit(EdgeAuditIssuanceRejected, kind, digest, "", wrapped.Error(), now)
		return MaterialAccessGrant{}, wrapped
	}
	if existing, recorded := r.materialEdges[digest]; recorded {
		r.appendAudit(EdgeAuditIssuanceMerged, kind, digest, "", "", now)
		return existing.Grant, nil
	}
	r.materialEdges[digest] = materialEdgeEntry{
		Grant: grant,
		Binding: MaterialAttemptBinding{
			AttemptId:       request.AttemptId,
			AllocationId:    request.AllocationId,
			AttemptBoundary: request.AttemptBoundary,
		},
	}
	r.appendAudit(EdgeAuditIssued, kind, digest, "", "", now)
	return grant, nil
}

// IssuePublicationAuthorization issues one PublicationAuthorization under
// the Core issuer bound to the frozen decision digests, and records it in
// the authority ledger. Identical issuance replays coalesce idempotently.
func (r *EdgeRuntime) IssuePublicationAuthorization(request PublicationIssuance, now time.Time) (PublicationAuthorization, error) {
	if r == nil {
		return PublicationAuthorization{}, errors.New("authority: edge runtime is not initialized")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	kind := EdgeKindPublicationAuthorization
	if err := request.validate(); err != nil {
		wrapped := fmt.Errorf("authority: publicationAuthorization: issuance rejected: %w", err)
		r.appendAudit(EdgeAuditIssuanceRejected, kind, "", "", wrapped.Error(), now)
		return PublicationAuthorization{}, wrapped
	}
	authorization := PublicationAuthorization{
		Issuer:                 r.issuer,
		SourceActor:            request.SourceActor,
		TargetActor:            request.TargetActor,
		Operation:              request.Operation,
		BoundPublicationDigest: request.BoundPublicationDigest,
		ExpectedPrincipal:      request.ExpectedPrincipal,
		Expiry:                 request.Expiry,
		Generation:             1,
	}
	digest, err := authorization.Digest()
	if err != nil {
		wrapped := fmt.Errorf("authority: publicationAuthorization: issuance rejected: %w", err)
		r.appendAudit(EdgeAuditIssuanceRejected, kind, "", "", wrapped.Error(), now)
		return PublicationAuthorization{}, wrapped
	}
	authorization.EdgeDigest = digest
	if err := authorization.Validate(); err != nil {
		wrapped := fmt.Errorf("authority: publicationAuthorization: issuance rejected: %w", err)
		r.appendAudit(EdgeAuditIssuanceRejected, kind, digest, "", wrapped.Error(), now)
		return PublicationAuthorization{}, wrapped
	}
	if existing, recorded := r.publicationEdges[digest]; recorded {
		if err := r.persistPublicationAuthorization(digest, existing.Authorization, existing.Decision); err != nil {
			wrapped := fmt.Errorf("authority: publicationAuthorization: durable issuance replay rejected: %w", err)
			r.appendAudit(EdgeAuditIssuanceRejected, kind, digest, "", wrapped.Error(), now)
			return PublicationAuthorization{}, wrapped
		}
		r.appendAudit(EdgeAuditIssuanceMerged, kind, digest, "", "", now)
		return existing.Authorization, nil
	}
	if err := r.persistPublicationAuthorization(digest, authorization, request.DecisionBinding); err != nil {
		wrapped := fmt.Errorf("authority: publicationAuthorization: durable issuance rejected: %w", err)
		r.appendAudit(EdgeAuditIssuanceRejected, kind, digest, "", wrapped.Error(), now)
		return PublicationAuthorization{}, wrapped
	}
	r.publicationEdges[digest] = publicationEdgeEntry{Authorization: authorization, Decision: request.DecisionBinding}
	r.appendAudit(EdgeAuditIssued, kind, digest, "", "", now)
	return authorization, nil
}

// RevokeDispatchResultCapability records the revocation of one dispatch
// result capability as an authority ledger fact: the current entry moves to
// the revoked successor record carrying revocationGeneration, the issuance
// digest stays the stable ledger key and the successor digest is aliased so
// both spellings of the edge resolve to the revocation. Revocation is
// idempotent; a security-critical revocation fires the immediate-effect hook
// after the fact is recorded (hook failures never roll back the fact).
func (r *EdgeRuntime) RevokeDispatchResultCapability(edgeDigest string, reason EdgeRevocationReason, now time.Time) (DispatchResultCapability, error) {
	if r == nil {
		return DispatchResultCapability{}, errors.New("authority: edge runtime is not initialized")
	}
	if err := reason.Validate(); err != nil {
		return DispatchResultCapability{}, err
	}
	r.mu.Lock()
	kind := EdgeKindDispatchResultCapability
	entry, recorded := r.dispatchEdges[edgeDigest]
	if !recorded {
		wrapped := fmt.Errorf("%w: %s", ErrEdgeNotRecorded, edgeDigest)
		r.appendAudit(EdgeAuditRevocationRejected, kind, edgeDigest, "", wrapped.Error(), now)
		r.mu.Unlock()
		return DispatchResultCapability{}, wrapped
	}
	if entry.Edge.RevocationGeneration > 0 {
		current := entry.Edge
		r.mu.Unlock()
		return current, nil
	}
	revoked := entry.Edge
	revoked.RevocationGeneration = revoked.Generation + 1
	revoked.EdgeDigest = ""
	revokedDigest, err := revoked.Digest()
	if err != nil {
		r.mu.Unlock()
		return DispatchResultCapability{}, fmt.Errorf("authority: dispatchResultCapability: revocation rejected: %w", err)
	}
	revoked.EdgeDigest = revokedDigest
	if err := revoked.Validate(); err != nil {
		r.mu.Unlock()
		return DispatchResultCapability{}, fmt.Errorf("authority: dispatchResultCapability: revocation rejected: %w", err)
	}
	entry.Edge = revoked
	r.dispatchEdges[edgeDigest] = entry
	r.revokedAliases[revokedDigest] = edgeDigest
	r.appendAudit(EdgeAuditRevoked, kind, edgeDigest, "", string(reason), now)
	hook := r.revokeHook
	r.mu.Unlock()
	if reason == EdgeRevocationSecurityCritical && hook != nil {
		if hookErr := hook.OnSecurityCriticalRevoke(kind, edgeDigest, now); hookErr != nil {
			return revoked, fmt.Errorf("authority: dispatchResultCapability: revocation recorded, immediate-effect hook failed: %w", hookErr)
		}
	}
	return revoked, nil
}

// RevokeMaterialAccessGrant records the revocation of one material access
// grant as an authority ledger fact. Semantics are identical to
// RevokeDispatchResultCapability.
func (r *EdgeRuntime) RevokeMaterialAccessGrant(edgeDigest string, reason EdgeRevocationReason, now time.Time) (MaterialAccessGrant, error) {
	if r == nil {
		return MaterialAccessGrant{}, errors.New("authority: edge runtime is not initialized")
	}
	if err := reason.Validate(); err != nil {
		return MaterialAccessGrant{}, err
	}
	r.mu.Lock()
	kind := EdgeKindMaterialAccessGrant
	entry, recorded := r.materialEdges[edgeDigest]
	if !recorded {
		wrapped := fmt.Errorf("%w: %s", ErrEdgeNotRecorded, edgeDigest)
		r.appendAudit(EdgeAuditRevocationRejected, kind, edgeDigest, "", wrapped.Error(), now)
		r.mu.Unlock()
		return MaterialAccessGrant{}, wrapped
	}
	if entry.Grant.RevocationGeneration > 0 {
		current := entry.Grant
		r.mu.Unlock()
		return current, nil
	}
	revoked := entry.Grant
	revoked.RevocationGeneration = revoked.Generation + 1
	revoked.EdgeDigest = ""
	revokedDigest, err := revoked.Digest()
	if err != nil {
		r.mu.Unlock()
		return MaterialAccessGrant{}, fmt.Errorf("authority: materialAccessGrant: revocation rejected: %w", err)
	}
	revoked.EdgeDigest = revokedDigest
	if err := revoked.Validate(); err != nil {
		r.mu.Unlock()
		return MaterialAccessGrant{}, fmt.Errorf("authority: materialAccessGrant: revocation rejected: %w", err)
	}
	entry.Grant = revoked
	r.materialEdges[edgeDigest] = entry
	r.revokedAliases[revokedDigest] = edgeDigest
	r.appendAudit(EdgeAuditRevoked, kind, edgeDigest, "", string(reason), now)
	hook := r.revokeHook
	r.mu.Unlock()
	if reason == EdgeRevocationSecurityCritical && hook != nil {
		if hookErr := hook.OnSecurityCriticalRevoke(kind, edgeDigest, now); hookErr != nil {
			return revoked, fmt.Errorf("authority: materialAccessGrant: revocation recorded, immediate-effect hook failed: %w", hookErr)
		}
	}
	return revoked, nil
}

// RevokePublicationAuthorization records the revocation of one publication
// authorization as an authority ledger fact. Semantics are identical to
// RevokeDispatchResultCapability.
func (r *EdgeRuntime) RevokePublicationAuthorization(edgeDigest string, reason EdgeRevocationReason, now time.Time) (PublicationAuthorization, error) {
	if r == nil {
		return PublicationAuthorization{}, errors.New("authority: edge runtime is not initialized")
	}
	if err := reason.Validate(); err != nil {
		return PublicationAuthorization{}, err
	}
	r.mu.Lock()
	kind := EdgeKindPublicationAuthorization
	entry, recorded := r.publicationEdges[edgeDigest]
	if !recorded {
		wrapped := fmt.Errorf("%w: %s", ErrEdgeNotRecorded, edgeDigest)
		r.appendAudit(EdgeAuditRevocationRejected, kind, edgeDigest, "", wrapped.Error(), now)
		r.mu.Unlock()
		return PublicationAuthorization{}, wrapped
	}
	if entry.Authorization.RevocationGeneration > 0 {
		current := entry.Authorization
		r.mu.Unlock()
		return current, nil
	}
	revoked := entry.Authorization
	revoked.RevocationGeneration = revoked.Generation + 1
	revoked.EdgeDigest = ""
	revokedDigest, err := revoked.Digest()
	if err != nil {
		r.mu.Unlock()
		return PublicationAuthorization{}, fmt.Errorf("authority: publicationAuthorization: revocation rejected: %w", err)
	}
	revoked.EdgeDigest = revokedDigest
	if err := revoked.Validate(); err != nil {
		r.mu.Unlock()
		return PublicationAuthorization{}, fmt.Errorf("authority: publicationAuthorization: revocation rejected: %w", err)
	}
	if err := r.persistPublicationAuthorization(edgeDigest, revoked, entry.Decision); err != nil {
		wrapped := fmt.Errorf("authority: publicationAuthorization: durable revocation rejected: %w", err)
		r.appendAudit(EdgeAuditRevocationRejected, kind, edgeDigest, "", wrapped.Error(), now)
		r.mu.Unlock()
		return PublicationAuthorization{}, wrapped
	}
	entry.Authorization = revoked
	r.publicationEdges[edgeDigest] = entry
	r.revokedAliases[revokedDigest] = edgeDigest
	r.appendAudit(EdgeAuditRevoked, kind, edgeDigest, "", string(reason), now)
	hook := r.revokeHook
	r.mu.Unlock()
	if reason == EdgeRevocationSecurityCritical && hook != nil {
		if hookErr := hook.OnSecurityCriticalRevoke(kind, edgeDigest, now); hookErr != nil {
			return revoked, fmt.Errorf("authority: publicationAuthorization: revocation recorded, immediate-effect hook failed: %w", hookErr)
		}
	}
	return revoked, nil
}

// CurrentDispatchResultCapability recovers the current ledger record and
// the issuance lease binding recorded under edgeDigest. Both the issuance
// digest and the revoked successor digest resolve.
func (r *EdgeRuntime) CurrentDispatchResultCapability(edgeDigest string) (DispatchResultCapability, EdgeLeaseBinding, bool) {
	if r == nil {
		return DispatchResultCapability{}, EdgeLeaseBinding{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := edgeDigest
	if alias, aliased := r.revokedAliases[edgeDigest]; aliased {
		key = alias
	}
	entry, recorded := r.dispatchEdges[key]
	if !recorded {
		return DispatchResultCapability{}, EdgeLeaseBinding{}, false
	}
	return entry.Edge, entry.Lease, true
}

// CurrentMaterialAccessGrant recovers the current ledger record and the
// issuance attempt binding recorded under edgeDigest.
func (r *EdgeRuntime) CurrentMaterialAccessGrant(edgeDigest string) (MaterialAccessGrant, MaterialAttemptBinding, bool) {
	if r == nil {
		return MaterialAccessGrant{}, MaterialAttemptBinding{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := edgeDigest
	if alias, aliased := r.revokedAliases[edgeDigest]; aliased {
		key = alias
	}
	entry, recorded := r.materialEdges[key]
	if !recorded {
		return MaterialAccessGrant{}, MaterialAttemptBinding{}, false
	}
	return entry.Grant, entry.Binding, true
}

// CurrentPublicationAuthorization recovers the current ledger record and the
// issuance decision binding recorded under edgeDigest.
func (r *EdgeRuntime) CurrentPublicationAuthorization(edgeDigest string) (PublicationAuthorization, PublicationDecisionBinding, bool) {
	if r == nil {
		return PublicationAuthorization{}, PublicationDecisionBinding{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := edgeDigest
	if alias, aliased := r.revokedAliases[edgeDigest]; aliased {
		key = alias
	}
	entry, recorded := r.publicationEdges[key]
	if !recorded {
		return PublicationAuthorization{}, PublicationDecisionBinding{}, false
	}
	return entry.Authorization, entry.Decision, true
}

// RestorePublicationAuthorization hydrates an already-issued durable
// PublicationAuthorization into a newly constructed runtime. It is not an
// issuance path: callers must present the original ledger key, the complete
// current record (including any revocation successor), and the frozen
// decision binding. This is the crash-recovery counterpart of current-ledger
// recheck; it never turns a revoked record back into an active authorization.
func (r *EdgeRuntime) RestorePublicationAuthorization(ledgerKey string, authorization PublicationAuthorization, decision PublicationDecisionBinding) error {
	if r == nil {
		return errors.New("authority: edge runtime is not initialized")
	}
	if err := authorization.Validate(); err != nil {
		return err
	}
	if err := decision.Validate(); err != nil {
		return err
	}
	if !authorization.Issuer.Equal(r.issuer) {
		return errors.New("authority: durable publication authorization issuer does not match this runtime")
	}
	issuance := authorization
	issuance.RevocationGeneration = 0
	issuance.EdgeDigest = ""
	issuanceDigest, err := issuance.Digest()
	if err != nil {
		return err
	}
	if ledgerKey != issuanceDigest {
		return errors.New("authority: durable publication authorization ledger key is not its issuance digest")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.publicationEdges[ledgerKey]; ok {
		if existing.Authorization != authorization || existing.Decision != decision {
			return ErrEdgeDiverged
		}
		return nil
	}
	r.publicationEdges[ledgerKey] = publicationEdgeEntry{Authorization: authorization, Decision: decision}
	if authorization.RevocationGeneration > 0 {
		r.revokedAliases[authorization.EdgeDigest] = ledgerKey
	}
	return nil
}

// RecheckDispatchResult is the current-ledger recheck of one result-ingress
// use of a DispatchResultCapability (ADR 0018 §3): the resolvers must be
// bound, the presented edge must validate and be recorded in the current
// authority ledger, the current record must be unrevoked/unexpired and must
// match the presented record exactly, the use request must match every
// binding (operation, source/target, attempt/allocation, lease identity)
// exactly, the target actor must still be eligible and the bound lease must
// still be active. Any unsatisfied item fails closed with an audit record.
// Identical accepted uses coalesce idempotently on the canonical replay key
// (edge reference + operation request digest).
func (r *EdgeRuntime) RecheckDispatchResult(presented DispatchResultCapability, request DispatchResultUseRequest, now time.Time) error {
	if r == nil {
		return errors.New("authority: edge runtime is not initialized")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	kind := EdgeKindDispatchResultCapability
	reject := func(stage string, err error) error {
		wrapped := fmt.Errorf("authority: dispatchResultCapability: recheck rejected: %s: %w", stage, err)
		r.appendAudit(EdgeAuditUseRejected, kind, presented.EdgeDigest, "", wrapped.Error(), now)
		return wrapped
	}
	if r.leaseResolver == nil || r.targetResolver == nil {
		return reject("resolver binding", ErrEdgeResolverUnbound)
	}
	if err := request.validate(); err != nil {
		return reject("use request", err)
	}
	if err := presented.Validate(); err != nil {
		return reject("structural validation", err)
	}
	key := presented.EdgeDigest
	if alias, aliased := r.revokedAliases[key]; aliased {
		key = alias
	}
	entry, recorded := r.dispatchEdges[key]
	if !recorded {
		return reject("authority ledger", fmt.Errorf("%w: %s", ErrEdgeNotRecorded, presented.EdgeDigest))
	}
	if entry.Edge.RevocationGeneration > 0 {
		return reject("use-time validity", fmt.Errorf("%w: the authority ledger records revocationGeneration %d", ErrEdgeRevoked, entry.Edge.RevocationGeneration))
	}
	if entry.Edge != presented {
		return reject("authority ledger", ErrEdgeDiverged)
	}
	if err := entry.Edge.ValidAt(now); err != nil {
		return reject("use-time validity", err)
	}
	if request.Operation != entry.Edge.Operation ||
		!request.SourceActor.Equal(entry.Edge.SourceActor) ||
		!request.TargetActor.Equal(entry.Edge.TargetActor) ||
		request.AttemptId != entry.Edge.BoundAttemptId ||
		request.AllocationId != entry.Edge.BoundAllocationId {
		return reject("binding match", ErrEdgeBindingMismatch)
	}
	if request.LeaseId != entry.Lease.LeaseId ||
		request.Generation != entry.Lease.Generation ||
		request.FencingToken != entry.Lease.FencingToken ||
		request.AttemptId != entry.Lease.AttemptId ||
		request.AllocationId != entry.Lease.AllocationId {
		return reject("lease binding", ErrEdgeBindingMismatch)
	}
	eligible, err := r.targetResolver.TargetEligible(entry.Edge.TargetActor)
	if err != nil {
		return reject("target eligibility", err)
	}
	if !eligible {
		return reject("target eligibility", fmt.Errorf("%w: target actor is no longer eligible in the current ledger", ErrEdgeTargetIneligible))
	}
	active, err := r.leaseResolver.LeaseActive(entry.Lease.LeaseId, entry.Lease.Generation, entry.Lease.FencingToken)
	if err != nil {
		return reject("lease ledger", err)
	}
	if !active {
		return reject("lease ledger", fmt.Errorf("%w: lease %s is no longer active in the current dispatch ledger", ErrEdgeLeaseInactive, entry.Lease.LeaseId))
	}
	replayKey, err := edgeUseReplayKey(presented.EdgeDigest, request.RequestDigest)
	if err != nil {
		return reject("replay key", err)
	}
	if _, merged := r.useReplays[replayKey]; merged {
		r.appendAudit(EdgeAuditUseReplayMerged, kind, presented.EdgeDigest, replayKey, "", now)
		return nil
	}
	r.useReplays[replayKey] = struct{}{}
	r.appendAudit(EdgeAuditUseAccepted, kind, presented.EdgeDigest, replayKey, "", now)
	return nil
}

// RecheckMaterialAccess is the current-ledger recheck of one use of a
// MaterialAccessGrant. The lease resolver does not participate; every other
// recheck item is identical to RecheckDispatchResult: the grant must be
// recorded, current, unrevoked/unexpired, the use request must match every
// binding (operation, source/target, material, attempt/allocation) exactly
// and the target actor must still be eligible. Any unsatisfied item fails
// closed with an audit record.
func (r *EdgeRuntime) RecheckMaterialAccess(presented MaterialAccessGrant, request MaterialAccessUseRequest, now time.Time) error {
	if r == nil {
		return errors.New("authority: edge runtime is not initialized")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	kind := EdgeKindMaterialAccessGrant
	reject := func(stage string, err error) error {
		wrapped := fmt.Errorf("authority: materialAccessGrant: recheck rejected: %s: %w", stage, err)
		r.appendAudit(EdgeAuditUseRejected, kind, presented.EdgeDigest, "", wrapped.Error(), now)
		return wrapped
	}
	if r.targetResolver == nil {
		return reject("resolver binding", ErrEdgeResolverUnbound)
	}
	if err := request.validate(); err != nil {
		return reject("use request", err)
	}
	if err := presented.Validate(); err != nil {
		return reject("structural validation", err)
	}
	key := presented.EdgeDigest
	if alias, aliased := r.revokedAliases[key]; aliased {
		key = alias
	}
	entry, recorded := r.materialEdges[key]
	if !recorded {
		return reject("authority ledger", fmt.Errorf("%w: %s", ErrEdgeNotRecorded, presented.EdgeDigest))
	}
	if entry.Grant.RevocationGeneration > 0 {
		return reject("use-time validity", fmt.Errorf("%w: the authority ledger records revocationGeneration %d", ErrEdgeRevoked, entry.Grant.RevocationGeneration))
	}
	if entry.Grant != presented {
		return reject("authority ledger", ErrEdgeDiverged)
	}
	if err := entry.Grant.ValidAt(now); err != nil {
		return reject("use-time validity", err)
	}
	if request.Operation != entry.Grant.Operation ||
		!request.SourceActor.Equal(entry.Grant.SourceActor) ||
		!request.TargetActor.Equal(entry.Grant.TargetActor) ||
		request.MaterialId != entry.Grant.MaterialId ||
		request.AttemptId != entry.Binding.AttemptId ||
		request.AllocationId != entry.Binding.AllocationId {
		return reject("binding match", ErrEdgeBindingMismatch)
	}
	eligible, err := r.targetResolver.TargetEligible(entry.Grant.TargetActor)
	if err != nil {
		return reject("target eligibility", err)
	}
	if !eligible {
		return reject("target eligibility", fmt.Errorf("%w: target actor is no longer eligible in the current ledger", ErrEdgeTargetIneligible))
	}
	replayKey, err := edgeUseReplayKey(presented.EdgeDigest, request.RequestDigest)
	if err != nil {
		return reject("replay key", err)
	}
	if _, merged := r.useReplays[replayKey]; merged {
		r.appendAudit(EdgeAuditUseReplayMerged, kind, presented.EdgeDigest, replayKey, "", now)
		return nil
	}
	r.useReplays[replayKey] = struct{}{}
	r.appendAudit(EdgeAuditUseAccepted, kind, presented.EdgeDigest, replayKey, "", now)
	return nil
}

// RecheckPublicationAuthorization is the current-ledger recheck of one use
// of a PublicationAuthorization. In addition to the shared recheck items,
// the SideEffectIntent/ReviewDecision/evidence digests must remain exactly
// the digests the authorization was issued for; any changed decision digest
// fails closed. Any unsatisfied item fails closed with an audit record.
func (r *EdgeRuntime) RecheckPublicationAuthorization(presented PublicationAuthorization, request PublicationUseRequest, now time.Time) error {
	if r == nil {
		return errors.New("authority: edge runtime is not initialized")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	kind := EdgeKindPublicationAuthorization
	reject := func(stage string, err error) error {
		wrapped := fmt.Errorf("authority: publicationAuthorization: recheck rejected: %s: %w", stage, err)
		r.appendAudit(EdgeAuditUseRejected, kind, presented.EdgeDigest, "", wrapped.Error(), now)
		return wrapped
	}
	if r.targetResolver == nil {
		return reject("resolver binding", ErrEdgeResolverUnbound)
	}
	if err := request.validate(); err != nil {
		return reject("use request", err)
	}
	if err := presented.Validate(); err != nil {
		return reject("structural validation", err)
	}
	key := presented.EdgeDigest
	if alias, aliased := r.revokedAliases[key]; aliased {
		key = alias
	}
	entry, recorded := r.publicationEdges[key]
	if !recorded {
		return reject("authority ledger", fmt.Errorf("%w: %s", ErrEdgeNotRecorded, presented.EdgeDigest))
	}
	if entry.Authorization.RevocationGeneration > 0 {
		return reject("use-time validity", fmt.Errorf("%w: the authority ledger records revocationGeneration %d", ErrEdgeRevoked, entry.Authorization.RevocationGeneration))
	}
	if entry.Authorization != presented {
		return reject("authority ledger", ErrEdgeDiverged)
	}
	if err := entry.Authorization.ValidAt(now); err != nil {
		return reject("use-time validity", err)
	}
	if request.Operation != entry.Authorization.Operation ||
		!request.SourceActor.Equal(entry.Authorization.SourceActor) ||
		!request.TargetActor.Equal(entry.Authorization.TargetActor) ||
		request.PublicationDigest != entry.Authorization.BoundPublicationDigest {
		return reject("binding match", ErrEdgeBindingMismatch)
	}
	if request.ExpectedPrincipal != entry.Authorization.ExpectedPrincipal {
		return reject("principal binding", ErrEdgeBindingMismatch)
	}
	if request.SideEffectIntentDigest != entry.Decision.SideEffectIntentDigest ||
		request.ReviewDecisionDigest != entry.Decision.ReviewDecisionDigest ||
		request.EvidenceDigest != entry.Decision.EvidenceDigest {
		return reject("decision binding", ErrEdgeBindingMismatch)
	}
	eligible, err := r.targetResolver.TargetEligible(entry.Authorization.TargetActor)
	if err != nil {
		return reject("target eligibility", err)
	}
	if !eligible {
		return reject("target eligibility", fmt.Errorf("%w: target actor is no longer eligible in the current ledger", ErrEdgeTargetIneligible))
	}
	replayKey, err := edgeUseReplayKey(presented.EdgeDigest, request.RequestDigest)
	if err != nil {
		return reject("replay key", err)
	}
	if _, merged := r.useReplays[replayKey]; merged {
		r.appendAudit(EdgeAuditUseReplayMerged, kind, presented.EdgeDigest, replayKey, "", now)
		return nil
	}
	r.useReplays[replayKey] = struct{}{}
	r.appendAudit(EdgeAuditUseAccepted, kind, presented.EdgeDigest, replayKey, "", now)
	return nil
}
