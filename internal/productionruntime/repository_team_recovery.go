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
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// RecoverInitialTeamCreations repairs only RB1-backed frozen creation work
// before the ordinary startup Run scan. It does not prepare new nodes, probe,
// approve, Start, extend a deadline or reconstruct a transport request.
func (session *RepositorySession) RecoverInitialTeamCreations(ctx context.Context) error {
	const operation = "recover-team-creations"
	if ctx == nil {
		return application.NewError(operation, application.ReasonInvalidRequest)
	}
	borrow, err := session.borrow()
	if err != nil {
		return err
	}
	defer borrow.Close()
	// An empty approval is used only to borrow the private current-owner read
	// guard. No approval/write operation accepts it; every mutation below is
	// guarded by the exact approval and creation read from this held ledger.
	reader := repositoryApprovedTeamVerifier{session: session}
	var obligations []resultingress.TeamCreationObligation
	err = reader.WithCurrentApprovedTeam(ctx, session.acquisition, resultingress.TeamPlanApproval{}, func() error {
		var readErr error
		obligations, readErr = session.ingress.ListTeamCreationObligations(session.acquisition.Scope)
		return readErr
	})
	if err != nil || len(obligations) == 0 {
		return err
	}
	if session.teamRunMaterializer == nil {
		return application.NewError(operation, application.ReasonCompositionIncomplete)
	}
	for _, obligation := range obligations {
		if err := session.ingress.RequireTaskNotStopped(session.acquisition.Scope, obligation.Creation.GoalID); errors.Is(err, resultingress.ErrTaskStopped) {
			continue
		} else if err != nil {
			return err
		}
		advanced := false
		// Serialize with current owner, not just the owner of the old creation
		// fact. Complete/advanced Runs must remain in the normal recovery path.
		verifier := repositoryApprovedTeamVerifier{session: session, approval: obligation.Plan.Approval}
		err := verifier.WithCurrentApprovedTeam(ctx, session.acquisition, obligation.Plan.Approval, func() error {
			plan, found, err := session.ingress.ReadTeamPlan(session.acquisition.Scope, obligation.Creation.GoalID)
			if err != nil {
				return err
			}
			if !found || plan.FactDigest != obligation.Plan.FactDigest || plan.Approval != obligation.Plan.Approval {
				return application.NewError(operation, application.ReasonAuthorityConflict)
			}
			current, found, err := session.ingress.ReadTeamRunCreation(session.acquisition.Scope, obligation.Creation.GoalID, obligation.Creation.NodeID)
			if err != nil {
				return err
			}
			if !found || current.FactDigest != obligation.Creation.FactDigest {
				return application.NewError(operation, application.ReasonAuthorityConflict)
			}
			advanced, err = session.teamCreationAlreadyAdvanced(ctx, current)
			return err
		})
		if err != nil {
			return err
		}
		if advanced {
			continue
		}
		if _, err := session.materializeTeamCreation(ctx, obligation.Plan.Approval, obligation.Plan.Inputs, obligation.Creation); err != nil {
			return err
		}
	}
	return nil
}

// Missing or at most two planning events may enter strict creation recovery.
// A later Run is skipped only after a complete authority read, exact frozen
// inputs and the original planning prefix. No read error means "already done".
func (session *RepositorySession) teamCreationAlreadyAdvanced(ctx context.Context, creation resultingress.TeamRunCreationState) (advanced bool, err error) {
	lease, err := session.runs.AcquireExisting(creation.RunID)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, lease.Release()) }()
	events, truncated, err := session.runs.ReadEventsUnderLease(lease)
	if truncated || (err != nil && !errors.Is(err, os.ErrNotExist)) {
		return false, application.NewError("recover-team-creation-journal", application.ReasonAuthorityConflict)
	}
	if len(events) <= 2 {
		return false, nil
	}
	if _, err := session.runs.ReadRunStartAuthorityUnderLease(ctx, lease); err != nil {
		return false, err
	}
	if events[0].Type != "planning.spec-accepted" || events[0].StateFrom != domain.StateCreated || events[0].StateTo != domain.StatePlanned ||
		events[1].Type != "planning.inputs-frozen" || events[1].StateFrom != domain.StatePlanned || events[1].StateTo != domain.StateReady ||
		events[0].AttemptID != "" || events[1].AttemptID != "" {
		return false, application.NewError("recover-team-creation-prefix", application.ReasonAuthorityConflict)
	}
	var frozen struct {
		BaseSHA    string          `json:"baseSha"`
		PreparedAt time.Time       `json:"preparedAt"`
		Task       json.RawMessage `json:"task"`
		Policy     json.RawMessage `json:"policy"`
		Capability json.RawMessage `json:"capability"`
	}
	if json.Unmarshal(creation.Inputs, &frozen) != nil {
		return false, application.NewError("recover-team-creation-inputs", application.ReasonAuthorityConflict)
	}
	state, err := runstore.InspectUnderLease(lease)
	var task domain.TaskSpec
	if err != nil || json.Unmarshal(frozen.Task, &task) != nil || state.RunID != creation.RunID || state.TaskID != task.Metadata.ID ||
		state.BaseSHA != frozen.BaseSHA || !state.CreatedAt.Equal(frozen.PreparedAt) ||
		!events[0].Timestamp.Equal(frozen.PreparedAt) || !events[1].Timestamp.Equal(frozen.PreparedAt) ||
		state.SpecDigest != canonical.DigestBytes(frozen.Task) || state.PolicyDigest != canonical.DigestBytes(frozen.Policy) || state.CapabilityDigest != canonical.DigestBytes(frozen.Capability) {
		return false, application.NewError("recover-team-creation-identity", application.ReasonAuthorityConflict)
	}
	for name, expected := range map[string][]byte{"task-spec.json": frozen.Task, "policy-snapshot.json": frozen.Policy, "capability-snapshot.json": frozen.Capability} {
		raw, err := runstore.ReadFileUnderLease(lease, int64(len(expected)+1), name)
		if err != nil || !bytes.Equal(raw, expected) {
			return false, application.NewError("recover-team-creation-inputs", application.ReasonAuthorityConflict)
		}
	}
	return true, nil
}
