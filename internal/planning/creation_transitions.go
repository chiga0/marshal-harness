package planning

import (
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
)

// One producer for both Plan and durable creation recovery. Recovery compares
// every frozen event field (except the already assigned random event ID).
func (p *PreparedPlan) creationTransitions(worktree *gitworktree.Worktree) ([]domain.RunEvent, []domain.RunState, error) {
	initial := domain.NewRunState(p.task.Metadata.ID, p.input.RunID, p.now)
	initial.SpecDigest, initial.PolicyDigest = p.specDigest, p.policyDigest
	plannedEvent, planned, err := transition(initial, "planning.spec-accepted", domain.StatePlanned, p.now, map[string]any{
		"specDigest": p.specDigest, "executionProfile": p.task.Worker.ExecutionProfile, "sessionPolicy": p.task.Worker.SessionPolicy,
	}, lifecycle.Guard{LeaseHeld: true, DraftValid: true})
	if err != nil {
		return nil, nil, err
	}
	requirements, err := domain.SandboxRequirementsFromLegacy(p.task.Worker.ExecutionProfile)
	if err != nil {
		return nil, nil, err
	}
	readyEvent, ready, err := transition(planned, "planning.inputs-frozen", domain.StateReady, p.now, map[string]any{
		"adapterId": p.selection.Adapter.ID(), "baseSha": p.baseSHA,
		"specDigest": p.specDigest, "policyDigest": p.policyDigest, "capabilityDigest": p.capabilityDigest,
		"worktreePath": worktree.Path, "branch": worktree.Branch,
		"fallbackAllowed": p.effective.AllowFallbackWorkers, "selectionAttempts": selectionAttemptPayload(p.selection.Attempts),
		"maxAttempts":         p.task.Budgets.MaxAttempts,
		"sandboxRequirements": map[string]any{"accessMode": string(requirements.AccessMode), "minimumAssuranceLevel": string(requirements.MinimumAssuranceLevel)},
	}, lifecycle.Guard{LeaseHeld: true, BaseResolved: true, PolicyAllowed: true, AdapterProbed: true, InputsFrozen: true})
	if err != nil {
		return nil, nil, err
	}
	ready.CapabilityDigest, ready.BaseSHA, ready.WorktreePath = p.capabilityDigest, p.baseSHA, worktree.Path
	return []domain.RunEvent{plannedEvent, readyEvent}, []domain.RunState{initial, planned, ready}, nil
}
