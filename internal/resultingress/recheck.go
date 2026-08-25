package resultingress

import (
	"errors"
	"fmt"
	"time"
)

// ── New sentinel errors (typed, fail-closed) ──────────────────────────────────

var (
	ErrUnknownKind            = errors.New("resultingress: unknown envelope kind")
	ErrOperationMismatch      = errors.New("resultingress: kind-operation mismatch")
	ErrIneligibleRegistration = errors.New("resultingress: ineligible registration")
	ErrIneligibleSnapshot     = errors.New("resultingress: ineligible snapshot")
	ErrIneligibleEvidence     = errors.New("resultingress: ineligible evidence")
	ErrExpired                = errors.New("resultingress: expired")
)

// ── Operation closed enumeration (ADR 0018) ───────────────────────────────────

// Operation is the closed set of DRC operations frozen by ADR 0018.
type Operation string

const (
	OpResult      Operation = "result"
	OpLog         Operation = "log"
	OpCheckpoint  Operation = "checkpoint"
	OpCandidate   Operation = "candidate"
	OpEvidenceRef Operation = "evidence-ref"
	OpHeartbeat   Operation = "heartbeat"
	OpReceipt     Operation = "receipt"
)

// isValidOperation reports whether op is in the ADR 0018 closed set.
func isValidOperation(op Operation) bool {
	switch op {
	case OpResult, OpLog, OpCheckpoint, OpCandidate, OpEvidenceRef, OpHeartbeat, OpReceipt:
		return true
	default:
		return false
	}
}

// ── Kind→Operation closed mapping (ADR 0044 R2) ──────────────────────────────

// kindToOperation maps each EnvelopeKind to its corresponding DRC Operation.
// Returns ok=false for kinds outside the closed set.
func kindToOperation(kind EnvelopeKind) (Operation, bool) {
	switch kind {
	case KindWorkerResult:
		return OpResult, true
	case KindAssessment:
		return OpResult, true
	case KindCandidate:
		return OpCandidate, true
	case KindEvidenceRef:
		return OpEvidenceRef, true
	case KindCheckpoint:
		return OpCheckpoint, true
	case KindHeartbeat:
		return OpHeartbeat, true
	case KindReceipt:
		return OpReceipt, true
	case KindLog:
		return OpLog, true
	default:
		return "", false
	}
}

// ── Hot/Cold path classification (ADR 0044 decision 4) ───────────────────────

// isHotPathKind reports whether kind follows the hot path
// (minimal fencing/replay, no eligibility recheck).
// Hot path kinds: checkpoint, heartbeat, log.
func isHotPathKind(kind EnvelopeKind) bool {
	switch kind {
	case KindCheckpoint, KindHeartbeat, KindLog:
		return true
	default:
		return false
	}
}

// ── Cold path eligibility recheck (ADR 0018 current-ledger recheck) ──────────

// recheckCold verifies that the DRC's RegistrationID, SnapshotDigest, and
// EvidenceDigest match the current ledger binding. Any mismatch fails closed
// with a typed error and quarantine record.
func (i *Ingress) recheckCold(drc DRC, drcDigest, envelopeDigest string, now time.Time) error {
	if drc.RegistrationID != i.ledger.RegistrationID {
		i.recordQuarantine(ReasonIneligibleRegistration, drcDigest, envelopeDigest, now)
		return fmt.Errorf("%w: registrationId mismatch: got %q want %q",
			ErrIneligibleRegistration, drc.RegistrationID, i.ledger.RegistrationID)
	}
	if drc.SnapshotDigest != i.ledger.SnapshotDigest {
		i.recordQuarantine(ReasonIneligibleSnapshot, drcDigest, envelopeDigest, now)
		return fmt.Errorf("%w: snapshotDigest mismatch: got %q want %q",
			ErrIneligibleSnapshot, drc.SnapshotDigest, i.ledger.SnapshotDigest)
	}
	if drc.EvidenceDigest != i.ledger.EvidenceDigest {
		i.recordQuarantine(ReasonIneligibleEvidence, drcDigest, envelopeDigest, now)
		return fmt.Errorf("%w: evidenceDigest mismatch: got %q want %q",
			ErrIneligibleEvidence, drc.EvidenceDigest, i.ledger.EvidenceDigest)
	}
	return nil
}
