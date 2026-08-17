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

// Wire contract status: the endpoint family below is the official
// Cloudflare stable Sandbox Bridge HTTP API (cloudflare/sandbox-sdk bridge
// worker), as re-verified online and frozen here:
//
//	GET    /health                     -> 200 {"ok":true}        (no credential)
//	POST   /v1/sandbox                 -> 200 {"id":"<locator>"}
//	DELETE /v1/sandbox/:id             -> 204
//	GET    /v1/sandbox/:id/running     -> 200 {"running":bool}
//	POST   /v1/sandbox/:id/exec        -> SSE stdout/stderr base64 + terminal
//	GET    /v1/sandbox/:id/file/*      -> raw bytes
//	PUT    /v1/sandbox/:id/file/*      -> raw bytes
//	POST   /v1/sandbox/:id/persist     -> raw tar
//	POST   /v1/sandbox/:id/hydrate     -> raw tar request
//	POST   /v1/sandbox/:id/session     -> {"sessionId":"<id>"}
//	DELETE /v1/sandbox/:id/session/:id -> 204
//
// The wire exposes no alternate health route, no bulk listing of sandboxes
// and no dedicated kill endpoint. Signal is delivered by deleting the exact
// session.

const (
	defaultMaxRetries     = 2
	defaultRetryDelay     = 5 * time.Millisecond
	defaultRequestTimeout = 10 * time.Second

	// maxRawBytes bounds one raw byte payload (file read/write, persist
	// tar, hydrate tar) at 32 MiB, the frozen official wire budget.
	maxRawBytes int64 = 32 << 20

	// maxStreamCaptureBytes bounds each captured exec output stream; it is a
	// Marshal bounded-capture budget, never a Cloudflare platform quota.
	maxStreamCaptureBytes int64 = 1 << 20

	redactedCredential = "[redacted:bridge-credential]"
)

// Endpoint paths of the official Bridge wire contract.
const (
	healthPath  = "/health"
	sandboxPath = "/v1/sandbox"
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
	// ErrBridgeUnavailable is returned after the bounded retry budget is
	// exhausted against transient failures (5xx or transport errors).
	ErrBridgeUnavailable = errors.New("cloudflare bridge: endpoint unavailable")
	// ErrInvalidBridgeResponse rejects a response body that fails canonical
	// JSON admission (including duplicate object members) or that cannot be
	// decoded into the expected wire shape.
	ErrInvalidBridgeResponse = errors.New("cloudflare bridge: response rejected")
	// ErrSandboxNotFound maps a Bridge 404 for an unknown sandbox.
	ErrSandboxNotFound = errors.New("cloudflare bridge: sandbox not found")
	// ErrSessionNotFound maps a Bridge 404 for an unknown session.
	ErrSessionNotFound = errors.New("cloudflare bridge: session not found")
	// ErrCheckpointNotFound maps a Bridge 404 for an unknown checkpoint.
	ErrCheckpointNotFound = errors.New("cloudflare bridge: checkpoint not found")
	// ErrContainerLost maps a Bridge 410: the container's file and process
	// state was lost (hibernation/fault/restart), fail closed.
	ErrContainerLost = errors.New("cloudflare bridge: container state lost")
	// ErrBridgeConflict maps a Bridge 409 duplicate-sandbox observation.
	ErrBridgeConflict = errors.New("cloudflare bridge: sandbox conflict")
	// ErrCapacityExhausted maps Bridge capacity refusals (HTTP 429/507);
	// they are never retried and never downgraded.
	ErrCapacityExhausted = errors.New("cloudflare bridge: capacity exhausted")
	// ErrBridgeRejected maps any other client-side Bridge refusal (4xx).
	ErrBridgeRejected = errors.New("cloudflare bridge: request rejected")
)

// Credential holds the Bridge Bearer transport credential. It is a
// transport-layer secret only: it never substitutes for the fencingToken,
// never enters business JSON, events, logs, digests or error messages, and
// String, Format and GoString all redact it, so no verb, pointer, carrier
// struct, error wrap or log call can surface the literal.
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

// String always returns the fixed redaction marker.
func (c Credential) String() string { return redactedCredential }

// Format implements fmt.Formatter so every formatting verb (%v, %+v, %#v,
// %s, %q, ...) redacts the credential; it never writes the token.
func (c Credential) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte(redactedCredential))
}

// GoString implements fmt.GoStringer so %#v redacts the credential instead
// of printing the struct literal with its token field.
func (c Credential) GoString() string { return redactedCredential }

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
	BaseURL        string
	Credential     Credential
	HTTPClient     *http.Client
	MaxRetries     int
	RetryDelay     time.Duration
	RequestTimeout time.Duration
}

// Client is the versioned HTTP client of the official Bridge endpoint
// family. The Bearer credential lives only inside the client and only ever
// travels as the Authorization header of one transport request; the health
// endpoint is the single unauthenticated member.
type Client struct {
	baseURL        string
	credential     Credential
	httpClient     *http.Client
	maxRetries     int
	retryDelay     time.Duration
	requestTimeout time.Duration
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
		baseURL:        baseURL,
		credential:     config.Credential,
		httpClient:     httpClient,
		maxRetries:     maxRetries,
		retryDelay:     retryDelay,
		requestTimeout: requestTimeout,
	}, nil
}

// Wire types of the official Bridge contract.

// HealthReport is the read-only health observation.
type HealthReport struct {
	OK bool `json:"ok"`
}

// CreateSandboxResult is the create observation; ID is the Bridge locator.
type CreateSandboxResult struct {
	ID string `json:"id"`
}

// RunningReport is the running observation of one sandbox.
type RunningReport struct {
	Running bool `json:"running"`
}

// SessionResult is the session-creation observation.
type SessionResult struct {
	SessionId string `json:"sessionId"`
}

// ExecStreamRequest is the exec payload: argv plus optional timeout and
// working directory. The operation identity never travels in the business
// JSON; the idempotency of the surrounding operations lives in headers.
type ExecStreamRequest struct {
	Argv      []string `json:"argv"`
	TimeoutMs int      `json:"timeout_ms,omitempty"`
	Cwd       string   `json:"cwd,omitempty"`
}

// ExecStreamResult is the client-side observation of one completed exec
// stream: exit code, the signaled flag (an error terminal) and the bounded
// stream captures.
type ExecStreamResult struct {
	ExitCode int
	Signaled bool
	Stdout   []byte
	Stderr   []byte
}

// Health probes the read-only health endpoint. It is the single
// unauthenticated member of the wire family and never carries the Bearer
// credential.
func (c *Client) Health(ctx context.Context) (report HealthReport, err error) {
	defer func() { err = c.scrub(err) }()
	data, err := c.do(ctx, http.MethodGet, healthPath, nil, nil, "", "", true, false)
	if err != nil {
		return HealthReport{}, err
	}
	if err := decodeCanonical(data, &report); err != nil {
		return HealthReport{}, err
	}
	if !report.OK {
		return HealthReport{}, fmt.Errorf("%w: the bridge health endpoint did not report ok", ErrInvalidBridgeResponse)
	}
	return report, nil
}

// CreateSandbox calls the create endpoint with one idempotency key and
// returns the Bridge locator the platform assigned. Create is an opaque side
// effect: a lost response is retried under the identical idempotency key, so
// a replay after a crash converges on the identical locator.
func (c *Client) CreateSandbox(ctx context.Context, idempotencyKey string) (id string, err error) {
	defer func() { err = c.scrub(err) }()
	data, err := c.do(ctx, http.MethodPost, sandboxPath, nil, []byte("{}"), "application/json", idempotencyKey, true, true)
	if err != nil {
		return "", err
	}
	var result CreateSandboxResult
	if err := decodeCanonical(data, &result); err != nil {
		return "", err
	}
	if strings.TrimSpace(result.ID) == "" {
		return "", fmt.Errorf("%w: the create endpoint returned no sandbox id", ErrInvalidBridgeResponse)
	}
	return result.ID, nil
}

// SandboxRunning reads the running observation of one sandbox.
func (c *Client) SandboxRunning(ctx context.Context, sandboxId string) (running bool, err error) {
	defer func() { err = c.scrub(err) }()
	data, err := c.do(ctx, http.MethodGet, sandboxRunningPath(sandboxId), nil, nil, "", "", true, true)
	if err != nil {
		return false, err
	}
	var report RunningReport
	if err := decodeCanonical(data, &report); err != nil {
		return false, err
	}
	return report.Running, nil
}

// Destroy calls the destroy endpoint; destroy is idempotent at the Bridge and
// returns 204 on success.
func (c *Client) Destroy(ctx context.Context, sandboxId, idempotencyKey string) (err error) {
	defer func() { err = c.scrub(err) }()
	_, err = c.do(ctx, http.MethodDelete, sandboxItemPath(sandboxId), nil, nil, "", idempotencyKey, true, true)
	return err
}

// CreateSession creates one interactive session inside a sandbox and returns
// its session id.
func (c *Client) CreateSession(ctx context.Context, sandboxId, idempotencyKey string) (sessionId string, err error) {
	defer func() { err = c.scrub(err) }()
	data, err := c.do(ctx, http.MethodPost, sandboxSessionPath(sandboxId), nil, []byte("{}"), "application/json", idempotencyKey, true, true)
	if err != nil {
		return "", err
	}
	var result SessionResult
	if err := decodeCanonical(data, &result); err != nil {
		return "", err
	}
	if strings.TrimSpace(result.SessionId) == "" {
		return "", fmt.Errorf("%w: the session endpoint returned no session id", ErrInvalidBridgeResponse)
	}
	return result.SessionId, nil
}

// DeleteSession deletes the exact session, delivering the kill that Signal
// maps onto. A 204 acknowledges the deletion; a 404 surfaces ErrSessionNotFound.
func (c *Client) DeleteSession(ctx context.Context, sandboxId, sessionId string) (err error) {
	defer func() { err = c.scrub(err) }()
	_, err = c.do(ctx, http.MethodDelete, sandboxSessionItemPath(sandboxId, sessionId), nil, nil, "", "", true, true)
	return err
}

// Exec drives the exec SSE endpoint. Exec is never auto-retried: a command
// may have started executing even when the stream breaks, so a broken
// stream fails closed instead of re-executing. The optional session id
// travels only as the Session-Id header.
func (c *Client) Exec(ctx context.Context, sandboxId, sessionId string, request ExecStreamRequest) (result ExecStreamResult, err error) {
	defer func() { err = c.scrub(err) }()
	raw, err := json.Marshal(request)
	if err != nil {
		return result, fmt.Errorf("cloudflare bridge: exec request encoding: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(sandboxExecPath(sandboxId)), bytes.NewReader(raw))
	if err != nil {
		return result, fmt.Errorf("cloudflare bridge: exec request construction: %w", err)
	}
	disableTransportRetries(req)
	req.Header.Set("Authorization", "Bearer "+c.credential.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if sessionId != "" {
		req.Header.Set("Session-Id", sessionId)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return result, fmt.Errorf("%w: the exec stream could not be established: %v", ErrBridgeUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxRawBytes))
		return result, c.decodeError(resp.StatusCode, data)
	}
	return parseExecStream(resp.Body)
}

// ReadFile reads one staged file back as raw bytes.
func (c *Client) ReadFile(ctx context.Context, sandboxId, path string) (content []byte, err error) {
	defer func() { err = c.scrub(err) }()
	data, err := c.do(ctx, http.MethodGet, sandboxFilePath(sandboxId, path), nil, nil, "", "", true, true)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// WriteFile writes one staged file as raw bytes. The digest discipline is
// the caller's: this endpoint is a dumb byte store.
func (c *Client) WriteFile(ctx context.Context, sandboxId, path string, content []byte, idempotencyKey string) (err error) {
	defer func() { err = c.scrub(err) }()
	_, err = c.do(ctx, http.MethodPut, sandboxFilePath(sandboxId, path), nil, content, "application/octet-stream", idempotencyKey, true, true)
	return err
}

// Persist snapshots the staged content and returns the raw tar bytes. The
// caller recomputes the checkpoint digest out of band; the Bridge never
// echoes a declared digest.
func (c *Client) Persist(ctx context.Context, sandboxId, idempotencyKey string) (tar []byte, err error) {
	defer func() { err = c.scrub(err) }()
	data, err := c.do(ctx, http.MethodPost, sandboxPersistPath(sandboxId), nil, []byte("{}"), "application/json", idempotencyKey, true, true)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Hydrate restores the staged content from the raw tar bytes.
func (c *Client) Hydrate(ctx context.Context, sandboxId string, tar []byte, idempotencyKey string) (err error) {
	defer func() { err = c.scrub(err) }()
	_, err = c.do(ctx, http.MethodPost, sandboxHydratePath(sandboxId), nil, tar, "application/octet-stream", idempotencyKey, true, true)
	return err
}

func sandboxItemPath(sandboxId string) string {
	return sandboxPath + "/" + url.PathEscape(sandboxId)
}

func sandboxRunningPath(sandboxId string) string {
	return sandboxItemPath(sandboxId) + "/running"
}

func sandboxExecPath(sandboxId string) string {
	return sandboxItemPath(sandboxId) + "/exec"
}

func sandboxPersistPath(sandboxId string) string {
	return sandboxItemPath(sandboxId) + "/persist"
}

func sandboxHydratePath(sandboxId string) string {
	return sandboxItemPath(sandboxId) + "/hydrate"
}

func sandboxSessionPath(sandboxId string) string {
	return sandboxItemPath(sandboxId) + "/session"
}

func sandboxSessionItemPath(sandboxId, sessionId string) string {
	return sandboxSessionPath(sandboxId) + "/" + url.PathEscape(sessionId)
}

// sandboxFilePath builds the wildcard file path, escaping each segment while
// preserving the path separators so the file content address is a raw byte
// route, never a JSON payload.
func sandboxFilePath(sandboxId, path string) string {
	segments := strings.Split(path, "/")
	escaped := make([]string, len(segments))
	for i, segment := range segments {
		escaped[i] = url.PathEscape(segment)
	}
	return sandboxItemPath(sandboxId) + "/file/" + strings.Join(escaped, "/")
}

func (c *Client) endpoint(path string) string {
	return c.baseURL + path
}

// disableTransportRetries makes one request fail closed in exactly one wire
// attempt. The stdlib Transport transparently re-sends a request after a
// connection-level failure whenever it can rewind the request body. That
// transparent retry would silently multiply the bounded retry budget of do:
// one logical attempt would consume several lost responses. The retry budget
// of this client is owned exclusively by do, so clearing GetBody removes the
// Transport's ability to rewind a bodied request, and a bodiless request
// receives a non-rewindable zero-length body for the identical reason.
func disableTransportRetries(req *http.Request) {
	req.GetBody = nil
	if req.Body == nil || req.Body == http.NoBody {
		req.Body = io.NopCloser(bytes.NewReader(nil))
		req.ContentLength = 0
	}
}

// do performs one request with the bounded deterministic retry budget: one
// attempt is exactly one wire request. Reads and idempotency-keyed mutations
// retry on 500/502/503/504 and on transport failures; credential refusals,
// capacity refusals and semantic 4xx errors never retry.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body []byte, contentType, idempotencyKey string, allowRetry, authenticated bool) ([]byte, error) {
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
		status, data, err := c.send(callCtx, method, path, query, body, contentType, idempotencyKey, authenticated)
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

func (c *Client) send(ctx context.Context, method, path string, query url.Values, body []byte, contentType, idempotencyKey string, authenticated bool) (int, []byte, error) {
	endpoint := c.endpoint(path)
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, nil, fmt.Errorf("cloudflare bridge: request construction: %w", err)
	}
	disableTransportRetries(req)
	req.Header.Set("Accept", "application/json")
	if authenticated {
		req.Header.Set("Authorization", "Bearer "+c.credential.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRawBytes+1))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if int64(len(data)) > maxRawBytes {
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
		case "session-not-found":
			return ErrSessionNotFound
		default:
			return ErrSandboxNotFound
		}
	case http.StatusConflict:
		return ErrBridgeConflict
	case http.StatusGone:
		return ErrContainerLost
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

// parseExecStream consumes one Bridge exec SSE stream. The "stdout" and
// "stderr" events carry base64 chunks; the stream must carry exactly one
// terminal event — "exit" carrying {"exit_code":N} or "error" carrying the
// abnormal-termination terminal. A stream that ends without a terminal, that
// carries two terminals, or that carries any event after the terminal, fails
// closed.
func parseExecStream(body io.Reader) (ExecStreamResult, error) {
	result := ExecStreamResult{ExitCode: -1}
	stdout := &boundedBuffer{limit: maxStreamCaptureBytes}
	stderr := &boundedBuffer{limit: maxStreamCaptureBytes}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	eventName := ""
	var dataLines []string
	terminated := false
	dispatch := func() error {
		if eventName == "" && len(dataLines) == 0 {
			return nil
		}
		name := eventName
		payload := strings.Join(dataLines, "\n")
		eventName = ""
		dataLines = nil
		if terminated {
			return fmt.Errorf("%w: the exec stream carried an event after its terminal event", ErrInvalidBridgeResponse)
		}
		switch name {
		case "stdout", "stderr":
			content, err := base64.StdEncoding.DecodeString(payload)
			if err != nil {
				return fmt.Errorf("%w: the exec stream carried a malformed output payload", ErrInvalidBridgeResponse)
			}
			if name == "stdout" {
				_, _ = stdout.Write(content)
			} else {
				_, _ = stderr.Write(content)
			}
		case "exit":
			var event struct {
				ExitCode int `json:"exit_code"`
			}
			if err := decodeCanonical([]byte(payload), &event); err != nil {
				return err
			}
			result.ExitCode = event.ExitCode
			result.Signaled = false
			terminated = true
		case "error":
			result.ExitCode = -1
			result.Signaled = true
			terminated = true
		default:
			return fmt.Errorf("%w: the exec stream carried an unknown event %q", ErrInvalidBridgeResponse, name)
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return result, err
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
	if err := dispatch(); err != nil {
		return result, err
	}
	if !terminated {
		return result, fmt.Errorf("%w: the exec stream ended without a terminal event", ErrInvalidBridgeResponse)
	}
	result.Stdout = stdout.bytes()
	result.Stderr = stderr.bytes()
	return result, nil
}
