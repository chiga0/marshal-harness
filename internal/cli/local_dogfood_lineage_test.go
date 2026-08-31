package cli

import (
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
)

func TestValidateLocalAttemptEventsAcceptsExclusiveSealedAndLegacyLineages(t *testing.T) {
	const attemptID = "attempt:local-lineage"
	sealed := []domain.RunEvent{
		localLineageEvent(attemptID, "run.start-outcome", "marshal-run-start-projector", domain.StateReady, domain.StateRunning,
			map[string]any{"protocolRevision": "run-start-outcome/v2", "dispatchObservationDigest": "dispatch"}),
		localLineageEvent(attemptID, "worker.completed", "marshal-production-runtime", domain.StateRunning, domain.StateVerifying,
			map[string]any{"dispatchObservationDigest": "dispatch", "ingressObservationDigest": "ingress"}),
	}
	if isSealed, err := validateLocalAttemptEvents(sealed, attemptID, "dispatch", "ingress"); err != nil || !isSealed {
		t.Fatalf("sealed lineage: sealed=%v err=%v", isSealed, err)
	}

	legacy := []domain.RunEvent{
		localLineageEvent(attemptID, "worker.started", "marshal-worker-runner", "", "",
			map[string]any{"dispatchObservationDigest": "dispatch"}),
		localLineageEvent(attemptID, "worker.completed", "marshal-worker-runner", "", "",
			map[string]any{"dispatchObservationDigest": "dispatch", "ingressObservationDigest": "ingress"}),
	}
	if isSealed, err := validateLocalAttemptEvents(legacy, attemptID, "dispatch", "ingress"); err != nil || isSealed {
		t.Fatalf("legacy lineage: sealed=%v err=%v", isSealed, err)
	}
}

func TestValidateLocalAttemptEventsRejectsMixedTamperedAndNonAdjacentLineages(t *testing.T) {
	const attemptID = "attempt:local-lineage"
	sealedStart := localLineageEvent(attemptID, "run.start-outcome", "marshal-run-start-projector", domain.StateReady, domain.StateRunning,
		map[string]any{"protocolRevision": "run-start-outcome/v2", "dispatchObservationDigest": "dispatch"})
	sealedComplete := localLineageEvent(attemptID, "worker.completed", "marshal-production-runtime", domain.StateRunning, domain.StateVerifying,
		map[string]any{"dispatchObservationDigest": "dispatch", "ingressObservationDigest": "ingress"})
	legacyStart := localLineageEvent(attemptID, "worker.started", "marshal-worker-runner", "", "",
		map[string]any{"dispatchObservationDigest": "dispatch"})

	for _, test := range []struct {
		name   string
		events []domain.RunEvent
	}{
		{name: "mixed producers", events: []domain.RunEvent{sealedStart, legacyStart, sealedComplete}},
		{name: "tampered dispatch", events: []domain.RunEvent{
			localLineageEvent(attemptID, "run.start-outcome", "marshal-run-start-projector", domain.StateReady, domain.StateRunning,
				map[string]any{"protocolRevision": "run-start-outcome/v2", "dispatchObservationDigest": "other"}),
			sealedComplete,
		}},
		{name: "non adjacent", events: []domain.RunEvent{
			sealedStart,
			localLineageEvent(attemptID, "attempt.noop", "marshal-production-runtime", domain.StateRunning, domain.StateRunning, map[string]any{}),
			sealedComplete,
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := validateLocalAttemptEvents(test.events, attemptID, "dispatch", "ingress"); err == nil {
				t.Fatal("hostile lineage was accepted")
			}
		})
	}
}

func localLineageEvent(attemptID, eventType, actorID string, from, to domain.State, payload map[string]any) domain.RunEvent {
	return domain.RunEvent{
		AttemptID: attemptID,
		Type:      eventType,
		StateFrom: from,
		StateTo:   to,
		Actor:     &domain.Actor{Type: "system", ID: actorID},
		Payload:   payload,
	}
}
