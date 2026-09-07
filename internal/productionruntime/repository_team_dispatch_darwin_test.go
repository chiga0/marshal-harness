//go:build darwin && arm64

package productionruntime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

func TestRepositoryTeamDispatchReadsApprovedLedgerAndColdHalt(t *testing.T) {
	fixture, request, prepares, calls := materializationFixture(t)
	session, err := OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if _, found, err := session.NextInitialTeamDispatch(context.Background(), 2); err != nil || found {
		t.Fatal("empty ledger dispatched")
	}
	approval, err := session.ApproveInitialTeam(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := session.NextInitialTeamDispatch(context.Background(), 0); err != nil || found {
		t.Fatal("zero capacity dispatched")
	}
	selected, found, err := session.NextInitialTeamDispatch(context.Background(), 2)
	if err != nil || !found || selected.GoalID != approval.GoalID || selected.PlanFactDigest != approval.FactDigest || selected.NodeID == "integration" {
		t.Fatalf("approved selector: %v", err)
	}
	if *prepares != 0 || *calls != 0 {
		t.Fatal("read-only scheduling materialized")
	}
	created, err := session.MaterializeApprovedInitialTeamRun(context.Background(), selected.GoalID, selected.NodeID, selected.PlanFactDigest)
	if err != nil || created.RunID != selected.RunID {
		t.Fatal("selected materialization mismatch")
	}
	lease, err := session.runs.AcquireExisting(created.RunID)
	if err != nil {
		t.Fatal(err)
	}
	_, leaseFound, readErr := session.NextInitialTeamDispatch(context.Background(), 2)
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if readErr != nil || leaseFound {
		t.Fatal("a legitimate verification lease became corruption or spare capacity")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	session, err = OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	replayed, found, err := session.NextInitialTeamDispatch(context.Background(), 2)
	if err != nil || !found || replayed != selected || *prepares != 1 || *calls != 1 {
		t.Fatal("cold READY was replaced or reprobed")
	}
	if _, err := session.HaltInitialTeam(context.Background(), selected.GoalID, selected.NodeID, selected.PlanFactDigest, "start"); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	session, err = OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := session.NextInitialTeamDispatch(context.Background(), 2); err != nil || found {
		t.Fatal("cold halted plan was dispatched")
	}
}

// Pure scheduling policy with explicitly synthetic Run projections. The
// production reader above supplies real authority; these are not live Runs.
func TestRepositoryTeamDispatchCapacityAndDependencyPolicy(t *testing.T) {
	fixture, request, _, _ := materializationFixture(t)
	session, err := OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	approval, err := session.ApproveInitialTeam(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := session.ingress.ListTeamPlans(session.acquisition.Scope)
	if err != nil || len(plans) != 1 {
		t.Fatal("plan fixture missing")
	}
	nodes := map[string]resultingress.TeamMaterialization{}
	for _, node := range plans[0].Materializations {
		nodes[node.NodeID] = node
	}
	projection := func(node string, state domain.State) application.RunProjection {
		n := nodes[node]
		return application.RunProjection{TaskID: n.TaskID, RunID: n.RunID, State: state, Sequence: 3, AttemptID: "fixture-attempt", AuthorityHead: canonical.DigestBytes([]byte(node))}
	}
	for _, mode := range []string{"one-running", "two-running", "review-backlog", "standalone", "completed-implementations", "rejected-upstream", "halted", "goal-capacity", "stale-ready"} {
		t.Run(mode, func(t *testing.T) {
			states := map[string]application.RunProjection{}
			halts := map[string]bool{}
			plan := plans[0]
			wantFound, wantError := false, false
			wantNode := "client"
			switch mode {
			case "one-running":
				states[nodes["service"].RunID] = projection("service", domain.StateRunning)
				wantFound = true
			case "two-running":
				states[nodes["service"].RunID] = projection("service", domain.StateRunning)
				states[nodes["client"].RunID] = projection("client", domain.StateRunning)
			case "review-backlog":
				states[nodes["service"].RunID] = projection("service", domain.StateReviewPending)
				states[nodes["client"].RunID] = projection("client", domain.StateVerifying)
			case "standalone":
				p := projection("service", domain.StateRunning)
				p.RunID = "standalone-run"
				states[p.RunID] = p
			case "completed-implementations":
				states[nodes["service"].RunID] = projection("service", domain.StateAccepted)
				states[nodes["client"].RunID] = projection("client", domain.StateAccepted)
				wantFound, wantNode = true, "integration"
			case "rejected-upstream":
				states[nodes["service"].RunID] = projection("service", domain.StateAccepted)
				states[nodes["client"].RunID] = projection("client", domain.StateRejected)
			case "halted":
				halts[approval.GoalID] = true
			case "goal-capacity":
				states[nodes["service"].RunID] = projection("service", domain.StateRunning)
				var input goal.TeamInputs
				if json.Unmarshal(plan.Inputs, &input) != nil {
					t.Fatal("fixture decode")
				}
				input.Limits.MaxConcurrentNodes = 1
				plan.Inputs, err = json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
			case "stale-ready":
				p := projection("service", domain.StateReady)
				states[p.RunID] = p
				wantError = true
			}
			selected, found, err := selectInitialTeamDispatch([]resultingress.TeamPlanState{plan}, halts, states, 2)
			if found != wantFound || (err != nil) != wantError {
				t.Fatalf("found=%t err=%v", found, err)
			}
			if found && selected.NodeID != wantNode {
				t.Fatal("wrong ready node selected")
			}
		})
	}
}
