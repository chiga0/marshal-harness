package resultingress

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
)

const taskDeliveryFactType = "task-delivery-created"
const taskDeliveryProtocol = "bounded-task-delivery/v1"

// The producer holds the current owner, original three accepted Run leases
// and a rechecked durable content file. Caller JSON is never a verifier.
type CurrentTaskDeliveryVerifier interface {
	WithCurrentTaskDelivery(context.Context, ControlOwnerAcquisition, string, func(goal.TaskDelivery) error) error
}

type taskDeliveryFact struct {
	ProtocolRevision string            `json:"protocolRevision"`
	FactType         string            `json:"factType"`
	Sequence         int64             `json:"sequence"`
	Scope            ControlOwnerScope `json:"scope"`
	OwnerFactDigest  string            `json:"ownerFactDigest"`
	Delivery         goal.TaskDelivery `json:"delivery"`
	Digest           string            `json:"digest"`
}

func validateTaskDelivery(in *Ingress, scope ControlOwnerScope, value goal.TaskDelivery) error {
	key := teamPlanKey(scope, value.GoalID)
	if _, stopped := in.taskStops[key]; stopped {
		return ErrTaskStopped
	}
	draft, drafted := in.taskDrafts[key]
	plan, planned := in.teamPlans[key]
	outcome, completed := in.teamOutcomes[key]
	if value.Validate() != nil || !drafted || !planned || !completed || draft.Draft.Request.Template != goal.TaskTemplateOrderQuote || plan.Approval.TaskDraftDigest != draft.Draft.FactDigest || value.OutcomeFactDigest != outcome.FactDigest || value.PlanFactDigest != plan.FactDigest || value.IntegrationRunID != outcome.Integration.RunID || value.IntegrationBaseSHA != outcome.IntegrationBaseSHA {
		return ErrTeamPlanConflict
	}
	var candidates, patches, decisions []string
	for _, source := range append(slices.Clone(outcome.Upstreams), outcome.Integration) {
		candidates = append(candidates, source.CandidateDigest)
		patches = append(patches, source.PatchDigest)
		decisions = append(decisions, source.DecisionDigest)
	}
	if !slices.Equal(candidates, value.CandidateDigests) || !slices.Equal(patches, value.PatchDigests) || !slices.Equal(decisions, value.DecisionDigests) {
		return ErrTeamPlanConflict
	}
	return nil
}

func (s *DurableStore) RecordTaskDelivery(ctx context.Context, verifier CurrentTaskDeliveryVerifier, owner ControlOwnerAcquisition, taskID string) (goal.TaskDelivery, error) {
	if ctx == nil || verifier == nil || owner.Validate() != nil || domain.ValidateID(taskID) != nil {
		return goal.TaskDelivery{}, ErrTeamPlanConflict
	}
	var result goal.TaskDelivery
	entered := false
	err := verifier.WithCurrentTaskDelivery(ctx, owner, taskID, func(value goal.TaskDelivery) error {
		if entered || value.GoalID != taskID || value.FactDigest != "" {
			return ErrTeamPlanConflict
		}
		entered = true
		raw, err := json.Marshal(value)
		if err != nil || json.Unmarshal(raw, &value) != nil {
			return ErrTeamPlanConflict
		}
		projection := newAuthorityProjection()
		return s.transact(projection, func() error {
			ownerKey, _ := owner.Scope.key()
			current, present := projection.controlOwners[ownerKey]
			if !present || current.Acquisition != owner {
				return ErrControlOwnerNotCurrent
			}
			if validateTaskDelivery(projection, owner.Scope, value) != nil {
				return ErrTeamPlanConflict
			}
			key := teamPlanKey(owner.Scope, taskID)
			if old, found := projection.taskDeliveries[key]; found {
				comparison := old
				comparison.FactDigest = ""
				if canonicalDigestOrEmpty(comparison) != canonicalDigestOrEmpty(value) {
					return ErrTeamPlanConflict
				}
				result = old
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			fact := &taskDeliveryFact{ProtocolRevision: taskDeliveryProtocol, FactType: taskDeliveryFactType, Sequence: s.nextSequence, Scope: owner.Scope, OwnerFactDigest: current.FactDigest, Delivery: value}
			if err := s.appendLine(fact, func() string { return fact.Digest }, func(d string) { fact.Digest = d }); err != nil {
				return err
			}
			s.nextSequence++
			value.FactDigest = fact.Digest
			projection.taskDeliveries[key], result = value, value
			return nil
		})
	})
	if err != nil {
		return goal.TaskDelivery{}, err
	}
	if !entered || result.Validate() != nil || result.FactDigest == "" {
		return goal.TaskDelivery{}, ErrTeamPlanConflict
	}
	return result, nil
}

func (s *DurableStore) ReadTaskDelivery(scope ControlOwnerScope, taskID string) (value goal.TaskDelivery, found bool, err error) {
	if scope.Validate() != nil || domain.ValidateID(taskID) != nil {
		return value, false, ErrTeamPlanConflict
	}
	projection := newAuthorityProjection()
	err = s.transact(projection, func() error { value, found = projection.taskDeliveries[teamPlanKey(scope, taskID)]; return nil })
	return
}

func applyTaskDeliveryLine(line []byte, in *Ingress, sequence int64) error {
	var fact taskDeliveryFact
	if len(line) > 16384 || decodeTeamRecord(line, &fact) != nil || fact.ProtocolRevision != taskDeliveryProtocol || fact.FactType != taskDeliveryFactType || fact.Sequence != sequence || fact.Delivery.FactDigest != "" {
		return ErrTeamPlanConflict
	}
	digest := fact.Digest
	fact.Digest = ""
	if requireDigest("task delivery", digest) != nil || canonicalDigestOrEmpty(fact) != digest {
		return ErrTeamPlanConflict
	}
	ownerKey, _ := fact.Scope.key()
	owner, exists := in.controlOwners[ownerKey]
	if !exists || owner.FactDigest != fact.OwnerFactDigest || validateTaskDelivery(in, fact.Scope, fact.Delivery) != nil {
		return ErrTeamPlanConflict
	}
	key := teamPlanKey(fact.Scope, fact.Delivery.GoalID)
	if _, exists := in.taskDeliveries[key]; exists {
		return ErrTeamPlanConflict
	}
	fact.Delivery.FactDigest = digest
	in.taskDeliveries[key] = fact.Delivery
	return nil
}
