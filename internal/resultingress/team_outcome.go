package resultingress

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
)

const teamOutcomeFactType = "team-outcome-completed"
const teamOutcomeProtocol = "bounded-team-outcome/v1"

var ErrTeamOutcomeNotReady = errors.New("resultingress: team outcome not ready")

// Immutable delivery references, not a publication receipt or token bill.
// BudgetDigest references the original reservation snapshot; AttemptsUsed
// is measured from Run journals. Unverified token/compute usage is not zero.
type TeamDeliveryOutcome struct {
	Outcome            goal.GoalOutcome     `json:"outcome"`
	PlanFactDigest     string               `json:"planFactDigest"`
	Upstreams          []TeamAcceptedSource `json:"upstreams"`
	Integration        TeamAcceptedSource   `json:"integration"`
	IntegrationBaseSHA string               `json:"integrationBaseSha"`
	AttemptsUsed       int64                `json:"attemptsUsed"`
	Measurement        string               `json:"measurement"`
	FactDigest         string               `json:"factDigest"`
}

// The callback's data is produced by a current-owner reader holding all three
// actual Run leases. There is deliberately no caller-supplied result argument.
type CurrentCompletedTeamVerifier interface {
	WithCurrentCompletedTeam(context.Context, ControlOwnerAcquisition, TeamPlanApproval, string, string, func(TeamDeliveryOutcome) error) error
}

type teamOutcomeFact struct {
	ProtocolRevision string              `json:"protocolRevision"`
	FactType         string              `json:"factType"`
	Sequence         int64               `json:"sequence"`
	Scope            ControlOwnerScope   `json:"scope"`
	OwnerFactDigest  string              `json:"ownerFactDigest"`
	Delivery         TeamDeliveryOutcome `json:"delivery"`
	Digest           string              `json:"digest"`
}

func validateTeamOutcome(in *Ingress, scope ControlOwnerScope, value TeamDeliveryOutcome) error {
	fail := func() error { return ErrTeamPlanConflict }
	stamp, stampErr := time.Parse(time.RFC3339Nano, value.Outcome.FinalizedAt)
	if stampErr != nil || stamp.UTC().Format(time.RFC3339Nano) != value.Outcome.FinalizedAt {
		return fail()
	}
	key := teamPlanKey(scope, value.Outcome.GoalId)
	if _, stopped := in.taskStops[key]; stopped {
		return ErrTaskStopped
	}
	plan, found := in.teamPlans[key]
	if !found || plan.FactDigest != value.PlanFactDigest || value.Outcome.Validate() != nil || value.Outcome.State != goal.OutcomeStateCompleted ||
		value.Outcome.AuthorityNamespaceId != scope.AuthorityNamespaceID || value.Outcome.Reason != "verified-team-delivery" ||
		value.Outcome.BudgetDigest != plan.Revision.BudgetSnapshotDigest || value.Measurement != "attempt-counts-only" || len(value.Upstreams) != 2 {
		return fail()
	}
	if _, halted := in.teamHalts[key]; halted {
		return fail()
	}
	revision, err := plan.Revision.Digest()
	if err != nil || value.Outcome.FinalPlanDigest != revision {
		return fail()
	}
	var input goal.TeamInputs
	if json.Unmarshal(plan.Inputs, &input) != nil || value.AttemptsUsed != 3 || value.AttemptsUsed > input.Limits.MaxTotalAttempts {
		// Initial profile freezes one Attempt per node. Replan needs its own
		// cumulative accounting; this entry must never reset it.
		return fail()
	}
	creation, found := in.teamRunCreations[teamRunCreationKey(scope, value.Outcome.GoalId, value.Integration.NodeID)]
	if !found || creation.PlanFactDigest != plan.FactDigest || creation.Integration == nil ||
		creation.Integration.CommitSHA != value.IntegrationBaseSHA || !slices.Equal(creation.Integration.Inputs.Sources, value.Upstreams) ||
		creation.RunID != value.Integration.RunID || creation.FactDigest != value.Integration.CreationFactDigest ||
		validateTeamIntegration(plan, input, creation) != nil || validateTeamIntegrationDependencies(in, scope, creation) != nil ||
		domain.ValidateID(value.Integration.AttemptID) != nil {
		return fail()
	}
	for _, digest := range []string{value.Integration.AuthorityHead, value.Integration.CandidateDigest, value.Integration.PatchDigest,
		value.Integration.DecisionDigest, value.Integration.PacketDigest, value.Integration.OutcomeDigest} {
		if requireDigest("team integration outcome", digest) != nil {
			return fail()
		}
	}
	return nil
}

// CompleteTeam atomically appends the producer's verified result once. The
// producer keeps current owner and all Run leases held through this callback.
func (s *DurableStore) CompleteTeam(ctx context.Context, verifier CurrentCompletedTeamVerifier, owner ControlOwnerAcquisition, approval TeamPlanApproval, goalID, planFact string) (TeamDeliveryOutcome, error) {
	if ctx == nil || verifier == nil || owner.Validate() != nil || domain.ValidateID(goalID) != nil || requireDigest("team plan", planFact) != nil {
		return TeamDeliveryOutcome{}, ErrTeamPlanConflict
	}
	var result TeamDeliveryOutcome
	entered := false
	err := verifier.WithCurrentCompletedTeam(ctx, owner, approval, goalID, planFact, func(value TeamDeliveryOutcome) error {
		if entered {
			return ErrTeamPlanConflict
		}
		entered = true
		if err := ctx.Err(); err != nil {
			return err
		}
		// Freeze callback-owned slices before validating/appending. This is a
		// typed clone, not the strict canonical wire decoder.
		raw, err := json.Marshal(value)
		if err != nil || json.Unmarshal(raw, &result) != nil {
			return ErrTeamPlanConflict
		}
		value = result
		if value.FactDigest != "" || value.Outcome.GoalId != goalID || value.PlanFactDigest != planFact {
			return ErrTeamPlanConflict
		}
		projection := newAuthorityProjection()
		return s.transact(projection, func() error {
			ownerKey, _ := owner.Scope.key()
			current, exists := projection.controlOwners[ownerKey]
			if !exists || current.Acquisition != owner {
				return ErrControlOwnerNotCurrent
			}
			key := teamPlanKey(owner.Scope, goalID)
			if projection.teamPlans[key].Approval != approval || validateTeamOutcome(projection, owner.Scope, value) != nil {
				return ErrTeamPlanConflict
			}
			if previous, exists := projection.teamOutcomes[key]; exists {
				comparison := previous
				comparison.FactDigest = ""
				if canonicalDigestOrEmpty(comparison) != canonicalDigestOrEmpty(value) {
					return ErrTeamPlanConflict
				}
				result = previous
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			fact := &teamOutcomeFact{ProtocolRevision: teamOutcomeProtocol, FactType: teamOutcomeFactType, Sequence: s.nextSequence,
				Scope: owner.Scope, OwnerFactDigest: current.FactDigest, Delivery: value}
			if err := s.appendLine(fact, func() string { return fact.Digest }, func(digest string) { fact.Digest = digest }); err != nil {
				return err
			}
			s.nextSequence++
			value.FactDigest = fact.Digest
			projection.teamOutcomes[key] = value
			result = value
			return nil
		})
	})
	if err != nil {
		return TeamDeliveryOutcome{}, err
	}
	if !entered || requireDigest("completed fact", result.FactDigest) != nil {
		return TeamDeliveryOutcome{}, ErrTeamPlanConflict
	}
	return result, nil
}

func (s *DurableStore) ReadTeamOutcome(scope ControlOwnerScope, goalID string) (value TeamDeliveryOutcome, found bool, err error) {
	if scope.Validate() != nil || domain.ValidateID(goalID) != nil {
		return value, false, ErrTeamPlanConflict
	}
	projection := newAuthorityProjection()
	err = s.transact(projection, func() error { value, found = projection.teamOutcomes[teamPlanKey(scope, goalID)]; return nil })
	return
}

func applyTeamOutcomeLine(line []byte, in *Ingress, sequence int64) error {
	var fact teamOutcomeFact
	if len(line) > 16384 || decodeTeamRecord(line, &fact) != nil || fact.ProtocolRevision != teamOutcomeProtocol ||
		fact.FactType != teamOutcomeFactType || fact.Sequence != sequence || fact.Delivery.FactDigest != "" {
		return ErrTeamPlanConflict
	}
	digest := fact.Digest
	fact.Digest = ""
	if requireDigest("team outcome", digest) != nil || canonicalDigestOrEmpty(fact) != digest {
		return ErrTeamPlanConflict
	}
	ownerKey, _ := fact.Scope.key()
	owner, found := in.controlOwners[ownerKey]
	if !found || owner.FactDigest != fact.OwnerFactDigest {
		return ErrControlOwnerNotCurrent
	}
	if validateTeamOutcome(in, fact.Scope, fact.Delivery) != nil {
		return ErrTeamPlanConflict
	}
	key := teamPlanKey(fact.Scope, fact.Delivery.Outcome.GoalId)
	if _, found := in.teamOutcomes[key]; found {
		return ErrTeamPlanConflict
	}
	fact.Delivery.FactDigest = digest
	in.teamOutcomes[key] = fact.Delivery
	return nil
}
