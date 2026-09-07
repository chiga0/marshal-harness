//go:build darwin && arm64

package productionruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
)

func TestRepositoryTeamAcceptedInputsRequireOriginalApprovedPlan(t *testing.T) {
	fixture, request, prepares, materializations := materializationFixture(t)
	session, err := OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, _, err := session.ReadAcceptedTeamInputs(context.Background(), "team-session", "integration", "not-approved"); err == nil {
		t.Fatal("unapproved integration input read")
	}
	plan, err := session.ApproveInitialTeam(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if values, ready, err := session.ReadAcceptedTeamInputs(context.Background(), "team-session", "integration", plan.FactDigest); err != nil || ready || len(values) != 0 {
		t.Fatalf("unmaterialized plan not waiting: ready=%v err=%v", ready, err)
	}
	if *prepares != 0 || *materializations != 0 {
		t.Fatal("read path prepared or created a Run")
	}
	for _, selector := range []struct{ node, fact string }{{"service", plan.FactDigest}, {"integration", "wrong-fact"}} {
		if _, _, err := session.ReadAcceptedTeamInputs(context.Background(), "team-session", selector.node, selector.fact); err == nil {
			t.Fatal("changed selector accepted")
		}
	}
	if _, err := session.HaltInitialTeam(context.Background(), "team-session", "service", plan.FactDigest, "start"); err != nil {
		t.Fatal(err)
	}
	if _, ready, err := session.ReadAcceptedTeamInputs(context.Background(), "team-session", "integration", plan.FactDigest); err == nil || ready {
		t.Fatal("halted plan may prepare integration")
	}
}

func TestRepositoryTeamAcceptedInputsWaitOnRealLeasesAndRejectSnapshotClaims(t *testing.T) {
	fixture, request, prepares, materializations := materializationFixture(t)
	session, err := OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	plan, err := session.ApproveInitialTeam(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	runs := map[string]domain.RunState{}
	for _, node := range []string{"service", "client"} {
		state, err := session.MaterializeInitialTeamRun(context.Background(), request, node)
		if err != nil {
			t.Fatal(err)
		}
		runs[node] = state
	}
	read := func() {
		t.Helper()
		if values, ready, err := session.ReadAcceptedTeamInputs(context.Background(), "team-session", "integration", plan.FactDigest); err != nil || ready || len(values) != 0 {
			t.Fatalf("non-accepted upstreams: ready=%v err=%v", ready, err)
		}
	}
	read()
	// Sorted acquisition takes client before service. An occupied second lease
	// must release the first; it is capacity waiting, not a missing Run/retry.
	lease, err := session.runs.AcquireExisting(runs["service"].RunID)
	if err != nil {
		t.Fatal(err)
	}
	read()
	other, err := session.runs.AcquireExisting(runs["client"].RunID)
	if err != nil {
		t.Fatal("partial read retained the other lease", err)
	}
	// Forge the first sorted input, so the assertion cannot pass merely
	// because another legitimate READY input was visited before the forgery.
	claimed := runs["client"]
	claimed.State = domain.StateAccepted
	claimed.CurrentAttemptID = "attempt-forged"
	if err := session.runs.WriteSnapshot(other, claimed); err == nil {
		t.Fatal("normal writer admitted a forged terminal snapshot")
	}
	// Simulate on-disk tampering only inside this test's temporary repository;
	// the real writer correctly refuses an inconsistent snapshot first.
	raw, err := json.Marshal(claimed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.repository, ".marshal", "runs", claimed.RunID, "state.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := other.Release(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if values, ready, _ := session.ReadAcceptedTeamInputs(context.Background(), "team-session", "integration", plan.FactDigest); ready || len(values) != 0 {
		t.Fatal("snapshot label replaced committed accepted evidence")
	}
	if *prepares != 2 || *materializations != 2 {
		t.Fatal("read path created extra work")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := session.ReadAcceptedTeamInputs(context.Background(), "team-session", "integration", plan.FactDigest); err == nil {
		t.Fatal("closed owner read integration inputs")
	}
}
