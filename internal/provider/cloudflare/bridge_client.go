package cloudflare

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// Wire contract status: the endpoint family below mirrors the official
// Bridge operation set recorded in docs/m10-cloud-deployment-research.md
// §2 (create / running / exec SSE / file / persist / hydrate / destroy),
// but the exact paths, payload shapes and version identifiers frozen here
// are the M10-a fixture contract. Every one of them is subject to the M10-b
// online re-verification of the official Bridge OpenAPI; drift discovered
// there is fail-closed material, not a compatibility negotiation. Platform
// quota numbers are deliberately NOT expressed as constants here (research
// doc §6.3): they were not re-verified online in M10-a.

// DefaultProtocolVersion is the only Bridge protocol version this client
// serves; any other advertised version fails closed.
const DefaultProtocolVersion = "v1"

const (
	defaultMaxRetries     = 2
	defaultRetryDelay     = 5 * time.Millisecond
	defaultRequestTimeout = 10 * time.Second

	// maxResponseBytes bounds one non-streaming Bridge response body. It
	// mirrors the Marshal stage-request budget (internal/sandbox
	// MaxStageRequestBytes), never a Cloudflare platform quota.
	maxResponseBytes int64 = 16 << 20

	// maxStreamCaptureBytes bounds each captured exec output stream, in the
	// spirit of internal/sandbox/local's bounded capture; it is a Marshal
	// bounded-capture budget, never a Cloudflare platform quota.
	maxStreamCaptureBytes int64 = 1 << 20

	redactedCredential = "[redacted:bridge-credential]"
)

// Endpoint paths of the M10-a fixture wire contract.
const (
	healthPath    = "/v1/health"
	sandboxesPath = "/v1/sandboxes"
)

var (
	// ErrInvalidClientConfig rejects a malformed client configuration fail
	// closed.
	ErrInvalidClientConfig = errors.New("cloudflare bridge: invalid client configuration")
	// ErrCredentialMissing fails closed when no Bridge transport credential
	// is configured.
	ErrCredentialMissing = errors.New("cloudflare bridge: the Bridge Bearer credential is missing")
	// ErrCredentialRejected is returned when the Bridge endpoint rejects the
	// transport credential (HTTP 401/403). It is never retried.
	ErrCredentialRejected = errors.New("cloudflare bridge: the endpoint rejected the transport credential")
	// ErrProtocolVersionMismatch fails closed when the Bridge endpoint
	// advertises a protocol version other than the one the client requires.
	ErrProtocolVersionMismatch = errors.New("cloudflare bridge: protocol version mismatch")
	// ErrBridgeUnavailable is returned after the bounded retry budget is
	// exhausted against transient failures (5xx or transport errors).
	ErrBridgeUnavailable = errors.New("cloudflare bridge: endpoint unavailable")
	// ErrInvalidBridgeResponse rejects a response body that fails canonical
	// JSON admission (including duplicate object members) or that cannot be
	// decoded into the expected wire shape.
	ErrInvalidBridgeResponse = errors.New("cloudflare bridge: response rejected")
	// ErrSandboxNotFound maps a Bridge 404 for an unknown sandbox.
	ErrSandboxNotFound = errors.New("cloudflare bridge: sandbox not found")
	// ErrCheckpointNotFound maps a Bridge 404 for an unknown checkpoint.
	ErrCheckpointNotFound = errors.New("cloudflare bridge: checkpoint not found")
	// ErrBridgeLocatorUnresolved maps a Bridge 404 reporting that a staged
	// locator could not be resolved by the container.
	ErrBridgeLocatorUnresolved = errors.New("cloudflare bridge: locator content is not resolvable")
	// ErrContainerLost maps a Bridge 410: the container's file and process
	// state was lost (hibernation/fault/restart), fail closed.
	ErrContainerLost = errors.New("cloudflare bridge: container state lost")
	// ErrBridgeConflict maps a Bridge 409 duplicate-sandbox observation.
	ErrBridgeConflict = errors.New("cloudflare bridge: sandbox conflict")
	// ErrDigestMismatch maps the Bridge's fail-closed pre-consumption digest
	// check (HTTP 422 code digest-mismatch).
	ErrDigestMismatch = errors.New("cloudflare bridge: digest mismatch before consumption")
	// ErrCapacityExhausted maps Bridge capacity refusals (HTTP 429/507);
	// they are never retried and never downgraded.
	ErrCapacityExhausted = errors.New("cloudflare bridge: capacity exhausted")
	// ErrBridgeRejected maps any other client-side Bridge refusal (4xx).
	ErrBridgeRejected = errors.New("cloudflare bridge: request rejected")
)

// Credential holds the Bridge Bearer transport credential. It is a
// transport-layer secret only: it never substitutes for the fencingToken (a
// non-credential stale-write guard), never enters business JSON, events,
// logs, digests or error messages, and String() always redacts it.
type Credential struct {
	token string
}

// NewCredential fails closed on an empty or blank token.
func NewCredential(token string) (Credential, error) {
	if strings.TrimSpace(token) == "" {
		return Credential{}, ErrCredentialMissing
	}
	return Credential{token: token}, nil
}

// String always returns the fixed redaction marker, so the credential can
// never surface through logging or formatting.
func (c Credential) String() string { return redactedCredential }

// BridgeError is one structured refusal observed on the Bridge wire. Its
// message is scrubbed of the credential before it can surface.
type BridgeError struct {
	Status   int
	Code     string
	Message  string
	sentinel error
}

// Error implements the error interface.
func (e *BridgeError) Error() string {
	message := e.Message
	if message == "" {
		message = "no detail"
	}
	return fmt.Sprintf("cloudflare bridge: endpoint refused the request: status=%d code=%s message=%s", e.Status, e.Code, message)
}

// Unwrap exposes the classification sentinel for errors.Is adjudication.
func (e *BridgeError) Unwrap() error { return e.sentinel }

// ClientConfig configures one Bridge client. Zero numeric values take the
// frozen defaults; negative MaxRetries disables retries and a negative
// RetryDelay disables the delay between attempts (deterministic tests).
type ClientConfig struct {
	BaseURL         string
	Credential      Credential
	ProtocolVersion string
	HTTPClient      *http.Client
	MaxRetries      int
	RetryDelay      time.Duration
	RequestTimeout  time.Duration
}

// Client is the versioned HTTP/JSON client of the Bridge endpoint family.
// The Bearer credential lives only inside the client and only ever travels
// as the Authorization header of one transport request.
type Client struct {
	baseURL         string
	credential      Credential
	protocolVersion string
	httpClient      *http.Client
	maxRetries      int
	retryDelay      time.Duration
	requestTimeout  time.Duration
}

// NewClient validates the configuration fail closed and constructs the
// client. A credential whose literal appears inside the base URL is
// rejected outright: no configuration may place the transport credential on
// a path that business errors or logs could echo.
func NewClient(config ClientConfig) (*Client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("%w: the bridge base URL must be a non-empty string", ErrInvalidClientConfig)
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("%w: the bridge base URL must be an absolute http(s) URL", ErrInvalidClientConfig)
	}
	if config.Credential.token == "" {
		return nil, ErrCredentialMissing
	}
	if strings.Contains(baseURL, config.Credential.token) {
		return nil, fmt.Errorf("%w: the transport credential must never appear inside the bridge base URL", ErrInvalidClientConfig)
	}
	protocolVersion := config.ProtocolVersion
	if protocolVersion == "" {
		protocolVersion = DefaultProtocolVersion
	}
	maxRetries := config.MaxRetries
	if maxRetries == 0 {
		maxRetries = defaultMaxRetries
	} else if maxRetries < 0 {
		maxRetries = 0
	}
	retryDelay := config.RetryDelay
	if retryDelay == 0 {
		retryDelay = defaultRetryDelay
	} else if retryDelay < 0 {
		retryDelay = 0
	}
	requestTimeout := config.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = defaultRequestTimeout
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{
		baseURL:         baseURL,
		credential:      config.Credential,
		protocolVersion: protocolVersion,
		httpClient:      httpClient,
		maxRetries:      maxRetries,
		retryDelay:      retryDelay,
		requestTimeout:  requestTimeout,
	}, nil
}

// Wire types of the M10-a fixture contract.

// HealthReport is the read-only health/version observation of the Bridge.
type HealthReport struct {
	Status          string `json:"status"`
	ProtocolVersion string `json:"protocolVersion"`
	BridgeVersion   string `json:"bridgeVersion"`
}

// SandboxRecord is the Bridge's view of one sandbox lifecycle record.
type SandboxRecord struct {
	SandboxId  string `json:"sandboxId"`
	RunId      string `json:"runId"`
	AttemptId  string `json:"attemptId"`
	Generation int64  `json:"generation"`
	State      string `json:"state"`
}

// CreateSandboxRequest is the create payload.
type CreateSandboxRequest struct {
	SandboxId  string `json:"sandboxId"`
	RunId      string `json:"runId"`
	AttemptId  string `json:"attemptId"`
	Generation int64  `json:"generation"`
}

// SandboxList is the running-class listing payload (reconcile input).
type SandboxList struct {
	Sandboxes []SandboxRecord `json:"sandboxes"`
}

// ViolationRecord is one observed containment failure reported by the
// Bridge observation channel.
type ViolationRecord struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// SandboxStatus is the read-only observation payload of one sandbox.
type SandboxStatus struct {
	SandboxId    string            `json:"sandboxId"`
	State        string            `json:"state"`
	ExitCode     int               `json:"exitCode"`
	SpawnCount   int64             `json:"spawnCount"`
	Violations   []ViolationRecord `json:"violations"`
	LogLines     []string          `json:"logLines"`
	LiveSessions int               `json:"liveSessions"`
}

// LocatorRef is the wire form of one content-addressed locator handed to
// the container: the bound store alias plus digest and size, never a URL
// and never a credential.
type LocatorRef struct {
	StoreId   string `json:"storeId"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"sizeBytes"`
}

// WriteFileRequest is the file-write payload: exactly one of inline
// content or locator.
type WriteFileRequest struct {
	Path           string      `json:"path"`
	DeclaredSHA256 string      `json:"declaredSha256"`
	ContentBase64  string      `json:"contentBase64,omitempty"`
	Locator        *LocatorRef `json:"locator,omitempty"`
}

// WriteFileResult carries the digests the Bridge recomputed before and
// after consumption; a provider must never echo the declared digest in
// their place.
type WriteFileResult struct {
	PreSHA256  string `json:"preSha256"`
	PostSHA256 string `json:"postSha256"`
	SizeBytes  int64  `json:"sizeBytes"`
}

// ReadFileResult is the file-read observation used for the Marshal-side
// post-consumption recomputation.
type ReadFileResult struct {
	Path          string `json:"path"`
	ContentBase64 string `json:"contentBase64"`
	SHA256        string `json:"sha256"`
	SizeBytes     int64  `json:"sizeBytes"`
}

// PersistResult is the persist (checkpoint) observation.
type PersistResult struct {
	CheckpointId string `json:"checkpointId"`
	SHA256       string `json:"sha256"`
	SizeBytes    int64  `json:"sizeBytes"`
}

// HydrateRequest names the checkpoint to hydrate from.
type HydrateRequest struct {
	CheckpointId string `json:"checkpointId"`
}

// HydrateResult is the hydrate observation.
type HydrateResult struct {
	SandboxId    string `json:"sandboxId"`
	CheckpointId string `json:"checkpointId"`
	FileCount    int    `json:"fileCount"`
	SHA256       string `json:"sha256"`
}

// SignalRequest carries one closed-enumeration signal name.
type SignalRequest struct {
	Signal string `json:"signal"`
}

// SignalResult observes the signal delivery.
type SignalResult struct {
	Signal    string `json:"signal"`
	Delivered bool   `json:"delivered"`
}

// ExecStreamRequest is the exec payload: argv plus optional stdin. The
// operation identity never travels in the business JSON; only the derived
// idempotency digest may accompany the call as a header.
type ExecStreamRequest struct {
	Command     []string `json:"command"`
	StdinBase64 string   `json:"stdinBase64,omitempty"`
}

// ExecStreamResult is the client-side observation of one completed exec
// stream: exit code, the signaled flag and the bounded stream captures.
type ExecStreamResult struct {
	ExitCode int
	Signaled bool
	Stdout   []byte
	Stderr   []byte
}

// Health probes the read-only health endpoint and fails closed on any
// protocol version drift.
func (c *Client) Health(ctx context.Context) (report HealthReport, err error) {
	defer func() { err = c.scrub(err) }()
	data, err := c.do(ctx, http.MethodGet, healthPath, nil, nil, "", true)
	if err != nil {
		return HealthReport{}, err
	}
	if err := decodeCanonical(data, &report); err != nil {
		return HealthReport{}, err
	}
	if report.ProtocolVersion != c.protocolVersion {
		return HealthReport{}, fmt.Errorf("%w: the endpoint serves protocol %q, the client requires %q", ErrProtocolVersionMismatch, report.ProtocolVersion, c.protocolVersion)
	}
	return report, nil
}

// CreateSandbox calls the create endpoint with one idempotency key.
func (c *Client) CreateSandbox(ctx context.Context, request CreateSandboxRequest, idempotencyKey string) (record SandboxRecord, err error) {
	defer func() { err = c.scrub(err) }()
	data, err := c.do(ctx, http.MethodPost, sandboxesPath, nil, request, idempotencyKey, true)
	if err != nil {
		return SandboxRecord{}, err
	}
	if err := decodeCanonical(data, &record); err != nil {
		return SandboxRecord{}, err
	}
	return record, nil
}

// ListSandboxes calls the running-class listing endpoint for one (runId,
// attemptId) scope.
func (c *Client) ListSandboxes(ctx context.Context, runId, attemptId string) (records []SandboxRecord, err error) {
	defer func() { err = c.scrub(err) }()
	query := url.Values{}
	query.Set("runId", runId)
	query.Set("attemptId", attemptId)
	data, err := c.do(ctx, http.MethodGet, sandboxesPath, query, nil, "", true)
	if err != nil {
		return nil, err
	}
	var list SandboxList
	if err := decodeCanonical(data, &list); err != nil {
		return nil, err
	}
	return list.Sandboxes, nil
}

// SandboxStatus reads the observation endpoint of one sandbox.
func (c *Client) SandboxStatus(ctx context.Context, sandboxId string) (status SandboxStatus, err error) {
	defer func() { err = c.scrub(err) }()
	data, err := c.do(ctx, http.MethodGet, sandboxItemPath(sandboxId), nil, nil, "", true)
	if err != nil {
		return SandboxStatus{}, err
	}
	if err := decodeCanonical(data, &status); err != nil {
		return SandboxStatus{}, err
	}
	return status, nil
}

// Exec drives the exec SSE endpoint. Exec is never auto-retried: a command
// may have started executing even when the stream breaks, so a broken
// stream fails closed instead of re-executing.
func (c *Client) Exec(ctx context.Context, sandboxId string, request ExecStreamRequest) (result ExecStreamResult, err error) {
	defer func() { err = c.scrub(err) }()
	raw, err := json.Marshal(request)
	if err != nil {
		return result, fmt.Errorf("cloudflare bridge: exec request encoding: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(sandboxActionPath(sandboxId, "exec")), bytes.NewReader(raw))
	if err != nil {
		return result, fmt.Errorf("cloudflare bridge: exec request construction: %w", err)
	}
	disableTransportRetries(req)
	req.Header.Set("Authorization", "Bearer "+c.credential.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return result, fmt.Errorf("%w: the exec stream could not be established: %v", ErrBridgeUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		return result, c.decodeError(resp.StatusCode, data)
	}
	return parseExecStream(resp.Body)
}

// WriteFile calls the file-write endpoint; the Bridge recomputes the
// digest before consumption and refuses a mismatch without writing.
func (c *Client) WriteFile(ctx context.Context, sandboxId string, request WriteFileRequest, idempotencyKey string) (result WriteFileResult, err error) {
	defer func() { err = c.scrub(err) }()
	data, err := c.do(ctx, http.MethodPost, sandboxActionPath(sandboxId, "file"), nil, request, idempotencyKey, true)
	if err != nil {
		return WriteFileResult{}, err
	}
	if err := decodeCanonical(data, &result); err != nil {
		return WriteFileResult{}, err
	}
	return result, nil
}

// ReadFile reads one staged file back for the Marshal-side recomputation.
func (c *Client) ReadFile(ctx context.Context, sandboxId, path string) (result ReadFileResult, err error) {
	defer func() { err = c.scrub(err) }()
	query := url.Values{}
	query.Set("path", path)
	data, err := c.do(ctx, http.MethodGet, sandboxActionPath(sandboxId, "file"), query, nil, "", true)
	if err != nil {
		return ReadFileResult{}, err
	}
	if err := decodeCanonical(data, &result); err != nil {
		return ReadFileResult{}, err
	}
	return result, nil
}

// Persist calls the persist endpoint (checkpoint of the staged content).
func (c *Client) Persist(ctx context.Context, sandboxId, idempotencyKey string) (result PersistResult, err error) {
	defer func() { err = c.scrub(err) }()
	data, err := c.do(ctx, http.MethodPost, sandboxActionPath(sandboxId, "persist"), nil, struct{}{}, idempotencyKey, true)
	if err != nil {
		return PersistResult{}, err
	}
	if err := decodeCanonical(data, &result); err != nil {
		return PersistResult{}, err
	}
	return result, nil
}

// Hydrate calls the hydrate endpoint of one sandbox against one checkpoint.
func (c *Client) Hydrate(ctx context.Context, sandboxId, checkpointId, idempotencyKey string) (result HydrateResult, err error) {
	defer func() { err = c.scrub(err) }()
	data, err := c.do(ctx, http.MethodPost, sandboxActionPath(sandboxId, "hydrate"), nil, HydrateRequest{CheckpointId: checkpointId}, idempotencyKey, true)
	if err != nil {
		return HydrateResult{}, err
	}
	if err := decodeCanonical(data, &result); err != nil {
		return HydrateResult{}, err
	}
	return result, nil
}

// Destroy calls the destroy endpoint; destroy is idempotent at the Bridge.
func (c *Client) Destroy(ctx context.Context, sandboxId, idempotencyKey string) (record SandboxRecord, err error) {
	defer func() { err = c.scrub(err) }()
	data, err := c.do(ctx, http.MethodDelete, sandboxItemPath(sandboxId), nil, nil, idempotencyKey, true)
	if err != nil {
		return SandboxRecord{}, err
	}
	if err := decodeCanonical(data, &record); err != nil {
		return SandboxRecord{}, err
	}
	return record, nil
}

// Signal delivers one closed-enumeration signal through the Bridge.
func (c *Client) Signal(ctx context.Context, sandboxId, signal, idempotencyKey string) (result SignalResult, err error) {
	defer func() { err = c.scrub(err) }()
	data, err := c.do(ctx, http.MethodPost, sandboxActionPath(sandboxId, "signal"), nil, SignalRequest{Signal: signal}, idempotencyKey, true)
	if err != nil {
		return SignalResult{}, err
	}
	if err := decodeCanonical(data, &result); err != nil {
		return SignalResult{}, err
	}
	return result, nil
}

func sandboxItemPath(sandboxId string) string {
	return sandboxesPath + "/" + url.PathEscape(sandboxId)
}

func sandboxActionPath(sandboxId, action string) string {
	return sandboxItemPath(sandboxId) + "/" + action
}

func (c *Client) endpoint(path string) string {
	return c.baseURL + path
}

// disableTransportRetries makes one request fail closed in exactly one wire
// attempt. The stdlib Transport transparently re-sends a request after a
// connection-level failure whenever it can rewind the request body
// (GetBody != nil, which http.NewRequestWithContext assigns for the
// bytes.Reader bodies this client builds) or whenever the request carries
// no body at all. That transparent retry would silently multiply the
// bounded retry budget of do(): one logical attempt would consume several
// lost responses and reach the endpoint well beyond the frozen attempt
// bound. The retry budget of this client is owned exclusively by do(), so
// clearing GetBody removes the Transport's ability to rewind a bodied
// request, and a bodiless request (the running-class reads and destroy)
// receives a non-rewindable zero-length body for the identical reason: one
// logical attempt is always exactly one wire request, and a lost or refused
// response surfaces for an explicit idempotency-keyed decision instead of
// disappearing into a transparent side effect. Recovery above the budget is
// the caller's decision, never the transport's.
func disableTransportRetries(req *http.Request) {
	req.GetBody = nil
	if req.Body == nil || req.Body == http.NoBody {
		req.Body = io.NopCloser(bytes.NewReader(nil))
		req.ContentLength = 0
	}
}

// do performs one request with the bounded deterministic retry budget: one
// attempt is exactly one wire request (disableTransportRetries strips the
// stdlib Transport's own transparent retry, so the budget below is the
// single retry authority of this client). Reads and idempotency-keyed
// mutations retry on 500/502/503/504 and on transport failures; credential
// refusals, capacity refusals and semantic 4xx errors never retry.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, idempotencyKey string, allowRetry bool) ([]byte, error) {
	attempts := 1
	if allowRetry {
		attempts = c.maxRetries + 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			if c.retryDelay > 0 {
				select {
				case <-ctx.Done():
					return nil, fmt.Errorf("cloudflare bridge: %w", ctx.Err())
				case <-time.After(c.retryDelay):
				}
			}
			if ctx.Err() != nil {
				return nil, fmt.Errorf("cloudflare bridge: %w", ctx.Err())
			}
		}
		callCtx := ctx
		var cancel context.CancelFunc
		if c.requestTimeout > 0 {
			callCtx, cancel = context.WithTimeout(ctx, c.requestTimeout)
		}
		status, data, err := c.send(callCtx, method, path, query, body, idempotencyKey)
		if cancel != nil {
			cancel()
		}
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, fmt.Errorf("cloudflare bridge: %w", err)
			}
			continue
		}
		if retryableStatus(status) {
			lastErr = c.decodeError(status, data)
			continue
		}
		if status < 200 || status >= 300 {
			return nil, c.decodeError(status, data)
		}
		return data, nil
	}
	return nil, fmt.Errorf("%w: after %d attempt(s): %v", ErrBridgeUnavailable, attempts, lastErr)
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func (c *Client) send(ctx context.Context, method, path string, query url.Values, body any, idempotencyKey string) (int, []byte, error) {
	endpoint := c.endpoint(path)
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("cloudflare bridge: request encoding: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, nil, fmt.Errorf("cloudflare bridge: request construction: %w", err)
	}
	disableTransportRetries(req)
	req.Header.Set("Authorization", "Bearer "+c.credential.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if int64(len(data)) > maxResponseBytes {
		return resp.StatusCode, nil, fmt.Errorf("%w: the response body exceeds the bounded capture budget", ErrInvalidBridgeResponse)
	}
	return resp.StatusCode, data, nil
}

// decodeError classifies one non-2xx observation into a BridgeError bound
// to its sentinel; the message is truncated and scrubbed before surfacing.
func (c *Client) decodeError(status int, data []byte) error {
	code, message := "unknown", ""
	if len(data) > 0 {
		if canonicalized, err := canonical.JSON(data); err == nil {
			var payload struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			if json.Unmarshal(canonicalized, &payload) == nil {
				code, message = payload.Code, payload.Message
			}
		}
	}
	if len(message) > 200 {
		message = message[:200]
	}
	return &BridgeError{Status: status, Code: code, Message: message, sentinel: sentinelFor(status, code)}
}

func sentinelFor(status int, code string) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrCredentialRejected
	case http.StatusNotFound:
		switch code {
		case "checkpoint-not-found":
			return ErrCheckpointNotFound
		case "locator-unresolved":
			return ErrBridgeLocatorUnresolved
		default:
			return ErrSandboxNotFound
		}
	case http.StatusConflict:
		return ErrBridgeConflict
	case http.StatusGone:
		return ErrContainerLost
	case http.StatusUnprocessableEntity:
		if code == "digest-mismatch" {
			return ErrDigestMismatch
		}
		return ErrBridgeRejected
	case http.StatusTooManyRequests, http.StatusInsufficientStorage:
		return ErrCapacityExhausted
	default:
		if status >= 500 {
			return ErrBridgeUnavailable
		}
		return ErrBridgeRejected
	}
}

// decodeCanonical admits a response body only through canonical JSON, so
// duplicate object members at any nesting depth are rejected fail closed
// before any field is interpreted (ADR 0017 §11).
func decodeCanonical(data []byte, out any) error {
	canonicalized, err := canonical.JSON(data)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidBridgeResponse, err)
	}
	if err := json.Unmarshal(canonicalized, out); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidBridgeResponse, err)
	}
	return nil
}

// scrub removes the transport credential from any error text before it can
// surface; it is wired into every public client method through a deferred
// call, so no code path can leak the credential through an error.
func (c *Client) scrub(err error) error {
	if err == nil {
		return nil
	}
	token := c.credential.token
	if token == "" || !strings.Contains(err.Error(), token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, redactedCredential))
}

// boundedBuffer captures at most limit bytes of one exec output stream.
type boundedBuffer struct {
	limit int64
	buf   bytes.Buffer
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if int64(b.buf.Len()) < b.limit {
		room := b.limit - int64(b.buf.Len())
		if int64(len(data)) > room {
			data = data[:room]
		}
		_, _ = b.buf.Write(data)
	}
	return len(data), nil
}

func (b *boundedBuffer) bytes() []byte { return bytes.Clone(b.buf.Bytes()) }

// parseExecStream consumes one Bridge exec SSE stream: "output" events
// feed the bounded stdout/stderr captures, the "exit" event carries the
// terminal observation. A stream that ends without an exit event fails
// closed.
func parseExecStream(body io.Reader) (ExecStreamResult, error) {
	result := ExecStreamResult{ExitCode: -1}
	stdout := &boundedBuffer{limit: maxStreamCaptureBytes}
	stderr := &boundedBuffer{limit: maxStreamCaptureBytes}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	eventName := ""
	var dataLines []string
	exited := false
	dispatch := func() error {
		if eventName == "" && len(dataLines) == 0 {
			return nil
		}
		name := eventName
		payload := strings.Join(dataLines, "\n")
		eventName = ""
		dataLines = nil
		switch name {
		case "output":
			var event struct {
				Stream string `json:"stream"`
				Data   string `json:"data"`
			}
			if err := decodeCanonical([]byte(payload), &event); err != nil {
				return err
			}
			content, err := base64.StdEncoding.DecodeString(event.Data)
			if err != nil {
				return fmt.Errorf("%w: the exec stream carried a malformed output payload", ErrInvalidBridgeResponse)
			}
			switch event.Stream {
			case "stdout":
				_, _ = stdout.Write(content)
			case "stderr":
				_, _ = stderr.Write(content)
			}
		case "exit":
			var event struct {
				ExitCode int  `json:"exitCode"`
				Signaled bool `json:"signaled"`
			}
			if err := decodeCanonical([]byte(payload), &event); err != nil {
				return err
			}
			result.ExitCode = event.ExitCode
			result.Signaled = event.Signaled
			exited = true
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return result, err
			}
			if exited {
				break
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		switch name {
		case "event":
			eventName = value
		case "data":
			dataLines = append(dataLines, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("%w: the exec stream broke: %v", ErrBridgeUnavailable, err)
	}
	if !exited {
		return result, fmt.Errorf("%w: the exec stream ended without an exit event", ErrBridgeUnavailable)
	}
	result.Stdout = stdout.bytes()
	result.Stderr = stderr.bytes()
	return result, nil
}
