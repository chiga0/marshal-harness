package resultingress

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/chiga0/marshal-harness/internal/goal"
	"testing"
	"time"
)

type taskCancellationTestVerifier struct {
	owner ControlOwnerAcquisition
	value TaskCancellation
}

func TestTaskCancelSelectionPastHundredAndRoundRobin(t *testing.T) {
	ctx := context.Background()
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
	reader := teamTestApproval{owner.Acquisition, TeamPlanApproval{}, false}
	var first, last goal.TaskDraft
	for n := 0; n < 101; n++ {
		draft, err := store.RecordTaskDraft(ctx, reader, owner.Acquisition, taskDraftTestInput(t, owner.Acquisition, fmt.Sprintf("page-%d", n), time.Now()))
		if err != nil {
			t.Fatal(err)
		}
		if first.GoalID == "" || draft.GoalID < first.GoalID {
			first = draft
		}
		if last.GoalID == "" || draft.GoalID > last.GoalID {
			last = draft
		}
	}
	if _, err := store.RequestTaskStop(ctx, reader, owner.Acquisition, last.GoalID, attemptTestDigest("last-key"), 1); err != nil {
		t.Fatal(err)
	}
	selected, err := store.NextTaskStop(owner.Acquisition.Scope, "")
	if err != nil || selected.TaskID != last.GoalID {
		t.Fatal("101st Task hidden by history page")
	}
	if _, err := store.RequestTaskStop(ctx, reader, owner.Acquisition, first.GoalID, attemptTestDigest("first-key"), 1); err != nil {
		t.Fatal(err)
	}
	selected, err = store.NextTaskStop(owner.Acquisition.Scope, last.GoalID)
	if err != nil || selected.TaskID != first.GoalID {
		t.Fatal("cursor did not wrap")
	}
	selected, err = store.NextTaskStop(owner.Acquisition.Scope, first.GoalID)
	if err != nil || selected.TaskID != last.GoalID {
		t.Fatal("pending intervention starved sibling")
	}
}

func (v taskCancellationTestVerifier) WithCurrentTaskCancellation(_ context.Context, owner ControlOwnerAcquisition, stop TaskStop, fn func(TaskCancellation) error) error {
	if owner != v.owner {
		return ErrControlOwnerNotCurrent
	}
	return fn(v.value)
}

func TestTaskCancelUnapprovedColdReplayAndApprovalFence(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
	verifier := teamTestApproval{owner.Acquisition, TeamPlanApproval{}, false}
	draft, err := store.RecordTaskDraft(ctx, verifier, owner.Acquisition, taskDraftTestInput(t, owner.Acquisition, "cancel-unapproved", time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	key := attemptTestDigest("cancel-key")
	stop, err := store.RequestTaskStop(ctx, verifier, owner.Acquisition, draft.GoalID, key, 1)
	if err != nil {
		t.Fatal(err)
	}
	before := reservationLedgerBytes(t, store)
	if _, done, rev, err := store.ReadTaskCancellation(owner.Acquisition.Scope, draft.GoalID); err != nil || done.FactDigest != "" || rev != 2 {
		t.Fatalf("intent not receipt: %v rev%d", err, rev)
	}
	approval := taskDraftTestApproval(draft)
	if _, err := store.AcceptInitialTeamPlan(ctx, teamTestApproval{owner.Acquisition, approval, false}, owner.Acquisition, approval, draft.Inputs); !errors.Is(err, ErrTaskStopped) {
		t.Fatalf("cancel then approve: %v", err)
	}
	value := TaskCancellation{TaskID: draft.GoalID, StopFactDigest: stop.FactDigest, Nodes: []TaskNodeDisposition{}}
	done, err := store.RecordTaskCancellation(ctx, taskCancellationTestVerifier{owner.Acquisition, value}, owner.Acquisition, draft.GoalID)
	if err != nil || done.FactDigest == "" {
		t.Fatalf("close empty obligations: %v", err)
	}
	after := reservationLedgerBytes(t, store)
	if bytes.Count(after, []byte{'\n'}) != bytes.Count(before, []byte{'\n'})+1 {
		t.Fatal("disposition must append once")
	}
	if _, err := store.RequestTaskStop(ctx, verifier, owner.Acquisition, draft.GoalID, key, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RequestTaskStop(ctx, verifier, owner.Acquisition, draft.GoalID, key, 3); !errors.Is(err, ErrTeamPlanConflict) {
		t.Fatal("same key changed content admitted")
	}
	if !bytes.Equal(after, reservationLedgerBytes(t, store)) {
		t.Fatal("replay appended")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	coldStop, coldDone, rev, err := store.ReadTaskCancellation(owner.Acquisition.Scope, draft.GoalID)
	if err != nil || coldStop != stop || coldDone.FactDigest != done.FactDigest || rev != 3 {
		t.Fatalf("cold disposition: %v rev%d", err, rev)
	}
}

func TestTaskCancelApprovedCASRevokesAllObligations(t *testing.T) {
	ctx := context.Background()
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
	reader := teamTestApproval{owner.Acquisition, TeamPlanApproval{}, false}
	draft, err := store.RecordTaskDraft(ctx, reader, owner.Acquisition, taskDraftTestInput(t, owner.Acquisition, "cancel-approved", time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	approval := taskDraftTestApproval(draft)
	verifier := teamTestApproval{owner.Acquisition, approval, false}
	plan, err := store.AcceptInitialTeamPlan(ctx, verifier, owner.Acquisition, approval, draft.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	key := attemptTestDigest("approved-cancel-key")
	before := reservationLedgerBytes(t, store)
	if _, err := store.RequestTaskStop(ctx, reader, owner.Acquisition, draft.GoalID, key, 1); !errors.Is(err, ErrTeamPlanConflict) {
		t.Fatalf("stale CAS: %v", err)
	}
	if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("stale CAS appended")
	}
	stop, err := store.RequestTaskStop(ctx, reader, owner.Acquisition, draft.GoalID, key, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptInitialTeamPlan(ctx, verifier, owner.Acquisition, approval, draft.Inputs); err != nil {
		t.Fatalf("original approve response loss: %v", err)
	}
	value := TaskCancellation{TaskID: draft.GoalID, StopFactDigest: stop.FactDigest, PlanFactDigest: plan.FactDigest}
	for _, node := range plan.Materializations {
		value.Nodes = append(value.Nodes, TaskNodeDisposition{NodeID: node.NodeID, RunID: node.RunID, Disposition: "not-created"})
		if err := store.RequireTaskRunNotStopped(owner.Acquisition.Scope.AuthorityNamespaceID, node.RunID); !errors.Is(err, ErrTaskStopped) {
			t.Fatal("run fence missing")
		}
	}
	bad := value
	bad.Nodes = value.Nodes[:2]
	if _, err := store.RecordTaskCancellation(ctx, taskCancellationTestVerifier{owner.Acquisition, bad}, owner.Acquisition, draft.GoalID); err == nil {
		t.Fatal("incomplete coverage accepted")
	}
	bad = value
	bad.Nodes = append([]TaskNodeDisposition{}, value.Nodes...)
	bad.Nodes[1] = bad.Nodes[0]
	if _, err := store.RecordTaskCancellation(ctx, taskCancellationTestVerifier{owner.Acquisition, bad}, owner.Acquisition, draft.GoalID); err == nil {
		t.Fatal("duplicate node accepted")
	}
	if _, err := store.RecordTaskCancellation(ctx, taskCancellationTestVerifier{owner.Acquisition, value}, owner.Acquisition, draft.GoalID); err != nil {
		t.Fatal(err)
	}
	before = reservationLedgerBytes(t, store)
	if _, err := store.RequestTaskStop(ctx, reader, owner.Acquisition, draft.GoalID, key, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordTaskCancellation(ctx, taskCancellationTestVerifier{owner.Acquisition, value}, owner.Acquisition, draft.GoalID); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("exact replay consumed more authority")
	}
}
