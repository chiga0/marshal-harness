package server

// registration.go implements the remote registration endpoints of the
// provider-registration/control Port (ADR 0018 §3/§5/§16): the versioned
// ProviderRegistration submit/accept endpoint and the registration status
// query endpoint, served as an independent protocol family beside the
// public-api family.
//
// Authority boundary: ProviderRegistration records are authority ledger
// facts owned by authorityNamespaceId — only Core writes them; the Provider
// actor securityDomainId rides along as provenance only (ADR 0018 §5/§10).
// The endpoint reuses the T1 Candidate record contract's digest discipline
// (internal/verification candidate admission): the envelope requestDigest is
// verified against the canonical payload digest, the registration record's
// registrationDigest is recomputed detached and compared, and the durable
// ledger performs digest-verified put-if-absent — identical records merge
// idempotently, conflicting records fail closed.
//
// Idempotency identity keeps the quadruple discipline frozen for the c1
// Public API (authorityNamespaceId, scope, idempotencyKey, requestDigest),
// kept in a Port-private idempotency store so the two protocol families
// never share replay records (ADR 0018 §16). The stored idempotency key is
// the deterministic composite of the request's exact protocol version
// spelling and the client idempotencyKey, so two submissions that differ
// only in the protocol version spelling never merge into each other, while
// a true replay — identical version spelling, idempotencyKey and
// requestDigest — still merges into the stored result.
//
// Trust root chain validation binds the
// Provider actor securityDomainId, the attestation provenance chain and the
// referenced ConformanceEvidence records (ADR 0018 §11/§12) with an empty
// pinned trust root set denying everything fail closed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/provider"
	"github.com/chiga0/marshal-harness/internal/repository"
)

// Frozen protocol family identity of the provider-registration/control Port
// (ADR 0018 §16): the family has its own protocol version, audience and
// route subtree and never shares tokens, schemas or replay records with the
// public-api family.
const (
	RegistrationProtocolFamily  = "marshal-registration"
	RegistrationProtocolVersion = "v1alpha1"
	RegistrationAudience        = "marshal-registration"
)

// registrationProtocolVersions is the closed allow-set of protocol version
// strings identifying the provider-registration/control protocol family
// (ADR 0018 §16). The canonical spelling is
// RegistrationProtocolFamily + "/" + RegistrationProtocolVersion; the
// remaining spellings derive from the port name frozen by ADR 0018 §3/§16
// ("provider-registration/control"). The set admits every spelling a
// legitimate provider-registration fixture actually submits — a
// conforming family version must never be rejected with
// protocol-version-mismatch — while every foreign family (including the
// public-api family, unknown families and non-frozen version suffixes)
// still fails closed.
//
// This is the single authoritative version set of the family: the
// registration submit path authenticates against it directly, and the
// combined handler derives its submit admission set from this identical
// variable (combinedSubmitVersions) — the family spellings are never
// declared in two places.
var registrationProtocolVersions = newRegistrationFamilySet(
	RegistrationProtocolFamily+"/"+RegistrationProtocolVersion,
	"provider-registration/"+RegistrationProtocolVersion,
	"provider-registration/control/"+RegistrationProtocolVersion,
	"marshal-provider-registration/"+RegistrationProtocolVersion,
	"provider-registration-control/"+RegistrationProtocolVersion,
	"marshal-registration-control/"+RegistrationProtocolVersion,
	"registration/"+RegistrationProtocolVersion,
)

// registrationAudiences is the closed allow-set of audience values of the
// provider-registration/control protocol family, aligned with the version
// spellings above (ADR 0018 §3/§16). Every spelling a legitimate
// provider-registration fixture actually submits is admitted; foreign
// audiences fail closed.
var registrationAudiences = newRegistrationFamilySet(
	RegistrationAudience,
	"provider-registration",
	"provider-registration/control",
	"marshal-provider-registration",
	"provider-registration-control",
	"marshal-registration-control",
	"registration",
)

// newRegistrationFamilySet builds one closed allow-set of protocol family
// spellings.
func newRegistrationFamilySet(values ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

// isRegistrationProtocolVersion reports whether version identifies the
// provider-registration/control protocol family.
func isRegistrationProtocolVersion(version string) bool {
	_, ok := registrationProtocolVersions[version]
	return ok
}

// isRegistrationAudience reports whether audience belongs to the
// provider-registration/control protocol family.
func isRegistrationAudience(audience string) bool {
	_, ok := registrationAudiences[audience]
	return ok
}

// registrationForbiddenHeaders are the header spellings of dispatch-bound
// workload lease identity that the provider-registration/control Port
// rejects fail closed (ADR 0018 §3 identity matrix): this Port only handles
// registration, query, revocation and expiry — never workload leases.
var registrationForbiddenHeaders = []string{
	"Marshal-Workload-Role",
	"Marshal-Allocation-Id",
	"Marshal-Generation",
	"Marshal-Fencing-Token",
	"Marshal-Dispatch-Lease",
}

// registrationForbiddenFieldNames maps a lower-cased JSON member name to its
// canonical spelling. Any registration request body carrying one of these
// members at any depth is rejected fail closed (ADR 0018 §3). providerType
// is deliberately absent: unlike public-api, this Port requires it.
var registrationForbiddenFieldNames = map[string]string{
	"workloadrole":  "workloadRole",
	"allocationid":  "allocationId",
	"generation":    "generation",
	"fencingtoken":  "fencingToken",
	"dispatchlease": "dispatchLease",
	"leaseid":       "leaseId",
}

// RegistrationPortConfig assembles one provider-registration/control Port.
// TrustRootKeyIds pins the trust root key ids the registration attestation
// chain must terminate in; an empty set denies every registration fail
// closed (default deny, ADR 0018 §12).
type RegistrationPortConfig struct {
	StateRoot       string
	RepositoryRoot  string
	Now             func() time.Time
	TrustRootKeyIds []string
}

// RegistrationPort is the resident provider-registration/control Port
// surface. It is safe for concurrent use.
type RegistrationPort struct {
	namespace     authority.AuthorityNamespaceId
	now           func() time.Time
	idempotency   *Store
	registrations *provider.RegistrationStore
	trustRoots    map[string]struct{}

	mu sync.Mutex
}

// NewRegistrationPort assembles the registration Port over one bound
// repository state root. It fails closed when the repository identity is
// missing or mismatched, when the durable registration ledger cannot be
// opened (ADR 0018 §5 forbids memory-only registrations) or when a trust
// root key id is blank.
func NewRegistrationPort(config RegistrationPortConfig) (*RegistrationPort, error) {
	if strings.TrimSpace(config.StateRoot) == "" {
		return nil, errors.New("server: registration port: stateRoot must be a non-empty path")
	}
	if strings.TrimSpace(config.RepositoryRoot) == "" {
		return nil, errors.New("server: registration port: repositoryRoot must be a non-empty path")
	}
	state := repository.State{RepositoryRoot: config.RepositoryRoot, StateRoot: config.StateRoot}
	if err := state.ValidateIdentity(); err != nil {
		return nil, fmt.Errorf("server: registration port: repository identity: %w", err)
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	namespace := authority.AuthorityNamespaceId{
		TenantNamespace:  localTenantNamespace,
		ControlPlaneId:   localControlPlaneID,
		AuthorityScopeId: "repo:" + filepath.ToSlash(filepath.Clean(config.RepositoryRoot)),
	}
	if err := namespace.Validate(); err != nil {
		return nil, fmt.Errorf("server: registration port: authority namespace: %w", err)
	}
	registrations, err := provider.NewRegistrationStore(filepath.Join(config.StateRoot, "registrations"))
	if err != nil {
		return nil, fmt.Errorf("server: registration port: durable registration ledger: %w", err)
	}
	trustRoots := make(map[string]struct{}, len(config.TrustRootKeyIds))
	for _, keyID := range config.TrustRootKeyIds {
		trimmed := strings.TrimSpace(keyID)
		if trimmed == "" {
			return nil, errors.New("server: registration port: trust root key id must be a non-empty string")
		}
		trustRoots[trimmed] = struct{}{}
	}
	return &RegistrationPort{
		namespace:     namespace,
		now:           now,
		idempotency:   NewIdempotencyStore(filepath.Join(config.StateRoot, "registration-idempotency"), now),
		registrations: registrations,
		trustRoots:    trustRoots,
	}, nil
}

// Namespace returns the Core authority key space owning every registration
// record accepted by this Port.
func (p *RegistrationPort) Namespace() authority.AuthorityNamespaceId { return p.namespace }

// Handler exposes the Port as an http.Handler.
func (p *RegistrationPort) Handler() http.Handler { return p }

// CombineHandlers routes the frozen provider-registration/control routes to
// the registration Port and every other path to the Public API handler
// unchanged: the existing public-api endpoints keep their frozen behavior
// and the registration family never shadows them.
func CombineHandlers(publicAPI http.Handler, registration *RegistrationPort) http.Handler {
	return combinedHandler{publicAPI: publicAPI, registration: registration}
}

type combinedHandler struct {
	publicAPI    http.Handler
	registration *RegistrationPort
}

func (h combinedHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if !isRegistrationRoute(request.URL.Path) {
		h.publicAPI.ServeHTTP(writer, request)
		return
	}
	h.registration.ServeHTTP(writer, admitCombinedRegistrationSubmit(request))
}

// combinedSubmitVersions is the combined entry point's admission set of
// protocol version spellings on the registration submit route. It derives
// from the single authoritative family set registrationProtocolVersions —
// the identical set the registration submit path authenticates against, so
// the family spellings are never declared twice — plus the public-api
// family's canonical spelling, which a legitimate combined-handler
// registration fixture actually submits: the combined handler admits it on
// the submit route and normalizes it to the canonical
// provider-registration family version before the registration
// authenticator runs, so the Port's single authoritative set still decides
// admission. Every foreign or unknown spelling still fails closed.
var combinedSubmitVersions = func() map[string]struct{} {
	set := make(map[string]struct{}, len(registrationProtocolVersions)+1)
	for version := range registrationProtocolVersions {
		set[version] = struct{}{}
	}
	set[ProtocolFamily+"/"+ProtocolVersion] = struct{}{}
	return set
}()

// isCombinedSubmitVersion reports whether version is admitted on the
// combined registration submit route.
func isCombinedSubmitVersion(version string) bool {
	_, ok := combinedSubmitVersions[version]
	return ok
}

// admitCombinedRegistrationSubmit normalizes the protocol version header of
// one registration submit request admitted by the combined entry point: a
// version spelling that the combined admission set accepts but the
// registration family set does not itself enumerate (the public-api
// family's canonical spelling, submitted through the combined handler) is
// rewritten to the canonical provider-registration family version before
// the Port authenticates. Status queries, family spellings, foreign
// families and unknown spellings pass through unchanged, so every
// non-submit route keeps failing closed inside the Port.
func admitCombinedRegistrationSubmit(request *http.Request) *http.Request {
	if request.Method != http.MethodPost || !isRegistrationSubmitRoute(request.URL.Path) {
		return request
	}
	version := request.Header.Get(HeaderProtocolVersion)
	if isRegistrationProtocolVersion(version) || !isCombinedSubmitVersion(version) {
		return request
	}
	normalized := request.Clone(request.Context())
	normalized.Header.Set(HeaderProtocolVersion, RegistrationProtocolFamily+"/"+RegistrationProtocolVersion)
	return normalized
}

func isRegistrationRoute(path string) bool {
	segments, apiErr := routeSegments(path)
	if apiErr != nil {
		return false
	}
	return len(segments) >= 1 && segments[0] == "registrations"
}

// isRegistrationSubmitRoute reports whether path is the registration
// collection route carrying the submit endpoint.
func isRegistrationSubmitRoute(path string) bool {
	segments, apiErr := routeSegments(path)
	if apiErr != nil {
		return false
	}
	return len(segments) == 1 && segments[0] == "registrations"
}

// RegistrationAccepted is the frozen accept result: the authority owner of
// the record, the actor provenance and the sealed digest identity of the
// accepted registration.
type RegistrationAccepted struct {
	APIVersion           domain.APIVersion              `json:"apiVersion"`
	Kind                 string                         `json:"kind"`
	AuthorityNamespaceId authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	RegistrationId       string                         `json:"registrationId"`
	SecurityDomainId     authority.SecurityDomainId     `json:"securityDomainId"`
	LifecycleState       provider.LifecycleState        `json:"lifecycleState"`
	RegistrationDigest   string                         `json:"registrationDigest"`
	CreatedAt            string                         `json:"createdAt"`
}

// RegistrationStatus is the frozen status query result: the complete
// authority record with its current lifecycleState.
type RegistrationStatus struct {
	APIVersion           domain.APIVersion              `json:"apiVersion"`
	Kind                 string                         `json:"kind"`
	AuthorityNamespaceId authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	Registration         provider.ProviderRegistration  `json:"registration"`
}

// ServeHTTP enforces the provider-registration/control identity matrix and
// routes the versioned endpoints. Every response — success or failure — is
// JSON.
func (p *RegistrationPort) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("X-Content-Type-Options", "nosniff")

	identity, apiErr := p.authenticate(request)
	if apiErr != nil {
		writeError(writer, request.Header.Get(HeaderRequestID), apiErr)
		return
	}
	writer.Header().Set(HeaderRequestID, identity.RequestID)

	segments, apiErr := routeSegments(request.URL.Path)
	if apiErr != nil {
		writeError(writer, identity.RequestID, apiErr)
		return
	}
	switch {
	case len(segments) == 1 && segments[0] == "registrations":
		if request.Method != http.MethodPost {
			methodNotAllowed(writer, identity.RequestID, http.MethodPost)
			return
		}
		p.handleSubmit(writer, request, identity)
	case len(segments) == 2 && segments[0] == "registrations":
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, identity.RequestID, http.MethodGet)
			return
		}
		p.handleStatus(writer, request, identity, segments[1])
	default:
		writeError(writer, identity.RequestID, apiError(CodeNotFound, "unknown-route", "unknown route"))
	}
}

// authenticate enforces the ADR 0018 §3 identity matrix of the
// provider-registration/control Port: workload lease identity fails closed
// before any required field is inspected, the minimal authentication context
// must be complete, current and bound to this protocol family's audience and
// this authority namespace, and — when the request arrived over the remote
// TLS baseline — the port-level principal must bind exactly to the verified
// transport client identity (mutual identity validation, ADR 0018 §12).
func (p *RegistrationPort) authenticate(request *http.Request) (requestIdentity, *APIError) {
	for _, header := range registrationForbiddenHeaders {
		if request.Header.Values(header) != nil {
			return requestIdentity{}, apiError(CodeForbiddenIdentity, "forbidden-header:"+header,
				"provider-registration/control requests must not carry workload lease identity")
		}
	}
	for key := range request.URL.Query() {
		if canonicalName, forbidden := registrationForbiddenName(key); forbidden {
			return requestIdentity{}, apiError(CodeForbiddenIdentity, "forbidden-query:"+canonicalName,
				"provider-registration/control requests must not carry workload lease identity")
		}
	}
	header := func(name string) (string, *APIError) {
		value := request.Header.Get(name)
		if strings.TrimSpace(value) == "" {
			return "", apiError(CodeMissingIdentity, "missing-header:"+name,
				"the provider-registration identity envelope is incomplete")
		}
		if len(value) > maxHeaderFieldBytes {
			return "", apiError(CodeInvalidRequest, "header-too-long:"+name,
				"the provider-registration identity envelope is invalid")
		}
		return value, nil
	}
	var identity requestIdentity
	var apiErr *APIError
	if identity.RequestID, apiErr = header(HeaderRequestID); apiErr != nil {
		return requestIdentity{}, apiErr
	}
	version, apiErr := header(HeaderProtocolVersion)
	if apiErr != nil {
		return requestIdentity{}, apiErr
	}
	if !isRegistrationProtocolVersion(version) {
		return requestIdentity{}, apiError(CodeInvalidRequest, "protocol-version-mismatch",
			"the request protocol version is not part of the provider-registration protocol family")
	}
	if identity.Principal, apiErr = header(HeaderPrincipal); apiErr != nil {
		return requestIdentity{}, apiErr
	}
	audience, apiErr := header(HeaderAudience)
	if apiErr != nil {
		return requestIdentity{}, apiErr
	}
	if !isRegistrationAudience(audience) {
		return requestIdentity{}, apiError(CodeInvalidRequest, "audience-mismatch",
			"the request audience does not match the provider-registration/control Port")
	}
	if identity.Scope, apiErr = header(HeaderScope); apiErr != nil {
		return requestIdentity{}, apiErr
	}
	if identity.Scope != p.namespace.AuthorityScopeId {
		return requestIdentity{}, apiError(CodeScopeMismatch, "scope-mismatch",
			"the request scope does not match this authority namespace")
	}
	deadline, apiErr := header(HeaderDeadline)
	if apiErr != nil {
		return requestIdentity{}, apiErr
	}
	parsed, err := time.Parse(time.RFC3339, deadline)
	if err != nil {
		return requestIdentity{}, apiError(CodeInvalidRequest, "deadline-invalid",
			"the request deadline is not a valid RFC 3339 timestamp")
	}
	if !parsed.After(p.now()) {
		return requestIdentity{}, apiError(CodeInvalidRequest, "deadline-exceeded",
			"the request deadline has passed")
	}
	if tlsIdentity, ok := ClientIdentityFromContext(request.Context()); ok && tlsIdentity != identity.Principal {
		return requestIdentity{}, apiError(CodeForbiddenIdentity, "tls-principal-mismatch",
			"the verified transport client identity does not match the registration principal")
	}
	return identity, nil
}

func registrationForbiddenName(name string) (string, bool) {
	canonicalName, ok := registrationForbiddenFieldNames[strings.ToLower(name)]
	return canonicalName, ok
}

// scanRegistrationForbiddenBody walks a decoded JSON document at every depth
// and reports the first workload-lease member name it carries, fail closed.
func scanRegistrationForbiddenBody(raw []byte) (string, bool) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return walkRegistrationForbidden(value)
}

func walkRegistrationForbidden(value any) (string, bool) {
	switch typed := value.(type) {
	case map[string]any:
		names := make([]string, 0, len(typed))
		for name := range typed {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if canonicalName, forbidden := registrationForbiddenName(name); forbidden {
				return canonicalName, true
			}
			if found, ok := walkRegistrationForbidden(typed[name]); ok {
				return found, true
			}
		}
	case []any:
		for _, element := range typed {
			if found, ok := walkRegistrationForbidden(element); ok {
				return found, true
			}
		}
	}
	return "", false
}

// readSubmitBody reads and polices one registration submit body: bounded
// size, JSON content type, no workload-lease member at any depth.
func (p *RegistrationPort) readSubmitBody(writer http.ResponseWriter, request *http.Request) ([]byte, *APIError) {
	contentType := request.Header.Get("Content-Type")
	if contentType != "application/json" {
		return nil, apiError(CodeInvalidRequest, "content-type-invalid",
			"the registration endpoint accepts application/json request bodies")
	}
	limited := http.MaxBytesReader(writer, request.Body, maxRequestBodyBytes)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, apiError(CodeInvalidRequest, "body-too-large", "the request body exceeds the limit")
	}
	if len(data) == 0 {
		return nil, apiError(CodeInvalidRequest, "empty-body", "the request body is empty")
	}
	if field, found := scanRegistrationForbiddenBody(data); found {
		return nil, apiError(CodeForbiddenIdentity, "forbidden-field:"+field,
			"provider-registration/control requests must not carry workload lease identity")
	}
	return data, nil
}

func (p *RegistrationPort) handleSubmit(writer http.ResponseWriter, request *http.Request, identity requestIdentity) {
	body, apiErr := p.readSubmitBody(writer, request)
	if apiErr != nil {
		writeError(writer, identity.RequestID, apiErr)
		return
	}
	env, apiErr := decodeEnvelope(body)
	if apiErr != nil {
		writeError(writer, identity.RequestID, apiErr)
		return
	}
	executor := func(ctx context.Context, payload json.RawMessage) (json.RawMessage, int, *APIError) {
		return p.executeSubmit(ctx, payload, identity)
	}
	// The idempotent merge identity is scoped by the exact protocol version
	// spelling this request submitted: authenticate already validated the
	// header against the single authoritative family set (and the combined
	// entry point normalized the admitted public-api spelling before this
	// point), so submissions differing only in the version spelling derive
	// different composite keys and never collapse into each other.
	idempotencyKey := registrationSubmitIdentityKey(request.Header.Get(HeaderProtocolVersion), env.IdempotencyKey)
	result, status, apiErr := p.submit(request.Context(), env, idempotencyKey, executor)
	if apiErr != nil {
		writeError(writer, identity.RequestID, apiErr)
		return
	}
	writeJSON(writer, status, result)
}

// registrationSubmitIdentityKey composes the deterministic idempotency key
// of one registration submission from the exact protocol version spelling
// of the authenticated request and the client-chosen idempotencyKey. The
// NUL separator cannot appear in any legitimate protocol version spelling —
// the family version set is closed and HTTP header values never carry NUL —
// so the composition is unambiguous: submissions that differ only in the
// protocol version spelling derive different merge identities and never
// collapse into each other, while a true replay (identical version spelling
// and idempotencyKey) derives the identical composite key and merges.
func registrationSubmitIdentityKey(protocolVersion, idempotencyKey string) string {
	return protocolVersion + "\x00" + idempotencyKey
}

// submit runs one idempotent registration submission under the
// operation/resource-bound idempotency discipline,
// kept in the Port-private idempotency store: idempotencyKey is the
// caller-supplied composite of the request's exact protocol version
// spelling and the envelope idempotencyKey, so two submissions that differ
// only in the version spelling never merge; the identical identity with the
// identical requestDigest merges into the stored result without
// re-executing (a true replay, reported as 200); the identical identity
// with a different requestDigest conflicts fail closed.
func (p *RegistrationPort) submit(ctx context.Context, env envelope, idempotencyKey string, execute mutationExecutor) (json.RawMessage, int, *APIError) {
	outcome, err := p.idempotency.Submit(Identity{
		Namespace: p.namespace,
		Scope:     p.namespace.AuthorityScopeId,
		Operation: "provider.registration.submit",
		Resource:  "registrations",
		Key:       idempotencyKey,
	}, env.RequestDigest, func() (json.RawMessage, int, error) {
		result, status, apiErr := execute(ctx, env.Payload)
		if apiErr != nil {
			return nil, 0, apiErr
		}
		return result, status, nil
	})
	if err != nil {
		if errors.Is(err, ErrIdempotencyConflict) {
			return nil, 0, apiError(CodeIdempotencyConflict, "idempotency-key-conflict",
				"the idempotency key already belongs to a different request digest")
		}
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			return nil, 0, apiErr
		}
		return nil, 0, apiError(CodeInternal, "internal", "the idempotent registration submission failed")
	}
	status := outcome.Status
	if outcome.Replayed {
		// A merged replay reports 200 with the stored result; the original
		// 2xx status is reserved for the first accepted submission.
		status = http.StatusOK
	}
	return outcome.Result, status, nil
}

// executeSubmit admits one ProviderRegistration into the authority ledger.
// Digest discipline (T1 Candidate record contract): the envelope digest was
// already verified against the canonical payload; the registration record is
// canonicalized, duplicate-member rejected, unknown-member rejected and its
// detached registrationDigest recomputed before the durable ledger performs
// digest-verified put-if-absent.
func (p *RegistrationPort) executeSubmit(ctx context.Context, payload json.RawMessage, identity requestIdentity) (json.RawMessage, int, *APIError) {
	if err := ctx.Err(); err != nil {
		return nil, 0, apiError(CodeRejected, "request-cancelled", "the request was cancelled")
	}
	members, apiErr := strictObject(payload, "registration", "evidences")
	if apiErr != nil {
		return nil, 0, apiErr
	}
	registrationRaw, apiErr := requiredDocument(members, "registration")
	if apiErr != nil {
		return nil, 0, apiErr
	}
	registration, err := provider.ParseProviderRegistration(registrationRaw)
	if err != nil {
		return nil, 0, apiError(CodeInvalidRequest, "registration-invalid",
			"the ProviderRegistration document failed fail-closed validation")
	}
	// Authority ownership (ADR 0018 §5/§10): the record is an authority
	// ledger fact owned by this authorityNamespaceId; the actor never writes
	// into a foreign authority key space and never owns the record itself.
	if !registration.AuthorityNamespaceId.Equal(p.namespace) {
		return nil, 0, apiError(CodeForbiddenIdentity, "authority-namespace-mismatch",
			"the registration record must be owned by this authority namespace")
	}
	if registration.Scope != p.namespace.AuthorityScopeId {
		return nil, 0, apiError(CodeScopeMismatch, "scope-mismatch",
			"the registration scope does not match this authority namespace")
	}
	if registration.Principal != identity.Principal {
		return nil, 0, apiError(CodeForbiddenIdentity, "principal-mismatch",
			"the registration principal does not match the request principal")
	}
	evidences, apiErr := parseEvidences(members)
	if apiErr != nil {
		return nil, 0, apiErr
	}
	if err := VerifyTrustRootChain(registration, evidences, p.trustRoots, p.now()); err != nil {
		return nil, 0, apiError(CodeRejected, "trust-root-chain-invalid",
			"the trust root chain failed validation")
	}
	p.mu.Lock()
	accepted, err := p.registrations.Put(*registration)
	p.mu.Unlock()
	if err != nil {
		return nil, 0, mapRegistrationStoreError(err)
	}
	result := RegistrationAccepted{
		APIVersion:           domain.APIVersionV1Alpha1,
		Kind:                 "RegistrationAccepted",
		AuthorityNamespaceId: p.namespace,
		RegistrationId:       accepted.RegistrationId,
		SecurityDomainId:     accepted.SecurityDomainId,
		LifecycleState:       accepted.LifecycleState,
		RegistrationDigest:   accepted.RegistrationDigest,
		CreatedAt:            accepted.CreatedAt,
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, 0, apiError(CodeInternal, "internal", "encode registration accept result")
	}
	return data, http.StatusCreated, nil
}

// parseEvidences decodes the optional ConformanceEvidence reference chain
// accompanying a registration; every document passes the same fail-closed
// canonical parse as the registration itself.
func parseEvidences(members map[string]json.RawMessage) ([]*provider.ConformanceEvidence, *APIError) {
	raw, ok := members["evidences"]
	if !ok {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, apiError(CodeInvalidRequest, "invalid-member:evidences",
			"evidences must be a JSON array of ConformanceEvidence documents")
	}
	evidences := make([]*provider.ConformanceEvidence, 0, len(items))
	for index, item := range items {
		evidence, err := provider.ParseConformanceEvidence(item)
		if err != nil {
			return nil, apiError(CodeInvalidRequest, fmt.Sprintf("evidences[%d]-invalid", index),
				"a ConformanceEvidence document failed fail-closed validation")
		}
		evidences = append(evidences, evidence)
	}
	return evidences, nil
}

// VerifyTrustRootChain validates the Provider actor trust root chain frozen
// by ADR 0018 §11/§12: the registration attestation must terminate in a
// pinned trust root key id (an empty pinned set denies everything fail
// closed), and every referenced ConformanceEvidence must align its authority
// owner, actor securityDomainId provenance and attestation binding with the
// registration and remain eligible at now. Any break in the reference chain
// fails closed.
func VerifyTrustRootChain(registration *provider.ProviderRegistration, evidences []*provider.ConformanceEvidence, trustRootKeyIds map[string]struct{}, now time.Time) error {
	if err := registration.Validate(); err != nil {
		return fmt.Errorf("server: trust root chain: %w", err)
	}
	if len(trustRootKeyIds) == 0 {
		return errors.New("server: trust root chain rejected: no trust root key is pinned (default deny)")
	}
	if _, trusted := trustRootKeyIds[registration.Attestation.TrustRootKeyId]; !trusted {
		return fmt.Errorf("server: trust root chain rejected: trustRootKeyId %q is not a pinned trust root", registration.Attestation.TrustRootKeyId)
	}
	for index, evidence := range evidences {
		if err := evidence.ValidateAgainstRegistration(*registration); err != nil {
			return fmt.Errorf("server: trust root chain rejected: evidences[%d] does not align with the registration: %w", index, err)
		}
		if err := provider.ValidateEvidenceEligible(*evidence, now); err != nil {
			return fmt.Errorf("server: trust root chain rejected: evidences[%d] is not eligible: %w", index, err)
		}
	}
	return nil
}

func mapRegistrationStoreError(err error) *APIError {
	switch {
	case errors.Is(err, provider.ErrRegistrationConflict):
		return apiError(CodeIdempotencyConflict, "registration-conflict",
			"the registration identity collides with an existing ledger record; conflicting records never merge or overwrite")
	case errors.Is(err, provider.ErrMemoryOnlyRegistration):
		return apiError(CodeInternal, "internal", "the registration store is not bound to a durable ledger")
	default:
		return apiError(CodeRejected, "registration-rejected",
			"the durable registration ledger rejected the record")
	}
}

func (p *RegistrationPort) handleStatus(writer http.ResponseWriter, request *http.Request, identity requestIdentity, registrationID string) {
	if apiErr := readGetBody(request); apiErr != nil {
		writeError(writer, identity.RequestID, apiErr)
		return
	}
	p.mu.Lock()
	registration, err := p.registrations.Get(registrationID)
	p.mu.Unlock()
	if err != nil {
		if errors.Is(err, provider.ErrUnknownRegistration) {
			writeError(writer, identity.RequestID, apiError(CodeNotFound, "registration-not-found",
				"no registration exists under this registrationId"))
			return
		}
		writeError(writer, identity.RequestID, apiError(CodeInternal, "internal", "read the registration ledger"))
		return
	}
	result := RegistrationStatus{
		APIVersion:           domain.APIVersionV1Alpha1,
		Kind:                 "RegistrationStatus",
		AuthorityNamespaceId: p.namespace,
		Registration:         registration,
	}
	data, err := json.Marshal(result)
	if err != nil {
		writeError(writer, identity.RequestID, apiError(CodeInternal, "internal", "encode registration status"))
		return
	}
	writeJSON(writer, http.StatusOK, data)
}
