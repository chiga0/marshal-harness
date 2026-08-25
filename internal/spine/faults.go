package spine

import (
	"context"
	"time"

	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// This file provides deterministic fault-injection fixture helpers for the
// R1 walking-skeleton spine negative-test vertical slice (I186-R1 step 7,
// Issue #188 R1-F, ADR 0044 decisions 3/5). All helpers construct
// deterministic fakes and verify closed recovery conclusions; they do not
// wire any production path.

// killProvider returns a FakeProvider that rejects the Exec operation with
// ErrFaultInjected, simulating a worker kill during command execution.
func killProvider() *sandbox.FakeProvider {
	return sandbox.NewFakeProvider(sandbox.FakeConfig{}).WithFaults(
		sandbox.FaultSpec{Operation: sandbox.OperationExec, Fault: sandbox.FaultReject},
	)
}

// lostResponseProvider returns a FakeProvider that drops the Exec response
// with ErrResponseLost, simulating a lost sandbox response.
func lostResponseProvider() *sandbox.FakeProvider {
	return sandbox.NewFakeProvider(sandbox.FakeConfig{}).WithFaults(
		sandbox.FaultSpec{Operation: sandbox.OperationExec, Fault: sandbox.FaultDropResponse},
	)
}

// allOperationsFaulty returns a FakeProvider that rejects every sandbox
// operation. If any code path touches the provider, the fault surfaces
// immediately as ErrFaultInjected — used to prove zero provider side effects.
func allOperationsFaulty() *sandbox.FakeProvider {
	return sandbox.NewFakeProvider(sandbox.FakeConfig{}).WithFaults(
		sandbox.FaultSpec{Operation: sandbox.OperationProvision, Fault: sandbox.FaultReject},
		sandbox.FaultSpec{Operation: sandbox.OperationStage, Fault: sandbox.FaultReject},
		sandbox.FaultSpec{Operation: sandbox.OperationExec, Fault: sandbox.FaultReject},
		sandbox.FaultSpec{Operation: sandbox.OperationInspect, Fault: sandbox.FaultReject},
		sandbox.FaultSpec{Operation: sandbox.OperationTerminate, Fault: sandbox.FaultReject},
	)
}

// expiredContext returns a context whose deadline is already in the past,
// so the first ctx.Err() check in the provider surfaces
// context.DeadlineExceeded without any wall-clock sleep.
func expiredContext() (context.Context, context.CancelFunc) {
	return context.WithDeadline(context.Background(), time.Now().Add(-1*time.Hour))
}

// quarantineIsClean returns true if the ingress quarantine is empty or
// contains only typed rejection records (no admission-semantics entries).
func quarantineIsClean(ingress *resultingress.Ingress) bool {
	for _, rec := range ingress.Quarantine() {
		switch rec.Reason {
		case resultingress.ReasonDigestMismatch,
			resultingress.ReasonRevoked,
			resultingress.ReasonStaleGeneration,
			resultingress.ReasonStaleLease,
			resultingress.ReasonMalformed:
		default:
			return false
		}
	}
	return true
}

// quarantineIsEmpty returns true if the ingress has no quarantine records,
// proving no admission was ever attempted.
func quarantineIsEmpty(ingress *resultingress.Ingress) bool {
	return len(ingress.Quarantine()) == 0
}
