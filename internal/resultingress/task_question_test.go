package resultingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/goal"
)

func taskQuestionTestRoot(t *testing.T, owner ControlOwnerAcquisition, key string, created time.Time) goal.TaskClarificationRoot {
	t.Helper()
	draft := taskDraftTestInput(t, owner, key, created)
	var inputs goal.TeamInputs
	if json.Unmarshal(draft.Inputs, &inputs) != nil {
		t.Fatal("inputs")
	}
	// The store fixture does not claim full Task/Policy schema validation;
	// held RepositorySession tests use complete real schema examples.
	for n := range inputs.Nodes {
		inputs.Nodes[n].Task = teamTestBytes(t, map[string]any{"work": map[string]any{"objective": "store fixture", "context": []string{"original context"}}, "acceptance": map[string]any{"frozen": "oracle"}})
	}
	request := draft.Request
	request.Template = "test-question-inputs/v1"
	template := goal.TaskQuestionTemplate{ID: request.Template, TemplateDigest: attemptTestDigest("q-template"), ProducerDigest: attemptTestDigest("q-producer"), ValidatorDigest: attemptTestDigest("q-validator"), RendererDigest: goal.TaskQuestionRendererDigest(), Slots: []goal.TaskInputSlot{{ID: "audience", Prompt: "指定测试交付说明的读者", NodeIDs: []string{"service", "integration"}}, {ID: "example", Prompt: "指定测试交付说明的示例", NodeIDs: []string{"client", "integration"}}}}
	root := goal.TaskClarificationRoot{TaskID: draft.GoalID, Request: request, RequestKeyDigest: draft.RequestKeyDigest, RequestDigest: canonical.DigestBytes(teamTestBytes(t, request)), CreatedAt: draft.CreatedAt, ConfirmBefore: draft.ConfirmBefore, Template: template, Inputs: teamTestBytes(t, inputs), InitialValues: []goal.TaskSlotValue{}, Questions: []goal.TaskQuestion{}}
	root.InputsDigest = canonical.DigestBytes(root.Inputs)
	for _, slot := range template.Slots {
		root.Questions = append(root.Questions, goal.TaskQuestion{ID: goal.QuestionID(root.TaskID, root.InputsDigest, slot.ID), SlotID: slot.ID, Revision: 1, Prompt: slot.Prompt})
	}
	if err := root.Validate(); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestTaskQuestionConcurrentCommandsHaveOneLinearization(t *testing.T) {
	for _, mode := range []string{"same-answer", "different-answers", "cancel-answer"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store, err := OpenResultIngressStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
			reader := teamTestApproval{owner.Acquisition, TeamPlanApproval{}, false}
			state, err := store.RecordTaskClarification(ctx, reader, owner.Acquisition, taskQuestionTestRoot(t, owner.Acquisition, mode, time.Now()))
			if err != nil {
				t.Fatal(err)
			}
			first, firstInputs := taskQuestionTestAnswer(t, state, 0, "concurrent-one")
			second, secondInputs := first, firstInputs
			if mode == "different-answers" {
				second, secondInputs = taskQuestionTestAnswer(t, state, 1, "concurrent-two")
			}
			before := reservationLedgerBytes(t, store)
			ready := make(chan struct{})
			results := make(chan error, 2)
			go func() {
				<-ready
				_, _, err := store.RecordTaskQuestionAnswer(ctx, reader, owner.Acquisition, first, firstInputs)
				results <- err
			}()
			go func() {
				<-ready
				var err error
				if mode == "cancel-answer" {
					_, err = store.RequestTaskStop(ctx, reader, owner.Acquisition, state.Root.TaskID, attemptTestDigest("concurrent-cancel"), 1)
				} else {
					_, _, err = store.RecordTaskQuestionAnswer(ctx, reader, owner.Acquisition, second, secondInputs)
				}
				results <- err
			}()
			close(ready)
			success := 0
			for range 2 {
				err := <-results
				if err == nil {
					success++
				} else if !errors.Is(err, ErrTeamPlanConflict) && !errors.Is(err, ErrTaskStopped) {
					t.Fatal(err)
				}
			}
			expectedSuccess := 1
			if mode == "same-answer" {
				expectedSuccess = 2
			}
			if success != expectedSuccess {
				t.Fatalf("%s successes=%d", mode, success)
			}
			if bytes.Count(reservationLedgerBytes(t, store), []byte{'\n'}) != bytes.Count(before, []byte{'\n'})+1 {
				t.Fatal("concurrent commands appended twice")
			}
			_, _, revision, err := store.ReadTaskCancellation(owner.Acquisition.Scope, state.Root.TaskID)
			if err != nil || revision != 2 {
				t.Fatal("nonlinear control revision", revision, err)
			}
		})
	}
}

func TestTaskQuestionMalformedOrInterruptedReplayCannotApprove(t *testing.T) {
	for _, mode := range []string{"truncated-answer", "unknown-version", "root-binding", "forged-answer"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			ctx := context.Background()
			store, err := OpenResultIngressStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
			reader := teamTestApproval{owner.Acquisition, TeamPlanApproval{}, false}
			state, err := store.RecordTaskClarification(ctx, reader, owner.Acquisition, taskQuestionTestRoot(t, owner.Acquisition, mode, time.Now()))
			if err != nil {
				t.Fatal(err)
			}
			q, inputs := taskQuestionTestAnswer(t, state, 0, "replay-test")
			if _, _, err := store.RecordTaskQuestionAnswer(ctx, reader, owner.Acquisition, q, inputs); err != nil {
				t.Fatal(err)
			}
			original := reservationLedgerBytes(t, store)
			lines := bytes.Split(bytes.TrimSuffix(original, []byte{'\n'}), []byte{'\n'})
			var fact taskQuestionFact
			if json.Unmarshal(lines[len(lines)-1], &fact) != nil {
				t.Fatal("answer fact")
			}
			switch mode {
			case "unknown-version":
				fact.ProtocolRevision = "task-clarification/unknown"
			case "root-binding":
				fact.RootFactDigest = attemptTestDigest("foreign-root")
			case "forged-answer":
				fact.Receipt.Request.Answer = "changed without changing exact frozen inputs"
			}
			// Recompute the detached digest, so negative cases hit protocol/current
			// producer-chain checks rather than failing only the outer hash.
			fact.Digest = ""
			fact.Digest = canonicalDigestOrEmpty(fact)
			lines[len(lines)-1] = teamTestBytes(t, fact)
			damaged := append(bytes.Join(lines, []byte{'\n'}), '\n')
			if mode == "truncated-answer" {
				damaged = damaged[:len(damaged)-7]
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, resultIngressStoreFileName), damaged, 0o600); err != nil {
				t.Fatal(err)
			}
			cold, err := OpenResultIngressStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer cold.Close()
			if _, _, err := cold.ReadTaskProposal(owner.Acquisition.Scope, state.Root.TaskID); !errors.Is(err, ErrDurableReplayConflict) {
				t.Fatalf("malformed authority was skipped: %v", err)
			}
			if got, err := os.ReadFile(filepath.Join(dir, resultIngressStoreFileName)); err != nil || !bytes.Equal(got, damaged) {
				t.Fatal("read repaired malformed authority")
			}
		})
	}
}

func taskQuestionTestAnswer(t *testing.T, state TaskClarificationState, index int, key string) (TaskQuestionAnswer, []byte) {
	t.Helper()
	q := state.Root.Questions[index]
	request := TaskQuestionAnswer{TaskID: state.Root.TaskID, QuestionID: q.ID, RequestKeyDigest: attemptTestDigest(key), ExpectedRevision: state.PreviewRevision, PreviewDigest: state.PreviewDigest, QuestionRevision: 1, Answer: "valid " + q.SlotID}
	values, err := questionAnswerValues(state, &request)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := goal.RenderTaskQuestionInputs(state.Root, values)
	if err != nil {
		t.Fatal(err)
	}
	return request, raw
}

func TestTaskQuestionAtomicAnswerPreviewAndColdApproval(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
	reader := teamTestApproval{owner.Acquisition, TeamPlanApproval{}, false}
	root := taskQuestionTestRoot(t, owner.Acquisition, "question-create", time.Now())
	before := reservationLedgerBytes(t, store)
	state, err := store.RecordTaskClarification(ctx, reader, owner.Acquisition, root)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(reservationLedgerBytes(t, store), []byte{'\n'}) != bytes.Count(before, []byte{'\n'})+1 {
		t.Fatal("initial questions not atomic")
	}
	if _, found, err := store.ReadTaskDraft(owner.Acquisition.Scope, root.TaskID); err != nil || found {
		t.Fatal("new profile impersonated legacy draft")
	}
	proposal, found, err := store.ReadTaskProposal(owner.Acquisition.Scope, root.TaskID)
	if err != nil || !found || proposal.QuestionsPending != 2 || proposal.Profile != goal.TaskQuestionProtocol {
		t.Fatal("current proposal missing")
	}
	ids, err := store.ListTaskDraftIDs(owner.Acquisition.Scope, "", 10)
	if err != nil || len(ids) != 1 || ids[0] != root.TaskID {
		t.Fatal("question Task missing from list")
	}
	for _, digest := range []string{"", state.RootFactDigest} {
		approval := TeamPlanApproval{InputsDigest: root.InputsDigest, RequestDigest: attemptTestDigest("confirm"), TaskDraftDigest: digest}
		if _, err := store.AcceptInitialTeamPlan(ctx, teamTestApproval{owner.Acquisition, approval, false}, owner.Acquisition, approval, root.Inputs); !errors.Is(err, ErrTaskQuestionsPending) {
			t.Fatalf("AF_UNIX/early approve bypass: %v", err)
		}
	}
	first, inputs := taskQuestionTestAnswer(t, state, 0, "answer-one")
	before = reservationLedgerBytes(t, store)
	receipt, replay, err := store.RecordTaskQuestionAnswer(ctx, reader, owner.Acquisition, first, inputs)
	if err != nil || replay || receipt.AcceptedRevision != 2 {
		t.Fatalf("first answer: %v", err)
	}
	if bytes.Count(reservationLedgerBytes(t, store), []byte{'\n'}) != bytes.Count(before, []byte{'\n'})+1 {
		t.Fatal("answer/preview not one fact")
	}
	state, found, err = store.ReadTaskClarification(owner.Acquisition.Scope, root.TaskID)
	if err != nil || !found || state.PreviewDigest != receipt.FactDigest || !bytes.Equal(state.Inputs, inputs) || len(state.Receipts) != 1 {
		t.Fatal("atomic preview state")
	}
	before = reservationLedgerBytes(t, store)
	if _, err := store.RequestTaskStop(ctx, reader, owner.Acquisition, root.TaskID, attemptTestDigest("stale-cancel"), 1); !errors.Is(err, ErrTeamPlanConflict) {
		t.Fatalf("answer first did not CAS cancel: %v", err)
	}
	oldReceipt, replay, err := store.RecordTaskQuestionAnswer(ctx, reader, owner.Acquisition, first, nil)
	if err != nil || !replay || oldReceipt != receipt || !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("exact replay did not precede current revision")
	}
	second, inputs := taskQuestionTestAnswer(t, state, 1, "answer-two")
	if _, _, err := store.RecordTaskQuestionAnswer(ctx, reader, owner.Acquisition, second, inputs); err != nil {
		t.Fatal(err)
	}
	proposal, _, err = store.ReadTaskProposal(owner.Acquisition.Scope, root.TaskID)
	if err != nil || proposal.QuestionsPending != 0 || proposal.Revision != 3 || proposal.ConfirmBefore != root.ConfirmBefore || proposal.RootFactDigest != state.RootFactDigest {
		t.Fatal("ready preview or original deadline lost")
	}
	approval := TeamPlanApproval{InputsDigest: proposal.InputsDigest, RequestDigest: attemptTestDigest("final-confirm"), TaskDraftDigest: proposal.FactDigest}
	bad := approval
	bad.TaskDraftDigest = state.RootFactDigest
	if _, err := store.AcceptInitialTeamPlan(ctx, teamTestApproval{owner.Acquisition, bad, false}, owner.Acquisition, bad, proposal.Inputs); !errors.Is(err, ErrTeamPlanConflict) {
		t.Fatal("old preview approved")
	}
	plan, err := store.AcceptInitialTeamPlan(ctx, teamTestApproval{owner.Acquisition, approval, false}, owner.Acquisition, approval, proposal.Inputs)
	if err != nil || len(plan.Materializations) != 3 {
		t.Fatalf("original accepted plan: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	owner, _ = supervisorTestAcquireOwner(t, store, attemptTestIdentity())
	reader = teamTestApproval{owner.Acquisition, TeamPlanApproval{}, false}
	cold, _, err := store.ReadTaskProposal(owner.Acquisition.Scope, root.TaskID)
	if err != nil || !bytes.Equal(teamTestBytes(t, proposal), teamTestBytes(t, cold)) {
		t.Fatalf("cold exact preview: %v", err)
	}
	before = reservationLedgerBytes(t, store)
	if _, replayed, err := store.RecordTaskQuestionAnswer(ctx, reader, owner.Acquisition, first, nil); err != nil || !replayed {
		t.Fatalf("cold postapprove answer replay: %v", err)
	}
	if _, err := store.AcceptInitialTeamPlan(ctx, teamTestApproval{owner.Acquisition, approval, false}, owner.Acquisition, approval, proposal.Inputs); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("cold receipt replay appended")
	}
	_, _, revision, err := store.ReadTaskCancellation(owner.Acquisition.Scope, root.TaskID)
	if err != nil || revision != 4 {
		t.Fatalf("control revision %d %v", revision, err)
	}
}

func TestTaskQuestionRejectedCommandsHaveNoAppend(t *testing.T) {
	ctx := context.Background()
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
	reader := teamTestApproval{owner.Acquisition, TeamPlanApproval{}, false}
	state, err := store.RecordTaskClarification(ctx, reader, owner.Acquisition, taskQuestionTestRoot(t, owner.Acquisition, "negatives", time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	request, inputs := taskQuestionTestAnswer(t, state, 0, "a")
	before := reservationLedgerBytes(t, store)
	for _, tc := range []struct {
		name   string
		change func(*TaskQuestionAnswer)
	}{
		{"old-revision", func(v *TaskQuestionAnswer) { v.ExpectedRevision++ }},
		{"old-preview", func(v *TaskQuestionAnswer) { v.PreviewDigest = attemptTestDigest("old") }},
		{"question-revision", func(v *TaskQuestionAnswer) { v.QuestionRevision++ }},
		{"foreign-question", func(v *TaskQuestionAnswer) { v.QuestionID = "foreign" }},
		{"foreign-task", func(v *TaskQuestionAnswer) { v.TaskID = "foreign" }},
		{"large-answer", func(v *TaskQuestionAnswer) { v.Answer = strings.Repeat("a", 4097) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := request
			tc.change(&bad)
			if _, _, err := store.RecordTaskQuestionAnswer(ctx, reader, owner.Acquisition, bad, inputs); err == nil {
				t.Fatal("invalid answer accepted")
			}
			if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
				t.Fatal("rejection appended")
			}
		})
	}
	for _, needle := range []string{"original context", "oracle"} {
		bad := bytes.Replace(inputs, []byte(needle), []byte("forged"), 1)
		if bytes.Equal(bad, inputs) {
			t.Fatal("negative did not edit")
		}
		if _, _, err := store.RecordTaskQuestionAnswer(ctx, reader, owner.Acquisition, request, bad); err == nil {
			t.Fatal("frozen boundary changed")
		}
	}
	if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("boundary rejection appended")
	}
	receipt, _, err := store.RecordTaskQuestionAnswer(ctx, reader, owner.Acquisition, request, inputs)
	if err != nil {
		t.Fatal(err)
	}
	before = reservationLedgerBytes(t, store)
	changed := request
	changed.Answer = "different"
	if _, _, err := store.RecordTaskQuestionAnswer(ctx, reader, owner.Acquisition, changed, nil); !errors.Is(err, ErrTeamPlanConflict) {
		t.Fatal("key changed answer")
	}
	changed = request
	changed.RequestKeyDigest = attemptTestDigest("new-key")
	changed.ExpectedRevision = 2
	changed.PreviewDigest = receipt.FactDigest
	if _, _, err := store.RecordTaskQuestionAnswer(ctx, reader, owner.Acquisition, changed, inputs); !errors.Is(err, ErrTeamPlanConflict) {
		t.Fatal("consumed question reopened")
	}
	if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("changed replay appended")
	}
}

func TestTaskQuestionCancelAndExpiryFence(t *testing.T) {
	for _, mode := range []string{"cancel", "expired"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store, err := OpenResultIngressStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
			reader := teamTestApproval{owner.Acquisition, TeamPlanApproval{}, false}
			created := time.Now()
			if mode == "expired" {
				created = created.Add(-time.Hour)
			}
			state, err := store.RecordTaskClarification(ctx, reader, owner.Acquisition, taskQuestionTestRoot(t, owner.Acquisition, mode, created))
			if err != nil {
				t.Fatal(err)
			}
			request, inputs := taskQuestionTestAnswer(t, state, 0, "answer")
			want := ErrTaskDraftExpired
			if mode == "cancel" {
				if _, err := store.RequestTaskStop(ctx, reader, owner.Acquisition, state.Root.TaskID, attemptTestDigest("cancel"), 1); err != nil {
					t.Fatal(err)
				}
				want = ErrTaskStopped
			}
			before := reservationLedgerBytes(t, store)
			_, _, err = store.RecordTaskQuestionAnswer(ctx, reader, owner.Acquisition, request, inputs)
			// Both stale revision and stop are deterministic rejection; neither is
			// allowed to add an answer after stop wins.
			if !errors.Is(err, want) && !(mode == "cancel" && errors.Is(err, ErrTeamPlanConflict)) {
				t.Fatalf("fence: %v", err)
			}
			if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
				t.Fatal("fence appended")
			}
		})
	}
}
