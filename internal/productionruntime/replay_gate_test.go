package productionruntime

import (
	"testing"

	"github.com/chiga0/marshal-harness/internal/resultingress"
)

// replayGateDigest is a non-empty stand-in for any fact/digest field.
// replayGateAccepts only inspects emptiness, so the exact value is irrelevant.
const replayGateDigest = "sha256:replay-gate"

// replayGateCommon returns a state with the common launch/head/pending
// conditions satisfied: launch authorized at the current head with no pending
// effect intent. Callers mutate path-specific fields.
func replayGateCommon() resultingress.AttemptAuthorityState {
	return resultingress.AttemptAuthorityState{LaunchAuthorizedDigest: replayGateDigest, HeadDigest: replayGateDigest}
}

// replayGatePathAComplete returns a state with path A (staging provision
// effect+receipt) complete and every existing-worktree bind/release field
// empty, on top of the common conditions.
func replayGatePathAComplete() resultingress.AttemptAuthorityState {
	s := replayGateCommon()
	s.AllocationProvisionEffectDigest = replayGateDigest
	s.AllocationProvisionReceiptDigest = replayGateDigest
	return s
}

// replayGatePathBComplete returns a state with path B (existing-worktree bind
// intent+receipt+digest) complete, no release, reservation present and every
// allocation provision field empty, on top of the common conditions.
func replayGatePathBComplete() resultingress.AttemptAuthorityState {
	s := replayGateCommon()
	s.ExistingWorktreeBindIntentFactDigest = replayGateDigest
	s.ExistingWorktreeBindReceiptFactDigest = replayGateDigest
	s.ExistingWorktreeBindReceiptDigest = replayGateDigest
	s.ReservationFactDigest = replayGateDigest
	return s
}

func TestReplayGateAcceptsPathAComplete(t *testing.T) {
	if !replayGateAccepts(replayGatePathAComplete()) {
		t.Fatal("path A complete was rejected")
	}
}

func TestReplayGateAcceptsPathBComplete(t *testing.T) {
	if !replayGateAccepts(replayGatePathBComplete()) {
		t.Fatal("path B complete was rejected")
	}
}

// TestReplayGateRejectsPartialPathA proves a partial path A (effect without
// receipt) is response-loss evidence and fails closed for recovery.
func TestReplayGateRejectsPartialPathA(t *testing.T) {
	s := replayGatePathAComplete()
	s.AllocationProvisionReceiptDigest = ""
	if replayGateAccepts(s) {
		t.Fatal("partial path A (effect without receipt) was accepted")
	}
}

// TestReplayGateRejectsPartialPathB proves a partial path B (bind intent
// without receipt) is response-loss evidence and fails closed for recovery.
func TestReplayGateRejectsPartialPathB(t *testing.T) {
	s := replayGatePathBComplete()
	s.ExistingWorktreeBindReceiptFactDigest = ""
	s.ExistingWorktreeBindReceiptDigest = ""
	if replayGateAccepts(s) {
		t.Fatal("partial path B (intent without receipt) was accepted")
	}
}

// TestReplayGateRejectsMixed proves a mixed state (both path A and path B
// fields populated) is not a single replayable path and fails closed.
func TestReplayGateRejectsMixed(t *testing.T) {
	s := replayGatePathAComplete()
	s.ExistingWorktreeBindIntentFactDigest = replayGateDigest
	s.ExistingWorktreeBindReceiptFactDigest = replayGateDigest
	s.ExistingWorktreeBindReceiptDigest = replayGateDigest
	s.ReservationFactDigest = replayGateDigest
	if replayGateAccepts(s) {
		t.Fatal("mixed path A + path B was accepted")
	}
}

// TestReplayGateRejectsReleasedPathB proves a complete path B with a release
// intent has moved past the bind-receipt replay point and fails closed.
func TestReplayGateRejectsReleasedPathB(t *testing.T) {
	s := replayGatePathBComplete()
	s.ExistingWorktreeReleaseIntentFactDigest = replayGateDigest
	if replayGateAccepts(s) {
		t.Fatal("released path B was accepted")
	}
}

// TestReplayGateRejectsPathBWithoutReservation proves path B requires a
// present reservation fact digest; its absence fails closed.
func TestReplayGateRejectsPathBWithoutReservation(t *testing.T) {
	s := replayGatePathBComplete()
	s.ReservationFactDigest = ""
	if replayGateAccepts(s) {
		t.Fatal("path B without reservation was accepted")
	}
}

// TestReplayGateRejectsPathBWithProvisionLeak proves a stray allocation
// provision effect alongside a complete path B is a mixed state and fails
// closed.
func TestReplayGateRejectsPathBWithProvisionLeak(t *testing.T) {
	s := replayGatePathBComplete()
	s.AllocationProvisionEffectDigest = replayGateDigest
	if replayGateAccepts(s) {
		t.Fatal("path B with a leaked provision effect was accepted")
	}
}

// TestReplayGateRejectsLaunchNotAuthorized proves the common launch condition
// is mandatory: no launch authorization fails closed even for a complete path.
func TestReplayGateRejectsLaunchNotAuthorized(t *testing.T) {
	s := replayGatePathAComplete()
	s.LaunchAuthorizedDigest = ""
	if replayGateAccepts(s) {
		t.Fatal("path A without launch authorization was accepted")
	}
}

// TestReplayGateRejectsHeadDrift proves the head must equal the launch
// authorization head; drift fails closed.
func TestReplayGateRejectsHeadDrift(t *testing.T) {
	s := replayGatePathAComplete()
	s.HeadDigest = "sha256:other"
	if replayGateAccepts(s) {
		t.Fatal("path A with head drift was accepted")
	}
}

// TestReplayGateRejectsPendingEffectIntent proves a pending effect intent
// blocks replay even for an otherwise complete path.
func TestReplayGateRejectsPendingEffectIntent(t *testing.T) {
	s := replayGatePathAComplete()
	s.PendingEffectIntentFactDigest = replayGateDigest
	if replayGateAccepts(s) {
		t.Fatal("path A with a pending effect intent was accepted")
	}
}
