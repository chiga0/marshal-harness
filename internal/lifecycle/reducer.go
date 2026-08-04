package lifecycle

import (
	"errors"
	"fmt"

	"github.com/chiga0/marshal-harness/internal/domain"
)

var ErrInvalidTransition = errors.New("invalid lifecycle transition")

var allowed = map[domain.State]map[domain.State]bool{
	domain.StateCreated:         {domain.StatePlanned: true},
	domain.StatePlanned:         {domain.StateReady: true},
	domain.StateReady:           {domain.StateRunning: true},
	domain.StateRunning:         {domain.StateVerifying: true, domain.StateRetryPending: true, domain.StateBlocked: true},
	domain.StateRetryPending:    {domain.StateRunning: true},
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
}

func Reduce(current domain.RunState, event domain.RunEvent, guard Guard) (domain.RunState, error) {
	if err := ValidateTransition(current.State, current.RunID, current.Sequence, event); err != nil {
		return current, err
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
	if (event.StateTo == domain.StateAccepted || event.StateTo == domain.StatePublishing) && (!guard.EvidenceCurrent || !guard.RequiredGatesPass) {
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

func Replay(current domain.RunState, event domain.RunEvent) (domain.RunState, error) {
	if err := ValidateTransition(current.State, current.RunID, current.Sequence, event); err != nil {
		return current, err
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
