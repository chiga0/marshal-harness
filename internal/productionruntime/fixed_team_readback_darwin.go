//go:build darwin && arm64

package productionruntime

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/goal"
)

// VerifyInitialTeamReadback is read-only. Callers must first complete fixed
// peer authentication/post-check. No path refresh or mutation is authorized:
// initial approval appends to an existing RB1 file and creates no directories.
// nil expected means the server reported absence, which must also be proved.
func (authority *FixedEndpointAuthority) VerifyInitialTeamReadback(ctx context.Context, request application.ApproveInitialTeamRequest, expected *application.InitialTeamApprovalProjection) error {
	if authority == nil || ctx == nil || ctx.Err() != nil {
		return ErrFixedDeliveryConflict
	}
	frozen, digest, err := request.Frozen()
	if err != nil {
		return ErrFixedDeliveryConflict
	}
	var inputs goal.TeamInputs
	if json.Unmarshal(frozen.Inputs, &inputs) != nil || inputs.Spec.Validate() != nil || (expected != nil && expected.Validate() != nil) {
		return ErrFixedDeliveryConflict
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.closed || authority.client == nil || authority.client.ingress == nil || authority.control == nil {
		return ErrFixedDeliveryConflict
	}
	client := authority.client
	if !inputs.Spec.AuthorityNamespaceId.Equal(client.scope.AuthorityNamespaceID) || inputs.Spec.Repository != client.root.repositoryPath {
		return ErrFixedDeliveryConflict
	}
	current, found, err := client.ingress.OpenOwner(client.scope)
	if err != nil || !found || current.Acquisition != authority.snapshot.Acquisition || current.FactDigest != authority.snapshot.OwnerFactDigest || validateFixedServerRoot(client.root, len(client.root.nodes)) != nil {
		return ErrFixedDeliveryConflict
	}
	plan, exists, err := client.ingress.ReadTeamPlan(client.scope, inputs.Spec.GoalId)
	if err != nil || exists != (expected != nil) {
		return ErrFixedDeliveryConflict
	}
	if exists && (plan.Approval.InputsDigest != frozen.InputsDigest || plan.Approval.RequestDigest != digest || plan.Approval.ExpectedHead != frozen.ExpectedHead || !bytes.Equal(plan.Inputs, frozen.Inputs) || teamApprovalProjection(plan) != *expected) {
		return ErrFixedDeliveryConflict
	}
	after, afterFound, err := client.ingress.OpenOwner(client.scope)
	if err != nil || !afterFound || after != current || ctx.Err() != nil || validateFixedServerRoot(client.root, len(client.root.nodes)) != nil {
		return ErrFixedDeliveryConflict
	}
	return nil
}

func (authority *FixedEndpointAuthority) VerifyInitialTeamOutcomeReadback(ctx context.Context, approval *application.InitialTeamApprovalProjection, expected *application.InitialTeamOutcomeProjection) error {
	if authority == nil || ctx == nil || ctx.Err() != nil {
		return ErrFixedDeliveryConflict
	}
	if approval == nil {
		if expected != nil {
			return ErrFixedDeliveryConflict
		}
		return nil
	}
	if approval.Validate() != nil || (expected != nil && expected.Validate() != nil) {
		return ErrFixedDeliveryConflict
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.closed || authority.client == nil || authority.client.ingress == nil || authority.control == nil {
		return ErrFixedDeliveryConflict
	}
	client := authority.client
	current, found, err := client.ingress.OpenOwner(client.scope)
	if err != nil || !found || current.Acquisition != authority.snapshot.Acquisition || current.FactDigest != authority.snapshot.OwnerFactDigest || validateFixedServerRoot(client.root, len(client.root.nodes)) != nil {
		return ErrFixedDeliveryConflict
	}
	plan, found, err := client.ingress.ReadTeamPlan(client.scope, approval.GoalID)
	if err != nil || !found || teamApprovalProjection(plan) != *approval {
		return ErrFixedDeliveryConflict
	}
	value, exists, err := client.ingress.ReadTeamOutcome(client.scope, approval.GoalID)
	if err != nil || exists != (expected != nil) {
		return ErrFixedDeliveryConflict
	}
	if exists {
		actual, err := projectTeamOutcome(value)
		if err != nil || actual != *expected || actual.PlanFactDigest != approval.FactDigest {
			return ErrFixedDeliveryConflict
		}
	}
	after, found, err := client.ingress.OpenOwner(client.scope)
	if err != nil || !found || after != current || ctx.Err() != nil || validateFixedServerRoot(client.root, len(client.root.nodes)) != nil {
		return ErrFixedDeliveryConflict
	}
	return nil
}
