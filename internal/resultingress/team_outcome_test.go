package resultingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/goal"
)

// Store contract fixture only. Production uses actual repository owner/Run
// leases and the original Decision reader, not these fabricated digests.
type completedTeamFixture struct {
	owner        ControlOwnerAcquisition
	approval     TeamPlanApproval
	value        TeamDeliveryOutcome
	err          error
	skipCallback bool
}

func (v completedTeamFixture) WithCurrentCompletedTeam(_ context.Context, owner ControlOwnerAcquisition, approval TeamPlanApproval, goalID, fact string, fn func(TeamDeliveryOutcome) error) error {
	if v.err != nil {
		return v.err
	}
	if v.skipCallback {
		return nil
	}
	if owner != v.owner || approval != v.approval || goalID != v.value.Outcome.GoalId || fact != v.value.PlanFactDigest {
		return ErrTeamPlanConflict
	}
	return fn(v.value)
}

func completedTeamFixtureInputs(t *testing.T, store *DurableStore, owner ControlOwnerAcquisition) completedTeamFixture {
	t.Helper()
	plan, approval, original := teamCreationFixture(t, store, owner)
	return completedTaskPlanFixture(t, store, owner, plan, approval, original)
}

func completedTaskPlanFixture(t *testing.T, store *DurableStore, owner ControlOwnerAcquisition, plan TeamPlanState, approval TeamPlanApproval, original []byte) completedTeamFixture {
	t.Helper()
	var input goal.TeamInputs
	var prepared map[string]json.RawMessage
	if json.Unmarshal(plan.Inputs, &input) != nil || json.Unmarshal(original, &prepared) != nil {
		t.Fatal("fixture input decode")
	}
	bound := TeamIntegrationInputs{GoalID: input.Spec.GoalId, NodeID: "integration", PlanFactDigest: plan.FactDigest, BaseSHA: input.BaseSHA}
	var integrate goal.TeamNodeInputs
	for _, node := range input.Nodes {
		if node.Role == "integrate" {
			integrate = node
			continue
		}
		_, runID, err := goal.TeamNodeIDs(input.Proposal, node.NodeID)
		if err != nil {
			t.Fatal(err)
		}
		prepared["runId"], prepared["task"], prepared["policy"] = teamTestBytes(t, runID), node.Task, node.Policy
		creation, err := store.FreezeInitialTeamRun(context.Background(), teamTestApproval{owner, approval, false}, owner, approval, input.Spec.GoalId, node.NodeID, plan.FactDigest, teamTestBytes(t, prepared))
		if err != nil {
			t.Fatal(err)
		}
		bound.Sources = append(bound.Sources, TeamAcceptedSource{NodeID: node.NodeID, RunID: runID, AttemptID: "attempt-1", CreationFactDigest: creation.FactDigest,
			AuthorityHead: attemptTestDigest("head-" + node.NodeID), CandidateDigest: attemptTestDigest("candidate-" + node.NodeID), PatchDigest: attemptTestDigest("patch-" + node.NodeID),
			DecisionDigest: attemptTestDigest("decision-" + node.NodeID), PacketDigest: attemptTestDigest("packet-" + node.NodeID), OutcomeDigest: attemptTestDigest("outcome-" + node.NodeID)})
	}
	sort.Slice(bound.Sources, func(i, j int) bool { return bound.Sources[i].NodeID < bound.Sources[j].NodeID })
	digest, err := bound.Digest()
	if err != nil {
		t.Fatal(err)
	}
	base := TeamIntegrationBase{Inputs: bound, InputsDigest: digest, TreeSHA: strings.Repeat("d", 40), CommitSHA: strings.Repeat("c", 40)}
	task, err := DeriveTeamIntegrationTask(integrate.Task, input.BaseSHA, base.CommitSHA)
	if err != nil {
		t.Fatal(err)
	}
	_, runID, err := goal.TeamNodeIDs(input.Proposal, integrate.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	prepared["runId"], prepared["baseSha"], prepared["task"], prepared["policy"] = teamTestBytes(t, runID), teamTestBytes(t, base.CommitSHA), task, integrate.Policy
	creation, err := store.FreezeIntegrationTeamRun(context.Background(), teamAcceptedFixtureVerifier{owner, approval, bound}, owner, approval, input.Spec.GoalId, integrate.NodeID, plan.FactDigest, teamTestBytes(t, prepared), base)
	if err != nil {
		t.Fatal("valid integration creation", err)
	}
	final := bound.Sources[0]
	final.NodeID, final.RunID, final.CreationFactDigest = integrate.NodeID, runID, creation.FactDigest
	final.AuthorityHead, final.CandidateDigest, final.PatchDigest = attemptTestDigest("integration-head"), attemptTestDigest("integration-candidate"), attemptTestDigest("integration-patch")
	final.DecisionDigest, final.PacketDigest, final.OutcomeDigest = attemptTestDigest("integration-decision"), attemptTestDigest("integration-packet"), attemptTestDigest("integration-outcome")
	revision, err := plan.Revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return completedTeamFixture{owner: owner, approval: approval, value: TeamDeliveryOutcome{
		Outcome: goal.GoalOutcome{AuthorityNamespaceId: owner.Scope.AuthorityNamespaceID, GoalId: input.Spec.GoalId, State: goal.OutcomeStateCompleted,
			Reason: "verified-team-delivery", FinalPlanDigest: revision, BudgetDigest: plan.Revision.BudgetSnapshotDigest, FinalizedAt: "2026-09-07T06:00:00Z"},
		PlanFactDigest: plan.FactDigest, Upstreams: bound.Sources, Integration: final, IntegrationBaseSHA: base.CommitSHA, AttemptsUsed: 3, Measurement: "attempt-counts-only"}}
}

func TestTeamOutcomeCommitOnceColdReplayAndRejectLateHalt(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
	v := completedTeamFixtureInputs(t, store, owner.Acquisition)
	before := reservationLedgerBytes(t, store)
	result, err := store.CompleteTeam(context.Background(), v, v.owner, v.approval, "team-1", v.value.PlanFactDigest)
	if err != nil || result.FactDigest == "" || bytes.Count(reservationLedgerBytes(t, store), []byte{'\n'}) != bytes.Count(before, []byte{'\n'})+1 {
		t.Fatal("valid completion failed", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	owner2, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
	v.owner = owner2.Acquisition
	before = reservationLedgerBytes(t, store)
	again, err := store.CompleteTeam(context.Background(), v, v.owner, v.approval, "team-1", v.value.PlanFactDigest)
	if err != nil || canonicalDigestOrEmpty(again) != canonicalDigestOrEmpty(result) || !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("replay changed completion or budget", err)
	}
	read, found, err := store.ReadTeamOutcome(v.owner.Scope, "team-1")
	if err != nil || !found || canonicalDigestOrEmpty(read) != canonicalDigestOrEmpty(result) {
		t.Fatal("cold query lost original result", err)
	}
	read.Upstreams[0].CandidateDigest = attemptTestDigest("caller-change")
	read, found, err = store.ReadTeamOutcome(v.owner.Scope, "team-1")
	if err != nil || !found || canonicalDigestOrEmpty(read) != canonicalDigestOrEmpty(result) {
		t.Fatal("query leaked mutable authority")
	}
	_, err = store.HaltTeamPlan(context.Background(), teamTestApproval{v.owner, v.approval, false}, v.owner, v.approval, TeamPlanHalt{GoalID: "team-1", NodeID: "integration", PlanFactDigest: v.value.PlanFactDigest, Stage: "inspect"})
	if err == nil || !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("late halt changed completed plan")
	}
	v.value.Outcome.FinalizedAt = "2026-09-07T06:01:00Z"
	if _, err := store.CompleteTeam(context.Background(), v, v.owner, v.approval, "team-1", v.value.PlanFactDigest); err == nil {
		t.Fatal("completion time refreshed")
	}
}

func TestTeamOutcomeRejectsInvalidBindingsWithoutAppend(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
	valid := completedTeamFixtureInputs(t, store, owner.Acquisition)
	for _, mode := range []string{"source", "candidate", "creation", "run", "base", "plan", "budget", "namespace", "attempts", "measurement", "claimed-fact", "not-ready", "denied", "canceled", "skip-callback", "stale-owner"} {
		t.Run(mode, func(t *testing.T) {
			v := valid
			v.value = TeamDeliveryOutcome{}
			if err := json.Unmarshal(teamTestBytes(t, valid.value), &v.value); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "skip-callback":
				v.skipCallback = true
			case "stale-owner":
				v.owner.OwnerEpoch++
			case "source":
				v.value.Upstreams[0].CandidateDigest = attemptTestDigest("stale")
			case "candidate":
				v.value.Integration.CandidateDigest = "invalid"
			case "creation":
				v.value.Integration.CreationFactDigest = attemptTestDigest("other")
			case "run":
				v.value.Integration.RunID = "other-run"
			case "base":
				v.value.IntegrationBaseSHA = strings.Repeat("e", 40)
			case "plan":
				v.value.Outcome.FinalPlanDigest = attemptTestDigest("other")
			case "budget":
				v.value.Outcome.BudgetDigest = attemptTestDigest("other")
			case "namespace":
				v.value.Outcome.AuthorityNamespaceId.ControlPlaneId = "other"
			case "attempts":
				v.value.AttemptsUsed = 0
			case "measurement":
				v.value.Measurement = "all-tokens-measured"
			case "claimed-fact":
				v.value.FactDigest = attemptTestDigest("claimed")
			case "not-ready":
				v.err = ErrTeamOutcomeNotReady
			case "denied":
				v.err = errors.New("fixture denied")
			case "canceled":
				cancel()
			}
			before := reservationLedgerBytes(t, store)
			if _, err := store.CompleteTeam(ctx, v, v.owner, v.approval, "team-1", v.value.PlanFactDigest); err == nil {
				t.Fatal("invalid completion")
			}
			if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
				t.Fatal("rejection appended")
			}
		})
	}
	// Re-prove the original valid path after all negative cases.
	if _, err := store.CompleteTeam(context.Background(), valid, valid.owner, valid.approval, "team-1", valid.value.PlanFactDigest); err != nil {
		t.Fatal("negatives damaged original", err)
	}
}
