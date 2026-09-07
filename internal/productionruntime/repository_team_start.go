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
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// RequireInitialTeamRunPlan derives the first implement Run's plan gate from
// the real approved plan and frozen creation. It writes no synthetic human
// ApprovalRecord and does not grant Start/publication on its own. A false,nil
// result means only "not a team member": the caller must apply the ordinary
// Run approval gate. Any team conflict is an error, never a fallback signal.
func (session *RepositorySession) RequireInitialTeamRunPlan(ctx context.Context, request application.StartRunRequest) (member bool, err error) {
	const operation = "require-team-run-plan"
	if ctx == nil || request.Validate() != nil {
		return false, application.NewError(operation, application.ReasonInvalidRequest)
	}
	borrow, err := session.borrow()
	if err != nil {
		return false, err
	}
	defer borrow.Close()
	reader := repositoryApprovedTeamVerifier{session: session}
	err = reader.WithCurrentApprovedTeam(ctx, session.acquisition, resultingress.TeamPlanApproval{}, func() error {
		obligation, found, err := session.ingress.ReadTeamRunObligation(session.acquisition.Scope, request.RunID)
		member = found
		if err != nil {
			return err
		}
		if member {
			if _, halted, err := session.ingress.ReadTeamPlanHalt(session.acquisition.Scope, obligation.Creation.GoalID); err != nil {
				return err
			} else if halted {
				return application.NewError(operation, application.ReasonRecoveryRequired)
			}
			return session.withCurrentCreationInputsUnderOwner(ctx, obligation.Creation, func() error { return session.requireInitialTeamReady(ctx, request, obligation.Creation) })
		}
		return nil
	})
	return member, err
}

// The caller holds current owner. The store's replay has already joined and
// validated the exact approved node/template/creation facts for this scope.
func (session *RepositorySession) requireInitialTeamReady(ctx context.Context, request application.StartRunRequest, creation resultingress.TeamRunCreationState) (err error) {
	fail := func() error { return application.NewError("require-team-ready", application.ReasonAuthorityConflict) }
	lease, err := session.runs.AcquireExisting(request.RunID)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lease.Release()) }()
	authority, err := session.runs.ReadRunStartAuthorityUnderLease(ctx, lease)
	if err != nil {
		return err
	}
	if authority.Run.State != domain.StateReady || authority.Run.Sequence != 2 || authority.Run.AttemptID != "" || authority.AttemptsUsed != 0 ||
		authority.Run.Sequence != request.ExpectedSequence || authority.Run.AuthorityHead != request.ExpectedAuthorityHead {
		return fail()
	}
	var frozen struct {
		BaseSHA    string          `json:"baseSha"`
		PreparedAt time.Time       `json:"preparedAt"`
		Task       json.RawMessage `json:"task"`
		Policy     json.RawMessage `json:"policy"`
		Capability json.RawMessage `json:"capability"`
	}
	if json.Unmarshal(creation.Inputs, &frozen) != nil {
		return fail()
	}
	state, err := runstore.InspectUnderLease(lease)
	var task domain.TaskSpec
	if err != nil || json.Unmarshal(frozen.Task, &task) != nil || state.RunID != creation.RunID || state.TaskID != task.Metadata.ID ||
		state.BaseSHA != frozen.BaseSHA || !state.CreatedAt.Equal(frozen.PreparedAt) ||
		state.SpecDigest != canonical.DigestBytes(frozen.Task) || state.PolicyDigest != canonical.DigestBytes(frozen.Policy) || state.CapabilityDigest != canonical.DigestBytes(frozen.Capability) {
		return fail()
	}
	for name, expected := range map[string][]byte{"task-spec.json": frozen.Task, "policy-snapshot.json": frozen.Policy, "capability-snapshot.json": frozen.Capability} {
		raw, err := runstore.ReadFileUnderLease(lease, int64(len(expected)+1), name)
		if err != nil || !bytes.Equal(raw, expected) {
			return fail()
		}
	}
	return nil
}
