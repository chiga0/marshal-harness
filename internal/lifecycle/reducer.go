package lifecycle

import (
	"errors"
	"fmt"
	"strings"

	"github.com/chiga0/marshal-harness/internal/domain"
)

var ErrInvalidTransition = errors.New("invalid lifecycle transition")

// RepairAuditEventType is the only event type allowed as a same-state audit
// event, including on terminal runs. It never changes business fields.
const RepairAuditEventType = "reconciliation.snapshot-repaired"

// AbortEventType is the explicit operator abort event recorded by
// `marshal task abort`. Its source state set is closed: the ADR 0012 exit
// expresses RETRY_PENDING -> BLOCKED, and the ADR 0029 pre-attempt exit
// expresses PLANNED/READY -> ABORTED for Runs that never produced an
// Attempt. Every other source state or target fails closed.
const AbortEventType = "run.aborted"

// AbortTerminalReason is the fixed terminalReason bound to explicit operator
// aborts of RETRY_PENDING Runs. The operator-provided reason is stored as a
// separate payload field and never inside the terminalReason value.
const AbortTerminalReason = "aborted-by-operator"

// PreAttemptAbortTerminalReason is the fixed terminalReason bound to the
// ADR 0029 pre-attempt abort exit (PLANNED/READY Runs with no Attempt
// records, no publication intent, no SideEffect and no publication fact). It
// is the second and final member of the closed terminalReason set next to
// AbortTerminalReason; free operator text never enters either value.
const PreAttemptAbortTerminalReason = "aborted-before-attempt"

// The issue #68 wall-clock watchdog (watchdog.go) is strictly advisory with
// respect to this transition table: it classifies non-terminal Runs against
// the frozen budgets.RunTimeoutSeconds window and emits guidance sentinels
// pointing at the abort exits frozen here (pre-attempt-abort at the ADR 0029
// PLANNED/READY exit, attempt-abort at the ADR 0012 exit, wait otherwise).
// The watchdog never appends journal events, never writes Run state and adds
// no transition; terminal disposition always happens through the existing
// legal commands.

// PublicationReconcileEventType is the ADR 0026 typed reconciliation event.
// It is the single named exception to terminal-state immutability: it may
// move a BLOCKED run to ACCEPTED after an accept-after-merge reconcile and is
// rejected for every other state combination.
const PublicationReconcileEventType = "publication.reconciled"

// PublicationMergedEventType is the ADR 0032 controlled-merge convergence
// event. It is the only CI_PENDING -> ACCEPTED trigger allowed under
// mergePolicy=policy and must be recorded by the fixed SCMMerger actor; the
// reducer enforces the actor and the closed receipt-bound payload on replay.
const PublicationMergedEventType = "publication.merged"

// MergerActorType and MergerActorID are the frozen producer-authority
// identity of the publication.merged event. Only this exact actor may record
// the controlled-merge convergence.
const (
	MergerActorType = "publisher"
	MergerActorID   = "marshal-scm-merger"
)

// mergedPayloadFields is the ADR 0032 §6 closed payload shape of the
// publication.merged event. Any other key is rejected on replay.
var mergedPayloadFields = []string{
	"intentId",
	"intentDigest",
	"receiptId",
	"receiptDigest",
	"headOid",
	"mergeCommitSha",
	"mergeMethod",
	"publicationDigest",
	"remoteCheckRecordDigest",
}

var allowed = map[domain.State]map[domain.State]bool{
	domain.StateCreated:         {domain.StatePlanned: true},
	domain.StatePlanned:         {domain.StateReady: true},
	domain.StateReady:           {domain.StateRunning: true},
	domain.StateRunning:         {domain.StateVerifying: true, domain.StateRetryPending: true, domain.StateBlocked: true},
	domain.StateRetryPending:    {domain.StateRunning: true, domain.StateBlocked: true},
	domain.StateVerifying:       {domain.StateReviewPending: true},
	domain.StateReviewPending:   {domain.StateReworkRequested: true, domain.StateRejected: true, domain.StateBlocked: true, domain.StateNoChange: true, domain.StatePublishing: true, domain.StateAccepted: true},
	domain.StateReworkRequested: {domain.StateRunning: true},
	domain.StatePublishing:      {domain.StatePublished: true, domain.StateBlocked: true},
	domain.StatePublished:       {domain.StateCIPending: true, domain.StateAccepted: true},
	domain.StateCIPending:       {domain.StateAccepted: true, domain.StateReworkRequested: true, domain.StateBlocked: true},
}

type Guard struct {
	LeaseHeld              bool
	DraftValid             bool
	BaseResolved           bool
	PolicyAllowed          bool
	AdapterProbed          bool
	InputsFrozen           bool
	WorkerProtocolComplete bool
	SnapshotRecorded       bool
	EvidenceCurrent        bool
	ReportComplete         bool
	RequiredGatesPass      bool
	DecisionCurrent        bool
	NoChangeAllowed        bool
	RemoteChecksRequired   bool
	PublicationCurrent     bool
	BudgetAvailable        bool
	AbortAuthorized        bool
	ChildrenStopped        bool
	EvidenceFlushed        bool
	// PreAttemptAbsenceProven is set only after the caller affirmatively
	// proved, against the authoritative storage of the Run, every ADR 0029
	// negative fact: zero Attempt records, no publication intent, no
	// SideEffect record and no publication record or published branch. An
	// unreadable or ambiguous record fails closed instead of setting it.
	PreAttemptAbsenceProven bool
	// ReconcileAuthorized is set only when the SCMMergeReceipt, the
	// PublicationReconcileRecord and the current-ledger recheck have all been
	// validated for an ADR 0026 typed reconciliation.
	ReconcileAuthorized bool
	// MergeAuthorized is set only when the ADR 0032 §5 receipt binding
	// verification has fully passed (authorityNamespaceId, runId, head/base,
	// method, publicationRecordId/publicationDigest triple, mergedBy canonical
	// identity and receiptDigest recomputation). It gates publication.merged.
	MergeAuthorized bool
}

func Reduce(current domain.RunState, event domain.RunEvent, guard Guard) (domain.RunState, error) {
	if err := ValidateTransition(current.State, current.RunID, current.Sequence, event); err != nil {
		return current, err
	}
	if event.Type == RepairAuditEventType {
		if !guard.LeaseHeld {
			return current, fmt.Errorf("%w: run lease is not held", ErrInvalidTransition)
		}
		return Replay(current, event)
	}
	if event.Type == PublicationReconcileEventType {
		if !guard.LeaseHeld {
			return current, fmt.Errorf("%w: run lease is not held", ErrInvalidTransition)
		}
		if !guard.ReconcileAuthorized || !guard.EvidenceCurrent || !guard.PublicationCurrent || !guard.DecisionCurrent {
			return current, fmt.Errorf("%w: reconciliation requires the authorized receipt, record and current-ledger recheck", ErrInvalidTransition)
		}
	}
	if event.Type == PublicationMergedEventType {
		if !guard.LeaseHeld {
			return current, fmt.Errorf("%w: run lease is not held", ErrInvalidTransition)
		}
		// ADR 0032 §6: controlled merge converges only on the fully
		// receipt-bound intent (MergeAuthorized) plus current evidence and
		// publication. Required checks were independently proven by the fresh
		// RemoteCheckRecord bound into the intent, so RequiredGatesPass is
		// deliberately not required here (it gates the never-merge path).
		if !guard.MergeAuthorized || !guard.EvidenceCurrent || !guard.PublicationCurrent {
			return current, fmt.Errorf("%w: controlled merge requires the authorized intent, receipt binding and current-ledger recheck", ErrInvalidTransition)
		}
	}
	if !guard.LeaseHeld {
		return current, fmt.Errorf("%w: run lease is not held", ErrInvalidTransition)
	}
	if current.State == domain.StateCreated && event.StateTo == domain.StatePlanned && !guard.DraftValid {
		return current, fmt.Errorf("%w: valid task draft required", ErrInvalidTransition)
	}
	if current.State == domain.StatePlanned && event.StateTo == domain.StateReady && (!guard.BaseResolved || !guard.PolicyAllowed || !guard.AdapterProbed || !guard.InputsFrozen) {
		return current, fmt.Errorf("%w: resolved base, policy, adapter probe and frozen inputs required", ErrInvalidTransition)
	}
	if event.StateTo == domain.StateRunning && (!guard.BudgetAvailable || event.AttemptID == "") {
		return current, fmt.Errorf("%w: attempt ID and budget required", ErrInvalidTransition)
	}
	if current.State == domain.StateRunning && event.StateTo == domain.StateVerifying && (!guard.WorkerProtocolComplete || !guard.SnapshotRecorded) {
		return current, fmt.Errorf("%w: completed worker protocol and snapshot required", ErrInvalidTransition)
	}
	if event.StateTo == domain.StateReviewPending && (!guard.EvidenceCurrent || !guard.ReportComplete) {
		return current, fmt.Errorf("%w: complete current report required", ErrInvalidTransition)
	}
	// Reconcile path choice (declared): the allowed map never opens a wildcard
	// BLOCKED exit. The ADR 0026 reconcile event instead reuses the
	// StateTo==StateAccepted evidence gate while exempting RequiredGatesPass:
	// the merged head's all-green required checks are independently proven by
	// the re-verified, materialized RemoteCheckRecord, and the ReviewDecision
	// and current-ledger recheck remain mandatory through EvidenceCurrent,
	// DecisionCurrent, PublicationCurrent and ReconcileAuthorized above.
	if (event.StateTo == domain.StateAccepted || event.StateTo == domain.StatePublishing) && (!guard.EvidenceCurrent || (event.Type != PublicationReconcileEventType && event.Type != PublicationMergedEventType && !guard.RequiredGatesPass)) {
		return current, fmt.Errorf("%w: current passing evidence required", ErrInvalidTransition)
	}
	if event.StateTo == domain.StateAccepted && current.State == domain.StateCIPending && !guard.PublicationCurrent {
		return current, fmt.Errorf("%w: current publication and CI evidence required", ErrInvalidTransition)
	}
	if (event.StateTo == domain.StateRetryPending || event.StateTo == domain.StateReworkRequested) && !guard.BudgetAvailable {
		return current, fmt.Errorf("%w: retry budget exhausted", ErrInvalidTransition)
	}
	if current.State == domain.StateReviewPending {
		if event.StateTo != domain.StateReworkRequested && !guard.DecisionCurrent {
			return current, fmt.Errorf("%w: current review decision required", ErrInvalidTransition)
		}
		if event.StateTo == domain.StateReworkRequested && guard.RequiredGatesPass && !guard.DecisionCurrent {
			return current, fmt.Errorf("%w: review decision or failed required gate required", ErrInvalidTransition)
		}
		if event.StateTo == domain.StateNoChange && !guard.NoChangeAllowed {
			return current, fmt.Errorf("%w: task does not allow no-change", ErrInvalidTransition)
		}
	}
	if current.State == domain.StatePublishing && event.StateTo == domain.StatePublished && !guard.PublicationCurrent {
		return current, fmt.Errorf("%w: publication record required", ErrInvalidTransition)
	}
	if current.State == domain.StatePublished && event.StateTo == domain.StateCIPending && !guard.RemoteChecksRequired {
		return current, fmt.Errorf("%w: remote checks were not requested", ErrInvalidTransition)
	}
	if current.State == domain.StatePublished && event.StateTo == domain.StateAccepted && guard.RemoteChecksRequired {
		return current, fmt.Errorf("%w: required remote checks are pending", ErrInvalidTransition)
	}
	if current.State == domain.StateCIPending && !guard.PublicationCurrent {
		return current, fmt.Errorf("%w: current publication evidence required", ErrInvalidTransition)
	}
	// ADR 0029 pre-attempt exit: the PLANNED/READY abort additionally
	// requires the caller's affirmative proof that no Attempt, publication
	// intent, SideEffect or publication fact exists. The proof obligation is
	// stricter than, and independent of, the generic ABORTED gate below.
	if event.Type == AbortEventType && (current.State == domain.StatePlanned || current.State == domain.StateReady) && !guard.PreAttemptAbsenceProven {
		return current, fmt.Errorf("%w: pre-attempt abort requires the proven absence of attempts, publication intent, side effects and publication facts", ErrInvalidTransition)
	}
	if event.StateTo == domain.StateAborted && (!guard.AbortAuthorized || !guard.ChildrenStopped || !guard.EvidenceFlushed) {
		return current, fmt.Errorf("%w: authorized abort with stopped children and flushed evidence required", ErrInvalidTransition)
	}
	next := current
	next.State = event.StateTo
	next.Sequence = event.Sequence
	next.UpdatedAt = event.Timestamp.UTC()
	if event.StateTo == domain.StateRunning {
		next.AttemptsUsed++
		next.CurrentAttemptID = event.AttemptID
	}
	if event.StateTo == domain.StateRetryPending {
		next.OperationalRetriesUsed++
	}
	if event.StateTo == domain.StateReworkRequested {
		next.ReworkRoundsUsed++
	}
	if event.StateTo == domain.StateReviewPending {
		next.ReviewRound++
	}
	if event.StateTo.Terminal() {
		if reason, ok := event.Payload["terminalReason"].(string); ok {
			next.TerminalReason = reason
		}
	}
	return next, nil
}

// ValidateTransition checks durable structural invariants without re-evaluating
// ephemeral runtime guards. It is safe for journal replay.
func ValidateTransition(current domain.State, runID string, sequence uint64, event domain.RunEvent) error {
	if event.Type == RepairAuditEventType {
		if event.RunID != runID || event.Sequence != sequence+1 || event.StateFrom != current || event.StateTo != current {
			return fmt.Errorf("%w: repair audit event identity, sequence or state does not match current state", ErrInvalidTransition)
		}
		return nil
	}
	if event.Type == PublicationReconcileEventType {
		// Single named terminal exception (ADR 0026): only the accept-after-
		// merge typed reconciliation may move BLOCKED to ACCEPTED. Every other
		// terminal state and every other combination of this event type is
		// rejected; the allowed map keeps no wildcard BLOCKED exit.
		if current != domain.StateBlocked || event.StateTo != domain.StateAccepted {
			return fmt.Errorf("%w: publication.reconciled only allows BLOCKED -> ACCEPTED", ErrInvalidTransition)
		}
		if event.Actor == nil || event.Actor.Type != "system" || event.Actor.ID != "marshal-reconciliation" {
			return fmt.Errorf("%w: publication.reconciled must be recorded by system/marshal-reconciliation", ErrInvalidTransition)
		}
		if event.RunID != runID || event.Sequence != sequence+1 || event.StateFrom != current {
			return fmt.Errorf("%w: event identity or sequence does not match current state", ErrInvalidTransition)
		}
		return nil
	}
	if event.Type == PublicationMergedEventType {
		// ADR 0032 §6: the controlled-merge convergence event is the single
		// CI_PENDING -> ACCEPTED trigger under mergePolicy=policy and carries
		// the fixed producer actor and closed receipt-bound payload.
		if current != domain.StateCIPending || event.StateTo != domain.StateAccepted {
			return fmt.Errorf("%w: publication.merged only allows CI_PENDING -> ACCEPTED", ErrInvalidTransition)
		}
		if event.Actor == nil || event.Actor.Type != MergerActorType || event.Actor.ID != MergerActorID {
			return fmt.Errorf("%w: publication.merged must be recorded by publisher/marshal-scm-merger", ErrInvalidTransition)
		}
		if event.RunID != runID || event.Sequence != sequence+1 || event.StateFrom != current {
			return fmt.Errorf("%w: event identity or sequence does not match current state", ErrInvalidTransition)
		}
		if err := validateMergedPayload(event.Payload); err != nil {
			return err
		}
		return nil
	}
	if event.Type == AbortEventType {
		// Closed source-state set (ADR 0012 + ADR 0029): the explicit abort
		// event never borrows the generic StateAborted structural exception
		// that other event types (control-plane interventions) use.
		switch current {
		case domain.StateRetryPending:
			if event.StateTo != domain.StateBlocked {
				return fmt.Errorf("%w: run.aborted allows RETRY_PENDING -> BLOCKED only", ErrInvalidTransition)
			}
		case domain.StatePlanned, domain.StateReady:
			if event.StateTo != domain.StateAborted {
				return fmt.Errorf("%w: pre-attempt run.aborted allows PLANNED/READY -> ABORTED only", ErrInvalidTransition)
			}
			if event.AttemptID != "" {
				return fmt.Errorf("%w: pre-attempt run.aborted must not carry an attempt identity", ErrInvalidTransition)
			}
		default:
			return fmt.Errorf("%w: run.aborted source state %s is not eligible", ErrInvalidTransition, current)
		}
		if event.Actor == nil || event.Actor.Type != domain.ControlSourceTypeHuman || strings.TrimSpace(event.Actor.ID) == "" {
			return fmt.Errorf("%w: run.aborted requires a human actor identity", ErrInvalidTransition)
		}
		if err := validateAbortPayload(current, event.Payload); err != nil {
			return err
		}
		if event.RunID != runID || event.Sequence != sequence+1 || event.StateFrom != current {
			return fmt.Errorf("%w: event identity or sequence does not match current state", ErrInvalidTransition)
		}
		return nil
	}
	if current.Terminal() {
		return fmt.Errorf("%w: terminal state %s", ErrInvalidTransition, current)
	}
	if event.RunID != runID || event.Sequence != sequence+1 || event.StateFrom != current {
		return fmt.Errorf("%w: event identity or sequence does not match current state", ErrInvalidTransition)
	}
	if event.StateTo != domain.StateAborted && !allowed[current][event.StateTo] {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, current, event.StateTo)
	}
	return nil
}

// validateAbortPayload freezes the run.aborted payload shape: exactly the
// machine terminalReason bound to the source class plus the operator reason
// as a separate free-text field. Any other key, a missing value or a
// terminalReason outside the closed set fails closed, so a journal entry can
// never carry injected machine fields.
func validateAbortPayload(current domain.State, payload map[string]any) error {
	expected := AbortTerminalReason
	if current == domain.StatePlanned || current == domain.StateReady {
		expected = PreAttemptAbortTerminalReason
	}
	if len(payload) != 2 {
		return fmt.Errorf("%w: run.aborted payload must carry exactly terminalReason and reason", ErrInvalidTransition)
	}
	terminalReason, ok := payload["terminalReason"].(string)
	if !ok || terminalReason != expected {
		return fmt.Errorf("%w: run.aborted terminalReason must be %s", ErrInvalidTransition, expected)
	}
	reason, ok := payload["reason"].(string)
	if !ok || strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: run.aborted reason must be a non-empty operator string", ErrInvalidTransition)
	}
	return nil
}

// validateMergedPayload freezes the publication.merged payload shape: exactly
// the nine closed receipt/intent-bound fields, every value a non-empty
// string. Any missing field, extra key, or non-string value fails closed so
// a journal entry can never carry injected machine fields.
func validateMergedPayload(payload map[string]any) error {
	if len(payload) != len(mergedPayloadFields) {
		return fmt.Errorf("%w: publication.merged payload must carry exactly the closed field set", ErrInvalidTransition)
	}
	for _, key := range mergedPayloadFields {
		value, ok := payload[key].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: publication.merged payload field %s must be a non-empty string", ErrInvalidTransition, key)
		}
	}
	return nil
}

func Replay(current domain.RunState, event domain.RunEvent) (domain.RunState, error) {
	if err := ValidateTransition(current.State, current.RunID, current.Sequence, event); err != nil {
		return current, err
	}
	if event.Type == RepairAuditEventType {
		next := current
		next.Sequence = event.Sequence
		next.UpdatedAt = event.Timestamp.UTC()
		return next, nil
	}
	if event.Type == PublicationReconcileEventType {
		// Replay fails closed on any missing required payload field. The
		// BLOCKED snapshot's Publication value is intentionally preserved: the
		// reconcile migrates the terminal state without rewriting publication
		// identity.
		for _, key := range []string{"receiptDigest", "reconcileId", "publicationDigest", "decisionDigest", "terminalReason"} {
			if payloadString(event.Payload, key) == "" {
				return current, fmt.Errorf("%w: publication.reconciled lacks required payload field %s", ErrInvalidTransition, key)
			}
		}
	}
	next := current
	next.State, next.Sequence, next.UpdatedAt = event.StateTo, event.Sequence, event.Timestamp.UTC()
	if event.Type == "publication.completed" {
		publication := &domain.RunPublication{
			Provider: payloadString(event.Payload, "provider"), Repository: payloadString(event.Payload, "repository"),
			HeadBranch: payloadString(event.Payload, "headBranch"), BaseBranch: payloadString(event.Payload, "baseBranch"),
			ExternalID: payloadString(event.Payload, "externalId"), URI: payloadString(event.Payload, "uri"),
			HeadSHA: payloadString(event.Payload, "headSha"),
		}
		if publication.Provider == "" || publication.Repository == "" || publication.HeadBranch == "" || publication.BaseBranch == "" || publication.ExternalID == "" || publication.URI == "" || publication.HeadSHA == "" {
			return current, fmt.Errorf("%w: publication.completed lacks replay identity", ErrInvalidTransition)
		}
		next.Publication = publication
	}
	if event.StateTo == domain.StateRunning {
		next.AttemptsUsed++
		next.CurrentAttemptID = event.AttemptID
	}
	if event.StateTo == domain.StateRetryPending {
		next.OperationalRetriesUsed++
	}
	if event.StateTo == domain.StateReworkRequested {
		next.ReworkRoundsUsed++
	}
	if event.StateTo == domain.StateReviewPending {
		next.ReviewRound++
	}
	if event.StateTo.Terminal() {
		if reason, ok := event.Payload["terminalReason"].(string); ok {
			next.TerminalReason = reason
		}
	}
	return next, nil
}

func payloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}
