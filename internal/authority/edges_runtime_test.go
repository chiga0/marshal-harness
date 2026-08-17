package authority

import (
	"errors"
	"testing"
	"time"
)

// edgeRuntimeNow is the reference clock of the edge runtime fixtures: after
// the issuance instants and before the default expiry values.
var edgeRuntimeNow = time.Date(2026, time.August, 13, 0, 0, 0, 0, time.UTC)

// runtimeAttemptBoundary is the attempt boundary of the material access
// fixtures: one day after edgeRuntimeNow.
var runtimeAttemptBoundary = time.Date(2026, time.August, 14, 0, 0, 0, 0, time.UTC)

// stubLeaseResolver is the controllable dispatch-ledger resolver of the edge
// runtime fixtures.
type stubLeaseResolver struct {
	active           bool
	err              error
	calls            int
	lastLeaseId      string
	lastGeneration   int64
	lastFencingToken string
}

func (s *stubLeaseResolver) LeaseActive(leaseId string, generation int64, fencingToken string) (bool, error) {
	s.calls++
	s.lastLeaseId, s.lastGeneration, s.lastFencingToken = leaseId, generation, fencingToken
	return s.active, s.err
}

// stubTargetResolver is the controllable target eligibility resolver of the
// edge runtime fixtures.
type stubTargetResolver struct {
	eligible bool
	err      error
	targets  []SecurityDomainId
}

func (s *stubTargetResolver) TargetEligible(target SecurityDomainId) (bool, error) {
	s.targets = append(s.targets, target)
	return s.eligible, s.err
}

// stubRevokeHook records the immediate-effect hook invocations.
type stubRevokeHook struct {
	calls []string
	err   error
}

func (h *stubRevokeHook) OnSecurityCriticalRevoke(kind EdgeKind, edgeDigest string, at time.Time) error {
	h.calls = append(h.calls, string(kind)+":"+edgeDigest)
	return h.err
}

func runtimeExecutionActor() SecurityDomainId {
	return securityDomainForKind(TrustDomainKindExecution, "isolation-execution")
}

func runtimeDataCapabilityActor() SecurityDomainId {
	return securityDomainForKind(TrustDomainKindDataCapability, "isolation-data-capability")
}

func runtimePublicationActor() SecurityDomainId {
	return securityDomainForKind(TrustDomainKindPublication, "isolation-publication")
}

// newEdgeRuntimeFixture builds an EdgeRuntime under the valid Core namespace
// with permissive resolvers bound; every recheck item except the mutated one
// passes by default.
func newEdgeRuntimeFixture(t *testing.T) (*EdgeRuntime, *stubLeaseResolver, *stubTargetResolver) {
	t.Helper()
	runtime, err := NewEdgeRuntime(validNamespace())
	if err != nil {
		t.Fatalf("NewEdgeRuntime: %v", err)
	}
	leaseResolver := &stubLeaseResolver{active: true}
	targetResolver := &stubTargetResolver{eligible: true}
	runtime.BindLeaseResolver(leaseResolver)
	runtime.BindTargetEligibilityResolver(targetResolver)
	return runtime, leaseResolver, targetResolver
}

func testEdgeLeaseBinding(suffix string) EdgeLeaseBinding {
	return EdgeLeaseBinding{
		LeaseId:      "lease-" + suffix,
		AttemptId:    "attempt-" + suffix,
		AllocationId: "allocation-" + suffix,
		Generation:   1,
		FencingToken: digestBytes([]byte("fencing-" + suffix)),
	}
}

func testDispatchResultIssuance(suffix string) DispatchResultIssuance {
	binding := testEdgeLeaseBinding(suffix)
	return DispatchResultIssuance{
		SourceActor:       runtimeExecutionActor(),
		TargetActor:       runtimeDataCapabilityActor(),
		Operation:         DispatchResultOperationAccept,
		BoundAttemptId:    binding.AttemptId,
		BoundAllocationId: binding.AllocationId,
		Expiry:            "2026-12-31T00:00:00Z",
		LeaseBinding:      binding,
	}
}

func issueRuntimeDispatchEdge(t *testing.T, runtime *EdgeRuntime, suffix string) (DispatchResultCapability, EdgeLeaseBinding) {
	t.Helper()
	request := testDispatchResultIssuance(suffix)
	edge, err := runtime.IssueDispatchResultCapability(request, edgeRuntimeNow)
	if err != nil {
		t.Fatalf("IssueDispatchResultCapability: %v", err)
	}
	return edge, request.LeaseBinding
}

func dispatchUseRequestFor(edge DispatchResultCapability, binding EdgeLeaseBinding, seed string) DispatchResultUseRequest {
	return DispatchResultUseRequest{
		SourceActor:   edge.SourceActor,
		TargetActor:   edge.TargetActor,
		Operation:     edge.Operation,
		AttemptId:     binding.AttemptId,
		AllocationId:  binding.AllocationId,
		LeaseId:       binding.LeaseId,
		Generation:    binding.Generation,
		FencingToken:  binding.FencingToken,
		RequestDigest: digestBytes([]byte(seed)),
	}
}

func testMaterialAccessIssuance(suffix string) MaterialAccessIssuance {
	return MaterialAccessIssuance{
		SourceActor:      runtimeExecutionActor(),
		TargetActor:      runtimeDataCapabilityActor(),
		Operation:        MaterialAccessOperationRead,
		MaterialId:       "material-" + suffix,
		ScopeRestriction: "sandbox-stage",
		AttemptId:        "attempt-" + suffix,
		AllocationId:     "allocation-" + suffix,
		AttemptBoundary:  runtimeAttemptBoundary,
		Expiry:           "2026-08-13T23:00:00Z",
	}
}

func materialUseRequestFor(grant MaterialAccessGrant, issuance MaterialAccessIssuance, seed string) MaterialAccessUseRequest {
	return MaterialAccessUseRequest{
		SourceActor:   grant.SourceActor,
		TargetActor:   grant.TargetActor,
		Operation:     grant.Operation,
		MaterialId:    grant.MaterialId,
		AttemptId:     issuance.AttemptId,
		AllocationId:  issuance.AllocationId,
		RequestDigest: digestBytes([]byte(seed)),
	}
}

func testPublicationIssuance(suffix string) PublicationIssuance {
	return PublicationIssuance{
		SourceActor:            runtimeExecutionActor(),
		TargetActor:            runtimePublicationActor(),
		Operation:              PublicationOperationSubmit,
		BoundPublicationDigest: digestBytes([]byte("publication-" + suffix)),
		ExpectedPrincipal:      "github-login:marshal-publisher",
		DecisionBinding: PublicationDecisionBinding{
			SideEffectIntentDigest: digestBytes([]byte("side-effect-intent-" + suffix)),
			ReviewDecisionDigest:   digestBytes([]byte("review-decision-" + suffix)),
			EvidenceDigest:         digestBytes([]byte("evidence-" + suffix)),
		},
		Expiry: "2026-12-31T00:00:00Z",
	}
}

func publicationUseRequestFor(authorization PublicationAuthorization, issuance PublicationIssuance, seed string) PublicationUseRequest {
	return PublicationUseRequest{
		SourceActor:            authorization.SourceActor,
		TargetActor:            authorization.TargetActor,
		Operation:              authorization.Operation,
		PublicationDigest:      authorization.BoundPublicationDigest,
		ExpectedPrincipal:      authorization.ExpectedPrincipal,
		SideEffectIntentDigest: issuance.DecisionBinding.SideEffectIntentDigest,
		ReviewDecisionDigest:   issuance.DecisionBinding.ReviewDecisionDigest,
		EvidenceDigest:         issuance.DecisionBinding.EvidenceDigest,
		RequestDigest:          digestBytes([]byte(seed)),
	}
}

func auditActions(trail []EdgeAuditRecord) []EdgeAuditAction {
	actions := make([]EdgeAuditAction, 0, len(trail))
	for _, record := range trail {
		actions = append(actions, record.Action)
	}
	return actions
}

func containsAuditAction(trail []EdgeAuditRecord, action EdgeAuditAction) bool {
	for _, record := range trail {
		if record.Action == action {
			return true
		}
	}
	return false
}

// TestEdgeRuntimeConstructionFailsClosed freezes the construction gate: an
// invalid Core namespace never constructs a runtime, and a nil runtime fails
// every issuance, revocation and recheck closed.
func TestEdgeRuntimeConstructionFailsClosed(t *testing.T) {
	for name, namespace := range map[string]AuthorityNamespaceId{
		"zero namespace":      {},
		"empty tenant":        {TenantNamespace: "", ControlPlaneId: "default", AuthorityScopeId: "scope"},
		"blank control plane": {TenantNamespace: "default", ControlPlaneId: "   ", AuthorityScopeId: "scope"},
		"empty scope":         {TenantNamespace: "default", ControlPlaneId: "default", AuthorityScopeId: ""},
	} {
		if _, err := NewEdgeRuntime(namespace); err == nil {
			t.Fatalf("NewEdgeRuntime accepted %s", name)
		}
	}

	var runtime *EdgeRuntime
	if _, err := runtime.IssueDispatchResultCapability(testDispatchResultIssuance("1"), edgeRuntimeNow); err == nil {
		t.Fatal("issuance succeeded on a nil runtime")
	}
	if _, err := runtime.RevokeDispatchResultCapability(digestBytes([]byte("edge")), EdgeRevocationOrdinary, edgeRuntimeNow); err == nil {
		t.Fatal("revocation succeeded on a nil runtime")
	}
	if err := runtime.RecheckDispatchResult(DispatchResultCapability{}, DispatchResultUseRequest{}, edgeRuntimeNow); err == nil {
		t.Fatal("recheck succeeded on a nil runtime")
	}
	if trail := runtime.AuditTrail(); trail != nil {
		t.Fatalf("a nil runtime must expose no audit trail, got %v", trail)
	}
	if issuer := runtime.Issuer(); issuer != (AuthorityNamespaceId{}) {
		t.Fatalf("a nil runtime must expose no issuer, got %v", issuer)
	}
}

// TestEdgeRuntimeIssuancePositiveBaseline freezes the positive baseline for
// all three edges: the issuer is mechanically the Core namespace, the
// issuance generation is 1, the sealed record validates through the frozen
// record layer, the record is recoverable from the ledger and the audit
// trail records the issuance.
func TestEdgeRuntimeIssuancePositiveBaseline(t *testing.T) {
	runtime, _, _ := newEdgeRuntimeFixture(t)

	dispatchRequest := testDispatchResultIssuance("1")
	dispatchEdge, err := runtime.IssueDispatchResultCapability(dispatchRequest, edgeRuntimeNow)
	if err != nil {
		t.Fatalf("IssueDispatchResultCapability: %v", err)
	}
	if !dispatchEdge.Issuer.Equal(validNamespace()) {
		t.Fatal("the issued dispatch edge must carry the Core issuer")
	}
	if dispatchEdge.Generation != 1 || dispatchEdge.RevocationGeneration != 0 {
		t.Fatalf("issuance must start at generation 1 unrevoked, got %d/%d", dispatchEdge.Generation, dispatchEdge.RevocationGeneration)
	}
	if err := dispatchEdge.Validate(); err != nil {
		t.Fatalf("the issued dispatch edge does not validate through the frozen record layer: %v", err)
	}
	replayKey, err := dispatchEdge.ReplayKey()
	if err != nil || replayKey != dispatchEdge.EdgeDigest {
		t.Fatalf("the replay key must equal the edge digest, got %s/%v", replayKey, err)
	}
	currentDispatch, currentLease, ok := runtime.CurrentDispatchResultCapability(dispatchEdge.EdgeDigest)
	if !ok || currentDispatch != dispatchEdge || currentLease != dispatchRequest.LeaseBinding {
		t.Fatal("the issued dispatch edge must be recoverable with its lease binding")
	}

	materialRequest := testMaterialAccessIssuance("1")
	materialGrant, err := runtime.IssueMaterialAccessGrant(materialRequest, edgeRuntimeNow)
	if err != nil {
		t.Fatalf("IssueMaterialAccessGrant: %v", err)
	}
	if !materialGrant.Issuer.Equal(validNamespace()) {
		t.Fatal("the issued material grant must carry the Core issuer")
	}
	if err := materialGrant.Validate(); err != nil {
		t.Fatalf("the issued material grant does not validate: %v", err)
	}
	currentGrant, currentBinding, ok := runtime.CurrentMaterialAccessGrant(materialGrant.EdgeDigest)
	if !ok || currentGrant != materialGrant || currentBinding.AttemptId != materialRequest.AttemptId || currentBinding.AttemptBoundary != runtimeAttemptBoundary {
		t.Fatal("the issued material grant must be recoverable with its attempt binding")
	}

	publicationRequest := testPublicationIssuance("1")
	publicationAuthorization, err := runtime.IssuePublicationAuthorization(publicationRequest, edgeRuntimeNow)
	if err != nil {
		t.Fatalf("IssuePublicationAuthorization: %v", err)
	}
	if !publicationAuthorization.Issuer.Equal(validNamespace()) {
		t.Fatal("the issued publication authorization must carry the Core issuer")
	}
	if err := publicationAuthorization.Validate(); err != nil {
		t.Fatalf("the issued publication authorization does not validate: %v", err)
	}
	currentAuthorization, currentDecision, ok := runtime.CurrentPublicationAuthorization(publicationAuthorization.EdgeDigest)
	if !ok || currentAuthorization != publicationAuthorization || currentDecision != publicationRequest.DecisionBinding {
		t.Fatal("the issued publication authorization must be recoverable with its decision binding")
	}

	trail := runtime.AuditTrail()
	if len(trail) != 3 {
		t.Fatalf("expected exactly three audit records, got %v", auditActions(trail))
	}
	for index, record := range trail {
		if record.Sequence != int64(index+1) {
			t.Fatalf("audit sequence must advance monotonically, got %d at position %d", record.Sequence, index)
		}
		if record.Action != EdgeAuditIssued {
			t.Fatalf("audit record %d must be edge-issued, got %s", index, string(record.Action))
		}
	}
}

// TestEdgeRuntimeIssuerIsMechanicallyCore freezes the forged-issuer and
// wrong-authority-scope fixtures: an edge issued under another Core
// namespace is never recorded here, and a record-layer edge sealed outside
// the runtime (self-issued by a provider) never authorizes a use.
func TestEdgeRuntimeIssuerIsMechanicallyCore(t *testing.T) {
	runtime, _, _ := newEdgeRuntimeFixture(t)
	edge, binding := issueRuntimeDispatchEdge(t, runtime, "1")

	foreignNamespace := validNamespace()
	foreignNamespace.AuthorityScopeId = "other-authority-scope"
	foreignRuntime, err := NewEdgeRuntime(foreignNamespace)
	if err != nil {
		t.Fatalf("NewEdgeRuntime: %v", err)
	}
	foreignEdge, err := foreignRuntime.IssueDispatchResultCapability(testDispatchResultIssuance("1"), edgeRuntimeNow)
	if err != nil {
		t.Fatalf("the foreign runtime must issue under its own namespace: %v", err)
	}
	if foreignEdge.EdgeDigest == edge.EdgeDigest {
		t.Fatal("edges issued under different authority scopes must carry distinct digests")
	}
	foreignRequest := dispatchUseRequestFor(foreignEdge, binding, "foreign-request")
	err = runtime.RecheckDispatchResult(foreignEdge, foreignRequest, edgeRuntimeNow)
	if !errors.Is(err, ErrEdgeNotRecorded) {
		t.Fatalf("expected ErrEdgeNotRecorded for an edge issued under another authority scope, got: %v", err)
	}

	selfIssued := sealDispatchResultCapability(t, dispatchResultCapabilityForPair(runtimeExecutionActor(), runtimeDataCapabilityActor(), DispatchResultOperationAccept))
	selfRequest := DispatchResultUseRequest{
		SourceActor:   selfIssued.SourceActor,
		TargetActor:   selfIssued.TargetActor,
		Operation:     selfIssued.Operation,
		AttemptId:     selfIssued.BoundAttemptId,
		AllocationId:  selfIssued.BoundAllocationId,
		LeaseId:       "lease-self",
		Generation:    1,
		FencingToken:  digestBytes([]byte("fencing-self")),
		RequestDigest: digestBytes([]byte("self-request")),
	}
	err = runtime.RecheckDispatchResult(selfIssued, selfRequest, edgeRuntimeNow)
	if !errors.Is(err, ErrEdgeNotRecorded) {
		t.Fatalf("expected ErrEdgeNotRecorded for a self-issued edge, got: %v", err)
	}
	if !containsAuditAction(runtime.AuditTrail(), EdgeAuditUseRejected) {
		t.Fatal("rejected forged issuances must be written to the audit trail")
	}
}

// TestEdgeRuntimeIssuanceIsIdempotent freezes the issuance replay semantics:
// identical issuances coalesce onto the identical ledger record, and an
// issuance replay after revocation returns the revoked current record.
func TestEdgeRuntimeIssuanceIsIdempotent(t *testing.T) {
	runtime, _, _ := newEdgeRuntimeFixture(t)
	first, err := runtime.IssueDispatchResultCapability(testDispatchResultIssuance("1"), edgeRuntimeNow)
	if err != nil {
		t.Fatalf("first issuance failed: %v", err)
	}
	second, err := runtime.IssueDispatchResultCapability(testDispatchResultIssuance("1"), edgeRuntimeNow)
	if err != nil {
		t.Fatalf("issuance replay failed: %v", err)
	}
	if first != second {
		t.Fatal("identical issuance replays must coalesce onto the identical ledger record")
	}
	if !containsAuditAction(runtime.AuditTrail(), EdgeAuditIssuanceMerged) {
		t.Fatal("the issuance replay must be recorded as an idempotent merge")
	}

	if _, err := runtime.RevokeDispatchResultCapability(first.EdgeDigest, EdgeRevocationOrdinary, edgeRuntimeNow); err != nil {
		t.Fatalf("RevokeDispatchResultCapability: %v", err)
	}
	replayed, err := runtime.IssueDispatchResultCapability(testDispatchResultIssuance("1"), edgeRuntimeNow)
	if err != nil {
		t.Fatalf("issuance replay after revocation failed: %v", err)
	}
	if replayed.RevocationGeneration == 0 {
		t.Fatal("an issuance replay after revocation must return the revoked current record")
	}
}

// TestEdgeRuntimeIssuanceRejectsMalformed freezes the fail-closed issuance
// gate for every edge type.
func TestEdgeRuntimeIssuanceRejectsMalformed(t *testing.T) {
	dispatchCases := []struct {
		name   string
		change func(*DispatchResultIssuance)
	}{
		{"invalid source actor", func(r *DispatchResultIssuance) { r.SourceActor.TenantNamespace = "" }},
		{"illegal trust domain pair", func(r *DispatchResultIssuance) { r.TargetActor = runtimeExecutionActor() }},
		{"unknown operation", func(r *DispatchResultIssuance) { r.Operation = "dispatch-result-write" }},
		{"empty boundAttemptId", func(r *DispatchResultIssuance) { r.BoundAttemptId = "" }},
		{"empty boundAllocationId", func(r *DispatchResultIssuance) { r.BoundAllocationId = "" }},
		{"lease binding attempt mismatch", func(r *DispatchResultIssuance) { r.LeaseBinding.AttemptId = "attempt-other" }},
		{"lease binding allocation mismatch", func(r *DispatchResultIssuance) { r.LeaseBinding.AllocationId = "allocation-other" }},
		{"empty leaseId", func(r *DispatchResultIssuance) { r.LeaseBinding.LeaseId = "" }},
		{"zero lease generation", func(r *DispatchResultIssuance) { r.LeaseBinding.Generation = 0 }},
		{"empty fencingToken", func(r *DispatchResultIssuance) { r.LeaseBinding.FencingToken = "" }},
		{"empty expiry", func(r *DispatchResultIssuance) { r.Expiry = "" }},
		{"malformed expiry", func(r *DispatchResultIssuance) { r.Expiry = "2026-13-44T99:00:00Z" }},
		{"zero-time expiry", func(r *DispatchResultIssuance) { r.Expiry = "0001-01-01T00:00:00Z" }},
	}
	for _, tc := range dispatchCases {
		t.Run("dispatchResultCapability "+tc.name, func(t *testing.T) {
			runtime, _, _ := newEdgeRuntimeFixture(t)
			request := testDispatchResultIssuance("1")
			tc.change(&request)
			if _, err := runtime.IssueDispatchResultCapability(request, edgeRuntimeNow); err == nil {
				t.Fatalf("issuance accepted %s", tc.name)
			}
			if !containsAuditAction(runtime.AuditTrail(), EdgeAuditIssuanceRejected) {
				t.Fatal("the rejected issuance must be written to the audit trail")
			}
		})
	}

	materialCases := []struct {
		name   string
		change func(*MaterialAccessIssuance)
	}{
		{"empty materialId", func(r *MaterialAccessIssuance) { r.MaterialId = "" }},
		{"empty scopeRestriction", func(r *MaterialAccessIssuance) { r.ScopeRestriction = "" }},
		{"empty attemptId", func(r *MaterialAccessIssuance) { r.AttemptId = "" }},
		{"empty allocationId", func(r *MaterialAccessIssuance) { r.AllocationId = "" }},
		{"zero attemptBoundary", func(r *MaterialAccessIssuance) { r.AttemptBoundary = time.Time{} }},
		{"empty expiry", func(r *MaterialAccessIssuance) { r.Expiry = "" }},
		{"expiry beyond the attempt boundary", func(r *MaterialAccessIssuance) { r.Expiry = "2026-08-14T00:00:01Z" }},
		{"illegal trust domain pair", func(r *MaterialAccessIssuance) { r.SourceActor = runtimeDataCapabilityActor() }},
	}
	for _, tc := range materialCases {
		t.Run("materialAccessGrant "+tc.name, func(t *testing.T) {
			runtime, _, _ := newEdgeRuntimeFixture(t)
			request := testMaterialAccessIssuance("1")
			tc.change(&request)
			if _, err := runtime.IssueMaterialAccessGrant(request, edgeRuntimeNow); err == nil {
				t.Fatalf("issuance accepted %s", tc.name)
			}
		})
	}

	publicationCases := []struct {
		name   string
		change func(*PublicationIssuance)
	}{
		{"malformed boundPublicationDigest", func(r *PublicationIssuance) { r.BoundPublicationDigest = "not-a-digest" }},
		{"empty sideEffectIntentDigest", func(r *PublicationIssuance) { r.DecisionBinding.SideEffectIntentDigest = "" }},
		{"empty reviewDecisionDigest", func(r *PublicationIssuance) { r.DecisionBinding.ReviewDecisionDigest = "" }},
		{"empty evidenceDigest", func(r *PublicationIssuance) { r.DecisionBinding.EvidenceDigest = "" }},
		{"empty expiry", func(r *PublicationIssuance) { r.Expiry = "" }},
		{"illegal trust domain pair", func(r *PublicationIssuance) { r.TargetActor = runtimeExecutionActor() }},
	}
	for _, tc := range publicationCases {
		t.Run("publicationAuthorization "+tc.name, func(t *testing.T) {
			runtime, _, _ := newEdgeRuntimeFixture(t)
			request := testPublicationIssuance("1")
			tc.change(&request)
			if _, err := runtime.IssuePublicationAuthorization(request, edgeRuntimeNow); err == nil {
				t.Fatalf("issuance accepted %s", tc.name)
			}
		})
	}
}

// TestEdgeRuntimeUsePositiveAndIdempotent freezes the legal-use fixture: the
// recheck accepts a fully aligned request, identical repeated requests
// coalesce on the canonical replay key, and a distinct request digest opens
// a distinct acceptance.
func TestEdgeRuntimeUsePositiveAndIdempotent(t *testing.T) {
	runtime, leaseResolver, targetResolver := newEdgeRuntimeFixture(t)
	edge, binding := issueRuntimeDispatchEdge(t, runtime, "1")
	request := dispatchUseRequestFor(edge, binding, "result-request-1")

	for repeat := 1; repeat <= 2; repeat++ {
		if err := runtime.RecheckDispatchResult(edge, request, edgeRuntimeNow); err != nil {
			t.Fatalf("recheck repeat %d rejected a fully aligned request: %v", repeat, err)
		}
	}
	if leaseResolver.calls != 2 || len(targetResolver.targets) != 2 {
		t.Fatalf("every use must consult the current ledger, lease calls = %d, target calls = %d", leaseResolver.calls, len(targetResolver.targets))
	}
	if leaseResolver.lastLeaseId != binding.LeaseId || leaseResolver.lastGeneration != binding.Generation || leaseResolver.lastFencingToken != binding.FencingToken {
		t.Fatal("the recheck must resolve the exact lease identity recorded at issuance")
	}

	distinct := request
	distinct.RequestDigest = digestBytes([]byte("result-request-2"))
	if err := runtime.RecheckDispatchResult(edge, distinct, edgeRuntimeNow); err != nil {
		t.Fatalf("a distinct request digest must open a distinct acceptance: %v", err)
	}

	trail := runtime.AuditTrail()
	if !containsAuditAction(trail, EdgeAuditUseAccepted) || !containsAuditAction(trail, EdgeAuditUseReplayMerged) {
		t.Fatalf("expected accepted and replay-merged audit records, got %v", auditActions(trail))
	}
	accepted, merged := 0, 0
	for _, record := range trail {
		switch record.Action {
		case EdgeAuditUseAccepted:
			accepted++
		case EdgeAuditUseReplayMerged:
			merged++
		}
	}
	if accepted != 2 || merged != 1 {
		t.Fatalf("expected two acceptances and one replay merge, got %d/%d", accepted, merged)
	}
}

// TestEdgeRuntimeRejectsBindingMismatch freezes the wrong-operation,
// source/target substitution, wrong attempt/allocation and wrong lease
// identity fixtures, including the same-domain bearer request: identical
// securityDomainIds never constitute authorization.
func TestEdgeRuntimeRejectsBindingMismatch(t *testing.T) {
	cases := []struct {
		name   string
		change func(*DispatchResultUseRequest)
	}{
		{"wrong operation", func(r *DispatchResultUseRequest) { r.Operation = DispatchResultOperationRead }},
		{"source substitution", func(r *DispatchResultUseRequest) {
			r.SourceActor = securityDomainForKind(TrustDomainKindExecution, "isolation-substitute")
		}},
		{"target substitution", func(r *DispatchResultUseRequest) {
			r.TargetActor = securityDomainForKind(TrustDomainKindDataCapability, "isolation-substitute")
		}},
		{"wrong attempt", func(r *DispatchResultUseRequest) { r.AttemptId = "attempt-other" }},
		{"wrong allocation", func(r *DispatchResultUseRequest) { r.AllocationId = "allocation-other" }},
		{"wrong leaseId", func(r *DispatchResultUseRequest) { r.LeaseId = "lease-other" }},
		{"stale generation", func(r *DispatchResultUseRequest) { r.Generation = 2 }},
		{"wrong fencingToken", func(r *DispatchResultUseRequest) { r.FencingToken = digestBytes([]byte("fencing-other")) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runtime, _, _ := newEdgeRuntimeFixture(t)
			edge, binding := issueRuntimeDispatchEdge(t, runtime, "1")
			request := dispatchUseRequestFor(edge, binding, "result-request-1")
			tc.change(&request)
			err := runtime.RecheckDispatchResult(edge, request, edgeRuntimeNow)
			if !errors.Is(err, ErrEdgeBindingMismatch) {
				t.Fatalf("expected ErrEdgeBindingMismatch for %s, got: %v", tc.name, err)
			}
		})
	}

	t.Run("same-domain bearer request", func(t *testing.T) {
		runtime, _, _ := newEdgeRuntimeFixture(t)
		edge, binding := issueRuntimeDispatchEdge(t, runtime, "1")
		// The bearer request keeps the identical securityDomainIds but skips
		// the attempt/allocation/lease gates of another attempt.
		bearer := dispatchUseRequestFor(edge, binding, "result-request-1")
		bearer.AttemptId = "attempt-bearer"
		bearer.AllocationId = "allocation-bearer"
		bearer.LeaseId = "lease-bearer"
		err := runtime.RecheckDispatchResult(edge, bearer, edgeRuntimeNow)
		if !errors.Is(err, ErrEdgeBindingMismatch) {
			t.Fatalf("a same-domain bearer request must fail closed, got: %v", err)
		}
	})
}

// TestEdgeRuntimeRejectsForgedAndTamperedEdges freezes the digest
// substitution fixtures: a swapped edgeDigest fails the frozen structural
// validation, a resealed tampered edge is unknown to the ledger, and a
// record-layer self-sealed edge never authorizes.
func TestEdgeRuntimeRejectsForgedAndTamperedEdges(t *testing.T) {
	runtime, _, _ := newEdgeRuntimeFixture(t)
	edge, binding := issueRuntimeDispatchEdge(t, runtime, "1")

	swapped := edge
	swapped.EdgeDigest = digestBytes([]byte("tampered-edge"))
	request := dispatchUseRequestFor(edge, binding, "result-request-1")
	err := runtime.RecheckDispatchResult(swapped, request, edgeRuntimeNow)
	if !errors.Is(err, ErrEdgeDigest) {
		t.Fatalf("expected the frozen ErrEdgeDigest for a swapped edgeDigest, got: %v", err)
	}

	tampered := edge
	tampered.BoundAttemptId = "attempt-tampered"
	tampered.EdgeDigest = ""
	tamperedDigest, err := tampered.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	tampered.EdgeDigest = tamperedDigest
	tamperedRequest := dispatchUseRequestFor(edge, binding, "result-request-1")
	tamperedRequest.AttemptId = "attempt-tampered"
	err = runtime.RecheckDispatchResult(tampered, tamperedRequest, edgeRuntimeNow)
	if !errors.Is(err, ErrEdgeNotRecorded) {
		t.Fatalf("expected ErrEdgeNotRecorded for a resealed tampered edge, got: %v", err)
	}
}

// TestEdgeRuntimeRejectsExpiredUse freezes the expiry boundary: the edge is
// usable up to and including the expiry instant and fails closed afterwards.
func TestEdgeRuntimeRejectsExpiredUse(t *testing.T) {
	runtime, _, _ := newEdgeRuntimeFixture(t)
	request := testDispatchResultIssuance("1")
	request.Expiry = "2026-08-13T01:00:00Z"
	edge, err := runtime.IssueDispatchResultCapability(request, edgeRuntimeNow)
	if err != nil {
		t.Fatalf("IssueDispatchResultCapability: %v", err)
	}
	useRequest := dispatchUseRequestFor(edge, request.LeaseBinding, "result-request-1")

	expiry, err := time.Parse(time.RFC3339, request.Expiry)
	if err != nil {
		t.Fatalf("parse expiry: %v", err)
	}
	if err := runtime.RecheckDispatchResult(edge, useRequest, expiry); err != nil {
		t.Fatalf("the edge must stay usable at the exact expiry instant: %v", err)
	}
	err = runtime.RecheckDispatchResult(edge, useRequest, expiry.Add(time.Second))
	if !errors.Is(err, ErrEdgeExpired) {
		t.Fatalf("expected ErrEdgeExpired past the expiry, got: %v", err)
	}
}

// TestEdgeRuntimeRevocationIsLedgerFact freezes the revocation semantics:
// revocation replaces the current ledger record with the revoked successor,
// both spellings of the edge fail every later use closed with ErrEdgeRevoked,
// the revoked record stays recoverable and revocation replays are idempotent.
func TestEdgeRuntimeRevocationIsLedgerFact(t *testing.T) {
	runtime, _, _ := newEdgeRuntimeFixture(t)
	edge, binding := issueRuntimeDispatchEdge(t, runtime, "1")
	useRequest := dispatchUseRequestFor(edge, binding, "result-request-1")
	if err := runtime.RecheckDispatchResult(edge, useRequest, edgeRuntimeNow); err != nil {
		t.Fatalf("the pre-revocation recheck failed: %v", err)
	}

	revoked, err := runtime.RevokeDispatchResultCapability(edge.EdgeDigest, EdgeRevocationOrdinary, edgeRuntimeNow)
	if err != nil {
		t.Fatalf("RevokeDispatchResultCapability: %v", err)
	}
	if revoked.RevocationGeneration != edge.Generation+1 {
		t.Fatalf("the revoked record must carry the revocation generation, got %d", revoked.RevocationGeneration)
	}
	if revoked.EdgeDigest == edge.EdgeDigest {
		t.Fatal("the revoked successor must carry a distinct digest")
	}

	err = runtime.RecheckDispatchResult(edge, useRequest, edgeRuntimeNow)
	if !errors.Is(err, ErrEdgeRevoked) {
		t.Fatalf("the original edge spelling must fail closed after revocation, got: %v", err)
	}
	revokedRequest := dispatchUseRequestFor(revoked, binding, "result-request-revoked")
	err = runtime.RecheckDispatchResult(revoked, revokedRequest, edgeRuntimeNow)
	if !errors.Is(err, ErrEdgeRevoked) {
		t.Fatalf("the revoked successor spelling must fail closed, got: %v", err)
	}

	current, _, ok := runtime.CurrentDispatchResultCapability(edge.EdgeDigest)
	if !ok || current != revoked {
		t.Fatal("the ledger must recover the revoked current record under the issuance digest")
	}
	currentViaSuccessor, _, ok := runtime.CurrentDispatchResultCapability(revoked.EdgeDigest)
	if !ok || currentViaSuccessor != revoked {
		t.Fatal("the ledger must resolve the revoked successor digest to the revocation fact")
	}

	replayed, err := runtime.RevokeDispatchResultCapability(edge.EdgeDigest, EdgeRevocationOrdinary, edgeRuntimeNow)
	if err != nil || replayed != revoked {
		t.Fatalf("revocation replay must be idempotent, got %v / %v", replayed, err)
	}

	if _, err := runtime.RevokeDispatchResultCapability(digestBytes([]byte("unknown-edge")), EdgeRevocationOrdinary, edgeRuntimeNow); !errors.Is(err, ErrEdgeNotRecorded) {
		t.Fatalf("revoking an unknown edge must fail closed with ErrEdgeNotRecorded, got: %v", err)
	}
	if _, err := runtime.RevokeDispatchResultCapability(edge.EdgeDigest, "drain", edgeRuntimeNow); err == nil {
		t.Fatal("an unknown revocation reason must fail closed")
	}
	if !containsAuditAction(runtime.AuditTrail(), EdgeAuditRevoked) {
		t.Fatal("the revocation must be written to the audit trail")
	}
}

// TestEdgeRuntimeSecurityCriticalRevokeHook freezes the immediate-effect
// hook seam: security-critical revocations fire the hook synchronously after
// the fact, ordinary revocations never fire it, hook failures never roll
// back the revocation fact and idempotent replays never re-fire the hook.
func TestEdgeRuntimeSecurityCriticalRevokeHook(t *testing.T) {
	runtime, _, _ := newEdgeRuntimeFixture(t)
	hook := &stubRevokeHook{}
	runtime.BindSecurityCriticalRevokeHook(hook)

	ordinary, _ := issueRuntimeDispatchEdge(t, runtime, "ordinary")
	if _, err := runtime.RevokeDispatchResultCapability(ordinary.EdgeDigest, EdgeRevocationOrdinary, edgeRuntimeNow); err != nil {
		t.Fatalf("ordinary revocation failed: %v", err)
	}
	if len(hook.calls) != 0 {
		t.Fatalf("an ordinary revocation must not fire the immediate-effect hook, got %v", hook.calls)
	}

	critical, _ := issueRuntimeDispatchEdge(t, runtime, "critical")
	if _, err := runtime.RevokeDispatchResultCapability(critical.EdgeDigest, EdgeRevocationSecurityCritical, edgeRuntimeNow); err != nil {
		t.Fatalf("security-critical revocation failed: %v", err)
	}
	if len(hook.calls) != 1 || hook.calls[0] != string(EdgeKindDispatchResultCapability)+":"+critical.EdgeDigest {
		t.Fatalf("the immediate-effect hook must fire exactly once with the edge identity, got %v", hook.calls)
	}
	if _, err := runtime.RevokeDispatchResultCapability(critical.EdgeDigest, EdgeRevocationSecurityCritical, edgeRuntimeNow); err != nil {
		t.Fatalf("the security-critical revocation replay failed: %v", err)
	}
	if len(hook.calls) != 1 {
		t.Fatalf("an idempotent revocation replay must not re-fire the hook, got %v", hook.calls)
	}

	failingHook := &stubRevokeHook{err: errors.New("kill channel unavailable")}
	runtime.BindSecurityCriticalRevokeHook(failingHook)
	edge, binding := issueRuntimeDispatchEdge(t, runtime, "hook-failure")
	revoked, err := runtime.RevokeDispatchResultCapability(edge.EdgeDigest, EdgeRevocationSecurityCritical, edgeRuntimeNow)
	if err == nil {
		t.Fatal("a hook failure must be surfaced to the caller for escalation")
	}
	if revoked.RevocationGeneration == 0 {
		t.Fatal("a hook failure must not roll back the revocation fact")
	}
	useRequest := dispatchUseRequestFor(edge, binding, "result-request-1")
	if recheckErr := runtime.RecheckDispatchResult(edge, useRequest, edgeRuntimeNow); !errors.Is(recheckErr, ErrEdgeRevoked) {
		t.Fatalf("the revocation fact must stay effective despite the hook failure, got: %v", recheckErr)
	}
}

// TestEdgeRuntimeDerivedHandleNeverAuthorizes freezes the one-way reference
// invariant: a prior acceptance never caches authorization, the identical
// edge reference with a mutated lease identity still fails closed, and the
// recover accessors expose records without granting any use.
func TestEdgeRuntimeDerivedHandleNeverAuthorizes(t *testing.T) {
	runtime, _, _ := newEdgeRuntimeFixture(t)
	edge, binding := issueRuntimeDispatchEdge(t, runtime, "1")
	accepted := dispatchUseRequestFor(edge, binding, "result-request-1")
	if err := runtime.RecheckDispatchResult(edge, accepted, edgeRuntimeNow); err != nil {
		t.Fatalf("the baseline recheck failed: %v", err)
	}

	bypass := accepted
	bypass.Generation = binding.Generation + 1
	err := runtime.RecheckDispatchResult(edge, bypass, edgeRuntimeNow)
	if !errors.Is(err, ErrEdgeBindingMismatch) {
		t.Fatalf("a mutated lease identity must fail closed despite the prior acceptance, got: %v", err)
	}

	// The recovered record is a one-way reference: holding it grants nothing
	// without a full current-ledger recheck of every binding.
	recovered, _, ok := runtime.CurrentDispatchResultCapability(edge.EdgeDigest)
	if !ok {
		t.Fatal("the edge must stay recoverable")
	}
	escalated := dispatchUseRequestFor(recovered, binding, "result-request-escalated")
	escalated.Operation = DispatchResultOperationRead
	if err := runtime.RecheckDispatchResult(recovered, escalated, edgeRuntimeNow); !errors.Is(err, ErrEdgeBindingMismatch) {
		t.Fatalf("an escalated operation on a recovered handle must fail closed, got: %v", err)
	}
}

// TestEdgeRuntimeResolversFailClosed freezes the resolver gates: rechecks
// fail closed while the resolvers are unbound, an inactive lease and an
// ineligible target fail closed with their fixed sentinels, and resolver
// errors propagate without a silent pass.
func TestEdgeRuntimeResolversFailClosed(t *testing.T) {
	t.Run("unbound resolvers", func(t *testing.T) {
		runtime, _, _ := newEdgeRuntimeFixture(t)
		edge, binding := issueRuntimeDispatchEdge(t, runtime, "1")
		unbound, err := NewEdgeRuntime(validNamespace())
		if err != nil {
			t.Fatalf("NewEdgeRuntime: %v", err)
		}
		request := dispatchUseRequestFor(edge, binding, "result-request-1")
		err = unbound.RecheckDispatchResult(edge, request, edgeRuntimeNow)
		if !errors.Is(err, ErrEdgeResolverUnbound) {
			t.Fatalf("expected ErrEdgeResolverUnbound, got: %v", err)
		}
	})

	runtime, leaseResolver, targetResolver := newEdgeRuntimeFixture(t)
	edge, binding := issueRuntimeDispatchEdge(t, runtime, "1")
	request := dispatchUseRequestFor(edge, binding, "result-request-1")

	t.Run("inactive lease", func(t *testing.T) {
		leaseResolver.active = false
		err := runtime.RecheckDispatchResult(edge, request, edgeRuntimeNow)
		if !errors.Is(err, ErrEdgeLeaseInactive) {
			t.Fatalf("expected ErrEdgeLeaseInactive, got: %v", err)
		}
		leaseResolver.active = true
	})

	t.Run("lease resolver error", func(t *testing.T) {
		leaseResolver.err = errors.New("dispatch ledger unreadable")
		err := runtime.RecheckDispatchResult(edge, request, edgeRuntimeNow)
		if err == nil || !errors.Is(err, leaseResolver.err) {
			t.Fatalf("a lease resolver error must propagate fail closed, got: %v", err)
		}
		leaseResolver.err = nil
	})

	t.Run("ineligible target", func(t *testing.T) {
		targetResolver.eligible = false
		err := runtime.RecheckDispatchResult(edge, request, edgeRuntimeNow)
		if !errors.Is(err, ErrEdgeTargetIneligible) {
			t.Fatalf("expected ErrEdgeTargetIneligible, got: %v", err)
		}
		targetResolver.eligible = true
	})

	t.Run("target resolver error", func(t *testing.T) {
		targetResolver.err = errors.New("registration ledger unreadable")
		err := runtime.RecheckDispatchResult(edge, request, edgeRuntimeNow)
		if err == nil || !errors.Is(err, targetResolver.err) {
			t.Fatalf("a target resolver error must propagate fail closed, got: %v", err)
		}
		targetResolver.err = nil
	})

	t.Run("material recheck without target resolver", func(t *testing.T) {
		unbound, err := NewEdgeRuntime(validNamespace())
		if err != nil {
			t.Fatalf("NewEdgeRuntime: %v", err)
		}
		grant, grantErr := unbound.IssueMaterialAccessGrant(testMaterialAccessIssuance("1"), edgeRuntimeNow)
		if grantErr != nil {
			t.Fatalf("IssueMaterialAccessGrant: %v", grantErr)
		}
		useRequest := materialUseRequestFor(grant, testMaterialAccessIssuance("1"), "material-request-1")
		if err := unbound.RecheckMaterialAccess(grant, useRequest, edgeRuntimeNow); !errors.Is(err, ErrEdgeResolverUnbound) {
			t.Fatalf("expected ErrEdgeResolverUnbound, got: %v", err)
		}
	})
}

// TestEdgeRuntimeMaterialGrantLifecycle freezes the material access grant
// fixtures: the attempt boundary caps the expiry, the aligned use passes,
// wrong attempt/allocation/material bindings and revocation fail closed.
func TestEdgeRuntimeMaterialGrantLifecycle(t *testing.T) {
	runtime, _, _ := newEdgeRuntimeFixture(t)

	atBoundary := testMaterialAccessIssuance("boundary")
	atBoundary.Expiry = runtimeAttemptBoundary.Format(time.RFC3339)
	if _, err := runtime.IssueMaterialAccessGrant(atBoundary, edgeRuntimeNow); err != nil {
		t.Fatalf("an expiry exactly at the attempt boundary must be accepted: %v", err)
	}

	issuance := testMaterialAccessIssuance("1")
	grant, err := runtime.IssueMaterialAccessGrant(issuance, edgeRuntimeNow)
	if err != nil {
		t.Fatalf("IssueMaterialAccessGrant: %v", err)
	}
	useRequest := materialUseRequestFor(grant, issuance, "material-request-1")
	if err := runtime.RecheckMaterialAccess(grant, useRequest, edgeRuntimeNow); err != nil {
		t.Fatalf("the aligned material use failed: %v", err)
	}

	cases := []struct {
		name   string
		change func(*MaterialAccessUseRequest)
	}{
		{"wrong material", func(r *MaterialAccessUseRequest) { r.MaterialId = "material-other" }},
		{"wrong attempt", func(r *MaterialAccessUseRequest) { r.AttemptId = "attempt-other" }},
		{"wrong allocation", func(r *MaterialAccessUseRequest) { r.AllocationId = "allocation-other" }},
		{"wrong operation", func(r *MaterialAccessUseRequest) { r.Operation = MaterialAccessOperationWrite }},
		{"target substitution", func(r *MaterialAccessUseRequest) {
			r.TargetActor = securityDomainForKind(TrustDomainKindDataCapability, "isolation-substitute")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := useRequest
			tc.change(&mutated)
			if err := runtime.RecheckMaterialAccess(grant, mutated, edgeRuntimeNow); !errors.Is(err, ErrEdgeBindingMismatch) {
				t.Fatalf("expected ErrEdgeBindingMismatch for %s, got: %v", tc.name, err)
			}
		})
	}

	if _, err := runtime.RevokeMaterialAccessGrant(grant.EdgeDigest, EdgeRevocationSecurityCritical, edgeRuntimeNow); err != nil {
		t.Fatalf("RevokeMaterialAccessGrant: %v", err)
	}
	if err := runtime.RecheckMaterialAccess(grant, useRequest, edgeRuntimeNow); !errors.Is(err, ErrEdgeRevoked) {
		t.Fatalf("the revoked material grant must fail closed, got: %v", err)
	}
}

// TestEdgeRuntimePublicationDecisionBinding freezes the publication
// authorization fixtures: the aligned use passes and any changed decision
// digest (SideEffectIntent/ReviewDecision/evidence) or publication digest
// fails closed.
func TestEdgeRuntimePublicationDecisionBinding(t *testing.T) {
	runtime, _, _ := newEdgeRuntimeFixture(t)
	issuance := testPublicationIssuance("1")
	authorization, err := runtime.IssuePublicationAuthorization(issuance, edgeRuntimeNow)
	if err != nil {
		t.Fatalf("IssuePublicationAuthorization: %v", err)
	}
	useRequest := publicationUseRequestFor(authorization, issuance, "publication-request-1")
	if err := runtime.RecheckPublicationAuthorization(authorization, useRequest, edgeRuntimeNow); err != nil {
		t.Fatalf("the aligned publication use failed: %v", err)
	}

	cases := []struct {
		name   string
		change func(*PublicationUseRequest)
	}{
		{"changed sideEffectIntent digest", func(r *PublicationUseRequest) {
			r.SideEffectIntentDigest = digestBytes([]byte("side-effect-intent-substituted"))
		}},
		{"changed reviewDecision digest", func(r *PublicationUseRequest) {
			r.ReviewDecisionDigest = digestBytes([]byte("review-decision-substituted"))
		}},
		{"changed evidence digest", func(r *PublicationUseRequest) {
			r.EvidenceDigest = digestBytes([]byte("evidence-substituted"))
		}},
		{"substituted publication digest", func(r *PublicationUseRequest) {
			r.PublicationDigest = digestBytes([]byte("publication-substituted"))
		}},
		{"wrong operation", func(r *PublicationUseRequest) { r.Operation = PublicationOperationChecksRead }},
		{"target substitution", func(r *PublicationUseRequest) {
			r.TargetActor = securityDomainForKind(TrustDomainKindPublication, "isolation-substitute")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := useRequest
			tc.change(&mutated)
			if err := runtime.RecheckPublicationAuthorization(authorization, mutated, edgeRuntimeNow); !errors.Is(err, ErrEdgeBindingMismatch) {
				t.Fatalf("expected ErrEdgeBindingMismatch for %s, got: %v", tc.name, err)
			}
		})
	}

	if _, err := runtime.RevokePublicationAuthorization(authorization.EdgeDigest, EdgeRevocationOrdinary, edgeRuntimeNow); err != nil {
		t.Fatalf("RevokePublicationAuthorization: %v", err)
	}
	if err := runtime.RecheckPublicationAuthorization(authorization, useRequest, edgeRuntimeNow); !errors.Is(err, ErrEdgeRevoked) {
		t.Fatalf("the revoked publication authorization must fail closed, got: %v", err)
	}
}

// TestEdgeRuntimeAuditTrailRecordsEveryDecision freezes the audit trail
// contract: every rejection carries the machine-readable reason, sequences
// advance monotonically and rejected uses never pollute the replay index.
func TestEdgeRuntimeAuditTrailRecordsEveryDecision(t *testing.T) {
	runtime, _, _ := newEdgeRuntimeFixture(t)
	edge, binding := issueRuntimeDispatchEdge(t, runtime, "1")
	request := dispatchUseRequestFor(edge, binding, "result-request-1")

	unknown := sealDispatchResultCapability(t, dispatchResultCapabilityForPair(runtimeExecutionActor(), runtimeDataCapabilityActor(), DispatchResultOperationRead))
	unknownRequest := DispatchResultUseRequest{
		SourceActor:   unknown.SourceActor,
		TargetActor:   unknown.TargetActor,
		Operation:     unknown.Operation,
		AttemptId:     unknown.BoundAttemptId,
		AllocationId:  unknown.BoundAllocationId,
		LeaseId:       "lease-unknown",
		Generation:    1,
		FencingToken:  digestBytes([]byte("fencing-unknown")),
		RequestDigest: digestBytes([]byte("unknown-request")),
	}
	if err := runtime.RecheckDispatchResult(unknown, unknownRequest, edgeRuntimeNow); err == nil {
		t.Fatal("the unknown edge must fail closed")
	}
	if err := runtime.RecheckDispatchResult(edge, request, edgeRuntimeNow); err != nil {
		t.Fatalf("the aligned use failed: %v", err)
	}

	trail := runtime.AuditTrail()
	var rejected *EdgeAuditRecord
	for index := range trail {
		if trail[index].Action == EdgeAuditUseRejected {
			rejected = &trail[index]
			break
		}
	}
	if rejected == nil || rejected.Reason == "" {
		t.Fatalf("the rejection must be audited with a machine-readable reason, got %v", trail)
	}
	for index := 1; index < len(trail); index++ {
		if trail[index].Sequence != trail[index-1].Sequence+1 {
			t.Fatalf("audit sequences must advance monotonically: %d -> %d", trail[index-1].Sequence, trail[index].Sequence)
		}
	}

	// A rejected use never occupies its replay key, and a replay of an
	// already accepted request against a meanwhile-revoked edge fails closed
	// instead of merging on the stale acceptance.
	if _, err := runtime.RevokeDispatchResultCapability(edge.EdgeDigest, EdgeRevocationOrdinary, edgeRuntimeNow); err != nil {
		t.Fatalf("RevokeDispatchResultCapability: %v", err)
	}
	if err := runtime.RecheckDispatchResult(edge, request, edgeRuntimeNow); !errors.Is(err, ErrEdgeRevoked) {
		t.Fatalf("the revoked edge must fail closed despite the earlier acceptance, got: %v", err)
	}
}
