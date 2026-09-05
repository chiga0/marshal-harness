package application

import (
	"errors"
	"fmt"
)

// ReasonCode is the closed, secret-safe failure vocabulary exposed by the
// public application port. Callers must branch on this value rather than on
// implementation error text.
type ReasonCode string

const (
	ReasonInvalidRequest             ReasonCode = "invalid-request"
	ReasonPlatformProfileUnavailable ReasonCode = "platform-profile-unavailable"
	ReasonOwnerUnavailable           ReasonCode = "production-owner-unavailable"
	ReasonOwnerNotCurrent            ReasonCode = "production-owner-not-current"
	ReasonBridgeUnavailable          ReasonCode = "production-bridge-unavailable"
	ReasonCompositionIncomplete      ReasonCode = "production-composition-incomplete"
	ReasonAuthorityConflict          ReasonCode = "authority-conflict"
	ReasonRecoveryRequired           ReasonCode = "recovery-required"
	ReasonAttemptStillRunning        ReasonCode = "attempt-still-running"
)

// Error is intentionally closed and input-free. Detail belongs in durable,
// redacted Outcome evidence rather than an API error string.
type Error struct {
	Operation string
	Reason    ReasonCode
}

func (e *Error) Error() string {
	if e == nil || !validReason(e.Reason) {
		return "application: unavailable"
	}
	return fmt.Sprintf("application: %s", e.Reason)
}

func NewError(operation string, reason ReasonCode) error {
	return &Error{Operation: operation, Reason: reason}
}

func HasReason(err error, reason ReasonCode) bool {
	var applicationError *Error
	return errors.As(err, &applicationError) && applicationError.Reason == reason
}

func validReason(reason ReasonCode) bool {
	switch reason {
	case ReasonInvalidRequest, ReasonPlatformProfileUnavailable, ReasonOwnerUnavailable, ReasonOwnerNotCurrent, ReasonBridgeUnavailable, ReasonCompositionIncomplete, ReasonAuthorityConflict, ReasonRecoveryRequired, ReasonAttemptStillRunning:
		return true
	default:
		return false
	}
}
