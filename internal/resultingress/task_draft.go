package resultingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
)

const taskDraftFactType = "task-draft-created"
const taskDraftProtocol = "bounded-task-draft/v1"

var ErrTaskDraftExpired = errors.New("resultingress: Task confirmation expired")

// This is a projection of one immutable RB1 fact, not an independently written
// Task state file. A draft consumes no Run/Attempt/reservation authority.
type taskDraftState struct {
	Scope ControlOwnerScope `json:"scope"`
	Draft goal.TaskDraft    `json:"draft"`
}

type taskDraftFact struct {
	ProtocolRevision string            `json:"protocolRevision"`
	FactType         string            `json:"factType"`
	Sequence         int64             `json:"sequence"`
	Scope            ControlOwnerScope `json:"scope"`
	OwnerFactDigest  string            `json:"ownerFactDigest"`
	Draft            goal.TaskDraft    `json:"draft"`
	Digest           string            `json:"digest"`
}

func TaskIDForRequest(scope ControlOwnerScope, requestKeyDigest string) (string, error) {
	if scope.Validate() != nil || requireDigest("task request key", requestKeyDigest) != nil {
		return "", ErrTeamPlanConflict
	}
	digest := canonicalDigestOrEmpty(struct {
		Scope ControlOwnerScope
		Key   string
	}{scope, requestKeyDigest})
	return "task-" + strings.TrimPrefix(digest, "sha256:"), nil
}

func validateTaskDraft(scope ControlOwnerScope, draft goal.TaskDraft) error {
	id, err := TaskIDForRequest(scope, draft.RequestKeyDigest)
	if err != nil || id != draft.GoalID || draft.Validate() != nil {
		return ErrTeamPlanConflict
	}
	var inputs goal.TeamInputs
	if decodeTeamRecord(draft.Inputs, &inputs) != nil || !inputs.Spec.AuthorityNamespaceId.Equal(scope.AuthorityNamespaceID) {
		return ErrTeamPlanConflict
	}
	return nil
}

// RecordTaskDraft is called only after the resident application's full pure
// template and actual environment preflight. Current owner is rechecked again
// inside the append transaction. A lost response reuses the original deadline.
func (s *DurableStore) RecordTaskDraft(ctx context.Context, verifier CurrentApprovedTeamVerifier, owner ControlOwnerAcquisition, draft goal.TaskDraft) (goal.TaskDraft, error) {
	if ctx == nil || verifier == nil || owner.Validate() != nil || draft.FactDigest != "" || validateTaskDraft(owner.Scope, draft) != nil {
		return goal.TaskDraft{}, ErrTeamPlanConflict
	}
	raw, err := json.Marshal(draft)
	if err != nil || json.Unmarshal(raw, &draft) != nil {
		return goal.TaskDraft{}, ErrTeamPlanConflict
	}
	var result goal.TaskDraft
	err = withCurrentOwnerLock(ctx, teamApprovalOwnerVerifier{verifier: verifier}, owner, func() error {
		projection := newAuthorityProjection()
		return s.transact(projection, func() error {
			ownerKey, _ := owner.Scope.key()
			current, found := projection.controlOwners[ownerKey]
			if !found || current.Acquisition != owner {
				return ErrControlOwnerNotCurrent
			}
			key := teamPlanKey(owner.Scope, draft.GoalID)
			if existing, found := projection.taskDrafts[key]; found {
				if existing.Draft.RequestKeyDigest != draft.RequestKeyDigest || existing.Draft.RequestDigest != draft.RequestDigest {
					return ErrTeamPlanConflict
				}
				result = existing.Draft
				return nil
			}
			if _, found := projection.teamPlans[key]; found {
				return ErrTeamPlanConflict
			}
			if _, found := projection.taskClarifications[key]; found {
				return ErrTeamPlanConflict
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			fact := &taskDraftFact{ProtocolRevision: taskDraftProtocol, FactType: taskDraftFactType, Sequence: s.nextSequence, Scope: owner.Scope, OwnerFactDigest: current.FactDigest, Draft: draft}
			encoded, err := json.Marshal(fact)
			if err != nil || len(encoded)+100 > goal.MaxTeamInputsBytes+(128<<10) {
				return ErrTeamPlanConflict
			}
			if err := s.appendLine(fact, func() string { return fact.Digest }, func(v string) { fact.Digest = v }); err != nil {
				return err
			}
			s.nextSequence++
			draft.FactDigest = fact.Digest
			projection.taskDrafts[key] = taskDraftState{Scope: owner.Scope, Draft: draft}
			result = draft
			return nil
		})
	})
	return result, err
}

func (s *DurableStore) ReadTaskDraft(scope ControlOwnerScope, taskID string) (goal.TaskDraft, bool, error) {
	if scope.Validate() != nil || domain.ValidateID(taskID) != nil {
		return goal.TaskDraft{}, false, ErrTeamPlanConflict
	}
	projection := newAuthorityProjection()
	var draft goal.TaskDraft
	var found bool
	err := s.transact(projection, func() error {
		state, exists := projection.taskDrafts[teamPlanKey(scope, taskID)]
		draft, found = state.Draft, exists
		return nil
	})
	return draft, found, err
}

// IDs only, bounded before expensive per-Task projection. The public page
// cursor is an opaque-to-authority Task ID; it grants no object access.
func (s *DurableStore) ListTaskDraftIDs(scope ControlOwnerScope, after string, limit int) ([]string, error) {
	if scope.Validate() != nil || limit < 1 || limit > 100 || after != "" && domain.ValidateID(after) != nil {
		return nil, ErrTeamPlanConflict
	}
	projection := newAuthorityProjection()
	result := []string{}
	err := s.transact(projection, func() error {
		for _, state := range projection.taskDrafts {
			if state.Scope == scope && state.Draft.GoalID > after {
				position, _ := slices.BinarySearch(result, state.Draft.GoalID)
				if position < limit {
					result = slices.Insert(result, position, state.Draft.GoalID)
					if len(result) > limit {
						result = result[:limit]
					}
				}
			}
		}
		for _, state := range projection.taskClarifications {
			if state.Scope == scope && state.Root.TaskID > after {
				position, _ := slices.BinarySearch(result, state.Root.TaskID)
				if position < limit {
					result = slices.Insert(result, position, state.Root.TaskID)
					if len(result) > limit {
						result = result[:limit]
					}
				}
			}
		}
		return nil
	})
	return result, err
}

func validateTaskDraftApproval(in *Ingress, scope ControlOwnerScope, taskID string, approval TeamPlanApproval) error {
	if _, stopped := in.taskStops[teamPlanKey(scope, taskID)]; stopped {
		return ErrTaskStopped
	}
	state, found := currentTaskProposal(in, teamPlanKey(scope, taskID))
	if !found {
		if approval.TaskDraftDigest != "" {
			return ErrTeamPlanConflict
		}
		return nil // Existing non-Task AF_UNIX contract is unchanged.
	}
	if state.QuestionsPending != 0 {
		return ErrTaskQuestionsPending
	}
	if state.FactDigest != approval.TaskDraftDigest || state.InputsDigest != approval.InputsDigest {
		return ErrTeamPlanConflict
	}
	return nil
}

func applyTaskDraftLine(line []byte, in *Ingress, sequence int64) error {
	if len(line) > goal.MaxTeamInputsBytes+(128<<10) {
		return ErrTeamPlanConflict
	}
	var fact taskDraftFact
	if decodeTeamRecord(line, &fact) != nil || fact.ProtocolRevision != taskDraftProtocol || fact.FactType != taskDraftFactType || fact.Sequence != sequence || fact.Draft.FactDigest != "" || validateTaskDraft(fact.Scope, fact.Draft) != nil {
		return ErrTeamPlanConflict
	}
	digest := fact.Digest
	fact.Digest = ""
	if requireDigest("task draft fact", digest) != nil || canonicalDigestOrEmpty(fact) != digest {
		return ErrTeamPlanConflict
	}
	ownerKey, _ := fact.Scope.key()
	current, found := in.controlOwners[ownerKey]
	if !found || current.FactDigest != fact.OwnerFactDigest {
		return ErrControlOwnerNotCurrent
	}
	key := teamPlanKey(fact.Scope, fact.Draft.GoalID)
	if _, found := in.taskDrafts[key]; found {
		return ErrTeamPlanConflict
	}
	if _, found := in.taskClarifications[key]; found {
		return ErrTeamPlanConflict
	}
	if _, found := in.teamPlans[key]; found {
		return ErrTeamPlanConflict
	}
	canonicalInputs, err := canonical.JSON(fact.Draft.Inputs)
	if err != nil || !bytes.Equal(canonicalInputs, fact.Draft.Inputs) {
		return ErrTeamPlanConflict
	}
	fact.Draft.FactDigest = digest
	in.taskDrafts[key] = taskDraftState{Scope: fact.Scope, Draft: fact.Draft}
	return nil
}
