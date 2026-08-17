package verification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
)

type Input struct {
	TaskID              string
	RunID               string
	AttemptID           string
	SpecDigest          string
	BaseSHA             string
	Worktree            string
	ExpectedCommonDir   string
	RunDirectory        string
	Scope               ScopePolicy
	Deliverables        []Deliverable
	Commands            []CommandSpec
	BaselinePath        string
	Environment         []string
	PatchCaptureBytes   int64
	WorkerDeclaredPaths []string // diagnostic only; never authorizes scope
	ToolAllowlist       []string // frozen worker.tools declaration; empty keeps the tool-audit gate skipped
	// AuthorityNamespaceID is the frozen local authority key space digest
	// (ADR 0018 §10) owning the Candidate records; injected together with
	// AttemptID by the orchestration layer. AttemptID switches candidate
	// mode on (ADR 0027 dual-record chain); an empty AttemptID keeps the
	// legacy read-compatible path for callers predating ADR 0027.
	AuthorityNamespaceID string
}

type Result struct {
	Report   Report
	Manifest ArtifactManifest

	denialsBenign  int
	denialsFatal   int
	denialsPresent bool
}

type Verifier struct {
	now func() time.Time
}

func New() *Verifier { return &Verifier{now: time.Now} }

func (v *Verifier) Verify(ctx context.Context, input Input) (Result, error) {
	started := v.now().UTC()
	empty, err := emptyObservation()
	if err != nil {
		return Result{}, err
	}
	// Artifacts starts as a non-nil empty slice: a nil slice would marshal
	// to JSON null and violate the ArtifactManifest schema's array contract
	// on every exit path that precedes the first observed artifact, notably
	// the early repository Gate fail/error returns (issue #142).
	result := Result{Report: Report{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindVerificationReport, TaskID: input.TaskID, RunID: input.RunID, SpecDigest: input.SpecDigest, BaseSHA: input.BaseSHA, Observed: empty, StartedAt: started}, Manifest: ArtifactManifest{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindArtifactManifest, TaskID: input.TaskID, RunID: input.RunID, Artifacts: []Artifact{}, GeneratedAt: started}}
	if err := validateInput(input); err != nil {
		return result, err
	}
	if err := os.MkdirAll(input.RunDirectory, 0o700); err != nil {
		return result, err
	}
	repositoryGate := verifyRepository(ctx, input)
	result.Report.Gates = append(result.Report.Gates, repositoryGate)
	if repositoryGate.Status == "error" || repositoryGate.Status == "fail" {
		// Repository integrity collapses before any git observation exists:
		// bind the deterministic empty Observation to its real empty patch
		// bytes so the run still carries a content-addressed observed patch
		// and remains reviewable (issue #142).
		if err := v.persistEarlyObservedPatch(input, &result, nil, false, []string{"repository:integrity"}); err != nil {
			return result, err
		}
		return v.finish(input, result)
	}
	observation, err := ObserveContext(ctx, input.Worktree, input.BaseSHA, input.PatchCaptureBytes)
	if err != nil {
		result.Report.Gates = append(result.Report.Gates, Gate{ID: "diff:observe", Category: "diff", Required: true, Status: "error", Summary: err.Error(), Evidence: []string{}})
		// The report keeps the deterministic empty Observation; persist the
		// matching empty patch bytes with their artifact (issue #142).
		if persistErr := v.persistEarlyObservedPatch(input, &result, nil, false, []string{"diff:observe"}); persistErr != nil {
			return result, persistErr
		}
		return v.finish(input, result)
	}
	result.Report.Observed = observation
	candidateMode := input.AttemptID != ""
	workerPatch := observation.Patch
	workerTruncated := observation.DiffTruncated
	if candidateMode {
		// worker.patch keeps the Worker's raw bytes; after this write the
		// verification flow never touches it again. observed.patch is the
		// only patch file normalization may replace, and its artifact digest
		// is written once below instead of rewritten in place (ADR 0027).
		if err := atomicWrite(filepath.Join(input.RunDirectory, "worker.patch"), workerPatch); err != nil {
			return result, err
		}
	}
	patchPath := filepath.Join(input.RunDirectory, "observed.patch")
	if err := atomicWrite(patchPath, observation.Patch); err != nil {
		return result, err
	}
	if !candidateMode {
		// Legacy byte-compatibility: the observed-patch artifact keeps its
		// pre-ADR-0027 manifest position and its presence on every failure
		// path; candidate mode defers the artifact until the head content is
		// known so its digest is written exactly once.
		result.Manifest.Artifacts = append(result.Manifest.Artifacts, Artifact{ID: "evidence:observed-patch", Kind: "patch", MediaType: "text/x-diff", Producer: "verifier", Required: true, Status: "validated", PathRoot: "run", RelativePath: "observed.patch", ByteSize: int64(len(observation.Patch)), Digest: canonical.DigestBytes(observation.Patch), CreatedAt: started, Truncated: observation.DiffTruncated, RelatedGates: []string{"diff:observe", "scope:changed-paths"}})
	}
	observeEvidence := []string{"artifact://evidence:observed-patch"}
	if candidateMode {
		observeEvidence = append(observeEvidence, "artifact://evidence:worker-patch")
	}
	result.Report.Gates = append(result.Report.Gates, Gate{ID: "diff:observe", Category: "diff", Required: true, Status: "pass", Summary: fmt.Sprintf("观察到 %d 个变更，%d 字节", observation.ChangedFileCount, observation.DiffBytes), Evidence: observeEvidence})
	result.Report.Gates = append(result.Report.Gates, EvaluateScope(observation, input.Scope))
	store := newLocalCandidateStore(input.RunDirectory)
	var workerCandidate, headCandidate domain.Candidate
	if candidateMode {
		admitted, admitErr := v.admitObservedCandidate(store, input, workerPatch, domain.ProducerKindWorker, candidateProducerWorker, "")
		if admitErr != nil {
			result.Report.Gates = append(result.Report.Gates, Gate{ID: "format:normalize", Category: "other", Required: true, Status: "fail", Summary: "Candidate 接纳失败：" + admitErr.Error(), Evidence: []string{}})
			if persistErr := v.persistEarlyObservedPatch(input, &result, observation.Patch, observation.DiffTruncated, observedPatchFailureGates); persistErr != nil {
				return result, persistErr
			}
			return v.finish(input, result)
		}
		workerCandidate, headCandidate = admitted, admitted
	}
	normalized, formatErr := normalizeFormat(ctx, input.Worktree, observation.ChangedFiles, input.Scope.AllowPaths)
	if formatErr != nil {
		result.Report.Gates = append(result.Report.Gates, Gate{ID: "format:normalize", Category: "other", Required: true, Status: "fail", Summary: "gofmt 归一化失败：" + formatErr.Error(), Evidence: []string{}})
		if candidateMode {
			// Legacy mode already carries the observed-patch artifact from
			// the pre-normalization append; candidate mode defers it and must
			// bind the last observed bytes here (issue #142).
			if persistErr := v.persistEarlyObservedPatch(input, &result, observation.Patch, observation.DiffTruncated, observedPatchFailureGates); persistErr != nil {
				return result, persistErr
			}
		}
		return v.finish(input, result)
	}
	if len(normalized) > 0 {
		updated, observeErr := ObserveContext(ctx, input.Worktree, input.BaseSHA, input.PatchCaptureBytes)
		if observeErr != nil {
			result.Report.Gates = append(result.Report.Gates, Gate{ID: "format:normalize", Category: "other", Required: true, Status: "fail", Summary: "归一化后无法重新观察 worktree：" + observeErr.Error(), Evidence: []string{}})
			if candidateMode {
				// The report still binds the pre-normalization observation;
				// re-bind those bytes so the manifest matches them (issue #142).
				if persistErr := v.persistEarlyObservedPatch(input, &result, observation.Patch, observation.DiffTruncated, observedPatchFailureGates); persistErr != nil {
					return result, persistErr
				}
			}
			return v.finish(input, result)
		}
		observation = updated
		result.Report.Observed = updated
		if err := atomicWrite(patchPath, observation.Patch); err != nil {
			return result, err
		}
		if candidateMode {
			admitted, admitErr := v.admitObservedCandidate(store, input, observation.Patch, domain.ProducerKindNormalizer, candidateProducerNormalizer, workerCandidate.CandidateDigest)
			if admitErr != nil {
				result.Report.Gates = append(result.Report.Gates, Gate{ID: "format:normalize", Category: "other", Required: true, Status: "fail", Summary: "Candidate 接纳失败：" + admitErr.Error(), Evidence: []string{}})
				if persistErr := v.persistEarlyObservedPatch(input, &result, observation.Patch, observation.DiffTruncated, observedPatchFailureGates); persistErr != nil {
					return result, persistErr
				}
				return v.finish(input, result)
			}
			headCandidate = admitted
		} else {
			// Legacy byte-compatibility only: finalize the pre-gate artifact
			// in memory before the manifest is persisted for the first time.
			// Candidate mode never rewrites an artifact field; its observed
			// artifact is appended once below with the head content (ADR 0027
			// forbids in-place digest rewrites).
			legacyArtifact := &result.Manifest.Artifacts[0]
			legacyArtifact.ByteSize = int64(len(observation.Patch))
			legacyArtifact.Digest = canonical.DigestBytes(observation.Patch)
			legacyArtifact.Truncated = observation.DiffTruncated
			legacyArtifact.RelatedGates = append(legacyArtifact.RelatedGates, "format:normalize")
		}
	}
	normalizeGateCandidate := ""
	if candidateMode {
		observedRelatedGates := []string{"diff:observe", "scope:changed-paths"}
		if len(normalized) > 0 {
			observedRelatedGates = append(observedRelatedGates, "format:normalize")
		}
		result.Manifest.Artifacts = append(result.Manifest.Artifacts, Artifact{ID: "evidence:observed-patch", Kind: "patch", MediaType: "text/x-diff", Producer: "verifier", Required: true, Status: "validated", PathRoot: "run", RelativePath: "observed.patch", ByteSize: int64(len(observation.Patch)), Digest: canonical.DigestBytes(observation.Patch), CandidateDigest: headCandidate.CandidateDigest, CreatedAt: started, Truncated: observation.DiffTruncated, RelatedGates: observedRelatedGates})
		result.Manifest.Artifacts = append(result.Manifest.Artifacts, Artifact{ID: "evidence:worker-patch", Kind: "patch", MediaType: "text/x-diff", Producer: "verifier", Required: true, Status: "validated", PathRoot: "run", RelativePath: "worker.patch", ByteSize: int64(len(workerPatch)), Digest: canonical.DigestBytes(workerPatch), CandidateDigest: workerCandidate.CandidateDigest, CreatedAt: started, Truncated: workerTruncated, RelatedGates: []string{"diff:observe", "scope:changed-paths", "format:normalize"}})
		result.Report.WorkerCandidateDigest = workerCandidate.CandidateDigest
		result.Report.CandidateDigest = headCandidate.CandidateDigest
		normalizeGateCandidate = headCandidate.CandidateDigest
	}
	result.Report.Gates = append(result.Report.Gates, formatNormalizeGate(normalized, normalizeGateCandidate))
	artifacts, artifactGates := CollectArtifacts(input.Worktree, input.Deliverables, started)
	result.Manifest.Artifacts = append(result.Manifest.Artifacts, artifacts...)
	result.Report.Gates = append(result.Report.Gates, artifactGates...)
	denialAssessment, err := assessDenialSummary(input.RunDirectory, started)
	if err != nil {
		return result, err
	}
	result.Report.Gates = append(result.Report.Gates, denialAssessment.Gate)
	if denialAssessment.Artifact != nil {
		result.Manifest.Artifacts = append(result.Manifest.Artifacts, *denialAssessment.Artifact)
	}
	result.denialsBenign, result.denialsFatal, result.denialsPresent = denialAssessment.Benign, denialAssessment.Fatal, denialAssessment.Present
	auditAttemptDir, _, _ := latestAttemptDirectory(filepath.Join(input.RunDirectory, "attempts"))
	result.Report.Gates = append(result.Report.Gates, assessToolAudit(input, auditAttemptDir))
	allowlistAssessment, err := assessToolAllowlist(input.RunDirectory, input.SpecDigest, started)
	if err != nil {
		return result, err
	}
	result.Report.Gates = append(result.Report.Gates, allowlistAssessment.Gate)
	if allowlistAssessment.Artifact != nil {
		result.Manifest.Artifacts = append(result.Manifest.Artifacts, *allowlistAssessment.Artifact)
	}
	runner := Runner{Environment: input.Environment}
	for _, spec := range input.Commands {
		if ctx.Err() != nil {
			result.Report.Gates = append(result.Report.Gates, Gate{ID: "command:" + spec.ID, Category: "command", Required: spec.Required, Status: "cancelled", Summary: "验证已取消", Evidence: []string{}})
			break
		}
		var baselineObservation Observation
		var baselineObserveErr error
		var candidateProtection []commandProtectedSource
		if input.BaselinePath != "" {
			baselineObservation, baselineObserveErr = ObserveContext(ctx, input.BaselinePath, input.BaseSHA, input.PatchCaptureBytes)
			if baselineObserveErr == nil {
				candidateProtection = append(candidateProtection, commandProtectedSource{Path: input.BaselinePath, BaseSHA: input.BaseSHA, Expected: baselineObservation})
			}
		}
		isolated := isolatedCommandResult{}
		var isolationErr error
		if baselineObserveErr != nil {
			isolationErr = errors.New("cannot freeze baseline before candidate command")
		} else {
			isolated, isolationErr = runCommandIsolated(ctx, runner, input.Worktree, input.BaseSHA, observation, spec, candidateProtection...)
		}
		commandResult := isolated.Command
		if isolationErr != nil && !isolated.Executed {
			commandResult = isolationErrorCommand(spec)
		}
		baselineRequested := spec.BaselinePolicy == "always" || (spec.BaselinePolicy == "on-failure" && commandResult.Status != "pass")
		baselineMissing := baselineRequested && input.BaselinePath == ""
		verdict := ""
		baselineMutated := false
		baselineMutationBefore, baselineMutationAfter := "", ""
		baselineIsolationFailed := false
		if input.BaselinePath != "" && baselineRequested {
			baseline := isolatedCommandResult{}
			var baselineErr error
			if baselineObserveErr == nil {
				baseline, baselineErr = runCommandIsolated(ctx, runner, input.BaselinePath, input.BaseSHA, baselineObservation, spec, commandProtectedSource{Path: input.Worktree, BaseSHA: input.BaseSHA, Expected: observation})
			}
			if baselineObserveErr != nil || baselineErr != nil {
				commandResult.Record.BaselineStatus = "error"
				baselineIsolationFailed = true
			} else if baseline.Mutated || baseline.Command.Status == "cancelled" {
				commandResult.Record.BaselineStatus = "error"
				baselineMutated = baseline.Mutated
				baselineMutationBefore, baselineMutationAfter = baseline.BeforeDigest, baseline.AfterDigest
			} else {
				commandResult.Record.BaselineStatus = baseline.Command.Status
			}
			verdict = baselineRegressionVerdict(commandResult.Status, commandResult.Record.BaselineStatus)
		}
		stdoutID, stderrID, persistErr := persistCommandLogs(input.RunDirectory, spec.ID, commandResult, &result.Manifest, started)
		if persistErr != nil {
			return result, persistErr
		}
		gate := Gate{ID: "command:" + spec.ID, Category: "command", Required: spec.Required, Status: commandResult.Status, Summary: commandSummary(commandResult), Evidence: []string{"artifact://" + stdoutID, "artifact://" + stderrID}, Command: &commandResult.Record}
		if verdict != "" {
			// The regression verdict is a mark only (issue #87): it never
			// alters the candidate status or the gate outcome. The command
			// record wire shape is frozen by the verification-report schema,
			// so the mark rides the gate summary and a namespaced evidence
			// entry instead of a new serialized field.
			gate.Summary += "；Baseline 回归裁决：" + verdict
			gate.Evidence = append(gate.Evidence, baselineVerdictEvidencePrefix+verdict)
		}
		if baselineMissing {
			gate.Status, gate.Summary = "error", "命令请求 Baseline Diagnostic，但没有提供干净 Base Worktree"
			commandResult.Record.BaselineStatus = "error"
		}
		if isolationErr != nil {
			gate.Status = "error"
			gate.Summary = commandSummary(commandResult) + "；verifier-command-isolation-error: " + isolationErr.Error()
		} else if isolated.Mutated {
			gate.Status = "fail"
			gate.Summary = commandSummary(commandResult) + "；" + verifierWorktreeMutatedReason + " before=" + isolated.BeforeDigest + " after=" + isolated.AfterDigest
		} else if baselineIsolationFailed {
			gate.Status = "error"
			gate.Summary += "；Baseline command isolation failed"
		} else if baselineMutated {
			gate.Status = "fail"
			gate.Summary += "；baseline-" + verifierWorktreeMutatedReason + " before=" + baselineMutationBefore + " after=" + baselineMutationAfter
		}
		result.Report.Gates = append(result.Report.Gates, gate)
	}
	return v.finish(input, result)
}

// emptyObservation returns the deterministic Observation of a change-free
// worktree, byte-compatible with what ObserveContext computes for a clean
// tree: json.Marshal serializes the empty change set to "null", RFC 8785
// canonicalization preserves it, and the diff is empty. The early exits that
// never reach git observation bind this Observation to real empty
// observed.patch bytes, so review-time re-observation and artifact digest
// recomputation reproduce identical identities (issue #142).
func emptyObservation() (Observation, error) {
	snapshotJSON, err := json.Marshal([]Change(nil))
	if err != nil {
		return Observation{}, err
	}
	canonicalSnapshot, err := canonical.JSON(snapshotJSON)
	if err != nil {
		return Observation{}, err
	}
	return Observation{SnapshotDigest: canonical.DigestBytes(canonicalSnapshot), DiffDigest: canonical.DigestBytes(nil), ChangedFiles: []string{}, Changes: []Change{}}, nil
}

// observedPatchFailureGates lists the gates the observed patch relates to
// when verification fails after a successful observation: diff:observe and
// scope:changed-paths already evaluated the bytes, and the format:normalize
// gate (fail) closed the run.
var observedPatchFailureGates = []string{"diff:observe", "scope:changed-paths", "format:normalize"}

// persistEarlyObservedPatch writes observed.patch and appends its validated
// artifact on the fail/error exits that never reach the normal artifact
// finalization: the repository Gate and diff:observe exits bind the
// deterministic empty Observation's real empty bytes, and the candidate-mode
// normalization exits bind the last successfully observed bytes. The digest
// and byte size always recompute from the persisted bytes; the candidate
// binding stays absent because no head Candidate identity reaches the report
// on failure paths; relatedGates name the gates that evaluated the bytes.
// This keeps every failed run reviewable instead of leaving an
// unexaminable REVIEW_PENDING (issue #142).
func (v *Verifier) persistEarlyObservedPatch(input Input, result *Result, patch []byte, truncated bool, relatedGates []string) error {
	if err := atomicWrite(filepath.Join(input.RunDirectory, "observed.patch"), patch); err != nil {
		return err
	}
	result.Manifest.Artifacts = append(result.Manifest.Artifacts, Artifact{ID: "evidence:observed-patch", Kind: "patch", MediaType: "text/x-diff", Producer: "verifier", Required: true, Status: "validated", PathRoot: "run", RelativePath: "observed.patch", ByteSize: int64(len(patch)), Digest: canonical.DigestBytes(patch), CreatedAt: result.Report.StartedAt, Truncated: truncated, RelatedGates: relatedGates})
	return nil
}

func (v *Verifier) finish(input Input, result Result) (Result, error) {
	result.Report.CompletedAt = v.now().UTC()
	result.Manifest.GeneratedAt = result.Report.CompletedAt
	// Finish is the serialization boundary for every exit path: keep the
	// artifact collection a JSON array even when nothing was observed
	// (issue #142), so contract validation never strands a Run in
	// VERIFYING over a null artifacts field.
	if result.Manifest.Artifacts == nil {
		result.Manifest.Artifacts = []Artifact{}
	}
	result.Report.Status = overallStatus(result.Report.Gates)
	result.Report.Summary = fmt.Sprintf("Verification %s：%d 个 Gate", result.Report.Status, len(result.Report.Gates))
	if result.denialsPresent {
		result.Report.Summary += fmt.Sprintf("；permission 拒绝分级 benign=%d、fatal=%d", result.denialsBenign, result.denialsFatal)
	}
	reportData, err := json.MarshalIndent(result.Report, "", "  ")
	if err != nil {
		return result, err
	}
	manifestData, err := json.MarshalIndent(result.Manifest, "", "  ")
	if err != nil {
		return result, err
	}
	validator, err := contract.NewValidator()
	if err != nil {
		return result, err
	}
	if err := validator.Validate(domain.KindVerificationReport, reportData); err != nil {
		return result, fmt.Errorf("generated verification report violates contract: %w", err)
	}
	if err := validator.Validate(domain.KindArtifactManifest, manifestData); err != nil {
		return result, fmt.Errorf("generated artifact manifest violates contract: %w", err)
	}
	if err := atomicWrite(filepath.Join(input.RunDirectory, "verification-report.json"), append(reportData, '\n')); err != nil {
		return result, err
	}
	if err := atomicWrite(filepath.Join(input.RunDirectory, "artifact-manifest.json"), append(manifestData, '\n')); err != nil {
		return result, err
	}
	return result, nil
}

func validateInput(input Input) error {
	for _, value := range []string{input.TaskID, input.RunID} {
		if err := domain.ValidateID(value); err != nil {
			return err
		}
	}
	if !strings.HasPrefix(input.SpecDigest, "sha256:") || len(input.SpecDigest) != 71 {
		return errors.New("invalid spec digest")
	}
	if input.RunDirectory == "" || input.Worktree == "" {
		return errors.New("worktree and run directory are required")
	}
	// Candidate chain mode (ADR 0027) is switched on by the orchestration
	// layer injecting the Attempt identity together with the owning authority
	// namespace. It is all-or-nothing and fails closed on any missing or
	// malformed piece; an empty AttemptID keeps the legacy path.
	if input.AttemptID != "" || input.AuthorityNamespaceID != "" {
		if err := domain.ValidateID(input.AttemptID); err != nil {
			return fmt.Errorf("candidate mode requires a valid attemptId: %w", err)
		}
		if err := domain.ValidateID(input.AuthorityNamespaceID); err != nil {
			return fmt.Errorf("candidate mode requires a valid authorityNamespaceId: %w", err)
		}
		if !candidateBaseShaPattern.MatchString(input.BaseSHA) {
			return errors.New("candidate mode requires baseSha to be a full SHA object id")
		}
	}
	return nil
}

// admitObservedCandidate admits the Candidate record for the observed patch
// bytes of the ADR 0027 dual-record chain. Content identity is authoritative
// (contentDigest is the content address): when the attempt already holds an
// admitted record for identical bytes the existing record is reused and no
// new fact is appended, so repeated Verify runs never inflate the chain.
// Normalizer reuse additionally requires the predecessor link to agree,
// keeping every chain hop traceable. Otherwise a fresh record is sealed and
// admitted through the digest-verified put-if-absent store; any failure is
// fail-closed.
func (v *Verifier) admitObservedCandidate(store *localCandidateStore, input Input, patch []byte, producerKind domain.ProducerKind, producer, predecessor string) (domain.Candidate, error) {
	contentDigest := canonical.DigestBytes(patch)
	existing, found, err := store.findAdmittedByContent(input.AttemptID, contentDigest)
	if err != nil {
		return domain.Candidate{}, err
	}
	if found && (predecessor == "" || (existing.ProducerKind == domain.ProducerKindNormalizer && existing.PredecessorCandidateDigest == predecessor)) {
		return existing, nil
	}
	record, err := buildCandidate(input, producerKind, producer, contentDigest, predecessor, v.now())
	if err != nil {
		return domain.Candidate{}, err
	}
	return store.Admit(record, patch)
}

func verifyRepository(ctx context.Context, input Input) Gate {
	gate := Gate{ID: "repository:integrity", Category: "repository", Required: true, Status: "pass", Summary: "Worktree 与锁定基线完整", Evidence: []string{}}
	repository, err := gitworktree.OpenContext(ctx, input.Worktree)
	if err != nil {
		gate.Status, gate.Summary = "error", err.Error()
		return gate
	}
	if input.ExpectedCommonDir != "" && repository.CommonDir != input.ExpectedCommonDir {
		gate.Status, gate.Summary = "fail", "Worktree 不属于预期 Git Common Directory"
		return gate
	}
	if _, err := repository.ResolveBaseContext(ctx, input.BaseSHA); err != nil {
		gate.Status, gate.Summary = "fail", err.Error()
		return gate
	}
	if _, err := gitBytesContext(ctx, input.Worktree, "merge-base", "--is-ancestor", input.BaseSHA, "HEAD"); err != nil {
		gate.Status, gate.Summary = "fail", "锁定 Base 不是当前 HEAD 的祖先"
		return gate
	}
	conflicts, err := gitBytesContext(ctx, input.Worktree, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		gate.Status, gate.Summary = "error", err.Error()
		return gate
	}
	if len(conflicts) > 0 {
		gate.Status, gate.Summary = "fail", "Worktree 存在未解决冲突"
		return gate
	}
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply"} {
		path, pathErr := gitBytesContext(ctx, input.Worktree, "rev-parse", "--git-path", marker)
		if pathErr != nil {
			gate.Status, gate.Summary = "error", pathErr.Error()
			return gate
		}
		if !filepath.IsAbs(string(path)) {
			path = []byte(filepath.Join(input.Worktree, string(path)))
		}
		if _, statErr := os.Stat(string(path)); statErr == nil {
			gate.Status, gate.Summary = "fail", "Worktree 存在未完成的 Git 操作: "+marker
			return gate
		} else if !errors.Is(statErr, os.ErrNotExist) {
			gate.Status, gate.Summary = "error", statErr.Error()
			return gate
		}
	}
	return gate
}

func persistCommandLogs(runDirectory, id string, result CommandResult, manifest *ArtifactManifest, now time.Time) (string, string, error) {
	if err := domain.ValidateID(id); err != nil {
		return "", "", err
	}
	directory := filepath.Join(runDirectory, "logs")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", "", err
	}
	stdoutPath, stderrPath := filepath.Join(directory, id+".stdout.log"), filepath.Join(directory, id+".stderr.log")
	if err := atomicWrite(stdoutPath, result.Stdout); err != nil {
		return "", "", err
	}
	if err := atomicWrite(stderrPath, result.Stderr); err != nil {
		return "", "", err
	}
	stdoutID, stderrID := "log:"+id+":stdout", "log:"+id+":stderr"
	manifest.Artifacts = append(manifest.Artifacts,
		Artifact{ID: stdoutID, Kind: "command-log", MediaType: "text/plain", Producer: "verifier", Status: "validated", PathRoot: "run", RelativePath: filepath.ToSlash(filepath.Join("logs", id+".stdout.log")), ByteSize: int64(len(result.Stdout)), Digest: canonical.DigestBytes(result.Stdout), CreatedAt: now, Truncated: result.StdoutTruncated, RelatedGates: []string{"command:" + id}},
		Artifact{ID: stderrID, Kind: "command-log", MediaType: "text/plain", Producer: "verifier", Status: "validated", PathRoot: "run", RelativePath: filepath.ToSlash(filepath.Join("logs", id+".stderr.log")), ByteSize: int64(len(result.Stderr)), Digest: canonical.DigestBytes(result.Stderr), CreatedAt: now, Truncated: result.StderrTruncated, RelatedGates: []string{"command:" + id}},
	)
	return stdoutID, stderrID, nil
}

func commandSummary(result CommandResult) string {
	if result.Status == "pass" {
		return fmt.Sprintf("命令通过，耗时 %dms", result.Record.DurationMilliseconds)
	}
	hasSignal := result.Record.Signal != nil && *result.Record.Signal != ""
	hasExit := result.Record.ExitCode != nil
	if hasSignal {
		if hasExit {
			return fmt.Sprintf("命令状态 %s，signal %s（退出码 %d）", result.Status, *result.Record.Signal, *result.Record.ExitCode)
		}
		return fmt.Sprintf("命令状态 %s，signal %s", result.Status, *result.Record.Signal)
	}
	if hasExit {
		return fmt.Sprintf("命令状态 %s，退出码 %d", result.Status, *result.Record.ExitCode)
	}
	return fmt.Sprintf("命令状态 %s，退出结果不可用", result.Status)
}

// Baseline regression verdict vocabulary (issue #87). The marks classify a
// failing candidate command against the rerun of the same command on the
// locked baseline; they never change the candidate's pass/fail outcome.
const (
	// BaselineVerdictRegressionConfirmed marks a candidate failure that
	// passes on the baseline: the change under verification introduced it.
	BaselineVerdictRegressionConfirmed = "regression-confirmed"
	// BaselineVerdictPreExistingFailure marks a candidate failure that
	// fails identically on the baseline: the failure predates the change
	// and does not constitute a new regression.
	BaselineVerdictPreExistingFailure = "pre-existing-failure"

	// baselineVerdictEvidencePrefix namespaces the verdict mark inside the
	// command gate evidence list.
	baselineVerdictEvidencePrefix = "baseline-verdict:"
)

// baselineRegressionVerdict derives the regression verdict for one command
// from the candidate and baseline outcomes. It classifies only a failing
// candidate against a decisive baseline: candidate fail + baseline pass is
// a confirmed regression; candidate fail + baseline fail is a pre-existing
// failure. Every other combination (candidate not failing, baselinePolicy
// none so the baseline never ran, or an inconclusive baseline outcome such
// as error or not-run) carries no verdict.
func baselineRegressionVerdict(candidateStatus, baselineStatus string) string {
	if candidateStatus != "fail" {
		return ""
	}
	switch baselineStatus {
	case "pass":
		return BaselineVerdictRegressionConfirmed
	case "fail":
		return BaselineVerdictPreExistingFailure
	default:
		return ""
	}
}

func overallStatus(gates []Gate) string {
	status := "pass"
	for _, gate := range gates {
		if !gate.Required {
			continue
		}
		switch gate.Status {
		case "cancelled":
			return "cancelled"
		case "error":
			status = "error"
		case "fail", "skipped":
			if status == "pass" {
				status = "fail"
			}
		}
	}
	return status
}

func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".marshal-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
