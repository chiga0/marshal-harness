package productionruntime

import (
	"testing"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

func TestStoppedReadBindsOnlyExactCurrentHeadToStoredIntent(t *testing.T) {
	state := stoppedAttemptFixture(t)
	current := application.RunProjection{RunID: state.Identity.RunID, TaskID: state.Identity.TaskID, AttemptID: state.Identity.AttemptID, State: domain.StateBlocked, Sequence: 4, AuthorityHead: canonical.DigestBytes([]byte("stopped-run"))}
	request := application.CancelRunRequest{CurrentRunRequest: application.CurrentRunRequest{RunID: current.RunID, AttemptID: current.AttemptID, ExpectedSequence: current.Sequence, ExpectedAuthorityHead: current.AuthorityHead}}
	bound := bindStoppedReadToOriginalIntent(current, request, state.StopIntent)
	if bound.ExpectedSequence != state.StopIntent.ExpectedSequence || bound.ExpectedAuthorityHead != state.StopIntent.ExpectedAuthorityHead || bound.RequestID != "" || bound.RunID != request.RunID || bound.AttemptID != request.AttemptID {
		t.Fatal("current stopped read did not select original intent without inventing a cancel")
	}
	for name, change := range map[string]func(*application.CancelRunRequest){
		"explicit-cancel": func(r *application.CancelRunRequest) { r.RequestID = "cancel-fresh" },
		"wrong-head": func(r *application.CancelRunRequest) {
			r.ExpectedAuthorityHead = canonical.DigestBytes([]byte("wrong"))
		},
		"wrong-sequence": func(r *application.CancelRunRequest) { r.ExpectedSequence++ },
		"wrong-attempt":  func(r *application.CancelRunRequest) { r.AttemptID = "attempt-other" },
		"wrong-run":      func(r *application.CancelRunRequest) { r.RunID = "run-other" },
		"original-replay": func(r *application.CancelRunRequest) {
			r.ExpectedSequence = state.StopIntent.ExpectedSequence
			r.ExpectedAuthorityHead = state.StopIntent.ExpectedAuthorityHead
		},
	} {
		t.Run(name, func(t *testing.T) {
			original := request
			change(&original)
			if got := bindStoppedReadToOriginalIntent(current, original, state.StopIntent); got != original {
				t.Fatal("non-current or explicit request rewritten")
			}
		})
	}
	current.State = domain.StateRunning
	if got := bindStoppedReadToOriginalIntent(current, request, state.StopIntent); got != request {
		t.Fatal("non-terminal read rewritten")
	}
}

func stoppedAttemptFixture(t *testing.T) resultingress.AttemptAuthorityState {
	t.Helper()
	digest := func(s string) string { return canonical.DigestBytes([]byte(s)) }
	id := resultingress.AttemptIdentity{
		AuthorityNamespaceID:  authority.AuthorityNamespaceId{TenantNamespace: "tenant-1", ControlPlaneId: "core-1", AuthorityScopeId: "scope-1"},
		AuthorityNamespaceRef: "authority:test", TaskID: "task-1", RunID: "run-1", AttemptID: "attempt-1", AllocationID: "allocation-1", LeaseID: "lease-1",
		LeaseDigest: digest("lease"), DispatchGeneration: 7, FencingTokenDigest: digest("fence"), OrchestratorID: "orchestrator-1", RunAuthorityDigest: digest("run"),
	}
	intent, err := resultingress.SealAttemptStopIntent(id, resultingress.AttemptStopIntent{RequestID: "cancel-1", ExpectedSequence: 3, ExpectedAuthorityHead: digest("current-run"), OperatorUID: 501, ObservedAt: "2026-09-06T00:00:00Z", Category: resultingress.StopOperatorRequest})
	if err != nil {
		t.Fatal(err)
	}
	return resultingress.AttemptAuthorityState{Identity: id, HeadDigest: digest("head"), BarrierDigest: digest("barrier"), AdmissionClosed: true, TerminalGeneration: 8, StopIntent: intent, EligibilityTerminal: intent.Eligibility(), ProcessTerminalDigest: digest("process"), AllocationTerminalDigest: digest("allocation"), SupervisorClosedDigest: digest("supervisor"), CleanupCompletedDigest: digest("completed"), CleanupReleasedDigest: digest("released")}
}

func TestStopProjectionRequiresExactTerminalEligibility(t *testing.T) {
	state := stoppedAttemptFixture(t)
	projection, err := terminalEligibilityProjection(state)
	if err != nil || projection.TerminalState != dispatch.LeaseStateCompleted || projection.CompletionReason != dispatch.CompletionReasonAttemptAborted || projection.AttemptAuthorityHeadDigest != state.BarrierDigest {
		t.Fatalf("projection=%+v err=%v", projection, err)
	}
	for name, mutate := range map[string]func(*resultingress.AttemptAuthorityState){
		"open-admission": func(s *resultingress.AttemptAuthorityState) { s.AdmissionClosed = false },
		"generation":     func(s *resultingress.AttemptAuthorityState) { s.TerminalGeneration++ },
		"admitted-result": func(s *resultingress.AttemptAuthorityState) {
			s.CommittedResultFactDigest = canonical.DigestBytes([]byte("late"))
		},
		"barrier-admission": func(s *resultingress.AttemptAuthorityState) { s.BarrierAdmissionSequence = 1 },
		"changed-intent":    func(s *resultingress.AttemptAuthorityState) { s.StopIntent.OperatorUID++ },
		"completed-instead": func(s *resultingress.AttemptAuthorityState) {
			s.EligibilityTerminal.CompletionReason = resultingress.TerminalAttemptCompleted
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := state
			mutate(&s)
			if _, err := terminalEligibilityProjection(s); err == nil {
				t.Fatal("forged projection accepted")
			}
		})
	}
}

func TestStopEventRequiresCompleteCleanupNotOnlySignal(t *testing.T) {
	state := stoppedAttemptFixture(t)
	payload, err := stopEventPayload(state)
	if err != nil || payload["stopIntentDigest"] != state.StopIntent.IntentDigest || len(payload) != 9 {
		t.Fatalf("payload=%v err=%v", payload, err)
	}
	for _, missing := range []string{"barrier", "process", "allocation", "supervisor", "completed", "released", "intent"} {
		t.Run(missing, func(t *testing.T) {
			s := state
			switch missing {
			case "barrier":
				s.BarrierDigest = ""
			case "process":
				s.ProcessTerminalDigest = ""
			case "allocation":
				s.AllocationTerminalDigest = ""
			case "supervisor":
				s.SupervisorClosedDigest = ""
			case "completed":
				s.CleanupCompletedDigest = ""
			case "released":
				s.CleanupReleasedDigest = ""
			case "intent":
				s.StopIntent = resultingress.AttemptStopIntent{}
			}
			if _, err := stopEventPayload(s); err == nil {
				t.Fatal("incomplete cleanup accepted")
			}
		})
	}
}
