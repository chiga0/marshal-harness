//go:build darwin && arm64

package productionruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/goal"
)

// Explicit session/RunStore fixture, not a real Worker or launch verdict.
func TestRepositoryTeamHaltBlocksColdCreationAndInitialStart(t *testing.T) {
	fixture, request, prepares, calls := materializationFixture(t)
	session, err := OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	approval, err := session.ApproveInitialTeam(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	created, err := session.MaterializeInitialTeamRun(context.Background(), request, "service")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := session.runs.AcquireExisting(created.RunID)
	if err != nil {
		t.Fatal(err)
	}
	authority, readErr := session.runs.ReadRunStartAuthorityUnderLease(context.Background(), lease)
	releaseErr := lease.Release()
	if readErr != nil || releaseErr != nil {
		t.Fatalf("read READY: %v %v", readErr, releaseErr)
	}
	start := application.StartRunRequest{RunID: created.RunID, ExpectedSequence: 2, ExpectedAuthorityHead: authority.Run.AuthorityHead}
	if _, err := session.HaltInitialTeam(context.Background(), approval.GoalID, "service", approval.FactDigest, "start"); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	session, err = OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	if member, err := session.RequireInitialTeamRunPlan(context.Background(), start); err == nil || !member {
		t.Fatal("halt bypassed through first Start or standalone fallback")
	}
	if _, err := session.MaterializeInitialTeamRun(context.Background(), request, "service"); err == nil {
		t.Fatal("original request bypassed halt")
	}
	if _, err := session.MaterializeApprovedInitialTeamRun(context.Background(), approval.GoalID, "service", approval.FactDigest); err == nil {
		t.Fatal("resident continuation bypassed halt")
	}
	if *prepares != 1 || *calls != 1 {
		t.Fatal("halt repeated preparation/materialization")
	}
	// Halt is not cancellation: frozen creation remains recoverable and the
	// original READY/Attempt budget is unchanged; only future dispatch stops.
	if err := session.RecoverInitialTeamCreations(context.Background()); err != nil {
		t.Fatal(err)
	}
	lease, err = session.runs.AcquireExisting(created.RunID)
	if err != nil {
		t.Fatal(err)
	}
	current, readErr := session.runs.ReadRunStartAuthorityUnderLease(context.Background(), lease)
	releaseErr = lease.Release()
	if readErr != nil || releaseErr != nil || current.Run != authority.Run || current.AttemptsUsed != 0 {
		t.Fatal("halt fabricated a Run terminal state or consumed/refunded an Attempt")
	}
}

func TestRepositoryTeamPlanGateUsesOriginalApprovalAndExactReady(t *testing.T) {
	fixture, approval, prepares, calls := materializationFixture(t)
	session, err := OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	var inputs goal.TeamInputs
	if err := json.Unmarshal(approval.Inputs, &inputs); err != nil {
		t.Fatal(err)
	}
	_, runID, err := goal.TeamNodeIDs(inputs.Proposal, "service")
	if err != nil {
		t.Fatal(err)
	}
	start := application.StartRunRequest{RunID: runID, ExpectedSequence: 2, ExpectedAuthorityHead: canonical.DigestBytes([]byte("not-yet-created"))}
	if member, err := session.RequireInitialTeamRunPlan(context.Background(), start); err != nil || member {
		t.Fatalf("unknown Run is not a team approval: member=%t err=%v", member, err)
	}
	if _, err := session.ApproveInitialTeam(context.Background(), approval); err != nil {
		t.Fatal(err)
	}
	if member, err := session.RequireInitialTeamRunPlan(context.Background(), start); err == nil || !member {
		t.Fatal("unfrozen member was admitted or allowed standalone fallback")
	}
	if _, err := session.PrepareInitialTeamRun(context.Background(), approval, "service"); err != nil {
		t.Fatal(err)
	}
	if member, err := session.RequireInitialTeamRunPlan(context.Background(), start); err == nil || !member {
		t.Fatal("missing READY was admitted")
	}
	if _, err := session.MaterializeInitialTeamRun(context.Background(), approval, "service"); err != nil {
		t.Fatal(err)
	}
	lease, err := session.runs.AcquireExisting(runID)
	if err != nil {
		t.Fatal(err)
	}
	current, readErr := session.runs.ReadRunStartAuthorityUnderLease(context.Background(), lease)
	releaseErr := lease.Release()
	if readErr != nil || releaseErr != nil {
		t.Fatalf("fixture READY authority: %v %v", readErr, releaseErr)
	}
	start.ExpectedAuthorityHead = current.Run.AuthorityHead
	for _, cold := range []bool{false, true} {
		if cold {
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			session, err = OpenRepositorySession(context.Background(), fixture.inputs)
			if err != nil {
				t.Fatal(err)
			}
		}
		if member, err := session.RequireInitialTeamRunPlan(context.Background(), start); err != nil || !member {
			t.Fatalf("exact team gate cold=%t member=%t err=%v", cold, member, err)
		}
	}
	if *prepares != 1 || *calls != 1 {
		t.Fatal("authorization repeated planning/materialization")
	}
	if _, err := os.Stat(filepath.Join(fixture.repository, ".marshal", "runs", runID, "control")); !os.IsNotExist(err) {
		t.Fatal("team gate created synthetic human approval")
	}
	for _, mutation := range []string{"head", "sequence", "integration", "canceled"} {
		bad := start
		ctx, cancel := context.WithCancel(context.Background())
		switch mutation {
		case "head":
			bad.ExpectedAuthorityHead = canonical.DigestBytes([]byte("changed"))
		case "sequence":
			bad.ExpectedSequence++
		case "integration":
			_, bad.RunID, err = goal.TeamNodeIDs(inputs.Proposal, "integration")
			if err != nil {
				t.Fatal(err)
			}
		case "canceled":
			cancel()
		}
		_, err := session.RequireInitialTeamRunPlan(ctx, bad)
		cancel()
		if err == nil {
			t.Fatalf("invalid team gate admitted: %s", mutation)
		}
	}
	if err := os.WriteFile(filepath.Join(fixture.repository, ".marshal", "runs", runID, "policy-snapshot.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if member, err := session.RequireInitialTeamRunPlan(context.Background(), start); err == nil || !member {
		t.Fatal("changed policy admitted or hidden by standalone fallback")
	}
}
