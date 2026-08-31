package productionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/verification"
)

func (l *CompositionLedger) CollectRunResult(ctx context.Context, verifier resultingress.CurrentOwnerLockVerifier, acquisition resultingress.ControlOwnerAcquisition, runID string) (CollectedRunResult, error) {
	if l == nil || l.resultParser == nil || runID == "" {
		return CollectedRunResult{}, application.NewError("collect-run-result", application.ReasonInvalidRequest)
	}
	read, attempt, found, err := l.currentRunningAttempt(ctx)
	if err != nil || !found || read.Run.RunID != runID {
		return CollectedRunResult{}, application.NewError("collect-run-result", application.ReasonAuthorityConflict)
	}
	var lease dispatch.DispatchLease
	var capability authority.DispatchResultCapability
	if attempt.CommittedResultFactDigest != "" {
		// Terminal eligibility revokes the active capability. Recovery of an
		// already committed admission therefore reads the immutable original
		// claim response without reissuing or reactivating that capability.
		current, state, _, currentErr := l.leaseLedger.Current(attempt.Identity.LeaseID)
		exactTuple := currentErr == nil && current.RunId == attempt.Identity.RunID && current.AttemptId == attempt.Identity.AttemptID && current.AllocationId == attempt.Identity.AllocationID
		activeAdmissionGap := (state == dispatch.LeaseStateClaimed || state == dispatch.LeaseStateActive) && current.Generation == attempt.Identity.DispatchGeneration
		completedTerminal := state == dispatch.LeaseStateCompleted && current.Generation == attempt.Identity.DispatchGeneration+1 && current.CompletionReason == dispatch.CompletionReasonAttemptCompleted
		if !exactTuple || !activeAdmissionGap && !completedTerminal {
			return CollectedRunResult{}, application.NewError("collect-run-result", application.ReasonAuthorityConflict)
		}
		request, requestErr := l.reservedClaimRequest(attempt.ReservationFactDigest, attempt.Identity.TaskID, attempt.Identity.RunID, attempt.Identity.AttemptID, attempt.Identity.AllocationID, current.AckDeadlineAt, current.ExpiresAt)
		if requestErr != nil {
			return CollectedRunResult{}, requestErr
		}
		claimed, found, lookupErr := l.matcher.LookupReservedClaim(request)
		if lookupErr != nil || !found || claimed.Lease.LeaseId != attempt.Identity.LeaseID || claimed.Lease.Generation != attempt.Identity.DispatchGeneration {
			return CollectedRunResult{}, application.NewError("collect-run-result", application.ReasonAuthorityConflict)
		}
		lease = claimed.Lease
		capability = claimed.ResultCapability
	} else {
		var err error
		lease, err = l.ensureAttemptLease(attempt.ReservationFactDigest, attempt.Identity.TaskID, attempt.Identity.RunID, attempt.Identity.AttemptID, attempt.Identity.AllocationID)
		if err != nil {
			return CollectedRunResult{}, err
		}
		var ok bool
		capability, ok = l.resultCapabilities[lease.LeaseId]
		if !ok {
			return CollectedRunResult{}, application.NewError("collect-run-result", application.ReasonAuthorityConflict)
		}
	}
	if capability.Validate() != nil {
		return CollectedRunResult{}, application.NewError("collect-run-result", application.ReasonAuthorityConflict)
	}

	attemptDirectory, err := runstore.OpenOrCreateDirectoryUnderLease(l.runLease, "attempts", attempt.Identity.AttemptID)
	if err != nil {
		return CollectedRunResult{}, err
	}
	defer attemptDirectory.Close()

	var result domain.Record
	var collectOutcomeDigest string
	if attempt.CommittedResultFactDigest == "" {
		terminal, probeErr := processsupervisor.ExpectedProcessTerminal(attempt.ProcessStartedEvidence.Outcome.Process)
		if probeErr != nil {
			return CollectedRunResult{}, probeErr
		}
		if !terminal {
			return CollectedRunResult{}, ErrAttemptStillRunning
		}
		collected, collectErr := l.ingress.CollectPreparedExecution(ctx, verifier, acquisition, attempt.Identity)
		if collectErr != nil {
			return CollectedRunResult{}, collectErr
		}
		collectOutcomeDigest = collected.OutcomeFactDigest
		startedAt, parseErr := time.Parse(time.RFC3339Nano, attempt.ObservedAt)
		if parseErr != nil {
			return CollectedRunResult{}, parseErr
		}
		completedAt, parseErr := time.Parse(time.RFC3339Nano, collected.Transcript.Report.ObservedAt)
		if parseErr != nil {
			return CollectedRunResult{}, parseErr
		}
		if len(l.closure.Arguments) < 2 {
			return CollectedRunResult{}, application.NewError("collect-run-result", application.ReasonAuthorityConflict)
		}
		result, err = l.resultParser(ctx, AttemptResultInput{
			Transcript: collected.Transcript.Stdout, Worktree: read.WorktreePath,
			TaskID: read.Run.TaskID, RunID: read.Run.RunID, AttemptID: read.Run.AttemptID,
			Executable: l.closure.Arguments[1], Version: PiProviderVersion, StartedAt: startedAt, CompletedAt: completedAt,
			MaxOutputBytes: 16 << 20,
		})
		if err != nil {
			return CollectedRunResult{}, fmt.Errorf("collect-run-result: parse worker result: %w", err)
		}
		if result.Kind != domain.KindWorkerResult {
			return CollectedRunResult{}, application.NewError("collect-run-result", application.ReasonAuthorityConflict)
		}
		if err := runstore.WriteFileInDirectory(attemptDirectory, "worker-result.json", result.Data, 0o600); err != nil {
			return CollectedRunResult{}, err
		}
		var refreshed bool
		attempt, refreshed, err = l.ingress.AttemptState(attempt.Identity)
		if err != nil || !refreshed {
			return CollectedRunResult{}, err
		}
	} else {
		data, readErr := runstore.ReadFileInDirectory(attemptDirectory, "worker-result.json", 16<<20)
		if readErr != nil {
			return CollectedRunResult{}, readErr
		}
		result = domain.Record{Kind: domain.KindWorkerResult, Data: data}
	}

	observation, observationBinding, err := l.resultObservation(ctx, attemptDirectory, read, attempt.CommittedResultFactDigest != "", attempt.CommittedResultObservation)
	if err != nil {
		return CollectedRunResult{}, err
	}
	localDispatchDigest, localIngressDigest, err := l.localIngressObservationDigests(attemptDirectory, attempt.CommittedResultFactDigest != "")
	if err != nil {
		return CollectedRunResult{}, err
	}

	envelopeDigest := canonical.DigestBytes(result.Data)
	drc, binding, err := l.resultAdmissionAuthority(attempt, lease, capability, envelopeDigest)
	if err != nil {
		return CollectedRunResult{}, err
	}
	ingress, err := resultingress.NewDurableIngress(binding, l.ingress)
	if err != nil {
		return CollectedRunResult{}, err
	}
	envelope := resultingress.ResultEnvelope{Kind: resultingress.KindWorkerResult, ResultDigest: envelopeDigest, Sequence: 1}
	var admission resultingress.AdmissionFact
	if attempt.CommittedResultFactDigest == "" {
		if collectOutcomeDigest == "" {
			return CollectedRunResult{}, application.NewError("collect-run-result", application.ReasonAuthorityConflict)
		}
		admission, err = ingress.AdmitWithSupervisorCollectOutcomeAndObservation(ctx, drc, envelope, collectOutcomeDigest, observationBinding)
	} else {
		var replayed bool
		admission, replayed, err = ingress.ReplayCommitted(drc, envelope)
		if err == nil && (!replayed || admission.FactDigest != attempt.CommittedResultFactDigest) {
			err = application.NewError("collect-run-result", application.ReasonAuthorityConflict)
		}
	}
	if err != nil {
		return CollectedRunResult{}, err
	}
	drcDigest, err := drc.Digest()
	if err != nil {
		return CollectedRunResult{}, err
	}
	currentAttempt, found, err := l.ingress.AttemptState(attempt.Identity)
	if err != nil || !found || currentAttempt.CommittedResultFactDigest != admission.FactDigest {
		return CollectedRunResult{}, application.NewError("collect-run-result", application.ReasonAuthorityConflict)
	}
	// BoundDirectory keeps the Run lease guard read-borrowed for descriptor
	// safety. Terminalization takes the same guard exclusively while it seals
	// the current Run authority, so carrying this borrow into
	// beginCompletedTerminalization would make the collecting goroutine wait on
	// its own RLock forever. All attempt-directory reads and writes are complete
	// once the result and observation are durably admitted; release that borrow
	// before entering the terminalization authority transaction. Close is
	// idempotent, so the deferred cleanup remains the failure-path backstop.
	if err := attemptDirectory.Close(); err != nil {
		return CollectedRunResult{}, err
	}
	terminal, err := l.terminalizeCompletedAttempt(ctx, verifier, acquisition, read, currentAttempt)
	if err != nil {
		return CollectedRunResult{}, err
	}
	if terminal.CleanupReleasedDigest == "" {
		return CollectedRunResult{}, application.NewError("collect-run-result", application.ReasonRecoveryRequired)
	}

	state, err := runstore.InspectUnderLease(l.runLease)
	if err != nil || state.State != domain.StateRunning || state.CurrentAttemptID != attempt.Identity.AttemptID {
		return CollectedRunResult{}, application.NewError("collect-run-result", application.ReasonAuthorityConflict)
	}
	eventID, err := domain.NewID("event")
	if err != nil {
		return CollectedRunResult{}, err
	}
	completedAt := l.now().UTC()
	payload := map[string]any{"snapshotDigest": observation.SnapshotDigest, "diffDigest": observation.DiffDigest,
		"resultAdmissionFactDigest": admission.FactDigest, "resultDRCDigest": drcDigest, "resultEnvelopeDigest": envelopeDigest,
		"terminalizationBarrierFactDigest": terminal.BarrierDigest, "processTerminalFactDigest": terminal.ProcessTerminalDigest,
		"allocationTerminatedFactDigest": terminal.AllocationTerminalDigest, "supervisorClosedFactDigest": terminal.SupervisorClosedDigest,
		"cleanupReleasedFactDigest": terminal.CleanupReleasedDigest}
	if localDispatchDigest != "" || localIngressDigest != "" {
		if localDispatchDigest == "" || localIngressDigest == "" {
			return CollectedRunResult{}, application.NewError("collect-run-result", application.ReasonAuthorityConflict)
		}
		payload["dispatchObservationDigest"] = localDispatchDigest
		payload["ingressObservationDigest"] = localIngressDigest
	}
	event := domain.RunEvent{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: eventID, RunID: state.RunID,
		AttemptID: attempt.Identity.AttemptID, Sequence: state.Sequence + 1, Type: "worker.completed",
		StateFrom: domain.StateRunning, StateTo: domain.StateVerifying, Timestamp: completedAt,
		Actor:   &domain.Actor{Type: "system", ID: "marshal-production-runtime"},
		Payload: payload,
	}
	next, err := lifecycle.Reduce(state, event, lifecycle.Guard{LeaseHeld: true, WorkerProtocolComplete: true, SnapshotRecorded: true})
	if err != nil {
		return CollectedRunResult{}, err
	}
	if err := l.runs.Append(l.runLease, event, state.Sequence); err != nil {
		return CollectedRunResult{}, err
	}
	if err := l.runs.WriteSnapshot(l.runLease, next); err != nil {
		return CollectedRunResult{}, err
	}
	projection, err := l.runs.ReadCurrentRunProjectionUnderLease(l.runLease)
	if err != nil {
		return CollectedRunResult{}, err
	}
	return CollectedRunResult{Run: projection, WorkerResult: result, AdmissionFactDigest: admission.FactDigest, DRCDigest: drcDigest, EnvelopeDigest: envelopeDigest}, nil
}

// resultObservation freezes the candidate observation before result admission
// and reuses those exact bytes after a crash. A committed result must never be
// rebound to recovery-time worktree contents: the stored observation is
// canonical-shape checked and then treated as immutable recovery evidence.
func (l *CompositionLedger) resultObservation(ctx context.Context, attemptDirectory *runstore.BoundDirectory, read runstore.RunStartAuthorityProjection, committed bool, expected resultingress.ResultObservationBinding) (verification.Observation, resultingress.ResultObservationBinding, error) {
	const snapshotName = "worktree-snapshot.json"
	if !committed {
		observation, err := verification.ObserveContext(ctx, read.WorktreePath, read.BaseSHA, 64<<20)
		if err != nil {
			return verification.Observation{}, resultingress.ResultObservationBinding{}, err
		}
		data, err := json.MarshalIndent(observation, "", "  ")
		if err != nil {
			return verification.Observation{}, resultingress.ResultObservationBinding{}, err
		}
		stored := append(data, '\n')
		if err := runstore.WriteFileInDirectory(attemptDirectory, snapshotName, stored, 0o600); err != nil {
			return verification.Observation{}, resultingress.ResultObservationBinding{}, err
		}
		binding := resultingress.ResultObservationBinding{ObservationDigest: canonical.DigestBytes(stored), SnapshotDigest: observation.SnapshotDigest, DiffDigest: observation.DiffDigest}
		return observation, binding, nil
	}

	stored, err := runstore.ReadFileInDirectory(attemptDirectory, snapshotName, 4<<20)
	if err != nil {
		return verification.Observation{}, resultingress.ResultObservationBinding{}, err
	}
	var observation verification.Observation
	if expected.Validate() != nil || len(stored) < 2 || stored[len(stored)-1] != '\n' || json.Unmarshal(stored, &observation) != nil || !validStoredObservation(observation) {
		return verification.Observation{}, resultingress.ResultObservationBinding{}, application.NewError("collect-run-result", application.ReasonAuthorityConflict)
	}
	canonicalShape, err := json.MarshalIndent(observation, "", "  ")
	if err != nil || !bytes.Equal(stored, append(canonicalShape, '\n')) {
		return verification.Observation{}, resultingress.ResultObservationBinding{}, application.NewError("collect-run-result", application.ReasonAuthorityConflict)
	}
	actual := resultingress.ResultObservationBinding{ObservationDigest: canonical.DigestBytes(stored), SnapshotDigest: observation.SnapshotDigest, DiffDigest: observation.DiffDigest}
	if actual != expected {
		return verification.Observation{}, resultingress.ResultObservationBinding{}, application.NewError("collect-run-result", application.ReasonAuthorityConflict)
	}
	return observation, actual, nil
}

func validStoredObservation(observation verification.Observation) bool {
	if !profileDigestPattern.MatchString(observation.SnapshotDigest) || !profileDigestPattern.MatchString(observation.DiffDigest) || observation.ChangedFileCount != len(observation.ChangedFiles) || observation.DiffBytes < 0 {
		return false
	}
	for index, path := range observation.ChangedFiles {
		if path == "" || index > 0 && observation.ChangedFiles[index-1] >= path {
			return false
		}
	}
	return true
}

func (l *CompositionLedger) resultAdmissionAuthority(attempt resultingress.AttemptAuthorityState, lease dispatch.DispatchLease, capability authority.DispatchResultCapability, resultDigest string) (resultingress.DRC, resultingress.LedgerBinding, error) {
	expiry, err := time.Parse(time.RFC3339, lease.ExpiresAt)
	if err != nil {
		return resultingress.DRC{}, resultingress.LedgerBinding{}, err
	}
	evidenceDigest := capability.EdgeDigest
	binding := resultingress.LedgerBinding{LeaseID: lease.LeaseId, Generation: uint64(lease.Generation), FencingToken: lease.FencingToken,
		AttemptID: lease.AttemptId, AllocationID: lease.AllocationId, Expiry: expiry, RegistrationID: lease.RegistrationId,
		SnapshotDigest: lease.ProviderCapabilitySnapshotDigest, EvidenceDigest: evidenceDigest}
	drc := resultingress.DRC{AuthorityNamespaceID: attempt.Identity.AuthorityNamespaceRef, TaskID: lease.TaskId, RunID: lease.RunId,
		AttemptID: lease.AttemptId, AllocationID: lease.AllocationId, LeaseID: lease.LeaseId, Generation: uint64(lease.Generation),
		FencingToken: lease.FencingToken, CommandID: attempt.CommandID, IdempotencyKey: "ingress:attempt:" + lease.AttemptId,
		RequestDigest: resultDigest, Nonce: capability.EdgeDigest, Expiry: expiry, Operation: resultingress.OpResult,
		RegistrationID: lease.RegistrationId, SnapshotDigest: lease.ProviderCapabilitySnapshotDigest, EvidenceDigest: evidenceDigest}
	if err := drc.Validate(); err != nil {
		return resultingress.DRC{}, resultingress.LedgerBinding{}, err
	}
	return drc, binding, nil
}
