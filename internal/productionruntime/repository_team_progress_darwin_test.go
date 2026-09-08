//go:build darwin && arm64

package productionruntime

import (
	"context"
	"testing"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/domain"
)

func TestRepositoryTeamProgressUsesApprovedCreationAndColdHalt(t *testing.T) {
	fixture, request, prepares, calls := materializationFixture(t)
	session, err := OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if _, found, err := session.NextInitialTeamProgress(context.Background(), "", domain.StateRunning); err != nil || found {
		t.Fatal("empty ledger selected")
	}
	approval, err := session.ApproveInitialTeam(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := session.NextInitialTeamProgress(context.Background(), "", domain.StateRunning); err != nil || found || *prepares != 0 || *calls != 0 {
		t.Fatal("approval started work")
	}
	created, err := session.MaterializeInitialTeamRun(context.Background(), request, "service")
	if err != nil {
		t.Fatal(err)
	}
	selection := InitialTeamProgress{InitialTeamDispatch: InitialTeamDispatch{GoalID: approval.GoalID, NodeID: "service", RunID: created.RunID, PlanFactDigest: approval.FactDigest}, Run: application.RunProjection{RunID: created.RunID, TaskID: created.TaskID}}
	if allowed, err := session.TeamProgressAllowed(context.Background(), selection); err != nil || !allowed {
		t.Fatal("valid scheduling membership denied")
	}
	if _, found, err := session.NextInitialTeamProgress(context.Background(), "", domain.StateRunning); err != nil || found {
		t.Fatal("READY selected as running")
	}
	bad := selection
	bad.NodeID = "foreign-node"
	if allowed, err := session.TeamProgressAllowed(context.Background(), bad); err == nil || allowed {
		t.Fatal("foreign membership admitted")
	}
	if _, err := session.HaltInitialTeam(context.Background(), approval.GoalID, "service", approval.FactDigest, "collect"); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	session, err = OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	if allowed, err := session.TeamProgressAllowed(context.Background(), selection); err != nil || allowed {
		t.Fatal("cold halt was ignored")
	}
	if _, found, err := session.NextInitialTeamProgress(context.Background(), "", domain.StateVerifying); err != nil || found {
		t.Fatal("halted plan selected")
	}
	if *prepares != 1 || *calls != 1 {
		t.Fatal("read path caused extra execution")
	}
}

func TestTeamProgressRoundRobinDoesNotStarveFinishedSibling(t *testing.T) {
	items := []InitialTeamProgress{{InitialTeamDispatch: InitialTeamDispatch{RunID: "run-b"}}, {InitialTeamDispatch: InitialTeamDispatch{RunID: "run-a"}}}
	for _, tc := range []struct{ after, want string }{{"", "run-a"}, {"run-a", "run-b"}, {"run-b", "run-a"}, {"removed", "run-a"}, {"z", "run-a"}} {
		got, found := selectTeamProgress(items, tc.after)
		if !found || got.RunID != tc.want {
			t.Fatalf("cursor %q: %q", tc.after, got.RunID)
		}
	}
	if _, found := selectTeamProgress(nil, "run-a"); found {
		t.Fatal("empty queue selected")
	}
}

func TestRepositoryTeamProgressColdVerificationBarrierAndBusySibling(t *testing.T) {
	ctx := context.Background()
	fixture, request, _, _ := materializationFixture(t)
	session, err := OpenRepositorySession(ctx, fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if _, err := session.ApproveInitialTeam(ctx, request); err != nil {
		t.Fatal(err)
	}
	var runs []fixedDeliveryFixture
	var running []application.RunProjection
	for _, node := range []string{"service", "client"} {
		created, err := session.MaterializeInitialTeamRun(ctx, request, node)
		if err != nil {
			t.Fatal(err)
		}
		ready, err := session.InspectRun(ctx, application.InspectRunRequest{RunID: created.RunID})
		if err != nil {
			t.Fatal(err)
		}
		f := fixedDeliveryFixture{repository: fixture.repository, session: session, request: application.StartRunRequest{RunID: ready.RunID, ExpectedSequence: ready.Sequence, ExpectedAuthorityHead: ready.AuthorityHead}}
		runs = append(runs, f)
		running = append(running, advanceFixedDeliveryRunToRunningWithBudget(t, f, 1))
	}
	if err := session.HaltColdInitialTeamVerifications(ctx); err != nil {
		t.Fatal(err)
	}
	first, found, err := session.NextInitialTeamProgress(ctx, "", domain.StateRunning)
	if err != nil || !found {
		t.Fatalf("running not selected: %v", err)
	}
	second, found, err := session.NextInitialTeamProgress(ctx, first.RunID, domain.StateRunning)
	if err != nil || !found || second.RunID == first.RunID {
		t.Fatalf("sibling starved: %v", err)
	}
	lease, err := session.runs.AcquireExisting(first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	sibling, found, err := session.NextInitialTeamProgress(ctx, "", domain.StateRunning)
	if err != nil || !found || sibling.RunID == first.RunID {
		t.Fatalf("busy Run hid sibling: %v", err)
	}
	if err := session.HaltColdInitialTeamVerifications(ctx); err == nil {
		t.Fatal("cold barrier skipped busy Run")
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	// Model an interrupted Verify: durable worker completion but no
	// verification.completed or halt. No Worker/verification command is run.
	advanceFixedDeliveryRunToVerifying(t, runs[0], running[0])
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	session, err = OpenRepositorySession(ctx, fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := session.NextInitialTeamProgress(ctx, "", domain.StateVerifying); err != nil || !found {
		t.Fatalf("missing interrupted fixture: %v", err)
	}
	if err := session.HaltColdInitialTeamVerifications(ctx); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []domain.State{domain.StateRunning, domain.StateVerifying} {
		if _, found, err := session.NextInitialTeamProgress(ctx, "", phase); err != nil || found {
			t.Fatalf("halt did not suppress automatic %s: %v", phase, err)
		}
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	session, err = OpenRepositorySession(ctx, fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := session.NextInitialTeamProgress(ctx, "", domain.StateVerifying); err != nil || found {
		t.Fatalf("second restart lost halt: %v", err)
	}
}
