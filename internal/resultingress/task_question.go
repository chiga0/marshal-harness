package resultingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
)

const taskQuestionCreatedFact = "task-clarification-created"
const taskQuestionAnsweredFact = "task-question-answered"

var ErrTaskQuestionNotFound = errors.New("resultingress: task question not found")
var ErrTaskQuestionsPending = errors.New("resultingress: task questions pending")

// TaskProposal is a current, read-only view. Its embedded fields reuse the old
// projection shape, not the immutable TaskDraft/v1 serialization or parser.
type TaskProposal struct {
	goal.TaskDraft
	Profile          string
	RootFactDigest   string
	QuestionsPending int
}

type TaskQuestionAnswer struct {
	TaskID           string `json:"taskId"`
	QuestionID       string `json:"questionId"`
	RequestKeyDigest string `json:"requestKeyDigest"`
	ExpectedRevision int64  `json:"expectedRevision"`
	PreviewDigest    string `json:"previewDigest"`
	QuestionRevision int64  `json:"questionRevision"`
	Answer           string `json:"answer"`
}

type TaskQuestionReceipt struct {
	Request          TaskQuestionAnswer `json:"request"`
	AcceptedRevision int64              `json:"acceptedRevision"`
	AnsweredAt       string             `json:"answeredAt"`
	FactDigest       string             `json:"factDigest"`
}

type TaskClarificationState struct {
	Scope           ControlOwnerScope          `json:"scope"`
	Root            goal.TaskClarificationRoot `json:"root"`
	RootFactDigest  string                     `json:"rootFactDigest"`
	PreviewRevision int64                      `json:"previewRevision"`
	PreviewDigest   string                     `json:"previewDigest"`
	Inputs          json.RawMessage            `json:"inputs"`
	Receipts        []TaskQuestionReceipt      `json:"receipts"`
}

type taskQuestionFact struct {
	ProtocolRevision string                      `json:"protocolRevision"`
	FactType         string                      `json:"factType"`
	Sequence         int64                       `json:"sequence"`
	Scope            ControlOwnerScope           `json:"scope"`
	OwnerFactDigest  string                      `json:"ownerFactDigest"`
	Root             *goal.TaskClarificationRoot `json:"root,omitempty"`
	RootFactDigest   string                      `json:"rootFactDigest,omitempty"`
	Receipt          *TaskQuestionReceipt        `json:"receipt,omitempty"`
	Inputs           json.RawMessage             `json:"inputs,omitempty"`
	Digest           string                      `json:"digest"`
}

func currentTaskProposal(in *Ingress, key string) (TaskProposal, bool) {
	if d, found := in.taskDrafts[key]; found {
		return TaskProposal{TaskDraft: d.Draft, Profile: taskDraftProtocol, RootFactDigest: d.Draft.FactDigest}, true
	}
	if q, found := in.taskClarifications[key]; found {
		r := q.Root
		return TaskProposal{TaskDraft: goal.TaskDraft{GoalID: r.TaskID, Revision: q.PreviewRevision, Request: r.Request, RequestDigest: r.RequestDigest, RequestKeyDigest: r.RequestKeyDigest, TemplateDigest: r.Template.TemplateDigest, Inputs: bytes.Clone(q.Inputs), InputsDigest: canonical.DigestBytes(q.Inputs), CreatedAt: r.CreatedAt, ConfirmBefore: r.ConfirmBefore, FactDigest: q.PreviewDigest}, Profile: goal.TaskQuestionProtocol, RootFactDigest: q.RootFactDigest, QuestionsPending: len(r.Questions) - len(q.Receipts)}, true
	}
	return TaskProposal{}, false
}

func taskProposalScope(in *Ingress, key string) ControlOwnerScope {
	if d, found := in.taskDrafts[key]; found {
		return d.Scope
	}
	return in.taskClarifications[key].Scope
}

func (s *DurableStore) ReadTaskProposal(scope ControlOwnerScope, taskID string) (result TaskProposal, found bool, err error) {
	if scope.Validate() != nil || domain.ValidateID(taskID) != nil {
		return result, false, ErrTeamPlanConflict
	}
	in := newAuthorityProjection()
	err = s.transact(in, func() error { result, found = currentTaskProposal(in, teamPlanKey(scope, taskID)); return nil })
	return
}

func (s *DurableStore) ReadTaskClarification(scope ControlOwnerScope, taskID string) (result TaskClarificationState, found bool, err error) {
	if scope.Validate() != nil || domain.ValidateID(taskID) != nil {
		return result, false, ErrTeamPlanConflict
	}
	in := newAuthorityProjection()
	err = s.transact(in, func() error { result, found = in.taskClarifications[teamPlanKey(scope, taskID)]; return nil })
	return
}

func validateClarificationRoot(scope ControlOwnerScope, root goal.TaskClarificationRoot) error {
	id, err := TaskIDForRequest(scope, root.RequestKeyDigest)
	var inputs goal.TeamInputs
	if err != nil || id != root.TaskID || root.Validate() != nil || decodeTeamRecord(root.Inputs, &inputs) != nil || !inputs.Spec.AuthorityNamespaceId.Equal(scope.AuthorityNamespaceID) {
		return ErrTeamPlanConflict
	}
	return nil
}

func (s *DurableStore) RecordTaskClarification(ctx context.Context, verifier CurrentApprovedTeamVerifier, owner ControlOwnerAcquisition, root goal.TaskClarificationRoot) (result TaskClarificationState, err error) {
	if ctx == nil || verifier == nil || owner.Validate() != nil || validateClarificationRoot(owner.Scope, root) != nil {
		return result, ErrTeamPlanConflict
	}
	raw, _ := json.Marshal(root)
	if json.Unmarshal(raw, &root) != nil {
		return result, ErrTeamPlanConflict
	}
	err = withCurrentOwnerLock(ctx, teamApprovalOwnerVerifier{verifier: verifier}, owner, func() error {
		in := newAuthorityProjection()
		return s.transact(in, func() error {
			ownerKey, _ := owner.Scope.key()
			current := in.controlOwners[ownerKey]
			if current.Acquisition != owner {
				return ErrControlOwnerNotCurrent
			}
			key := teamPlanKey(owner.Scope, root.TaskID)
			if old, found := in.taskClarifications[key]; found {
				if old.Root.RequestKeyDigest != root.RequestKeyDigest || old.Root.RequestDigest != root.RequestDigest {
					return ErrTeamPlanConflict
				}
				result = old
				return nil
			}
			if _, exists := in.taskDrafts[key]; exists {
				return ErrTeamPlanConflict
			}
			if _, exists := in.teamPlans[key]; exists {
				return ErrTeamPlanConflict
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			fact := &taskQuestionFact{ProtocolRevision: goal.TaskQuestionProtocol, FactType: taskQuestionCreatedFact, Sequence: s.nextSequence, Scope: owner.Scope, OwnerFactDigest: current.FactDigest, Root: &root}
			if err := s.appendTaskQuestionFact(fact); err != nil {
				return err
			}
			s.nextSequence++
			result = TaskClarificationState{Scope: owner.Scope, Root: root, RootFactDigest: fact.Digest, PreviewRevision: 1, PreviewDigest: fact.Digest, Inputs: bytes.Clone(root.Inputs), Receipts: []TaskQuestionReceipt{}}
			return nil
		})
	})
	return
}

func (s *DurableStore) appendTaskQuestionFact(fact *taskQuestionFact) error {
	raw, err := json.Marshal(fact)
	if err != nil || len(raw)+100 > goal.MaxTeamInputsBytes+(128<<10) {
		return ErrTeamPlanConflict
	}
	return s.appendLine(fact, func() string { return fact.Digest }, func(d string) { fact.Digest = d })
}

func questionAnswerShape(q TaskQuestionAnswer) bool {
	return domain.ValidateID(q.TaskID) == nil && domain.ValidateID(q.QuestionID) == nil && requireDigest("answer key", q.RequestKeyDigest) == nil && requireDigest("preview", q.PreviewDigest) == nil && q.ExpectedRevision > 0 && q.QuestionRevision == 1 && (goal.TaskSlotValue{SlotID: "answer", Value: q.Answer}).Validate() == nil
}

func findQuestionReplay(state TaskClarificationState, request TaskQuestionAnswer) (TaskQuestionReceipt, bool, error) {
	for _, old := range state.Receipts {
		if old.Request.RequestKeyDigest == request.RequestKeyDigest {
			if old.Request != request {
				return TaskQuestionReceipt{}, false, ErrTeamPlanConflict
			}
			return old, true, nil
		}
	}
	return TaskQuestionReceipt{}, false, nil
}

func questionAnswerValues(state TaskClarificationState, request *TaskQuestionAnswer) ([]goal.TaskSlotValue, error) {
	values := []goal.TaskSlotValue{}
	requests := make([]TaskQuestionAnswer, 0, len(state.Receipts)+1)
	for _, receipt := range state.Receipts {
		requests = append(requests, receipt.Request)
	}
	if request != nil {
		requests = append(requests, *request)
	}
	for _, r := range requests {
		n := slices.IndexFunc(state.Root.Questions, func(q goal.TaskQuestion) bool { return q.ID == r.QuestionID })
		if n < 0 {
			return nil, ErrTaskQuestionNotFound
		}
		values = append(values, goal.TaskSlotValue{SlotID: state.Root.Questions[n].SlotID, Value: r.Answer})
	}
	return values, nil
}

func validateQuestionAnswer(in *Ingress, scope ControlOwnerScope, state TaskClarificationState, receipt TaskQuestionReceipt, inputs []byte) error {
	q := receipt.Request
	key := teamPlanKey(scope, q.TaskID)
	if !questionAnswerShape(q) || q.TaskID != state.Root.TaskID || receipt.AcceptedRevision != state.PreviewRevision+1 || q.PreviewDigest != state.PreviewDigest || q.ExpectedRevision != taskControlRevision(in, key) {
		return ErrTeamPlanConflict
	}
	if _, stopped := in.taskStops[key]; stopped {
		return ErrTaskStopped
	}
	if _, approved := in.teamPlans[key]; approved {
		return ErrTeamPlanConflict
	}
	if slices.ContainsFunc(state.Receipts, func(r TaskQuestionReceipt) bool {
		return r.Request.QuestionID == q.QuestionID || r.Request.RequestKeyDigest == q.RequestKeyDigest
	}) {
		return ErrTeamPlanConflict
	}
	at, e1 := time.Parse(time.RFC3339Nano, receipt.AnsweredAt)
	deadline, e2 := time.Parse(time.RFC3339Nano, state.Root.ConfirmBefore)
	created, _ := time.Parse(time.RFC3339Nano, state.Root.CreatedAt)
	if e1 != nil || e2 != nil || at.UTC().Format(time.RFC3339Nano) != receipt.AnsweredAt || at.Before(created) {
		return ErrTeamPlanConflict
	}
	if !at.Before(deadline) {
		return ErrTaskDraftExpired
	}
	values, err := questionAnswerValues(state, &q)
	if err != nil {
		return err
	}
	if goal.ValidateTaskQuestionInputs(state.Root, values, inputs) != nil {
		return ErrTeamPlanConflict
	}
	return nil
}

// The application supplies the frozen business-validator result; this owner
// transaction independently proves CAS, stop, root and the exact Core transform.
func (s *DurableStore) RecordTaskQuestionAnswer(ctx context.Context, verifier CurrentApprovedTeamVerifier, owner ControlOwnerAcquisition, request TaskQuestionAnswer, inputs []byte) (result TaskQuestionReceipt, replayed bool, err error) {
	if ctx == nil || verifier == nil || owner.Validate() != nil || !questionAnswerShape(request) {
		return result, false, ErrTeamPlanConflict
	}
	inputs = bytes.Clone(inputs)
	err = withCurrentOwnerLock(ctx, teamApprovalOwnerVerifier{verifier: verifier}, owner, func() error {
		in := newAuthorityProjection()
		return s.transact(in, func() error {
			ownerKey, _ := owner.Scope.key()
			current := in.controlOwners[ownerKey]
			if current.Acquisition != owner {
				return ErrControlOwnerNotCurrent
			}
			key := teamPlanKey(owner.Scope, request.TaskID)
			state, found := in.taskClarifications[key]
			if !found {
				return ErrTaskQuestionNotFound
			}
			old, found, e := findQuestionReplay(state, request)
			if e != nil {
				return e
			}
			if found {
				result, replayed = old, true
				return nil
			}
			value := TaskQuestionReceipt{Request: request, AcceptedRevision: state.PreviewRevision + 1, AnsweredAt: time.Now().UTC().Format(time.RFC3339Nano)}
			if err := validateQuestionAnswer(in, owner.Scope, state, value, inputs); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			deadline, _ := time.Parse(time.RFC3339Nano, state.Root.ConfirmBefore)
			if !time.Now().Before(deadline) {
				return ErrTaskDraftExpired
			}
			fact := &taskQuestionFact{ProtocolRevision: goal.TaskQuestionProtocol, FactType: taskQuestionAnsweredFact, Sequence: s.nextSequence, Scope: owner.Scope, OwnerFactDigest: current.FactDigest, RootFactDigest: state.RootFactDigest, Receipt: &value, Inputs: inputs}
			if err := s.appendTaskQuestionFact(fact); err != nil {
				return err
			}
			s.nextSequence++
			value.FactDigest = fact.Digest
			result = value
			return nil
		})
	})
	return
}

func applyTaskQuestionLine(line []byte, in *Ingress, sequence int64) error {
	var fact taskQuestionFact
	if len(line) > goal.MaxTeamInputsBytes+(128<<10) || decodeTeamRecord(line, &fact) != nil || fact.ProtocolRevision != goal.TaskQuestionProtocol || fact.Sequence != sequence {
		return ErrTeamPlanConflict
	}
	digest := fact.Digest
	fact.Digest = ""
	if requireDigest("question fact", digest) != nil || canonicalDigestOrEmpty(fact) != digest {
		return ErrTeamPlanConflict
	}
	ownerKey, _ := fact.Scope.key()
	current, found := in.controlOwners[ownerKey]
	if !found || current.FactDigest != fact.OwnerFactDigest {
		return ErrControlOwnerNotCurrent
	}
	if fact.FactType == taskQuestionCreatedFact {
		if fact.Root == nil || fact.Receipt != nil || fact.RootFactDigest != "" || len(fact.Inputs) != 0 || validateClarificationRoot(fact.Scope, *fact.Root) != nil {
			return ErrTeamPlanConflict
		}
		key := teamPlanKey(fact.Scope, fact.Root.TaskID)
		if _, found := currentTaskProposal(in, key); found {
			return ErrTeamPlanConflict
		}
		if _, found := in.teamPlans[key]; found {
			return ErrTeamPlanConflict
		}
		in.taskClarifications[key] = TaskClarificationState{Scope: fact.Scope, Root: *fact.Root, RootFactDigest: digest, PreviewRevision: 1, PreviewDigest: digest, Inputs: bytes.Clone(fact.Root.Inputs), Receipts: []TaskQuestionReceipt{}}
		return nil
	}
	if fact.FactType != taskQuestionAnsweredFact || fact.Root != nil || fact.Receipt == nil || fact.Receipt.FactDigest != "" {
		return ErrTeamPlanConflict
	}
	key := teamPlanKey(fact.Scope, fact.Receipt.Request.TaskID)
	state, found := in.taskClarifications[key]
	if !found || state.RootFactDigest != fact.RootFactDigest {
		return ErrTeamPlanConflict
	}
	if err := validateQuestionAnswer(in, fact.Scope, state, *fact.Receipt, fact.Inputs); err != nil {
		return err
	}
	receipt := *fact.Receipt
	receipt.FactDigest = digest
	state.Receipts = append(state.Receipts, receipt)
	state.PreviewRevision = receipt.AcceptedRevision
	state.PreviewDigest = digest
	state.Inputs = bytes.Clone(fact.Inputs)
	in.taskClarifications[key] = state
	return nil
}
