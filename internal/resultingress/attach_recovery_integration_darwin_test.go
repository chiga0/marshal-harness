//go:build darwin && arm64

package resultingress

import (
	"context"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

func integrationRebindDigest(label string) string { return canonical.DigestBytes([]byte("label:" + label)) }

// TestRebindRealSupervisorProducesDurableResultIngressOutcome is the real
// Darwin integration test: it starts a real in-process supervisor loop in bound
// state, performs a read-only Attach + bind-authority(owner-successor) rebind
// through the real wire protocol, converts the real response to a
// VerifiedCommandOutcome, then feeds it through NewSupervisorCommandEvidence
// and verifies it passes the same durable validation the orchestrator uses.
// This proves the real supervisor's receipt format is compatible with the
// ResultIngress evidence chain — not just a fake self-consistent outcome.
func TestRebindRealSupervisorProducesDurableResultIngressOutcome(t *testing.T) {
	startedFact := integrationRebindDigest("started")
	observationDigest := integrationRebindDigest("obs")
	child := processsupervisor.ProcessIdentity{PID: 9900, BirthSeconds: 1_700_000_040, BirthMicroseconds: 1, SessionID: 9899, ProcessGroupID: 9899}
	successorHead := integrationRebindDigest("successor-head")
	setup, err := processsupervisor.StartIntegrationTestRebind(startedFact, observationDigest, child)
	if err != nil {
		t.Fatalf("start integration supervisor: %v", err)
	}
	defer setup.Cleanup()
	outcome, err := setup.Rebind(context.Background(), successorHead)
	if err != nil {
		t.Fatalf("real rebind: %v", err)
	}
	if outcome.Command != processsupervisor.CommandBindAuthority || outcome.Status != "ok" || outcome.Disposition != "ok" {
		t.Fatalf("real outcome=%+v", outcome)
	}
	if outcome.Recovery.PostCommand.CurrentAuthorityHead != successorHead {
		t.Fatalf("real post authority head=%s want=%s", outcome.Recovery.PostCommand.CurrentAuthorityHead, successorHead)
	}
	if outcome.ObservationDigest != startedFact {
		t.Fatalf("real observation digest=%s want=%s (SupervisorStartedFactDigest)", outcome.ObservationDigest, startedFact)
	}
	evidence, err := NewSupervisorCommandEvidence(outcome)
	if err != nil {
		t.Fatalf("NewSupervisorCommandEvidence from real outcome: %v", err)
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("real evidence failed validation: %v", err)
	}
	if evidence.BoundAuthorityHead != successorHead {
		t.Fatalf("real evidence bound authority head=%s want=%s", evidence.BoundAuthorityHead, successorHead)
	}
	if evidence.ObservationDigest != startedFact {
		t.Fatalf("real evidence observation digest=%s want=%s", evidence.ObservationDigest, startedFact)
	}
}

var _ = canonical.DigestBytes
