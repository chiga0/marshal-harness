package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/provider"
	"github.com/chiga0/marshal-harness/internal/repository"
)

const (
	registrationTrustRoot = "fixture-trust-root-key"
	fixturePrincipal      = "fixture-provider"
)

// registrationFixture assembles one hermetic provider-registration/control
// Port bound to a fresh repository identity.
type registrationFixture struct {
	t              *testing.T
	port           *RegistrationPort
	repositoryRoot string
	stateRoot      string
	scope          string
	namespace      authority.AuthorityNamespaceId
}

func newRegistrationFixtureWithTrustRoots(t *testing.T, trustRoots []string) *registrationFixture {
	t.Helper()
	root := fixtureRepository(t)
	stateRoot := filepath.Join(root, ".marshal")
	if err := (repository.State{RepositoryRoot: root, StateRoot: stateRoot}).Init(); err != nil {
		t.Fatalf("bind the fixture repository identity: %v", err)
	}
	port, err := NewRegistrationPort(RegistrationPortConfig{
		StateRoot:       stateRoot,
		RepositoryRoot:  root,
		Now:             func() time.Time { return fixtureClock },
		TrustRootKeyIds: trustRoots,
	})
	if err != nil {
		t.Fatalf("assemble the registration port: %v", err)
	}
	scope := "repo:" + filepath.ToSlash(root)
	return &registrationFixture{
		t:              t,
		port:           port,
		repositoryRoot: root,
		stateRoot:      stateRoot,
		scope:          scope,
		namespace: authority.AuthorityNamespaceId{
			TenantNamespace:  localTenantNamespace,
			ControlPlaneId:   localControlPlaneID,
			AuthorityScopeId: scope,
		},
	}
}

func newRegistrationFixture(t *testing.T) *registrationFixture {
	t.Helper()
	return newRegistrationFixtureWithTrustRoots(t, []string{registrationTrustRoot})
}

func (f *registrationFixture) do(method, path string, headers map[string]string, body []byte) recordedResponse {
	f.t.Helper()
	return f.doWithContext(context.Background(), method, path, headers, body)
}

func (f *registrationFixture) doWithContext(ctx context.Context, method, path string, headers map[string]string, body []byte) recordedResponse {
	f.t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader).WithContext(ctx)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	f.port.ServeHTTP(recorder, request)
	return recordedResponse{status: recorder.Code, header: recorder.Result().Header, body: recorder.Body.Bytes()}
}

func (f *registrationFixture) headers(requestID string) map[string]string {
	return f.headersFor(requestID, fixturePrincipal)
}

func (f *registrationFixture) headersFor(requestID, principal string) map[string]string {
	return map[string]string{
		HeaderRequestID:       requestID,
		HeaderProtocolVersion: RegistrationProtocolFamily + "/" + RegistrationProtocolVersion,
		HeaderPrincipal:       principal,
		HeaderAudience:        RegistrationAudience,
		HeaderScope:           f.scope,
		HeaderDeadline:        fixtureDeadline,
	}
}

// registrationOptions steers the fixture registration builder.
type registrationOptions struct {
	registrationID  string
	principal       string
	recordKey       string
	trustRootKeyID  string
	createdAt       string
	providerVersion string
	namespace       *authority.AuthorityNamespaceId
	scope           string
}

// buildRegistration renders one self-consistent ProviderRegistration record:
// the requestDigest is the canonical digest of the registration intent
// binding and the registrationDigest is the detached canonical content
// digest recomputed exactly like the admission path.
func (f *registrationFixture) buildRegistration(opts registrationOptions) provider.ProviderRegistration {
	f.t.Helper()
	if opts.registrationID == "" {
		opts.registrationID = "reg-fixture"
	}
	if opts.principal == "" {
		opts.principal = fixturePrincipal
	}
	if opts.recordKey == "" {
		opts.recordKey = "record-key-" + opts.registrationID
	}
	if opts.trustRootKeyID == "" {
		opts.trustRootKeyID = registrationTrustRoot
	}
	if opts.createdAt == "" {
		opts.createdAt = "2026-08-13T11:30:00Z"
	}
	if opts.providerVersion == "" {
		opts.providerVersion = "1.0.0"
	}
	namespace := f.namespace
	if opts.namespace != nil {
		namespace = *opts.namespace
	}
	scope := f.scope
	if opts.scope != "" {
		scope = opts.scope
	}
	intentBinding := map[string]any{
		"principal":       opts.principal,
		"providerName":    "fixture-sandbox",
		"providerVersion": opts.providerVersion,
		"idempotencyKey":  opts.recordKey,
	}
	requestDigest, err := canonical.DigestJSON(mustMarshal(f.t, intentBinding))
	if err != nil {
		f.t.Fatalf("digest the registration intent binding: %v", err)
	}
	registration := provider.ProviderRegistration{
		RegistrationId:       opts.registrationID,
		AuthorityNamespaceId: namespace,
		SecurityDomainId: authority.SecurityDomainId{
			TenantNamespace:   "local",
			TrustDomainKind:   authority.TrustDomainKindExecution,
			IsolationDomainId: "default",
		},
		Principal:       opts.principal,
		ProviderType:    "sandbox",
		ProviderName:    "fixture-sandbox",
		ProviderVersion: opts.providerVersion,
		ProtocolVersion: "fixture-sandbox/v1",
		Scope:           scope,
		IdempotencyKey:  opts.recordKey,
		RequestDigest:   requestDigest,
		Attestation: provider.Attestation{
			ProviderInstanceId: "fixture-instance-1",
			ConfigDigest:       "sha256:" + strings.Repeat("c", 64),
			TrustRootKeyId:     opts.trustRootKeyID,
			TrustRootAlgorithm: "ecdsa-p256",
		},
		LifecycleState: provider.LifecycleStateCreate,
		CreatedAt:      opts.createdAt,
	}
	digest, err := registration.Digest()
	if err != nil {
		f.t.Fatalf("digest the registration record: %v", err)
	}
	registration.RegistrationDigest = digest
	return registration
}

// buildEvidence renders one self-consistent ConformanceEvidence aligned with
// the registration's attestation chain.
func (f *registrationFixture) buildEvidence(registration provider.ProviderRegistration, validUntil string) provider.ConformanceEvidence {
	f.t.Helper()
	evidence := provider.ConformanceEvidence{
		AuthorityNamespaceId: registration.AuthorityNamespaceId,
		SecurityDomainId:     registration.SecurityDomainId,
		ProviderInstanceId:   registration.Attestation.ProviderInstanceId,
		ConfigDigest:         registration.Attestation.ConfigDigest,
		TrustRootKeyId:       registration.Attestation.TrustRootKeyId,
		SuiteName:            "fixture-suite",
		ProbeArtifactDigest:  "sha256:" + strings.Repeat("d", 64),
		DimensionResults: map[provider.ConformanceDimension]provider.DimensionResult{
			provider.ConformanceDimensionMount:      provider.DimensionResultPassed,
			provider.ConformanceDimensionNetwork:    provider.DimensionResultPassed,
			provider.ConformanceDimensionResource:   provider.DimensionResultPassed,
			provider.ConformanceDimensionCredential: provider.DimensionResultPassed,
		},
		EvidenceState:      provider.EvidenceStateValid,
		ProviderSelfSigned: false,
		SignedAt:           "2026-08-13T11:00:00Z",
		ValidUntil:         validUntil,
	}
	digest, err := evidence.Digest()
	if err != nil {
		f.t.Fatalf("digest the evidence record: %v", err)
	}
	evidence.EvidenceDigest = digest
	return evidence
}

// registrationBody builds one idempotent registration submit body: the
// payload plus its canonical requestDigest, exactly the c1 quadruple
// envelope discipline.
func registrationBody(t *testing.T, key string, payload any) []byte {
	t.Helper()
	payloadRaw := mustMarshal(t, payload)
	digest, err := canonical.DigestJSON(payloadRaw)
	if err != nil {
		t.Fatalf("digest the registration payload: %v", err)
	}
	return mustMarshal(t, map[string]any{
		"idempotencyKey": key,
		"requestDigest":  digest,
		"payload":        json.RawMessage(payloadRaw),
	})
}

func registrationPayload(registration provider.ProviderRegistration, evidences ...provider.ConformanceEvidence) map[string]any {
	payload := map[string]any{"registration": registration}
	if len(evidences) > 0 {
		payload["evidences"] = evidences
	}
	return payload
}

// ledgerLines counts the durable registration ledger facts.
func (f *registrationFixture) ledgerLines() int {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.stateRoot, "registrations", "registrations.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		f.t.Fatalf("read the registration ledger: %v", err)
	}
	lines := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			lines++
		}
	}
	return lines
}

// TestRegistrationSubmitAcceptAndStatusQuery drives the frozen happy path:
// submit admits the record into the durable authority ledger, the identical
// replay merges without a second ledger fact, and the status query projects
// the current record.
func TestRegistrationSubmitAcceptAndStatusQuery(t *testing.T) {
	fixture := newRegistrationFixture(t)
	registration := fixture.buildRegistration(registrationOptions{registrationID: "reg-accept-1"})
	body := registrationBody(t, "env-key-accept", registrationPayload(registration))

	response := fixture.do(http.MethodPost, APIPrefix+"/registrations", withContentType(fixture.headers("req-reg-accept")), body)
	if response.status != http.StatusCreated {
		t.Fatalf("submit status = %d, body: %s", response.status, response.body)
	}
	var accepted RegistrationAccepted
	if err := json.Unmarshal(response.body, &accepted); err != nil {
		t.Fatalf("decode RegistrationAccepted: %v", err)
	}
	if accepted.Kind != "RegistrationAccepted" || accepted.RegistrationId != "reg-accept-1" ||
		accepted.LifecycleState != provider.LifecycleStateCreate ||
		accepted.RegistrationDigest != registration.RegistrationDigest ||
		accepted.CreatedAt != registration.CreatedAt {
		t.Fatalf("RegistrationAccepted = %+v", accepted)
	}
	if !accepted.AuthorityNamespaceId.Equal(fixture.namespace) {
		t.Fatalf("the accept result lacks the owning authorityNamespaceId: %+v", accepted.AuthorityNamespaceId)
	}
	if !accepted.SecurityDomainId.Equal(registration.SecurityDomainId) {
		t.Fatalf("the accept result lost the actor securityDomainId provenance: %+v", accepted.SecurityDomainId)
	}
	if lines := fixture.ledgerLines(); lines != 1 {
		t.Fatalf("the durable ledger carries %d facts, want 1", lines)
	}

	// Identical replay merges: same quadruple, stored result verbatim, no
	// second ledger fact.
	replay := fixture.do(http.MethodPost, APIPrefix+"/registrations", withContentType(fixture.headers("req-reg-replay")), body)
	if replay.status != http.StatusOK {
		t.Fatalf("replay status = %d, body: %s", replay.status, replay.body)
	}
	var replayAccepted RegistrationAccepted
	if err := json.Unmarshal(replay.body, &replayAccepted); err != nil {
		t.Fatalf("decode replay RegistrationAccepted: %v", err)
	}
	if !reflect.DeepEqual(replayAccepted, accepted) {
		t.Fatalf("the replay diverged from the stored result:\n got %+v\nwant %+v", replayAccepted, accepted)
	}
	if lines := fixture.ledgerLines(); lines != 1 {
		t.Fatalf("the replay appended a ledger fact: %d facts", lines)
	}

	// Status query projects the accepted record with its lifecycle state.
	status := fixture.do(http.MethodGet, APIPrefix+"/registrations/reg-accept-1", fixture.headers("req-reg-status"), nil)
	if status.status != http.StatusOK {
		t.Fatalf("status query status = %d, body: %s", status.status, status.body)
	}
	var view RegistrationStatus
	if err := json.Unmarshal(status.body, &view); err != nil {
		t.Fatalf("decode RegistrationStatus: %v", err)
	}
	if view.Kind != "RegistrationStatus" || !view.AuthorityNamespaceId.Equal(fixture.namespace) {
		t.Fatalf("RegistrationStatus envelope = %+v", view)
	}
	if !reflect.DeepEqual(view.Registration, registration) {
		t.Fatalf("status query projected a divergent record:\n got %+v\nwant %+v", view.Registration, registration)
	}
}

// TestRegistrationEnvelopeIdempotencyConflict proves the quadruple rule: the
// identical envelope key with a different request digest conflicts fail
// closed and never reaches the durable ledger.
func TestRegistrationEnvelopeIdempotencyConflict(t *testing.T) {
	fixture := newRegistrationFixture(t)
	first := fixture.buildRegistration(registrationOptions{registrationID: "reg-env-conflict-1"})
	second := fixture.buildRegistration(registrationOptions{registrationID: "reg-env-conflict-2"})

	response := fixture.do(http.MethodPost, APIPrefix+"/registrations",
		withContentType(fixture.headers("req-env-1")), registrationBody(t, "env-key-conflict", registrationPayload(first)))
	if response.status != http.StatusCreated {
		t.Fatalf("first submit status = %d, body: %s", response.status, response.body)
	}
	conflict := fixture.do(http.MethodPost, APIPrefix+"/registrations",
		withContentType(fixture.headers("req-env-2")), registrationBody(t, "env-key-conflict", registrationPayload(second)))
	if conflict.status != http.StatusConflict {
		t.Fatalf("conflict status = %d, body: %s", conflict.status, conflict.body)
	}
	body := conflict.decodeError(t)
	if body.Code != CodeIdempotencyConflict || body.Reason != "idempotency-key-conflict" {
		t.Fatalf("conflict error = %+v", body)
	}
	if lines := fixture.ledgerLines(); lines != 1 {
		t.Fatalf("the conflict appended a ledger fact: %d facts", lines)
	}
}

// TestRegistrationLedgerConflict proves the durable ledger's digest-verified
// put-if-absent: the identical idempotency identity with a different
// registrationDigest, and a repeated registrationId under a different
// identity, both fail closed as conflicts.
func TestRegistrationLedgerConflict(t *testing.T) {
	fixture := newRegistrationFixture(t)
	accepted := fixture.buildRegistration(registrationOptions{registrationID: "reg-ledger-conflict"})
	response := fixture.do(http.MethodPost, APIPrefix+"/registrations",
		withContentType(fixture.headers("req-ledger-1")), registrationBody(t, "ledger-key-1", registrationPayload(accepted)))
	if response.status != http.StatusCreated {
		t.Fatalf("first submit status = %d, body: %s", response.status, response.body)
	}

	// Identical identity + idempotencyKey + requestDigest but a different
	// createdAt, hence a different registrationDigest: never merges, never
	// overwrites.
	divergent := fixture.buildRegistration(registrationOptions{
		registrationID: "reg-ledger-conflict",
		createdAt:      "2026-08-13T11:45:00Z",
	})
	if divergent.RegistrationDigest == accepted.RegistrationDigest {
		t.Fatalf("the fixture failed to diverge the registrationDigest")
	}
	conflict := fixture.do(http.MethodPost, APIPrefix+"/registrations",
		withContentType(fixture.headers("req-ledger-2")), registrationBody(t, "ledger-key-2", registrationPayload(divergent)))
	if conflict.status != http.StatusConflict {
		t.Fatalf("digest conflict status = %d, body: %s", conflict.status, conflict.body)
	}
	if body := conflict.decodeError(t); body.Code != CodeIdempotencyConflict || body.Reason != "registration-conflict" {
		t.Fatalf("digest conflict error = %+v", body)
	}

	// The identical registrationId under a different idempotency identity
	// (substituted provider version) conflicts as well.
	substitute := fixture.buildRegistration(registrationOptions{
		registrationID:  "reg-ledger-conflict",
		providerVersion: "2.0.0",
	})
	conflict = fixture.do(http.MethodPost, APIPrefix+"/registrations",
		withContentType(fixture.headers("req-ledger-3")), registrationBody(t, "ledger-key-3", registrationPayload(substitute)))
	if conflict.status != http.StatusConflict {
		t.Fatalf("substitution conflict status = %d, body: %s", conflict.status, conflict.body)
	}
	if body := conflict.decodeError(t); body.Code != CodeIdempotencyConflict || body.Reason != "registration-conflict" {
		t.Fatalf("substitution conflict error = %+v", body)
	}
	if lines := fixture.ledgerLines(); lines != 1 {
		t.Fatalf("the conflicts appended ledger facts: %d facts", lines)
	}
}

// TestRegistrationTrustRootChain covers the ADR 0018 §11/§12 chain
// validation: unpinned trust roots, an empty pinned set, misaligned and
// expired evidence all fail closed; an aligned eligible chain is accepted.
func TestRegistrationTrustRootChain(t *testing.T) {
	fixture := newRegistrationFixture(t)
	headers := withContentType(fixture.headers("req-trust"))

	// An unpinned trust root key id fails closed.
	unpinned := fixture.buildRegistration(registrationOptions{
		registrationID: "reg-trust-unpinned",
		trustRootKeyID: "evil-root-key",
	})
	response := fixture.do(http.MethodPost, APIPrefix+"/registrations", headers,
		registrationBody(t, "trust-key-unpinned", registrationPayload(unpinned)))
	if response.status != http.StatusUnprocessableEntity {
		t.Fatalf("unpinned trust root status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Code != CodeRejected || body.Reason != "trust-root-chain-invalid" {
		t.Fatalf("unpinned trust root error = %+v", body)
	}

	// Misaligned evidence (substituted isolation domain) breaks the chain.
	misaligned := fixture.buildRegistration(registrationOptions{registrationID: "reg-trust-misaligned"})
	evidence := fixture.buildEvidence(misaligned, "2026-09-13T00:00:00Z")
	evidence.SecurityDomainId.IsolationDomainId = "substituted-domain"
	evidence.EvidenceDigest = ""
	divergedDigest, err := evidence.Digest()
	if err != nil {
		t.Fatalf("re-digest the misaligned evidence: %v", err)
	}
	evidence.EvidenceDigest = divergedDigest
	response = fixture.do(http.MethodPost, APIPrefix+"/registrations", headers,
		registrationBody(t, "trust-key-misaligned", registrationPayload(misaligned, evidence)))
	if response.status != http.StatusUnprocessableEntity {
		t.Fatalf("misaligned evidence status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Code != CodeRejected || body.Reason != "trust-root-chain-invalid" {
		t.Fatalf("misaligned evidence error = %+v", body)
	}

	// Expired evidence (validUntil before the fixture clock) breaks the chain.
	expiredEvidence := fixture.buildEvidence(misaligned, "2026-08-13T11:00:00Z")
	response = fixture.do(http.MethodPost, APIPrefix+"/registrations", headers,
		registrationBody(t, "trust-key-expired", registrationPayload(misaligned, expiredEvidence)))
	if response.status != http.StatusUnprocessableEntity {
		t.Fatalf("expired evidence status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Code != CodeRejected || body.Reason != "trust-root-chain-invalid" {
		t.Fatalf("expired evidence error = %+v", body)
	}

	// An aligned, eligible evidence chain is accepted.
	aligned := fixture.buildRegistration(registrationOptions{registrationID: "reg-trust-aligned"})
	alignedEvidence := fixture.buildEvidence(aligned, "2026-09-13T00:00:00Z")
	response = fixture.do(http.MethodPost, APIPrefix+"/registrations", headers,
		registrationBody(t, "trust-key-aligned", registrationPayload(aligned, alignedEvidence)))
	if response.status != http.StatusCreated {
		t.Fatalf("aligned chain status = %d, body: %s", response.status, response.body)
	}
}

// TestRegistrationDefaultDenyWithoutTrustRoots proves the fail-closed
// default: a Port with no pinned trust root denies every registration.
func TestRegistrationDefaultDenyWithoutTrustRoots(t *testing.T) {
	fixture := newRegistrationFixtureWithTrustRoots(t, nil)
	registration := fixture.buildRegistration(registrationOptions{registrationID: "reg-default-deny"})
	response := fixture.do(http.MethodPost, APIPrefix+"/registrations",
		withContentType(fixture.headers("req-default-deny")),
		registrationBody(t, "default-deny-key", registrationPayload(registration)))
	if response.status != http.StatusUnprocessableEntity {
		t.Fatalf("default deny status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Code != CodeRejected || body.Reason != "trust-root-chain-invalid" {
		t.Fatalf("default deny error = %+v", body)
	}
}

// TestRegistrationRejectsWorkloadLeaseIdentity enforces the ADR 0018 §3
// matrix: workload lease identity is rejected fail closed on headers, query
// parameters and body members at any depth — while providerType remains a
// required, accepted member of this Port.
func TestRegistrationRejectsWorkloadLeaseIdentity(t *testing.T) {
	fixture := newRegistrationFixture(t)
	registration := fixture.buildRegistration(registrationOptions{registrationID: "reg-forbidden"})
	body := registrationBody(t, "forbidden-key", registrationPayload(registration))

	headers := fixture.headers("req-forbidden-header")
	headers["Marshal-Workload-Role"] = "worker"
	response := fixture.do(http.MethodPost, APIPrefix+"/registrations", withContentType(headers), body)
	if response.status != http.StatusForbidden {
		t.Fatalf("forbidden header status = %d, body: %s", response.status, response.body)
	}
	if errBody := response.decodeError(t); errBody.Code != CodeForbiddenIdentity || errBody.Reason != "forbidden-header:Marshal-Workload-Role" {
		t.Fatalf("forbidden header error = %+v", errBody)
	}

	response = fixture.do(http.MethodGet, APIPrefix+"/registrations/reg-forbidden?fencingToken=1", fixture.headers("req-forbidden-query"), nil)
	if response.status != http.StatusForbidden {
		t.Fatalf("forbidden query status = %d, body: %s", response.status, response.body)
	}
	if errBody := response.decodeError(t); errBody.Code != CodeForbiddenIdentity || errBody.Reason != "forbidden-query:fencingToken" {
		t.Fatalf("forbidden query error = %+v", errBody)
	}

	for _, field := range []string{"workloadRole", "allocationId", "generation", "fencingToken", "dispatchLease", "leaseId"} {
		topLevel := map[string]any{
			"idempotencyKey": "forbidden-top-" + field,
			"requestDigest":  "sha256:" + strings.Repeat("e", 64),
			"payload":        registrationPayload(registration),
			field:            "1",
		}
		response = fixture.do(http.MethodPost, APIPrefix+"/registrations",
			withContentType(fixture.headers("req-forbidden-top")), mustMarshal(t, topLevel))
		if response.status != http.StatusForbidden {
			t.Fatalf("top-level %s status = %d, body: %s", field, response.status, response.body)
		}
		nested := map[string]any{
			"idempotencyKey": "forbidden-nested-" + field,
			"requestDigest":  "sha256:" + strings.Repeat("e", 64),
			"payload":        map[string]any{"registration": map[string]any{"providerName": "fixture", field: "1"}},
		}
		response = fixture.do(http.MethodPost, APIPrefix+"/registrations",
			withContentType(fixture.headers("req-forbidden-nested")), mustMarshal(t, nested))
		if response.status != http.StatusForbidden {
			t.Fatalf("nested %s status = %d, body: %s", field, response.status, response.body)
		}
	}
}

// TestRegistrationOwnershipAndPrincipalChecks proves the authority boundary:
// a foreign authorityNamespaceId owner, a divergent scope, a mismatched
// principal and a mismatched transport client identity all fail closed.
func TestRegistrationOwnershipAndPrincipalChecks(t *testing.T) {
	fixture := newRegistrationFixture(t)

	foreignNamespace := fixture.namespace
	foreignNamespace.AuthorityScopeId = "repo:/foreign-repository"
	foreign := fixture.buildRegistration(registrationOptions{
		registrationID: "reg-foreign-owner",
		namespace:      &foreignNamespace,
		scope:          "repo:/foreign-repository",
	})
	response := fixture.do(http.MethodPost, APIPrefix+"/registrations",
		withContentType(fixture.headers("req-foreign")), registrationBody(t, "foreign-key", registrationPayload(foreign)))
	if response.status != http.StatusForbidden {
		t.Fatalf("foreign owner status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Code != CodeForbiddenIdentity || body.Reason != "authority-namespace-mismatch" {
		t.Fatalf("foreign owner error = %+v", body)
	}

	divergentScope := fixture.buildRegistration(registrationOptions{
		registrationID: "reg-divergent-scope",
		scope:          "repo:/divergent-scope",
	})
	response = fixture.do(http.MethodPost, APIPrefix+"/registrations",
		withContentType(fixture.headers("req-scope")), registrationBody(t, "scope-key", registrationPayload(divergentScope)))
	if response.status != http.StatusBadRequest {
		t.Fatalf("divergent scope status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Code != CodeScopeMismatch || body.Reason != "scope-mismatch" {
		t.Fatalf("divergent scope error = %+v", body)
	}

	mismatchedPrincipal := fixture.buildRegistration(registrationOptions{
		registrationID: "reg-principal-mismatch",
		principal:      "someone-else",
	})
	response = fixture.do(http.MethodPost, APIPrefix+"/registrations",
		withContentType(fixture.headers("req-principal")), registrationBody(t, "principal-key", registrationPayload(mismatchedPrincipal)))
	if response.status != http.StatusForbidden {
		t.Fatalf("principal mismatch status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Code != CodeForbiddenIdentity || body.Reason != "principal-mismatch" {
		t.Fatalf("principal mismatch error = %+v", body)
	}

	// Mutual identity validation: a verified transport client identity that
	// does not match the registration principal fails closed, while the
	// matching identity binds and passes.
	valid := fixture.buildRegistration(registrationOptions{registrationID: "reg-tls-binding"})
	body := registrationBody(t, "tls-binding-key", registrationPayload(valid))
	mismatchedCtx := context.WithValue(context.Background(), clientIdentityContextKey, "other-client")
	response = fixture.doWithContext(mismatchedCtx, http.MethodPost, APIPrefix+"/registrations", withContentType(fixture.headers("req-tls-mismatch")), body)
	if response.status != http.StatusForbidden {
		t.Fatalf("tls principal mismatch status = %d, body: %s", response.status, response.body)
	}
	if errBody := response.decodeError(t); errBody.Code != CodeForbiddenIdentity || errBody.Reason != "tls-principal-mismatch" {
		t.Fatalf("tls principal mismatch error = %+v", errBody)
	}
	matchingCtx := context.WithValue(context.Background(), clientIdentityContextKey, fixturePrincipal)
	response = fixture.doWithContext(matchingCtx, http.MethodPost, APIPrefix+"/registrations", withContentType(fixture.headers("req-tls-match")), body)
	if response.status != http.StatusCreated {
		t.Fatalf("tls principal match status = %d, body: %s", response.status, response.body)
	}
}

// TestRegistrationEnvelopeDigestDiscipline proves the reused T1 Candidate
// digest discipline: a mismatched envelope requestDigest and a tampered
// registrationDigest both fail closed.
func TestRegistrationEnvelopeDigestDiscipline(t *testing.T) {
	fixture := newRegistrationFixture(t)
	registration := fixture.buildRegistration(registrationOptions{registrationID: "reg-digest-discipline"})

	wrongDigest := mutationBodyWithDigest(t, "discipline-key", "sha256:"+strings.Repeat("e", 64),
		registrationPayload(registration))
	response := fixture.do(http.MethodPost, APIPrefix+"/registrations",
		withContentType(fixture.headers("req-digest-mismatch")), wrongDigest)
	if response.status != http.StatusBadRequest {
		t.Fatalf("digest mismatch status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Code != CodeInvalidRequest || body.Reason != "request-digest-mismatch" {
		t.Fatalf("digest mismatch error = %+v", body)
	}

	tampered := registration
	tampered.RegistrationDigest = "sha256:" + strings.Repeat("f", 64)
	response = fixture.do(http.MethodPost, APIPrefix+"/registrations",
		withContentType(fixture.headers("req-tampered")),
		registrationBody(t, "discipline-key-tampered", registrationPayload(tampered)))
	if response.status != http.StatusBadRequest {
		t.Fatalf("tampered record status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Code != CodeInvalidRequest || body.Reason != "registration-invalid" {
		t.Fatalf("tampered record error = %+v", body)
	}
}

// TestRegistrationStatusQueryAndMethodRules covers the status endpoint's
// error surface and the frozen route/method rules of the Port.
func TestRegistrationStatusQueryAndMethodRules(t *testing.T) {
	fixture := newRegistrationFixture(t)

	response := fixture.do(http.MethodGet, APIPrefix+"/registrations/reg-absent", fixture.headers("req-absent"), nil)
	if response.status != http.StatusNotFound {
		t.Fatalf("absent status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Code != CodeNotFound || body.Reason != "registration-not-found" {
		t.Fatalf("absent error = %+v", body)
	}

	response = fixture.do(http.MethodGet, APIPrefix+"/registrations/reg-absent", fixture.headers("req-get-body"), []byte("{}"))
	if response.status != http.StatusBadRequest {
		t.Fatalf("GET with body status = %d, body: %s", response.status, response.body)
	}

	response = fixture.do(http.MethodPost, APIPrefix+"/registrations/reg-absent",
		withContentType(fixture.headers("req-post-one")), []byte("{}"))
	if response.status != http.StatusMethodNotAllowed {
		t.Fatalf("POST on one registration status = %d, body: %s", response.status, response.body)
	}
	response = fixture.do(http.MethodGet, APIPrefix+"/registrations", fixture.headers("req-get-collection"), nil)
	if response.status != http.StatusMethodNotAllowed {
		t.Fatalf("GET on the collection status = %d, body: %s", response.status, response.body)
	}
	response = fixture.do(http.MethodGet, APIPrefix+"/registrations/reg/x", fixture.headers("req-deep"), nil)
	if response.status != http.StatusNotFound {
		t.Fatalf("deep route status = %d, body: %s", response.status, response.body)
	}

	identityHeaders := fixture.headers("req-envelope")
	delete(identityHeaders, HeaderRequestID)
	response = fixture.do(http.MethodGet, APIPrefix+"/registrations/reg-absent", identityHeaders, nil)
	if response.status != http.StatusBadRequest {
		t.Fatalf("missing header status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Code != CodeMissingIdentity || body.Reason != "missing-header:Marshal-Request-Id" {
		t.Fatalf("missing header error = %+v", body)
	}

	wrongFamily := fixture.headers("req-family")
	wrongFamily[HeaderProtocolVersion] = ProtocolFamily + "/" + ProtocolVersion
	response = fixture.do(http.MethodGet, APIPrefix+"/registrations/reg-absent", wrongFamily, nil)
	if response.status != http.StatusBadRequest {
		t.Fatalf("wrong protocol family status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Code != CodeInvalidRequest || body.Reason != "protocol-version-mismatch" {
		t.Fatalf("wrong protocol family error = %+v", body)
	}

	wrongAudience := fixture.headers("req-audience")
	wrongAudience[HeaderAudience] = Audience
	response = fixture.do(http.MethodGet, APIPrefix+"/registrations/reg-absent", wrongAudience, nil)
	if response.status != http.StatusBadRequest {
		t.Fatalf("wrong audience status = %d, body: %s", response.status, response.body)
	}

	expired := fixture.headers("req-deadline")
	expired[HeaderDeadline] = "2026-08-13T11:00:00Z"
	response = fixture.do(http.MethodGet, APIPrefix+"/registrations/reg-absent", expired, nil)
	if response.status != http.StatusBadRequest {
		t.Fatalf("expired deadline status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Code != CodeInvalidRequest || body.Reason != "deadline-exceeded" {
		t.Fatalf("expired deadline error = %+v", body)
	}
}

// TestCombinedHandlerRoutesBothPorts proves the combined handler mounts the
// registration Port beside the Public API without touching the public-api
// endpoints: each family authenticates its own routes with its own protocol
// version, and foreign family requests are rejected by the correct Port.
func TestCombinedHandlerRoutesBothPorts(t *testing.T) {
	fixture := newServerFixture(t)
	port, err := NewRegistrationPort(RegistrationPortConfig{
		StateRoot:       fixture.stateRoot,
		RepositoryRoot:  fixture.repositoryRoot,
		Now:             func() time.Time { return fixtureClock },
		TrustRootKeyIds: []string{registrationTrustRoot},
	})
	if err != nil {
		t.Fatalf("assemble the registration port: %v", err)
	}
	combined := CombineHandlers(fixture.server.Handler(), port)
	do := func(method, path string, headers map[string]string, body []byte) recordedResponse {
		t.Helper()
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		request := httptest.NewRequest(method, path, reader)
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		recorder := httptest.NewRecorder()
		combined.ServeHTTP(recorder, request)
		return recordedResponse{status: recorder.Code, header: recorder.Result().Header, body: recorder.Body.Bytes()}
	}

	// A public-api route still reaches the public-api Port: the registration
	// protocol version is rejected by the public-api authenticator.
	publicHeaders := fixture.identityHeaders("req-combined-public")
	publicHeaders[HeaderProtocolVersion] = RegistrationProtocolFamily + "/" + RegistrationProtocolVersion
	response := do(http.MethodGet, APIPrefix+"/tasks/"+fixtureTaskID, publicHeaders, nil)
	if response.status != http.StatusBadRequest {
		t.Fatalf("public route with registration version status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Reason != "protocol-version-mismatch" {
		t.Fatalf("public route error = %+v", body)
	}

	// A registration route reaches the registration Port: the public-api
	// protocol version is rejected by the registration authenticator.
	registrationHeaders := map[string]string{
		HeaderRequestID:       "req-combined-registration",
		HeaderProtocolVersion: ProtocolFamily + "/" + ProtocolVersion,
		HeaderPrincipal:       fixturePrincipal,
		HeaderAudience:        RegistrationAudience,
		HeaderScope:           fixture.scope,
		HeaderDeadline:        fixtureDeadline,
	}
	response = do(http.MethodGet, APIPrefix+"/registrations/reg-x", registrationHeaders, nil)
	if response.status != http.StatusBadRequest {
		t.Fatalf("registration route with public version status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Reason != "protocol-version-mismatch" {
		t.Fatalf("registration route error = %+v", body)
	}

	// A complete registration submission succeeds through the combined
	// handler.
	namespace := authority.AuthorityNamespaceId{
		TenantNamespace:  localTenantNamespace,
		ControlPlaneId:   localControlPlaneID,
		AuthorityScopeId: fixture.scope,
	}
	registration := provider.ProviderRegistration{
		RegistrationId:       "reg-combined",
		AuthorityNamespaceId: namespace,
		SecurityDomainId: authority.SecurityDomainId{
			TenantNamespace:   "local",
			TrustDomainKind:   authority.TrustDomainKindExecution,
			IsolationDomainId: "default",
		},
		Principal:       fixturePrincipal,
		ProviderType:    "sandbox",
		ProviderName:    "fixture-sandbox",
		ProviderVersion: "1.0.0",
		ProtocolVersion: "fixture-sandbox/v1",
		Scope:           fixture.scope,
		IdempotencyKey:  "record-key-combined",
		RequestDigest:   "sha256:" + strings.Repeat("c", 64),
		Attestation: provider.Attestation{
			ProviderInstanceId: "fixture-instance-combined",
			ConfigDigest:       "sha256:" + strings.Repeat("c", 64),
			TrustRootKeyId:     registrationTrustRoot,
			TrustRootAlgorithm: "ecdsa-p256",
		},
		LifecycleState: provider.LifecycleStateCreate,
		CreatedAt:      "2026-08-13T11:30:00Z",
	}
	digest, err := registration.Digest()
	if err != nil {
		t.Fatalf("digest the combined registration: %v", err)
	}
	registration.RegistrationDigest = digest
	submitHeaders := registrationHeaders
	submitHeaders[HeaderRequestID] = "req-combined-submit"
	response = do(http.MethodPost, APIPrefix+"/registrations", withContentType(submitHeaders),
		registrationBody(t, "combined-key", registrationPayload(registration)))
	if response.status != http.StatusCreated {
		t.Fatalf("combined submit status = %d, body: %s", response.status, response.body)
	}
}

// TestLoopbackRegistrationOverPlainHTTP proves the loopback path is
// unaffected by the remote baseline: the combined handler served on a plain
// loopback listener accepts registration submissions and status queries
// without any TLS/nonce/signature headers, and the public-api endpoints keep
// answering with their frozen error model on the same listener.
func TestLoopbackRegistrationOverPlainHTTP(t *testing.T) {
	fixture := newServerFixture(t)
	port, err := NewRegistrationPort(RegistrationPortConfig{
		StateRoot:       fixture.stateRoot,
		RepositoryRoot:  fixture.repositoryRoot,
		Now:             func() time.Time { return fixtureClock },
		TrustRootKeyIds: []string{registrationTrustRoot},
	})
	if err != nil {
		t.Fatalf("assemble the registration port: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind loopback listener: %v", err)
	}
	httpServer := &http.Server{Handler: CombineHandlers(fixture.server.Handler(), port)}
	go func() { _ = httpServer.Serve(listener) }()
	defer func() { _ = httpServer.Close() }()
	baseURL := "http://" + listener.Addr().String()
	client := &http.Client{Timeout: 30 * time.Second}

	doHTTP := func(method, path string, headers map[string]string, body []byte) recordedResponse {
		t.Helper()
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		request, err := http.NewRequest(method, baseURL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("read response body: %v", err)
		}
		return recordedResponse{status: response.StatusCode, header: response.Header, body: data}
	}

	namespace := authority.AuthorityNamespaceId{
		TenantNamespace:  localTenantNamespace,
		ControlPlaneId:   localControlPlaneID,
		AuthorityScopeId: fixture.scope,
	}
	registration := provider.ProviderRegistration{
		RegistrationId:       "reg-loopback",
		AuthorityNamespaceId: namespace,
		SecurityDomainId: authority.SecurityDomainId{
			TenantNamespace:   "local",
			TrustDomainKind:   authority.TrustDomainKindExecution,
			IsolationDomainId: "default",
		},
		Principal:       fixturePrincipal,
		ProviderType:    "sandbox",
		ProviderName:    "fixture-sandbox",
		ProviderVersion: "1.0.0",
		ProtocolVersion: "fixture-sandbox/v1",
		Scope:           fixture.scope,
		IdempotencyKey:  "record-key-loopback",
		RequestDigest:   "sha256:" + strings.Repeat("c", 64),
		Attestation: provider.Attestation{
			ProviderInstanceId: "fixture-instance-loopback",
			ConfigDigest:       "sha256:" + strings.Repeat("c", 64),
			TrustRootKeyId:     registrationTrustRoot,
			TrustRootAlgorithm: "ecdsa-p256",
		},
		LifecycleState: provider.LifecycleStateCreate,
		CreatedAt:      "2026-08-13T11:30:00Z",
	}
	digest, err := registration.Digest()
	if err != nil {
		t.Fatalf("digest the loopback registration: %v", err)
	}
	registration.RegistrationDigest = digest
	registrationHeaders := map[string]string{
		HeaderRequestID:       "req-loopback-submit",
		HeaderProtocolVersion: RegistrationProtocolFamily + "/" + RegistrationProtocolVersion,
		HeaderPrincipal:       fixturePrincipal,
		HeaderAudience:        RegistrationAudience,
		HeaderScope:           fixture.scope,
		HeaderDeadline:        fixtureDeadline,
	}
	response := doHTTP(http.MethodPost, APIPrefix+"/registrations", withContentType(registrationHeaders),
		registrationBody(t, "loopback-key", registrationPayload(registration)))
	if response.status != http.StatusCreated {
		t.Fatalf("loopback submit status = %d, body: %s", response.status, response.body)
	}

	statusHeaders := registrationHeaders
	statusHeaders[HeaderRequestID] = "req-loopback-status"
	response = doHTTP(http.MethodGet, APIPrefix+"/registrations/reg-loopback", statusHeaders, nil)
	if response.status != http.StatusOK {
		t.Fatalf("loopback status query status = %d, body: %s", response.status, response.body)
	}

	// The public-api surface keeps its frozen behavior on the same plain
	// loopback listener.
	response = doHTTP(http.MethodGet, APIPrefix+"/runs/run-missing/status", fixture.identityHeaders("req-loopback-public"), nil)
	if response.status != http.StatusNotFound {
		t.Fatalf("public run status status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Code != CodeNotFound || body.Reason != "run-not-found" {
		t.Fatalf("public run status error = %+v", body)
	}
}

// TestRegistrationProtocolFamilyAliasesAccepted proves every frozen spelling
// of the provider-registration/control protocol family is admitted by the
// version and audience checks, while foreign families — including the
// public-api family — still fail closed. Every provider-registration version
// a legitimate fixture actually submits must be part of the allow-set and
// must never be rejected with protocol-version-mismatch.
func TestRegistrationProtocolFamilyAliasesAccepted(t *testing.T) {
	fixture := newRegistrationFixture(t)

	versions := []string{
		RegistrationProtocolFamily + "/" + RegistrationProtocolVersion,
		"provider-registration/" + RegistrationProtocolVersion,
		"provider-registration/control/" + RegistrationProtocolVersion,
		"marshal-provider-registration/" + RegistrationProtocolVersion,
		"provider-registration-control/" + RegistrationProtocolVersion,
		"marshal-registration-control/" + RegistrationProtocolVersion,
		"registration/" + RegistrationProtocolVersion,
	}
	if len(versions) != len(registrationProtocolVersions) {
		t.Fatalf("the fixture must cover every frozen version spelling: %d frozen, %d covered",
			len(registrationProtocolVersions), len(versions))
	}
	for _, version := range versions {
		headers := fixture.headers("req-version-alias")
		headers[HeaderProtocolVersion] = version
		response := fixture.do(http.MethodGet, APIPrefix+"/registrations/reg-absent", headers, nil)
		if response.status != http.StatusNotFound {
			t.Fatalf("version %q status = %d, body: %s; a legitimate provider-registration version must be admitted",
				version, response.status, response.body)
		}
		if body := response.decodeError(t); body.Code != CodeNotFound || body.Reason != "registration-not-found" {
			t.Fatalf("version %q error = %+v", version, body)
		}
	}
	for _, version := range []string{
		ProtocolFamily + "/" + ProtocolVersion, // the public-api family never enters this Port
		"provider-registration/v9",
		"marshal-registration/v1beta1",
		"unknown-family/" + RegistrationProtocolVersion,
	} {
		headers := fixture.headers("req-version-foreign")
		headers[HeaderProtocolVersion] = version
		response := fixture.do(http.MethodGet, APIPrefix+"/registrations/reg-absent", headers, nil)
		if response.status != http.StatusBadRequest {
			t.Fatalf("foreign version %q status = %d, body: %s", version, response.status, response.body)
		}
		if body := response.decodeError(t); body.Code != CodeInvalidRequest || body.Reason != "protocol-version-mismatch" {
			t.Fatalf("foreign version %q error = %+v", version, body)
		}
	}

	audiences := []string{
		RegistrationAudience,
		"provider-registration",
		"provider-registration/control",
		"marshal-provider-registration",
		"provider-registration-control",
		"marshal-registration-control",
		"registration",
	}
	if len(audiences) != len(registrationAudiences) {
		t.Fatalf("the fixture must cover every frozen audience spelling: %d frozen, %d covered",
			len(registrationAudiences), len(audiences))
	}
	for _, audience := range audiences {
		headers := fixture.headers("req-audience-alias")
		headers[HeaderAudience] = audience
		response := fixture.do(http.MethodGet, APIPrefix+"/registrations/reg-absent", headers, nil)
		if response.status != http.StatusNotFound {
			t.Fatalf("audience %q status = %d, body: %s; a legitimate provider-registration audience must be admitted",
				audience, response.status, response.body)
		}
	}
	for _, audience := range []string{Audience, "unknown-audience"} {
		headers := fixture.headers("req-audience-foreign")
		headers[HeaderAudience] = audience
		response := fixture.do(http.MethodGet, APIPrefix+"/registrations/reg-absent", headers, nil)
		if response.status != http.StatusBadRequest {
			t.Fatalf("foreign audience %q status = %d, body: %s", audience, response.status, response.body)
		}
		if body := response.decodeError(t); body.Code != CodeInvalidRequest || body.Reason != "audience-mismatch" {
			t.Fatalf("foreign audience %q error = %+v", audience, body)
		}
	}
}

// TestRegistrationSubmitAcceptsEveryFamilyVersionSpelling proves the
// allow-set admission on the actual submission path: a registration
// submitted under any frozen provider-registration protocol version
// spelling is accepted, never rejected with protocol-version-mismatch.
// This is the discipline the version allow-set owes to every fixture that
// actually submits a provider-registration version.
func TestRegistrationSubmitAcceptsEveryFamilyVersionSpelling(t *testing.T) {
	fixture := newRegistrationFixture(t)
	for _, version := range []string{
		RegistrationProtocolFamily + "/" + RegistrationProtocolVersion,
		"provider-registration/" + RegistrationProtocolVersion,
		"provider-registration/control/" + RegistrationProtocolVersion,
		"marshal-provider-registration/" + RegistrationProtocolVersion,
		"provider-registration-control/" + RegistrationProtocolVersion,
		"marshal-registration-control/" + RegistrationProtocolVersion,
		"registration/" + RegistrationProtocolVersion,
	} {
		registrationID := "reg-version-" + strings.NewReplacer("/", "-", ".", "-").Replace(version)
		registration := fixture.buildRegistration(registrationOptions{registrationID: registrationID})
		headers := fixture.headers("req-submit-version-" + registrationID)
		headers[HeaderProtocolVersion] = version
		response := fixture.do(http.MethodPost, APIPrefix+"/registrations",
			withContentType(headers), registrationBody(t, "version-key-"+registrationID, registrationPayload(registration)))
		if response.status != http.StatusCreated {
			t.Fatalf("submit under version %q status = %d, body: %s; a legitimate provider-registration version must be admitted", version, response.status, response.body)
		}
		var accepted RegistrationAccepted
		if err := json.Unmarshal(response.body, &accepted); err != nil {
			t.Fatalf("version %q: decode RegistrationAccepted: %v", version, err)
		}
		if accepted.RegistrationId != registrationID {
			t.Fatalf("version %q: accepted registration %+v", version, accepted)
		}
	}
}

// TestRegistrationPortAssemblyFailsClosed proves the Port never assembles
// over a missing state root or repository root, a blank trust root key id,
// a mismatched repository identity or an unbound repository.
func TestRegistrationPortAssemblyFailsClosed(t *testing.T) {
	root := fixtureRepository(t)
	stateRoot := filepath.Join(root, ".marshal")
	if err := (repository.State{RepositoryRoot: root, StateRoot: stateRoot}).Init(); err != nil {
		t.Fatalf("bind the fixture repository identity: %v", err)
	}

	if _, err := NewRegistrationPort(RegistrationPortConfig{RepositoryRoot: root, TrustRootKeyIds: []string{registrationTrustRoot}}); err == nil {
		t.Fatal("a registration port assembled without a state root")
	}
	if _, err := NewRegistrationPort(RegistrationPortConfig{StateRoot: stateRoot, TrustRootKeyIds: []string{registrationTrustRoot}}); err == nil {
		t.Fatal("a registration port assembled without a repository root")
	}
	if _, err := NewRegistrationPort(RegistrationPortConfig{StateRoot: stateRoot, RepositoryRoot: root, TrustRootKeyIds: []string{"ok", " "}}); err == nil {
		t.Fatal("a registration port assembled with a blank trust root key id")
	}
	otherRoot := fixtureRepository(t)
	if _, err := NewRegistrationPort(RegistrationPortConfig{StateRoot: stateRoot, RepositoryRoot: otherRoot, TrustRootKeyIds: []string{registrationTrustRoot}}); err == nil {
		t.Fatal("a registration port assembled over a mismatched repository identity")
	}
	if _, err := NewRegistrationPort(RegistrationPortConfig{StateRoot: filepath.Join(t.TempDir(), "state"), RepositoryRoot: t.TempDir(), TrustRootKeyIds: []string{registrationTrustRoot}}); err == nil {
		t.Fatal("a registration port assembled outside a bound repository")
	}
}

// TestRegistrationSubmitBodyPolicing covers the submit body gate: a wrong or
// missing content type, an empty body and an oversized body all fail closed
// before any envelope is inspected.
func TestRegistrationSubmitBodyPolicing(t *testing.T) {
	fixture := newRegistrationFixture(t)
	registration := fixture.buildRegistration(registrationOptions{registrationID: "reg-body-policing"})
	body := registrationBody(t, "body-policing-key", registrationPayload(registration))

	headers := fixture.headers("req-body-ct")
	headers["Content-Type"] = "text/plain"
	response := fixture.do(http.MethodPost, APIPrefix+"/registrations", headers, body)
	if response.status != http.StatusBadRequest {
		t.Fatalf("wrong content type status = %d, body: %s", response.status, response.body)
	}
	if errBody := response.decodeError(t); errBody.Code != CodeInvalidRequest || errBody.Reason != "content-type-invalid" {
		t.Fatalf("wrong content type error = %+v", errBody)
	}

	response = fixture.do(http.MethodPost, APIPrefix+"/registrations", fixture.headers("req-body-noct"), body)
	if response.status != http.StatusBadRequest {
		t.Fatalf("missing content type status = %d, body: %s", response.status, response.body)
	}
	if errBody := response.decodeError(t); errBody.Code != CodeInvalidRequest || errBody.Reason != "content-type-invalid" {
		t.Fatalf("missing content type error = %+v", errBody)
	}

	response = fixture.do(http.MethodPost, APIPrefix+"/registrations", withContentType(fixture.headers("req-body-empty")), nil)
	if response.status != http.StatusBadRequest {
		t.Fatalf("empty body status = %d, body: %s", response.status, response.body)
	}
	if errBody := response.decodeError(t); errBody.Code != CodeInvalidRequest || errBody.Reason != "empty-body" {
		t.Fatalf("empty body error = %+v", errBody)
	}

	oversized := make([]byte, maxRequestBodyBytes+1)
	for index := range oversized {
		oversized[index] = 'a'
	}
	response = fixture.do(http.MethodPost, APIPrefix+"/registrations", withContentType(fixture.headers("req-body-large")), oversized)
	if response.status != http.StatusBadRequest {
		t.Fatalf("oversized body status = %d, body: %s", response.status, response.body)
	}
	if errBody := response.decodeError(t); errBody.Code != CodeInvalidRequest || errBody.Reason != "body-too-large" {
		t.Fatalf("oversized body error = %+v", errBody)
	}
	if lines := fixture.ledgerLines(); lines != 0 {
		t.Fatalf("rejected bodies appended ledger facts: %d", lines)
	}
}

// TestRegistrationEvidencesMemberPolicing proves the evidences member is
// policed as a JSON array of fail-closed ConformanceEvidence documents.
func TestRegistrationEvidencesMemberPolicing(t *testing.T) {
	fixture := newRegistrationFixture(t)
	registration := fixture.buildRegistration(registrationOptions{registrationID: "reg-evidence-policing"})

	notArray := registrationPayload(registration)
	notArray["evidences"] = map[string]any{"not": "an array"}
	response := fixture.do(http.MethodPost, APIPrefix+"/registrations",
		withContentType(fixture.headers("req-evidence-shape")), registrationBody(t, "evidence-shape-key", notArray))
	if response.status != http.StatusBadRequest {
		t.Fatalf("non-array evidences status = %d, body: %s", response.status, response.body)
	}
	if errBody := response.decodeError(t); errBody.Code != CodeInvalidRequest || errBody.Reason != "invalid-member:evidences" {
		t.Fatalf("non-array evidences error = %+v", errBody)
	}

	invalidDocument := registrationPayload(registration)
	invalidDocument["evidences"] = []map[string]any{{"evidenceDigest": "not-a-digest"}}
	response = fixture.do(http.MethodPost, APIPrefix+"/registrations",
		withContentType(fixture.headers("req-evidence-doc")), registrationBody(t, "evidence-doc-key", invalidDocument))
	if response.status != http.StatusBadRequest {
		t.Fatalf("invalid evidence document status = %d, body: %s", response.status, response.body)
	}
	if errBody := response.decodeError(t); errBody.Code != CodeInvalidRequest || errBody.Reason != "evidences[0]-invalid" {
		t.Fatalf("invalid evidence document error = %+v", errBody)
	}
	if lines := fixture.ledgerLines(); lines != 0 {
		t.Fatalf("rejected evidences appended ledger facts: %d", lines)
	}
}

// TestRegistrationIdentityEnvelopeRejections covers the remaining frozen
// identity envelope rejections of the status endpoint: a divergent scope, a
// missing principal, an invalid deadline and an oversized header all fail
// closed.
func TestRegistrationIdentityEnvelopeRejections(t *testing.T) {
	fixture := newRegistrationFixture(t)

	wrongScope := fixture.headers("req-env-scope")
	wrongScope[HeaderScope] = "repo:/elsewhere"
	response := fixture.do(http.MethodGet, APIPrefix+"/registrations/reg-absent", wrongScope, nil)
	if response.status != http.StatusBadRequest {
		t.Fatalf("wrong scope status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Code != CodeScopeMismatch || body.Reason != "scope-mismatch" {
		t.Fatalf("wrong scope error = %+v", body)
	}

	missingPrincipal := fixture.headers("req-env-principal")
	delete(missingPrincipal, HeaderPrincipal)
	response = fixture.do(http.MethodGet, APIPrefix+"/registrations/reg-absent", missingPrincipal, nil)
	if response.status != http.StatusBadRequest {
		t.Fatalf("missing principal status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Code != CodeMissingIdentity || body.Reason != "missing-header:Marshal-Principal" {
		t.Fatalf("missing principal error = %+v", body)
	}

	invalidDeadline := fixture.headers("req-env-deadline")
	invalidDeadline[HeaderDeadline] = "not-a-timestamp"
	response = fixture.do(http.MethodGet, APIPrefix+"/registrations/reg-absent", invalidDeadline, nil)
	if response.status != http.StatusBadRequest {
		t.Fatalf("invalid deadline status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Code != CodeInvalidRequest || body.Reason != "deadline-invalid" {
		t.Fatalf("invalid deadline error = %+v", body)
	}

	longPrincipal := fixture.headers("req-env-long")
	longPrincipal[HeaderPrincipal] = strings.Repeat("p", maxHeaderFieldBytes+1)
	response = fixture.do(http.MethodGet, APIPrefix+"/registrations/reg-absent", longPrincipal, nil)
	if response.status != http.StatusBadRequest {
		t.Fatalf("oversized principal status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Code != CodeInvalidRequest || body.Reason != "header-too-long:Marshal-Principal" {
		t.Fatalf("oversized principal error = %+v", body)
	}
}

// TestRegistrationLedgerRecoversAcrossReopen proves the durable ledger and
// the durable idempotency records survive a port restart: a second Port
// assembled over the identical state root projects the accepted record,
// merges the identical replay with the stored result and never appends a
// second ledger fact.
func TestRegistrationLedgerRecoversAcrossReopen(t *testing.T) {
	fixture := newRegistrationFixture(t)
	registration := fixture.buildRegistration(registrationOptions{registrationID: "reg-reopen"})
	body := registrationBody(t, "reopen-key", registrationPayload(registration))

	response := fixture.do(http.MethodPost, APIPrefix+"/registrations",
		withContentType(fixture.headers("req-reopen-submit")), body)
	if response.status != http.StatusCreated {
		t.Fatalf("submit status = %d, body: %s", response.status, response.body)
	}
	var accepted RegistrationAccepted
	if err := json.Unmarshal(response.body, &accepted); err != nil {
		t.Fatalf("decode RegistrationAccepted: %v", err)
	}

	reopened, err := NewRegistrationPort(RegistrationPortConfig{
		StateRoot:       fixture.stateRoot,
		RepositoryRoot:  fixture.repositoryRoot,
		Now:             func() time.Time { return fixtureClock },
		TrustRootKeyIds: []string{registrationTrustRoot},
	})
	if err != nil {
		t.Fatalf("reassemble the registration port over the existing state root: %v", err)
	}
	doReopened := func(method, path string, headers map[string]string, requestBody []byte) recordedResponse {
		t.Helper()
		var reader io.Reader
		if requestBody != nil {
			reader = bytes.NewReader(requestBody)
		}
		request := httptest.NewRequest(method, path, reader)
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		recorder := httptest.NewRecorder()
		reopened.ServeHTTP(recorder, request)
		return recordedResponse{status: recorder.Code, header: recorder.Result().Header, body: recorder.Body.Bytes()}
	}

	status := doReopened(http.MethodGet, APIPrefix+"/registrations/reg-reopen", fixture.headers("req-reopen-status"), nil)
	if status.status != http.StatusOK {
		t.Fatalf("recovered status query status = %d, body: %s", status.status, status.body)
	}
	var view RegistrationStatus
	if err := json.Unmarshal(status.body, &view); err != nil {
		t.Fatalf("decode RegistrationStatus: %v", err)
	}
	if !reflect.DeepEqual(view.Registration, registration) {
		t.Fatalf("the reopened port projected a divergent record:\n got %+v\nwant %+v", view.Registration, registration)
	}

	replay := doReopened(http.MethodPost, APIPrefix+"/registrations", withContentType(fixture.headers("req-reopen-replay")), body)
	if replay.status != http.StatusOK {
		t.Fatalf("reopened replay status = %d, body: %s", replay.status, replay.body)
	}
	var replayAccepted RegistrationAccepted
	if err := json.Unmarshal(replay.body, &replayAccepted); err != nil {
		t.Fatalf("decode replay RegistrationAccepted: %v", err)
	}
	if !reflect.DeepEqual(replayAccepted, accepted) {
		t.Fatalf("the reopened replay diverged from the stored result:\n got %+v\nwant %+v", replayAccepted, accepted)
	}
	if lines := fixture.ledgerLines(); lines != 1 {
		t.Fatalf("the reopen replay appended a ledger fact: %d facts", lines)
	}
}
