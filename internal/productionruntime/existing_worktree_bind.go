package productionruntime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// existingWorktreeFrozenInputs is the closed struct whose canonical-JSON
// sha256 is ExistingWorktreeBindingV1.FrozenInputsDigest (ADR 0070). BaseSHA
// and WorktreePath are bound separately in ExistingWorktreeBindRequestV1 and
// are deliberately not part of this digest.
type existingWorktreeFrozenInputs struct {
	SpecDigest       string `json:"specDigest"`
	PolicyDigest     string `json:"policyDigest"`
	CapabilityDigest string `json:"capabilityDigest"`
}

// existingWorktreeFrozenInputsDigest derives the path B frozen-inputs digest
// from the READY Run's three frozen launch-input digests. It is the sha256 of
// the canonical JSON of the closed struct above; it never accepts caller
// digest-only echoes and never reuses ReservationKeyDigest.
func existingWorktreeFrozenInputsDigest(specDigest, policyDigest, capabilityDigest string) (string, error) {
	raw, err := json.Marshal(existingWorktreeFrozenInputs{SpecDigest: specDigest, PolicyDigest: policyDigest, CapabilityDigest: capabilityDigest})
	if err != nil {
		return "", err
	}
	canonicalRaw, err := canonical.JSON(raw)
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(canonicalRaw), nil
}

// existingWorktreeCurrentVerifier is the per-bind Core-side authority
// provider for path B. It is constructed inside PrepareRunStart after
// BindOwnerToAttempt, capturing the immutable expected current derived from
// the durable READY projection and the current owner/Attempt state. The
// RunAuthority RLock is held by the caller for the DescriptorBoundRunV1
// lifetime; this verifier therefore never re-reads the Run journal. Inside
// the callback it re-verifies the exact current owner fact digest and the
// exact current Attempt head/revision, then hands the held descriptor graph
// to the RB1 session.
type existingWorktreeCurrentVerifier struct {
	ledger   *CompositionLedger
	scope    resultingress.ControlOwnerScope
	expected allocationcontrol.ExistingWorktreeCurrentAuthorityV1
	graph    allocationcontrol.ExistingWorktreeDescriptorGraphV1
}

var _ resultingress.CurrentExistingWorktreeAuthorityVerifier = (*existingWorktreeCurrentVerifier)(nil)

func (verifier *existingWorktreeCurrentVerifier) WithCurrentExistingWorktreeBind(ctx context.Context, check resultingress.ExistingWorktreeBindAuthorityCheck, callback func(allocationcontrol.ExistingWorktreeCurrentAuthorityV1, allocationcontrol.ExistingWorktreeDescriptorGraphV1) error) error {
	if verifier == nil || verifier.ledger == nil || callback == nil || verifier.scope.Validate() != nil {
		return resultingress.ErrExistingWorktreeAuthorityConflict
	}
	if err := ctx.Err(); err != nil {
		return resultingress.ErrExistingWorktreeAuthorityConflict
	}
	// Re-verify the exact current owner fact digest bound into the binding.
	// This reads only the ResultIngress ledger; it never re-reads the Run
	// journal under the RunAuthority RLock.
	ownerState, found, err := verifier.ledger.ingress.OpenOwner(verifier.scope)
	if err != nil || !found || ownerState.FactDigest != verifier.expected.RepositoryOwnerDigest {
		return resultingress.ErrExistingWorktreeAuthorityConflict
	}
	// Re-verify the exact current Attempt head/revision derived before the
	// RunAuthority was opened. Any drift fails closed without appending.
	current, attemptFound, err := verifier.ledger.ingress.AttemptState(check.Identity)
	if err != nil || !attemptFound || current.HeadDigest != verifier.expected.AttemptAuthorityHeadDigest || current.Revision != verifier.expected.ExpectedAttemptSequence {
		return resultingress.ErrExistingWorktreeAuthorityConflict
	}
	return callback(verifier.expected, verifier.graph)
}

func (verifier *existingWorktreeCurrentVerifier) WithCurrentExistingWorktreeRelease(ctx context.Context, check resultingress.ExistingWorktreeReleaseAuthorityCheck, callback func(allocationcontrol.ExistingWorktreeCurrentAuthorityV1, allocationcontrol.ExistingWorktreeDescriptorGraphV1) error) error {
	if verifier == nil || verifier.ledger == nil || callback == nil || verifier.scope.Validate() != nil || check.Request.Validate() != nil {
		return resultingress.ErrExistingWorktreeAuthorityConflict
	}
	owner, found, err := verifier.ledger.ingress.OpenOwner(verifier.scope)
	if err != nil || !found {
		return resultingress.ErrExistingWorktreeAuthorityConflict
	}
	attempt, found, err := verifier.ledger.ingress.AttemptState(check.Identity)
	if err != nil || !found || attempt.Identity != check.Identity || attempt.Owner.OwnerEpoch != owner.Acquisition.OwnerEpoch || attempt.Owner.ControlOwnerAcquiredFactDigest != owner.FactDigest || attempt.ProcessTerminalDigest != check.Request.ProcessTerminalFactDigest || attempt.TerminalizationID != check.Request.TerminalizationID || attempt.CleanupBindingDigest != check.Request.CleanupBindingDigest || attempt.AllocationTerminalDigest != "" || attempt.ExistingWorktreeReleaseReceiptFactDigest != "" {
		return resultingress.ErrExistingWorktreeAuthorityConflict
	}
	replayRequest, _, replayFound, replayComplete, err := verifier.ledger.ingress.CurrentExistingWorktreeRelease(check.Identity)
	if err != nil || replayComplete || replayFound && replayRequest != check.Request || !replayFound && attempt.HeadDigest != check.Request.AttemptAuthorityHeadDigest {
		return resultingress.ErrExistingWorktreeAuthorityConflict
	}
	binding := check.Request.Binding
	current := allocationcontrol.ExistingWorktreeCurrentAuthorityV1{
		AuthorityNamespaceID: binding.AuthorityNamespaceID, RepositoryOwnerDigest: binding.RepositoryOwnerDigest,
		TaskID: binding.TaskID, RunID: binding.RunID, RunAuthorityHeadDigest: check.Request.RunAuthorityHeadDigest,
		AttemptID: binding.AttemptID, AttemptAuthorityHeadDigest: attempt.HeadDigest,
		ReservationFactDigest: binding.ReservationFactDigest, AttemptOpenedFactDigest: binding.AttemptOpenedFactDigest,
		AllocationID: binding.AllocationID, LeaseID: binding.LeaseID, Generation: binding.Generation,
		FencingTokenDigest: binding.FencingTokenDigest, FrozenInputsDigest: binding.FrozenInputsDigest,
		ExpectedAttemptSequence: binding.ExpectedAttemptSequence,
		TerminalizationID:       attempt.TerminalizationID, TerminalAttemptHeadDigest: check.Request.AttemptAuthorityHeadDigest,
		CleanupBindingDigest: attempt.CleanupBindingDigest, ProcessTerminalFactDigest: attempt.ProcessTerminalDigest,
		CleanupDisposition: check.Request.CleanupDisposition,
	}
	return callback(current, verifier.graph)
}

// bindExistingWorktree drives the RB1 existing-worktree bind closed-union
// (bind-intent → bind-receipt) for path B. The Run descriptor is opened from
// the held Run lease via runstore.DupRunDirectory and bound through
// allocationcontrol.NewDescriptorBoundRunV1; the target identity is observed
// from the held target descriptor, never from a pathname. The closure is not
// re-sealed to a staging directory: path B keeps the target worktree as the
// agent working directory.
func (l *CompositionLedger) bindExistingWorktree(ctx context.Context, acquisition resultingress.ControlOwnerAcquisition, ready resultingress.ReadyRunAuthority, reservation resultingress.AttemptReservationState, identity resultingress.AttemptIdentity, bound resultingress.AttemptAuthorityState) error {
	frozenInputsDigest, err := existingWorktreeFrozenInputsDigest(ready.SpecDigest, ready.PolicyDigest, ready.CapabilityDigest)
	if err != nil {
		return err
	}
	targetIdentity, err := allocationcontrol.ObserveHeldDirectoryIdentity(l.existingWorktreeTarget)
	if err != nil {
		return err
	}
	ownerState, found, err := l.ingress.OpenOwner(acquisition.Scope)
	if err != nil || !found {
		return application.NewError("prepare-run-start", application.ReasonOwnerNotCurrent)
	}
	namespaceDigest, err := l.namespace.Digest()
	if err != nil {
		return err
	}
	binding := allocationcontrol.ExistingWorktreeBindingV1{
		AuthorityNamespaceID:    namespaceDigest,
		RepositoryOwnerDigest:   ownerState.FactDigest,
		TaskID:                  identity.TaskID,
		RunID:                   identity.RunID,
		AttemptID:               identity.AttemptID,
		ReservationFactDigest:   reservation.ReservationFactDigest,
		AttemptOpenedFactDigest: bound.OpenedDigest,
		AllocationID:            identity.AllocationID,
		LeaseID:                 identity.LeaseID,
		Generation:              identity.DispatchGeneration,
		FencingTokenDigest:      identity.FencingTokenDigest,
		FrozenInputsDigest:      frozenInputsDigest,
		ExpectedAttemptSequence: bound.Revision,
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	expected := allocationcontrol.ExistingWorktreeCurrentAuthorityV1{
		AuthorityNamespaceID:       namespaceDigest,
		RepositoryOwnerDigest:      ownerState.FactDigest,
		TaskID:                     identity.TaskID,
		RunID:                      identity.RunID,
		RunAuthorityHeadDigest:     ready.ReadyAuthorityHead,
		AttemptID:                  identity.AttemptID,
		AttemptAuthorityHeadDigest: bound.HeadDigest,
		ReservationFactDigest:      reservation.ReservationFactDigest,
		AttemptOpenedFactDigest:    bound.OpenedDigest,
		AllocationID:               identity.AllocationID,
		LeaseID:                    identity.LeaseID,
		Generation:                 identity.DispatchGeneration,
		FencingTokenDigest:         identity.FencingTokenDigest,
		FrozenInputsDigest:         frozenInputsDigest,
		ExpectedAttemptSequence:    bound.Revision,
		WorktreePath:               ready.WorktreePath,
		ExpectedWorktreeIdentity:   targetIdentity,
		ExpectedBaseSHA:            ready.BaseSHA,
	}
	// Open the RunAuthority (RLock on the lease guard) only for the
	// DescriptorBoundRunV1 lifetime. The verifier callback runs under this
	// RLock and must not re-read the Run journal; it uses the immutable
	// expected current derived above.
	runAuthority, err := runstore.DupRunDirectory(l.runLease)
	if err != nil {
		return err
	}
	defer func() { _ = runAuthority.Close() }()
	if runAuthority.File == nil {
		return application.NewError("prepare-run-start", application.ReasonOwnerUnavailable)
	}
	run, err := allocationcontrol.NewDescriptorBoundRunV1(identity.RunID, runAuthority.File, ready.ReadyAuthorityHead)
	if err != nil {
		return err
	}
	request := allocationcontrol.ExistingWorktreeBindRequestV1{
		Binding:                  binding,
		WorktreePath:             ready.WorktreePath,
		ExpectedWorktreeIdentity: targetIdentity,
		ExpectedBaseSHA:          ready.BaseSHA,
		RunDirectoryIdentity:     run.DirectoryIdentity,
		RunAuthorityHeadDigest:   ready.ReadyAuthorityHead,
	}
	if err := request.Seal(); err != nil {
		return err
	}
	verifier := &existingWorktreeCurrentVerifier{ledger: l, scope: acquisition.Scope, expected: expected, graph: l.existingWorktreeGraph}
	authority, err := resultingress.NewExistingWorktreeAuthority(l.ingress, verifier)
	if err != nil {
		return err
	}
	controller, err := allocationcontrol.NewExistingWorktreeController(authority)
	if err != nil {
		return err
	}
	if _, err := controller.Bind(ctx, run, request); err != nil {
		return fmt.Errorf("composition: existing-worktree bind: %w", err)
	}
	return nil
}
