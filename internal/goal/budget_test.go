package goal

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/chiga0/marshal-harness/internal/authority"
)

// acceptThroughAdmission produces an accepted revision and its reservation
// plan through the pure admission evaluator, guaranteeing that the budget
// ledger fixtures exercise exactly the records admission emits.
func acceptThroughAdmission(t *testing.T, state AuthorityState, policy AdmissionPolicy, proposal GoalPlanProposal) (AcceptedGoalPlanRevision, []ReservationRequest) {
	t.Helper()
	decision := Evaluate(proposalBytes(t, proposal), state, policy)
	if !decision.Accepted {
		t.Fatalf("expected acceptance, got %+v", decision.Rejection)
	}
	return *decision.Revision, decision.ReservationPlan
}

func manualRevision(t *testing.T, ledger *BudgetLedger, planRevision int64, previousDigest string) AcceptedGoalPlanRevision {
	t.Helper()
	snapshotDigest, err := ledger.Snapshot().Digest()
	if err != nil {
		t.Fatalf("snapshot digest: %v", err)
	}
	return AcceptedGoalPlanRevision{
		AuthorityNamespaceId: testNamespace(),
		GoalId:               "goal-1",
		PlanRevision:         planRevision,
		PreviousPlanDigest:   previousDigest,
		ProposalDigest:       digestOfLiteral(fmt.Sprintf("proposal-%d", planRevision)),
		PolicyDigest:         digestOfLiteral("policy"),
		BudgetSnapshotDigest: snapshotDigest,
		Nodes:                []GoalNode{validNode("m1")},
		Edges:                []GoalEdge{},
		Supersessions:        []NodeSupersession{},
	}
}

func manualRequest(id string, planRevision int64, estimate NodeEstimate) ReservationRequest {
	return ReservationRequest{
		ReservationId:  "reservation:" + id,
		IdempotencyKey: "reserve:" + id,
		GoalId:         "goal-1",
		NodeId:         id,
		PlanRevision:   planRevision,
		CommandId:      "materialize:" + id,
		Estimate:       estimate,
	}
}

func TestBudgetLedgerApplyPlanCreatesLiveReservations(t *testing.T) {
	state := testAuthorityState(t)
	policy := testPolicy()
	proposal := validProposal()
	proposal.Nodes = []GoalNode{validNode("n1"), validNode("n2")}
	proposal.Edges = []GoalEdge{{From: "n1", To: "n2", Kind: EdgeKindDependsOn}}
	revision, requests := acceptThroughAdmission(t, state, policy, proposal)

	ledger, err := NewBudgetLedger(state.Budget)
	if err != nil {
		t.Fatalf("NewBudgetLedger: %v", err)
	}
	created, err := ledger.ApplyPlan(revision, requests)
	if err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("ApplyPlan created %d reservations, want 2", len(created))
	}
	if ledger.LiveReservationCount() != 2 {
		t.Fatalf("live reservation count = %d, want 2", ledger.LiveReservationCount())
	}
	if got := len(ledger.AcceptedRevisions()); got != 1 {
		t.Fatalf("accepted revisions = %d, want 1", got)
	}
	if ledger.Snapshot().Used.PlanRevisions != 1 {
		t.Fatalf("used planRevisions = %d, want 1", ledger.Snapshot().Used.PlanRevisions)
	}
	for _, reservation := range created {
		if reservation.State != ReservationStateReserved || reservation.Revision != 1 {
			t.Fatalf("reservation %s created in state %s revision %d", reservation.ReservationId, reservation.State, reservation.Revision)
		}
		if err := reservation.Validate(); err != nil {
			t.Fatalf("reservation record invalid: %v", err)
		}
	}
	if got := len(ledger.Events()); got != 2 {
		t.Fatalf("history carries %d events, want 2", got)
	}
}

func TestBudgetLedgerReplayNeverCreatesSecondReservation(t *testing.T) {
	state := testAuthorityState(t)
	revision, requests := acceptThroughAdmission(t, state, testPolicy(), validProposal())
	ledger, err := NewBudgetLedger(state.Budget)
	if err != nil {
		t.Fatalf("NewBudgetLedger: %v", err)
	}
	first, err := ledger.ApplyPlan(revision, requests)
	if err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}
	replayed, err := ledger.ApplyPlan(revision, requests)
	if err != nil {
		t.Fatalf("replayed ApplyPlan must be idempotent: %v", err)
	}
	if !reflectDeepEqualReservations(first, replayed) {
		t.Fatal("replayed ApplyPlan returned different reservations")
	}
	if ledger.LiveReservationCount() != len(first) {
		t.Fatalf("replay changed the live reservation count to %d", ledger.LiveReservationCount())
	}
	if got := len(ledger.AcceptedRevisions()); got != 1 {
		t.Fatalf("replay appended a second revision: %d", got)
	}
	if got := len(ledger.Events()); got != len(first) {
		t.Fatalf("replay appended extra events: %d", got)
	}

	// A replay carrying diverging content fails closed.
	diverging := make([]ReservationRequest, len(requests))
	copy(diverging, requests)
	diverging[0].Estimate.Tokens++
	if _, err := ledger.ApplyPlan(revision, diverging); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("diverging replay returned %v, want ErrIdempotencyConflict", err)
	}
}

func reflectDeepEqualReservations(first, second []BudgetReservation) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if !first[index].Equal(second[index]) {
			return false
		}
	}
	return true
}

func TestBudgetLedgerApplyPlanCASConflictLeavesZeroLiveReservations(t *testing.T) {
	state := testAuthorityState(t)
	revision, requests := acceptThroughAdmission(t, state, testPolicy(), validProposal())
	ledger, err := NewBudgetLedger(state.Budget)
	if err != nil {
		t.Fatalf("NewBudgetLedger: %v", err)
	}

	t.Run("first revision must not bind a predecessor", func(t *testing.T) {
		broken := revision
		broken.PreviousPlanDigest = digestOfLiteral("forged")
		if _, err := ledger.ApplyPlan(broken, requests); !errors.Is(err, ErrPlanRevisionConflict) {
			t.Fatalf("forged predecessor returned %v, want ErrPlanRevisionConflict", err)
		}
	})
	t.Run("revision number must advance by one", func(t *testing.T) {
		broken := revision
		broken.PlanRevision = 7
		if _, err := ledger.ApplyPlan(broken, requests); !errors.Is(err, ErrPlanRevisionConflict) {
			t.Fatalf("skipped revision returned %v, want ErrPlanRevisionConflict", err)
		}
	})
	t.Run("budget snapshot binding must match", func(t *testing.T) {
		broken := revision
		broken.BudgetSnapshotDigest = digestOfLiteral("stale-snapshot")
		if _, err := ledger.ApplyPlan(broken, requests); !errors.Is(err, ErrPlanRevisionConflict) {
			t.Fatalf("stale snapshot returned %v, want ErrPlanRevisionConflict", err)
		}
	})
	t.Run("foreign goal rejected", func(t *testing.T) {
		broken := revision
		broken.GoalId = "goal-9"
		if _, err := ledger.ApplyPlan(broken, requests); !errors.Is(err, ErrPlanRevisionConflict) {
			t.Fatalf("foreign goal returned %v, want ErrPlanRevisionConflict", err)
		}
	})

	if ledger.LiveReservationCount() != 0 {
		t.Fatalf("failed transactions left %d live reservations", ledger.LiveReservationCount())
	}
	if len(ledger.AcceptedRevisions()) != 0 {
		t.Fatal("failed transactions appended revisions")
	}
}

func TestBudgetLedgerApplyPlanNeverOversells(t *testing.T) {
	budget := validBudgetRecord()
	budget.Limits.MaxTotalRuns = 2
	ledger, err := NewBudgetLedger(budget)
	if err != nil {
		t.Fatalf("NewBudgetLedger: %v", err)
	}
	revision := manualRevision(t, ledger, 1, "")
	overselling := []ReservationRequest{manualRequest("big", 1, NodeEstimate{Runs: 3, Attempts: 1})}
	if _, err := ledger.ApplyPlan(revision, overselling); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("overselling reservation returned %v, want ErrBudgetExhausted", err)
	}
	if ledger.LiveReservationCount() != 0 {
		t.Fatalf("overselling attempt left %d live reservations", ledger.LiveReservationCount())
	}

	t.Run("duplicate identities fail closed", func(t *testing.T) {
		duplicate := []ReservationRequest{
			manualRequest("dup", 1, NodeEstimate{Runs: 1, Attempts: 1}),
			manualRequest("dup", 1, NodeEstimate{Runs: 1, Attempts: 1}),
		}
		if _, err := ledger.ApplyPlan(revision, duplicate); !errors.Is(err, ErrPlanRevisionConflict) {
			t.Fatalf("duplicate reservationId returned %v, want ErrPlanRevisionConflict", err)
		}
	})
	t.Run("stale plan revision binding fails closed", func(t *testing.T) {
		stale := []ReservationRequest{manualRequest("stale", 9, NodeEstimate{Runs: 1, Attempts: 1})}
		if _, err := ledger.ApplyPlan(revision, stale); !errors.Is(err, ErrPlanRevisionConflict) {
			t.Fatalf("stale request binding returned %v, want ErrPlanRevisionConflict", err)
		}
	})
	if ledger.LiveReservationCount() != 0 {
		t.Fatalf("failed attempts left %d live reservations", ledger.LiveReservationCount())
	}
}

func TestBudgetLedgerSecondRevisionChainAndSnapshotBinding(t *testing.T) {
	state := testAuthorityState(t)
	policy := testPolicy()
	firstProposal := validProposal()
	firstRevision, firstRequests := acceptThroughAdmission(t, state, policy, firstProposal)

	ledger, err := NewBudgetLedger(state.Budget)
	if err != nil {
		t.Fatalf("NewBudgetLedger: %v", err)
	}
	if _, err := ledger.ApplyPlan(firstRevision, firstRequests); err != nil {
		t.Fatalf("ApplyPlan revision 1: %v", err)
	}

	second := manualRevision(t, ledger, 2, "")
	firstDigest, err := firstRevision.Digest()
	if err != nil {
		t.Fatalf("first revision digest: %v", err)
	}
	second.PreviousPlanDigest = firstDigest
	second.Nodes = []GoalNode{validNode("m2")}
	secondRequests := []ReservationRequest{manualRequest("m2", 2, NodeEstimate{Runs: 1, Attempts: 1})}
	if _, err := ledger.ApplyPlan(second, secondRequests); err != nil {
		t.Fatalf("ApplyPlan revision 2: %v", err)
	}
	if ledger.Snapshot().Used.PlanRevisions != 2 {
		t.Fatalf("used planRevisions = %d, want 2", ledger.Snapshot().Used.PlanRevisions)
	}
	if ledger.LiveReservationCount() != 2 {
		t.Fatalf("live reservation count = %d, want 2", ledger.LiveReservationCount())
	}

	// A revision bound to the stale pre-revision-1 snapshot fails closed.
	secondDigest, err := second.Digest()
	if err != nil {
		t.Fatalf("second revision digest: %v", err)
	}
	stale := manualRevision(t, ledger, 3, secondDigest)
	staleSnapshotDigest, err := state.Budget.Digest()
	if err != nil {
		t.Fatalf("stale snapshot digest: %v", err)
	}
	stale.BudgetSnapshotDigest = staleSnapshotDigest
	stale.Nodes = []GoalNode{validNode("m3")}
	if _, err := ledger.ApplyPlan(stale, []ReservationRequest{manualRequest("m3", 3, NodeEstimate{Runs: 1, Attempts: 1})}); !errors.Is(err, ErrPlanRevisionConflict) {
		t.Fatalf("stale snapshot binding returned %v, want ErrPlanRevisionConflict", err)
	}
	if ledger.Snapshot().Used.PlanRevisions != 2 {
		t.Fatalf("failed stale apply changed used planRevisions to %d", ledger.Snapshot().Used.PlanRevisions)
	}
}

func TestBudgetLedgerCommitSettleReleaseExpireStateMachine(t *testing.T) {
	state := testAuthorityState(t)
	proposal := validProposal()
	proposal.Nodes = []GoalNode{validNode("n1"), validNode("n2")}
	proposal.Edges = []GoalEdge{{From: "n1", To: "n2", Kind: EdgeKindDependsOn}}
	revision, requests := acceptThroughAdmission(t, state, testPolicy(), proposal)
	ledger, err := NewBudgetLedger(state.Budget)
	if err != nil {
		t.Fatalf("NewBudgetLedger: %v", err)
	}
	if _, err := ledger.ApplyPlan(revision, requests); err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}
	reservationID := requests[0].ReservationId

	// reserved → committed, with lost-response replay idempotency.
	committed, err := ledger.Commit(reservationID, 1)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if committed.State != ReservationStateCommitted || committed.Revision != 2 {
		t.Fatalf("committed reservation in state %s revision %d", committed.State, committed.Revision)
	}
	replayed, err := ledger.Commit(reservationID, 1)
	if err != nil {
		t.Fatalf("lost-response replay of Commit must be idempotent: %v", err)
	}
	if !replayed.Equal(committed) {
		t.Fatal("replayed Commit returned a different record")
	}
	if _, err := ledger.Commit(reservationID, 2); !errors.Is(err, ErrReservationConflict) {
		t.Fatalf("Commit with stale expectation returned %v, want ErrReservationConflict", err)
	}

	// committed → settled within the estimate.
	actual := NodeEstimate{Runs: 1, Attempts: 1, WallTimeSeconds: 300, ComputeUnits: 1, Tokens: 500, ArtifactBytes: 512}
	settled, decision, err := ledger.Settle(reservationID, 2, actual)
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if settled.State != ReservationStateSettled || settled.Actual == nil || *settled.Actual != actual {
		t.Fatalf("settled reservation malformed: %+v", settled)
	}
	if decision.HaltNewDispatch {
		t.Fatalf("settlement within the estimate must not halt dispatch: %+v", decision)
	}
	used := ledger.Snapshot().Used
	if used.TotalRuns != 1 || used.Tokens != 500 {
		t.Fatalf("settlement did not accumulate usage: %+v", used)
	}

	// Duplicate settle with identical actuals is idempotent.
	duplicate, _, err := ledger.Settle(reservationID, 2, actual)
	if err != nil {
		t.Fatalf("duplicate Settle with identical actuals must be idempotent: %v", err)
	}
	if !duplicate.Equal(settled) {
		t.Fatal("duplicate Settle returned a different record")
	}
	// Duplicate settle with diverging actuals fails closed.
	if _, _, err := ledger.Settle(reservationID, 2, NodeEstimate{Runs: 2, Attempts: 1}); !errors.Is(err, ErrReservationConflict) {
		t.Fatalf("duplicate Settle with diverging actuals returned %v, want ErrReservationConflict", err)
	}
	// Settled is terminal.
	if _, err := ledger.Commit(reservationID, 3); !errors.Is(err, ErrReservationConflict) {
		t.Fatalf("Commit from settled returned %v, want ErrReservationConflict", err)
	}

	// Second reservation: reserved → released, stale revision release fails.
	secondID := requests[1].ReservationId
	if _, err := ledger.Release(secondID, 1, 99); !errors.Is(err, ErrReservationStale) {
		t.Fatalf("stale revision release returned %v, want ErrReservationStale", err)
	}
	released, err := ledger.Release(secondID, 1, 1)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if released.State != ReservationStateReleased || released.Revision != 2 {
		t.Fatalf("released reservation in state %s revision %d", released.State, released.Revision)
	}
	replayedRelease, err := ledger.Release(secondID, 1, 1)
	if err != nil {
		t.Fatalf("replayed Release must be idempotent: %v", err)
	}
	if !replayedRelease.Equal(released) {
		t.Fatal("replayed Release returned a different record")
	}
	if ledger.LiveReservationCount() != 0 {
		t.Fatalf("live reservation count = %d after settle+release, want 0", ledger.LiveReservationCount())
	}
}

func TestBudgetLedgerIllegalTransitionsFailClosed(t *testing.T) {
	state := testAuthorityState(t)
	revision, requests := acceptThroughAdmission(t, state, testPolicy(), validProposal())
	ledger, err := NewBudgetLedger(state.Budget)
	if err != nil {
		t.Fatalf("NewBudgetLedger: %v", err)
	}
	if _, err := ledger.ApplyPlan(revision, requests); err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}
	reservationID := requests[0].ReservationId

	if _, _, err := ledger.Settle(reservationID, 1, NodeEstimate{Runs: 1, Attempts: 1}); !errors.Is(err, ErrReservationConflict) {
		t.Fatalf("Settle from reserved returned %v, want ErrReservationConflict", err)
	}
	if _, _, err := ledger.Settle(reservationID, 1, NodeEstimate{Runs: -1, Attempts: 1}); !errors.Is(err, ErrReservationConflict) {
		t.Fatalf("Settle with negative actuals returned %v, want ErrReservationConflict", err)
	}
	if _, err := ledger.Expire(reservationID, 1); err != nil {
		t.Fatalf("Expire from reserved: %v", err)
	}
	if _, err := ledger.Expire(reservationID, 1); err != nil {
		t.Fatalf("replayed Expire must be idempotent: %v", err)
	}
	if _, err := ledger.Commit(reservationID, 2); !errors.Is(err, ErrReservationConflict) {
		t.Fatalf("Commit from expired returned %v, want ErrReservationConflict", err)
	}
	if _, err := ledger.Release(reservationID, 2, 1); !errors.Is(err, ErrReservationConflict) {
		t.Fatalf("Release from expired returned %v, want ErrReservationConflict", err)
	}
	if _, err := ledger.Commit("reservation:ghost", 1); !errors.Is(err, ErrReservationNotFound) {
		t.Fatalf("Commit of unknown reservation returned %v, want ErrReservationNotFound", err)
	}
}

func TestBudgetLedgerActualExceedsReservedHaltsNewDispatch(t *testing.T) {
	budget := validBudgetRecord()
	budget.Limits.MaxTotalRuns = 2
	ledger, err := NewBudgetLedger(budget)
	if err != nil {
		t.Fatalf("NewBudgetLedger: %v", err)
	}
	revision := manualRevision(t, ledger, 1, "")
	requests := []ReservationRequest{manualRequest("over", 1, NodeEstimate{Runs: 1, Attempts: 1, Tokens: 100})}
	if _, err := ledger.ApplyPlan(revision, requests); err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}
	if _, err := ledger.Commit("reservation:over", 1); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	actual := NodeEstimate{Runs: 5, Attempts: 1, Tokens: 100}
	settled, decision, err := ledger.Settle("reservation:over", 2, actual)
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if !decision.HaltNewDispatch {
		t.Fatalf("actual > reserved must halt new dispatch: %+v", decision)
	}
	if decision.Reason == "" {
		t.Fatal("halt decision must carry a reason")
	}
	if settled.Actual == nil || settled.Actual.Runs != 5 {
		t.Fatalf("settled actuals malformed: %+v", settled.Actual)
	}
	if !ledger.HaltNewDispatch() {
		t.Fatal("ledger must report the halt after an over-actual settlement")
	}
	if ledger.Snapshot().Used.TotalRuns != 5 {
		t.Fatalf("used totalRuns = %d, want 5", ledger.Snapshot().Used.TotalRuns)
	}
	// Availability may report the true deficit; the halt, not negative
	// bookkeeping, prevents further dispatch.
	if ledger.Availability().TotalRuns >= 0 {
		t.Fatalf("availability totalRuns = %d, want a true deficit", ledger.Availability().TotalRuns)
	}

	// No new reservation after the halt.
	next := manualRevision(t, ledger, 2, "")
	firstDigest, err := revision.Digest()
	if err != nil {
		t.Fatalf("revision digest: %v", err)
	}
	next.PreviousPlanDigest = firstDigest
	if _, err := ledger.ApplyPlan(next, []ReservationRequest{manualRequest("late", 2, NodeEstimate{Runs: 1, Attempts: 1})}); !errors.Is(err, ErrDispatchHalted) {
		t.Fatalf("ApplyPlan after halt returned %v, want ErrDispatchHalted", err)
	}
	if ledger.LiveReservationCount() != 0 {
		t.Fatalf("halted ledger carries %d live reservations", ledger.LiveReservationCount())
	}
}

func TestBudgetLedgerConcurrentTransactionsNeverOversell(t *testing.T) {
	t.Run("racing first revisions admit exactly one", func(t *testing.T) {
		budget := validBudgetRecord()
		budget.Limits.MaxTotalRuns = 1
		ledger, err := NewBudgetLedger(budget)
		if err != nil {
			t.Fatalf("NewBudgetLedger: %v", err)
		}
		build := func(name string) (AcceptedGoalPlanRevision, []ReservationRequest) {
			revision := manualRevision(t, ledger, 1, "")
			revision.ProposalDigest = digestOfLiteral(name)
			return revision, []ReservationRequest{manualRequest(name, 1, NodeEstimate{Runs: 1, Attempts: 1})}
		}
		revisionA, requestsA := build("contender-a")
		revisionB, requestsB := build("contender-b")

		var waitGroup sync.WaitGroup
		results := make([]error, 2)
		waitGroup.Add(2)
		go func() {
			defer waitGroup.Done()
			_, results[0] = ledger.ApplyPlan(revisionA, requestsA)
		}()
		go func() {
			defer waitGroup.Done()
			_, results[1] = ledger.ApplyPlan(revisionB, requestsB)
		}()
		waitGroup.Wait()

		successes := 0
		for _, err := range results {
			if err == nil {
				successes++
			} else if !errors.Is(err, ErrPlanRevisionConflict) {
				t.Fatalf("losing contender returned %v, want ErrPlanRevisionConflict", err)
			}
		}
		if successes != 1 {
			t.Fatalf("racing first revisions produced %d successes, want exactly 1", successes)
		}
		if ledger.LiveReservationCount() != 1 || len(ledger.AcceptedRevisions()) != 1 {
			t.Fatalf("race left %d live reservations and %d revisions", ledger.LiveReservationCount(), len(ledger.AcceptedRevisions()))
		}
	})

	t.Run("racing transitions admit exactly one", func(t *testing.T) {
		state := testAuthorityState(t)
		revision, requests := acceptThroughAdmission(t, state, testPolicy(), validProposal())
		ledger, err := NewBudgetLedger(state.Budget)
		if err != nil {
			t.Fatalf("NewBudgetLedger: %v", err)
		}
		if _, err := ledger.ApplyPlan(revision, requests); err != nil {
			t.Fatalf("ApplyPlan: %v", err)
		}
		reservationID := requests[0].ReservationId

		var waitGroup sync.WaitGroup
		var commitErr, expireErr error
		waitGroup.Add(2)
		go func() {
			defer waitGroup.Done()
			_, commitErr = ledger.Commit(reservationID, 1)
		}()
		go func() {
			defer waitGroup.Done()
			_, expireErr = ledger.Expire(reservationID, 1)
		}()
		waitGroup.Wait()

		if (commitErr == nil) == (expireErr == nil) {
			t.Fatalf("racing transitions must admit exactly one: commit=%v expire=%v", commitErr, expireErr)
		}
		events := ledger.Events()
		if len(events) != 3 {
			t.Fatalf("race appended %d events, want 3", len(events))
		}
		last := events[len(events)-1]
		if last.ReservationId != reservationID {
			t.Fatalf("last event belongs to %s, want %s", last.ReservationId, reservationID)
		}
		if last.State != ReservationStateCommitted && last.State != ReservationStateExpired {
			t.Fatalf("contended reservation ended in state %s", last.State)
		}
	})
}

func TestBudgetReservationRecordValidation(t *testing.T) {
	reservation := BudgetReservation{
		AuthorityNamespaceId: testNamespace(),
		ReservationId:        "reservation:r1",
		IdempotencyKey:       "reserve:r1",
		GoalId:               "goal-1",
		NodeId:               "n1",
		PlanRevision:         1,
		CommandId:            "materialize:r1",
		Estimate:             NodeEstimate{Runs: 1, Attempts: 1},
		State:                ReservationStateReserved,
		Revision:             1,
	}
	if err := reservation.Validate(); err != nil {
		t.Fatalf("Validate rejected a valid reservation: %v", err)
	}
	digest, err := reservation.Digest()
	if err != nil || digest == "" {
		t.Fatalf("Digest failed: %v", err)
	}

	settled := reservation
	settled.State = ReservationStateSettled
	if err := settled.Validate(); err == nil {
		t.Fatal("Validate accepted a settled reservation without actual usage")
	}
	actual := NodeEstimate{Runs: 1, Attempts: 1}
	settled.Actual = &actual
	if err := settled.Validate(); err != nil {
		t.Fatalf("Validate rejected a settled reservation with actuals: %v", err)
	}
	withActual := reservation
	withActual.Actual = &actual
	if err := withActual.Validate(); err == nil {
		t.Fatal("Validate accepted actual usage outside the settled state")
	}
	negativeActual := settled
	negative := NodeEstimate{Runs: -1, Attempts: 1}
	negativeActual.Actual = &negative
	if err := negativeActual.Validate(); err == nil {
		t.Fatal("Validate accepted negative actual usage")
	}

	for _, mutate := range []func(*BudgetReservation){
		func(reservation *BudgetReservation) {
			reservation.AuthorityNamespaceId = authority.AuthorityNamespaceId{}
		},
		func(reservation *BudgetReservation) { reservation.ReservationId = "bad id" },
		func(reservation *BudgetReservation) { reservation.PlanRevision = 0 },
		func(reservation *BudgetReservation) { reservation.Revision = 0 },
		func(reservation *BudgetReservation) { reservation.State = ReservationState("paused") },
		func(reservation *BudgetReservation) { reservation.Estimate.Runs = 0 },
	} {
		broken := reservation
		mutate(&broken)
		if err := broken.Validate(); err == nil {
			t.Fatalf("Validate accepted reservation %+v", broken)
		}
	}

	withActualCopy := settled
	actualCopy := actual
	withActualCopy.Actual = &actualCopy
	if !settled.Equal(withActualCopy) {
		t.Fatal("Equal rejected identical settled content")
	}
	diverging := settled
	divergingActual := NodeEstimate{Runs: 2, Attempts: 1}
	diverging.Actual = &divergingActual
	if settled.Equal(diverging) {
		t.Fatal("Equal accepted diverging actual usage")
	}
}
