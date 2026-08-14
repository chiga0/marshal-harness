package marshalclient

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrorCode is the stable machine-readable error vocabulary of the
// marshal-public-api protocol family, frozen by internal/server/openapi.json
// (components.schemas.Error). The code plus the reason string is the frozen
// contract; the human message never carries dynamic or sensitive detail.
type ErrorCode string

// Frozen error codes of the public-api protocol family.
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

// Sentinel classification errors. Every APIError unwraps to the sentinel of
// its code, and protocol-version/audience rejections additionally unwrap to
// their dedicated sentinels, so errors.Is decisions are deterministic.
var (
	ErrInvalidRequest      = errors.New("marshalclient: INVALID_REQUEST")
	ErrMissingIdentity     = errors.New("marshalclient: MISSING_IDENTITY")
	ErrForbiddenIdentity   = errors.New("marshalclient: FORBIDDEN_IDENTITY")
	ErrScopeMismatch       = errors.New("marshalclient: SCOPE_MISMATCH")
	ErrNotFound            = errors.New("marshalclient: NOT_FOUND")
	ErrIdempotencyConflict = errors.New("marshalclient: IDEMPOTENCY_CONFLICT")
	ErrInvalidState        = errors.New("marshalclient: INVALID_STATE")
	ErrRejected            = errors.New("marshalclient: REJECTED")
	ErrInternal            = errors.New("marshalclient: INTERNAL")

	// ErrProtocolVersionRejected classifies the fail-closed rejection of a
	// request whose Marshal-Protocol-Version is not part of the frozen
	// protocol family.
	ErrProtocolVersionRejected = errors.New("marshalclient: protocol version rejected by the server")
	// ErrAudienceRejected classifies the fail-closed rejection of a request
	// whose Marshal-Audience does not address the public-api Port.
	ErrAudienceRejected = errors.New("marshalclient: audience rejected by the server")

	// ErrInvalidID reports a path identifier that is not a valid Marshal ID
	// (^[A-Za-z0-9][A-Za-z0-9._:-]{0,159}$). The client rejects it before any
	// request leaves the process.
	ErrInvalidID = errors.New("marshalclient: invalid Marshal ID")

	// ErrPayloadRejected reports a mutating payload that cannot be
	// canonicalized under RFC 8785 and therefore cannot carry a valid
	// requestDigest. Its text is fixed and never includes payload content.
	ErrPayloadRejected = errors.New("marshalclient: payload rejected: not RFC 8785 canonicalizable")
)

// APIError is the typed mapping of one versioned Error document returned by
// the public-api Port. The body reason is always preserved and is never
// swallowed; credential material (principal, audience, scope) is never part
// of the error text.
type APIError struct {
	// Code is the frozen machine-readable error code.
	Code ErrorCode
	// Reason is the frozen machine-readable reason of the code.
	Reason string
	// Message is the human-readable message of the Error document.
	Message string
	// RequestID is the requestId member echoed by the server, when present.
	RequestID string
	// Status is the HTTP status the Error document arrived with.
	Status int
}

// Error renders the typed error without any credential material.
func (e *APIError) Error() string {
	return fmt.Sprintf("marshalclient: HTTP %d %s: %s: %s", e.Status, e.Code, e.Reason, e.Message)
}

// Unwrap exposes the deterministic classification of the error: the
// reason-level sentinels for protocol-version and audience rejections take
// precedence, then the code-level sentinel.
func (e *APIError) Unwrap() error {
	switch e.Reason {
	case "protocol-version-mismatch":
		return ErrProtocolVersionRejected
	case "audience-mismatch":
		return ErrAudienceRejected
	}
	switch e.Code {
	case CodeInvalidRequest:
		return ErrInvalidRequest
	case CodeMissingIdentity:
		return ErrMissingIdentity
	case CodeForbiddenIdentity:
		return ErrForbiddenIdentity
	case CodeScopeMismatch:
		return ErrScopeMismatch
	case CodeNotFound:
		return ErrNotFound
	case CodeIdempotencyConflict:
		return ErrIdempotencyConflict
	case CodeInvalidState:
		return ErrInvalidState
	case CodeRejected:
		return ErrRejected
	case CodeInternal:
		return ErrInternal
	}
	return nil
}

// TransportError reports one HTTP transport failure (dial, request write or
// response read). It is deliberately distinct from APIError: a transport
// failure carries no server Error document at all.
type TransportError struct {
	// Op names the failed transport step.
	Op string
	// Err is the underlying transport error.
	Err error
}

// Error renders the transport failure.
func (e *TransportError) Error() string {
	return "marshalclient: transport: " + e.Op + ": " + e.Err.Error()
}

// Unwrap exposes the underlying transport error.
func (e *TransportError) Unwrap() error { return e.Err }

// ResyncRequiredError carries one deterministic EventResync directive
// (ADR 0018 §4/§14): the cursor is expired, gap or unservable and the
// subscription must be rebuilt from the directive's startSequence against
// its snapshotDigest. It is never a silent continuation, and it is
// deliberately not the Error envelope.
type ResyncRequiredError struct {
	// Directive is the deterministic resync directive of the server.
	Directive EventResync
}

// Error renders the resync requirement.
func (e *ResyncRequiredError) Error() string {
	return fmt.Sprintf("marshalclient: event resync required: reason %s, startSequence %d, snapshotDigest %s",
		e.Directive.Reason, e.Directive.StartSequence, e.Directive.SnapshotDigest)
}

// UnexpectedResponseError reports one non-2xx response whose body is
// neither a frozen Error document nor a frozen EventResync directive. The
// body snippet is preserved so the server's reason is never swallowed.
type UnexpectedResponseError struct {
	// Status is the HTTP status of the response.
	Status int
	// Body is a bounded snippet of the response body.
	Body string
}

// Error renders the unexpected response.
func (e *UnexpectedResponseError) Error() string {
	return fmt.Sprintf("marshalclient: unexpected HTTP %d response: %s", e.Status, e.Body)
}

// ProtocolViolationError reports one response or stream frame that violates
// the frozen protocol (undecodable success body, malformed SSE frame, event
// projection without identity). The stream fails closed on it.
type ProtocolViolationError struct {
	// Detail describes the violation.
	Detail string
}

// Error renders the protocol violation.
func (e *ProtocolViolationError) Error() string {
	return "marshalclient: protocol violation: " + e.Detail
}

// SequenceGapError reports a gap in the monotonic event ledger: the stream
// delivered ledgerSequence Got while Expected was the next sequence. The
// stream fails closed instead of silently continuing over the gap.
type SequenceGapError struct {
	// Expected is the ledgerSequence the consumer expected next.
	Expected uint64
	// Got is the ledgerSequence actually delivered.
	Got uint64
}

// Error renders the ledger gap.
func (e *SequenceGapError) Error() string {
	return fmt.Sprintf("marshalclient: event ledger gap: expected sequence %d, got %d", e.Expected, e.Got)
}

// AsAPIError extracts the typed APIError of one error, when present.
func AsAPIError(err error) (*APIError, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr, true
	}
	return nil, false
}

// IsAPIError reports whether the error is (or wraps) a typed APIError.
func IsAPIError(err error) bool {
	_, ok := AsAPIError(err)
	return ok
}

// AsResyncRequired extracts the typed ResyncRequiredError of one error,
// when present.
func AsResyncRequired(err error) (*ResyncRequiredError, bool) {
	var resync *ResyncRequiredError
	if errors.As(err, &resync) {
		return resync, true
	}
	return nil, false
}

// IsResyncRequired reports whether the error is (or wraps) a typed
// ResyncRequiredError.
func IsResyncRequired(err error) bool {
	_, ok := AsResyncRequired(err)
	return ok
}

// IsTransportError reports whether the error is (or wraps) a typed
// TransportError.
func IsTransportError(err error) bool {
	var transport *TransportError
	return errors.As(err, &transport)
}

// IsProtocolVersionRejected reports whether the server rejected the request
// protocol version fail closed.
func IsProtocolVersionRejected(err error) bool {
	return errors.Is(err, ErrProtocolVersionRejected)
}

// IsAudienceRejected reports whether the server rejected the request
// audience fail closed.
func IsAudienceRejected(err error) bool {
	return errors.Is(err, ErrAudienceRejected)
}

// mapHTTPError maps one non-2xx response body to its typed error: the frozen
// Error envelope becomes an APIError (reason preserved), the frozen
// EventResync directive becomes a ResyncRequiredError, and anything else
// becomes an UnexpectedResponseError that retains a bounded body snippet.
func mapHTTPError(status int, body []byte) error {
	var envelope struct {
		APIVersion string    `json:"apiVersion"`
		Kind       string    `json:"kind"`
		Code       ErrorCode `json:"code"`
		Reason     string    `json:"reason"`
		Message    string    `json:"message"`
		RequestID  string    `json:"requestId"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Kind == "Error" && envelope.Code != "" {
		return &APIError{
			Code:      envelope.Code,
			Reason:    envelope.Reason,
			Message:   envelope.Message,
			RequestID: envelope.RequestID,
			Status:    status,
		}
	}
	var directive EventResync
	if err := json.Unmarshal(body, &directive); err == nil && directive.Kind == "EventResync" {
		return &ResyncRequiredError{Directive: directive}
	}
	return &UnexpectedResponseError{Status: status, Body: bodySnippet(body)}
}

// bodySnippet bounds the retained snippet of one opaque response body.
func bodySnippet(body []byte) string {
	const limit = 512
	if len(body) > limit {
		return string(body[:limit]) + "..."
	}
	return string(body)
}
