package productionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

var _ application.TaskDraftPort = (*RepositorySession)(nil)

func taskRequestKey(key string) (string, error) {
	if len(key) > 128 || domain.ValidateID(key) != nil {
		return "", application.NewError("task", application.ReasonInvalidRequest)
	}
	return canonical.DigestBytes([]byte("task-http-key/v1\x00" + key)), nil
}

func taskError(err error) error {
	if errors.Is(err, resultingress.ErrTaskDraftExpired) {
		return application.NewError("task", application.ReasonTaskConfirmationExpired)
	}
	if errors.Is(err, resultingress.ErrTeamPlanConflict) {
		return application.NewError("task", application.ReasonAuthorityConflict)
	}
	return err
}

// CreateTask first resolves same-scope idempotency from RB1, before template
// regeneration/environment probing. A retry cannot refresh the frozen deadline.
func (s *RepositorySession) CreateTask(ctx context.Context, request application.CreateTaskRequest) (application.TaskProjection, error) {
	key, err := taskRequestKey(request.IdempotencyKey)
	if ctx == nil || err != nil || request.Submission.Validate() != nil {
		return application.TaskProjection{}, application.NewError("create-task", application.ReasonInvalidRequest)
	}
	borrow, err := s.borrow()
	if err != nil {
		return application.TaskProjection{}, err
	}
	defer borrow.Close()
	id, err := resultingress.TaskIDForRequest(s.acquisition.Scope, key)
	if err != nil {
		return application.TaskProjection{}, taskError(err)
	}
	requestRaw, _ := json.Marshal(request.Submission)
	requestDigest, err := canonical.DigestJSON(requestRaw)
	if err != nil {
		return application.TaskProjection{}, err
	}
	var draft goal.TaskDraft
	var found bool
	reader := repositoryApprovedTeamVerifier{session: s}
	err = reader.WithCurrentApprovedTeam(ctx, s.acquisition, resultingress.TeamPlanApproval{}, func() error {
		var readErr error
		draft, found, readErr = s.ingress.ReadTaskDraft(s.acquisition.Scope, id)
		return readErr
	})
	if err != nil {
		return application.TaskProjection{}, err
	}
	if found {
		if draft.RequestDigest != requestDigest {
			return application.TaskProjection{}, application.NewError("create-task", application.ReasonAuthorityConflict)
		}
		return s.readTaskBorrowed(ctx, id)
	}
	if s.taskTemplate == nil || s.taskTemplate.Digest() == "" || s.teamInputPreflight == nil {
		return application.TaskProjection{}, application.NewError("create-task", application.ReasonCompositionIncomplete)
	}
	inputs, err := s.taskTemplate.RenderTask(id, request.Submission)
	if err != nil {
		return application.TaskProjection{}, application.NewError("create-task", application.ReasonInvalidRequest)
	}
	canonicalInputs, err := canonical.JSON(inputs)
	if err != nil || !bytes.Equal(inputs, canonicalInputs) {
		return application.TaskProjection{}, application.NewError("create-task", application.ReasonInvalidRequest)
	}
	inputsDigest := canonical.DigestBytes(canonicalInputs)
	validation := bytes.Clone(canonicalInputs)
	if s.teamInputPreflight(validation) != nil || canonical.DigestBytes(validation) != inputsDigest {
		return application.TaskProjection{}, application.NewError("create-task", application.ReasonInvalidRequest)
	}
	now := time.Now().UTC()
	draft = goal.TaskDraft{GoalID: id, Revision: 1, RequestKeyDigest: key, RequestDigest: requestDigest, Request: request.Submission, TemplateDigest: s.taskTemplate.Digest(), InputsDigest: inputsDigest, Inputs: canonicalInputs, CreatedAt: now.Format(time.RFC3339Nano), ConfirmBefore: now.Add(30 * time.Minute).Format(time.RFC3339Nano)}
	if _, err = s.ingress.RecordTaskDraft(ctx, reader, s.acquisition, draft); err != nil {
		return application.TaskProjection{}, taskError(err)
	}
	return s.readTaskBorrowed(ctx, id)
}

func (s *RepositorySession) ReadTask(ctx context.Context, id string) (application.TaskProjection, error) {
	if ctx == nil || domain.ValidateID(id) != nil {
		return application.TaskProjection{}, application.NewError("read-task", application.ReasonInvalidRequest)
	}
	borrow, err := s.borrow()
	if err != nil {
		return application.TaskProjection{}, err
	}
	defer borrow.Close()
	return s.readTaskBorrowed(ctx, id)
}

func (s *RepositorySession) ListTasks(ctx context.Context, request application.TaskListRequest) (application.TaskPage, error) {
	if ctx == nil || request.Limit < 1 || request.Limit > 20 || request.After != "" && domain.ValidateID(request.After) != nil {
		return application.TaskPage{}, application.NewError("list-tasks", application.ReasonInvalidRequest)
	}
	borrow, err := s.borrow()
	if err != nil {
		return application.TaskPage{}, err
	}
	defer borrow.Close()
	reader := repositoryApprovedTeamVerifier{session: s}
	var ids []string
	err = reader.WithCurrentApprovedTeam(ctx, s.acquisition, resultingress.TeamPlanApproval{}, func() error {
		var e error
		ids, e = s.ingress.ListTaskDraftIDs(s.acquisition.Scope, request.After, request.Limit+1)
		return e
	})
	if err != nil {
		return application.TaskPage{}, err
	}
	page := application.TaskPage{Items: []application.TaskProjection{}}
	if len(ids) > request.Limit {
		ids = ids[:request.Limit]
		page.NextCursor = ids[len(ids)-1]
	}
	for _, id := range ids {
		item, e := s.readTaskBorrowed(ctx, id)
		if e != nil {
			return application.TaskPage{}, e
		}
		page.Items = append(page.Items, item)
	}
	return page, nil
}

// Confirmation is the existing accepted-plan append, not a second approval
// event. Replays match that fact before checking an expired draft/preflight.
func (s *RepositorySession) ApproveTask(ctx context.Context, request application.ApproveTaskRequest) (application.TaskProjection, error) {
	key, err := taskRequestKey(request.IdempotencyKey)
	if ctx == nil || err != nil || domain.ValidateID(request.TaskID) != nil || request.ExpectedRevision < 1 {
		return application.TaskProjection{}, application.NewError("approve-task", application.ReasonInvalidRequest)
	}
	borrow, err := s.borrow()
	if err != nil {
		return application.TaskProjection{}, err
	}
	defer borrow.Close()
	var draft goal.TaskDraft
	var plan resultingress.TeamPlanState
	var approved bool
	reader := repositoryApprovedTeamVerifier{session: s}
	err = reader.WithCurrentApprovedTeam(ctx, s.acquisition, resultingress.TeamPlanApproval{}, func() error {
		var found bool
		var e error
		draft, found, e = s.ingress.ReadTaskDraft(s.acquisition.Scope, request.TaskID)
		if e != nil {
			return e
		}
		if !found {
			return application.NewError("approve-task", application.ReasonTaskNotFound)
		}
		plan, approved, e = s.ingress.ReadTeamPlan(s.acquisition.Scope, request.TaskID)
		return e
	})
	if err != nil {
		return application.TaskProjection{}, err
	}
	if draft.Revision != request.ExpectedRevision || draft.FactDigest != request.PreviewDigest {
		return application.TaskProjection{}, application.NewError("approve-task", application.ReasonAuthorityConflict)
	}
	raw, _ := json.Marshal(struct {
		Protocol, TaskID, Key, Preview string
		Revision                       int64
	}{"task-confirm/v1", request.TaskID, key, request.PreviewDigest, request.ExpectedRevision})
	digest, err := canonical.DigestJSON(raw)
	if err != nil {
		return application.TaskProjection{}, err
	}
	approval := resultingress.TeamPlanApproval{InputsDigest: draft.InputsDigest, RequestDigest: digest, TaskDraftDigest: draft.FactDigest}
	if approved {
		if plan.Approval != approval {
			return application.TaskProjection{}, application.NewError("approve-task", application.ReasonAuthorityConflict)
		}
		return s.readTaskBorrowed(ctx, request.TaskID)
	}
	deadline, _ := time.Parse(time.RFC3339Nano, draft.ConfirmBefore)
	if !time.Now().Before(deadline) {
		return application.TaskProjection{}, application.NewError("approve-task", application.ReasonTaskConfirmationExpired)
	}
	if s.teamInputPreflight == nil {
		return application.TaskProjection{}, application.NewError("approve-task", application.ReasonCompositionIncomplete)
	}
	validation := bytes.Clone(draft.Inputs)
	if s.teamInputPreflight(validation) != nil || canonical.DigestBytes(validation) != draft.InputsDigest {
		return application.TaskProjection{}, application.NewError("approve-task", application.ReasonInvalidRequest)
	}
	verifier := repositoryApprovedTeamVerifier{session: s, approval: approval}
	_, err = s.ingress.AcceptInitialTeamPlan(ctx, verifier, s.acquisition, approval, draft.Inputs)
	if err != nil {
		return application.TaskProjection{}, taskError(err)
	}
	return s.readTaskBorrowed(ctx, request.TaskID)
}

func (s *RepositorySession) readTaskBorrowed(ctx context.Context, id string) (result application.TaskProjection, resultErr error) {
	reader := repositoryApprovedTeamVerifier{session: s}
	resultErr = reader.WithCurrentApprovedTeam(ctx, s.acquisition, resultingress.TeamPlanApproval{}, func() error {
		draft, found, err := s.ingress.ReadTaskDraft(s.acquisition.Scope, id)
		if err != nil {
			return err
		}
		if !found {
			return application.NewError("read-task", application.ReasonTaskNotFound)
		}
		var inputs goal.TeamInputs
		if json.Unmarshal(draft.Inputs, &inputs) != nil {
			return application.NewError("read-task", application.ReasonAuthorityConflict)
		}
		if s.taskTemplate == nil {
			return application.NewError("read-task", application.ReasonCompositionIncomplete)
		}
		nodes, err := s.taskTemplate.InspectTask(bytes.Clone(draft.Inputs))
		if err != nil {
			return application.NewError("read-task", application.ReasonAuthorityConflict)
		}
		result = application.TaskProjection{ID: id, Status: "awaiting-confirmation", Revision: draft.Revision, PreviewDigest: draft.FactDigest, Request: draft.Request, CreatedAt: draft.CreatedAt, ConfirmBefore: draft.ConfirmBefore, AllowedActions: []string{"query"}, Workers: []application.TaskWorkerProjection{}, Edges: []application.TaskEdge{}, UsageSource: "unavailable", Preview: application.TaskPreview{TemplateDigest: draft.TemplateDigest, InputsDigest: draft.InputsDigest, Publication: "none", Limits: inputs.Limits, Nodes: []application.TaskPreviewNode{}}}
		result.Preview.Nodes = nodes
		deadline, _ := time.Parse(time.RFC3339Nano, draft.ConfirmBefore)
		if time.Now().Before(deadline) {
			result.AllowedActions = append(result.AllowedActions, "approve")
		} else {
			result.Status = "confirmation-expired"
		}
		plan, approved, err := s.ingress.ReadTeamPlan(s.acquisition.Scope, id)
		if err != nil {
			return err
		}
		if approved {
			if plan.Approval.TaskDraftDigest != draft.FactDigest {
				return application.NewError("read-task", application.ReasonAuthorityConflict)
			}
			p := teamApprovalProjection(plan)
			result.Approval = &p
			result.Status = "approved"
			result.AllowedActions = []string{"query"}
		}
		for _, node := range inputs.Nodes {
			_, runID, e := goal.TeamNodeIDs(inputs.Proposal, node.NodeID)
			if e != nil {
				return e
			}
			worker := application.TaskWorkerProjection{ID: runID, NodeID: node.NodeID, Role: node.Role, Status: "planned"}
			creation, created, e := s.ingress.ReadTeamRunCreation(s.acquisition.Scope, id, node.NodeID)
			if e != nil {
				return e
			}
			if created {
				worker.Status = "creating"
				lease, e := s.runs.AcquireExisting(creation.RunID)
				if errors.Is(e, runstore.ErrLeaseHeld) {
					worker.Status = "busy"
				} else if errors.Is(e, os.ErrNotExist) {
				} else if e != nil {
					return e
				} else {
					read, e := s.runs.ReadRunStartAuthorityUnderLease(ctx, lease)
					releaseErr := lease.Release()
					if e != nil || releaseErr != nil {
						return errors.Join(e, releaseErr)
					}
					p := read.Run
					worker.Run = &p
					worker.Status = string(p.State)
				}
			}
			result.Workers = append(result.Workers, worker)
			state := domain.State(worker.Status)
			if state.Terminal() && state != domain.StateAccepted {
				result.Status = "blocked"
				result.Reason = "worker-" + worker.Status
			} else if worker.Status == string(domain.StateReviewPending) && result.Status != "blocked" {
				result.Status = "review-pending"
			} else if result.Status == "approved" && created {
				result.Status = "running"
			}
		}
		for _, edge := range inputs.Proposal.Edges {
			result.Edges = append(result.Edges, application.TaskEdge{From: edge.From, To: edge.To})
		}
		halt, halted, e := s.ingress.ReadTeamPlanHalt(s.acquisition.Scope, id)
		if e != nil {
			return e
		}
		if halted {
			result.Status = "blocked"
			result.Reason = "team-halted-" + halt.Stage
		}
		outcome, completed, e := s.ingress.ReadTeamOutcome(s.acquisition.Scope, id)
		if e != nil {
			return e
		}
		if completed {
			p, e := projectTeamOutcome(outcome)
			if e != nil {
				return e
			}
			result.Outcome = &p
			result.Status = "verified-awaiting-delivery"
			result.Reason = "delivery-not-yet-supported"
		}
		return nil
	})
	return result, resultErr
}
