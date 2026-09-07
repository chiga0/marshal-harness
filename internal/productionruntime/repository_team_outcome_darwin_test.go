//go:build darwin && arm64

package productionruntime

import (
	"context"
	"testing"

	"github.com/chiga0/marshal-harness/internal/resultingress"
)

func TestRepositoryTeamOutcomeNeverCreatesRunsOrClaimsPendingSuccess(t *testing.T) {
	fixture, request, prepares, materializations := materializationFixture(t)
	session, err := OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, found, err := session.ReadInitialTeamOutcome(context.Background(), request); err != nil || found {
		t.Fatal("unapproved result", err)
	}
	plan, err := session.ApproveInitialTeam(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := session.FinalizeReadyInitialTeams(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, found, err := session.ReadInitialTeamOutcome(context.Background(), request); err != nil || found {
			t.Fatal("pending plan completed", err)
		}
	}
	if *prepares != 0 || *materializations != 0 {
		t.Fatal("completion query created paid work")
	}
	if _, found, err := session.ingress.ReadTeamOutcome(session.acquisition.Scope, plan.GoalID); err != nil || found {
		t.Fatal("phantom durable completion")
	}
	if _, err := session.HaltInitialTeam(context.Background(), plan.GoalID, "service", plan.FactDigest, "prepare"); err != nil {
		t.Fatal(err)
	}
	if err := session.FinalizeReadyInitialTeams(context.Background()); err != nil {
		t.Fatal("halted scan should not retry", err)
	}
	if _, found, err := session.ReadInitialTeamOutcome(context.Background(), request); err != nil || found {
		t.Fatal("halt converted to success", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := session.FinalizeReadyInitialTeams(ctx); err == nil {
		t.Fatal("canceled finalizer proceeded")
	}
}

func TestRepositoryTeamCompletedVerifierRequiresCurrentOwnerBeforeCallback(t *testing.T) {
	fixture, request, _, _ := materializationFixture(t)
	session, err := OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	plan, err := session.ApproveInitialTeam(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	owner := session.acquisition
	owner.OwnerEpoch++
	called := false
	err = (repositoryCompletedTeamVerifier{session}).WithCurrentCompletedTeam(context.Background(), owner, resultingress.TeamPlanApproval{}, plan.GoalID, plan.FactDigest, func(resultingress.TeamDeliveryOutcome) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("stale owner reached outcome producer")
	}
}
