package lifecycle

import (
	"fmt"
	"regexp"

	"github.com/chiga0/marshal-harness/internal/domain"
)

const WorkerStoppedEventType = "worker.stopped"

var stopEvidenceDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var stopEvidenceFields = [...]string{
	"stopIntentDigest", "stopRequestDigest", "originalRunAuthorityHead",
	"terminalizationBarrierFactDigest", "processTerminalFactDigest",
	"allocationTerminatedFactDigest", "supervisorClosedFactDigest", "cleanupReleasedFactDigest",
}

// ValidateWorkerStopped checks only the immutable event shape. The producer
// must independently join these references to current authority under lease;
// digest-shaped strings alone never satisfy Guard.StopAuthorized.
func ValidateWorkerStopped(event domain.RunEvent) error {
	if event.Type != WorkerStoppedEventType || event.StateFrom != domain.StateRunning || event.StateTo != domain.StateBlocked || event.Timestamp.IsZero() || domain.ValidateID(event.AttemptID) != nil || event.Actor == nil || event.Actor.Type != "system" || event.Actor.ID != "marshal-core" || len(event.Payload) != len(stopEvidenceFields)+1 {
		return fmt.Errorf("%w: invalid worker.stopped envelope", ErrInvalidTransition)
	}
	for _, name := range stopEvidenceFields {
		value, ok := event.Payload[name].(string)
		if !ok || !stopEvidenceDigest.MatchString(value) {
			return fmt.Errorf("%w: invalid worker.stopped evidence", ErrInvalidTransition)
		}
	}
	switch event.Payload["terminalReason"] {
	case AbortTerminalReason, "attempt-deadline-exceeded", "run-deadline-exceeded":
		return nil
	default:
		return fmt.Errorf("%w: invalid worker.stopped reason", ErrInvalidTransition)
	}
}
