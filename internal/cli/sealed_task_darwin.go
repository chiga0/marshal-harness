//go:build darwin && arm64

package cli

import (
	"context"
	"github.com/chiga0/marshal-harness/internal/application"
)

var _ application.TaskDraftPort = (*sealedRepositoryApplication)(nil)
var _ application.TaskArtifactPort = (*sealedRepositoryApplication)(nil)
var _ application.TaskCancelPort = (*sealedRepositoryApplication)(nil)
var _ application.TaskQuestionPort = (*sealedRepositoryApplication)(nil)

func (a *sealedRepositoryApplication) ReadTaskQuestions(ctx context.Context, id string) (application.TaskQuestions, error) {
	a.statusMu.RLock()
	defer a.statusMu.RUnlock()
	if a.closed {
		return application.TaskQuestions{}, application.NewError("task-questions", application.ReasonOwnerUnavailable)
	}
	return a.session.ReadTaskQuestions(ctx, id)
}

func (a *sealedRepositoryApplication) AnswerTaskQuestion(ctx context.Context, request application.AnswerTaskQuestionRequest) (application.TaskQuestionAnswerResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.statusMu.RLock()
	defer a.statusMu.RUnlock()
	if a.closed {
		return application.TaskQuestionAnswerResult{}, application.NewError("answer-task-question", application.ReasonOwnerUnavailable)
	}
	return a.session.AnswerTaskQuestion(ctx, request)
}

func (a *sealedRepositoryApplication) CancelTask(ctx context.Context, request application.CancelTaskRequest) (application.TaskProjection, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.statusMu.RLock()
	defer a.statusMu.RUnlock()
	if a.closed {
		return application.TaskProjection{}, application.NewError("cancel-task", application.ReasonOwnerUnavailable)
	}
	return a.session.CancelTask(ctx, request)
}

func (a *sealedRepositoryApplication) ReadTaskArtifact(ctx context.Context, id string) (application.TaskArtifact, error) {
	a.statusMu.RLock()
	defer a.statusMu.RUnlock()
	if a.closed {
		return application.TaskArtifact{}, application.NewError("task-artifact", application.ReasonOwnerUnavailable)
	}
	return a.session.ReadTaskArtifact(ctx, id)
}

func (a *sealedRepositoryApplication) CreateTask(ctx context.Context, request application.CreateTaskRequest) (application.TaskProjection, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.statusMu.RLock()
	defer a.statusMu.RUnlock()
	if a.closed {
		return application.TaskProjection{}, application.NewError("create-task", application.ReasonOwnerUnavailable)
	}
	return a.session.CreateTask(ctx, request)
}

func (a *sealedRepositoryApplication) ApproveTask(ctx context.Context, request application.ApproveTaskRequest) (application.TaskProjection, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.statusMu.RLock()
	defer a.statusMu.RUnlock()
	if a.closed {
		return application.TaskProjection{}, application.NewError("approve-task", application.ReasonOwnerUnavailable)
	}
	return a.session.ApproveTask(ctx, request)
}

func (a *sealedRepositoryApplication) ReadTask(ctx context.Context, id string) (application.TaskProjection, error) {
	a.statusMu.RLock()
	defer a.statusMu.RUnlock()
	if a.closed {
		return application.TaskProjection{}, application.NewError("read-task", application.ReasonOwnerUnavailable)
	}
	return a.session.ReadTask(ctx, id)
}

func (a *sealedRepositoryApplication) ListTasks(ctx context.Context, request application.TaskListRequest) (application.TaskPage, error) {
	a.statusMu.RLock()
	defer a.statusMu.RUnlock()
	if a.closed {
		return application.TaskPage{}, application.NewError("list-tasks", application.ReasonOwnerUnavailable)
	}
	return a.session.ListTasks(ctx, request)
}
