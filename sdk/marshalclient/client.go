// Package marshalclient is the typed Go client of the Marshal Public API.
//
// The client follows the frozen contract internal/server/openapi.json of the
// resident marshal-server public-api Port (ADR 0018 §1/§3/§16): versioned
// HTTP/JSON endpoints for Task create/get/cancel, Run approval/status, the
// SSE event projection and its polling fallback. It uses only the Go
// standard library.
package marshalclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// Frozen protocol family identity of the public-api Port, mirrored from
// internal/server/openapi.json. Every request binds the protocol version,
// the audience and the authority scope in the versioned identity headers.
const (
	ProtocolFamily          = "marshal-public-api"
	ProtocolVersionV1Alpha1 = "v1alpha1"
	DefaultProtocolVersion  = ProtocolFamily + "/" + ProtocolVersionV1Alpha1
	DefaultAudience         = "marshal-public-api"
	APIPrefix               = "/v1alpha1"
)

// Header names of the versioned identity envelope frozen by openapi.json.
const (
	HeaderRequestID       = "Marshal-Request-Id"
	HeaderProtocolVersion = "Marshal-Protocol-Version"
	HeaderPrincipal       = "Marshal-Principal"
	HeaderAudience        = "Marshal-Audience"
	HeaderScope           = "Marshal-Scope"
	HeaderDeadline        = "Marshal-Deadline"
)

const (
	// DefaultRequestTimeout is the default Marshal-Deadline horizon of one
	// request when neither Config.RequestTimeout nor the request context
	// bound it earlier.
	DefaultRequestTimeout = 30 * time.Second

	// maxResponseBytes bounds one response body read by the client.
	maxResponseBytes int64 = 8 << 20

	// maxIdempotencyKeyBytes is the frozen size limit of idempotencyKey.
	maxIdempotencyKeyBytes = 512
)

// marshalIDPattern is the frozen Marshal ID pattern of openapi.json.
var marshalIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,159}$`)

// Config assembles one Public API client. Credential material (principal,
// audience, scope) is injected here once and never appears in error
// messages produced by the client.
type Config struct {
	// BaseURL is the absolute URL of the marshal-server public-api Port,
	// for example http://127.0.0.1:7718.
	BaseURL string
	// Principal is the AuthN principal carried by Marshal-Principal.
	Principal string
	// Scope is the authority scope carried by Marshal-Scope; it must equal
	// the authorityScopeId of the serving authority namespace.
	Scope string
	// Audience overrides the Marshal-Audience value; empty selects
	// DefaultAudience.
	Audience string
	// ProtocolVersion overrides the Marshal-Protocol-Version value; empty
	// selects DefaultProtocolVersion.
	ProtocolVersion string
	// RequestTimeout bounds one request's Marshal-Deadline horizon; zero or
	// negative selects DefaultRequestTimeout. An earlier context deadline
	// always wins.
	RequestTimeout time.Duration
	// HTTPClient performs the requests; nil selects a bare http.Client
	// without a global timeout (SSE streams are long-lived).
	HTTPClient *http.Client
	// NewRequestID generates one Marshal-Request-Id per request; nil
	// selects a crypto/rand based generator.
	NewRequestID func() string
}

// Client is the typed Public API client. It is safe for concurrent use; it
// never writes any store directly — every operation goes through the
// versioned HTTP API.
type Client struct {
	baseURL         string
	principal       string
	scope           string
	audience        string
	protocolVersion string
	requestTimeout  time.Duration
	httpClient      *http.Client
	newRequestID    func() string
}

// New assembles one Client from Config, failing closed on missing identity.
func New(config Config) (*Client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		return nil, errors.New("marshalclient: Config.BaseURL is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("marshalclient: Config.BaseURL must be an absolute URL")
	}
	principal := strings.TrimSpace(config.Principal)
	if principal == "" {
		return nil, errors.New("marshalclient: Config.Principal is required")
	}
	scope := strings.TrimSpace(config.Scope)
	if scope == "" {
		return nil, errors.New("marshalclient: Config.Scope is required")
	}
	audience := strings.TrimSpace(config.Audience)
	if audience == "" {
		audience = DefaultAudience
	}
	protocolVersion := strings.TrimSpace(config.ProtocolVersion)
	if protocolVersion == "" {
		protocolVersion = DefaultProtocolVersion
	}
	requestTimeout := config.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = DefaultRequestTimeout
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	newRequestID := config.NewRequestID
	if newRequestID == nil {
		newRequestID = newRandomRequestID
	}
	return &Client{
		baseURL:         baseURL,
		principal:       principal,
		scope:           scope,
		audience:        audience,
		protocolVersion: protocolVersion,
		requestTimeout:  requestTimeout,
		httpClient:      httpClient,
		newRequestID:    newRequestID,
	}, nil
}

// newRandomRequestID generates one client-chosen request identity.
func newRandomRequestID() string {
	var buffer [16]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return "req-" + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return "req-" + hex.EncodeToString(buffer[:])
}

// ---------------------------------------------------------------------------
// Frozen request/response types of openapi.json
// ---------------------------------------------------------------------------

// RunLifecycleState is the frozen Run lifecycle state vocabulary.
type RunLifecycleState string

// ApprovalGate is one frozen human approval gate of a Run.
type ApprovalGate string

// Frozen approval gates.
const (
	GatePlan    ApprovalGate = "plan"
	GatePublish ApprovalGate = "publish"
)

// AuthorityNamespaceId is the composite authority-side key space owning the
// idempotent submission records.
type AuthorityNamespaceId struct {
	TenantNamespace  string `json:"tenantNamespace"`
	ControlPlaneId   string `json:"controlPlaneId"`
	AuthorityScopeId string `json:"authorityScopeId"`
}

// TaskSubmission is the frozen result of createTask (HTTP 201 for the first
// accepted submission, HTTP 200 for an idempotent replay merge).
type TaskSubmission struct {
	APIVersion           string               `json:"apiVersion"`
	Kind                 string               `json:"kind"`
	AuthorityNamespaceId AuthorityNamespaceId `json:"authorityNamespaceId"`
	TaskID               string               `json:"taskId"`
	RunID                string               `json:"runId"`
	AdapterID            string               `json:"adapterId"`
	State                RunState             `json:"state"`

	// Replayed reports that the submission merged into the stored result of
	// an earlier accepted submission (HTTP 200 instead of HTTP 201). It is
	// client-side metadata and never part of the wire document.
	Replayed bool `json:"-"`
}

// TaskView is the read projection of one Task and its Runs.
type TaskView struct {
	APIVersion  string       `json:"apiVersion"`
	Kind        string       `json:"kind"`
	TaskID      string       `json:"taskId"`
	Title       string       `json:"title,omitempty"`
	LatestRunID string       `json:"latestRunId"`
	Runs        []RunSummary `json:"runs"`
}

// RunSummary is one Run of a TaskView projection.
type RunSummary struct {
	RunID          string            `json:"runId"`
	State          RunLifecycleState `json:"state"`
	Sequence       uint64            `json:"sequence"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
	TerminalReason string            `json:"terminalReason,omitempty"`
}

// TaskCancellation is the frozen result of cancelTask.
type TaskCancellation struct {
	APIVersion           string               `json:"apiVersion"`
	Kind                 string               `json:"kind"`
	AuthorityNamespaceId AuthorityNamespaceId `json:"authorityNamespaceId"`
	TaskID               string               `json:"taskId"`
	RunID                string               `json:"runId"`
	State                RunLifecycleState    `json:"state"`
	TerminalReason       string               `json:"terminalReason"`
	Actor                string               `json:"actor"`
	Sequence             uint64               `json:"sequence"`
}

// ApprovalRecord is the frozen result of approveRun (HTTP 201 for the first
// accepted submission, HTTP 200 for an idempotent replay merge).
type ApprovalRecord struct {
	APIVersion      string          `json:"apiVersion"`
	Kind            string          `json:"kind"`
	RecordID        string          `json:"recordId"`
	TaskID          string          `json:"taskId"`
	RunID           string          `json:"runId"`
	ControlSequence uint64          `json:"controlSequence"`
	Gate            ApprovalGate    `json:"gate"`
	Source          ControlSource   `json:"source"`
	Binding         ApprovalBinding `json:"binding"`
	Outcome         string          `json:"outcome"`
	CreatedAt       time.Time       `json:"createdAt"`

	// Replayed reports that the submission merged into the stored
	// ApprovalRecord of an earlier accepted submission (HTTP 200 instead of
	// HTTP 201). It is client-side metadata and never part of the wire
	// document.
	Replayed bool `json:"-"`
}

// ControlSource is the source of one control record.
type ControlSource struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// ApprovalBinding is the frozen input/evidence binding of one
// ApprovalRecord.
type ApprovalBinding struct {
	StateSequence    uint64 `json:"stateSequence"`
	SpecDigest       string `json:"specDigest"`
	PolicyDigest     string `json:"policyDigest"`
	CapabilityDigest string `json:"capabilityDigest"`
	BaseSHA          string `json:"baseSha"`
	ReviewRound      uint64 `json:"reviewRound,omitempty"`
	DecisionDigest   string `json:"decisionDigest,omitempty"`
	EvidenceDigest   string `json:"evidenceDigest,omitempty"`
}

// RunState is the durable RunState of one Run.
type RunState struct {
	APIVersion             string            `json:"apiVersion"`
	Kind                   string            `json:"kind"`
	TaskID                 string            `json:"taskId"`
	RunID                  string            `json:"runId"`
	State                  RunLifecycleState `json:"state"`
	Sequence               uint64            `json:"sequence"`
	SpecDigest             string            `json:"specDigest,omitempty"`
	PolicyDigest           string            `json:"policyDigest,omitempty"`
	CapabilityDigest       string            `json:"capabilityDigest,omitempty"`
	BaseSHA                string            `json:"baseSha,omitempty"`
	WorktreePath           string            `json:"worktreePath,omitempty"`
	Publication            *RunPublication   `json:"publication,omitempty"`
	CurrentAttemptID       string            `json:"currentAttemptId,omitempty"`
	ReviewRound            uint64            `json:"reviewRound"`
	AttemptsUsed           uint64            `json:"attemptsUsed"`
	OperationalRetriesUsed uint64            `json:"operationalRetriesUsed"`
	ReworkRoundsUsed       uint64            `json:"reworkRoundsUsed"`
	TerminalReason         string            `json:"terminalReason,omitempty"`
	CreatedAt              time.Time         `json:"createdAt"`
	UpdatedAt              time.Time         `json:"updatedAt"`
}

// RunPublication is the publication binding of one RunState, when present.
type RunPublication struct {
	Provider   string `json:"provider"`
	Repository string `json:"repository"`
	HeadBranch string `json:"headBranch"`
	BaseBranch string `json:"baseBranch"`
	ExternalID string `json:"externalId,omitempty"`
	URI        string `json:"uri,omitempty"`
	HeadSHA    string `json:"headSha,omitempty"`
}

// EventProjection is the read-only projection of one Run ledger event. One
// constructor feeds both the SSE data frames and the polling fallback on the
// server, so the JSON is field-identical across the two channels.
type EventProjection struct {
	APIVersion           string               `json:"apiVersion"`
	Kind                 string               `json:"kind"`
	AuthorityNamespaceId AuthorityNamespaceId `json:"authorityNamespaceId"`
	Scope                string               `json:"scope"`
	EventID              string               `json:"eventId"`
	LedgerSequence       uint64               `json:"ledgerSequence"`
	RunID                string               `json:"runId"`
	RunSequence          uint64               `json:"runSequence"`
	TaskID               string               `json:"taskId,omitempty"`
	AttemptID            string               `json:"attemptId,omitempty"`
	Type                 string               `json:"type"`
	StateFrom            RunLifecycleState    `json:"stateFrom,omitempty"`
	StateTo              RunLifecycleState    `json:"stateTo,omitempty"`
	Timestamp            time.Time            `json:"timestamp"`
	PayloadDigest        string               `json:"payloadDigest"`
}

// EventPage is one polling-fallback page of the event projection.
type EventPage struct {
	APIVersion           string               `json:"apiVersion"`
	Kind                 string               `json:"kind"`
	AuthorityNamespaceId AuthorityNamespaceId `json:"authorityNamespaceId"`
	Scope                string               `json:"scope"`
	Events               []EventProjection    `json:"events"`
	NextCursor           uint64               `json:"nextCursor"`
	SnapshotDigest       string               `json:"snapshotDigest"`
}

// EventResync is the deterministic resync directive: the cursor is expired,
// gap or unservable, and the subscription must be rebuilt from
// StartSequence against the SnapshotDigest.
type EventResync struct {
	APIVersion           string               `json:"apiVersion"`
	Kind                 string               `json:"kind"`
	AuthorityNamespaceId AuthorityNamespaceId `json:"authorityNamespaceId"`
	Scope                string               `json:"scope"`
	Reason               string               `json:"reason"`
	StartSequence        uint64               `json:"startSequence"`
	SnapshotDigest       string               `json:"snapshotDigest"`
}

// ---------------------------------------------------------------------------
// Frozen request payloads of openapi.json
// ---------------------------------------------------------------------------

// TaskCreateRequest is one idempotent createTask submission.
type TaskCreateRequest struct {
	// IdempotencyKey is the client-chosen submission identity member.
	IdempotencyKey string
	// Payload is the createTask payload whose RFC 8785 canonical digest
	// becomes the envelope requestDigest.
	Payload TaskCreatePayload
}

// TaskCreatePayload is the createTask payload document.
type TaskCreatePayload struct {
	RunID          string          `json:"runId"`
	TaskSpec       json.RawMessage `json:"taskSpec"`
	PolicySnapshot json.RawMessage `json:"policySnapshot"`
}

// TaskCancelRequest is one idempotent cancelTask submission.
type TaskCancelRequest struct {
	IdempotencyKey string
	Payload        TaskCancelPayload
}

// TaskCancelPayload is the cancelTask payload document. Without RunID the
// unique non-terminal Run of the Task is selected.
type TaskCancelPayload struct {
	Actor  string `json:"actor"`
	Reason string `json:"reason"`
	RunID  string `json:"runId,omitempty"`
}

// RunApprovalRequest is one idempotent approveRun submission.
type RunApprovalRequest struct {
	IdempotencyKey string
	Payload        RunApprovalPayload
}

// RunApprovalPayload is the approveRun payload document.
type RunApprovalPayload struct {
	Gate  ApprovalGate `json:"gate"`
	Actor string       `json:"actor"`
}

// ---------------------------------------------------------------------------
// Endpoint methods
// ---------------------------------------------------------------------------

// CreateTask submits one Task with its first Run (POST /v1alpha1/tasks).
// The identical (authorityNamespaceId, scope, idempotencyKey, requestDigest)
// quadruple merges into the stored result (Replayed reports the merge); the
// identical key with a different requestDigest conflicts fail closed.
func (c *Client) CreateTask(ctx context.Context, request TaskCreateRequest) (*TaskSubmission, error) {
	if strings.TrimSpace(request.Payload.RunID) == "" {
		return nil, errors.New("marshalclient: TaskCreatePayload.RunID is required")
	}
	if len(request.Payload.TaskSpec) == 0 || len(request.Payload.PolicySnapshot) == 0 {
		return nil, errors.New("marshalclient: TaskCreatePayload requires TaskSpec and PolicySnapshot")
	}
	body, err := buildEnvelope(request.IdempotencyKey, request.Payload)
	if err != nil {
		return nil, err
	}
	data, status, err := c.do(ctx, http.MethodPost, APIPrefix+"/tasks", nil, body)
	if err != nil {
		return nil, err
	}
	var submission TaskSubmission
	if err := decodeJSON(data, &submission); err != nil {
		return nil, err
	}
	submission.Replayed = status == http.StatusOK
	return &submission, nil
}

// GetTask reads the projection of one Task and its Runs
// (GET /v1alpha1/tasks/{taskId}).
func (c *Client) GetTask(ctx context.Context, taskID string) (*TaskView, error) {
	if err := validateMarshalID(taskID); err != nil {
		return nil, err
	}
	data, _, err := c.do(ctx, http.MethodGet, APIPrefix+"/tasks/"+taskID, nil, nil)
	if err != nil {
		return nil, err
	}
	var view TaskView
	if err := decodeJSON(data, &view); err != nil {
		return nil, err
	}
	return &view, nil
}

// CancelTask cancels one Task's Run through the frozen explicit-abort path
// (POST /v1alpha1/tasks/{taskId}/cancel).
func (c *Client) CancelTask(ctx context.Context, taskID string, request TaskCancelRequest) (*TaskCancellation, error) {
	if err := validateMarshalID(taskID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.Payload.Actor) == "" || strings.TrimSpace(request.Payload.Reason) == "" {
		return nil, errors.New("marshalclient: TaskCancelPayload requires Actor and Reason")
	}
	if request.Payload.RunID != "" {
		if err := validateMarshalID(request.Payload.RunID); err != nil {
			return nil, err
		}
	}
	body, err := buildEnvelope(request.IdempotencyKey, request.Payload)
	if err != nil {
		return nil, err
	}
	data, _, err := c.do(ctx, http.MethodPost, APIPrefix+"/tasks/"+taskID+"/cancel", nil, body)
	if err != nil {
		return nil, err
	}
	var cancellation TaskCancellation
	if err := decodeJSON(data, &cancellation); err != nil {
		return nil, err
	}
	return &cancellation, nil
}

// ApproveRun appends one human ApprovalRecord to a Run gate
// (POST /v1alpha1/runs/{runId}/approval). Replayed reports an idempotent
// replay merge (HTTP 200 instead of HTTP 201).
func (c *Client) ApproveRun(ctx context.Context, runID string, request RunApprovalRequest) (*ApprovalRecord, error) {
	if err := validateMarshalID(runID); err != nil {
		return nil, err
	}
	if request.Payload.Gate != GatePlan && request.Payload.Gate != GatePublish {
		return nil, errors.New("marshalclient: RunApprovalPayload.Gate must be plan or publish")
	}
	if strings.TrimSpace(request.Payload.Actor) == "" {
		return nil, errors.New("marshalclient: RunApprovalPayload.Actor is required")
	}
	body, err := buildEnvelope(request.IdempotencyKey, request.Payload)
	if err != nil {
		return nil, err
	}
	data, status, err := c.do(ctx, http.MethodPost, APIPrefix+"/runs/"+runID+"/approval", nil, body)
	if err != nil {
		return nil, err
	}
	var record ApprovalRecord
	if err := decodeJSON(data, &record); err != nil {
		return nil, err
	}
	record.Replayed = status == http.StatusOK
	return &record, nil
}

// GetRunStatus reads the durable RunState of one Run
// (GET /v1alpha1/runs/{runId}/status).
func (c *Client) GetRunStatus(ctx context.Context, runID string) (*RunState, error) {
	if err := validateMarshalID(runID); err != nil {
		return nil, err
	}
	data, _, err := c.do(ctx, http.MethodGet, APIPrefix+"/runs/"+runID+"/status", nil, nil)
	if err != nil {
		return nil, err
	}
	var state RunState
	if err := decodeJSON(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// PollEvents reads one page of the read-only event projection
// (GET /v1alpha1/events/poll), the fallback channel of the SSE stream.
// Cursor is the last consumed ledgerSequence (exclusive lower bound); zero
// reads from ledgerSequence 1. A limit of zero leaves the server default.
// An expired, gap or unservable cursor yields a typed ResyncRequiredError.
func (c *Client) PollEvents(ctx context.Context, cursor uint64, limit int) (*EventPage, error) {
	query := url.Values{}
	if cursor > 0 {
		query.Set("cursor", strconv.FormatUint(cursor, 10))
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	data, _, err := c.do(ctx, http.MethodGet, APIPrefix+"/events/poll", query, nil)
	if err != nil {
		return nil, err
	}
	var page EventPage
	if err := decodeJSON(data, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

// ---------------------------------------------------------------------------
// Transport plumbing
// ---------------------------------------------------------------------------

// do executes one API request and reads the complete response body. 2xx
// responses return the body and status; every other status is mapped to its
// typed error.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body []byte) ([]byte, int, error) {
	response, err := c.execute(ctx, method, path, query, body, "application/json", nil)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return nil, 0, &TransportError{Op: "read response", Err: err}
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return data, response.StatusCode, nil
	}
	return nil, 0, mapHTTPError(response.StatusCode, data)
}

// execute builds and sends one versioned request carrying the complete
// frozen identity envelope (protocol version, principal, audience, scope,
// request id and deadline).
func (c *Client) execute(ctx context.Context, method, path string, query url.Values, body []byte, accept string, extraHeaders map[string]string) (*http.Response, error) {
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, &TransportError{Op: "build request", Err: err}
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", accept)
	request.Header.Set(HeaderRequestID, c.newRequestID())
	request.Header.Set(HeaderProtocolVersion, c.protocolVersion)
	request.Header.Set(HeaderPrincipal, c.principal)
	request.Header.Set(HeaderAudience, c.audience)
	request.Header.Set(HeaderScope, c.scope)
	request.Header.Set(HeaderDeadline, c.deadline(ctx).UTC().Format(time.RFC3339))
	for name, value := range extraHeaders {
		request.Header.Set(name, value)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, &TransportError{Op: "execute request", Err: err}
	}
	return response, nil
}

// deadline derives the Marshal-Deadline of one request: the earlier of the
// request context deadline and the configured horizon.
func (c *Client) deadline(ctx context.Context) time.Time {
	horizon := time.Now().UTC().Add(c.requestTimeout)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(horizon) {
		return deadline
	}
	return horizon
}

// decodeJSON decodes one success response body, failing closed on malformed
// documents.
func decodeJSON(data []byte, out any) error {
	if err := json.Unmarshal(data, out); err != nil {
		return &ProtocolViolationError{Detail: "undecodable success response: " + err.Error()}
	}
	return nil
}

// validateMarshalID fails closed on path identifiers outside the frozen
// Marshal ID pattern, before any request leaves the process.
func validateMarshalID(id string) error {
	if !marshalIDPattern.MatchString(id) {
		return fmt.Errorf("%w: %s", ErrInvalidID, id)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Idempotent submission envelope
// ---------------------------------------------------------------------------

// buildEnvelope renders the frozen idempotent submission envelope shared by
// every mutating endpoint: idempotencyKey plus the RFC 8785 canonical
// requestDigest of the payload plus the payload itself.
func buildEnvelope(idempotencyKey string, payload any) ([]byte, error) {
	key := strings.TrimSpace(idempotencyKey)
	if key == "" {
		return nil, errors.New("marshalclient: idempotencyKey is required")
	}
	if len(key) > maxIdempotencyKeyBytes {
		return nil, errors.New("marshalclient: idempotencyKey exceeds 512 bytes")
	}
	payloadRaw, err := json.Marshal(payload)
	if err != nil {
		return nil, ErrPayloadRejected
	}
	digest, err := RequestDigest(payloadRaw)
	if err != nil {
		return nil, err
	}
	envelope := struct {
		IdempotencyKey string          `json:"idempotencyKey"`
		RequestDigest  string          `json:"requestDigest"`
		Payload        json.RawMessage `json:"payload"`
	}{IdempotencyKey: key, RequestDigest: digest, Payload: payloadRaw}
	return json.Marshal(envelope)
}

// RequestDigest returns the "sha256:"-prefixed RFC 8785 canonical digest of
// one payload document — the requestDigest member of the idempotent
// submission envelope. Any input that cannot be canonicalized yields
// ErrPayloadRejected with fixed error text that never echoes the input.
func RequestDigest(payload []byte) (string, error) {
	canonicalized, err := canonicalizeJSON(payload)
	if err != nil {
		return "", ErrPayloadRejected
	}
	sum := sha256.Sum256(canonicalized)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// ---------------------------------------------------------------------------
// RFC 8785 JSON Canonicalization Scheme (stdlib-only)
// ---------------------------------------------------------------------------

// canonicalizeJSON renders one JSON value in RFC 8785 canonical form:
// sorted object members (UTF-16 code unit order), no insignificant
// whitespace, ES6 number serialization and minimal string escaping. The
// server validates requestDigest against the identical canonicalization, so
// the two implementations must agree byte for byte.
func canonicalizeJSON(input []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	canonical, err := canonicalValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("payload must be exactly one JSON value")
	}
	return canonical, nil
}

// canonicalValue renders the next JSON value of the decoder in canonical
// form.
func canonicalValue(decoder *json.Decoder) ([]byte, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			return canonicalObject(decoder)
		case '[':
			return canonicalArray(decoder)
		}
		return nil, errors.New("unexpected delimiter")
	case nil:
		return []byte("null"), nil
	case bool:
		if value {
			return []byte("true"), nil
		}
		return []byte("false"), nil
	case string:
		return appendString(nil, value), nil
	case json.Number:
		return canonicalNumber(value)
	}
	return nil, errors.New("unsupported JSON token")
}

// canonicalObject renders one JSON object with members sorted by UTF-16
// code unit order, rejecting duplicate member names exactly like the
// server's canonicalization.
func canonicalObject(decoder *json.Decoder) ([]byte, error) {
	type member struct {
		key string
		raw json.RawMessage
	}
	var members []member
	seen := map[string]struct{}{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("object key is not a string")
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, errors.New("duplicate object member")
		}
		seen[key] = struct{}{}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		members = append(members, member{key: key, raw: raw})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	sort.Slice(members, func(i, j int) bool {
		return utf16Less(members[i].key, members[j].key)
	})
	var out []byte
	out = append(out, '{')
	for index, entry := range members {
		if index > 0 {
			out = append(out, ',')
		}
		out = appendString(out, entry.key)
		out = append(out, ':')
		value, err := canonicalizeJSON(entry.raw)
		if err != nil {
			return nil, err
		}
		out = append(out, value...)
	}
	out = append(out, '}')
	return out, nil
}

// canonicalArray renders one JSON array preserving element order.
func canonicalArray(decoder *json.Decoder) ([]byte, error) {
	var out []byte
	out = append(out, '[')
	first := true
	for decoder.More() {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		if !first {
			out = append(out, ',')
		}
		first = false
		value, err := canonicalizeJSON(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, value...)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	out = append(out, ']')
	return out, nil
}

// canonicalNumber renders one JSON number per RFC 8785: every number is an
// IEEE 754 double serialized by the ECMAScript Number-to-String algorithm.
func canonicalNumber(number json.Number) ([]byte, error) {
	value, err := number.Float64()
	if err != nil {
		return nil, errors.New("number is not representable")
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, errors.New("NaN and Infinity are not allowed by RFC 8785")
	}
	return []byte(es6Number(value)), nil
}

// es6Number serializes one finite float64 per the ECMAScript
// Number-to-String algorithm: integers below 1e21 without exponent, fixed
// notation between 1e-6 and 1e21, and shortest-digit exponent notation
// beyond, with a signed exponent carrying no leading zeros.
func es6Number(value float64) string {
	if value == 0 {
		// Both +0 and -0 serialize as "0".
		return "0"
	}
	sign := ""
	if value < 0 {
		sign = "-"
		value = -value
	}
	scientific := strconv.FormatFloat(value, 'e', -1, 64)
	exponentIndex := strings.IndexByte(scientific, 'e')
	mantissa := scientific[:exponentIndex]
	exponent, _ := strconv.Atoi(scientific[exponentIndex+1:])
	digits := strings.ReplaceAll(mantissa, ".", "")
	// n positions the decimal point: value = digits * 10^(n-k), k = len(digits).
	n := exponent + 1
	k := len(digits)
	var rendered string
	switch {
	case k <= n && n <= 21:
		rendered = digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		rendered = digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		rendered = "0." + strings.Repeat("0", -n) + digits
	case k == 1:
		rendered = digits + "e" + es6Exponent(n-1)
	default:
		rendered = digits[:1] + "." + digits[1:] + "e" + es6Exponent(n-1)
	}
	return sign + rendered
}

// es6Exponent renders the signed exponent without leading zeros.
func es6Exponent(exponent int) string {
	if exponent >= 0 {
		return "+" + strconv.Itoa(exponent)
	}
	return "-" + strconv.Itoa(-exponent)
}

// appendString renders one JSON string with RFC 8785 minimal escaping: the
// short escapes for \b \f \n \r \t, \" and \\, lowercase \u00xx for the
// remaining control characters, and unescaped UTF-8 everywhere else.
func appendString(out []byte, value string) []byte {
	out = append(out, '"')
	for _, r := range value {
		switch r {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\b':
			out = append(out, '\\', 'b')
		case '\f':
			out = append(out, '\\', 'f')
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case '\t':
			out = append(out, '\\', 't')
		default:
			if r < 0x20 {
				out = append(out, fmt.Sprintf("\\u%04x", r)...)
			} else {
				out = utf8.AppendRune(out, r)
			}
		}
	}
	return append(out, '"')
}

// utf16Less orders two strings by UTF-16 code units, the RFC 8785 member
// ordering.
func utf16Less(a, b string) bool {
	unitsA := utf16.Encode([]rune(a))
	unitsB := utf16.Encode([]rune(b))
	for index := 0; index < len(unitsA) && index < len(unitsB); index++ {
		if unitsA[index] != unitsB[index] {
			return unitsA[index] < unitsB[index]
		}
	}
	return len(unitsA) < len(unitsB)
}
