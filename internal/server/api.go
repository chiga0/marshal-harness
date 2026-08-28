// Package server implements marshal-server's versioned HTTP/JSON Public API
// (ADR 0018 §1/§3/§16): the public-api Port of the resident Control Plane.
//
// Core is the only business authority. Every handler delegates to the
// existing internal packages — planning for Task create, runstore/lifecycle/
// review for Task cancel, control for Run approval, runstore for Run status —
// and this package never recreates a lifecycle state machine or a second
// store write path. The package adds only the public-api protocol family
// itself: the versioned identity envelope, the ADR 0018 §3 identity matrix
// rejections, the idempotency authority records and the versioned error
// model frozen by openapi.json.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter"
	"github.com/chiga0/marshal-harness/internal/app"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	controlplane "github.com/chiga0/marshal-harness/internal/control"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/planning"
	"github.com/chiga0/marshal-harness/internal/repository"
	"github.com/chiga0/marshal-harness/internal/review"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// Frozen protocol family identity of the public-api Port (ADR 0018 §16).
// Every request must bind the protocol version, the audience and the
// authority scope; the loopback MVP carries the identity in HTTP headers.
const (
	ProtocolFamily  = "marshal-public-api"
	ProtocolVersion = "v1alpha1"
	APIPrefix       = "/v1alpha1"
	Audience        = "marshal-public-api"
)

// HeaderRequestID and its siblings carry the minimal public authentication
// context frozen by ADR 0018 §3 for the public-api Port.
const (
	HeaderRequestID       = "Marshal-Request-Id"
	HeaderProtocolVersion = "Marshal-Protocol-Version"
	HeaderPrincipal       = "Marshal-Principal"
	HeaderAudience        = "Marshal-Audience"
	HeaderScope           = "Marshal-Scope"
	HeaderDeadline        = "Marshal-Deadline"
)

// forbiddenHeaders are the header spellings of dispatch-bound identity that
// the public-api Port rejects fail closed (ADR 0018 §3 identity matrix).
var forbiddenHeaders = []string{
	"Marshal-Provider-Type",
	"Marshal-Workload-Role",
	"Marshal-Allocation-Id",
	"Marshal-Generation",
	"Marshal-Fencing-Token",
	"Marshal-Dispatch-Lease",
}

// forbiddenFieldNames maps a lower-cased JSON member name to its canonical
// spelling. Any request body carrying one of these members at any depth is
// rejected fail closed (ADR 0018 §3): providerType is forbidden on
// public-api outright, and workloadRole/allocationId/generation/fencingToken/
// DispatchLease belong exclusively to dispatch-bound Ports.
var forbiddenFieldNames = map[string]string{
	"providertype":  "providerType",
	"workloadrole":  "workloadRole",
	"allocationid":  "allocationId",
	"generation":    "generation",
	"fencingtoken":  "fencingToken",
	"dispatchlease": "dispatchLease",
	"leaseid":       "leaseId",
}

// Local single-instance derivation of the authority key space (ADR 0018 §10),
// identical to the embedded runtime: tenantNamespace=local,
// controlPlaneId=default, authorityScopeId derived from the bound repository
// identity.
const (
	localTenantNamespace = "local"
	localControlPlaneID  = "default"
)

const (
	maxHeaderFieldBytes    = 512
	maxActorBytes          = 512
	maxReasonBytes         = 12000
	maxIdempotencyKeyBytes = 512
)

// maxRequestBodyBytes caps one public-api request body.
const maxRequestBodyBytes int64 = 32 << 20

// digestPrefix is the prefix of every canonical content digest.
const digestPrefix = "sha256:"

// ErrorCode is the stable machine-readable error vocabulary of the public-api
// protocol family. The code plus the reason string is the frozen contract;
// the human message never carries dynamic or sensitive detail.
type ErrorCode string

const (
	CodeInvalidRequest      ErrorCode = "INVALID_REQUEST"
	CodeMissingIdentity     ErrorCode = "MISSING_IDENTITY"
	CodeForbiddenIdentity   ErrorCode = "FORBIDDEN_IDENTITY"
	CodeScopeMismatch       ErrorCode = "SCOPE_MISMATCH"
	CodeNotFound            ErrorCode = "NOT_FOUND"
	CodeIdempotencyConflict ErrorCode = "IDEMPOTENCY_CONFLICT"
	CodeInvalidState        ErrorCode = "INVALID_STATE"
	CodeRejected            ErrorCode = "REJECTED"
	CodeInternal            ErrorCode = "INTERNAL"
)

// status maps each stable code to its frozen HTTP status.
func (code ErrorCode) status() int {
	switch code {
	case CodeInvalidRequest, CodeMissingIdentity, CodeScopeMismatch:
		return http.StatusBadRequest
	case CodeForbiddenIdentity:
		return http.StatusForbidden
	case CodeNotFound:
		return http.StatusNotFound
	case CodeIdempotencyConflict, CodeInvalidState:
		return http.StatusConflict
	case CodeRejected:
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

// ErrorBody is the versioned error document frozen by openapi.json.
type ErrorBody struct {
	APIVersion domain.APIVersion `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Code       ErrorCode         `json:"code"`
	Reason     string            `json:"reason"`
	Message    string            `json:"message"`
	RequestID  string            `json:"requestId,omitempty"`
}

// APIError is the internal carrier of one versioned error. It implements
// error so executors can surface it through the idempotency seam.
type APIError struct {
	Code    ErrorCode
	Reason  string
	Message string
	// Status optionally overrides the code's frozen default status (used for
	// 405 method-not-allowed responses).
	Status int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("server: %s: %s", e.Code, e.Reason)
}

func apiError(code ErrorCode, reason, message string) *APIError {
	return &APIError{Code: code, Reason: reason, Message: message}
}

// requestIdentity is the validated public authentication context of one
// request. The deadline is validated by authenticate and bounds the request
// at the transport layer; it is never stored beyond the check.
type requestIdentity struct {
	RequestID string
	Principal string
	Scope     string
	Deadline  time.Time
}

// Config assembles one marshal-server Public API surface. Selector,
// Validator, Now and Getenv are injectable seams; the production defaults
// mirror the embedded CLI assembly exactly. The SSE projection seams
// (EventWatchInterval, SSEBufferLimit, SSEHeartbeatInterval,
// SSEReauthzInterval, Authorizer) select frozen defaults when zero/nil.
type Config struct {
	StateRoot      string
	RepositoryRoot string
	// Selector overrides the Worker adapter selector. When nil the server
	// builds app.NewWorkerRuntime(Getenv) exactly like the embedded CLI.
	Selector *adapter.Selector
	// Validator overrides the contract validator.
	Validator *contract.Validator
	// Now is the server clock; nil selects time.Now.
	Now func() time.Time
	// Getenv is the environment lookup for the default Worker runtime; nil
	// selects os.Getenv.
	Getenv func(string) string
	// RunExecutor invokes the single production execution composition root for
	// an existing durable Run. marshal-server injects the fixed CLI task-run
	// application boundary so the HTTP Port does not recreate sandbox,
	// authority, result-ingress or lifecycle wiring. Tests may inject the same
	// execution.Service seam directly. A nil executor leaves Task planning and
	// read/control endpoints available but makes /runs/{runId}/start fail
	// closed.
	RunExecutor func(context.Context, string) error
	// EventWatchInterval bounds how long a journaled event may remain
	// unprojected without a notify wake; zero selects the default.
	EventWatchInterval time.Duration
	// SSEBufferLimit caps one SSE subscriber's bounded buffer; a slow
	// subscriber exceeding it is disconnected and guided to resync. Zero
	// selects the default.
	SSEBufferLimit int
	// SSEHeartbeatInterval is the SSE keep-alive comment interval; zero
	// selects the default.
	SSEHeartbeatInterval time.Duration
	// SSEReauthzInterval is the periodic re-Authorization interval of SSE
	// subscriptions; zero selects the default.
	SSEReauthzInterval time.Duration
	// Authorizer decides whether one principal may keep observing the
	// scope's event projection during periodic and sensitive-change
	// re-Authorization; nil selects the default namespace/scope
	// revalidation.
	Authorizer func(principal string, namespace authority.AuthorityNamespaceId, scope string) error
}

// Server is the resident Public API surface. It is safe for concurrent use.
type Server struct {
	namespace      authority.AuthorityNamespaceId
	stateRoot      string
	repositoryRoot string
	now            func() time.Time
	validator      *contract.Validator
	selector       *adapter.Selector
	runExecutor    func(context.Context, string) error
	store          *runstore.Store
	idempotency    *Store

	events               *Projection
	sseBufferLimit       int
	sseHeartbeatInterval time.Duration
	sseReauthzInterval   time.Duration
	authorizer           func(principal string, namespace authority.AuthorityNamespaceId, scope string) error
}

// New assembles the Public API over one bound repository state root. It fails
// closed when the repository identity record is missing or mismatched, when
// the contracts do not compile, or when the Worker runtime cannot be built.
func New(config Config) (*Server, error) {
	if strings.TrimSpace(config.StateRoot) == "" {
		return nil, errors.New("server: stateRoot must be a non-empty path")
	}
	if strings.TrimSpace(config.RepositoryRoot) == "" {
		return nil, errors.New("server: repositoryRoot must be a non-empty path")
	}
	state := repository.State{RepositoryRoot: config.RepositoryRoot, StateRoot: config.StateRoot}
	if err := state.ValidateIdentity(); err != nil {
		return nil, fmt.Errorf("server: repository identity: %w", err)
	}
	validator := config.Validator
	if validator == nil {
		created, err := contract.NewValidator()
		if err != nil {
			return nil, fmt.Errorf("server: initialize contract validator: %w", err)
		}
		validator = created
	}
	selector := config.Selector
	if selector == nil {
		getenv := config.Getenv
		if getenv == nil {
			getenv = os.Getenv
		}
		runtime, err := app.NewWorkerRuntime(getenv)
		if err != nil {
			return nil, fmt.Errorf("server: initialize worker runtime: %w", err)
		}
		selector = runtime.Selector()
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
		return nil, fmt.Errorf("server: authority namespace: %w", err)
	}
	store := runstore.New(config.StateRoot)
	watchInterval := config.EventWatchInterval
	if watchInterval <= 0 {
		watchInterval = defaultEventWatchInterval
	}
	bufferLimit := config.SSEBufferLimit
	if bufferLimit <= 0 {
		bufferLimit = defaultSSEBufferLimit
	}
	heartbeat := config.SSEHeartbeatInterval
	if heartbeat <= 0 {
		heartbeat = defaultSSEHeartbeatInterval
	}
	reauthz := config.SSEReauthzInterval
	if reauthz <= 0 {
		reauthz = defaultSSEReauthzInterval
	}
	authorizer := config.Authorizer
	if authorizer == nil {
		authorizer = defaultAuthorizer
	}
	server := &Server{
		namespace:            namespace,
		stateRoot:            config.StateRoot,
		repositoryRoot:       config.RepositoryRoot,
		now:                  now,
		validator:            validator,
		selector:             selector,
		runExecutor:          config.RunExecutor,
		store:                store,
		idempotency:          NewIdempotencyStore(filepath.Join(config.StateRoot, "idempotency"), now),
		events:               newProjection(config.StateRoot, namespace, store, watchInterval),
		sseBufferLimit:       bufferLimit,
		sseHeartbeatInterval: heartbeat,
		sseReauthzInterval:   reauthz,
		authorizer:           authorizer,
	}
	go server.events.watch()
	return server, nil
}

// Namespace returns the Core authority key space this server writes under.
func (s *Server) Namespace() authority.AuthorityNamespaceId { return s.namespace }

// Handler exposes the server as an http.Handler.
func (s *Server) Handler() http.Handler { return s }

// ServeHTTP enforces the public-api identity matrix and routes the versioned
// endpoints. Every response — success or failure — is JSON, except one
// established /events SSE stream, which switches to text/event-stream.
func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("X-Content-Type-Options", "nosniff")

	identity, apiErr := s.authenticate(request)
	if apiErr != nil {
		writeError(writer, request.Header.Get(HeaderRequestID), apiErr)
		return
	}
	writer.Header().Set(HeaderRequestID, identity.RequestID)
	requestContext, cancel := context.WithTimeout(request.Context(), identity.Deadline.Sub(s.now()))
	defer cancel()
	request = request.WithContext(requestContext)

	segments, apiErr := routeSegments(request.URL.Path)
	if apiErr != nil {
		writeError(writer, identity.RequestID, apiErr)
		return
	}
	switch {
	case len(segments) == 1 && segments[0] == "tasks":
		if request.Method != http.MethodPost {
			methodNotAllowed(writer, identity.RequestID, http.MethodPost)
			return
		}
		s.handleTaskCreate(writer, request, identity)
	case len(segments) == 2 && segments[0] == "tasks":
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, identity.RequestID, http.MethodGet)
			return
		}
		s.handleTaskGet(writer, request, identity, segments[1])
	case len(segments) == 3 && segments[0] == "tasks" && segments[2] == "cancel":
		if request.Method != http.MethodPost {
			methodNotAllowed(writer, identity.RequestID, http.MethodPost)
			return
		}
		s.handleTaskCancel(writer, request, identity, segments[1])
	case len(segments) == 3 && segments[0] == "runs" && segments[2] == "approval":
		if request.Method != http.MethodPost {
			methodNotAllowed(writer, identity.RequestID, http.MethodPost)
			return
		}
		s.handleRunApproval(writer, request, identity, segments[1])
	case len(segments) == 3 && segments[0] == "runs" && segments[2] == "start":
		if request.Method != http.MethodPost {
			methodNotAllowed(writer, identity.RequestID, http.MethodPost)
			return
		}
		s.handleRunStart(writer, request, identity, segments[1])
	case len(segments) == 3 && segments[0] == "runs" && segments[2] == "status":
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, identity.RequestID, http.MethodGet)
			return
		}
		s.handleRunStatus(writer, request, identity, segments[1])
	case len(segments) == 1 && segments[0] == "events":
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, identity.RequestID, http.MethodGet)
			return
		}
		s.handleEventsStream(writer, request, identity)
	case len(segments) == 2 && segments[0] == "events" && segments[1] == "poll":
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, identity.RequestID, http.MethodGet)
			return
		}
		s.handleEventsPoll(writer, request, identity)
	default:
		writeError(writer, identity.RequestID, apiError(CodeNotFound, "unknown-route", "unknown route"))
	}
}

func routeSegments(path string) ([]string, *APIError) {
	if !strings.HasPrefix(path, APIPrefix+"/") {
		return nil, apiError(CodeNotFound, "unknown-route", "unknown route")
	}
	segments := strings.Split(path[len(APIPrefix)+1:], "/")
	for _, segment := range segments {
		if segment == "" {
			return nil, apiError(CodeNotFound, "unknown-route", "unknown route")
		}
	}
	return segments, nil
}

func methodNotAllowed(writer http.ResponseWriter, requestID string, allowed ...string) {
	writer.Header().Set("Allow", strings.Join(allowed, ", "))
	writeError(writer, requestID, &APIError{
		Code:    CodeInvalidRequest,
		Reason:  "method-not-allowed",
		Message: "method not allowed on this endpoint",
		Status:  http.StatusMethodNotAllowed,
	})
}

// authenticate enforces the ADR 0018 §3 identity matrix of the public-api
// Port: forbidden dispatch-bound identity fails closed before any required
// field is even inspected, and the minimal authentication context must be
// complete, current and scope-bound to this authority namespace.
func (s *Server) authenticate(request *http.Request) (requestIdentity, *APIError) {
	for _, header := range forbiddenHeaders {
		if request.Header.Values(header) != nil {
			return requestIdentity{}, apiError(CodeForbiddenIdentity, "forbidden-header:"+header,
				"public-api requests must not carry dispatch-bound identity")
		}
	}
	for key := range request.URL.Query() {
		if canonicalName, forbidden := forbiddenName(key); forbidden {
			return requestIdentity{}, apiError(CodeForbiddenIdentity, "forbidden-query:"+canonicalName,
				"public-api requests must not carry dispatch-bound identity")
		}
	}
	header := func(name string) (string, *APIError) {
		value := request.Header.Get(name)
		if strings.TrimSpace(value) == "" {
			return "", apiError(CodeMissingIdentity, "missing-header:"+name,
				"the public-api identity envelope is incomplete")
		}
		if len(value) > maxHeaderFieldBytes {
			return "", apiError(CodeInvalidRequest, "header-too-long:"+name,
				"the public-api identity envelope is invalid")
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
	if version != ProtocolFamily+"/"+ProtocolVersion {
		return requestIdentity{}, apiError(CodeInvalidRequest, "protocol-version-mismatch",
			"the request protocol version is not part of this protocol family")
	}
	if identity.Principal, apiErr = header(HeaderPrincipal); apiErr != nil {
		return requestIdentity{}, apiErr
	}
	audience, apiErr := header(HeaderAudience)
	if apiErr != nil {
		return requestIdentity{}, apiErr
	}
	if audience != Audience {
		return requestIdentity{}, apiError(CodeInvalidRequest, "audience-mismatch",
			"the request audience does not match the public-api Port")
	}
	if identity.Scope, apiErr = header(HeaderScope); apiErr != nil {
		return requestIdentity{}, apiErr
	}
	if identity.Scope != s.namespace.AuthorityScopeId {
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
	if !parsed.After(s.now()) {
		return requestIdentity{}, apiError(CodeInvalidRequest, "deadline-exceeded",
			"the request deadline has passed")
	}
	identity.Deadline = parsed
	return identity, nil
}

func forbiddenName(name string) (string, bool) {
	canonicalName, ok := forbiddenFieldNames[strings.ToLower(name)]
	return canonicalName, ok
}

// scanForbiddenBody walks a decoded JSON document at every depth and reports
// the first dispatch-bound member name it carries, fail closed.
func scanForbiddenBody(raw []byte) (string, bool) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return walkForbidden(value)
}

func walkForbidden(value any) (string, bool) {
	switch typed := value.(type) {
	case map[string]any:
		names := make([]string, 0, len(typed))
		for name := range typed {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if canonicalName, forbidden := forbiddenName(name); forbidden {
				return canonicalName, true
			}
			if found, ok := walkForbidden(typed[name]); ok {
				return found, true
			}
		}
	case []any:
		for _, element := range typed {
			if found, ok := walkForbidden(element); ok {
				return found, true
			}
		}
	}
	return "", false
}

// readMutationBody reads and police-checks one mutating request body: bounded
// size, JSON content type, no forbidden dispatch-bound member at any depth.
func readMutationBody(writer http.ResponseWriter, request *http.Request) ([]byte, *APIError) {
	contentType := request.Header.Get("Content-Type")
	if contentType != "application/json" {
		return nil, apiError(CodeInvalidRequest, "content-type-invalid",
			"mutating endpoints accept application/json request bodies")
	}
	limited := http.MaxBytesReader(writer, request.Body, maxRequestBodyBytes)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, apiError(CodeInvalidRequest, "body-too-large", "the request body exceeds the limit")
	}
	if len(data) == 0 {
		return nil, apiError(CodeInvalidRequest, "empty-body", "the request body is empty")
	}
	if field, found := scanForbiddenBody(data); found {
		return nil, apiError(CodeForbiddenIdentity, "forbidden-field:"+field,
			"public-api requests must not carry dispatch-bound identity")
	}
	return data, nil
}

// readGetBody fails closed on any body attached to a read endpoint.
func readGetBody(request *http.Request) *APIError {
	if request.Body == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(request.Body, 1))
	if err != nil || len(data) > 0 {
		return apiError(CodeInvalidRequest, "body-not-allowed", "read endpoints accept no request body")
	}
	return nil
}

// strictObject decodes one JSON object and rejects any member outside the
// frozen allowed set, so unknown fields can never smuggle content past the
// endpoint schemas.
func strictObject(raw []byte, allowed ...string) (map[string]json.RawMessage, *APIError) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return nil, apiError(CodeInvalidRequest, "malformed-json", "the document is not a JSON object")
	}
	for name := range members {
		if !slices.Contains(allowed, name) {
			return nil, apiError(CodeInvalidRequest, "unknown-member:"+name,
				"the document carries a member this endpoint does not accept")
		}
	}
	return members, nil
}

func requiredString(members map[string]json.RawMessage, name string, maxBytes int) (string, *APIError) {
	raw, ok := members[name]
	if !ok {
		return "", apiError(CodeInvalidRequest, "missing-member:"+name, "a required member is absent")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", apiError(CodeInvalidRequest, "invalid-member:"+name, "a required member is not a string")
	}
	if strings.TrimSpace(value) == "" {
		return "", apiError(CodeInvalidRequest, "empty-member:"+name, "a required member is empty")
	}
	if len(value) > maxBytes {
		return "", apiError(CodeInvalidRequest, "member-too-long:"+name, "a member exceeds its size limit")
	}
	return value, nil
}

func optionalString(members map[string]json.RawMessage, name string, maxBytes int) (string, *APIError) {
	raw, ok := members[name]
	if !ok {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", apiError(CodeInvalidRequest, "invalid-member:"+name, "a member is not a string")
	}
	if strings.TrimSpace(value) == "" {
		return "", apiError(CodeInvalidRequest, "empty-member:"+name, "a member is empty")
	}
	if len(value) > maxBytes {
		return "", apiError(CodeInvalidRequest, "member-too-long:"+name, "a member exceeds its size limit")
	}
	return value, nil
}

// envelope is the frozen idempotent submission envelope shared by every
// mutating endpoint: the idempotency key, the client-declared request digest
// and the endpoint payload whose canonical digest must equal it. Bound to the
// server-derived authorityNamespaceId and the request scope header, one
// envelope yields the complete ADR 0018 §3 submission identity quadruple
// (authorityNamespaceId, scope, idempotencyKey, requestDigest).
type envelope struct {
	IdempotencyKey string          `json:"idempotencyKey"`
	RequestDigest  string          `json:"requestDigest"`
	Payload        json.RawMessage `json:"payload"`
}

func decodeEnvelope(body []byte) (envelope, *APIError) {
	members, apiErr := strictObject(body, "idempotencyKey", "requestDigest", "payload")
	if apiErr != nil {
		return envelope{}, apiErr
	}
	key, apiErr := requiredString(members, "idempotencyKey", maxIdempotencyKeyBytes)
	if apiErr != nil {
		return envelope{}, apiErr
	}
	digest, apiErr := requiredString(members, "requestDigest", 256)
	if apiErr != nil {
		return envelope{}, apiErr
	}
	if !strings.HasPrefix(digest, digestPrefix) || len(digest) != len(digestPrefix)+64 {
		return envelope{}, apiError(CodeInvalidRequest, "request-digest-invalid",
			"requestDigest must be a sha256 hex digest")
	}
	payloadRaw, ok := members["payload"]
	if !ok || len(payloadRaw) == 0 {
		return envelope{}, apiError(CodeInvalidRequest, "missing-member:payload", "a required member is absent")
	}
	payloadDigest, err := canonical.DigestJSON(payloadRaw)
	if err != nil {
		return envelope{}, apiError(CodeInvalidRequest, "payload-not-json",
			"the payload is not a canonicalizable JSON document")
	}
	if payloadDigest != digest {
		return envelope{}, apiError(CodeInvalidRequest, "request-digest-mismatch",
			"requestDigest does not match the canonical payload digest")
	}
	return envelope{IdempotencyKey: key, RequestDigest: digest, Payload: payloadRaw}, nil
}

// mutationExecutor performs one business operation for an idempotent
// submission. Executors return the frozen result document and its status, or
// an *APIError; business failures are never recorded as idempotency facts.
type mutationExecutor func(ctx context.Context, payload json.RawMessage) (json.RawMessage, int, *APIError)

// submit runs one idempotent mutation under the complete ADR 0018 §3
// submission identity quadruple (authorityNamespaceId, scope, idempotencyKey,
// requestDigest): the identical quadruple merges into the stored result
// without re-executing the business operation; the identical
// (authorityNamespaceId, scope, idempotencyKey) with a different
// requestDigest conflicts fail closed.
func (s *Server) submit(ctx context.Context, env envelope, execute mutationExecutor) (json.RawMessage, int, *APIError) {
	outcome, err := s.idempotency.Submit(Identity{
		Namespace: s.namespace,
		Scope:     s.namespace.AuthorityScopeId,
		Key:       env.IdempotencyKey,
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
		return nil, 0, apiError(CodeInternal, "internal", "the idempotent submission failed")
	}
	status := outcome.Status
	if outcome.Replayed {
		// A merged replay reports 200 with the stored result; the original
		// 2xx status is reserved for the first accepted submission.
		status = http.StatusOK
	}
	return outcome.Result, status, nil
}

// TaskSubmission is the frozen create result: the submission identity, the
// selected adapter and the durable READY RunState. AuthorityNamespaceId is
// the authority key space owning the idempotent submission record; together
// with scope, idempotencyKey and requestDigest it forms the frozen ADR 0018
// §3 submission identity quadruple.
type TaskSubmission struct {
	APIVersion           domain.APIVersion              `json:"apiVersion"`
	Kind                 string                         `json:"kind"`
	AuthorityNamespaceId authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	TaskID               string                         `json:"taskId"`
	RunID                string                         `json:"runId"`
	AdapterID            string                         `json:"adapterId"`
	State                domain.RunState                `json:"state"`
}

// RunSummary is one Run of a TaskView projection.
type RunSummary struct {
	RunID          string       `json:"runId"`
	State          domain.State `json:"state"`
	Sequence       uint64       `json:"sequence"`
	CreatedAt      time.Time    `json:"createdAt"`
	UpdatedAt      time.Time    `json:"updatedAt"`
	TerminalReason string       `json:"terminalReason,omitempty"`
}

// TaskView is the read projection of one Task over the Run store. It is a
// rebuildable projection and never a second authority.
type TaskView struct {
	APIVersion  domain.APIVersion `json:"apiVersion"`
	Kind        string            `json:"kind"`
	TaskID      string            `json:"taskId"`
	Title       string            `json:"title,omitempty"`
	LatestRunID string            `json:"latestRunId"`
	Runs        []RunSummary      `json:"runs"`
}

// TaskCancellation is the frozen cancel result. AuthorityNamespaceId is the
// authority key space owning the idempotent submission record of this
// cancellation (ADR 0018 §3 submission identity quadruple).
type TaskCancellation struct {
	APIVersion           domain.APIVersion              `json:"apiVersion"`
	Kind                 string                         `json:"kind"`
	AuthorityNamespaceId authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	TaskID               string                         `json:"taskId"`
	RunID                string                         `json:"runId"`
	State                domain.State                   `json:"state"`
	TerminalReason       string                         `json:"terminalReason"`
	Actor                string                         `json:"actor"`
	Sequence             uint64                         `json:"sequence"`
}

// RunExecution is the durable result projection returned after the one
// production execution composition root finishes a bounded Worker attempt.
// It deliberately carries no Worker-authored claims: State is re-read from
// the authoritative Run journal/snapshot after execution returns.
type RunExecution struct {
	APIVersion           domain.APIVersion              `json:"apiVersion"`
	Kind                 string                         `json:"kind"`
	AuthorityNamespaceId authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	TaskID               string                         `json:"taskId"`
	RunID                string                         `json:"runId"`
	AttemptID            string                         `json:"attemptId"`
	State                domain.RunState                `json:"state"`
}

func (s *Server) handleTaskCreate(writer http.ResponseWriter, request *http.Request, identity requestIdentity) {
	body, apiErr := readMutationBody(writer, request)
	if apiErr != nil {
		writeError(writer, identity.RequestID, apiErr)
		return
	}
	env, apiErr := decodeEnvelope(body)
	if apiErr != nil {
		writeError(writer, identity.RequestID, apiErr)
		return
	}
	result, status, apiErr := s.submit(request.Context(), env, s.executeTaskCreate)
	if apiErr != nil {
		writeError(writer, identity.RequestID, apiErr)
		return
	}
	writeJSON(writer, status, result)
}

func (s *Server) executeTaskCreate(ctx context.Context, payload json.RawMessage) (json.RawMessage, int, *APIError) {
	members, apiErr := strictObject(payload, "runId", "taskSpec", "policySnapshot")
	if apiErr != nil {
		return nil, 0, apiErr
	}
	runID, apiErr := requiredString(members, "runId", 256)
	if apiErr != nil {
		return nil, 0, apiErr
	}
	if err := domain.ValidateID(runID); err != nil {
		return nil, 0, apiError(CodeInvalidRequest, "invalid-id", "the runId is not a valid Marshal ID")
	}
	taskSpec, apiErr := requiredDocument(members, "taskSpec")
	if apiErr != nil {
		return nil, 0, apiErr
	}
	policySnapshot, apiErr := requiredDocument(members, "policySnapshot")
	if apiErr != nil {
		return nil, 0, apiErr
	}
	if err := s.validator.Validate(domain.KindTask, taskSpec); err != nil {
		return nil, 0, apiError(CodeRejected, "task-spec-invalid", "the TaskSpec failed contract validation")
	}
	if err := s.validator.Validate(domain.KindPolicySnapshot, policySnapshot); err != nil {
		return nil, 0, apiError(CodeRejected, "policy-snapshot-invalid", "the PolicySnapshot failed contract validation")
	}
	result, err := planning.Plan(ctx, planning.Input{
		StateRoot:      s.stateRoot,
		RepositoryRoot: s.repositoryRoot,
		RunID:          runID,
		TaskSpec:       taskSpec,
		PolicySnapshot: policySnapshot,
		Selector:       s.selector,
		Validator:      s.validator,
		Now:            s.now(),
	})
	if err != nil {
		return nil, 0, mapPlanningError(err)
	}
	if result.Adapter == nil || result.State.State != domain.StateReady {
		return nil, 0, apiError(CodeRejected, "planning-rejected", "planning produced no READY Run")
	}
	var task domain.TaskSpec
	if err := json.Unmarshal(taskSpec, &task); err != nil {
		return nil, 0, apiError(CodeRejected, "task-spec-invalid", "the TaskSpec failed contract validation")
	}
	submission := TaskSubmission{
		APIVersion:           domain.APIVersionV1Alpha1,
		Kind:                 "TaskSubmission",
		AuthorityNamespaceId: s.namespace,
		TaskID:               task.Metadata.ID,
		RunID:                result.State.RunID,
		AdapterID:            result.Adapter.ID(),
		State:                result.State,
	}
	data, err := json.Marshal(submission)
	if err != nil {
		return nil, 0, apiError(CodeInternal, "internal", "encode submission result")
	}
	return data, http.StatusCreated, nil
}

func requiredDocument(members map[string]json.RawMessage, name string) ([]byte, *APIError) {
	raw, ok := members[name]
	if !ok || len(raw) == 0 {
		return nil, apiError(CodeInvalidRequest, "missing-member:"+name, "a required member is absent")
	}
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, apiError(CodeInvalidRequest, "invalid-member:"+name, "a required member is not a JSON document")
	}
	return raw, nil
}

func mapPlanningError(err error) *APIError {
	if errors.Is(err, runstore.ErrLeaseHeld) {
		return apiError(CodeInvalidState, "run-lease-held", "the Run lease is held by another writer")
	}
	if strings.Contains(err.Error(), "already exists") {
		return apiError(CodeInvalidState, "run-already-exists", "a Run with this runId already exists")
	}
	return apiError(CodeRejected, "planning-rejected", "task planning failed")
}

func (s *Server) handleTaskGet(writer http.ResponseWriter, request *http.Request, identity requestIdentity, taskID string) {
	if apiErr := readGetBody(request); apiErr != nil {
		writeError(writer, identity.RequestID, apiErr)
		return
	}
	if err := domain.ValidateID(taskID); err != nil {
		writeError(writer, identity.RequestID, apiError(CodeInvalidRequest, "invalid-id", "the taskId is not a valid Marshal ID"))
		return
	}
	runs, apiErr := s.taskRuns(taskID)
	if apiErr != nil {
		writeError(writer, identity.RequestID, apiErr)
		return
	}
	if len(runs) == 0 {
		writeError(writer, identity.RequestID, apiError(CodeNotFound, "task-not-found", "no Run belongs to this Task"))
		return
	}
	latest := runs[len(runs)-1]
	view := TaskView{
		APIVersion:  domain.APIVersionV1Alpha1,
		Kind:        "TaskView",
		TaskID:      taskID,
		Title:       s.taskTitle(latest.RunID),
		LatestRunID: latest.RunID,
	}
	for _, state := range runs {
		view.Runs = append(view.Runs, RunSummary{
			RunID:          state.RunID,
			State:          state.State,
			Sequence:       state.Sequence,
			CreatedAt:      state.CreatedAt,
			UpdatedAt:      state.UpdatedAt,
			TerminalReason: state.TerminalReason,
		})
	}
	data, err := json.Marshal(view)
	if err != nil {
		writeError(writer, identity.RequestID, apiError(CodeInternal, "internal", "encode task view"))
		return
	}
	writeJSON(writer, http.StatusOK, data)
}

// taskRuns projects every Run of one Task from the Run store snapshots in a
// deterministic order (createdAt, then runId). Read errors fail closed.
func (s *Server) taskRuns(taskID string) ([]domain.RunState, *APIError) {
	runsDirectory := filepath.Join(s.stateRoot, "runs")
	entries, err := os.ReadDir(runsDirectory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, apiError(CodeInternal, "internal", "read run store")
	}
	var runs []domain.RunState
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		state, err := s.store.ReadSnapshot(entry.Name())
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, apiError(CodeInternal, "internal", "read run snapshot")
		}
		if state.TaskID == taskID {
			runs = append(runs, state)
		}
	}
	sort.Slice(runs, func(i, j int) bool {
		if !runs[i].CreatedAt.Equal(runs[j].CreatedAt) {
			return runs[i].CreatedAt.Before(runs[j].CreatedAt)
		}
		return runs[i].RunID < runs[j].RunID
	})
	return runs, nil
}

// taskTitle reads the frozen TaskSpec title of one Run's directory; an
// absent or unreadable spec yields an empty title rather than an error,
// because the projection must stay read-only.
func (s *Server) taskTitle(runID string) string {
	data, err := os.ReadFile(filepath.Join(s.stateRoot, "runs", runID, "task-spec.json"))
	if err != nil {
		return ""
	}
	var task domain.TaskSpec
	if json.Unmarshal(data, &task) != nil {
		return ""
	}
	return task.Metadata.Title
}

func (s *Server) handleTaskCancel(writer http.ResponseWriter, request *http.Request, identity requestIdentity, taskID string) {
	if err := domain.ValidateID(taskID); err != nil {
		writeError(writer, identity.RequestID, apiError(CodeInvalidRequest, "invalid-id", "the taskId is not a valid Marshal ID"))
		return
	}
	body, apiErr := readMutationBody(writer, request)
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
		return s.executeTaskCancel(ctx, taskID, payload)
	}
	result, status, apiErr := s.submit(request.Context(), env, executor)
	if apiErr != nil {
		writeError(writer, identity.RequestID, apiErr)
		return
	}
	writeJSON(writer, status, result)
}

func (s *Server) executeTaskCancel(ctx context.Context, taskID string, payload json.RawMessage) (json.RawMessage, int, *APIError) {
	members, apiErr := strictObject(payload, "actor", "reason", "runId")
	if apiErr != nil {
		return nil, 0, apiErr
	}
	actor, apiErr := requiredString(members, "actor", maxActorBytes)
	if apiErr != nil {
		return nil, 0, apiErr
	}
	reason, apiErr := requiredString(members, "reason", maxReasonBytes)
	if apiErr != nil {
		return nil, 0, apiErr
	}
	runID, apiErr := optionalString(members, "runId", 256)
	if apiErr != nil {
		return nil, 0, apiErr
	}
	if runID != "" {
		if err := domain.ValidateID(runID); err != nil {
			return nil, 0, apiError(CodeInvalidRequest, "invalid-id", "the runId is not a valid Marshal ID")
		}
	}
	runs, apiErr := s.taskRuns(taskID)
	if apiErr != nil {
		return nil, 0, apiErr
	}
	if len(runs) == 0 {
		return nil, 0, apiError(CodeNotFound, "task-not-found", "no Run belongs to this Task")
	}
	var target *domain.RunState
	if runID != "" {
		for index := range runs {
			if runs[index].RunID == runID {
				target = &runs[index]
				break
			}
		}
		if target == nil {
			return nil, 0, apiError(CodeNotFound, "run-not-found", "the Run does not belong to this Task")
		}
	} else {
		var candidates []domain.RunState
		for _, state := range runs {
			if !state.State.Terminal() {
				candidates = append(candidates, state)
			}
		}
		if len(candidates) == 0 {
			return nil, 0, apiError(CodeInvalidState, "no-cancelable-run", "every Run of this Task is terminal")
		}
		if len(candidates) > 1 {
			return nil, 0, apiError(CodeRejected, "ambiguous-run", "exactly one runId is required to cancel this Task")
		}
		target = &candidates[0]
	}
	return s.abortRun(ctx, *target, actor, reason)
}

// abortRun reuses the frozen explicit-abort flow: the lifecycle reducer is
// the only state machine, the journal is the only write path, and the
// terminal Outcome records follow the identical staging order as the
// embedded CLI abort. Only RETRY_PENDING runs are cancelable.
func (s *Server) abortRun(ctx context.Context, state domain.RunState, actor, reason string) (json.RawMessage, int, *APIError) {
	if err := ctx.Err(); err != nil {
		return nil, 0, apiError(CodeRejected, "request-cancelled", "the request was cancelled")
	}
	lease, err := s.store.Acquire(state.RunID)
	if err != nil {
		if errors.Is(err, runstore.ErrLeaseHeld) {
			return nil, 0, apiError(CodeInvalidState, "run-lease-held", "the Run lease is held by another writer")
		}
		return nil, 0, apiError(CodeRejected, "run-lease-unavailable", "the Run lease could not be acquired")
	}
	defer func() { _ = lease.Release() }()
	current, err := s.store.Inspect(state.RunID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, apiError(CodeNotFound, "run-not-found", "the Run does not exist")
		}
		return nil, 0, apiError(CodeRejected, "run-inspect-failed", "the Run state could not be inspected")
	}
	if current.State.Terminal() {
		return nil, 0, apiError(CodeInvalidState, "run-terminal", "the Run is already terminal")
	}
	if current.State != domain.StateRetryPending {
		return nil, 0, apiError(CodeInvalidState, "invalid-lifecycle-transition",
			"only a RETRY_PENDING Run can be cancelled")
	}
	eventID, err := domain.NewID("event")
	if err != nil {
		return nil, 0, apiError(CodeInternal, "internal", "generate event identity")
	}
	timestamp := s.now().UTC()
	payload := map[string]any{"terminalReason": lifecycle.AbortTerminalReason, "reason": reason}
	event := domain.RunEvent{
		APIVersion: domain.APIVersionV1Alpha1,
		Kind:       domain.KindRunEvent,
		EventID:    eventID,
		RunID:      current.RunID,
		AttemptID:  current.CurrentAttemptID,
		Sequence:   current.Sequence + 1,
		Type:       lifecycle.AbortEventType,
		StateFrom:  current.State,
		StateTo:    domain.StateBlocked,
		Timestamp:  timestamp,
		Actor:      &domain.Actor{Type: domain.ControlSourceTypeHuman, ID: actor},
		Payload:    payload,
	}
	next, err := lifecycle.Reduce(current, event, lifecycle.Guard{LeaseHeld: true})
	if err != nil {
		return nil, 0, apiError(CodeInvalidState, "invalid-lifecycle-transition", "the lifecycle rejected the abort")
	}
	payloadData, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, apiError(CodeInternal, "internal", "encode abort evidence")
	}
	abortDigest, err := canonical.DigestJSON(payloadData)
	if err != nil {
		return nil, 0, apiError(CodeInternal, "internal", "digest abort evidence")
	}
	runDirectory := filepath.Join(s.stateRoot, "runs", current.RunID)
	prepared, err := review.PrepareOutcome(runDirectory, review.OutcomeData{
		TaskID:              current.TaskID,
		RunID:               current.RunID,
		TerminalState:       domain.StateBlocked,
		Verdict:             "abort",
		FinalReviewRound:    max(1, current.ReviewRound),
		FinalReviewDigest:   abortDigest,
		FinalEvidenceDigest: abortDigest,
		Summary:             reason,
		FindingCount:        0,
		GeneratedAt:         timestamp,
	})
	if err != nil {
		return nil, 0, apiError(CodeRejected, "outcome-unavailable", "the terminal Outcome could not be staged")
	}
	if err := stageAbortResult(runDirectory, current, actor, reason, timestamp); err != nil {
		prepared.Abort()
		return nil, 0, apiError(CodeRejected, "abort-staging-failed", "the abort record could not be staged")
	}
	if err := s.store.Append(lease, event, current.Sequence); err != nil {
		prepared.Abort()
		removeAbortResult(runDirectory)
		if errors.Is(err, lifecycle.ErrInvalidTransition) {
			return nil, 0, apiError(CodeInvalidState, "invalid-lifecycle-transition", "the lifecycle rejected the abort")
		}
		if errors.Is(err, runstore.ErrConflict) {
			return nil, 0, apiError(CodeInvalidState, "journal-conflict", "the Run journal rejected the abort event")
		}
		return nil, 0, apiError(CodeRejected, "journal-append-failed", "the abort event could not be journaled")
	}
	if err := commitAbortResult(runDirectory); err != nil {
		return nil, 0, apiError(CodeRejected, "abort-commit-failed",
			"the abort record could not be committed; the journal event is retained and reconciliation is required")
	}
	if err := prepared.Commit(); err != nil {
		return nil, 0, apiError(CodeRejected, "outcome-commit-failed",
			"the terminal Outcome could not be committed; the journal event is retained and reconciliation is required")
	}
	if err := s.store.WriteSnapshot(lease, next); err != nil {
		return nil, 0, apiError(CodeRejected, "snapshot-write-failed",
			"the state snapshot could not be written; the journal and Outcome are retained and reconciliation is required")
	}
	cancellation := TaskCancellation{
		APIVersion:           domain.APIVersionV1Alpha1,
		Kind:                 "TaskCancellation",
		AuthorityNamespaceId: s.namespace,
		TaskID:               current.TaskID,
		RunID:                current.RunID,
		State:                next.State,
		TerminalReason:       lifecycle.AbortTerminalReason,
		Actor:                actor,
		Sequence:             next.Sequence,
	}
	data, err := json.Marshal(cancellation)
	if err != nil {
		return nil, 0, apiError(CodeInternal, "internal", "encode cancellation result")
	}
	return data, http.StatusOK, nil
}

// stageAbortResult stages the human-readable abort record exactly like the
// embedded CLI abort: a pending file is written and synced first, and the
// final result.md is only ever produced by commitAbortResult.
func stageAbortResult(runDirectory string, state domain.RunState, actor, reason string, now time.Time) error {
	if _, err := os.Lstat(filepath.Join(runDirectory, "result.md")); err == nil {
		return errors.New("terminal result.md already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	pending := filepath.Join(runDirectory, "result.md.pending")
	if err := os.Remove(pending); err != nil && !os.IsNotExist(err) {
		return err
	}
	content := fmt.Sprintf("# Run 终止记录\n\n- 任务 ID：%s\n- Run ID：%s\n- 终态：BLOCKED\n- 终态原因：%s\n- 操作者：%s\n- 终止原因：%s\n- 生成时间：%s\n",
		state.TaskID, state.RunID, lifecycle.AbortTerminalReason, actor, reason, now.UTC().Format(time.RFC3339))
	file, err := os.OpenFile(pending, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(content)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr == nil {
		writeErr = syncErr
	}
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		os.Remove(pending)
	}
	return writeErr
}

func commitAbortResult(runDirectory string) error {
	if err := os.Rename(filepath.Join(runDirectory, "result.md.pending"), filepath.Join(runDirectory, "result.md")); err != nil {
		return err
	}
	directory, err := os.Open(runDirectory)
	if err != nil {
		return err
	}
	err = directory.Sync()
	_ = directory.Close()
	return err
}

func removeAbortResult(runDirectory string) {
	_ = os.Remove(filepath.Join(runDirectory, "result.md.pending"))
}

// handleRunStart starts or resumes one existing Run through the injected
// production composition root. The endpoint is idempotent at the Public API
// boundary; a lost response can be replayed after server restart without
// launching a second Attempt.
func (s *Server) handleRunStart(writer http.ResponseWriter, request *http.Request, identity requestIdentity, runID string) {
	if err := domain.ValidateID(runID); err != nil {
		writeError(writer, identity.RequestID, apiError(CodeInvalidRequest, "invalid-id", "the runId is not a valid Marshal ID"))
		return
	}
	body, apiErr := readMutationBody(writer, request)
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
		return s.executeRunStart(ctx, runID, payload)
	}
	result, status, apiErr := s.submit(request.Context(), env, executor)
	if apiErr != nil {
		writeError(writer, identity.RequestID, apiErr)
		return
	}
	writeJSON(writer, status, result)
}

func (s *Server) executeRunStart(ctx context.Context, runID string, payload json.RawMessage) (json.RawMessage, int, *APIError) {
	// v1 deliberately exposes no execution options here. Runtime profile,
	// adapter, budgets and verification boundaries are frozen by the existing
	// Task/Policy/Capability snapshots; accepting caller-selected knobs would
	// create a second policy surface.
	members, apiErr := strictObject(payload)
	if apiErr != nil {
		return nil, 0, apiErr
	}
	// encoding/json accepts the literal null into a nil map. The wire
	// contract is intentionally an empty object, not an absent value, so keep
	// that representation distinction fail closed.
	if members == nil {
		return nil, 0, apiError(CodeInvalidRequest, "malformed-json", "the document is not a JSON object")
	}
	before, err := s.store.Inspect(runID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, apiError(CodeNotFound, "run-not-found", "the Run does not exist")
		}
		return nil, 0, apiError(CodeRejected, "run-inspect-failed", "the Run state could not be inspected")
	}
	if before.State.Terminal() {
		return nil, 0, apiError(CodeInvalidState, "run-terminal", "the Run is already terminal")
	}
	// A crash can occur after execution durably commits worker.completed but
	// before the HTTP idempotency result is renamed into place. In that exact
	// window the authoritative Run is already VERIFYING. Reconstruct the
	// response from the journal/snapshot instead of invoking task run again;
	// the reconstructed response is then committed as this submission's
	// idempotency result. This is recovery from Core authority, not a second
	// controller state machine.
	if before.State == domain.StateVerifying && before.CurrentAttemptID != "" {
		return s.encodeRunExecution(before)
	}
	if s.runExecutor == nil {
		return nil, 0, apiError(CodeRejected, "run-executor-unavailable",
			"the server has no production Run executor configured")
	}
	if err := s.runExecutor(ctx, runID); err != nil {
		// The durable journal remains the authority for any partial progress.
		// Do not expose adapter/host paths or provider diagnostics through the
		// public protocol; callers inspect status/events and may safely retry a
		// request that never committed an idempotency result.
		return nil, 0, apiError(CodeRejected, "run-execution-failed", "the Run execution attempt failed")
	}
	after, err := s.store.Inspect(runID)
	if err != nil {
		return nil, 0, apiError(CodeRejected, "run-inspect-failed", "the Run state could not be inspected after execution")
	}
	if after.TaskID != before.TaskID || after.RunID != runID || after.Sequence <= before.Sequence || after.CurrentAttemptID == "" {
		return nil, 0, apiError(CodeRejected, "run-execution-not-observed",
			"the production executor returned without durable Run progress")
	}
	return s.encodeRunExecution(after)
}

func (s *Server) encodeRunExecution(state domain.RunState) (json.RawMessage, int, *APIError) {
	result := RunExecution{
		APIVersion:           domain.APIVersionV1Alpha1,
		Kind:                 "RunExecution",
		AuthorityNamespaceId: s.namespace,
		TaskID:               state.TaskID,
		RunID:                state.RunID,
		AttemptID:            state.CurrentAttemptID,
		State:                state,
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, 0, apiError(CodeInternal, "internal", "encode Run execution result")
	}
	return data, http.StatusAccepted, nil
}

func (s *Server) handleRunApproval(writer http.ResponseWriter, request *http.Request, identity requestIdentity, runID string) {
	if err := domain.ValidateID(runID); err != nil {
		writeError(writer, identity.RequestID, apiError(CodeInvalidRequest, "invalid-id", "the runId is not a valid Marshal ID"))
		return
	}
	body, apiErr := readMutationBody(writer, request)
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
		return s.executeRunApproval(ctx, runID, payload)
	}
	result, status, apiErr := s.submit(request.Context(), env, executor)
	if apiErr != nil {
		writeError(writer, identity.RequestID, apiErr)
		return
	}
	writeJSON(writer, status, result)
}

func (s *Server) executeRunApproval(ctx context.Context, runID string, payload json.RawMessage) (json.RawMessage, int, *APIError) {
	members, apiErr := strictObject(payload, "gate", "actor")
	if apiErr != nil {
		return nil, 0, apiErr
	}
	gate, apiErr := requiredString(members, "gate", 64)
	if apiErr != nil {
		return nil, 0, apiErr
	}
	if gate != domain.ApprovalGatePlan && gate != domain.ApprovalGatePublish {
		return nil, 0, apiError(CodeInvalidRequest, "invalid-gate", "the approval gate must be plan or publish")
	}
	actor, apiErr := requiredString(members, "actor", maxActorBytes)
	if apiErr != nil {
		return nil, 0, apiErr
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, apiError(CodeRejected, "request-cancelled", "the request was cancelled")
	}
	record, err := controlplane.Approve(controlplane.ApprovalInput{
		StateRoot: s.stateRoot,
		RunID:     runID,
		Gate:      gate,
		SourceID:  actor,
		Now:       s.now().UTC(),
		Validator: s.validator,
	})
	if err != nil {
		return nil, 0, mapApprovalError(err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		return nil, 0, apiError(CodeInternal, "internal", "encode approval record")
	}
	return data, http.StatusCreated, nil
}

func mapApprovalError(err error) *APIError {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return apiError(CodeNotFound, "run-not-found", "the Run does not exist")
	case errors.Is(err, controlplane.ErrInvalidApprovalState):
		return apiError(CodeInvalidState, "approval-gate-unavailable", "the approval gate is unavailable in the current state")
	case errors.Is(err, controlplane.ErrApprovalStale):
		return apiError(CodeInvalidState, "approval-stale", "the approval binding is stale")
	case errors.Is(err, controlplane.ErrApprovalNotRequired):
		return apiError(CodeRejected, "approval-not-required", "policy does not require this approval gate")
	case errors.Is(err, controlplane.ErrInvalidControlInput):
		return apiError(CodeRejected, "control-input-invalid", "the Run evidence is invalid for approval")
	case errors.Is(err, runstore.ErrLeaseHeld):
		return apiError(CodeInvalidState, "run-lease-held", "the Run lease is held by another writer")
	case errors.Is(err, runstore.ErrTruncatedTail):
		return apiError(CodeRejected, "journal-truncated", "the Run journal has a truncated final record")
	case errors.Is(err, runstore.ErrConflict):
		return apiError(CodeInvalidState, "journal-conflict", "the control journal rejected the approval record")
	case errors.Is(err, lifecycle.ErrInvalidTransition):
		return apiError(CodeInvalidState, "invalid-lifecycle-transition", "the lifecycle rejected the approval")
	default:
		return apiError(CodeRejected, "approval-rejected", "the approval was rejected")
	}
}

func (s *Server) handleRunStatus(writer http.ResponseWriter, request *http.Request, identity requestIdentity, runID string) {
	if apiErr := readGetBody(request); apiErr != nil {
		writeError(writer, identity.RequestID, apiErr)
		return
	}
	if err := domain.ValidateID(runID); err != nil {
		writeError(writer, identity.RequestID, apiError(CodeInvalidRequest, "invalid-id", "the runId is not a valid Marshal ID"))
		return
	}
	state, err := s.store.Inspect(runID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(writer, identity.RequestID, apiError(CodeNotFound, "run-not-found", "the Run does not exist"))
			return
		}
		if errors.Is(err, runstore.ErrConflict) {
			writeError(writer, identity.RequestID, apiError(CodeRejected, "run-state-conflict", "the Run snapshot conflicts with its journal"))
			return
		}
		writeError(writer, identity.RequestID, apiError(CodeRejected, "run-inspect-failed", "the Run state could not be inspected"))
		return
	}
	data, err := json.Marshal(state)
	if err != nil {
		writeError(writer, identity.RequestID, apiError(CodeInternal, "internal", "encode run state"))
		return
	}
	writeJSON(writer, http.StatusOK, data)
}

func writeJSON(writer http.ResponseWriter, status int, data json.RawMessage) {
	writer.WriteHeader(status)
	_, _ = writer.Write(append(data, '\n'))
}

func writeError(writer http.ResponseWriter, requestID string, apiErr *APIError) {
	status := apiErr.Status
	if status == 0 {
		status = apiErr.Code.status()
	}
	body := ErrorBody{
		APIVersion: domain.APIVersionV1Alpha1,
		Kind:       "Error",
		Code:       apiErr.Code,
		Reason:     apiErr.Reason,
		Message:    apiErr.Message,
		RequestID:  requestID,
	}
	data, err := json.Marshal(body)
	if err != nil {
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}
	writer.WriteHeader(status)
	_, _ = writer.Write(append(data, '\n'))
}
