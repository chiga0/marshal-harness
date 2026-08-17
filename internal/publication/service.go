// Package publication owns the evidence-to-publication gate and controlled
// commit creation. Provider-specific remote operations remain behind the
// Publisher port.
package publication

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/port"
	githubpublisher "github.com/chiga0/marshal-harness/internal/publisher/github"
	"github.com/chiga0/marshal-harness/internal/review"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/verification"
)

type Input struct {
	StateRoot, RepositoryRoot, RunID string
	Publisher                        port.Publisher
	Validator                        *contract.Validator
}

type Result struct {
	State       domain.RunState          `json:"state"`
	Publication domain.PublicationRecord `json:"publication"`
}

func Publish(ctx context.Context, input Input) (Result, error) {
	if input.Publisher == nil || input.Validator == nil {
		return Result{}, errors.New("publisher and validator are required")
	}
	store := runstore.New(input.StateRoot)
	lease, err := store.Acquire(input.RunID)
	if err != nil {
		return Result{}, err
	}
	defer lease.Release()
	state, err := store.Inspect(input.RunID)
	if err != nil {
		return Result{}, err
	}
	if state.State != domain.StatePublishing {
		return Result{}, fmt.Errorf("run state %s cannot publish", state.State)
	}
	runDir := filepath.Join(input.StateRoot, "runs", input.RunID)
	evidence, err := loadEvidence(runDir, state, input.Validator, store)
	if err != nil {
		return block(store, lease, state, runDir, err)
	}
	if evidence.task.Publication.Provider != "github" || evidence.task.Publication.Mode != "draft" ||
		(evidence.task.Publication.MergePolicy != domain.MergePolicyNever && evidence.task.Publication.MergePolicy != domain.MergePolicyPolicy) ||
		!evidence.task.Publication.Required {
		return block(store, lease, state, runDir, errors.New("publication only supports required GitHub draft PRs with mergePolicy=never|policy"))
	}
	taskRepository, err := filepath.EvalSymlinks(evidence.task.Repository.Path)
	if err != nil || taskRepository != input.RepositoryRoot {
		return block(store, lease, state, runDir, errors.New("TaskSpec repository identity mismatch"))
	}
	repository, err := gitworktree.Open(input.RepositoryRoot)
	if err != nil {
		return block(store, lease, state, runDir, err)
	}
	worktree, err := repository.Acquire(input.StateRoot, state.TaskID, state.WorktreePath, state.BaseSHA)
	if err != nil {
		return block(store, lease, state, runDir, err)
	}
	defer worktree.Release()
	current, err := verification.ObserveContext(ctx, state.WorktreePath, state.BaseSHA, patchCaptureLimit(evidence.task.Scope.MaxDiffBytes))
	if err != nil || current.SnapshotDigest != evidence.report.Observed.SnapshotDigest || current.DiffDigest != evidence.report.Observed.DiffDigest {
		return block(store, lease, state, runDir, errors.New("accepted worktree snapshot is stale"))
	}
	if evidence.task.Repository.ExpectedRemoteURL == "" {
		return block(store, lease, state, runDir, errors.New("publication requires a frozen expectedRemoteUrl"))
	}
	remoteURL, err := gitOutput(ctx, input.RepositoryRoot, nil, "remote", "get-url", evidence.task.Publication.Remote)
	if err != nil {
		return block(store, lease, state, runDir, err)
	}
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL != evidence.task.Repository.ExpectedRemoteURL {
		return block(store, lease, state, runDir, errors.New("git remote URL differs from frozen TaskSpec"))
	}
	repositoryName, err := githubpublisher.ParseRepositoryURL(remoteURL)
	if err != nil {
		return block(store, lease, state, runDir, err)
	}
	headBranch := deriveBranch(state.TaskID, state.RunID)
	var priorPublication *domain.PublicationRecord
	if state.Publication != nil {
		priorPublication, err = loadCurrentPublication(runDir, state, input.Validator, store)
		if err != nil {
			return block(store, lease, state, runDir, err)
		}
	}
	previousHeadSHA := ""
	if priorPublication != nil {
		previousHeadSHA = priorPublication.HeadSHA
	}
	intentPath := filepath.Join(runDir, "publication-intent.json")
	intent, err := existingIntent(intentPath, input.Validator)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return block(store, lease, state, runDir, err)
	}
	if err == nil {
		if validationErr := validateIntent(intent, state, evidence, repositoryName, remoteURL, headBranch, previousHeadSHA, current); validationErr != nil {
			if priorPublication == nil || !priorIntentCanRotate(intent, *priorPublication, state, repositoryName, remoteURL, headBranch) {
				return block(store, lease, state, runDir, validationErr)
			}
			if err := archivePublicationGeneration(runDir, intent); err != nil {
				return block(store, lease, state, runDir, err)
			}
			err = os.ErrNotExist
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		if priorPublication != nil {
			if err := archivePublicationArtifacts(runDir, priorPublication.ReviewRound, priorPublication.HeadSHA); err != nil {
				return block(store, lease, state, runDir, err)
			}
		}
		parentSHA := state.BaseSHA
		if previousHeadSHA != "" {
			parentSHA = previousHeadSHA
		}
		commitSHA, commitErr := controlledCommit(ctx, state.WorktreePath, runDir, state.BaseSHA, parentSHA, evidence.task.Metadata.Title, state, evidence.decision, current, evidence.candidates)
		if commitErr != nil {
			return block(store, lease, state, runDir, commitErr)
		}
		postCommit, observeErr := verification.ObserveContext(ctx, state.WorktreePath, state.BaseSHA, patchCaptureLimit(evidence.task.Scope.MaxDiffBytes))
		if observeErr != nil || postCommit.SnapshotDigest != current.SnapshotDigest || postCommit.DiffDigest != current.DiffDigest {
			return block(store, lease, state, runDir, errors.New("accepted worktree changed while controlled commit was created"))
		}
		intent = domain.PublicationIntent{
			APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindPublicationIntent, TaskID: state.TaskID, RunID: state.RunID,
			Provider: "github", Repository: repositoryName, Remote: evidence.task.Publication.Remote, RemoteURL: remoteURL,
			BaseBranch: evidence.task.Publication.BaseBranch, HeadBranch: headBranch, ReviewRound: state.ReviewRound,
			BaseSHA: state.BaseSHA, PreviousHeadSHA: previousHeadSHA, CommitSHA: commitSHA,
			SnapshotDigest: current.SnapshotDigest, DiffDigest: current.DiffDigest, SpecDigest: state.SpecDigest, PolicyDigest: state.PolicyDigest,
			EvidenceDigest: evidence.decision.EvidenceDigest, VerificationDigest: evidence.decision.VerificationDigest, ReviewDecisionDigest: evidence.decisionDigest,
			Marker: marker(state.TaskID, state.RunID), Mode: "draft", MergePolicy: evidence.task.Publication.MergePolicy, Summary: evidence.task.Metadata.Title, CreatedAt: time.Now().UTC(),
		}
		intentData, marshalErr := json.Marshal(intent)
		if marshalErr != nil {
			return block(store, lease, state, runDir, marshalErr)
		}
		if err := input.Validator.Validate(domain.KindPublicationIntent, intentData); err != nil {
			return block(store, lease, state, runDir, err)
		}
		if err := atomicWrite(intentPath, append(intentData, '\n')); err != nil {
			return block(store, lease, state, runDir, err)
		}
	} else {
		parentSHA := state.BaseSHA
		if intent.PreviousHeadSHA != "" {
			parentSHA = intent.PreviousHeadSHA
		}
		recomputed, commitErr := controlledCommit(ctx, state.WorktreePath, runDir, state.BaseSHA, parentSHA, evidence.task.Metadata.Title, state, evidence.decision, current, evidence.candidates)
		if commitErr != nil || recomputed != intent.CommitSHA {
			return block(store, lease, state, runDir, errors.New("persisted PublicationIntent commit does not match accepted snapshot"))
		}
	}
	publicationRecord, err := input.Publisher.Publish(ctx, mustRecord(intent))
	if err != nil {
		_ = writeDiagnostic(filepath.Join(runDir, "publication-error.json"), err)
		if port.IsPermanent(err) {
			return block(store, lease, state, runDir, err)
		}
		return Result{State: state}, err
	}
	if publicationRecord.Kind != domain.KindPublicationRecord || input.Validator.Validate(domain.KindPublicationRecord, publicationRecord.Data) != nil {
		return block(store, lease, state, runDir, errors.New("publisher returned an invalid PublicationRecord"))
	}
	var published domain.PublicationRecord
	if err := json.Unmarshal(publicationRecord.Data, &published); err != nil {
		return block(store, lease, state, runDir, err)
	}
	if !publicationMatchesIntent(published, intent) {
		return block(store, lease, state, runDir, errors.New("PublicationRecord identity does not match intent"))
	}
	recordPath := filepath.Join(runDir, "publication-record.json")
	if existing, existingErr := existingPublicationRecord(recordPath, input.Validator); existingErr == nil {
		if !samePublicationIdentity(existing, published) {
			return block(store, lease, state, runDir, errors.New("existing PublicationRecord differs from reconciled remote identity"))
		}
		published = existing
		publicationRecord = mustPublicationRecord(existing)
	} else if !errors.Is(existingErr, os.ErrNotExist) {
		return block(store, lease, state, runDir, existingErr)
	} else if err := atomicWrite(recordPath, append(publicationRecord.Data, '\n')); err != nil {
		return Result{}, err
	}
	_ = os.Remove(filepath.Join(runDir, "publication-error.json"))
	recordDigest, _ := canonical.DigestJSON(publicationRecord.Data)
	completedPayload := map[string]any{
		"publicationDigest": recordDigest, "provider": published.Provider, "repository": published.Repository.NameWithOwner,
		"headBranch": published.HeadBranch, "baseBranch": published.BaseBranch, "externalId": published.Request.ID,
		"headSha": published.HeadSHA, "uri": published.Request.URL,
	}
	if evidence.candidates.present() {
		completedPayload["candidateDigest"] = evidence.candidates.head
		completedPayload["workerCandidateDigest"] = evidence.candidates.worker
	}
	event, next, err := transition(state, "publication.completed", domain.StatePublished, completedPayload, lifecycle.Guard{LeaseHeld: true, PublicationCurrent: true})
	if err != nil {
		return Result{}, err
	}
	next.Publication = &domain.RunPublication{Provider: "github", Repository: published.Repository.NameWithOwner, HeadBranch: published.HeadBranch, BaseBranch: published.BaseBranch, ExternalID: published.Request.ID, URI: published.Request.URL, HeadSHA: published.HeadSHA}
	if err := store.Append(lease, event, state.Sequence); err != nil {
		return Result{}, err
	}
	if err := store.WriteSnapshot(lease, next); err != nil {
		return Result{}, err
	}
	state = next
	if len(evidence.task.Publication.RequiredChecks) > 0 {
		event, next, err = transition(state, "publication.checks-requested", domain.StateCIPending, map[string]any{"requiredChecks": evidence.task.Publication.RequiredChecks}, lifecycle.Guard{LeaseHeld: true, RemoteChecksRequired: true})
	} else {
		event, next, err = transition(state, "publication.accepted", domain.StateAccepted, map[string]any{"terminalReason": "published draft PR; no remote checks required"}, lifecycle.Guard{LeaseHeld: true, EvidenceCurrent: true, RequiredGatesPass: true})
	}
	if err != nil {
		return Result{}, err
	}
	next.Publication = state.Publication
	var preparedOutcome *review.PreparedRecords
	if next.State == domain.StateAccepted {
		preparedOutcome, err = prepareOutcome(runDir, input.Validator, next, "published draft PR; no remote checks required", intent.ReviewDecisionDigest, intent.EvidenceDigest)
		if err != nil {
			return Result{}, err
		}
	}
	if err := store.Append(lease, event, state.Sequence); err != nil {
		if preparedOutcome != nil {
			preparedOutcome.Abort()
		}
		return Result{}, err
	}
	if preparedOutcome != nil {
		if err := preparedOutcome.Commit(); err != nil {
			return Result{}, err
		}
	}
	if err := store.WriteSnapshot(lease, next); err != nil {
		return Result{}, err
	}
	return Result{State: next, Publication: published}, nil
}

type evidenceSet struct {
	task           domain.TaskSpec
	report         verification.Report
	decision       domain.ReviewDecision
	decisionDigest string
	candidates     candidateBinding
}

// candidateBinding carries the optional ADR 0027 Candidate identities bound
// to a publication generation. PublicationIntent and PublicationRecord keep
// their frozen field set (their schemas admit no additional members), so the
// binding is appended instead of substituted: it travels with the controlled
// commit trailers and the publication.completed journal payload, while every
// legacy binding field keeps its exact semantics (§4.2 stage A, §5.1).
type candidateBinding struct {
	head   string
	worker string
}

func (b candidateBinding) present() bool { return b.head != "" }

// candidateBindingForPublication cross-checks the Candidate identities
// carried by the verification report and the review packet fail-closed:
// candidate mode is all-or-nothing, and every present identity must agree
// field by field across the two records. Legacy evidence without Candidate
// records yields the empty binding and leaves publication behavior
// byte-identical to pre-adoption runs.
func candidateBindingForPublication(report verification.Report, packet domain.ReviewPacket) (candidateBinding, error) {
	if report.CandidateDigest == "" && report.WorkerCandidateDigest == "" && packet.CandidateDigest == "" && packet.WorkerCandidateDigest == "" {
		return candidateBinding{}, nil
	}
	if report.CandidateDigest == "" || report.WorkerCandidateDigest == "" || packet.CandidateDigest == "" || packet.WorkerCandidateDigest == "" {
		return candidateBinding{}, errors.New("publication evidence carries a partial candidate binding")
	}
	if report.CandidateDigest != packet.CandidateDigest || report.WorkerCandidateDigest != packet.WorkerCandidateDigest {
		return candidateBinding{}, errors.New("publication candidate binding differs between verification report and review packet")
	}
	return candidateBinding{head: report.CandidateDigest, worker: report.WorkerCandidateDigest}, nil
}

func loadEvidence(runDir string, state domain.RunState, validator *contract.Validator, store *runstore.Store) (evidenceSet, error) {
	frozen, err := frozenEvidence(store, state.RunID)
	if err != nil {
		return evidenceSet{}, err
	}
	read := func(name string, kind domain.Kind) ([]byte, error) {
		data, err := os.ReadFile(filepath.Join(runDir, name))
		if err != nil {
			return nil, err
		}
		if err := validator.Validate(kind, data); err != nil {
			return nil, err
		}
		return data, nil
	}
	taskData, err := read("task-spec.json", domain.KindTask)
	if err != nil {
		return evidenceSet{}, err
	}
	if digest, _ := canonical.DigestJSON(taskData); digest != state.SpecDigest {
		return evidenceSet{}, errors.New("TaskSpec digest mismatch")
	}
	var task domain.TaskSpec
	if err := json.Unmarshal(taskData, &task); err != nil {
		return evidenceSet{}, err
	}
	policyData, err := read("policy-snapshot.json", domain.KindPolicySnapshot)
	if err != nil {
		return evidenceSet{}, err
	}
	if digest, _ := canonical.DigestJSON(policyData); digest != state.PolicyDigest {
		return evidenceSet{}, errors.New("PolicySnapshot digest mismatch")
	}
	var policy struct {
		Effective struct {
			AllowPublication bool `json:"allowPublication"`
		} `json:"effective"`
	}
	_ = json.Unmarshal(policyData, &policy)
	if !policy.Effective.AllowPublication {
		return evidenceSet{}, errors.New("frozen policy forbids publication")
	}
	reportData, err := read("verification-report.json", domain.KindVerificationReport)
	if err != nil {
		return evidenceSet{}, err
	}
	var report verification.Report
	if err := json.Unmarshal(reportData, &report); err != nil {
		return evidenceSet{}, err
	}
	if report.Status != "pass" || report.TaskID != state.TaskID || report.RunID != state.RunID || report.SpecDigest != state.SpecDigest || report.BaseSHA != state.BaseSHA {
		return evidenceSet{}, errors.New("VerificationReport is not current and passing")
	}
	for _, gate := range report.Gates {
		if gate.Required && gate.Status != "pass" {
			return evidenceSet{}, fmt.Errorf("required gate %s did not pass", gate.ID)
		}
	}
	manifestData, err := read("artifact-manifest.json", domain.KindArtifactManifest)
	if err != nil {
		return evidenceSet{}, err
	}
	packetData, err := read("review-packet.json", domain.KindReviewPacket)
	if err != nil {
		return evidenceSet{}, err
	}
	_, decision, decisionDigest, err := authorizePublicationDecision(runDir, state, validator, frozen, task.Publication.MergePolicy)
	if err != nil {
		return evidenceSet{}, err
	}
	var packet domain.ReviewPacket
	_ = json.Unmarshal(packetData, &packet)
	reportDigest, _ := canonical.DigestJSON(reportData)
	manifestDigest, _ := canonical.DigestJSON(manifestData)
	packetDigest, _ := canonical.DigestJSON(packetData)
	if reportDigest != frozen.reportDigest || manifestDigest != frozen.manifestDigest {
		return evidenceSet{}, errors.New("publication evidence differs from frozen lifecycle event")
	}
	if packet.TaskID != state.TaskID || packet.RunID != state.RunID || packet.ReviewRound != state.ReviewRound || packet.SpecDigest != state.SpecDigest || packet.BaseSHA != state.BaseSHA || packet.SnapshotDigest != report.Observed.SnapshotDigest || packet.DiffDigest != report.Observed.DiffDigest {
		return evidenceSet{}, errors.New("ReviewPacket does not bind the verified snapshot")
	}
	if decision.VerificationDigest != reportDigest || decision.ArtifactManifestDigest != manifestDigest || decision.ReviewPacketDigest != packetDigest || decision.EvidenceDigest != packet.EvidenceDigest {
		return evidenceSet{}, errors.New("review evidence digest binding mismatch")
	}
	candidates, err := candidateBindingForPublication(report, packet)
	if err != nil {
		return evidenceSet{}, err
	}
	return evidenceSet{task: task, report: report, decision: decision, decisionDigest: decisionDigest, candidates: candidates}, nil
}

// authorizePublicationDecision verifies the current review round's
// ReviewDecision against the frozen lifecycle evidence: the round-bound file
// must be schema-valid, its canonical digest must equal the frozen
// review.accept decisionDigest, its evidence digest must equal the frozen
// evidenceDigest, and the verdict must authorize publication with no
// blocking findings. It is shared by the live publication gate and the ADR
// 0026 reconcile current-ledger recheck so neither path can drift.
func authorizePublicationDecision(runDir string, state domain.RunState, validator *contract.Validator, frozen frozenPublicationEvidence, mergePolicy string) ([]byte, domain.ReviewDecision, string, error) {
	decisionData, err := os.ReadFile(filepath.Join(runDir, "decisions", fmt.Sprintf("decision-%03d.json", state.ReviewRound)))
	if err != nil {
		return nil, domain.ReviewDecision{}, "", err
	}
	if err := validator.Validate(domain.KindReviewDecision, decisionData); err != nil {
		return nil, domain.ReviewDecision{}, "", err
	}
	decisionDigest, err := canonical.DigestJSON(decisionData)
	if err != nil {
		return nil, domain.ReviewDecision{}, "", err
	}
	if decisionDigest != frozen.decisionDigest {
		return nil, domain.ReviewDecision{}, "", errors.New("publication evidence differs from frozen lifecycle event")
	}
	var decision domain.ReviewDecision
	if err := json.Unmarshal(decisionData, &decision); err != nil {
		return nil, domain.ReviewDecision{}, "", err
	}
	if decision.EvidenceDigest != frozen.evidenceDigest {
		return nil, domain.ReviewDecision{}, "", errors.New("publication evidence differs from frozen lifecycle event")
	}
	expectedMergeRecommendation := "do-not-merge"
	if mergePolicy == domain.MergePolicyPolicy {
		expectedMergeRecommendation = mergeRecommendationEligible
	}
	if decision.TaskID != state.TaskID || decision.RunID != state.RunID || decision.SpecDigest != state.SpecDigest || decision.Verdict != "accept" || decision.PublicationRecommendation != "publish" || decision.MergeRecommendation != expectedMergeRecommendation || len(decision.BlockingFindings) != 0 {
		return nil, domain.ReviewDecision{}, "", errors.New("ReviewDecision does not authorize publication")
	}
	return decisionData, decision, decisionDigest, nil
}

type frozenPublicationEvidence struct {
	reportDigest, manifestDigest, decisionDigest, evidenceDigest string
}

func frozenEvidence(store *runstore.Store, runID string) (frozenPublicationEvidence, error) {
	events, _, err := store.ReadEvents(runID)
	if err != nil {
		return frozenPublicationEvidence{}, err
	}
	var result frozenPublicationEvidence
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		switch event.Type {
		case "review.accept":
			// Authority binding: only the exact review producer may freeze
			// the decision evidence; an omitted or forged actor can never
			// authorize publication.
			if !actorMatches(event.Actor, "system", "marshal-review") {
				return frozenPublicationEvidence{}, errors.New("review.accept event must be recorded by system/marshal-review")
			}
			if result.decisionDigest == "" {
				result.decisionDigest, _ = event.Payload["decisionDigest"].(string)
				result.evidenceDigest, _ = event.Payload["evidenceDigest"].(string)
			}
		case "verification.completed":
			// Authority binding: only the exact verifier producer may freeze
			// the verification evidence digests.
			if !actorMatches(event.Actor, "system", "marshal-verifier") {
				return frozenPublicationEvidence{}, errors.New("verification.completed event must be recorded by system/marshal-verifier")
			}
			if result.reportDigest == "" {
				result.reportDigest, _ = event.Payload["reportDigest"].(string)
				result.manifestDigest, _ = event.Payload["artifactManifestDigest"].(string)
			}
		}
	}
	if result.reportDigest == "" || result.manifestDigest == "" || result.decisionDigest == "" || result.evidenceDigest == "" {
		return frozenPublicationEvidence{}, errors.New("lifecycle journal lacks frozen publication evidence")
	}
	return result, nil
}

const controlledCommitAttempts = 3

var controlledCommitRetryDelay = 250 * time.Millisecond

var controlledCommitObjectID = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

// errControlledCommitInput marks deterministic controlled-commit failures
// (malformed or missing input objects) that a retry cannot repair.
var errControlledCommitInput = errors.New("controlled commit input rejected")

func controlledCommit(ctx context.Context, worktree, runDir, baseSHA, parentSHA, title string, state domain.RunState, decision domain.ReviewDecision, observation verification.Observation, candidates candidateBinding) (string, error) {
	if !controlledCommitObjectID.MatchString(baseSHA) {
		return "", fmt.Errorf("%w: base %q is not a full object id", errControlledCommitInput, baseSHA)
	}
	if !controlledCommitObjectID.MatchString(parentSHA) {
		return "", fmt.Errorf("%w: parent %q is not a full object id", errControlledCommitInput, parentSHA)
	}
	if err := requireStoredCommit(ctx, worktree, baseSHA, "base"); err != nil {
		return "", err
	}
	if err := requireStoredCommit(ctx, worktree, parentSHA, "parent"); err != nil {
		return "", err
	}
	var lastErr error
	for attempt := 0; attempt < controlledCommitAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt) * controlledCommitRetryDelay):
			}
		}
		commit, err := createControlledCommit(ctx, worktree, runDir, baseSHA, parentSHA, title, state, decision, observation, candidates)
		if err == nil {
			return commit, nil
		}
		if errors.Is(err, errControlledCommitInput) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", err
		}
		lastErr = err
	}
	return "", lastErr
}

func createControlledCommit(ctx context.Context, worktree, runDir, baseSHA, parentSHA, title string, state domain.RunState, decision domain.ReviewDecision, observation verification.Observation, candidates candidateBinding) (string, error) {
	index, err := os.CreateTemp(runDir, ".publication-index-*")
	if err != nil {
		return "", err
	}
	indexPath := index.Name()
	defer os.Remove(indexPath)
	defer os.Remove(indexPath + ".lock")
	if err := index.Close(); err != nil {
		return "", fmt.Errorf("close temporary controlled index: %w", err)
	}
	if err := os.Remove(indexPath); err != nil {
		return "", fmt.Errorf("reset temporary controlled index %s: %w", filepath.Base(indexPath), err)
	}
	if _, err := os.Lstat(indexPath); !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("temporary controlled index %s survived reset", filepath.Base(indexPath))
	}
	environment := controlledGitEnvironment(indexPath, decision.DecidedAt)
	if _, err := gitOutput(ctx, worktree, environment, "read-tree", baseSHA); err != nil {
		return "", err
	}
	if _, err := os.Lstat(indexPath); err != nil {
		return "", fmt.Errorf("git read-tree did not create the controlled index %s: %w", filepath.Base(indexPath), err)
	}
	if err := stageRawChanges(ctx, worktree, environment, observation.ChangedFiles); err != nil {
		return "", err
	}
	staged, err := gitBytes(ctx, worktree, environment, "ls-files", "--stage", "-z")
	if err != nil {
		return "", err
	}
	if err := verifyStagedContent(ctx, worktree, environment, staged); err != nil {
		return "", err
	}
	tree, err := gitOutput(ctx, worktree, environment, "write-tree")
	if err != nil {
		return "", err
	}
	tree = strings.TrimSpace(tree)
	if !controlledCommitObjectID.MatchString(tree) {
		return "", fmt.Errorf("%w: git write-tree returned %q instead of a tree object id", errControlledCommitInput, safeText([]byte(tree)))
	}
	if err := requireStoredObject(ctx, worktree, tree, "tree"); err != nil {
		return "", err
	}
	subject := sanitizeSubject(title)
	trailers := fmt.Sprintf("Marshal-Task: %s\nMarshal-Run: %s\nMarshal-Spec-Digest: %s\nMarshal-Evidence-Digest: %s\nMarshal-Snapshot-Digest: %s\n", state.TaskID, state.RunID, state.SpecDigest, decision.EvidenceDigest, observation.SnapshotDigest)
	if candidates.present() {
		// Appended trailers only: legacy commits keep their exact message
		// bytes, and the candidate identities never replace an existing
		// trailer (design §8 R1).
		trailers += fmt.Sprintf("Marshal-Candidate-Digest: %s\nMarshal-Worker-Candidate-Digest: %s\n", candidates.head, candidates.worker)
	}
	message := fmt.Sprintf("%s\n\n%s", subject, trailers)
	// commit-tree stdout carries exactly one commit object id; stderr may
	// carry hook, warning or advice noise and must never be mixed into it.
	stdout, stderr, err := gitExec(ctx, worktree, environment, &message, "commit-tree", tree, "-p", parentSHA)
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(stdout)
	if !controlledCommitObjectID.MatchString(commit) {
		return "", fmt.Errorf("%w: git commit-tree stdout %q is not a commit object id (stderr: %q)", errControlledCommitInput, safeText([]byte(commit)), safeText([]byte(stderr)))
	}
	if err := requireStoredObject(ctx, worktree, commit, "commit"); err != nil {
		return "", err
	}
	return commit, nil
}

// requireStoredCommit proves that objectID resolves to a commit in the shared
// object store behind the worktree, so a missing base or parent fails with an
// explicit diagnosis instead of an opaque downstream rejection.
func requireStoredCommit(ctx context.Context, worktree, objectID, role string) error {
	if _, err := gitOutput(ctx, worktree, nil, "cat-file", "-e", objectID+"^{commit}"); err != nil {
		return fmt.Errorf("%w: %s object %s is missing from the repository object store", errControlledCommitInput, role, objectID)
	}
	return nil
}

func requireStoredObject(ctx context.Context, worktree, objectID, kind string) error {
	if _, err := gitOutput(ctx, worktree, nil, "cat-file", "-e", objectID); err != nil {
		return fmt.Errorf("%w: %s object %s is missing from the repository object store", errControlledCommitInput, kind, objectID)
	}
	return nil
}

func stageRawChanges(ctx context.Context, worktree string, environment, paths []string) error {
	for _, path := range paths {
		if err := validatePublicationPath(path); err != nil {
			return err
		}
		fullPath := filepath.Join(worktree, filepath.FromSlash(path))
		info, err := os.Lstat(fullPath)
		if errors.Is(err, os.ErrNotExist) {
			if _, err := gitOutput(ctx, worktree, environment, "update-index", "--force-remove", "--", path); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		var mode, blob string
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			mode = "120000"
			target, err := os.Readlink(fullPath)
			if err != nil {
				return err
			}
			blob, err = gitInput(ctx, worktree, environment, target, "hash-object", "-w", "--stdin")
			if err != nil {
				return err
			}
		case info.Mode().IsRegular():
			mode = "100644"
			if info.Mode().Perm()&0o111 != 0 {
				mode = "100755"
			}
			blob, err = gitOutput(ctx, worktree, environment, "hash-object", "-w", "--no-filters", "--", path)
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported publication file type: %s", path)
		}
		if _, err := gitOutput(ctx, worktree, environment, "update-index", "--add", "--cacheinfo", mode, strings.TrimSpace(blob), path); err != nil {
			return err
		}
	}
	return nil
}

func validatePublicationPath(path string) error {
	clean := filepath.Clean(filepath.FromSlash(path))
	if path == "" || path == ".marshal" || strings.HasPrefix(path, ".marshal/") || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.ToSlash(clean) != path {
		return fmt.Errorf("forbidden publication path %q", path)
	}
	return nil
}

func verifyStagedContent(ctx context.Context, worktree string, environment []string, data []byte) error {
	for _, record := range strings.Split(string(data), "\x00") {
		if record == "" {
			continue
		}
		metadata, path, ok := strings.Cut(record, "\t")
		if !ok {
			return errors.New("malformed staged entry")
		}
		fields := strings.Fields(metadata)
		if len(fields) != 3 || fields[2] != "0" {
			return errors.New("unmerged or malformed staged entry")
		}
		if err := validatePublicationPath(path); err != nil {
			return err
		}
		if fields[0] == "160000" {
			return errors.New("submodule publication is not supported in M5")
		}
		if fields[0] != "100644" && fields[0] != "100755" && fields[0] != "120000" {
			return fmt.Errorf("unsupported Git mode %s", fields[0])
		}
		var raw string
		var err error
		if fields[0] == "120000" {
			target, readErr := os.Readlink(filepath.Join(worktree, filepath.FromSlash(path)))
			if readErr != nil {
				return fmt.Errorf("read staged symlink %s: %w", path, readErr)
			}
			raw, err = gitInput(ctx, worktree, environment, target, "hash-object", "--stdin")
		} else {
			raw, err = gitOutput(ctx, worktree, environment, "hash-object", "--no-filters", "--", path)
		}
		if err != nil || strings.TrimSpace(raw) != fields[1] {
			return fmt.Errorf("staged blob differs from raw worktree content: %s", path)
		}
	}
	return nil
}

func controlledGitEnvironment(index string, decidedAt time.Time) []string {
	result := baseGitEnvironment()
	return append(result, "GIT_INDEX_FILE="+index, "GIT_AUTHOR_NAME=Marshal Publisher", "GIT_AUTHOR_EMAIL=marshal@localhost.invalid", "GIT_COMMITTER_NAME=Marshal Publisher", "GIT_COMMITTER_EMAIL=marshal@localhost.invalid", "GIT_AUTHOR_DATE="+decidedAt.UTC().Format(time.RFC3339), "GIT_COMMITTER_DATE="+decidedAt.UTC().Format(time.RFC3339))
}

func baseGitEnvironment() []string {
	result := []string{"LC_ALL=C", "LANG=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=/dev/null", "GIT_CONFIG_KEY_1=credential.helper", "GIT_CONFIG_VALUE_1="}
	for _, key := range []string{"HOME", "PATH", "TMPDIR"} {
		if value := os.Getenv(key); value != "" {
			result = append(result, key+"="+value)
		}
	}
	return result
}

func gitOutput(ctx context.Context, directory string, environment []string, args ...string) (string, error) {
	stdout, _, err := gitExec(ctx, directory, environment, nil, args...)
	return stdout, err
}
func gitInput(ctx context.Context, directory string, environment []string, input string, args ...string) (string, error) {
	stdout, _, err := gitExec(ctx, directory, environment, &input, args...)
	return stdout, err
}
func gitBytes(ctx context.Context, directory string, environment []string, args ...string) ([]byte, error) {
	stdout, _, err := gitExec(ctx, directory, environment, nil, args...)
	return []byte(stdout), err
}

// gitExec runs git with stdout and stderr kept strictly apart: stdout is the
// only source of command data (object ids, index listings), while stderr only
// ever surfaces inside error diagnostics. This prevents hook, warning or
// advice noise from polluting SHA extraction.
func gitExec(ctx context.Context, directory string, environment []string, input *string, args ...string) (string, string, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", "", err
	}
	command := exec.CommandContext(ctx, gitPath, args...)
	command.Dir = directory
	if environment == nil {
		environment = baseGitEnvironment()
	}
	command.Env = environment
	if input != nil {
		command.Stdin = strings.NewReader(*input)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if runErr := command.Run(); runErr != nil {
		details := safeText(stderr.Bytes())
		if details == "" {
			details = safeText(stdout.Bytes())
		}
		return "", "", fmt.Errorf("git %s: %w: %s", args[0], runErr, details)
	}
	return stdout.String(), stderr.String(), nil
}

func existingIntent(path string, validator *contract.Validator) (domain.PublicationIntent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.PublicationIntent{}, err
	}
	if err := validator.Validate(domain.KindPublicationIntent, data); err != nil {
		return domain.PublicationIntent{}, err
	}
	var intent domain.PublicationIntent
	err = json.Unmarshal(data, &intent)
	return intent, err
}
func validateIntent(intent domain.PublicationIntent, state domain.RunState, evidence evidenceSet, repository, remoteURL, branch, previousHeadSHA string, observation verification.Observation) error {
	if intent.APIVersion != domain.APIVersionV1Alpha1 || intent.Kind != domain.KindPublicationIntent ||
		intent.TaskID != state.TaskID || intent.RunID != state.RunID || intent.Provider != domain.PublicationProviderGitHub ||
		intent.Repository != repository || intent.Remote != evidence.task.Publication.Remote || intent.RemoteURL != remoteURL ||
		intent.BaseBranch != evidence.task.Publication.BaseBranch || intent.HeadBranch != branch || intent.ReviewRound != state.ReviewRound ||
		intent.BaseSHA != state.BaseSHA || intent.PreviousHeadSHA != previousHeadSHA ||
		intent.SnapshotDigest != observation.SnapshotDigest || intent.DiffDigest != observation.DiffDigest ||
		intent.SpecDigest != state.SpecDigest || intent.PolicyDigest != state.PolicyDigest ||
		intent.EvidenceDigest != evidence.decision.EvidenceDigest || intent.VerificationDigest != evidence.decision.VerificationDigest ||
		intent.ReviewDecisionDigest != evidence.decisionDigest || intent.Marker != marker(state.TaskID, state.RunID) ||
		intent.Mode != domain.PublicationModeDraft || intent.MergePolicy != evidence.task.Publication.MergePolicy ||
		!regexp.MustCompile(`^[0-9a-f]{40,64}$`).MatchString(intent.CommitSHA) {
		return errors.New("persisted PublicationIntent is stale or incomplete")
	}
	return nil
}

func priorIntentCanRotate(intent domain.PublicationIntent, published domain.PublicationRecord, state domain.RunState, repository, remoteURL, branch string) bool {
	return state.Publication != nil && state.ReviewRound > intent.ReviewRound && intent.ReviewRound == published.ReviewRound &&
		intent.TaskID == state.TaskID && intent.RunID == state.RunID && intent.Repository == repository && intent.RemoteURL == remoteURL &&
		intent.HeadBranch == branch && intent.CommitSHA == published.HeadSHA && publicationMatchesIntent(published, intent) &&
		state.Publication.ExternalID == published.Request.ID && state.Publication.HeadSHA == published.HeadSHA
}

func archivePublicationGeneration(runDir string, intent domain.PublicationIntent) error {
	return archivePublicationArtifacts(runDir, intent.ReviewRound, intent.CommitSHA)
}

func archivePublicationArtifacts(runDir string, reviewRound uint, headSHA string) error {
	if reviewRound == 0 || !regexp.MustCompile(`^[0-9a-f]{40,64}$`).MatchString(headSHA) {
		return errors.New("cannot archive publication generation with invalid identity")
	}
	directory := filepath.Join(runDir, "publications", fmt.Sprintf("review-%03d-%s", reviewRound, headSHA[:12]))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	for _, name := range []string{"publication-record.json", "remote-check-record.json", "publication-error.json", "publication-intent.json"} {
		source, destination := filepath.Join(runDir, name), filepath.Join(directory, name)
		if _, err := os.Lstat(source); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if _, err := os.Lstat(destination); err == nil {
			sourceData, sourceErr := os.ReadFile(source)
			destinationData, destinationErr := os.ReadFile(destination)
			if sourceErr != nil || destinationErr != nil || !bytes.Equal(sourceData, destinationData) {
				return fmt.Errorf("publication archive contains conflicting %s", name)
			}
			if err := os.Remove(source); err != nil {
				return err
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(source, destination); err != nil {
			return err
		}
	}
	return nil
}

func existingPublicationRecord(path string, validator *contract.Validator) (domain.PublicationRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.PublicationRecord{}, err
	}
	if err := validator.Validate(domain.KindPublicationRecord, data); err != nil {
		return domain.PublicationRecord{}, err
	}
	var record domain.PublicationRecord
	err = json.Unmarshal(data, &record)
	return record, err
}

// currentPublicationData resolves and reads the current PublicationRecord
// bytes for a run: the live publication-record.json, or the unique archived
// generation matching the snapshot head SHA. Shared by loadCurrentPublication
// and the ADR 0026 reconcile current-ledger recheck.
func currentPublicationData(runDir string, state domain.RunState) ([]byte, error) {
	path := filepath.Join(runDir, "publication-record.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && state.Publication != nil && regexp.MustCompile(`^[0-9a-f]{40,64}$`).MatchString(state.Publication.HeadSHA) {
		matches, globErr := filepath.Glob(filepath.Join(runDir, "publications", "review-*-"+state.Publication.HeadSHA[:12], "publication-record.json"))
		if globErr != nil {
			return nil, globErr
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf("cannot reconcile archived PublicationRecord: found %d candidates", len(matches))
		}
		data, err = os.ReadFile(matches[0])
	}
	return data, err
}

func loadCurrentPublication(runDir string, state domain.RunState, validator *contract.Validator, store *runstore.Store) (*domain.PublicationRecord, error) {
	data, err := currentPublicationData(runDir, state)
	if err != nil {
		return nil, err
	}
	if err := validator.Validate(domain.KindPublicationRecord, data); err != nil {
		return nil, err
	}
	var record domain.PublicationRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, err
	}
	digest, _ := canonical.DigestJSON(data)
	frozenDigest, err := frozenPublicationDigest(store, state.RunID)
	if err != nil || digest != frozenDigest || state.Publication.HeadSHA != record.HeadSHA || state.Publication.ExternalID != record.Request.ID || state.Publication.Repository != record.Repository.NameWithOwner || record.TaskID != state.TaskID || record.RunID != state.RunID {
		return nil, errors.New("current PublicationRecord differs from frozen lifecycle identity")
	}
	return &record, nil
}

func frozenPublicationDigest(store *runstore.Store, runID string) (string, error) {
	events, _, err := store.ReadEvents(runID)
	if err != nil {
		return "", err
	}
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type == "publication.completed" {
			// Authority binding: only the exact publisher producer may
			// freeze the PublicationRecord digest; an omitted or forged
			// actor fails closed instead of anchoring CI observation.
			if !actorMatches(events[index].Actor, "publisher", "marshal-github-publisher") {
				return "", errors.New("publication.completed event must be recorded by publisher/marshal-github-publisher")
			}
			digest, _ := events[index].Payload["publicationDigest"].(string)
			if digest != "" {
				return digest, nil
			}
		}
	}
	return "", errors.New("lifecycle journal lacks frozen PublicationRecord digest")
}

// actorMatches reports whether the event carries the exact producer actor.
func actorMatches(actor *domain.Actor, actorType, actorID string) bool {
	return actor != nil && actor.Type == actorType && actor.ID == actorID
}

func publicationMatchesIntent(published domain.PublicationRecord, intent domain.PublicationIntent) bool {
	return published.APIVersion == intent.APIVersion && published.Kind == domain.KindPublicationRecord &&
		published.TaskID == intent.TaskID && published.RunID == intent.RunID && published.Provider == intent.Provider &&
		published.Repository.NameWithOwner == intent.Repository && published.Remote == intent.Remote &&
		published.BaseBranch == intent.BaseBranch && published.HeadBranch == intent.HeadBranch && published.ReviewRound == intent.ReviewRound &&
		published.BaseSHA == intent.BaseSHA && published.PreviousHeadSHA == intent.PreviousHeadSHA &&
		published.HeadSHA == intent.CommitSHA && published.CommitSHA == intent.CommitSHA &&
		published.SnapshotDigest == intent.SnapshotDigest && published.DiffDigest == intent.DiffDigest &&
		published.SpecDigest == intent.SpecDigest && published.PolicyDigest == intent.PolicyDigest && published.EvidenceDigest == intent.EvidenceDigest &&
		published.VerificationDigest == intent.VerificationDigest && published.ReviewDecisionDigest == intent.ReviewDecisionDigest &&
		published.Marker == intent.Marker && published.Mode == intent.Mode && published.MergePolicy == intent.MergePolicy &&
		published.Request.Draft && published.Request.State == "OPEN"
}

func samePublicationIdentity(left, right domain.PublicationRecord) bool {
	return left.TaskID == right.TaskID && left.RunID == right.RunID && left.Provider == right.Provider &&
		left.Repository == right.Repository && left.Remote == right.Remote && left.BaseBranch == right.BaseBranch && left.HeadBranch == right.HeadBranch &&
		left.ReviewRound == right.ReviewRound && left.BaseSHA == right.BaseSHA && left.PreviousHeadSHA == right.PreviousHeadSHA &&
		left.HeadSHA == right.HeadSHA && left.CommitSHA == right.CommitSHA && left.SnapshotDigest == right.SnapshotDigest && left.DiffDigest == right.DiffDigest &&
		left.SpecDigest == right.SpecDigest && left.PolicyDigest == right.PolicyDigest && left.EvidenceDigest == right.EvidenceDigest &&
		left.VerificationDigest == right.VerificationDigest && left.ReviewDecisionDigest == right.ReviewDecisionDigest && left.Marker == right.Marker &&
		left.Mode == right.Mode && left.MergePolicy == right.MergePolicy && left.Request == right.Request && left.Actor == right.Actor
}

func mustPublicationRecord(value domain.PublicationRecord) domain.Record {
	data, _ := json.Marshal(value)
	return domain.Record{Kind: domain.KindPublicationRecord, Data: data}
}

func prepareOutcome(runDir string, validator *contract.Validator, state domain.RunState, summary, expectedDecisionDigest, expectedEvidenceDigest string) (*review.PreparedRecords, error) {
	decisionData, err := os.ReadFile(filepath.Join(runDir, "decisions", fmt.Sprintf("decision-%03d.json", state.ReviewRound)))
	if err != nil {
		return nil, err
	}
	if err := validator.Validate(domain.KindReviewDecision, decisionData); err != nil {
		return nil, err
	}
	var decision domain.ReviewDecision
	if err := json.Unmarshal(decisionData, &decision); err != nil {
		return nil, err
	}
	decisionDigest, err := canonical.DigestJSON(decisionData)
	if err != nil {
		return nil, err
	}
	if decision.TaskID != state.TaskID || decision.RunID != state.RunID || decision.Verdict != "accept" || decisionDigest != expectedDecisionDigest || decision.EvidenceDigest != expectedEvidenceDigest {
		return nil, errors.New("terminal Outcome review identity mismatch")
	}
	return review.PrepareOutcome(runDir, review.OutcomeData{
		TaskID: state.TaskID, RunID: state.RunID, TerminalState: state.State, Verdict: decision.Verdict,
		FinalReviewRound: decision.ReviewRound, FinalReviewDigest: decisionDigest, FinalEvidenceDigest: decision.EvidenceDigest,
		Summary: summary, FindingCount: uint(len(decision.BlockingFindings) + len(decision.NonBlockingFindings)), GeneratedAt: state.UpdatedAt,
	})
}
func mustRecord(value domain.PublicationIntent) domain.Record {
	data, _ := json.Marshal(value)
	return domain.Record{Kind: domain.KindPublicationIntent, Data: data}
}
func deriveBranch(taskID, runID string) string {
	safe := regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(taskID, "-")
	safe = strings.Trim(safe, "-.")
	sum := sha256.Sum256([]byte(runID))
	return "marshal/" + safe + "-" + hex.EncodeToString(sum[:6])
}
func marker(taskID, runID string) string {
	return "<!-- marshal task=" + taskID + " run=" + runID + " -->"
}
func sanitizeSubject(value string) string {
	value = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(value))
	if value == "" {
		value = "Marshal task"
	}
	if len(value) > 72 {
		value = value[:72]
	}
	return value
}
func patchCaptureLimit(value int64) int64 {
	if value <= 0 {
		return 64 << 20
	}
	return value + 1
}

func transition(state domain.RunState, eventType string, target domain.State, payload map[string]any, guard lifecycle.Guard) (domain.RunEvent, domain.RunState, error) {
	id, err := domain.NewID("event")
	if err != nil {
		return domain.RunEvent{}, state, err
	}
	event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: id, RunID: state.RunID, Sequence: state.Sequence + 1, Type: eventType, StateFrom: state.State, StateTo: target, Timestamp: time.Now().UTC(), Actor: &domain.Actor{Type: "publisher", ID: "marshal-github-publisher"}, Payload: payload}
	next, err := lifecycle.Reduce(state, event, guard)
	return event, next, err
}
func block(store *runstore.Store, lease *runstore.Lease, state domain.RunState, runDir string, cause error) (Result, error) {
	frozen, frozenErr := frozenEvidence(store, state.RunID)
	var preparedOutcome *review.PreparedRecords
	if frozenErr == nil {
		preparedOutcome, frozenErr = review.PrepareOutcome(runDir, review.OutcomeData{
			TaskID: state.TaskID, RunID: state.RunID, TerminalState: domain.StateBlocked, Verdict: "blocked",
			FinalReviewRound: state.ReviewRound, FinalReviewDigest: frozen.decisionDigest, FinalEvidenceDigest: frozen.evidenceDigest,
			Summary: "publication safety gate failed: " + safeText([]byte(cause.Error())), GeneratedAt: time.Now().UTC(),
		})
	}
	guard := lifecycle.Guard{LeaseHeld: true, PublicationCurrent: state.State == domain.StateCIPending}
	event, next, err := transition(state, "publication.blocked", domain.StateBlocked, map[string]any{"error": safeText([]byte(cause.Error())), "terminalReason": "publication safety gate failed"}, guard)
	if err == nil {
		err = store.Append(lease, event, state.Sequence)
	}
	if err != nil && preparedOutcome != nil {
		preparedOutcome.Abort()
	}
	if err == nil && preparedOutcome != nil {
		err = preparedOutcome.Commit()
	}
	if err == nil {
		err = store.WriteSnapshot(lease, next)
	}
	return Result{State: next}, errors.Join(cause, frozenErr, err)
}
func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".publication-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	_ = file.Chmod(0o600)
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func writeDiagnostic(path string, cause error) error {
	data, _ := json.MarshalIndent(map[string]any{"error": safeText([]byte(cause.Error())), "recordedAt": time.Now().UTC()}, "", "  ")
	return atomicWrite(path, append(data, '\n'))
}
func safeText(data []byte) string {
	data = []byte(strings.ToValidUTF8(string(data), "�"))
	if len(data) > 4096 {
		data = data[len(data)-4096:]
	}
	return strings.TrimSpace(string(data))
}
