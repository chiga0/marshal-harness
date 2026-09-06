package resultingress

import (
	"context"
	"github.com/chiga0/marshal-harness/internal/domain"
)

const teamHaltFactType = "team-plan-halted"
const teamHaltProtocol = "bounded-team-halt/v1"

// TeamPlanHalt revokes further dispatch only. It is not a Run terminal result,
// budget settlement, cancellation receipt or permission to replace the plan.
type TeamPlanHalt struct {
	GoalID         string `json:"goalId"`
	NodeID         string `json:"nodeId"`
	PlanFactDigest string `json:"planFactDigest"`
	Stage          string `json:"stage"`
	FactDigest     string `json:"factDigest"`
}

type teamHaltFact struct {
	ProtocolRevision string            `json:"protocolRevision"`
	FactType         string            `json:"factType"`
	Sequence         int64             `json:"sequence"`
	Scope            ControlOwnerScope `json:"scope"`
	OwnerFactDigest  string            `json:"ownerFactDigest"`
	Halt             TeamPlanHalt      `json:"halt"`
	Digest           string            `json:"digest"`
}

func validateTeamHalt(plan TeamPlanState, halt TeamPlanHalt) error {
	if domain.ValidateID(halt.GoalID) != nil || domain.ValidateID(halt.NodeID) != nil ||
		plan.Revision.GoalId != halt.GoalID || plan.FactDigest != halt.PlanFactDigest {
		return ErrTeamPlanConflict
	}
	switch halt.Stage {
	case "prepare", "materialize", "start", "inspect":
	default:
		return ErrTeamPlanConflict
	}
	for _, node := range plan.Materializations {
		if node.NodeID == halt.NodeID {
			return nil
		}
	}
	return ErrTeamPlanConflict
}

func (s *DurableStore) HaltTeamPlan(ctx context.Context, verifier CurrentApprovedTeamVerifier, owner ControlOwnerAcquisition, approval TeamPlanApproval, halt TeamPlanHalt) (TeamPlanHalt, error) {
	if ctx == nil || verifier == nil || owner.Validate() != nil || halt.FactDigest != "" {
		return TeamPlanHalt{}, ErrTeamPlanConflict
	}
	var result TeamPlanHalt
	err := withCurrentOwnerLock(ctx, teamApprovalOwnerVerifier{verifier, approval}, owner, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		projection := newAuthorityProjection()
		return s.transact(projection, func() error {
			ownerKey, _ := owner.Scope.key()
			current, found := projection.controlOwners[ownerKey]
			if !found || current.Acquisition != owner {
				return ErrControlOwnerNotCurrent
			}
			key := teamPlanKey(owner.Scope, halt.GoalID)
			plan, found := projection.teamPlans[key]
			if !found || plan.Approval != approval || validateTeamHalt(plan, halt) != nil {
				return ErrTeamPlanConflict
			}
			if old, found := projection.teamHalts[key]; found {
				comparison := old
				comparison.FactDigest = ""
				if comparison != halt {
					return ErrTeamPlanConflict
				}
				result = old
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			fact := &teamHaltFact{ProtocolRevision: teamHaltProtocol, FactType: teamHaltFactType, Sequence: s.nextSequence, Scope: owner.Scope, OwnerFactDigest: current.FactDigest, Halt: halt}
			if err := s.appendLine(fact, func() string { return fact.Digest }, func(digest string) { fact.Digest = digest }); err != nil {
				return err
			}
			s.nextSequence++
			halt.FactDigest = fact.Digest
			projection.teamHalts[key] = halt
			result = halt
			return nil
		})
	})
	return result, err
}

func (s *DurableStore) ReadTeamPlanHalt(scope ControlOwnerScope, goalID string) (halt TeamPlanHalt, found bool, err error) {
	if scope.Validate() != nil || domain.ValidateID(goalID) != nil {
		return halt, false, ErrTeamPlanConflict
	}
	projection := newAuthorityProjection()
	err = s.transact(projection, func() error { halt, found = projection.teamHalts[teamPlanKey(scope, goalID)]; return nil })
	return
}

func applyTeamHaltLine(line []byte, in *Ingress, sequence int64) error {
	if len(line) > 4096 {
		return ErrTeamPlanConflict
	}
	var fact teamHaltFact
	if decodeTeamRecord(line, &fact) != nil || fact.ProtocolRevision != teamHaltProtocol || fact.FactType != teamHaltFactType || fact.Sequence != sequence || fact.Halt.FactDigest != "" {
		return ErrTeamPlanConflict
	}
	digest := fact.Digest
	fact.Digest = ""
	if requireDigest("team halt", digest) != nil || canonicalDigestOrEmpty(fact) != digest {
		return ErrTeamPlanConflict
	}
	ownerKey, _ := fact.Scope.key()
	owner, found := in.controlOwners[ownerKey]
	if !found || owner.FactDigest != fact.OwnerFactDigest {
		return ErrControlOwnerNotCurrent
	}
	key := teamPlanKey(fact.Scope, fact.Halt.GoalID)
	plan, found := in.teamPlans[key]
	if !found || validateTeamHalt(plan, fact.Halt) != nil {
		return ErrTeamPlanConflict
	}
	if _, found := in.teamHalts[key]; found {
		return ErrTeamPlanConflict
	}
	fact.Halt.FactDigest = digest
	in.teamHalts[key] = fact.Halt
	return nil
}
