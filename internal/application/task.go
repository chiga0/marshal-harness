package application

import (
	"context"

	"github.com/chiga0/marshal-harness/internal/goal"
)

// TaskApplicationPort belongs to the same resident application. IDs select
// current Goal facts; none of these inputs carry authority or host paths.
type TaskApplicationPort interface {
	CreateTask(context.Context, CreateTaskRequest) (TaskProjection, error)
	ReadTask(context.Context, string) (TaskProjection, error)
	ListTasks(context.Context) ([]TaskProjection, error)
	ApproveTask(context.Context, ApproveTaskRequest) (TaskProjection, error)
	CancelTask(context.Context, CancelTaskRequest) (TaskProjection, error)
	ReadTaskArtifact(context.Context, string) (TaskArtifact, error)
}

type CreateTaskRequest struct {
	IdempotencyKey string              `json:"-"`
	Submission     goal.TaskSubmission `json:"submission"`
}
type ApproveTaskRequest struct {
	TaskID           string `json:"-"`
	IdempotencyKey   string `json:"-"`
	ExpectedRevision int64  `json:"expectedRevision"`
	PreviewDigest    string `json:"previewDigest"`
}
type CancelTaskRequest struct {
	TaskID         string `json:"-"`
	IdempotencyKey string `json:"-"`
}
type TaskWorkerProjection struct {
	ID     string         `json:"id"`
	NodeID string         `json:"nodeId"`
	Role   string         `json:"role"`
	Status string         `json:"status"`
	Run    *RunProjection `json:"run,omitempty"`
}
type TaskEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}
type TaskProjection struct {
	ID                    string                         `json:"id"`
	Status                string                         `json:"status"`
	Reason                string                         `json:"reason,omitempty"`
	Revision              int64                          `json:"revision"`
	PreviewDigest         string                         `json:"previewDigest"`
	Request               goal.TaskSubmission            `json:"request"`
	CreatedAt             string                         `json:"createdAt"`
	ConfirmBefore         string                         `json:"confirmBefore"`
	AllowedActions        []string                       `json:"allowedActions"`
	Workers               []TaskWorkerProjection         `json:"workers"`
	Edges                 []TaskEdge                     `json:"edges"`
	Approval              *InitialTeamApprovalProjection `json:"approval,omitempty"`
	Outcome               *InitialTeamOutcomeProjection  `json:"outcome,omitempty"`
	Delivery              *goal.TaskDelivery             `json:"delivery,omitempty"`
	CancellationRequested bool                           `json:"cancellationRequested"`
	UsageSource           string                         `json:"usageSource"`
}
type TaskArtifact struct {
	Manifest goal.TaskDelivery `json:"manifest"`
	Content  []byte            `json:"-"`
}
