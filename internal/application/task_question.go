package application

import (
	"context"

	"github.com/chiga0/marshal-harness/internal/goal"
)

// Optional, trusted composition. No production registration is introduced by
// this interface. The renderer is the closed Core context-slots transform;
// template code can validate business values but cannot choose writable fields.
type TaskQuestionTemplatePort interface {
	TaskTemplatePort
	QuestionTemplate() goal.TaskQuestionTemplate
	SuppliedTaskInputs(goal.TaskSubmission) ([]goal.TaskSlotValue, error)
	ValidateTaskInput(goal.TaskInputSlot, string) error
}

type AnswerTaskQuestionRequest struct {
	TaskID           string `json:"-"`
	QuestionID       string `json:"-"`
	IdempotencyKey   string `json:"-"`
	ExpectedRevision int64  `json:"expectedRevision"`
	PreviewDigest    string `json:"previewDigest"`
	QuestionRevision int64  `json:"questionRevision"`
	Answer           string `json:"answer"`
}

type TaskQuestionPort interface {
	ReadTaskQuestions(context.Context, string) (TaskQuestions, error)
	AnswerTaskQuestion(context.Context, AnswerTaskQuestionRequest) (TaskQuestionAnswerResult, error)
}

type TaskQuestionView struct {
	goal.TaskQuestion
	Status string  `json:"status"`
	Answer *string `json:"answer,omitempty"`
}

type TaskQuestions struct {
	TaskID        string             `json:"taskId"`
	Revision      int64              `json:"revision"`
	PreviewDigest string             `json:"previewDigest"`
	SubjectDigest string             `json:"subjectDigest,omitempty"`
	ConfirmBefore string             `json:"confirmBefore"`
	Questions     []TaskQuestionView `json:"questions"`
}

type TaskQuestionAnswerResult struct {
	QuestionID            string `json:"questionId"`
	AnswerFactDigest      string `json:"answerFactDigest"`
	AcceptedPreviewDigest string `json:"acceptedPreviewDigest"`
	AcceptedRevision      int64  `json:"acceptedRevision"`
	Replayed              bool   `json:"replayed"`
	TaskProjection
}
