package application

import (
	"context"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
)

// TaskTemplatePort is trusted, process-local template composition. It only
// produces input bytes and read-only previews; approval, persistence and Run
// creation remain with the consuming application. InspectTask must describe
// the supplied frozen inputs, independently of the currently installed draft
// template, so disabling new submissions cannot erase historical queries.
type TaskTemplatePort interface {
	Digest() string
	RenderTask(string, goal.TaskSubmission) ([]byte, error)
	InspectTask([]byte) ([]TaskPreviewNode, error)
}

// TaskDraftPort belongs to the same resident application. IDs select
// current Goal facts; none of these inputs carry authority or host paths.
type TaskDraftPort interface {
	CreateTask(context.Context, CreateTaskRequest) (TaskProjection, error)
	ReadTask(context.Context, string) (TaskProjection, error)
	ListTasks(context.Context, TaskListRequest) (TaskPage, error)
	ApproveTask(context.Context, ApproveTaskRequest) (TaskProjection, error)
}

// These later capabilities are intentionally separate from the implemented
// draft/confirmation Port. An adapter must not advertise dummy cancellation
// or artifact handlers merely to satisfy a larger interface.
type TaskApplicationPort interface {
	TaskDraftPort
	CancelTask(context.Context, CancelTaskRequest) (TaskProjection, error)
	ReadTaskArtifact(context.Context, string) (TaskArtifact, error)
}

type TaskListRequest struct {
	After string `json:"after"`
	Limit int    `json:"limit"`
}
type TaskPage struct {
	Items      []TaskProjection `json:"items"`
	NextCursor string           `json:"nextCursor,omitempty"`
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

type TaskPreviewNode struct {
	ID           string          `json:"id"`
	Role         string          `json:"role"`
	Work         domain.TaskWork `json:"work"`
	Paths        []string        `json:"paths"`
	OracleDigest string          `json:"oracleDigest"`
}

type TaskPreview struct {
	TemplateDigest string            `json:"templateDigest"`
	InputsDigest   string            `json:"inputsDigest"`
	Publication    string            `json:"publication"`
	Limits         goal.Guardrails   `json:"limits"`
	Nodes          []TaskPreviewNode `json:"nodes"`
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
	Preview               TaskPreview                    `json:"preview"`
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
