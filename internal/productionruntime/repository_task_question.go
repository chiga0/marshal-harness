package productionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

var _ application.TaskQuestionPort = (*RepositorySession)(nil)

func (s *RepositorySession) validTaskSubmission(v goal.TaskSubmission) bool {
	// Only shape is checked before creation replay; a removed template cannot
	// erase an existing Task. Fresh submissions still require exact installed DI.
	if len(v.Template) == 0 || len(v.Template) > 128 || !utf8.ValidString(v.Template) || strings.ContainsAny(v.Template, " \t\r\n\x00") {
		return false
	}
	v.Template = goal.TaskTemplateOrderQuote
	return v.Validate() == nil && utf8.ValidString(v.Intent) && utf8.ValidString(v.Context.Text)
}

func questionTemplateCopy(port application.TaskQuestionTemplatePort) (goal.TaskQuestionTemplate, error) {
	d := port.QuestionTemplate()
	raw, err := json.Marshal(d)
	if err != nil || json.Unmarshal(raw, &d) != nil || d.Validate() != nil || d.TemplateDigest != port.Digest() {
		return d, application.NewError("task-question", application.ReasonCompositionIncomplete)
	}
	return d, nil
}

func sameQuestionTemplate(a, b goal.TaskQuestionTemplate) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func (s *RepositorySession) createTaskClarification(ctx context.Context, reader repositoryApprovedTeamVerifier, id, key, requestDigest string, submission goal.TaskSubmission) (application.TaskProjection, error) {
	fail := func() (application.TaskProjection, error) {
		return application.TaskProjection{}, application.NewError("create-task", application.ReasonInvalidRequest)
	}
	port, ok := s.taskTemplate.(application.TaskQuestionTemplatePort)
	if !ok || s.teamInputPreflight == nil {
		return fail()
	}
	template, err := questionTemplateCopy(port)
	if err != nil || template.ID != submission.Template || template.ID == goal.TaskTemplateOrderQuote {
		return fail()
	}
	values, err := port.SuppliedTaskInputs(submission)
	if err != nil {
		return fail()
	}
	missing, err := goal.MissingTaskSlots(template, values)
	// No production non-order-quote template is registered by this slice.
	// A complete-input profile must be explicitly implemented, never coerced
	// into an order-quote draft or made to ask a redundant question.
	if err != nil || len(missing) == 0 {
		return fail()
	}
	for _, value := range values {
		index := slices.IndexFunc(template.Slots, func(s goal.TaskInputSlot) bool { return s.ID == value.SlotID })
		slot := template.Slots[index]
		slot.NodeIDs = slices.Clone(slot.NodeIDs)
		if port.ValidateTaskInput(slot, value.Value) != nil {
			return fail()
		}
	}
	raw, err := port.RenderTask(id, submission)
	if err != nil {
		return fail()
	}
	inputs, err := canonical.JSON(raw)
	if err != nil || !bytes.Equal(inputs, raw) {
		return fail()
	}
	check := bytes.Clone(inputs)
	if s.teamInputPreflight(check) != nil || !bytes.Equal(check, inputs) {
		return fail()
	}
	after, err := questionTemplateCopy(port)
	if err != nil || !sameQuestionTemplate(template, after) {
		return fail()
	}
	now := time.Now().UTC()
	root := goal.TaskClarificationRoot{TaskID: id, Request: submission, RequestKeyDigest: key, RequestDigest: requestDigest, CreatedAt: now.Format(time.RFC3339Nano), ConfirmBefore: now.Add(30 * time.Minute).Format(time.RFC3339Nano), Template: template, InitialValues: slices.Clone(values), Inputs: inputs, InputsDigest: canonical.DigestBytes(inputs), Questions: []goal.TaskQuestion{}}
	for _, slot := range missing {
		root.Questions = append(root.Questions, goal.TaskQuestion{ID: goal.QuestionID(id, root.InputsDigest, slot.ID), SlotID: slot.ID, Revision: 1, Prompt: slot.Prompt})
	}
	if root.Validate() != nil {
		return fail()
	}
	if _, err := s.ingress.RecordTaskClarification(ctx, reader, s.acquisition, root); err != nil {
		return application.TaskProjection{}, taskError(err)
	}
	return s.readTaskBorrowed(ctx, id)
}

func (s *RepositorySession) inspectTaskProposal(draft resultingress.TaskProposal) ([]application.TaskPreviewNode, error) {
	if draft.Profile != goal.TaskQuestionProtocol {
		if s.taskTemplate == nil {
			return nil, application.NewError("read-task", application.ReasonCompositionIncomplete)
		}
		return s.taskTemplate.InspectTask(bytes.Clone(draft.Inputs))
	}
	// Read-only historical projection does not invoke a mutable planner or
	// require the old business validator to remain installed.
	var inputs goal.TeamInputs
	if json.Unmarshal(draft.Inputs, &inputs) != nil {
		return nil, application.NewError("read-task", application.ReasonAuthorityConflict)
	}
	nodes := []application.TaskPreviewNode{}
	for _, input := range inputs.Nodes {
		var task domain.TaskSpec
		if json.Unmarshal(input.Task, &task) != nil {
			return nil, application.NewError("read-task", application.ReasonAuthorityConflict)
		}
		raw, _ := json.Marshal(task.Acceptance)
		digest, err := canonical.DigestJSON(raw)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, application.TaskPreviewNode{ID: input.NodeID, Role: input.Role, Work: task.Work, Paths: task.Scope.AllowPaths, OracleDigest: digest})
	}
	return nodes, nil
}

func (s *RepositorySession) ReadTaskQuestions(ctx context.Context, id string) (result application.TaskQuestions, err error) {
	if ctx == nil || domain.ValidateID(id) != nil {
		return result, application.NewError("task-questions", application.ReasonInvalidRequest)
	}
	borrow, err := s.borrow()
	if err != nil {
		return result, err
	}
	defer borrow.Close()
	reader := repositoryApprovedTeamVerifier{session: s}
	err = reader.WithCurrentApprovedTeam(ctx, s.acquisition, resultingress.TeamPlanApproval{}, func() error {
		proposal, found, e := s.ingress.ReadTaskProposal(s.acquisition.Scope, id)
		if e != nil {
			return e
		}
		if !found {
			return application.NewError("task-questions", application.ReasonTaskNotFound)
		}
		_, _, revision, e := s.ingress.ReadTaskCancellation(s.acquisition.Scope, id)
		if e != nil {
			return e
		}
		result = application.TaskQuestions{TaskID: id, Revision: revision, PreviewDigest: proposal.FactDigest, ConfirmBefore: proposal.ConfirmBefore, Questions: []application.TaskQuestionView{}}
		state, found, e := s.ingress.ReadTaskClarification(s.acquisition.Scope, id)
		if e != nil || !found {
			return e
		}
		result.SubjectDigest = state.Root.InputsDigest
		for _, q := range state.Root.Questions {
			view := application.TaskQuestionView{TaskQuestion: q, Status: "pending"}
			for _, r := range state.Receipts {
				if r.Request.QuestionID == q.ID {
					answer := r.Request.Answer
					view.Status = "answered"
					view.Answer = &answer
				}
			}
			result.Questions = append(result.Questions, view)
		}
		return nil
	})
	return
}

func (s *RepositorySession) AnswerTaskQuestion(ctx context.Context, request application.AnswerTaskQuestionRequest) (result application.TaskQuestionAnswerResult, err error) {
	key, e := taskRequestKey(request.IdempotencyKey)
	if ctx == nil || e != nil || domain.ValidateID(request.TaskID) != nil || domain.ValidateID(request.QuestionID) != nil || request.ExpectedRevision < 1 || request.QuestionRevision != 1 || goal.ValidateTaskQuestionDigest(request.PreviewDigest) != nil || (goal.TaskSlotValue{SlotID: "answer", Value: request.Answer}).Validate() != nil {
		return result, application.NewError("answer-task-question", application.ReasonInvalidRequest)
	}
	borrow, err := s.borrow()
	if err != nil {
		return result, err
	}
	defer borrow.Close()
	q := resultingress.TaskQuestionAnswer{TaskID: request.TaskID, QuestionID: request.QuestionID, RequestKeyDigest: key, ExpectedRevision: request.ExpectedRevision, PreviewDigest: request.PreviewDigest, QuestionRevision: request.QuestionRevision, Answer: request.Answer}
	reader := repositoryApprovedTeamVerifier{session: s}
	var state resultingress.TaskClarificationState
	var replay bool
	err = reader.WithCurrentApprovedTeam(ctx, s.acquisition, resultingress.TeamPlanApproval{}, func() error {
		var found bool
		var e error
		state, found, e = s.ingress.ReadTaskClarification(s.acquisition.Scope, request.TaskID)
		if e != nil {
			return e
		}
		if !found {
			return resultingress.ErrTaskQuestionNotFound
		}
		for _, r := range state.Receipts {
			if r.Request.RequestKeyDigest == key {
				if r.Request != q {
					return resultingress.ErrTeamPlanConflict
				}
				replay = true
				return nil
			}
		}
		stop, _, revision, e := s.ingress.ReadTaskCancellation(s.acquisition.Scope, request.TaskID)
		if e != nil {
			return e
		}
		if stop.FactDigest != "" {
			return resultingress.ErrTaskStopped
		}
		if _, approved, e := s.ingress.ReadTeamPlan(s.acquisition.Scope, request.TaskID); e != nil {
			return e
		} else if approved {
			return resultingress.ErrTeamPlanConflict
		}
		if revision != request.ExpectedRevision || state.PreviewDigest != request.PreviewDigest {
			return resultingress.ErrTeamPlanConflict
		}
		deadline, _ := time.Parse(time.RFC3339Nano, state.Root.ConfirmBefore)
		if !time.Now().Before(deadline) {
			return resultingress.ErrTaskDraftExpired
		}
		return nil
	})
	if err != nil {
		return result, taskError(err)
	}
	var inputs []byte
	if !replay {
		port, ok := s.taskTemplate.(application.TaskQuestionTemplatePort)
		if !ok {
			return result, application.NewError("answer-task-question", application.ReasonCompositionIncomplete)
		}
		descriptor, e := questionTemplateCopy(port)
		if e != nil || !sameQuestionTemplate(descriptor, state.Root.Template) {
			return result, application.NewError("answer-task-question", application.ReasonCompositionIncomplete)
		}
		n := slices.IndexFunc(state.Root.Questions, func(q goal.TaskQuestion) bool { return q.ID == request.QuestionID })
		if n < 0 {
			return result, taskError(resultingress.ErrTaskQuestionNotFound)
		}
		if slices.ContainsFunc(state.Receipts, func(r resultingress.TaskQuestionReceipt) bool { return r.Request.QuestionID == request.QuestionID }) {
			return result, taskError(resultingress.ErrTeamPlanConflict)
		}
		slotIndex := slices.IndexFunc(descriptor.Slots, func(slot goal.TaskInputSlot) bool { return slot.ID == state.Root.Questions[n].SlotID })
		slot := descriptor.Slots[slotIndex]
		slot.NodeIDs = slices.Clone(slot.NodeIDs)
		if port.ValidateTaskInput(slot, request.Answer) != nil {
			return result, application.NewError("answer-task-question", application.ReasonInvalidRequest)
		}
		values := []goal.TaskSlotValue{}
		for _, r := range state.Receipts {
			index := slices.IndexFunc(state.Root.Questions, func(q goal.TaskQuestion) bool { return q.ID == r.Request.QuestionID })
			values = append(values, goal.TaskSlotValue{SlotID: state.Root.Questions[index].SlotID, Value: r.Request.Answer})
		}
		values = append(values, goal.TaskSlotValue{SlotID: slot.ID, Value: request.Answer})
		inputs, e = goal.RenderTaskQuestionInputs(state.Root, values)
		if e != nil {
			return result, application.NewError("answer-task-question", application.ReasonInvalidRequest)
		}
		if s.teamInputPreflight == nil {
			return result, application.NewError("answer-task-question", application.ReasonCompositionIncomplete)
		}
		check := bytes.Clone(inputs)
		if s.teamInputPreflight(check) != nil || !bytes.Equal(check, inputs) {
			return result, application.NewError("answer-task-question", application.ReasonInvalidRequest)
		}
		after, e := questionTemplateCopy(port)
		if e != nil || !sameQuestionTemplate(after, descriptor) {
			return result, application.NewError("answer-task-question", application.ReasonCompositionIncomplete)
		}
	}
	receipt, replayed, err := s.ingress.RecordTaskQuestionAnswer(ctx, reader, s.acquisition, q, inputs)
	if err != nil {
		return result, taskError(err)
	}
	projection, err := s.readTaskBorrowed(ctx, request.TaskID)
	if err != nil {
		return result, err
	}
	return application.TaskQuestionAnswerResult{TaskProjection: projection, QuestionID: request.QuestionID, AnswerFactDigest: receipt.FactDigest, AcceptedPreviewDigest: receipt.FactDigest, AcceptedRevision: receipt.AcceptedRevision, Replayed: replayed}, nil
}
