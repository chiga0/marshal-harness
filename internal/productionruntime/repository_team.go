package productionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// ApproveInitialTeam is a privileged application seam. It does not authenticate
// a transport on its own. The authenticated team route's input
// adapter must supply the authenticated operator request, not Worker output.
// Approval commits obligations only; Run creation is a later reconciliation.
func (session *RepositorySession) ApproveInitialTeam(ctx context.Context, request application.ApproveInitialTeamRequest) (application.InitialTeamApprovalProjection, error) {
	const operation = "approve-initial-team"
	if ctx == nil {
		return application.InitialTeamApprovalProjection{}, application.NewError(operation, application.ReasonInvalidRequest)
	}
	frozen, requestDigest, err := request.Frozen()
	if err != nil {
		return application.InitialTeamApprovalProjection{}, err
	}
	deadline, _ := time.Parse(time.RFC3339Nano, frozen.Deadline)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	borrow, err := session.borrow()
	if err != nil {
		return application.InitialTeamApprovalProjection{}, err
	}
	defer borrow.Close()
	if session.teamInputPreflight == nil {
		return application.InitialTeamApprovalProjection{}, application.NewError(operation, application.ReasonCompositionIncomplete)
	}
	if err := ctx.Err(); err != nil {
		return application.InitialTeamApprovalProjection{}, err
	}
	// Full schema/Policy validation is outside the repository owner lock, but
	// inside the session lifetime. Give the injected pure validator a copy so
	// even an erroneous validator cannot replace approved bytes in the commit.
	validationCopy := bytes.Clone(frozen.Inputs)
	if session.teamInputPreflight(validationCopy) != nil || canonical.DigestBytes(validationCopy) != frozen.InputsDigest {
		return application.InitialTeamApprovalProjection{}, application.NewError(operation, application.ReasonInvalidRequest)
	}
	approval := resultingress.TeamPlanApproval{InputsDigest: frozen.InputsDigest, RequestDigest: requestDigest, ExpectedHead: frozen.ExpectedHead}
	verifier := repositoryApprovedTeamVerifier{session: session, approval: approval}
	plan, err := session.ingress.AcceptInitialTeamPlan(ctx, verifier, session.acquisition, approval, frozen.Inputs)
	if err != nil {
		if errors.Is(err, resultingress.ErrTeamPlanConflict) {
			return application.InitialTeamApprovalProjection{}, application.NewError(operation, application.ReasonAuthorityConflict)
		}
		return application.InitialTeamApprovalProjection{}, err
	}
	return teamApprovalProjection(plan), nil
}

func teamApprovalProjection(plan resultingress.TeamPlanState) application.InitialTeamApprovalProjection {
	return application.InitialTeamApprovalProjection{
		GoalID: plan.Revision.GoalId, PlanRevision: plan.Revision.PlanRevision,
		InputsDigest: plan.Approval.InputsDigest, RequestDigest: plan.Approval.RequestDigest,
		FactDigest: plan.FactDigest, ObligationCount: len(plan.Materializations),
	}
}

// PrepareInitialTeamRun is a privileged controller seam, not a transport
// endpoint. It reads the approved fact before any probe, then freezes exactly
// the immutable composition's planning result. A cold/exact replay reads the
// original preparation without probing or refreshing its timestamp.
// Run creation/recovery and Goal-to-Run approval are NOT performed here.
func (session *RepositorySession) PrepareInitialTeamRun(ctx context.Context, request application.ApproveInitialTeamRequest, nodeID string) (resultingress.TeamRunCreationState, error) {
	const operation = "prepare-initial-team-run"
	if ctx == nil || domain.ValidateID(nodeID) != nil {
		return resultingress.TeamRunCreationState{}, application.NewError(operation, application.ReasonAuthorityConflict)
	}
	frozen, digest, err := request.Frozen()
	if err != nil {
		return resultingress.TeamRunCreationState{}, err
	}
	borrow, err := session.borrow()
	if err != nil {
		return resultingress.TeamRunCreationState{}, err
	}
	defer borrow.Close()
	approval := resultingress.TeamPlanApproval{InputsDigest: frozen.InputsDigest, RequestDigest: digest, ExpectedHead: frozen.ExpectedHead}
	return session.prepareApprovedTeamRun(ctx, frozen.Inputs, approval, nodeID)
}

// The caller borrows the session lifetime. Both the original-request path and
// the resident controller use this same preflight, current-ledger check and
// creation freeze; no transport request is reconstructed during cold recovery.
func (session *RepositorySession) prepareApprovedTeamRun(ctx context.Context, raw []byte, approval resultingress.TeamPlanApproval, nodeID string) (resultingress.TeamRunCreationState, error) {
	const operation = "prepare-initial-team-run"
	fail := func() (resultingress.TeamRunCreationState, error) {
		return resultingress.TeamRunCreationState{}, application.NewError(operation, application.ReasonAuthorityConflict)
	}
	if session.teamInputPreflight == nil || session.teamRunPreparer == nil {
		return resultingress.TeamRunCreationState{}, application.NewError(operation, application.ReasonCompositionIncomplete)
	}
	if err := ctx.Err(); err != nil {
		return resultingress.TeamRunCreationState{}, err
	}
	validation := bytes.Clone(raw)
	if session.teamInputPreflight(validation) != nil || canonical.DigestBytes(validation) != approval.InputsDigest {
		return fail()
	}
	var inputs goal.TeamInputs
	if json.Unmarshal(raw, &inputs) != nil || inputs.Spec.Validate() != nil ||
		!inputs.Spec.AuthorityNamespaceId.Equal(session.acquisition.Scope.AuthorityNamespaceID) {
		return fail()
	}
	var node goal.TeamNodeInputs
	for _, candidate := range inputs.Nodes {
		if candidate.NodeID == nodeID {
			node = candidate
		}
	}
	if node.Role != "implement" {
		return fail()
	}
	for _, edge := range inputs.Proposal.Edges {
		if edge.To == nodeID {
			return fail()
		}
	}
	_, runID, err := goal.TeamNodeIDs(inputs.Proposal, nodeID)
	if err != nil {
		return fail()
	}
	verifier := repositoryApprovedTeamVerifier{session: session, approval: approval}
	var plan resultingress.TeamPlanState
	var existing resultingress.TeamRunCreationState
	var found bool
	err = verifier.WithCurrentApprovedTeam(ctx, session.acquisition, approval, func() error {
		var exists bool
		var readErr error
		plan, exists, readErr = session.ingress.ReadTeamPlan(session.acquisition.Scope, inputs.Spec.GoalId)
		if readErr != nil {
			return readErr
		}
		if !exists || plan.Approval != approval || !bytes.Equal(plan.Inputs, raw) {
			return application.NewError(operation, application.ReasonAuthorityConflict)
		}
		existing, found, readErr = session.ingress.ReadTeamRunCreation(session.acquisition.Scope, inputs.Spec.GoalId, nodeID)
		return readErr
	})
	if err != nil {
		return resultingress.TeamRunCreationState{}, err
	}
	if found {
		if existing.PlanFactDigest != plan.FactDigest || existing.RunID != runID {
			return fail()
		}
		return existing, nil
	}
	// No repository-owner/RB1 lock is held during validation subprocesses or
	// the one fixed Pi probe. The commit rechecks current owner and exact plan.
	prepared, err := session.teamRunPreparer(ctx, bytes.Clone(node.Task), bytes.Clone(node.Policy), runID)
	if err != nil {
		return resultingress.TeamRunCreationState{}, err
	}
	return session.ingress.FreezeInitialTeamRun(ctx, verifier, session.acquisition, approval, inputs.Spec.GoalId, nodeID, plan.FactDigest, prepared)
}

// MaterializeApprovedInitialTeamRun is the resident controller's privileged
// continuation of a committed plan, including approval-before-freeze crashes.
// IDs/digest are selectors, never authority: the exact plan is read under the
// current owner before any preflight/probe, and every mutation rechecks it.
// It does not accept client templates, reconstruct HTTP deadlines, Start a
// Worker, retry a failure, or authorize integration before its dependencies.
func (session *RepositorySession) MaterializeApprovedInitialTeamRun(ctx context.Context, goalID, nodeID, planFactDigest string) (domain.RunState, error) {
	const operation = "materialize-approved-team-run"
	if ctx == nil || domain.ValidateID(goalID) != nil || domain.ValidateID(nodeID) != nil || planFactDigest == "" {
		return domain.RunState{}, application.NewError(operation, application.ReasonInvalidRequest)
	}
	borrow, err := session.borrow()
	if err != nil {
		return domain.RunState{}, err
	}
	defer borrow.Close()
	if session.teamRunMaterializer == nil {
		return domain.RunState{}, application.NewError(operation, application.ReasonCompositionIncomplete)
	}
	reader := repositoryApprovedTeamVerifier{session: session}
	var plan resultingress.TeamPlanState
	err = reader.WithCurrentApprovedTeam(ctx, session.acquisition, resultingress.TeamPlanApproval{}, func() error {
		var found bool
		var err error
		plan, found, err = session.ingress.ReadTeamPlan(session.acquisition.Scope, goalID)
		if err != nil {
			return err
		}
		if !found || plan.FactDigest != planFactDigest {
			return application.NewError(operation, application.ReasonAuthorityConflict)
		}
		return nil
	})
	if err != nil {
		return domain.RunState{}, err
	}
	creation, err := session.prepareApprovedTeamRun(ctx, plan.Inputs, plan.Approval, nodeID)
	if err != nil {
		return domain.RunState{}, err
	}
	return session.materializeTeamCreation(ctx, plan.Approval, plan.Inputs, creation)
}

// MaterializeInitialTeamRun is a privileged controller seam, not an HTTP
// endpoint or plan approval. It creates/reuses READY from the original fact;
// it neither starts a Worker nor consumes another Goal reservation.
func (session *RepositorySession) MaterializeInitialTeamRun(ctx context.Context, request application.ApproveInitialTeamRequest, nodeID string) (domain.RunState, error) {
	const operation = "materialize-initial-team-run"
	if ctx == nil || session == nil || session.teamRunMaterializer == nil {
		return domain.RunState{}, application.NewError(operation, application.ReasonCompositionIncomplete)
	}
	creation, err := session.PrepareInitialTeamRun(ctx, request, nodeID)
	if err != nil {
		return domain.RunState{}, err
	}
	borrow, err := session.borrow()
	if err != nil {
		return domain.RunState{}, err
	}
	defer borrow.Close()
	frozen, digest, err := request.Frozen()
	if err != nil {
		return domain.RunState{}, err
	}
	approval := resultingress.TeamPlanApproval{InputsDigest: frozen.InputsDigest, RequestDigest: digest, ExpectedHead: frozen.ExpectedHead}
	return session.materializeTeamCreation(ctx, approval, frozen.Inputs, creation)
}

// The session lifetime must already be borrowed. Startup uses the approval
// stored in RB1, not a fabricated original transport request/deadline.
func (session *RepositorySession) materializeTeamCreation(ctx context.Context, approval resultingress.TeamPlanApproval, inputs []byte, creation resultingress.TeamRunCreationState) (domain.RunState, error) {
	const operation = "materialize-initial-team-run"
	verifier := repositoryApprovedTeamVerifier{session: session, approval: approval}
	guard := func(operationContext context.Context, fn func() error) error {
		return verifier.WithCurrentApprovedTeam(operationContext, session.acquisition, approval, func() error {
			plan, found, err := session.ingress.ReadTeamPlan(session.acquisition.Scope, creation.GoalID)
			if err != nil {
				return err
			}
			if !found || plan.Approval != approval || plan.FactDigest != creation.PlanFactDigest || !bytes.Equal(plan.Inputs, inputs) {
				return application.NewError(operation, application.ReasonAuthorityConflict)
			}
			current, found, err := session.ingress.ReadTeamRunCreation(session.acquisition.Scope, creation.GoalID, creation.NodeID)
			if err != nil {
				return err
			}
			if !found || current.FactDigest != creation.FactDigest || current.RunID != creation.RunID || !bytes.Equal(current.Inputs, creation.Inputs) {
				return application.NewError(operation, application.ReasonAuthorityConflict)
			}
			return fn()
		})
	}
	if err := guard(ctx, func() error { return nil }); err != nil {
		return domain.RunState{}, err
	}
	state, err := session.teamRunMaterializer(ctx, bytes.Clone(creation.Inputs), guard)
	if err != nil {
		return domain.RunState{}, err
	}
	var prepared struct {
		BaseSHA    string          `json:"baseSha"`
		PreparedAt time.Time       `json:"preparedAt"`
		Task       json.RawMessage `json:"task"`
		Policy     json.RawMessage `json:"policy"`
		Capability json.RawMessage `json:"capability"`
	}
	var task struct {
		Metadata struct {
			ID string `json:"id"`
		} `json:"metadata"`
	}
	if json.Unmarshal(creation.Inputs, &prepared) != nil || json.Unmarshal(prepared.Task, &task) != nil {
		return domain.RunState{}, application.NewError(operation, application.ReasonAuthorityConflict)
	}
	// A factory return value is not proof of a committed Run. Read it back
	// through this session's held canonical state root under the same owner.
	err = guard(ctx, func() error {
		if state.RunID != creation.RunID || state.TaskID != task.Metadata.ID || state.State != domain.StateReady || state.Sequence != 2 ||
			state.BaseSHA != prepared.BaseSHA || !state.CreatedAt.Equal(prepared.PreparedAt) ||
			state.SpecDigest != canonical.DigestBytes(prepared.Task) || state.PolicyDigest != canonical.DigestBytes(prepared.Policy) || state.CapabilityDigest != canonical.DigestBytes(prepared.Capability) {
			return application.NewError(operation, application.ReasonAuthorityConflict)
		}
		lease, err := session.runs.AcquireExisting(creation.RunID)
		if err != nil {
			return err
		}
		defer lease.Release()
		for name, expected := range map[string][]byte{"task-spec.json": prepared.Task, "policy-snapshot.json": prepared.Policy, "capability-snapshot.json": prepared.Capability} {
			raw, err := runstore.ReadFileUnderLease(lease, int64(len(expected)+1), name)
			if err != nil || !bytes.Equal(raw, expected) {
				return application.NewError(operation, application.ReasonAuthorityConflict)
			}
		}
		actual, err := runstore.InspectUnderLease(lease)
		if err != nil {
			return err
		}
		want, err := json.Marshal(state)
		if err != nil {
			return err
		}
		got, err := json.Marshal(actual)
		if err != nil || !bytes.Equal(want, got) {
			return application.NewError(operation, application.ReasonAuthorityConflict)
		}
		return nil
	})
	if err != nil {
		return domain.RunState{}, err
	}
	return state, nil
}

// ReconcileInitialTeamApproval never appends or refreshes a deadline. A query
// after the original approval deadline may recover the exact committed fact.
func (session *RepositorySession) ReconcileInitialTeamApproval(ctx context.Context, request application.ApproveInitialTeamRequest) (application.InitialTeamApprovalProjection, bool, error) {
	if ctx == nil {
		return application.InitialTeamApprovalProjection{}, false, application.NewError("reconcile-team-approval", application.ReasonInvalidRequest)
	}
	frozen, digest, err := request.Frozen()
	if err != nil {
		return application.InitialTeamApprovalProjection{}, false, err
	}
	var inputs goal.TeamInputs
	if json.Unmarshal(frozen.Inputs, &inputs) != nil || inputs.Spec.Validate() != nil {
		return application.InitialTeamApprovalProjection{}, false, application.NewError("reconcile-team-approval", application.ReasonInvalidRequest)
	}
	borrow, err := session.borrow()
	if err != nil {
		return application.InitialTeamApprovalProjection{}, false, err
	}
	defer borrow.Close()
	if !inputs.Spec.AuthorityNamespaceId.Equal(session.acquisition.Scope.AuthorityNamespaceID) {
		return application.InitialTeamApprovalProjection{}, false, application.NewError("reconcile-team-approval", application.ReasonAuthorityConflict)
	}
	approval := resultingress.TeamPlanApproval{InputsDigest: frozen.InputsDigest, RequestDigest: digest, ExpectedHead: frozen.ExpectedHead}
	verifier := repositoryApprovedTeamVerifier{session: session, approval: approval}
	var result application.InitialTeamApprovalProjection
	var found bool
	err = verifier.WithCurrentApprovedTeam(ctx, session.acquisition, approval, func() error {
		plan, exists, err := session.ingress.ReadTeamPlan(session.acquisition.Scope, inputs.Spec.GoalId)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		if plan.Approval != approval || !bytes.Equal(plan.Inputs, frozen.Inputs) {
			return application.NewError("reconcile-team-approval", application.ReasonAuthorityConflict)
		}
		result, found = teamApprovalProjection(plan), true
		return result.Validate()
	})
	return result, found, err
}

// This verifier cannot be constructed by an input adapter or deserialized from
// a request. It holds the real claimed owner and rechecks both physical root
// and RB1 owner before invoking the ledger transaction, which rechecks again.
type repositoryApprovedTeamVerifier struct {
	session  *RepositorySession
	approval resultingress.TeamPlanApproval
}

func (v repositoryApprovedTeamVerifier) WithCurrentApprovedTeam(ctx context.Context, owner resultingress.ControlOwnerAcquisition, approval resultingress.TeamPlanApproval, fn func() error) error {
	if ctx == nil || v.session == nil || fn == nil || owner != v.session.acquisition || approval != v.approval {
		return application.NewError("approve-initial-team", application.ReasonInvalidRequest)
	}
	return v.session.owner.WithCurrentOwnerLock(ctx, owner, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := validateFixedServerRoot(v.session.fixedRoot, 5); err != nil {
			return err
		}
		current, found, err := v.session.ingress.OpenOwner(owner.Scope)
		if err != nil {
			return err
		}
		if !found || current.Acquisition != owner || current.FactDigest != v.session.ownerState.FactDigest {
			return application.NewError("approve-initial-team", application.ReasonOwnerNotCurrent)
		}
		return fn()
	})
}
