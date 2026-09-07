package resultingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/goal"
)

// Store-only verifier. This is not production accepted-Run authority; session
// tests exercise real owner/lease rejection and live dogfood remains required.
type teamAcceptedFixtureVerifier struct {
	owner    ControlOwnerAcquisition
	approval TeamPlanApproval
	input    TeamIntegrationInputs
}

func (v teamAcceptedFixtureVerifier) WithCurrentAcceptedTeam(_ context.Context, owner ControlOwnerAcquisition, approval TeamPlanApproval, input TeamIntegrationInputs, fn func() error) error {
	want, e1 := v.input.Digest()
	actual, e2 := input.Digest()
	if owner != v.owner || approval != v.approval || e1 != nil || e2 != nil || actual != want {
		return errors.New("fixture accepted authority mismatch")
	}
	return fn()
}

func TestTeamIntegrationFreezesExactDerivedTaskAndReplays(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
	plan, approval, original := teamCreationFixture(t, store, owner.Acquisition)
	var input goal.TeamInputs
	if err := json.Unmarshal(plan.Inputs, &input); err != nil {
		t.Fatal(err)
	}
	var prepared map[string]json.RawMessage
	if err := json.Unmarshal(original, &prepared); err != nil {
		t.Fatal(err)
	}
	bound := TeamIntegrationInputs{GoalID: "team-1", NodeID: "integration", PlanFactDigest: plan.FactDigest, BaseSHA: input.BaseSHA}
	var integration goal.TeamNodeInputs
	for _, node := range input.Nodes {
		if node.Role == "integrate" {
			integration = node
			continue
		}
		_, runID, err := goal.TeamNodeIDs(input.Proposal, node.NodeID)
		if err != nil {
			t.Fatal(err)
		}
		prepared["runId"] = teamTestBytes(t, runID)
		prepared["task"], prepared["policy"] = node.Task, node.Policy
		creation, err := store.FreezeInitialTeamRun(context.Background(), teamTestApproval{owner.Acquisition, approval, false}, owner.Acquisition, approval, "team-1", node.NodeID, plan.FactDigest, teamTestBytes(t, prepared))
		if err != nil {
			t.Fatal(err)
		}
		bound.Sources = append(bound.Sources, TeamAcceptedSource{NodeID: node.NodeID, RunID: runID, AttemptID: "fixture-attempt", AuthorityHead: attemptTestDigest("head-" + node.NodeID), CreationFactDigest: creation.FactDigest,
			CandidateDigest: attemptTestDigest("candidate-" + node.NodeID), PatchDigest: attemptTestDigest("patch-" + node.NodeID), DecisionDigest: attemptTestDigest("decision-" + node.NodeID), PacketDigest: attemptTestDigest("packet-" + node.NodeID), OutcomeDigest: attemptTestDigest("outcome-" + node.NodeID)})
	}
	sort.Slice(bound.Sources, func(i, j int) bool { return bound.Sources[i].NodeID < bound.Sources[j].NodeID })
	digest, err := bound.Digest()
	if err != nil {
		t.Fatal(err)
	}
	derived := TeamIntegrationBase{Inputs: bound, InputsDigest: digest, TreeSHA: strings.Repeat("d", 40), CommitSHA: strings.Repeat("c", 40)}
	task, err := DeriveTeamIntegrationTask(integration.Task, input.BaseSHA, derived.CommitSHA)
	if err != nil {
		t.Fatal(err)
	}
	_, runID, err := goal.TeamNodeIDs(input.Proposal, integration.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	prepared["runId"], prepared["baseSha"] = teamTestBytes(t, runID), teamTestBytes(t, derived.CommitSHA)
	prepared["task"], prepared["policy"] = task, integration.Policy
	raw := teamTestBytes(t, prepared)
	verifier := teamAcceptedFixtureVerifier{owner.Acquisition, approval, bound}
	before := reservationLedgerBytes(t, store)
	if _, err := store.FreezeInitialTeamRun(context.Background(), teamTestApproval{owner.Acquisition, approval, false}, owner.Acquisition, approval, "team-1", "integration", plan.FactDigest, raw); err == nil {
		t.Fatal("initial implement entry admitted integration")
	}
	if _, err := store.FreezeIntegrationTeamRun(context.Background(), nil, owner.Acquisition, approval, "team-1", "integration", plan.FactDigest, raw, derived); err == nil {
		t.Fatal("unverified integration freeze")
	}
	if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("rejection mutated ledger")
	}
	for _, mode := range []string{"source-order", "source-creation", "wrong-base", "wrong-plan", "bad-commit", "changed-task"} {
		t.Run(mode, func(t *testing.T) {
			var bad TeamIntegrationBase
			if err := json.Unmarshal(teamTestBytes(t, derived), &bad); err != nil {
				t.Fatal(err)
			}
			badRaw := raw
			switch mode {
			case "source-order":
				bad.Inputs.Sources[0], bad.Inputs.Sources[1] = bad.Inputs.Sources[1], bad.Inputs.Sources[0]
			case "source-creation":
				bad.Inputs.Sources[0].CreationFactDigest = attemptTestDigest("not-the-original-creation")
			case "wrong-base":
				bad.Inputs.BaseSHA = strings.Repeat("b", 40)
			case "wrong-plan":
				bad.Inputs.PlanFactDigest = attemptTestDigest("different-plan")
			case "bad-commit":
				bad.CommitSHA = "HEAD"
			case "changed-task":
				var document map[string]json.RawMessage
				if err := json.Unmarshal(task, &document); err != nil {
					t.Fatal(err)
				}
				document["work"] = teamTestBytes(t, map[string]any{"context": []string{"changed unapproved objective"}})
				copyPrepared := map[string]json.RawMessage{}
				for key, value := range prepared {
					copyPrepared[key] = value
				}
				copyPrepared["task"] = teamTestBytes(t, document)
				badRaw = teamTestBytes(t, copyPrepared)
			}
			bad.InputsDigest, err = bad.Inputs.Digest()
			if err != nil {
				t.Fatal(err)
			}
			// Even a test verifier claiming current acceptance cannot override
			// the store's original plan/creation/template constraints.
			badVerifier := teamAcceptedFixtureVerifier{owner.Acquisition, approval, bad.Inputs}
			if _, err := store.FreezeIntegrationTeamRun(context.Background(), badVerifier, owner.Acquisition, approval, "team-1", "integration", plan.FactDigest, badRaw, bad); err == nil {
				t.Fatal("invalid integration binding admitted")
			}
			if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
				t.Fatal("rejection changed ledger")
			}
		})
	}
	created, err := store.FreezeIntegrationTeamRun(context.Background(), verifier, owner.Acquisition, approval, "team-1", "integration", plan.FactDigest, raw, derived)
	if err != nil || created.Integration == nil {
		t.Fatal("valid integration freeze", err)
	}
	if bytes.Count(reservationLedgerBytes(t, store), []byte{'\n'}) != bytes.Count(before, []byte{'\n'})+1 {
		t.Fatal("integration freeze added a reservation or extra fact")
	}
	before = reservationLedgerBytes(t, store)
	replayed, err := store.FreezeIntegrationTeamRun(context.Background(), verifier, owner.Acquisition, approval, "team-1", "integration", plan.FactDigest, raw, derived)
	if err != nil || replayed.FactDigest != created.FactDigest || !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("exact replay changed creation", err)
	}
	prepared["task"] = integration.Task
	if _, err := store.FreezeIntegrationTeamRun(context.Background(), verifier, owner.Acquisition, approval, "team-1", "integration", plan.FactDigest, teamTestBytes(t, prepared), derived); err == nil {
		t.Fatal("original base replaced derived accepted inputs")
	}
	if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("conflict appended")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	actual, found, err := store.ReadTeamRunCreation(owner.Acquisition.Scope, "team-1", "integration")
	if err != nil || !found || actual.FactDigest != created.FactDigest || !equalTeamIntegration(actual.Integration, &derived) {
		t.Fatal("cold integration record lost", err)
	}
	obligation, member, err := store.ReadTeamRunObligation(owner.Acquisition.Scope, runID)
	if err != nil || !member || obligation.Creation.FactDigest != created.FactDigest {
		t.Fatal("integration not on original Run membership path", err)
	}
}

func TestTeamIntegrationTaskDerivationPreservesCompleteTemplate(t *testing.T) {
	base, target := strings.Repeat("a", 40), strings.Repeat("b", 40)
	template := teamTestBytes(t, map[string]any{"repository": map[string]any{"baseRef": base, "expectedRemoteUrl": "https://example.invalid/repo"}, "work": map[string]any{"context": []string{"keep"}}, "deliverables": []any{map[string]any{"kind": "code"}}})
	derived, err := DeriveTeamIntegrationTask(template, base, target)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DeriveTeamIntegrationTask(derived, target, base)
	if err != nil || canonical.DigestBytes(restored) != canonical.DigestBytes(template) {
		t.Fatal("non-base fields changed", err)
	}
	if _, err := DeriveTeamIntegrationTask(template, target, base); err == nil {
		t.Fatal("wrong original base accepted")
	}
}
