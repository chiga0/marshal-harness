//go:build darwin && arm64

package productionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/goal"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

// This explicit, test-only DI template exercises complete real Task/Policy
// schemas, owner/session/RB1 and HTTP. It is not registered in production,
// does not change order-quote, and does not claim a zero-Git business template.
type repositoryQuestionTemplate struct {
	application.TaskTemplatePort
	descriptor goal.TaskQuestionTemplate
}

func (p *repositoryQuestionTemplate) QuestionTemplate() goal.TaskQuestionTemplate {
	return p.descriptor
}
func (p *repositoryQuestionTemplate) SuppliedTaskInputs(goal.TaskSubmission) ([]goal.TaskSlotValue, error) {
	return []goal.TaskSlotValue{}, nil
}
func (p *repositoryQuestionTemplate) ValidateTaskInput(slot goal.TaskInputSlot, value string) error {
	if slot.ID == "audience" && (value == "developers" || value == "analysts") || slot.ID == "example" && value == "small-order" {
		return nil
	}
	return errors.New("invalid fixture business input")
}
func (p *repositoryQuestionTemplate) RenderTask(id string, request goal.TaskSubmission) ([]byte, error) {
	request.Template = goal.TaskTemplateOrderQuote
	return p.TaskTemplatePort.RenderTask(id, request)
}

func repositoryQuestionFixture(t *testing.T) (publicFixedDeliveryInputs, *RepositorySession, *repositoryQuestionTemplate) {
	t.Helper()
	fixture, s := taskCancelSessionFixture(t)
	port := &repositoryQuestionTemplate{TaskTemplatePort: s.taskTemplate}
	port.descriptor = goal.TaskQuestionTemplate{ID: "test-required-context/v1", TemplateDigest: port.Digest(), ProducerDigest: canonical.DigestBytes([]byte("test-context-producer/v1")), ValidatorDigest: canonical.DigestBytes([]byte("test-context-validator/v1")), RendererDigest: goal.TaskQuestionRendererDigest(), Slots: []goal.TaskInputSlot{{ID: "audience", Prompt: "测试交付说明面向哪一类读者？", NodeIDs: []string{"service", "integration"}}, {ID: "example", Prompt: "测试交付说明使用哪个样例？", NodeIDs: []string{"client", "integration"}}}}
	if err := port.descriptor.Validate(); err != nil {
		t.Fatal(err)
	}
	s.taskTemplate = port
	fixture.inputs.TaskTemplate = port
	return fixture, s, port
}
func repositoryQuestionCreate(t *testing.T, s *RepositorySession, key string) application.TaskProjection {
	t.Helper()
	w := taskCancelHTTP(t, s, http.MethodPost, "/v1/tasks", key, goal.TaskSubmission{Template: "test-required-context/v1", Intent: "生成测试交付说明，保留全部固定验收和权限", Context: goal.TaskContext{Text: "这是协议组合测试，不是生产业务模板"}})
	var task application.TaskProjection
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &task) != nil || task.Status != "awaiting-answer" || task.Revision != 1 || len(task.Workers) != 3 || slices.Contains(task.AllowedActions, "approve") || !slices.Contains(task.AllowedActions, "answer") {
		t.Fatalf("question create: %d %s", w.Code, w.Body.String())
	}
	return task
}
func repositoryQuestions(t *testing.T, s *RepositorySession, id string) application.TaskQuestions {
	t.Helper()
	w := taskCancelHTTP(t, s, http.MethodGet, "/v1/tasks/"+id+"/questions", "", nil)
	var result application.TaskQuestions
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatalf("questions: %d %s", w.Code, w.Body.String())
	}
	return result
}
func repositoryAnswerRequest(task application.TaskProjection, q application.TaskQuestionView, value string) application.AnswerTaskQuestionRequest {
	return application.AnswerTaskQuestionRequest{TaskID: task.ID, QuestionID: q.ID, ExpectedRevision: task.Revision, PreviewDigest: task.PreviewDigest, QuestionRevision: q.Revision, Answer: value}
}
func repositoryAnswerHTTP(t *testing.T, s *RepositorySession, key string, request application.AnswerTaskQuestionRequest) application.TaskQuestionAnswerResult {
	t.Helper()
	w := taskCancelHTTP(t, s, http.MethodPost, "/v1/tasks/"+request.TaskID+"/questions/"+request.QuestionID+"/answers", key, request)
	var result application.TaskQuestionAnswerResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.ID != request.TaskID || result.AnswerFactDigest == "" {
		t.Fatalf("answer: %d %s", w.Code, w.Body.String())
	}
	return result
}

func TestRepositoryTaskQuestionsHTTPApproveAndColdReplay(t *testing.T) {
	ctx := context.Background()
	fixture, s, _ := repositoryQuestionFixture(t)
	task := repositoryQuestionCreate(t, s, "question-http")
	questions := repositoryQuestions(t, s, task.ID)
	if len(questions.Questions) != 2 || questions.Revision != 1 || questions.PreviewDigest != task.PreviewDigest || questions.ConfirmBefore != task.ConfirmBefore || questions.SubjectDigest == "" {
		t.Fatal("question binding")
	}
	if _, found, err := s.ingress.ReadTeamPlan(s.acquisition.Scope, task.ID); err != nil || found {
		t.Fatal("questions approved authority")
	}
	if obligations, err := s.ingress.ListTeamCreationObligations(s.acquisition.Scope); err != nil || len(obligations) != 0 {
		t.Fatal("questions created Runs")
	}
	if page, err := s.ListTasks(ctx, application.TaskListRequest{Limit: 20}); err != nil || len(page.Items) != 1 || page.Items[0].ID != task.ID {
		t.Fatal("question Task lost in list")
	}
	approvePath := "/v1/tasks/" + task.ID + "/approve"
	originalApprove := application.ApproveTaskRequest{ExpectedRevision: task.Revision, PreviewDigest: task.PreviewDigest}
	if w := taskCancelHTTP(t, s, http.MethodPost, approvePath, "too-early", originalApprove); w.Code != 409 {
		t.Fatal("unanswered approved", w.Code)
	}
	first := repositoryAnswerRequest(task, questions.Questions[0], "developers")
	answered := repositoryAnswerHTTP(t, s, "question-first", first)
	if answered.Replayed || answered.Revision != 2 || answered.Status != "awaiting-answer" || answered.AcceptedRevision != 2 || answered.PreviewDigest == task.PreviewDigest || answered.ConfirmBefore != task.ConfirmBefore {
		t.Fatal("first answer projection")
	}
	if w := taskCancelHTTP(t, s, http.MethodPost, "/v1/tasks/"+task.ID+"/cancel", "stale-cancel", application.CancelTaskRequest{ExpectedRevision: 1}); w.Code != 409 {
		t.Fatal("cancel implicitly refreshed CAS", w.Code)
	}
	if w := taskCancelHTTP(t, s, http.MethodPost, approvePath, "old-preview", originalApprove); w.Code != 409 {
		t.Fatal("old preview approved", w.Code)
	}
	second := repositoryAnswerRequest(answered.TaskProjection, questions.Questions[1], "small-order")
	ready := repositoryAnswerHTTP(t, s, "question-second", second)
	if ready.Revision != 3 || ready.Status != "awaiting-confirmation" || slices.Contains(ready.AllowedActions, "answer") || !slices.Contains(ready.AllowedActions, "approve") || ready.ConfirmBefore != task.ConfirmBefore {
		t.Fatal("final preview")
	}
	latest := repositoryQuestions(t, s, task.ID)
	if latest.SubjectDigest != questions.SubjectDigest || len(latest.Questions) != 2 || latest.Questions[0].Status != "answered" || latest.Questions[1].Status != "answered" {
		t.Fatal("question subject or consumption changed")
	}
	initial, found, err := s.ingress.ReadTaskClarification(s.acquisition.Scope, task.ID)
	if err != nil || !found {
		t.Fatal(err)
	}
	values := []goal.TaskSlotValue{{SlotID: "audience", Value: "developers"}, {SlotID: "example", Value: "small-order"}}
	if goal.ValidateTaskQuestionInputs(initial.Root, values, initial.Inputs) != nil {
		t.Fatal("frozen original boundary changed")
	}
	approve := application.ApproveTaskRequest{ExpectedRevision: ready.Revision, PreviewDigest: ready.PreviewDigest}
	w := taskCancelHTTP(t, s, http.MethodPost, approvePath, "final-question-confirm", approve)
	var approved application.TaskProjection
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &approved) != nil || approved.Status != "approved" || approved.Revision != 4 || approved.Outcome != nil || approved.Delivery != nil {
		t.Fatalf("original approved chain: %d %s", w.Code, w.Body.String())
	}
	plan, found, err := s.ingress.ReadTeamPlan(s.acquisition.Scope, task.ID)
	if err != nil || !found || plan.Approval.TaskDraftDigest != ready.PreviewDigest || !bytes.Equal(plan.Inputs, initial.Inputs) || len(plan.Materializations) != 3 {
		t.Fatal("current preview not exact accepted-plan")
	}
	if _, found, err := s.ingress.ReadTaskDraft(s.acquisition.Scope, task.ID); err != nil || found {
		t.Fatal("new template acquired legacy objective eligibility")
	}
	objective, err := s.taskObjectiveUnderOwner(ctx, resultingress.TeamRunCreationState{GoalID: task.ID})
	if err != nil || objective != nil {
		t.Fatal("test template acquired automatic Decision")
	}
	if _, err := s.ReadTaskArtifact(ctx, task.ID); !application.HasReason(err, application.ReasonTaskNotFound) {
		t.Fatal("test template acquired fixed ZIP capability", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.inputs.TaskTemplate = nil
	fixture.inputs.TeamInputPreflight = nil
	cold, err := OpenRepositorySession(ctx, fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	defer cold.Close()
	replay := repositoryAnswerHTTP(t, cold, "question-first", first)
	if !replay.Replayed || replay.AcceptedRevision != 2 || replay.AnswerFactDigest != answered.AnswerFactDigest || replay.Revision != 4 || replay.Status != "approved" {
		t.Fatal("cold replay rewrote receipt or current Task")
	}
	w = taskCancelHTTP(t, cold, http.MethodPost, approvePath, "final-question-confirm", approve)
	if w.Code != 202 {
		t.Fatalf("cold exact approve: %d %s", w.Code, w.Body.String())
	}
	coldQuestions := repositoryQuestions(t, cold, task.ID)
	if !bytes.Equal(repositoryTaskJSON(t, coldQuestions), repositoryTaskJSON(t, latestWithRevision(latest, 4))) {
		t.Fatal("cold questions changed")
	}
	if current, found, err := cold.ingress.ReadTeamPlan(cold.acquisition.Scope, task.ID); err != nil || !found || current.FactDigest != plan.FactDigest {
		t.Fatal("cold replay appended plan")
	}
}
func latestWithRevision(q application.TaskQuestions, revision int64) application.TaskQuestions {
	q.Revision = revision
	return q
}

func TestRepositoryTaskQuestionsCancelReplayAndValidatorFence(t *testing.T) {
	ctx := context.Background()
	_, s, port := repositoryQuestionFixture(t)
	task := repositoryQuestionCreate(t, s, "question-cancel")
	questions := repositoryQuestions(t, s, task.ID)
	request := repositoryAnswerRequest(task, questions.Questions[0], "developers")
	initial, _, _ := s.ingress.ReadTaskClarification(s.acquisition.Scope, task.ID)
	for _, mode := range []string{"business-value", "validator-drift", "preflight-failure"} {
		t.Run(mode, func(t *testing.T) {
			originalDescriptor := port.descriptor
			preflight := s.teamInputPreflight
			r := request
			switch mode {
			case "business-value":
				r.Answer = "undeclared-audience"
			case "validator-drift":
				port.descriptor.ValidatorDigest = canonical.DigestBytes([]byte("replacement"))
			case "preflight-failure":
				s.teamInputPreflight = func([]byte) error { return errors.New("fixture denied") }
			}
			w := taskCancelHTTP(t, s, http.MethodPost, "/v1/tasks/"+task.ID+"/questions/"+r.QuestionID+"/answers", "bad-"+mode, r)
			port.descriptor = originalDescriptor
			s.teamInputPreflight = preflight
			if w.Code < 400 {
				t.Fatal("failed business verifier accepted")
			}
			state, _, err := s.ingress.ReadTaskClarification(s.acquisition.Scope, task.ID)
			if err != nil || state.PreviewDigest != initial.PreviewDigest || len(state.Receipts) != 0 {
				t.Fatal("failed answer advanced")
			}
		})
	}
	first := repositoryAnswerHTTP(t, s, "valid-answer", request)
	if w := taskCancelHTTP(t, s, http.MethodPost, "/v1/tasks/"+task.ID+"/cancel", "cancel-now", application.CancelTaskRequest{ExpectedRevision: first.Revision}); w.Code != 202 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	if err := s.FinishTaskCancellation(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	stopped, err := s.ReadTask(ctx, task.ID)
	if err != nil || stopped.Status != "cancelled" {
		t.Fatal("real preapproval cancellation", stopped.Status, err)
	}
	second := repositoryAnswerRequest(first.TaskProjection, questions.Questions[1], "small-order")
	w := taskCancelHTTP(t, s, http.MethodPost, "/v1/tasks/"+task.ID+"/questions/"+second.QuestionID+"/answers", "after-cancel", second)
	if w.Code != 409 {
		t.Fatalf("stop fence: %d %s", w.Code, w.Body.String())
	}
	s.taskTemplate = nil
	s.teamInputPreflight = nil
	replay := repositoryAnswerHTTP(t, s, "valid-answer", request)
	if !replay.Replayed || replay.Status != "cancelled" || replay.AnswerFactDigest != first.AnswerFactDigest || replay.AcceptedRevision != first.AcceptedRevision || replay.Revision != stopped.Revision {
		t.Fatal("replay resurrected stopped task")
	}
	if _, found, err := s.ingress.ReadTeamPlan(s.acquisition.Scope, task.ID); err != nil || found {
		t.Fatal("stop produced plan")
	}
}

func TestRepositoryTaskQuestionsOrderQuoteRemainsZeroQuestion(t *testing.T) {
	_, s := taskCancelSessionFixture(t)
	task := taskCancelCreate(t, s)
	questions := repositoryQuestions(t, s, task.ID)
	if len(questions.Questions) != 0 || questions.Revision != 1 || questions.PreviewDigest != task.PreviewDigest {
		t.Fatal("artificial order-quote question")
	}
	request := application.AnswerTaskQuestionRequest{ExpectedRevision: 1, PreviewDigest: task.PreviewDigest, QuestionRevision: 1, Answer: "irrelevant"}
	if w := taskCancelHTTP(t, s, http.MethodPost, "/v1/tasks/"+task.ID+"/questions/missing/answers", "not-a-question", request); w.Code != 404 {
		t.Fatal("legacy answer invented a question", w.Code)
	}
	approved := taskCancelApprove(t, s, task)
	if approved.Status != "approved" || approved.Revision != 2 {
		t.Fatal("original single confirmation changed")
	}
}
