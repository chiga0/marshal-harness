//go:build darwin && arm64

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/app"
	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
	"github.com/chiga0/marshal-harness/internal/review"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
	"github.com/chiga0/marshal-harness/internal/verification"
)

func (adapter *sealedRepositoryApplication) CollectRunResult(ctx context.Context, request application.CollectRunResultRequest) (application.CollectedRunProjection, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.closed || adapter.session == nil || request.Validate() != nil {
		return application.CollectedRunProjection{}, application.NewError("collect-run-result", application.ReasonInvalidRequest)
	}
	current, err := adapter.session.InspectRun(ctx, application.InspectRunRequest{RunID: request.RunID})
	if err != nil {
		return application.CollectedRunProjection{}, err
	}
	if current.State == domain.StateVerifying && current.Sequence == request.ExpectedSequence+1 {
		return adapter.rehydrateCollectedRun(ctx, request)
	}
	if !currentRunMatches(current, application.CurrentRunRequest(request), domain.StateRunning) {
		return application.CollectedRunProjection{}, application.NewError("collect-run-result", application.ReasonAuthorityConflict)
	}
	run, err := adapter.openRun(ctx, request.RunID)
	if err != nil {
		return application.CollectedRunProjection{}, err
	}
	defer run.Close()
	before, err := run.runtime.InspectRun(ctx, application.InspectRunRequest{RunID: request.RunID})
	if err != nil || !currentRunMatches(before, application.CurrentRunRequest(request), domain.StateRunning) {
		return application.CollectedRunProjection{}, application.NewError("collect-run-result", application.ReasonAuthorityConflict)
	}
	collected, err := run.runtime.CollectRunResult(ctx, request.RunID)
	if errors.Is(err, productionruntime.ErrAttemptStillRunning) {
		return application.CollectedRunProjection{}, application.NewError("collect-run-result", application.ReasonAttemptStillRunning)
	}
	if err != nil {
		return application.CollectedRunProjection{}, err
	}
	projection := application.CollectedRunProjection{
		ProtocolRevision: application.FullLifecycleProtocolRevision,
		Run:              collected.Run, AdmissionFactDigest: collected.AdmissionFactDigest,
		DRCDigest: collected.DRCDigest, EnvelopeDigest: collected.EnvelopeDigest,
	}
	if projection.Validate() != nil || projection.Run.RunID != request.RunID || projection.Run.AttemptID != request.AttemptID || projection.Run.Sequence <= request.ExpectedSequence || projection.Run.AuthorityHead == request.ExpectedAuthorityHead {
		return application.CollectedRunProjection{}, application.NewError("collect-run-result", application.ReasonAuthorityConflict)
	}
	return projection, nil
}

func (adapter *sealedRepositoryApplication) VerifyRun(ctx context.Context, request application.VerifyRunRequest) (result application.VerificationProjection, resultErr error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.closed || adapter.validator == nil || adapter.entryIdentity == nil || request.Validate() != nil {
		return application.VerificationProjection{}, application.NewError("verify-run", application.ReasonInvalidRequest)
	}
	lease, err := adapter.runs.AcquireExisting(request.RunID)
	if err != nil {
		return application.VerificationProjection{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, lease.Release()) }()
	authorityProjection, err := adapter.runs.ReadRunStartAuthorityUnderLease(ctx, lease)
	if err != nil {
		return application.VerificationProjection{}, err
	}
	if authorityProjection.Run.State == domain.StateReviewPending && authorityProjection.Run.Sequence == request.ExpectedSequence+1 {
		return rehydrateVerificationUnderLease(ctx, adapter.runs, lease, request)
	}
	if !currentRunMatches(authorityProjection.Run, application.CurrentRunRequest(request), domain.StateVerifying) {
		return application.VerificationProjection{}, application.NewError("verify-run", application.ReasonAuthorityConflict)
	}
	state, err := runstore.InspectUnderLease(lease)
	if err != nil || state.State != domain.StateVerifying || state.CurrentAttemptID != request.AttemptID {
		return application.VerificationProjection{}, application.NewError("verify-run", application.ReasonAuthorityConflict)
	}
	taskData, err := runstore.ReadFileUnderLease(lease, 2<<20, "task-spec.json")
	if err != nil {
		return application.VerificationProjection{}, err
	}
	digest, err := canonical.DigestJSON(taskData)
	if err != nil || digest != state.SpecDigest {
		return application.VerificationProjection{}, application.NewError("verify-run", application.ReasonAuthorityConflict)
	}
	applicationInstance, err := app.New()
	if err != nil {
		return application.VerificationProjection{}, err
	}
	task, err := applicationInstance.ParseTaskSpec(taskData)
	if err != nil || task.Metadata.ID != state.TaskID || state.WorktreePath == "" || state.BaseSHA == "" {
		return application.VerificationProjection{}, application.NewError("verify-run", application.ReasonAuthorityConflict)
	}
	localVerificationInput, err := prepareLocalVerificationBinding(ctx, lease, state, localDogfoodObservation(ctx), adapter.validator)
	if err != nil {
		return application.VerificationProjection{}, err
	}
	repositoryIdentity, err := gitworktree.Open(adapter.repositoryRoot)
	if err != nil {
		return application.VerificationProjection{}, err
	}
	worktreeLease, err := repositoryIdentity.Acquire(adapter.stateRoot, state.TaskID, state.WorktreePath, state.BaseSHA)
	if err != nil {
		return application.VerificationProjection{}, err
	}
	defer worktreeLease.Release()
	runDirectory := filepath.Join(adapter.stateRoot, "runs", request.RunID)
	scope, deliverables, commands := verification.PolicyFromTask(task)
	baselinePath := ""
	if commandsNeedBaseline(commands) {
		baseline, baselineErr := repositoryIdentity.CreateDetached(adapter.stateRoot, filepath.Join(runDirectory, "baseline-worktree"), state.BaseSHA)
		if baselineErr != nil {
			return application.VerificationProjection{}, baselineErr
		}
		defer baseline.Remove()
		baselinePath = baseline.Path
	}
	authorityNamespaceID, err := verifyAuthorityNamespaceID(adapter.repositoryRoot)
	if err != nil {
		return application.VerificationProjection{}, err
	}
	verificationContext, cancelVerification := context.WithTimeout(ctx, time.Duration(task.Budgets.RunTimeoutSeconds)*time.Second)
	defer cancelVerification()
	verified, err := verification.New().Verify(verificationContext, verification.Input{
		TaskID: state.TaskID, RunID: state.RunID, AttemptID: request.AttemptID, AuthorityNamespaceID: authorityNamespaceID,
		SpecDigest: state.SpecDigest, BaseSHA: state.BaseSHA, Worktree: state.WorktreePath, ExpectedCommonDir: repositoryIdentity.CommonDir,
		RunDirectory: runDirectory, Scope: scope, Deliverables: deliverables, Commands: commands, BaselinePath: baselinePath,
		PatchCaptureBytes: patchCaptureLimit(scope.MaxDiffBytes), LocalSelfIdentity: localVerificationInput,
	})
	if err != nil {
		return application.VerificationProjection{}, err
	}
	reportData, err := json.Marshal(verified.Report)
	if err != nil {
		return application.VerificationProjection{}, err
	}
	reportDigest, err := canonical.DigestJSON(reportData)
	if err != nil {
		return application.VerificationProjection{}, err
	}
	manifestData, err := json.Marshal(verified.Manifest)
	if err != nil {
		return application.VerificationProjection{}, err
	}
	manifestDigest, err := canonical.DigestJSON(manifestData)
	if err != nil {
		return application.VerificationProjection{}, err
	}
	eventID, err := domain.NewID("event")
	if err != nil {
		return application.VerificationProjection{}, err
	}
	eventPayload := map[string]any{"reportDigest": reportDigest, "artifactManifestDigest": manifestDigest, "status": verified.Report.Status}
	if verified.Report.LocalSelfIdentityBinding != nil {
		bindingDigest, digestErr := selfidentity.DigestVerificationBinding(*verified.Report.LocalSelfIdentityBinding)
		if digestErr != nil {
			return application.VerificationProjection{}, localPhaseRejected()
		}
		eventPayload["localSelfIdentityBindingDigest"] = bindingDigest
	}
	event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: eventID, RunID: state.RunID, AttemptID: request.AttemptID, Sequence: state.Sequence + 1, Type: "verification.completed", StateFrom: state.State, StateTo: domain.StateReviewPending, Timestamp: verified.Report.CompletedAt, Actor: &domain.Actor{Type: "system", ID: "marshal-verifier"}, Payload: eventPayload}
	nextState, err := lifecycle.Reduce(state, event, lifecycle.Guard{LeaseHeld: true, EvidenceCurrent: true, ReportComplete: true})
	if err != nil {
		return application.VerificationProjection{}, err
	}
	if err := adapter.runs.Append(lease, event, state.Sequence); err != nil {
		return application.VerificationProjection{}, err
	}
	if err := adapter.runs.WriteSnapshot(lease, nextState); err != nil {
		return application.VerificationProjection{}, err
	}
	after, err := adapter.runs.ReadRunStartAuthorityUnderLease(ctx, lease)
	if err != nil {
		return application.VerificationProjection{}, err
	}
	result = application.VerificationProjection{ProtocolRevision: application.FullLifecycleProtocolRevision, Run: after.Run, Status: verified.Report.Status, ReportDigest: reportDigest, ArtifactManifestDigest: manifestDigest}
	if result.Validate() != nil || result.Run.RunID != request.RunID || result.Run.AttemptID != request.AttemptID || result.Run.Sequence <= request.ExpectedSequence || result.Run.AuthorityHead == request.ExpectedAuthorityHead {
		return application.VerificationProjection{}, application.NewError("verify-run", application.ReasonAuthorityConflict)
	}
	return result, nil
}

func (adapter *sealedRepositoryApplication) BuildReviewPacket(ctx context.Context, request application.BuildReviewPacketRequest) (result application.ReviewPacketProjection, resultErr error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.closed || adapter.validator == nil || request.Validate() != nil {
		return application.ReviewPacketProjection{}, application.NewError("build-review-packet", application.ReasonInvalidRequest)
	}
	lease, err := adapter.runs.AcquireExisting(request.RunID)
	if err != nil {
		return application.ReviewPacketProjection{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, lease.Release()) }()
	state, task, taskData, report, reportData, manifest, manifestData, current, err := adapter.loadCurrentReviewInputs(ctx, lease, application.CurrentRunRequest(request))
	if err != nil {
		return application.ReviewPacketProjection{}, err
	}
	if existing, exists, existingErr := readExistingLocalReviewPacket(lease, adapter.validator); existingErr != nil {
		return application.ReviewPacketProjection{}, existingErr
	} else if exists {
		existingData, readErr := runstore.ReadFileUnderLease(lease, 8<<20, "review-packet.json")
		existingDigest, digestErr := canonical.DigestJSON(existingData)
		result = application.ReviewPacketProjection{ProtocolRevision: application.FullLifecycleProtocolRevision, Run: current, PacketDigest: existingDigest, ReviewRound: state.ReviewRound, Packet: existing}
		if readErr != nil || digestErr != nil || result.Validate() != nil || existing.SpecDigest != state.SpecDigest || existing.VerificationDigest == "" || existing.ArtifactManifestDigest == "" {
			return application.ReviewPacketProjection{}, application.NewError("build-review-packet", application.ReasonAuthorityConflict)
		}
		return result, nil
	}
	localBinding, err := prepareLocalReviewBinding(ctx, lease, state, localDogfoodObservation(ctx), adapter.validator, report, manifest, true)
	if err != nil {
		return application.ReviewPacketProjection{}, err
	}
	runDirectory := filepath.Join(adapter.stateRoot, "runs", request.RunID)
	builder := review.PacketBuilder{RunDirectory: runDirectory, Validator: adapter.validator}
	packet, packetDigest, err := builder.Build(review.PacketBuildInput{Task: task, TaskData: taskData, Report: report, ReportData: reportData, Manifest: manifest, ManifestData: manifestData, TaskID: state.TaskID, RunID: state.RunID, SpecDigest: state.SpecDigest, BaseSHA: state.BaseSHA, ReviewRound: state.ReviewRound, AttemptsUsed: state.AttemptsUsed, LocalSelfIdentityBinding: localBinding})
	if err != nil {
		return application.ReviewPacketProjection{}, err
	}
	result = application.ReviewPacketProjection{ProtocolRevision: application.FullLifecycleProtocolRevision, Run: current, PacketDigest: packetDigest, ReviewRound: state.ReviewRound, Packet: *packet}
	if result.Validate() != nil {
		return application.ReviewPacketProjection{}, application.NewError("build-review-packet", application.ReasonAuthorityConflict)
	}
	return result, nil
}

func (adapter *sealedRepositoryApplication) ApplyReviewDecision(ctx context.Context, request application.ApplyReviewDecisionRequest) (result application.ReviewDecisionProjection, resultErr error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.closed || adapter.validator == nil || request.Validate() != nil {
		return application.ReviewDecisionProjection{}, application.NewError("apply-review-decision", application.ReasonInvalidRequest)
	}
	submittedDigest, err := canonical.DigestJSON(request.Decision)
	if err != nil || submittedDigest != request.DecisionDigest {
		return application.ReviewDecisionProjection{}, application.NewError("apply-review-decision", application.ReasonAuthorityConflict)
	}
	lease, err := adapter.runs.AcquireExisting(request.RunID)
	if err != nil {
		return application.ReviewDecisionProjection{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, lease.Release()) }()
	authorityProjection, err := adapter.runs.ReadRunStartAuthorityUnderLease(ctx, lease)
	if err != nil {
		return application.ReviewDecisionProjection{}, err
	}
	if authorityProjection.Run.Sequence == request.ExpectedSequence+1 && authorityProjection.Run.State != domain.StateReviewPending {
		return adapter.rehydrateReviewDecisionUnderLease(ctx, lease, request)
	}
	state, task, _, report, _, manifest, _, _, err := adapter.loadCurrentReviewInputs(ctx, lease, application.CurrentRunRequest{RunID: request.RunID, AttemptID: request.AttemptID, ExpectedSequence: request.ExpectedSequence, ExpectedAuthorityHead: request.ExpectedAuthorityHead})
	if err != nil {
		return application.ReviewDecisionProjection{}, err
	}
	localBinding, err := prepareLocalReviewBinding(ctx, lease, state, localDogfoodObservation(ctx), adapter.validator, report, manifest, false)
	if err != nil {
		return application.ReviewDecisionProjection{}, err
	}
	runDirectory := filepath.Join(adapter.stateRoot, "runs", request.RunID)
	imported, err := (&review.DecisionImporter{RunDirectory: runDirectory, Validator: adapter.validator}).ImportBytes(review.DecisionInput{Task: task, TaskID: state.TaskID, RunID: state.RunID, SpecDigest: state.SpecDigest, ReviewRound: state.ReviewRound, AttemptsUsed: state.AttemptsUsed, ReworkRoundsUsed: state.ReworkRoundsUsed, Report: report, Manifest: manifest, LocalSelfIdentityBinding: localBinding}, request.Decision)
	if err != nil {
		return application.ReviewDecisionProjection{}, err
	}
	eventID, err := domain.NewID("event")
	if err != nil {
		return application.ReviewDecisionProjection{}, err
	}
	timestamp := time.Now().UTC()
	eventType := "review." + strings.ReplaceAll(imported.Decision.Verdict, "_", "-")
	if imported.Decision.Verdict == "rework" && imported.TargetState == domain.StateRejected {
		eventType = "review.rework-budget-exhausted"
	}
	payload := map[string]any{"verdict": imported.Decision.Verdict, "decisionDigest": imported.DecisionDigest, "evidenceDigest": imported.Decision.EvidenceDigest}
	if imported.Decision.LocalSelfIdentityBindingDigest != "" {
		payload["localSelfIdentityBindingDigest"] = imported.Decision.LocalSelfIdentityBindingDigest
	}
	if imported.TargetState.Terminal() {
		reason := imported.Decision.Summary
		if imported.BudgetExhausted && imported.Decision.Verdict == "rework" {
			reason = "Rework/Attempt 预算耗尽：" + reason
		}
		payload["terminalReason"] = reason
	}
	event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: eventID, RunID: state.RunID, AttemptID: request.AttemptID, Sequence: state.Sequence + 1, Type: eventType, StateFrom: state.State, StateTo: imported.TargetState, Timestamp: timestamp, Actor: &domain.Actor{Type: "system", ID: "marshal-review"}, Payload: payload}
	guard := lifecycle.Guard{LeaseHeld: true, EvidenceCurrent: true, RequiredGatesPass: report.Status == "pass", DecisionCurrent: true, NoChangeAllowed: task.Acceptance.AllowNoChange, BudgetAvailable: !imported.BudgetExhausted}
	nextState, err := lifecycle.Reduce(state, event, guard)
	if err != nil {
		return application.ReviewDecisionProjection{}, err
	}
	outcome := review.TerminalOutcome(state.TaskID, state.RunID, imported.TargetState, imported, timestamp)
	prepared, err := review.PrepareRecords(runDirectory, imported, outcome)
	if err != nil {
		return application.ReviewDecisionProjection{}, err
	}
	if err := adapter.runs.Append(lease, event, state.Sequence); err != nil {
		prepared.Abort()
		return application.ReviewDecisionProjection{}, err
	}
	if err := prepared.Commit(); err != nil {
		return application.ReviewDecisionProjection{}, err
	}
	if err := adapter.runs.WriteSnapshot(lease, nextState); err != nil {
		return application.ReviewDecisionProjection{}, err
	}
	after, err := adapter.runs.ReadRunStartAuthorityUnderLease(ctx, lease)
	if err != nil {
		return application.ReviewDecisionProjection{}, err
	}
	outcomeDigest := ""
	if nextState.State.Terminal() {
		outcomeData, readErr := runstore.ReadFileUnderLease(lease, 2<<20, "outcome.json")
		if readErr != nil {
			return application.ReviewDecisionProjection{}, readErr
		}
		outcomeDigest, err = canonical.DigestJSON(outcomeData)
		if err != nil {
			return application.ReviewDecisionProjection{}, err
		}
	}
	result = application.ReviewDecisionProjection{ProtocolRevision: application.FullLifecycleProtocolRevision, Run: after.Run, Verdict: imported.Decision.Verdict, DecisionDigest: imported.DecisionDigest, EvidenceDigest: imported.Decision.EvidenceDigest, OutcomeDigest: outcomeDigest}
	if result.Validate() != nil || result.Run.Sequence <= request.ExpectedSequence || result.Run.AuthorityHead == request.ExpectedAuthorityHead {
		return application.ReviewDecisionProjection{}, application.NewError("apply-review-decision", application.ReasonAuthorityConflict)
	}
	return result, nil
}

func currentRunMatches(projection application.RunProjection, request application.CurrentRunRequest, state domain.State) bool {
	return projection.Validate() == nil && projection.RunID == request.RunID && projection.AttemptID == request.AttemptID && projection.Sequence == request.ExpectedSequence && projection.AuthorityHead == request.ExpectedAuthorityHead && projection.State == state
}

func (adapter *sealedRepositoryApplication) rehydrateCollectedRun(ctx context.Context, request application.CollectRunResultRequest) (result application.CollectedRunProjection, resultErr error) {
	lease, err := adapter.runs.AcquireExisting(request.RunID)
	if err != nil {
		return application.CollectedRunProjection{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, lease.Release()) }()
	transition, err := adapter.runs.ReadCurrentRunTransitionUnderLease(ctx, lease, request.ExpectedSequence, request.ExpectedAuthorityHead)
	if err != nil || transition.Event.Type != "worker.completed" || !currentRunMatches(transition.Before, application.CurrentRunRequest(request), domain.StateRunning) || transition.After.State != domain.StateVerifying {
		return application.CollectedRunProjection{}, application.NewError("collect-run-result", application.ReasonAuthorityConflict)
	}
	admission, admissionOK := transition.Event.Payload["resultAdmissionFactDigest"].(string)
	drc, drcOK := transition.Event.Payload["resultDRCDigest"].(string)
	envelope, envelopeOK := transition.Event.Payload["resultEnvelopeDigest"].(string)
	result = application.CollectedRunProjection{ProtocolRevision: application.FullLifecycleProtocolRevision, Run: transition.After, AdmissionFactDigest: admission, DRCDigest: drc, EnvelopeDigest: envelope}
	if !admissionOK || !drcOK || !envelopeOK || result.Validate() != nil {
		return application.CollectedRunProjection{}, application.NewError("collect-run-result", application.ReasonAuthorityConflict)
	}
	return result, nil
}

func rehydrateVerificationUnderLease(ctx context.Context, store *runstore.Store, lease *runstore.Lease, request application.VerifyRunRequest) (application.VerificationProjection, error) {
	transition, err := store.ReadCurrentRunTransitionUnderLease(ctx, lease, request.ExpectedSequence, request.ExpectedAuthorityHead)
	if err != nil || transition.Event.Type != "verification.completed" || !currentRunMatches(transition.Before, application.CurrentRunRequest(request), domain.StateVerifying) || transition.After.State != domain.StateReviewPending {
		return application.VerificationProjection{}, application.NewError("verify-run", application.ReasonAuthorityConflict)
	}
	status, statusOK := transition.Event.Payload["status"].(string)
	reportDigest, reportOK := transition.Event.Payload["reportDigest"].(string)
	manifestDigest, manifestOK := transition.Event.Payload["artifactManifestDigest"].(string)
	reportData, reportErr := runstore.ReadFileUnderLease(lease, 8<<20, "verification-report.json")
	manifestData, manifestErr := runstore.ReadFileUnderLease(lease, 8<<20, "artifact-manifest.json")
	observedReport, reportDigestErr := canonical.DigestJSON(reportData)
	observedManifest, manifestDigestErr := canonical.DigestJSON(manifestData)
	result := application.VerificationProjection{ProtocolRevision: application.FullLifecycleProtocolRevision, Run: transition.After, Status: status, ReportDigest: reportDigest, ArtifactManifestDigest: manifestDigest}
	if !statusOK || !reportOK || !manifestOK || reportErr != nil || manifestErr != nil || reportDigestErr != nil || manifestDigestErr != nil || observedReport != reportDigest || observedManifest != manifestDigest || result.Validate() != nil {
		return application.VerificationProjection{}, application.NewError("verify-run", application.ReasonAuthorityConflict)
	}
	return result, nil
}

func (adapter *sealedRepositoryApplication) rehydrateReviewDecisionUnderLease(ctx context.Context, lease *runstore.Lease, request application.ApplyReviewDecisionRequest) (application.ReviewDecisionProjection, error) {
	if adapter.validator.Validate(domain.KindReviewDecision, request.Decision) != nil {
		return application.ReviewDecisionProjection{}, application.NewError("apply-review-decision", application.ReasonAuthorityConflict)
	}
	var supplied domain.ReviewDecision
	if json.Unmarshal(request.Decision, &supplied) != nil || supplied.RunID != request.RunID {
		return application.ReviewDecisionProjection{}, application.NewError("apply-review-decision", application.ReasonAuthorityConflict)
	}
	transition, err := adapter.runs.ReadCurrentRunTransitionUnderLease(ctx, lease, request.ExpectedSequence, request.ExpectedAuthorityHead)
	if err != nil || !strings.HasPrefix(transition.Event.Type, "review.") || !currentRunMatches(transition.Before, application.CurrentRunRequest{RunID: request.RunID, AttemptID: request.AttemptID, ExpectedSequence: request.ExpectedSequence, ExpectedAuthorityHead: request.ExpectedAuthorityHead}, domain.StateReviewPending) {
		return application.ReviewDecisionProjection{}, application.NewError("apply-review-decision", application.ReasonAuthorityConflict)
	}
	decisionDigest, digestOK := transition.Event.Payload["decisionDigest"].(string)
	evidenceDigest, evidenceOK := transition.Event.Payload["evidenceDigest"].(string)
	verdict, verdictOK := transition.Event.Payload["verdict"].(string)
	decisionData, readErr := runstore.ReadFileUnderLease(lease, 8<<20, "decisions", "decision-"+leftPadReviewRound(supplied.ReviewRound)+".json")
	storedDecisionDigest, digestErr := canonical.DigestJSON(decisionData)
	outcomeDigest := ""
	if transition.After.State.Terminal() {
		outcomeData, outcomeErr := runstore.ReadFileUnderLease(lease, 2<<20, "outcome.json")
		if outcomeErr != nil {
			return application.ReviewDecisionProjection{}, outcomeErr
		}
		outcomeDigest, err = canonical.DigestJSON(outcomeData)
		if err != nil {
			return application.ReviewDecisionProjection{}, err
		}
	}
	result := application.ReviewDecisionProjection{ProtocolRevision: application.FullLifecycleProtocolRevision, Run: transition.After, Verdict: verdict, DecisionDigest: decisionDigest, EvidenceDigest: evidenceDigest, OutcomeDigest: outcomeDigest}
	if !digestOK || !evidenceOK || !verdictOK || readErr != nil || digestErr != nil || storedDecisionDigest != decisionDigest || result.Validate() != nil {
		return application.ReviewDecisionProjection{}, application.NewError("apply-review-decision", application.ReasonAuthorityConflict)
	}
	return result, nil
}

func leftPadReviewRound(round uint) string {
	return fmt.Sprintf("%03d", round)
}

func (adapter *sealedRepositoryApplication) loadCurrentReviewInputs(ctx context.Context, lease *runstore.Lease, request application.CurrentRunRequest) (domain.RunState, domain.TaskSpec, []byte, verification.Report, []byte, verification.ArtifactManifest, []byte, application.RunProjection, error) {
	empty := func(err error) (domain.RunState, domain.TaskSpec, []byte, verification.Report, []byte, verification.ArtifactManifest, []byte, application.RunProjection, error) {
		return domain.RunState{}, domain.TaskSpec{}, nil, verification.Report{}, nil, verification.ArtifactManifest{}, nil, application.RunProjection{}, err
	}
	authorityProjection, err := adapter.runs.ReadRunStartAuthorityUnderLease(ctx, lease)
	if err != nil || !currentRunMatches(authorityProjection.Run, request, domain.StateReviewPending) {
		return empty(application.NewError("review-current-inputs", application.ReasonAuthorityConflict))
	}
	state, err := runstore.InspectUnderLease(lease)
	if err != nil || state.State != domain.StateReviewPending || state.CurrentAttemptID != request.AttemptID {
		return empty(application.NewError("review-current-inputs", application.ReasonAuthorityConflict))
	}
	taskData, err := runstore.ReadFileUnderLease(lease, 2<<20, "task-spec.json")
	if err != nil {
		return empty(err)
	}
	taskDigest, err := canonical.DigestJSON(taskData)
	if err != nil || taskDigest != state.SpecDigest {
		return empty(application.NewError("review-current-inputs", application.ReasonAuthorityConflict))
	}
	applicationInstance, err := app.New()
	if err != nil {
		return empty(err)
	}
	task, err := applicationInstance.ParseTaskSpec(taskData)
	if err != nil || task.Metadata.ID != state.TaskID {
		return empty(application.NewError("review-current-inputs", application.ReasonAuthorityConflict))
	}
	reportData, err := runstore.ReadFileUnderLease(lease, 8<<20, "verification-report.json")
	if err != nil || adapter.validator.Validate(domain.KindVerificationReport, reportData) != nil {
		return empty(application.NewError("review-current-inputs", application.ReasonAuthorityConflict))
	}
	var report verification.Report
	if err := json.Unmarshal(reportData, &report); err != nil {
		return empty(err)
	}
	manifestData, err := runstore.ReadFileUnderLease(lease, 8<<20, "artifact-manifest.json")
	if err != nil || adapter.validator.Validate(domain.KindArtifactManifest, manifestData) != nil {
		return empty(application.NewError("review-current-inputs", application.ReasonAuthorityConflict))
	}
	var manifest verification.ArtifactManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return empty(err)
	}
	frozenReportDigest, frozenManifestDigest, err := frozenVerificationDigests(lease)
	if err != nil {
		return empty(err)
	}
	currentReportDigest, reportDigestErr := canonical.DigestJSON(reportData)
	currentManifestDigest, manifestDigestErr := canonical.DigestJSON(manifestData)
	if reportDigestErr != nil || manifestDigestErr != nil || currentReportDigest != frozenReportDigest || currentManifestDigest != frozenManifestDigest {
		return empty(application.NewError("review-current-inputs", application.ReasonAuthorityConflict))
	}
	repositoryIdentity, err := gitworktree.Open(adapter.repositoryRoot)
	if err != nil {
		return empty(err)
	}
	worktreeLease, err := repositoryIdentity.Acquire(adapter.stateRoot, state.TaskID, state.WorktreePath, state.BaseSHA)
	if err != nil {
		return empty(err)
	}
	defer worktreeLease.Release()
	observation, err := verification.ObserveContext(ctx, state.WorktreePath, state.BaseSHA, patchCaptureLimit(task.Scope.MaxDiffBytes))
	if err != nil || review.ValidateCurrentObservation(report, observation) != nil {
		return empty(application.NewError("review-current-inputs", application.ReasonAuthorityConflict))
	}
	return state, task, taskData, report, reportData, manifest, manifestData, authorityProjection.Run, nil
}
