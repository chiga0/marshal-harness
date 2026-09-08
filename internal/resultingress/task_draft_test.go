package resultingress

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/goal"
)

func taskDraftTestInput(t *testing.T, owner ControlOwnerAcquisition, key string, created time.Time) goal.TaskDraft {
	t.Helper()
	keyDigest := attemptTestDigest("task-key:" + key)
	id, err := TaskIDForRequest(owner.Scope, keyDigest)
	if err != nil {
		t.Fatal(err)
	}
	inputs, _, _ := teamTestInputs(t, owner)
	inputs.Spec.GoalId, inputs.Proposal.GoalId = id, id
	inputs.Proposal.GoalSpecDigest, err = inputs.Spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	raw := teamTestBytes(t, inputs)
	request := goal.TaskSubmission{Template: goal.TaskTemplateOrderQuote, Intent: "生成订单报价功能", Context: goal.TaskContext{Text: "RB1 fixture"}}
	created = created.UTC()
	draft := goal.TaskDraft{GoalID: id, Revision: 1, RequestKeyDigest: keyDigest, RequestDigest: canonical.DigestBytes(teamTestBytes(t, request)), Request: request, TemplateDigest: attemptTestDigest("task-template"), InputsDigest: canonical.DigestBytes(raw), Inputs: raw, CreatedAt: created.Format(time.RFC3339Nano), ConfirmBefore: created.Add(30 * time.Minute).Format(time.RFC3339Nano)}
	if err := draft.Validate(); err != nil {
		t.Fatal(err)
	}
	return draft
}

func taskDraftTestApproval(draft goal.TaskDraft) TeamPlanApproval {
	return TeamPlanApproval{InputsDigest: draft.InputsDigest, RequestDigest: attemptTestDigest("confirm:" + draft.GoalID), TaskDraftDigest: draft.FactDigest}
}

func TestTaskDraftColdConfirmationIsOneExistingPlan(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	id := attemptTestIdentity()
	owner, _ := supervisorTestAcquireOwner(t, store, id)
	draft := taskDraftTestInput(t, owner.Acquisition, "create-1", time.Now())
	before := reservationLedgerBytes(t, store)
	created, err := store.RecordTaskDraft(ctx, teamTestApproval{owner.Acquisition, TeamPlanApproval{}, false}, owner.Acquisition, draft)
	if err != nil || created.FactDigest == "" {
		t.Fatalf("draft: %v", err)
	}
	afterCreate := reservationLedgerBytes(t, store)
	if bytes.Count(afterCreate, []byte{'\n'}) != bytes.Count(before, []byte{'\n'})+1 {
		t.Fatal("draft did not append exactly one fact")
	}
	if _, found, err := store.ReadTeamPlan(owner.Acquisition.Scope, draft.GoalID); err != nil || found {
		t.Fatalf("draft approved plan: %t %v", found, err)
	}
	if got, err := store.ListTeamCreationObligations(owner.Acquisition.Scope); err != nil || len(got) != 0 {
		t.Fatalf("draft created Runs: %v", err)
	}
	retry := draft
	retry.TemplateDigest = attemptTestDigest("new-template")
	retry.CreatedAt = time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)
	newTime, _ := time.Parse(time.RFC3339Nano, retry.CreatedAt)
	retry.ConfirmBefore = newTime.Add(30 * time.Minute).Format(time.RFC3339Nano)
	replayed, err := store.RecordTaskDraft(ctx, teamTestApproval{owner.Acquisition, TeamPlanApproval{}, false}, owner.Acquisition, retry)
	if err != nil || !bytes.Equal(teamTestBytes(t, replayed), teamTestBytes(t, created)) {
		t.Fatalf("retry refreshed draft: %v", err)
	}
	changed := draft
	changed.Request.Intent = "另一个请求"
	changed.RequestDigest = canonical.DigestBytes(teamTestBytes(t, changed.Request))
	if _, err := store.RecordTaskDraft(ctx, teamTestApproval{owner.Acquisition, TeamPlanApproval{}, false}, owner.Acquisition, changed); !errors.Is(err, ErrTeamPlanConflict) {
		t.Fatalf("same key changed request: %v", err)
	}
	if !bytes.Equal(afterCreate, reservationLedgerBytes(t, store)) {
		t.Fatal("retry changed ledger")
	}
	oldOwner := owner
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	owner, _ = supervisorTestAcquireOwner(t, store, id)
	before = reservationLedgerBytes(t, store)
	cold, found, err := store.ReadTaskDraft(owner.Acquisition.Scope, draft.GoalID)
	if err != nil || !found || !bytes.Equal(teamTestBytes(t, cold), teamTestBytes(t, created)) {
		t.Fatalf("cold draft: %v", err)
	}
	if _, err := store.RecordTaskDraft(ctx, teamTestApproval{oldOwner.Acquisition, TeamPlanApproval{}, false}, oldOwner.Acquisition, draft); !errors.Is(err, ErrControlOwnerNotCurrent) {
		t.Fatalf("old owner: %v", err)
	}
	ids, err := store.ListTaskDraftIDs(owner.Acquisition.Scope, "", 1)
	if err != nil || len(ids) != 1 || ids[0] != draft.GoalID {
		t.Fatalf("list: %v %v", ids, err)
	}
	ids, err = store.ListTaskDraftIDs(owner.Acquisition.Scope, draft.GoalID, 1)
	if err != nil || len(ids) != 0 {
		t.Fatal("exclusive cursor repeated Task")
	}
	approval := taskDraftTestApproval(created)
	for _, link := range []string{"", attemptTestDigest("wrong-draft")} {
		bad := approval
		bad.TaskDraftDigest = link
		if _, err := store.AcceptInitialTeamPlan(ctx, teamTestApproval{owner.Acquisition, bad, false}, owner.Acquisition, bad, created.Inputs); !errors.Is(err, ErrTeamPlanConflict) {
			t.Fatalf("unbound approval: %v", err)
		}
	}
	if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("reads or rejection appended")
	}
	plan, err := store.AcceptInitialTeamPlan(ctx, teamTestApproval{owner.Acquisition, approval, false}, owner.Acquisition, approval, created.Inputs)
	if err != nil || len(plan.Materializations) != 3 || plan.FactDigest == "" || plan.Approval != approval {
		t.Fatalf("confirm: %v", err)
	}
	for _, obligation := range plan.Materializations {
		if obligation.Reservation.Validate() != nil || obligation.TaskID == "" || obligation.RunID == "" {
			t.Fatal("invalid frozen obligation")
		}
	}
	after := reservationLedgerBytes(t, store)
	if bytes.Count(after, []byte{'\n'}) != bytes.Count(before, []byte{'\n'})+1 {
		t.Fatal("confirmation was not one fact")
	}
	again, err := store.AcceptInitialTeamPlan(ctx, teamTestApproval{owner.Acquisition, approval, false}, owner.Acquisition, approval, created.Inputs)
	if err != nil || !bytes.Equal(teamTestBytes(t, again), teamTestBytes(t, plan)) || !bytes.Equal(after, reservationLedgerBytes(t, store)) {
		t.Fatalf("confirm replay: %v", err)
	}
}

func TestTaskDraftExpiredFirstConfirmationAndColdAcceptedReplay(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	id := attemptTestIdentity()
	owner, _ := supervisorTestAcquireOwner(t, store, id)
	input := taskDraftTestInput(t, owner.Acquisition, "expired", time.Now().Add(-time.Hour))
	draft, err := store.RecordTaskDraft(ctx, teamTestApproval{owner.Acquisition, TeamPlanApproval{}, false}, owner.Acquisition, input)
	if err != nil {
		t.Fatal(err)
	}
	approval := taskDraftTestApproval(draft)
	before := reservationLedgerBytes(t, store)
	if _, err := store.AcceptInitialTeamPlan(ctx, teamTestApproval{owner.Acquisition, approval, false}, owner.Acquisition, approval, draft.Inputs); !errors.Is(err, ErrTaskDraftExpired) {
		t.Fatalf("expired admission: %v", err)
	}
	if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("expired rejection appended")
	}
	// Explicit historical prefix fixture, not production approval: represent
	// a plan committed while its draft was live, without sleeps/clock races.
	_, plan, err := deriveInitialTeam(owner.Acquisition.Scope, approval, draft.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	err = store.transact(newAuthorityProjection(), func() error {
		fact := &teamPlanFact{ProtocolRevision: teamPlanProtocol, FactType: teamPlanFactType, Sequence: store.nextSequence, Scope: owner.Acquisition.Scope, OwnerFactDigest: owner.FactDigest, Plan: plan}
		if err := store.appendLine(fact, func() string { return fact.Digest }, func(digest string) { fact.Digest = digest }); err != nil {
			return err
		}
		store.nextSequence++
		plan.FactDigest = fact.Digest
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	owner, _ = supervisorTestAcquireOwner(t, store, id)
	before = reservationLedgerBytes(t, store)
	again, err := store.AcceptInitialTeamPlan(ctx, teamTestApproval{owner.Acquisition, approval, false}, owner.Acquisition, approval, draft.Inputs)
	if err != nil || !bytes.Equal(teamTestBytes(t, again), teamTestBytes(t, plan)) {
		t.Fatalf("cold accepted replay: %v", err)
	}
	bad := approval
	bad.RequestDigest = attemptTestDigest("other-confirmation")
	if _, err := store.AcceptInitialTeamPlan(ctx, teamTestApproval{owner.Acquisition, bad, false}, owner.Acquisition, bad, draft.Inputs); !errors.Is(err, ErrTeamPlanConflict) {
		t.Fatalf("non-identical replay: %v", err)
	}
	if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("replay changed ledger")
	}
}
