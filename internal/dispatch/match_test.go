package dispatch

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/provider"
)

// dispatchTestNow is the reference clock of the matcher fixtures: after the
// fixture createdAt/signedAt values and before the default validUntil.
var dispatchTestNow = time.Date(2026, time.August, 13, 0, 0, 0, 0, time.UTC)

// forceDigest canonicalizes value and returns its digest without validating
// the content first, so fixtures that must fail a later validation stage can
// still carry a well-formed digest binding.
func forceDigest(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	canonicalized, err := canonical.JSON(raw)
	if err != nil {
		panic(err)
	}
	return canonical.DigestBytes(canonicalized)
}

func setTestRegistrationDigest(registration *provider.ProviderRegistration) {
	detached := *registration
	detached.RegistrationDigest = ""
	registration.RegistrationDigest = forceDigest(detached)
}

// testRegistration builds a structurally valid registration whose
// registrationDigest is backfilled from the canonical content.
func testRegistration(suffix string) provider.ProviderRegistration {
	registration := provider.ProviderRegistration{
		RegistrationId:       "registration-" + suffix,
		AuthorityNamespaceId: testAuthorityNamespace(),
		SecurityDomainId:     testSecurityDomain(),
		Principal:            "principal-local-sandbox",
		ProviderType:         "sandbox",
		ProviderName:         "provider-a",
		ProviderVersion:      "1.0.0",
		ProtocolVersion:      "v1alpha1",
		Scope:                "repository:marshal-harness",
		IdempotencyKey:       "idempotency-key-" + suffix,
		RequestDigest:        fixedDigest("registration-request-" + suffix),
		Attestation:          testAttestation(),
		LifecycleState:       provider.LifecycleStateActive,
		CreatedAt:            "2026-08-12T00:00:00Z",
	}
	setTestRegistrationDigest(&registration)
	return registration
}

func setTestSnapshotDigest(snapshot *provider.ProviderCapabilitySnapshot) {
	detached := *snapshot
	detached.ProviderCapabilitySnapshotDigest = ""
	snapshot.ProviderCapabilitySnapshotDigest = forceDigest(detached)
}

// testSnapshot builds a snapshot aligned with registration; capabilities
// default to the hardened declaration, evidenceDigests default to the empty
// closed set and mutate runs before the digest backfill.
func testSnapshot(registration provider.ProviderRegistration, capabilities map[string]string, evidenceDigests []string, mutate func(*provider.ProviderCapabilitySnapshot)) provider.ProviderCapabilitySnapshot {
	if capabilities == nil {
		capabilities = hardenedCapabilities()
	}
	if evidenceDigests == nil {
		evidenceDigests = []string{}
	}
	snapshot := provider.ProviderCapabilitySnapshot{
		RegistrationId:             registration.RegistrationId,
		ProtocolVersion:            registration.ProtocolVersion,
		ProviderType:               registration.ProviderType,
		ProviderName:               registration.ProviderName,
		ProviderVersion:            registration.ProviderVersion,
		Capabilities:               capabilities,
		ConformanceEvidenceDigests: evidenceDigests,
		Scope:                      registration.Scope,
		SnapshotState:              provider.SnapshotStateActive,
		CreatedAt:                  "2026-08-12T00:00:01Z",
		Attestation:                registration.Attestation,
	}
	if mutate != nil {
		mutate(&snapshot)
	}
	setTestSnapshotDigest(&snapshot)
	return snapshot
}

func setTestEvidenceDigest(evidence *provider.ConformanceEvidence) {
	detached := *evidence
	detached.EvidenceDigest = ""
	evidence.EvidenceDigest = forceDigest(detached)
}

func passedDimensionResults() map[provider.ConformanceDimension]provider.DimensionResult {
	return map[provider.ConformanceDimension]provider.DimensionResult{
		provider.ConformanceDimensionMount:      provider.DimensionResultPassed,
		provider.ConformanceDimensionNetwork:    provider.DimensionResultPassed,
		provider.ConformanceDimensionResource:   provider.DimensionResultPassed,
		provider.ConformanceDimensionCredential: provider.DimensionResultPassed,
	}
}

// testEvidence builds a structurally valid all-passed evidence record aligned
// with registration; mutate runs before the digest backfill.
func testEvidence(registration provider.ProviderRegistration, mutate func(*provider.ConformanceEvidence)) provider.ConformanceEvidence {
	evidence := provider.ConformanceEvidence{
		AuthorityNamespaceId: registration.AuthorityNamespaceId,
		SecurityDomainId:     registration.SecurityDomainId,
		ProviderInstanceId:   registration.Attestation.ProviderInstanceId,
		ConfigDigest:         registration.Attestation.ConfigDigest,
		TrustRootKeyId:       registration.Attestation.TrustRootKeyId,
		SuiteName:            "marshal-sandbox-conformance-suite",
		ProbeArtifactDigest:  fixedDigest("probe-artifact-" + "1"),
		DimensionResults:     passedDimensionResults(),
		EvidenceState:        provider.EvidenceStateValid,
		ProviderSelfSigned:   false,
		SignedAt:             "2026-08-12T00:00:02Z",
		ValidUntil:           "2026-09-11T00:00:00Z",
	}
	if mutate != nil {
		mutate(&evidence)
	}
	setTestEvidenceDigest(&evidence)
	return evidence
}

func hardenedCapabilities() map[string]string {
	return map[string]string{
		capabilityKeyAccessMode:            "workspace-write",
		capabilityKeyMinimumAssuranceLevel: "hardened",
		"structuredOutput":                 "json",
	}
}

func readOnlyCapabilities() map[string]string {
	return map[string]string{
		capabilityKeyAccessMode:            "read-only",
		capabilityKeyMinimumAssuranceLevel: "workspace-write",
	}
}

func workspaceWriteCapabilities() map[string]string {
	return map[string]string{
		capabilityKeyAccessMode:            "workspace-write",
		capabilityKeyMinimumAssuranceLevel: "workspace-write",
	}
}

func cloneCapabilities(capabilities map[string]string) map[string]string {
	clone := make(map[string]string, len(capabilities))
	for key, value := range capabilities {
		clone[key] = value
	}
	return clone
}

func dropAccessMode(capabilities map[string]string) map[string]string {
	clone := cloneCapabilities(capabilities)
	delete(clone, capabilityKeyAccessMode)
	return clone
}

func dropAssuranceLevel(capabilities map[string]string) map[string]string {
	clone := cloneCapabilities(capabilities)
	delete(clone, capabilityKeyMinimumAssuranceLevel)
	return clone
}

func withAccessMode(capabilities map[string]string, value string) map[string]string {
	clone := cloneCapabilities(capabilities)
	clone[capabilityKeyAccessMode] = value
	return clone
}

func withAssuranceLevel(capabilities map[string]string, value string) map[string]string {
	clone := cloneCapabilities(capabilities)
	clone[capabilityKeyMinimumAssuranceLevel] = value
	return clone
}

func hardenedRequirements() domain.SandboxRequirements {
	requirements, err := domain.NewSandboxRequirements(domain.AccessModeWorkspaceWrite, domain.AssuranceLevelHardened)
	if err != nil {
		panic(err)
	}
	return requirements
}

func workspaceWriteRequirements() domain.SandboxRequirements {
	requirements, err := domain.NewSandboxRequirements(domain.AccessModeWorkspaceWrite, domain.AssuranceLevelWorkspaceWrite)
	if err != nil {
		panic(err)
	}
	return requirements
}

func readOnlyRequirements() domain.SandboxRequirements {
	requirements, err := domain.NewSandboxRequirements(domain.AccessModeReadOnly, domain.AssuranceLevelWorkspaceWrite)
	if err != nil {
		panic(err)
	}
	return requirements
}

func testClaimRequest(registration provider.ProviderRegistration, snapshot provider.ProviderCapabilitySnapshot, evidences []provider.ConformanceEvidence, requirements domain.SandboxRequirements, suffix string) ClaimRequest {
	return ClaimRequest{
		AuthorityNamespaceId: registration.AuthorityNamespaceId,
		RegistrationId:       registration.RegistrationId,
		Snapshot:             snapshot,
		Evidences:            evidences,
		Requirements:         requirements,
		TargetActor:          testResultIngressTarget(),
		TaskId:               "task-" + suffix,
		RunId:                "run-" + suffix,
		AttemptId:            "attempt-" + suffix,
		AllocationId:         "allocation-" + suffix,
		AckDeadlineAt:        "2026-08-13T00:30:00Z",
		ExpiresAt:            "2026-08-13T02:00:00Z",
	}
}

func evidenceDigestsOf(evidences []provider.ConformanceEvidence) []string {
	digests := make([]string, 0, len(evidences))
	for _, evidence := range evidences {
		digests = append(digests, evidence.EvidenceDigest)
	}
	return digests
}

// claimEdgeLeaseResolver is the permissive dispatch-ledger resolver of the
// claim fixtures; the resolver fail-closed matrix is covered by the
// authority runtime fixture tests.
type claimEdgeLeaseResolver struct{}

func (claimEdgeLeaseResolver) LeaseActive(string, int64, string) (bool, error) { return true, nil }

// claimEdgeTargetResolver is the permissive target eligibility resolver of
// the claim fixtures.
type claimEdgeTargetResolver struct{}

func (claimEdgeTargetResolver) TargetEligible(authority.SecurityDomainId) (bool, error) {
	return true, nil
}

// newClaimEdgeRuntime builds the Core edge runtime of the claim fixtures
// under the test authority namespace with permissive resolvers bound.
func newClaimEdgeRuntime(t *testing.T) *authority.EdgeRuntime {
	t.Helper()
	runtime, err := authority.NewEdgeRuntime(testAuthorityNamespace())
	if err != nil {
		t.Fatalf("NewEdgeRuntime: %v", err)
	}
	runtime.BindLeaseResolver(claimEdgeLeaseResolver{})
	runtime.BindTargetEligibilityResolver(claimEdgeTargetResolver{})
	return runtime
}

// testResultIngressTarget is the result-ingress securityDomainId the claim
// fixtures bind as the targetActor of the issued capability; the
// (execution, data-capability) pair belongs to the closed typed-edge matrix.
func testResultIngressTarget() authority.SecurityDomainId {
	return authority.SecurityDomainId{
		TenantNamespace:   "default",
		TrustDomainKind:   authority.TrustDomainKindDataCapability,
		IsolationDomainId: "isolation-result-ingress",
	}
}

// eligibleFixture assembles a durable store holding one accepted
// registration, an all-passed evidence set and an aligned snapshot declaring
// the closed evidence digest set, plus a Matcher bound to the store and a
// Core edge runtime.
func eligibleFixture(t *testing.T) (*provider.RegistrationStore, *Matcher, provider.ProviderRegistration, provider.ProviderCapabilitySnapshot, []provider.ConformanceEvidence) {
	t.Helper()
	store, err := provider.NewRegistrationStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistrationStore: %v", err)
	}
	registration := testRegistration("1")
	if _, err := store.Put(registration); err != nil {
		t.Fatalf("Put rejected the baseline registration: %v", err)
	}
	evidence := testEvidence(registration, nil)
	snapshot := testSnapshot(registration, nil, []string{evidence.EvidenceDigest}, nil)
	return store, NewMatcherWithEdgeRuntime(store, newClaimEdgeRuntime(t)), registration, snapshot, []provider.ConformanceEvidence{evidence}
}

// eligibleEdgeFixture is eligibleFixture plus direct access to the Core
// edge runtime, for the issuance-wiring fixtures that recheck or revoke the
// issued edge.
func eligibleEdgeFixture(t *testing.T) (*Matcher, *authority.EdgeRuntime, provider.ProviderRegistration, provider.ProviderCapabilitySnapshot, []provider.ConformanceEvidence) {
	t.Helper()
	store, err := provider.NewRegistrationStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistrationStore: %v", err)
	}
	registration := testRegistration("1")
	if _, err := store.Put(registration); err != nil {
		t.Fatalf("Put rejected the baseline registration: %v", err)
	}
	evidence := testEvidence(registration, nil)
	snapshot := testSnapshot(registration, nil, []string{evidence.EvidenceDigest}, nil)
	runtime := newClaimEdgeRuntime(t)
	return NewMatcherWithEdgeRuntime(store, runtime), runtime, registration, snapshot, []provider.ConformanceEvidence{evidence}
}

// TestMatcherClaimPositiveBaseline freezes the positive baseline: a fully
// eligible bundle claims, the sealed lease validates with generation 1 and a
// verifiable leaseDigest, the fencing guard accepts it, and Revalidate keeps
// passing while the ledger does not change.
func TestMatcherClaimPositiveBaseline(t *testing.T) {
	_, matcher, registration, snapshot, evidences := eligibleFixture(t)
	request := testClaimRequest(registration, snapshot, evidences, hardenedRequirements(), "1")
	lease, err := matcher.Claim(request, dispatchTestNow)
	if err != nil {
		t.Fatalf("Claim rejected a fully eligible bundle: %v", err)
	}
	if err := lease.Validate(); err != nil {
		t.Fatalf("the claimed lease does not validate: %v", err)
	}
	if lease.Generation != 1 {
		t.Fatalf("the first claim must carry generation 1, got %d", lease.Generation)
	}
	if lease.LeaseState != LeaseStateClaimed || lease.CancelReason != "" {
		t.Fatalf("the first claim must stay claimed without a cancelReason, got %q/%q", string(lease.LeaseState), string(lease.CancelReason))
	}
	computed, err := lease.Digest()
	if err != nil {
		t.Fatalf("Digest failed: %v", err)
	}
	if computed != lease.LeaseDigest {
		t.Fatal("leaseDigest must equal the canonical content digest")
	}
	if !lease.AuthorityNamespaceId.Equal(registration.AuthorityNamespaceId) {
		t.Fatal("the lease must bind the authorityNamespaceId of the stored registration")
	}
	if !lease.SecurityDomainId.Equal(registration.SecurityDomainId) {
		t.Fatal("the lease must bind the securityDomainId actor provenance")
	}
	if lease.RegistrationId != registration.RegistrationId {
		t.Fatal("the lease must reference the stored registration")
	}
	if lease.ProviderCapabilitySnapshotDigest != snapshot.ProviderCapabilitySnapshotDigest {
		t.Fatal("the lease must bind the snapshot digest")
	}
	if !reflect.DeepEqual(lease.ConformanceEvidenceDigests, snapshot.ConformanceEvidenceDigests) {
		t.Fatal("the lease must copy the closed conformanceEvidenceDigests set of the snapshot")
	}
	if lease.Attestation != registration.Attestation {
		t.Fatal("the lease must copy the registration attestation")
	}
	if lease.TaskId != request.TaskId || lease.RunId != request.RunId ||
		lease.AttemptId != request.AttemptId || lease.AllocationId != request.AllocationId {
		t.Fatal("the lease must carry the claimed identity tuple")
	}
	if lease.CreatedAt != dispatchTestNow.UTC().Format(time.RFC3339) {
		t.Fatal("createdAt must be the injected now without any clock side effect")
	}
	if err := ValidateLeaseFencing(lease, lease.Generation, lease.FencingToken); err != nil {
		t.Fatalf("the fencing guard rejected the freshly claimed lease: %v", err)
	}
	if err := matcher.Revalidate(lease, snapshot, evidences, hardenedRequirements(), dispatchTestNow); err != nil {
		t.Fatalf("Revalidate rejected a lease whose ledger did not change: %v", err)
	}
}

// TestMatcherClaimRejectsUnboundStore freezes negative fixture (1): a nil
// matcher, a nil store and a zero-value store all fail closed with the
// memory-only rejection.
func TestMatcherClaimRejectsUnboundStore(t *testing.T) {
	request := testClaimRequest(testRegistration("1"), provider.ProviderCapabilitySnapshot{}, nil, hardenedRequirements(), "1")
	for name, matcher := range map[string]*Matcher{
		"nil matcher":      nil,
		"nil store":        NewMatcher(nil),
		"zero-value store": NewMatcherWithEdgeRuntime(&provider.RegistrationStore{}, newClaimEdgeRuntime(t)),
	} {
		if _, err := matcher.Claim(request, dispatchTestNow); err == nil {
			t.Fatalf("Claim succeeded with %s", name)
		} else if !strings.Contains(err.Error(), "memory-only") {
			t.Fatalf("expected the memory-only rejection with %s, got: %v", name, err)
		}
	}
}

// TestMatcherClaimRejectsUnknownRegistration freezes negative fixture (2).
func TestMatcherClaimRejectsUnknownRegistration(t *testing.T) {
	_, matcher, registration, snapshot, evidences := eligibleFixture(t)
	request := testClaimRequest(registration, snapshot, evidences, hardenedRequirements(), "1")
	request.RegistrationId = "registration-" + "missing"
	_, err := matcher.Claim(request, dispatchTestNow)
	if err == nil {
		t.Fatal("Claim accepted an unknown registrationId")
	}
	if !errors.Is(err, provider.ErrUnknownRegistration) {
		t.Fatalf("expected ErrUnknownRegistration, got: %v", err)
	}
}

// TestMatcherClaimRejectsNonActiveLifecycle freezes negative fixture (3):
// create, revoked and expired registrations fail closed, including a claim
// after the revoke replay where Get still returns the terminal state.
func TestMatcherClaimRejectsNonActiveLifecycle(t *testing.T) {
	store, err := provider.NewRegistrationStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistrationStore: %v", err)
	}
	matcher := NewMatcherWithEdgeRuntime(store, newClaimEdgeRuntime(t))

	pending := testRegistration("pending")
	pending.LifecycleState = provider.LifecycleStateCreate
	setTestRegistrationDigest(&pending)
	if _, err := store.Put(pending); err != nil {
		t.Fatalf("Put rejected the create-state registration: %v", err)
	}
	pendingEvidence := testEvidence(pending, nil)
	pendingSnapshot := testSnapshot(pending, nil, []string{pendingEvidence.EvidenceDigest}, nil)
	pendingRequest := testClaimRequest(pending, pendingSnapshot, []provider.ConformanceEvidence{pendingEvidence}, hardenedRequirements(), "1")
	if _, err := matcher.Claim(pendingRequest, dispatchTestNow); err == nil {
		t.Fatal("Claim accepted a create-state registration")
	} else if !strings.Contains(err.Error(), "lifecycleState") {
		t.Fatalf("expected the lifecycleState rejection, got: %v", err)
	}

	active := testRegistration("active")
	if _, err := store.Put(active); err != nil {
		t.Fatalf("Put rejected the active registration: %v", err)
	}
	if err := store.Revoke(active.RegistrationId); err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}
	stored, err := store.Get(active.RegistrationId)
	if err != nil {
		t.Fatalf("Get failed after the revoke: %v", err)
	}
	if stored.LifecycleState != provider.LifecycleStateRevoked {
		t.Fatalf("Get lost the terminal revoked state, got %q", string(stored.LifecycleState))
	}
	activeEvidence := testEvidence(active, nil)
	activeSnapshot := testSnapshot(active, nil, []string{activeEvidence.EvidenceDigest}, nil)
	revokedRequest := testClaimRequest(active, activeSnapshot, []provider.ConformanceEvidence{activeEvidence}, hardenedRequirements(), "2")
	if _, err := matcher.Claim(revokedRequest, dispatchTestNow); err == nil {
		t.Fatal("Claim accepted a revoked registration after the replay")
	} else if !strings.Contains(err.Error(), "lifecycleState") {
		t.Fatalf("expected the lifecycleState rejection, got: %v", err)
	}

	expiring := testRegistration("expiring")
	if _, err := store.Put(expiring); err != nil {
		t.Fatalf("Put rejected the expiring registration: %v", err)
	}
	if err := store.Expire(expiring.RegistrationId); err != nil {
		t.Fatalf("Expire failed: %v", err)
	}
	expiringEvidence := testEvidence(expiring, nil)
	expiringSnapshot := testSnapshot(expiring, nil, []string{expiringEvidence.EvidenceDigest}, nil)
	expiredRequest := testClaimRequest(expiring, expiringSnapshot, []provider.ConformanceEvidence{expiringEvidence}, hardenedRequirements(), "3")
	if _, err := matcher.Claim(expiredRequest, dispatchTestNow); err == nil {
		t.Fatal("Claim accepted an expired registration")
	}
}

// TestMatcherClaimRejectsInactiveSnapshot freezes negative fixture (4).
func TestMatcherClaimRejectsInactiveSnapshot(t *testing.T) {
	for _, state := range []provider.SnapshotState{provider.SnapshotStateExpired, provider.SnapshotStateSuperseded} {
		t.Run(string(state), func(t *testing.T) {
			_, matcher, registration, _, _ := eligibleFixture(t)
			evidence := testEvidence(registration, nil)
			snapshot := testSnapshot(registration, nil, []string{evidence.EvidenceDigest}, func(s *provider.ProviderCapabilitySnapshot) {
				s.SnapshotState = state
			})
			request := testClaimRequest(registration, snapshot, []provider.ConformanceEvidence{evidence}, hardenedRequirements(), "1")
			if _, err := matcher.Claim(request, dispatchTestNow); err == nil {
				t.Fatalf("Claim accepted a snapshot with snapshotState %q", string(state))
			} else if !strings.Contains(err.Error(), "snapshotState") {
				t.Fatalf("expected the snapshotState rejection, got: %v", err)
			}
		})
	}
}

// TestMatcherClaimRejectsEvidenceSetDrift freezes negative fixture (5):
// missing, extra and duplicate coverage of the closed digest set fail
// closed.
func TestMatcherClaimRejectsEvidenceSetDrift(t *testing.T) {
	_, matcher, registration, snapshot, evidences := eligibleFixture(t)

	missing := testClaimRequest(registration, snapshot, nil, hardenedRequirements(), "1")
	if _, err := matcher.Claim(missing, dispatchTestNow); err == nil {
		t.Fatal("Claim accepted a declared evidence digest without covering evidence")
	}

	extra := testEvidence(registration, func(e *provider.ConformanceEvidence) {
		e.SuiteName = "conformance-suite-extra"
	})
	extraRequest := testClaimRequest(registration, snapshot, append([]provider.ConformanceEvidence{}, evidences[0], extra), hardenedRequirements(), "2")
	if _, err := matcher.Claim(extraRequest, dispatchTestNow); err == nil {
		t.Fatal("Claim accepted an evidence outside the closed digest set")
	}

	duplicateRequest := testClaimRequest(registration, snapshot, []provider.ConformanceEvidence{evidences[0], evidences[0]}, hardenedRequirements(), "3")
	if _, err := matcher.Claim(duplicateRequest, dispatchTestNow); err == nil {
		t.Fatal("Claim accepted duplicate evidence coverage")
	}
}

// TestMatcherClaimRejectsIneligibleEvidence freezes negative fixture (6):
// revoked or expired evidence state, a validUntil before now and self-signed
// evidence all fail closed.
func TestMatcherClaimRejectsIneligibleEvidence(t *testing.T) {
	cases := []struct {
		name   string
		change func(*provider.ConformanceEvidence)
	}{
		{"revoked evidence", func(e *provider.ConformanceEvidence) { e.EvidenceState = provider.EvidenceStateRevoked }},
		{"expired evidence", func(e *provider.ConformanceEvidence) { e.EvidenceState = provider.EvidenceStateExpired }},
		{"validUntil before now", func(e *provider.ConformanceEvidence) { e.ValidUntil = "2026-08-12T00:00:00Z" }},
		{"self-signed evidence", func(e *provider.ConformanceEvidence) { e.ProviderSelfSigned = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, matcher, registration, _, _ := eligibleFixture(t)
			evidence := testEvidence(registration, tc.change)
			snapshot := testSnapshot(registration, nil, []string{evidence.EvidenceDigest}, nil)
			request := testClaimRequest(registration, snapshot, []provider.ConformanceEvidence{evidence}, hardenedRequirements(), "1")
			if _, err := matcher.Claim(request, dispatchTestNow); err == nil {
				t.Fatalf("Claim accepted %s", tc.name)
			}
		})
	}
}

// TestMatcherClaimRejectsAttestationSubstitution freezes negative fixture
// (7): swapping providerInstanceId, configDigest or trustRootKeyId between
// snapshot and registration or between evidence and snapshot never claims.
func TestMatcherClaimRejectsAttestationSubstitution(t *testing.T) {
	snapshotCases := []struct {
		name   string
		change func(*provider.Attestation)
	}{
		{"providerInstanceId", func(a *provider.Attestation) { a.ProviderInstanceId = "provider-instance-" + "substituted" }},
		{"configDigest", func(a *provider.Attestation) { a.ConfigDigest = fixedDigest("effective-config-" + "substituted") }},
		{"trustRootKeyId", func(a *provider.Attestation) { a.TrustRootKeyId = "trust-root-key-" + "substituted" }},
	}
	for _, tc := range snapshotCases {
		t.Run("snapshot "+tc.name, func(t *testing.T) {
			_, matcher, registration, _, _ := eligibleFixture(t)
			evidence := testEvidence(registration, nil)
			snapshot := testSnapshot(registration, nil, []string{evidence.EvidenceDigest}, func(s *provider.ProviderCapabilitySnapshot) {
				tc.change(&s.Attestation)
			})
			request := testClaimRequest(registration, snapshot, []provider.ConformanceEvidence{evidence}, hardenedRequirements(), "1")
			_, err := matcher.Claim(request, dispatchTestNow)
			if err == nil {
				t.Fatalf("Claim accepted a snapshot attestation substitution of %s", tc.name)
			}
			if !strings.Contains(err.Error(), "attestation") {
				t.Fatalf("expected the attestation rejection, got: %v", err)
			}
		})
	}

	evidenceCases := []struct {
		name   string
		change func(*provider.ConformanceEvidence)
	}{
		{"providerInstanceId", func(e *provider.ConformanceEvidence) { e.ProviderInstanceId = "provider-instance-" + "substituted" }},
		{"configDigest", func(e *provider.ConformanceEvidence) {
			e.ConfigDigest = fixedDigest("effective-config-" + "substituted")
		}},
		{"trustRootKeyId", func(e *provider.ConformanceEvidence) { e.TrustRootKeyId = "trust-root-key-" + "substituted" }},
	}
	for _, tc := range evidenceCases {
		t.Run("evidence "+tc.name, func(t *testing.T) {
			_, matcher, registration, _, _ := eligibleFixture(t)
			evidence := testEvidence(registration, tc.change)
			snapshot := testSnapshot(registration, nil, []string{evidence.EvidenceDigest}, nil)
			request := testClaimRequest(registration, snapshot, []provider.ConformanceEvidence{evidence}, hardenedRequirements(), "1")
			_, err := matcher.Claim(request, dispatchTestNow)
			if err == nil {
				t.Fatalf("Claim accepted an evidence attestation substitution of %s", tc.name)
			}
			if !strings.Contains(err.Error(), "attestation") {
				t.Fatalf("expected the attestation rejection, got: %v", err)
			}
		})
	}
}

// TestMatcherClaimRejectsHardenedDowngrade freezes negative fixture (8):
// hardened requirements fail closed on any evidence that is not hardened
// eligible, never a silent downgrade to workspace-write.
func TestMatcherClaimRejectsHardenedDowngrade(t *testing.T) {
	for _, dimension := range []provider.ConformanceDimension{
		provider.ConformanceDimensionMount,
		provider.ConformanceDimensionNetwork,
		provider.ConformanceDimensionResource,
		provider.ConformanceDimensionCredential,
	} {
		for _, result := range []provider.DimensionResult{provider.DimensionResultFailed, provider.DimensionResultSkipped} {
			t.Run(string(dimension)+"-"+string(result), func(t *testing.T) {
				_, matcher, registration, _, _ := eligibleFixture(t)
				evidence := testEvidence(registration, func(e *provider.ConformanceEvidence) {
					e.DimensionResults[dimension] = result
				})
				snapshot := testSnapshot(registration, nil, []string{evidence.EvidenceDigest}, nil)
				request := testClaimRequest(registration, snapshot, []provider.ConformanceEvidence{evidence}, hardenedRequirements(), "1")
				_, err := matcher.Claim(request, dispatchTestNow)
				if err == nil {
					t.Fatalf("hardened requirements accepted evidence with %q on %s", string(result), string(dimension))
				}
				if !strings.Contains(err.Error(), "hardened") || !strings.Contains(err.Error(), "downgrade") {
					t.Fatalf("expected the hardened fail-closed rejection without downgrade, got: %v", err)
				}
			})
		}
	}

	_, matcher, registration, _, _ := eligibleFixture(t)
	bareSnapshot := testSnapshot(registration, nil, nil, nil)
	bareRequest := testClaimRequest(registration, bareSnapshot, nil, hardenedRequirements(), "2")
	if _, err := matcher.Claim(bareRequest, dispatchTestNow); err == nil {
		t.Fatal("hardened requirements accepted an empty evidence set")
	}
}

// TestMatcherClaimRejectsCapabilityMismatch freezes negative fixture (9):
// missing declarations, unknown values and capability conflicts fail closed,
// while the superset direction stays satisfiable.
func TestMatcherClaimRejectsCapabilityMismatch(t *testing.T) {
	cases := []struct {
		name         string
		capabilities map[string]string
		requirements domain.SandboxRequirements
	}{
		{"missing accessMode declaration", dropAccessMode(hardenedCapabilities()), hardenedRequirements()},
		{"missing minimumAssuranceLevel declaration", dropAssuranceLevel(hardenedCapabilities()), hardenedRequirements()},
		{"workspace-write required over read-only capability", readOnlyCapabilities(), workspaceWriteRequirements()},
		{"hardened required over workspace-write assurance", workspaceWriteCapabilities(), hardenedRequirements()},
		{"unknown accessMode declaration", withAccessMode(hardenedCapabilities(), "privileged"), hardenedRequirements()},
		{"unknown assurance declaration", withAssuranceLevel(hardenedCapabilities(), "none"), hardenedRequirements()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, matcher, registration, _, _ := eligibleFixture(t)
			evidence := testEvidence(registration, nil)
			snapshot := testSnapshot(registration, tc.capabilities, []string{evidence.EvidenceDigest}, nil)
			request := testClaimRequest(registration, snapshot, []provider.ConformanceEvidence{evidence}, tc.requirements, "1")
			if _, err := matcher.Claim(request, dispatchTestNow); err == nil {
				t.Fatalf("Claim accepted %s", tc.name)
			}
		})
	}

	_, matcher, registration, _, _ := eligibleFixture(t)
	evidence := testEvidence(registration, nil)
	snapshot := testSnapshot(registration, hardenedCapabilities(), []string{evidence.EvidenceDigest}, nil)
	request := testClaimRequest(registration, snapshot, []provider.ConformanceEvidence{evidence}, readOnlyRequirements(), "2")
	if _, err := matcher.Claim(request, dispatchTestNow); err != nil {
		t.Fatalf("read-only requirements must be satisfied by the workspace-write superset capability: %v", err)
	}
}

// TestMatcherClaimRejectsIdentityTupleAndDeadlines freezes negative fixture
// (10): any empty identity field and any malformed or empty deadline fail
// closed.
func TestMatcherClaimRejectsIdentityTupleAndDeadlines(t *testing.T) {
	cases := []struct {
		name   string
		change func(*ClaimRequest)
	}{
		{"empty taskId", func(r *ClaimRequest) { r.TaskId = "" }},
		{"empty runId", func(r *ClaimRequest) { r.RunId = "" }},
		{"empty attemptId", func(r *ClaimRequest) { r.AttemptId = "" }},
		{"empty allocationId", func(r *ClaimRequest) { r.AllocationId = "" }},
		{"blank taskId", func(r *ClaimRequest) { r.TaskId = "   " }},
		{"malformed ackDeadlineAt", func(r *ClaimRequest) { r.AckDeadlineAt = "not-a-timestamp" }},
		{"malformed expiresAt", func(r *ClaimRequest) { r.ExpiresAt = "not-a-timestamp" }},
		{"empty ackDeadlineAt", func(r *ClaimRequest) { r.AckDeadlineAt = "" }},
		{"empty expiresAt", func(r *ClaimRequest) { r.ExpiresAt = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, matcher, registration, snapshot, evidences := eligibleFixture(t)
			request := testClaimRequest(registration, snapshot, evidences, hardenedRequirements(), "1")
			tc.change(&request)
			if _, err := matcher.Claim(request, dispatchTestNow); err == nil {
				t.Fatalf("Claim accepted %s", tc.name)
			}
		})
	}
}

// TestMatcherClaimUniqueClaimInvariant freezes negative fixture (11): a
// second claim for the identical (runId, attemptId) fails closed while the
// first lease exists.
func TestMatcherClaimUniqueClaimInvariant(t *testing.T) {
	_, matcher, registration, snapshot, evidences := eligibleFixture(t)
	request := testClaimRequest(registration, snapshot, evidences, hardenedRequirements(), "1")
	if _, err := matcher.Claim(request, dispatchTestNow); err != nil {
		t.Fatalf("the first claim failed: %v", err)
	}
	replay := request
	replay.AllocationId = "allocation-" + "replay"
	if _, err := matcher.Claim(replay, dispatchTestNow); err == nil {
		t.Fatal("a second claim for the identical (runId, attemptId) succeeded")
	} else if !strings.Contains(err.Error(), "already") {
		t.Fatalf("expected the unique claim rejection, got: %v", err)
	}
}

// TestMatcherRevalidateRejectsExpiredDeadline freezes negative fixture (15):
// once now reaches the deadlines the lease loses eligibility with
// deadline-exceeded, and there is no in-place retry of the identical
// attempt.
func TestMatcherRevalidateRejectsExpiredDeadline(t *testing.T) {
	_, matcher, registration, snapshot, evidences := eligibleFixture(t)
	request := testClaimRequest(registration, snapshot, evidences, hardenedRequirements(), "1")
	lease, err := matcher.Claim(request, dispatchTestNow)
	if err != nil {
		t.Fatalf("Claim failed: %v", err)
	}

	atExpiry, err := time.Parse(time.RFC3339, lease.ExpiresAt)
	if err != nil {
		t.Fatalf("parse expiresAt: %v", err)
	}
	err = matcher.Revalidate(lease, snapshot, evidences, hardenedRequirements(), atExpiry)
	if err == nil {
		t.Fatal("Revalidate accepted a lease at its expiresAt boundary")
	} else if !strings.Contains(err.Error(), string(CancelReasonDeadlineExceeded)) {
		t.Fatalf("expected cancelReason %s, got: %v", string(CancelReasonDeadlineExceeded), err)
	}

	pastAck, err := time.Parse(time.RFC3339, lease.AckDeadlineAt)
	if err != nil {
		t.Fatalf("parse ackDeadlineAt: %v", err)
	}
	err = matcher.Revalidate(lease, snapshot, evidences, hardenedRequirements(), pastAck)
	if err == nil {
		t.Fatal("Revalidate accepted a lease at its ackDeadlineAt boundary")
	} else if !strings.Contains(err.Error(), string(CancelReasonDeadlineExceeded)) {
		t.Fatalf("expected cancelReason %s, got: %v", string(CancelReasonDeadlineExceeded), err)
	}

	if _, err := matcher.Claim(request, dispatchTestNow); err == nil {
		t.Fatal("the unique claim guard allowed an in-place retry of the identical attempt")
	}
}

// TestMatcherRevalidateAfterRegistrationRevoke freezes negative fixture
// (16): a security-critical revoke kills the lease immediately without any
// drain window, and Cancel carries the machine-readable reason code.
func TestMatcherRevalidateAfterRegistrationRevoke(t *testing.T) {
	store, matcher, registration, snapshot, evidences := eligibleFixture(t)
	lease, err := matcher.Claim(testClaimRequest(registration, snapshot, evidences, hardenedRequirements(), "1"), dispatchTestNow)
	if err != nil {
		t.Fatalf("Claim failed: %v", err)
	}
	if err := store.Revoke(registration.RegistrationId); err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}
	err = matcher.Revalidate(lease, snapshot, evidences, hardenedRequirements(), dispatchTestNow)
	if err == nil {
		t.Fatal("Revalidate accepted a lease whose registration was revoked")
	}
	if !strings.Contains(err.Error(), string(CancelReasonSecurityCriticalRevoke)) {
		t.Fatalf("expected cancelReason %s, got: %v", string(CancelReasonSecurityCriticalRevoke), err)
	}
	if strings.Contains(err.Error(), "drain") {
		t.Fatal("a security-critical revoke must fail closed without any drain window")
	}

	cancelled, err := lease.Cancel(CancelReasonSecurityCriticalRevoke)
	if err != nil {
		t.Fatalf("Cancel rejected the security-critical-revoke reason: %v", err)
	}
	if cancelled.LeaseState != LeaseStateCancelled || cancelled.CancelReason != CancelReasonSecurityCriticalRevoke {
		t.Fatal("Cancel must carry the machine-readable reason code in the cancelled state")
	}
	if cancelled.Generation != lease.Generation+1 {
		t.Fatal("the cancelled record must bump the generation")
	}
}

// TestMatcherRevalidateAfterRegistrationExpiry freezes negative fixture
// (17).
func TestMatcherRevalidateAfterRegistrationExpiry(t *testing.T) {
	store, matcher, registration, snapshot, evidences := eligibleFixture(t)
	lease, err := matcher.Claim(testClaimRequest(registration, snapshot, evidences, hardenedRequirements(), "1"), dispatchTestNow)
	if err != nil {
		t.Fatalf("Claim failed: %v", err)
	}
	if err := store.Expire(registration.RegistrationId); err != nil {
		t.Fatalf("Expire failed: %v", err)
	}
	err = matcher.Revalidate(lease, snapshot, evidences, hardenedRequirements(), dispatchTestNow)
	if err == nil {
		t.Fatal("Revalidate accepted a lease whose registration expired")
	}
	if !strings.Contains(err.Error(), string(CancelReasonRegistrationExpired)) {
		t.Fatalf("expected cancelReason %s, got: %v", string(CancelReasonRegistrationExpired), err)
	}
}

// TestMatcherRevalidateAfterProtocolSubstitution freezes negative fixture
// (18): a snapshot impersonating the bound registrationId under a changed
// protocolVersion or provider identity fails closed with
// registration-incompatible.
func TestMatcherRevalidateAfterProtocolSubstitution(t *testing.T) {
	_, matcher, registration, _, evidences := eligibleFixture(t)
	lease, err := matcher.Claim(testClaimRequest(registration, testSnapshot(registration, nil, evidenceDigestsOf(evidences), nil), evidences, hardenedRequirements(), "1"), dispatchTestNow)
	if err != nil {
		t.Fatalf("Claim failed: %v", err)
	}
	snapshot := testSnapshot(registration, nil, evidenceDigestsOf(evidences), nil)

	protocolBump := testSnapshot(registration, nil, evidenceDigestsOf(evidences), func(s *provider.ProviderCapabilitySnapshot) {
		s.ProtocolVersion = "v1beta1"
	})
	err = matcher.Revalidate(lease, protocolBump, evidences, hardenedRequirements(), dispatchTestNow)
	if err == nil {
		t.Fatal("Revalidate accepted a snapshot with a substituted protocolVersion")
	}
	if !strings.Contains(err.Error(), string(CancelReasonRegistrationIncompatible)) {
		t.Fatalf("expected cancelReason %s, got: %v", string(CancelReasonRegistrationIncompatible), err)
	}

	renamed := testSnapshot(registration, nil, evidenceDigestsOf(evidences), func(s *provider.ProviderCapabilitySnapshot) {
		s.ProviderName = "provider-" + "substituted"
	})
	err = matcher.Revalidate(lease, renamed, evidences, hardenedRequirements(), dispatchTestNow)
	if err == nil {
		t.Fatal("Revalidate accepted a snapshot with a substituted providerName")
	}
	if !strings.Contains(err.Error(), string(CancelReasonRegistrationIncompatible)) {
		t.Fatalf("expected cancelReason %s, got: %v", string(CancelReasonRegistrationIncompatible), err)
	}

	if err := matcher.Revalidate(lease, snapshot, evidences, hardenedRequirements(), dispatchTestNow); err != nil {
		t.Fatalf("the unmodified snapshot must keep revalidating: %v", err)
	}
}

// TestMatcherRevalidateAfterSnapshotSupersede freezes negative fixture (19):
// both the successor with a new digest and the old record flipped to
// superseded fail closed with snapshot-superseded.
func TestMatcherRevalidateAfterSnapshotSupersede(t *testing.T) {
	_, matcher, registration, snapshot, evidences := eligibleFixture(t)
	lease, err := matcher.Claim(testClaimRequest(registration, snapshot, evidences, hardenedRequirements(), "1"), dispatchTestNow)
	if err != nil {
		t.Fatalf("Claim failed: %v", err)
	}

	successor := testSnapshot(registration, nil, evidenceDigestsOf(evidences), func(s *provider.ProviderCapabilitySnapshot) {
		s.CreatedAt = "2026-08-13T05:00:00Z"
	})
	if successor.ProviderCapabilitySnapshotDigest == snapshot.ProviderCapabilitySnapshotDigest {
		t.Fatal("the successor must carry a new snapshot digest")
	}
	err = matcher.Revalidate(lease, successor, evidences, hardenedRequirements(), dispatchTestNow)
	if err == nil {
		t.Fatal("Revalidate accepted the superseding successor snapshot")
	}
	if !strings.Contains(err.Error(), string(CancelReasonSnapshotSuperseded)) {
		t.Fatalf("expected cancelReason %s, got: %v", string(CancelReasonSnapshotSuperseded), err)
	}

	flipped := snapshot
	flipped.SnapshotState = provider.SnapshotStateSuperseded
	setTestSnapshotDigest(&flipped)
	err = matcher.Revalidate(lease, flipped, evidences, hardenedRequirements(), dispatchTestNow)
	if err == nil {
		t.Fatal("Revalidate accepted a snapshot flipped to superseded")
	}
	if !strings.Contains(err.Error(), string(CancelReasonSnapshotSuperseded)) {
		t.Fatalf("expected cancelReason %s, got: %v", string(CancelReasonSnapshotSuperseded), err)
	}
}

// TestMatcherRevalidateAfterSnapshotExpiry freezes negative fixture (20).
func TestMatcherRevalidateAfterSnapshotExpiry(t *testing.T) {
	_, matcher, registration, snapshot, evidences := eligibleFixture(t)
	lease, err := matcher.Claim(testClaimRequest(registration, snapshot, evidences, hardenedRequirements(), "1"), dispatchTestNow)
	if err != nil {
		t.Fatalf("Claim failed: %v", err)
	}
	flipped := snapshot
	flipped.SnapshotState = provider.SnapshotStateExpired
	setTestSnapshotDigest(&flipped)
	err = matcher.Revalidate(lease, flipped, evidences, hardenedRequirements(), dispatchTestNow)
	if err == nil {
		t.Fatal("Revalidate accepted a snapshot flipped to expired")
	}
	if !strings.Contains(err.Error(), string(CancelReasonSnapshotExpired)) {
		t.Fatalf("expected cancelReason %s, got: %v", string(CancelReasonSnapshotExpired), err)
	}
}

// TestMatcherRevalidateAfterEvidenceRevoke freezes negative fixture (21):
// both a revoked record under the bound digest and a reissued revoked record
// outside the closed set fail closed with evidence-revoked.
func TestMatcherRevalidateAfterEvidenceRevoke(t *testing.T) {
	_, matcher, registration, snapshot, evidences := eligibleFixture(t)
	lease, err := matcher.Claim(testClaimRequest(registration, snapshot, evidences, hardenedRequirements(), "1"), dispatchTestNow)
	if err != nil {
		t.Fatalf("Claim failed: %v", err)
	}

	revoked := evidences[0]
	revoked.EvidenceState = provider.EvidenceStateRevoked
	err = matcher.Revalidate(lease, snapshot, []provider.ConformanceEvidence{revoked}, hardenedRequirements(), dispatchTestNow)
	if err == nil {
		t.Fatal("Revalidate accepted revoked evidence")
	}
	if !strings.Contains(err.Error(), string(CancelReasonEvidenceRevoked)) {
		t.Fatalf("expected cancelReason %s, got: %v", string(CancelReasonEvidenceRevoked), err)
	}

	reissued := testEvidence(registration, func(e *provider.ConformanceEvidence) {
		e.EvidenceState = provider.EvidenceStateRevoked
	})
	err = matcher.Revalidate(lease, snapshot, []provider.ConformanceEvidence{reissued}, hardenedRequirements(), dispatchTestNow)
	if err == nil {
		t.Fatal("Revalidate accepted a reissued revoked evidence outside the closed set")
	}
	if !strings.Contains(err.Error(), string(CancelReasonEvidenceRevoked)) {
		t.Fatalf("expected cancelReason %s, got: %v", string(CancelReasonEvidenceRevoked), err)
	}
}

// TestMatcherRevalidateAfterEvidenceExpiry freezes negative fixture (22):
// once now crosses validUntil, or the record flips to expired, the lease
// loses eligibility with evidence-expired.
func TestMatcherRevalidateAfterEvidenceExpiry(t *testing.T) {
	_, matcher, registration, _, _ := eligibleFixture(t)
	shortLived := testEvidence(registration, func(e *provider.ConformanceEvidence) {
		e.ValidUntil = "2026-08-13T00:15:00Z"
	})
	snapshot := testSnapshot(registration, nil, []string{shortLived.EvidenceDigest}, nil)
	evidences := []provider.ConformanceEvidence{shortLived}
	lease, err := matcher.Claim(testClaimRequest(registration, snapshot, evidences, hardenedRequirements(), "1"), dispatchTestNow)
	if err != nil {
		t.Fatalf("Claim failed: %v", err)
	}

	atValidUntil, err := time.Parse(time.RFC3339, shortLived.ValidUntil)
	if err != nil {
		t.Fatalf("parse validUntil: %v", err)
	}
	err = matcher.Revalidate(lease, snapshot, evidences, hardenedRequirements(), atValidUntil)
	if err == nil {
		t.Fatal("Revalidate accepted evidence at its validUntil boundary")
	}
	if !strings.Contains(err.Error(), string(CancelReasonEvidenceExpired)) {
		t.Fatalf("expected cancelReason %s, got: %v", string(CancelReasonEvidenceExpired), err)
	}

	stateExpired := shortLived
	stateExpired.EvidenceState = provider.EvidenceStateExpired
	err = matcher.Revalidate(lease, snapshot, []provider.ConformanceEvidence{stateExpired}, hardenedRequirements(), dispatchTestNow)
	if err == nil {
		t.Fatal("Revalidate accepted expired-state evidence")
	}
	if !strings.Contains(err.Error(), string(CancelReasonEvidenceExpired)) {
		t.Fatalf("expected cancelReason %s, got: %v", string(CancelReasonEvidenceExpired), err)
	}
}

// TestMatcherRevalidatePersistentFailureAfterEligibilityLoss freezes
// negative fixture (23): after the eligibility loss every further Revalidate
// keeps failing, no ordinary replay resurrects the revoked registration, and
// rewriting the old lease digest never validates or passes the fencing
// guard.
func TestMatcherRevalidatePersistentFailureAfterEligibilityLoss(t *testing.T) {
	store, matcher, registration, snapshot, evidences := eligibleFixture(t)
	lease, err := matcher.Claim(testClaimRequest(registration, snapshot, evidences, hardenedRequirements(), "1"), dispatchTestNow)
	if err != nil {
		t.Fatalf("Claim failed: %v", err)
	}
	if err := store.Revoke(registration.RegistrationId); err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		err := matcher.Revalidate(lease, snapshot, evidences, hardenedRequirements(), dispatchTestNow)
		if err == nil {
			t.Fatalf("revalidate attempt %d accepted a revoked ledger", attempt)
		}
		if !strings.Contains(err.Error(), string(CancelReasonSecurityCriticalRevoke)) {
			t.Fatalf("attempt %d expected cancelReason %s, got: %v", attempt, string(CancelReasonSecurityCriticalRevoke), err)
		}
	}

	if _, err := store.Put(registration); err == nil {
		t.Fatal("an ordinary replay resurrected the revoked registration")
	}
	stored, err := store.Get(registration.RegistrationId)
	if err != nil {
		t.Fatalf("Get failed after the rejected replay: %v", err)
	}
	if stored.LifecycleState != provider.LifecycleStateRevoked {
		t.Fatalf("the rejected replay changed the lifecycleState to %q", string(stored.LifecycleState))
	}

	rewritten := lease
	rewritten.LeaseDigest = fixedDigest("lease-digest-" + "resurrected")
	if err := rewritten.Validate(); err == nil {
		t.Fatal("Validate accepted a rewritten leaseDigest")
	}
	if err := ValidateLeaseFencing(rewritten, rewritten.Generation, rewritten.FencingToken); err == nil {
		t.Fatal("the fencing guard accepted a lease with a rewritten leaseDigest")
	}
}

// TestMatcherReclaimAfterDeadlineExceededRequiresNewAttempt freezes negative
// fixture (24): after the lease loses eligibility, a new attempt with a new
// allocation claims again while the invalidated old attempt stays rejected
// by the unique claim guard.
func TestMatcherReclaimAfterDeadlineExceededRequiresNewAttempt(t *testing.T) {
	_, matcher, registration, snapshot, evidences := eligibleFixture(t)
	request := testClaimRequest(registration, snapshot, evidences, hardenedRequirements(), "1")
	first, err := matcher.Claim(request, dispatchTestNow)
	if err != nil {
		t.Fatalf("Claim failed: %v", err)
	}

	lateNow, err := time.Parse(time.RFC3339, "2026-08-13T03:00:00Z")
	if err != nil {
		t.Fatalf("parse lateNow: %v", err)
	}
	err = matcher.Revalidate(first, snapshot, evidences, hardenedRequirements(), lateNow)
	if err == nil {
		t.Fatal("Revalidate accepted a lease past its expiresAt")
	}
	if !strings.Contains(err.Error(), string(CancelReasonDeadlineExceeded)) {
		t.Fatalf("expected cancelReason %s, got: %v", string(CancelReasonDeadlineExceeded), err)
	}
	if _, err := first.Cancel(CancelReasonDeadlineExceeded); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}

	continuation := request
	continuation.AttemptId = "attempt-" + "2"
	continuation.AllocationId = "allocation-" + "2"
	continuation.AckDeadlineAt = "2026-08-13T03:30:00Z"
	continuation.ExpiresAt = "2026-08-13T05:00:00Z"
	second, err := matcher.Claim(continuation, lateNow)
	if err != nil {
		t.Fatalf("a new attempt must be able to claim again: %v", err)
	}
	if err := second.Validate(); err != nil {
		t.Fatalf("the continuation lease does not validate: %v", err)
	}
	if second.AttemptId != continuation.AttemptId || second.AllocationId != continuation.AllocationId {
		t.Fatal("the continuation lease must carry the new attempt identity")
	}
	if err := matcher.Revalidate(second, snapshot, evidences, hardenedRequirements(), lateNow); err != nil {
		t.Fatalf("the continuation lease must revalidate: %v", err)
	}

	if _, err := matcher.Claim(request, lateNow); err == nil {
		t.Fatal("the unique claim guard allowed the invalidated old attempt to reclaim")
	}
}

// TestMatcherRestartRecovery freezes positive fixture (25): after a restart
// the durable registration still claims and revalidates, and the
// deterministic derivation reproduces the identical leaseId.
func TestMatcherRestartRecovery(t *testing.T) {
	dir := t.TempDir()
	store, err := provider.NewRegistrationStore(dir)
	if err != nil {
		t.Fatalf("NewRegistrationStore: %v", err)
	}
	registration := testRegistration("1")
	if _, err := store.Put(registration); err != nil {
		t.Fatalf("Put rejected the baseline registration: %v", err)
	}
	evidence := testEvidence(registration, nil)
	snapshot := testSnapshot(registration, nil, []string{evidence.EvidenceDigest}, nil)
	evidences := []provider.ConformanceEvidence{evidence}
	request := testClaimRequest(registration, snapshot, evidences, hardenedRequirements(), "1")

	firstMatcher := NewMatcherWithEdgeRuntime(store, newClaimEdgeRuntime(t))
	firstLease, err := firstMatcher.Claim(request, dispatchTestNow)
	if err != nil {
		t.Fatalf("Claim before the restart failed: %v", err)
	}

	reopened, err := provider.NewRegistrationStore(dir)
	if err != nil {
		t.Fatalf("NewRegistrationStore failed to reopen the ledger directory: %v", err)
	}
	secondMatcher := NewMatcherWithEdgeRuntime(reopened, newClaimEdgeRuntime(t))

	recoveredLease, err := secondMatcher.Claim(request, dispatchTestNow)
	if err != nil {
		t.Fatalf("Claim after the restart recovery failed: %v", err)
	}
	if recoveredLease.LeaseId != firstLease.LeaseId {
		t.Fatal("the deterministic derivation must reproduce the identical leaseId after restart")
	}
	firstEdge, ok := firstMatcher.IssuedResultCapability(firstLease.LeaseId)
	if !ok {
		t.Fatal("the pre-restart claim must have issued a DispatchResultCapability")
	}
	recoveredEdge, ok := secondMatcher.IssuedResultCapability(recoveredLease.LeaseId)
	if !ok {
		t.Fatal("the recovered claim must have issued a DispatchResultCapability")
	}
	if recoveredEdge.EdgeDigest != firstEdge.EdgeDigest {
		t.Fatal("the deterministic issuance must reproduce the identical edge digest after restart")
	}
	if err := recoveredLease.Validate(); err != nil {
		t.Fatalf("the recovered lease does not validate: %v", err)
	}
	if err := secondMatcher.Revalidate(recoveredLease, snapshot, evidences, hardenedRequirements(), dispatchTestNow); err != nil {
		t.Fatalf("Revalidate after the restart recovery failed: %v", err)
	}
	if err := secondMatcher.Revalidate(firstLease, snapshot, evidences, hardenedRequirements(), dispatchTestNow); err != nil {
		t.Fatalf("the pre-restart lease must revalidate against the recovered ledger: %v", err)
	}
}

// TestMatcherProviderSubstitutionFailsClosed freezes negative fixture (26):
// a substituted provider registers under a distinct idempotency identity and
// can never claim under the original registrationId; its own registration
// claims normally.
func TestMatcherProviderSubstitutionFailsClosed(t *testing.T) {
	store, err := provider.NewRegistrationStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistrationStore: %v", err)
	}
	original := testRegistration("original")
	if _, err := store.Put(original); err != nil {
		t.Fatalf("Put rejected the original registration: %v", err)
	}
	substitute := testRegistration("substitute")
	substitute.ProviderName = "provider-" + "b"
	substitute.ProviderVersion = "2.0.0"
	setTestRegistrationDigest(&substitute)
	if _, err := store.Put(substitute); err != nil {
		t.Fatalf("the substituted provider must register under its own idempotency identity: %v", err)
	}
	originalIdentity, err := original.IdempotencyDigest()
	if err != nil {
		t.Fatalf("IdempotencyDigest failed: %v", err)
	}
	substituteIdentity, err := substitute.IdempotencyDigest()
	if err != nil {
		t.Fatalf("IdempotencyDigest failed: %v", err)
	}
	if originalIdentity == substituteIdentity {
		t.Fatal("the substitution must carry a distinct idempotency identity")
	}

	matcher := NewMatcherWithEdgeRuntime(store, newClaimEdgeRuntime(t))
	originalEvidence := testEvidence(original, nil)
	substituteSnapshot := testSnapshot(original, nil, []string{originalEvidence.EvidenceDigest}, func(s *provider.ProviderCapabilitySnapshot) {
		s.ProviderName = substitute.ProviderName
		s.ProviderVersion = substitute.ProviderVersion
	})
	request := testClaimRequest(original, substituteSnapshot, []provider.ConformanceEvidence{originalEvidence}, hardenedRequirements(), "1")
	_, err = matcher.Claim(request, dispatchTestNow)
	if err == nil {
		t.Fatal("Claim accepted a substitute provider identity under the original registrationId")
	}
	if !strings.Contains(err.Error(), "align") {
		t.Fatalf("expected the identity alignment rejection, got: %v", err)
	}

	substituteEvidence := testEvidence(substitute, nil)
	substituteSnapshotAligned := testSnapshot(substitute, nil, []string{substituteEvidence.EvidenceDigest}, nil)
	substituteRequest := testClaimRequest(substitute, substituteSnapshotAligned, []provider.ConformanceEvidence{substituteEvidence}, hardenedRequirements(), "2")
	if _, err := matcher.Claim(substituteRequest, dispatchTestNow); err != nil {
		t.Fatalf("the substitute registration must claim under its own registrationId: %v", err)
	}
}

// TestMatcherMatchDirectSemantics guards the Match surface in isolation: an
// eligible bundle passes, zero-value requirements fail closed, and Match
// does not require a durable store binding.
func TestMatcherMatchDirectSemantics(t *testing.T) {
	_, matcher, registration, snapshot, evidences := eligibleFixture(t)
	if err := matcher.Match(registration, snapshot, evidences, hardenedRequirements(), dispatchTestNow); err != nil {
		t.Fatalf("Match rejected an eligible bundle: %v", err)
	}
	if err := matcher.Match(registration, snapshot, evidences, domain.SandboxRequirements{}, dispatchTestNow); err == nil {
		t.Fatal("Match accepted zero-value requirements")
	}
	storeless := NewMatcher(nil)
	if err := storeless.Match(registration, snapshot, evidences, hardenedRequirements(), dispatchTestNow); err != nil {
		t.Fatalf("Match must not require a store binding: %v", err)
	}
}

// TestMatcherRevalidateRejectsUnboundStore guards the revalidate
// precondition: a matcher without a durable store fails closed.
func TestMatcherRevalidateRejectsUnboundStore(t *testing.T) {
	err := NewMatcher(nil).Revalidate(validLease(), provider.ProviderCapabilitySnapshot{}, nil, hardenedRequirements(), dispatchTestNow)
	if err == nil {
		t.Fatal("Revalidate succeeded on a matcher without a durable store")
	}
	if !strings.Contains(err.Error(), "memory-only") {
		t.Fatalf("expected the memory-only rejection, got: %v", err)
	}
}

// TestMatcherRevalidateRejectsTerminalLease guards the in-flight
// precondition: a cancelled or expired lease is never revalidated.
func TestMatcherRevalidateRejectsTerminalLease(t *testing.T) {
	_, matcher, registration, snapshot, evidences := eligibleFixture(t)
	lease, err := matcher.Claim(testClaimRequest(registration, snapshot, evidences, hardenedRequirements(), "1"), dispatchTestNow)
	if err != nil {
		t.Fatalf("Claim failed: %v", err)
	}
	cancelled, err := lease.Cancel(CancelReasonDeadlineExceeded)
	if err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}
	if err := matcher.Revalidate(cancelled, snapshot, evidences, hardenedRequirements(), dispatchTestNow); err == nil {
		t.Fatal("Revalidate accepted a cancelled lease")
	}
	expired, err := lease.Expire(dispatchTestNow)
	if err != nil {
		t.Fatalf("Expire failed: %v", err)
	}
	if err := matcher.Revalidate(expired, snapshot, evidences, hardenedRequirements(), dispatchTestNow); err == nil {
		t.Fatal("Revalidate accepted an expired lease")
	}
}

// recordingRevokeHook records the immediate-effect hook invocations of the
// security-critical revocation fixtures.
type recordingRevokeHook struct {
	calls []string
}

func (h *recordingRevokeHook) OnSecurityCriticalRevoke(kind authority.EdgeKind, edgeDigest string, at time.Time) error {
	h.calls = append(h.calls, string(kind)+":"+edgeDigest)
	return nil
}

// dispatchResultUseRequestFor assembles the fully aligned use request of
// the issued edge for the claim-wiring fixtures.
func dispatchResultUseRequestFor(edge authority.DispatchResultCapability, lease DispatchLease, seed string) authority.DispatchResultUseRequest {
	return authority.DispatchResultUseRequest{
		SourceActor:   edge.SourceActor,
		TargetActor:   edge.TargetActor,
		Operation:     edge.Operation,
		AttemptId:     lease.AttemptId,
		AllocationId:  lease.AllocationId,
		LeaseId:       lease.LeaseId,
		Generation:    lease.Generation,
		FencingToken:  lease.FencingToken,
		RequestDigest: fixedDigest(seed),
	}
}

// TestMatcherClaimRequiresBoundEdgeRuntime freezes the typed-edge
// precondition: a matcher without the Core edge runtime fails every claim
// closed before any lease is recorded.
func TestMatcherClaimRequiresBoundEdgeRuntime(t *testing.T) {
	store, err := provider.NewRegistrationStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistrationStore: %v", err)
	}
	registration := testRegistration("1")
	if _, err := store.Put(registration); err != nil {
		t.Fatalf("Put rejected the baseline registration: %v", err)
	}
	evidence := testEvidence(registration, nil)
	snapshot := testSnapshot(registration, nil, []string{evidence.EvidenceDigest}, nil)
	request := testClaimRequest(registration, snapshot, []provider.ConformanceEvidence{evidence}, hardenedRequirements(), "1")
	if _, err := NewMatcher(store).Claim(request, dispatchTestNow); err == nil {
		t.Fatal("Claim succeeded without a bound typed-edge runtime")
	} else if !strings.Contains(err.Error(), "typed-edge runtime") {
		t.Fatalf("expected the typed-edge runtime precondition, got: %v", err)
	}
}

// TestMatcherClaimIssuesDispatchResultCapability freezes the positive
// issuance wiring: the accepted claim issues the Core capability bound to
// the lease identity (attempt/allocation/generation/fencingToken),
// recoverable from the matcher index, and the authority runtime recheck
// accepts the aligned result use.
func TestMatcherClaimIssuesDispatchResultCapability(t *testing.T) {
	matcher, runtime, registration, snapshot, evidences := eligibleEdgeFixture(t)
	request := testClaimRequest(registration, snapshot, evidences, hardenedRequirements(), "1")
	lease, err := matcher.Claim(request, dispatchTestNow)
	if err != nil {
		t.Fatalf("Claim failed: %v", err)
	}
	edge, ok := matcher.IssuedResultCapability(lease.LeaseId)
	if !ok {
		t.Fatal("the accepted claim must issue a DispatchResultCapability")
	}
	if err := edge.Validate(); err != nil {
		t.Fatalf("the issued edge does not validate: %v", err)
	}
	if !edge.Issuer.Equal(testAuthorityNamespace()) {
		t.Fatal("the issued edge must carry the Core issuer")
	}
	if !edge.SourceActor.Equal(registration.SecurityDomainId) {
		t.Fatal("the edge sourceActor must be the claimed registration securityDomainId")
	}
	if !edge.TargetActor.Equal(request.TargetActor) {
		t.Fatal("the edge targetActor must be the claimed result-ingress target")
	}
	if edge.Operation != authority.DispatchResultOperationAccept {
		t.Fatalf("the claim issues the result acceptance operation, got %q", string(edge.Operation))
	}
	if edge.BoundAttemptId != request.AttemptId || edge.BoundAllocationId != request.AllocationId {
		t.Fatal("the edge must bind the claimed attempt and allocation")
	}
	if edge.Expiry != request.ExpiresAt {
		t.Fatal("the edge expiry must be bounded by the lease expiry window")
	}
	if edge.Generation != 1 || edge.RevocationGeneration != 0 {
		t.Fatal("the issued edge must start at generation 1 unrevoked")
	}
	current, currentLease, ok := runtime.CurrentDispatchResultCapability(edge.EdgeDigest)
	if !ok || current != edge {
		t.Fatal("the authority ledger must recover the issued edge")
	}
	if currentLease.LeaseId != lease.LeaseId || currentLease.Generation != lease.Generation || currentLease.FencingToken != lease.FencingToken {
		t.Fatal("the authority ledger must record the exact lease identity of the claim")
	}
	if err := runtime.RecheckDispatchResult(edge, dispatchResultUseRequestFor(edge, lease, "result-request-1"), dispatchTestNow); err != nil {
		t.Fatalf("the current-ledger recheck rejected the aligned result use: %v", err)
	}
	if _, ok := matcher.IssuedResultCapability(fixedDigest("lease-unknown")); ok {
		t.Fatal("an unknown leaseId must not expose an issued capability")
	}
}

// TestMatcherClaimRejectsIllegalEdgeTarget freezes the target gate: a
// targetActor outside the closed typed-edge matrix or an invalid
// targetActor fails the whole claim closed, and no lease is recorded.
func TestMatcherClaimRejectsIllegalEdgeTarget(t *testing.T) {
	t.Run("execution target violates the typed-edge matrix", func(t *testing.T) {
		_, matcher, registration, snapshot, evidences := eligibleFixture(t)
		request := testClaimRequest(registration, snapshot, evidences, hardenedRequirements(), "1")
		request.TargetActor = authority.SecurityDomainId{
			TenantNamespace:   "default",
			TrustDomainKind:   authority.TrustDomainKindExecution,
			IsolationDomainId: "isolation-other",
		}
		if _, err := matcher.Claim(request, dispatchTestNow); err == nil {
			t.Fatal("Claim accepted a targetActor outside the typed-edge matrix")
		}
		if _, ok := matcher.IssuedResultCapability("anything"); ok {
			t.Fatal("a failed claim must not record an issued capability")
		}
	})
	t.Run("invalid target actor", func(t *testing.T) {
		_, matcher, registration, snapshot, evidences := eligibleFixture(t)
		request := testClaimRequest(registration, snapshot, evidences, hardenedRequirements(), "1")
		request.TargetActor = authority.SecurityDomainId{}
		if _, err := matcher.Claim(request, dispatchTestNow); err == nil {
			t.Fatal("Claim accepted an invalid targetActor")
		}
	})
}

// TestMatcherClaimEdgeRevocationFailsRecheck freezes the security-critical
// revocation path of the issued edge: the revocation fact fires the
// immediate-effect hook and fails every later result use closed.
func TestMatcherClaimEdgeRevocationFailsRecheck(t *testing.T) {
	matcher, runtime, registration, snapshot, evidences := eligibleEdgeFixture(t)
	hook := &recordingRevokeHook{}
	runtime.BindSecurityCriticalRevokeHook(hook)
	request := testClaimRequest(registration, snapshot, evidences, hardenedRequirements(), "1")
	lease, err := matcher.Claim(request, dispatchTestNow)
	if err != nil {
		t.Fatalf("Claim failed: %v", err)
	}
	edge, ok := matcher.IssuedResultCapability(lease.LeaseId)
	if !ok {
		t.Fatal("the accepted claim must issue a DispatchResultCapability")
	}
	useRequest := dispatchResultUseRequestFor(edge, lease, "result-request-1")
	if err := runtime.RecheckDispatchResult(edge, useRequest, dispatchTestNow); err != nil {
		t.Fatalf("the pre-revocation recheck failed: %v", err)
	}
	if _, err := runtime.RevokeDispatchResultCapability(edge.EdgeDigest, authority.EdgeRevocationSecurityCritical, dispatchTestNow); err != nil {
		t.Fatalf("security-critical revocation failed: %v", err)
	}
	if len(hook.calls) != 1 || hook.calls[0] != string(authority.EdgeKindDispatchResultCapability)+":"+edge.EdgeDigest {
		t.Fatalf("the immediate-effect hook must fire exactly once with the edge identity, got %v", hook.calls)
	}
	if err := runtime.RecheckDispatchResult(edge, useRequest, dispatchTestNow); !errors.Is(err, authority.ErrEdgeRevoked) {
		t.Fatalf("expected ErrEdgeRevoked after the security-critical revocation, got: %v", err)
	}
}
