package resultingress

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/goal"
)

type taskDeliveryTestVerifier struct {
	owner ControlOwnerAcquisition
	value goal.TaskDelivery
	skip  bool
}

func (v taskDeliveryTestVerifier) WithCurrentTaskDelivery(_ context.Context, owner ControlOwnerAcquisition, id string, consume func(goal.TaskDelivery) error) error {
	if v.skip {
		return nil
	}
	if owner != v.owner || id != v.value.GoalID {
		return ErrTeamPlanConflict
	}
	return consume(v.value)
}

func taskDeliveryStoreFixture(t *testing.T, store *DurableStore, owner ControlOwnerAcquisition) taskDeliveryTestVerifier {
	t.Helper()
	ctx := context.Background()
	draft := taskDraftTestInput(t, owner, "delivery", time.Now())
	var inputs goal.TeamInputs
	if json.Unmarshal(draft.Inputs, &inputs) != nil {
		t.Fatal("fixture")
	}
	for i := range inputs.Nodes {
		node := &inputs.Nodes[i]
		taskID, runID, err := goal.TeamNodeIDs(inputs.Proposal, node.NodeID)
		if err != nil {
			t.Fatal(err)
		}
		node.Task = teamTestBytes(t, map[string]any{"metadata": map[string]any{"id": taskID}, "repository": map[string]any{"path": inputs.Spec.Repository, "baseRef": inputs.BaseSHA}})
		node.Policy = teamTestBytes(t, map[string]any{"taskId": taskID, "runId": runID})
	}
	draft.Inputs = teamTestBytes(t, inputs)
	draft.InputsDigest = canonical.DigestBytes(draft.Inputs)
	draft, err := store.RecordTaskDraft(ctx, teamTestApproval{owner, TeamPlanApproval{}, false}, owner, draft)
	if err != nil {
		t.Fatal(err)
	}
	approval := taskDraftTestApproval(draft)
	plan, err := store.AcceptInitialTeamPlan(ctx, teamTestApproval{owner, approval, false}, owner, approval, draft.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	prepared := teamTestBytes(t, map[string]any{"repositoryRoot": inputs.Spec.Repository, "baseSha": inputs.BaseSHA, "preparedAt": time.Now().UTC(), "capability": map[string]any{"adapterId": "pi", "probeStatus": "supported"}, "selectionAttempts": []any{map[string]any{"AdapterID": "pi", "Outcome": "selected"}}})
	completed := completedTaskPlanFixture(t, store, owner, plan, approval, prepared)
	outcome, err := store.CompleteTeam(ctx, completed, owner, approval, draft.GoalID, plan.FactDigest)
	if err != nil {
		t.Fatal(err)
	}
	value := goal.TaskDelivery{GoalID: draft.GoalID, OutcomeFactDigest: outcome.FactDigest, PlanFactDigest: plan.FactDigest, IntegrationRunID: outcome.Integration.RunID, IntegrationBaseSHA: outcome.IntegrationBaseSHA, MediaType: "application/zip", ContentBytes: 100, ContentDigest: attemptTestDigest("complete-zip")}
	for _, source := range append(outcome.Upstreams, outcome.Integration) {
		value.CandidateDigests = append(value.CandidateDigests, source.CandidateDigest)
		value.PatchDigests = append(value.PatchDigests, source.PatchDigest)
		value.DecisionDigests = append(value.DecisionDigests, source.DecisionDigest)
	}
	for _, name := range []string{"quote_api.py", "quote_client.py", "quote_delivery.json"} {
		value.Files = append(value.Files, goal.TaskDeliveryFile{Path: name, SHA256: attemptTestDigest(name), Bytes: 10})
	}
	return taskDeliveryTestVerifier{owner: owner, value: value}
}

func TestTaskDeliveryReferenceDurableOnceAndColdReplay(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
	v := taskDeliveryStoreFixture(t, store, owner.Acquisition)
	before := reservationLedgerBytes(t, store)
	created, err := store.RecordTaskDelivery(ctx, v, v.owner, v.value.GoalID)
	if err != nil || created.FactDigest == "" {
		t.Fatal("record", err)
	}
	after := reservationLedgerBytes(t, store)
	if bytes.Count(after, []byte{'\n'}) != bytes.Count(before, []byte{'\n'})+1 {
		t.Fatal("not one append")
	}
	again, err := store.RecordTaskDelivery(ctx, v, v.owner, v.value.GoalID)
	if err != nil || canonicalDigestOrEmpty(again) != canonicalDigestOrEmpty(created) || !bytes.Equal(after, reservationLedgerBytes(t, store)) {
		t.Fatal("replay mutated", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	owner, _ = supervisorTestAcquireOwner(t, store, attemptTestIdentity())
	old := v.owner
	v.owner = owner.Acquisition
	read, found, err := store.ReadTaskDelivery(v.owner.Scope, v.value.GoalID)
	if err != nil || !found || canonicalDigestOrEmpty(read) != canonicalDigestOrEmpty(created) {
		t.Fatal("cold reference lost", err)
	}
	before = reservationLedgerBytes(t, store)
	if _, err := store.RecordTaskDelivery(ctx, taskDeliveryTestVerifier{owner: old, value: v.value}, old, v.value.GoalID); err == nil {
		t.Fatal("stale owner admitted")
	}
	for _, mode := range []string{"skip", "wrong-outcome", "wrong-source", "last-patch-only", "content-drift", "wrong-path"} {
		t.Run(mode, func(t *testing.T) {
			raw := teamTestBytes(t, v.value)
			var value goal.TaskDelivery
			_ = json.Unmarshal(raw, &value)
			bad := taskDeliveryTestVerifier{owner: v.owner, value: value}
			switch mode {
			case "skip":
				bad.skip = true
			case "wrong-outcome":
				bad.value.OutcomeFactDigest = attemptTestDigest("changed")
			case "wrong-source":
				bad.value.CandidateDigests[0] = attemptTestDigest("changed")
			case "last-patch-only":
				bad.value.PatchDigests = bad.value.PatchDigests[2:]
			case "content-drift":
				bad.value.ContentDigest = attemptTestDigest("changed")
			case "wrong-path":
				bad.value.Files[0].Path = "../secret"
			}
			if _, err := store.RecordTaskDelivery(ctx, bad, bad.owner, bad.value.GoalID); err == nil {
				t.Fatal("bad reference accepted")
			}
		})
	}
	if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("negative cases appended")
	}
}
