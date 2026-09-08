package resultingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/goal"
)

// Explicit store-only fixture. This is not production authenticated approval
// or Task schema validation; those must be exercised by the fixed API seam.
type teamTestApproval struct {
	owner    ControlOwnerAcquisition
	approval TeamPlanApproval
	deny     bool
}

func (v teamTestApproval) WithCurrentApprovedTeam(ctx context.Context, owner ControlOwnerAcquisition, approval TeamPlanApproval, fn func() error) error {
	if ctx.Err() != nil || v.deny || owner != v.owner || approval != v.approval {
		return ErrTeamPlanConflict
	}
	return fn()
}

func teamTestBytes(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = canonical.JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func teamTestInputs(t *testing.T, owner ControlOwnerAcquisition) (goal.TeamInputs, []byte, TeamPlanApproval) {
	t.Helper()
	namespace := owner.Scope.AuthorityNamespaceID
	spec := goal.GoalSpecRevision{AuthorityNamespaceId: namespace, GoalId: "team-1", Revision: 1, ProjectId: "project", Repository: "/fixture/team", Title: "team", Description: "store-only fixture"}
	specDigest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	proposal := goal.GoalPlanProposal{AuthorityNamespaceId: namespace, ProposalId: "proposal-1", GoalId: spec.GoalId, ProjectId: spec.ProjectId, Repository: spec.Repository, GoalSpecRevision: 1, GoalSpecDigest: specDigest, PlannerIdentity: "untrusted-planner"}
	paths := [][]string{{"service.py"}, {"client.py"}, {"service.py", "client.py"}}
	for i, id := range []string{"service", "client", "integration"} {
		proposal.Nodes = append(proposal.Nodes, goal.GoalNode{NodeId: id, ExecutorKind: goal.ExecutorKindImplement, Title: id, Repository: spec.Repository, Paths: paths[i], SideEffectClasses: []string{"workspace-write"}, Estimate: goal.NodeEstimate{Runs: 1, Attempts: 2, WallTimeSeconds: 100, ArtifactBytes: 1000}})
	}
	proposal.Edges = []goal.GoalEdge{{From: "service", To: "integration", Kind: goal.EdgeKindDependsOn}, {From: "client", To: "integration", Kind: goal.EdgeKindDependsOn}}
	inputs := goal.TeamInputs{SchemaVersion: goal.TeamInputsVersion, Spec: spec, Proposal: proposal, BaseSHA: strings.Repeat("a", 40), Limits: goal.Guardrails{MaxNodes: 3, MaxDepth: 3, MaxFanOut: 2, MaxConcurrentNodes: 3, MaxPlanRevisions: 2, MaxTotalRuns: 6, MaxTotalAttempts: 12, MaxWallTimeSeconds: 1000, MaxComputeUnits: 100, MaxTokens: 10000, MaxArtifactBytes: 10000}, AdmissionPolicy: goal.AdmissionPolicy{ExecutorKinds: []goal.ExecutorKind{goal.ExecutorKindImplement}, Repositories: []string{spec.Repository}, Paths: []string{"service.py", "client.py"}, SideEffectClasses: []string{"workspace-write"}}}
	for _, node := range proposal.Nodes {
		role := "implement"
		if node.NodeId == "integration" {
			role = "integrate"
		}
		inputs.Nodes = append(inputs.Nodes, goal.TeamNodeInputs{NodeID: node.NodeId, Role: role, Task: json.RawMessage(`{"storeFixture":"task"}`), Policy: json.RawMessage(`{"storeFixture":"policy"}`)})
	}
	raw := teamTestBytes(t, inputs)
	return inputs, raw, TeamPlanApproval{InputsDigest: canonical.DigestBytes(raw), RequestDigest: attemptTestDigest("authenticated-request")}
}

func TestTeamPlanAtomicReplayAndOwnerSuccessor(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	owner, _ := supervisorTestAcquireOwner(t, store, id)
	_, raw, approval := teamTestInputs(t, owner.Acquisition)
	before := reservationLedgerBytes(t, store)
	plan, err := store.AcceptInitialTeamPlan(context.Background(), teamTestApproval{owner.Acquisition, approval, false}, owner.Acquisition, approval, raw)
	if err != nil {
		t.Fatal(err)
	}
	after := reservationLedgerBytes(t, store)
	if bytes.Count(after, []byte{'\n'}) != bytes.Count(before, []byte{'\n'})+1 || len(plan.Materializations) != 3 || plan.Revision.PlanRevision != 1 || !bytes.Equal(plan.Inputs, raw) {
		t.Fatal("acceptance was not one complete fact")
	}
	for _, command := range plan.Materializations {
		if command.Reservation.Validate() != nil || command.TaskID == "" || command.RunID == "" {
			t.Fatal("missing reserved creation obligation")
		}
	}
	// Ignore the original response and reopen the physical ledger, as after
	// response loss. No in-memory admission state may decide this replay.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	owner2, _ := supervisorTestAcquireOwner(t, reopened, id)
	beforeReplay := reservationLedgerBytes(t, reopened)
	replayed, err := reopened.AcceptInitialTeamPlan(context.Background(), teamTestApproval{owner2.Acquisition, approval, false}, owner2.Acquisition, approval, raw)
	if err != nil || !bytes.Equal(teamTestBytes(t, replayed), teamTestBytes(t, plan)) || !bytes.Equal(beforeReplay, reservationLedgerBytes(t, reopened)) {
		t.Fatalf("replay consumed another plan/budget: %v", err)
	}
	read, found, err := reopened.ReadTeamPlan(owner2.Acquisition.Scope, "team-1")
	if err != nil || !found || read.FactDigest != plan.FactDigest {
		t.Fatalf("cold read: %v", err)
	}
	if _, err := reopened.AcceptInitialTeamPlan(context.Background(), teamTestApproval{owner.Acquisition, approval, false}, owner.Acquisition, approval, raw); !errors.Is(err, ErrControlOwnerNotCurrent) {
		t.Fatalf("stale owner: %v", err)
	}
}

func TestTeamPlanRejectsUnapprovedConflictAndOverBudgetWithoutAppend(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
	inputs, raw, approval := teamTestInputs(t, owner.Acquisition)
	before := reservationLedgerBytes(t, store)
	if _, err := store.AcceptInitialTeamPlan(context.Background(), nil, owner.Acquisition, approval, raw); err == nil {
		t.Fatal("missing approval accepted")
	}
	if _, err := store.AcceptInitialTeamPlan(context.Background(), teamTestApproval{owner.Acquisition, approval, true}, owner.Acquisition, approval, raw); err == nil {
		t.Fatal("denied approval accepted")
	}
	inputs.Limits.MaxTotalRuns = 2
	oversold := teamTestBytes(t, inputs)
	bad := approval
	bad.InputsDigest = canonical.DigestBytes(oversold)
	if _, err := store.AcceptInitialTeamPlan(context.Background(), teamTestApproval{owner.Acquisition, bad, false}, owner.Acquisition, bad, oversold); err == nil {
		t.Fatal("oversold budget accepted")
	}
	if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("rejection appended authority")
	}
	if _, err := store.AcceptInitialTeamPlan(context.Background(), teamTestApproval{owner.Acquisition, approval, false}, owner.Acquisition, approval, raw); err != nil {
		t.Fatal(err)
	}
	before = reservationLedgerBytes(t, store)
	inputs.Limits.MaxTotalRuns = 6
	inputs.Proposal.ProposalId = "proposal-new-budget-reset"
	changed := teamTestBytes(t, inputs)
	bad.InputsDigest = canonical.DigestBytes(changed)
	if _, err := store.AcceptInitialTeamPlan(context.Background(), teamTestApproval{owner.Acquisition, bad, false}, owner.Acquisition, bad, changed); !errors.Is(err, ErrTeamPlanConflict) {
		t.Fatalf("existing Goal reset: %v", err)
	}
	if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("conflict appended authority")
	}
}

func TestTeamPlanConcurrentSameApprovalHasOneCommit(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
	_, raw, approval := teamTestInputs(t, owner.Acquisition)
	before := reservationLedgerBytes(t, store)
	var wg sync.WaitGroup
	results := make(chan TeamPlanState, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			plan, err := store.AcceptInitialTeamPlan(context.Background(), teamTestApproval{owner.Acquisition, approval, false}, owner.Acquisition, approval, raw)
			if err != nil {
				t.Error(err)
			}
			results <- plan
		}()
	}
	wg.Wait()
	close(results)
	var digest string
	for plan := range results {
		if digest != "" && digest != plan.FactDigest {
			t.Fatal("two accepted heads")
		}
		digest = plan.FactDigest
	}
	if digest == "" || bytes.Count(reservationLedgerBytes(t, store), []byte{'\n'}) != bytes.Count(before, []byte{'\n'})+1 {
		t.Fatal("duplicate commit")
	}
}

func TestTeamPlanReplayRejectsForgedMaterializationAndDuplicateFact(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(map[bool]string{false: "forged-command", true: "duplicate-fact"}[duplicate], func(t *testing.T) {
			store, err := OpenResultIngressStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
			_, raw, approval := teamTestInputs(t, owner.Acquisition)
			if _, err := store.AcceptInitialTeamPlan(context.Background(), teamTestApproval{owner.Acquisition, approval, false}, owner.Acquisition, approval, raw); err != nil {
				t.Fatal(err)
			}
			lines := bytes.Split(bytes.TrimSpace(reservationLedgerBytes(t, store)), []byte{'\n'})
			var fact teamPlanFact
			if json.Unmarshal(lines[len(lines)-1], &fact) != nil {
				t.Fatal("decode fixture")
			}
			if duplicate {
				fact.Sequence++
			} else {
				fact.Plan.Materializations[0].RunID = "forged-run"
			}
			fact.Digest = ""
			fact.Digest = canonicalDigestOrEmpty(fact)
			if duplicate {
				lines = append(lines, teamTestBytes(t, fact))
			} else {
				lines[len(lines)-1] = teamTestBytes(t, fact)
			}
			if err := os.WriteFile(store.ledgerPath(), append(bytes.Join(lines, []byte{'\n'}), '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.ReadTeamPlan(owner.Acquisition.Scope, "team-1"); err == nil {
				t.Fatal("forged replay accepted")
			}
		})
	}
}
