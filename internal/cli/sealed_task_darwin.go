//go:build darwin && arm64

package cli

import (
	"context"
	"github.com/chiga0/marshal-harness/internal/application"
)

var _ application.TaskDraftPort = (*sealedRepositoryApplication)(nil)

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
