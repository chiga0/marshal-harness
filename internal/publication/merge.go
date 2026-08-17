package publication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/port"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// ADR 0032 §1 merge recommendation: the review decision must explicitly
// recommend merge only after policy gates it eligible.
const mergeRecommendationEligible = "eligible-after-policy"

// MergeInput carries every port the controlled merge needs. The SCMMerger is
// the only credentialed mutation port; the observers are strictly read-only.
type MergeInput struct {
	StateRoot, RunID     string
	Merger               port.SCMMerger
	TargetObserver       port.MergeTargetObserver
	CredentialObserver   port.SCMMergeCredentialObserver
	ReceiptObserver      port.MergeReceiptObserver
	CheckObserver        port.RemoteCheckObserver
	AuthorizationRuntime *authority.EdgeRuntime
	AuthorizationSource  authority.SecurityDomainId
	AuthorizationTarget  authority.SecurityDomainId
	Validator            *contract.Validator
	RequestedBy          string
	Now                  time.Time
}

type MergeResult struct {
	State   domain.RunState        `json:"state"`
	Intent  domain.SCMMergeIntent  `json:"scmMergeIntent"`
	Receipt domain.SCMMergeReceipt `json:"scmMergeReceipt"`
}

// mergeAdmission is the fully validated local admission set (ADR 0032 §1
// M1-M9) bound to the current publication generation.
type mergeAdmission struct {
	task              domain.TaskSpec
	decision          domain.ReviewDecision
	decisionDigest    string
	publication       domain.PublicationRecord
	publicationData   []byte
	publicationDigest string
	approval          domain.ApprovalRecord
	approvalDigest    string
}

// Merge executes the ADR 0032 controlled merge: admission, re-observation,
// intent-first persistence, ready + merge, receipt verification and the
// receipt/intent-bound publication.merged convergence. Every remote side
// effect happens strictly after the immutable SCMMergeIntent is persisted,
// and every recovery path re-enters through the same intent instead of blind
// replay.
func Merge(ctx context.Context, input MergeInput) (MergeResult, error) {
	if input.Merger == nil || input.TargetObserver == nil || input.CredentialObserver == nil || input.AuthorizationRuntime == nil ||
		input.ReceiptObserver == nil || input.CheckObserver == nil || input.Validator == nil {
		return MergeResult{}, errors.New("merger, target observer, credential observer, receipt observer, check observer and validator are required")
	}
	requestedBy := strings.TrimSpace(input.RequestedBy)
	if requestedBy == "" || len(requestedBy) > 256 {
		return MergeResult{}, errors.New("requestedBy is required")
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	store := runstore.New(input.StateRoot)
	lease, err := store.Acquire(input.RunID)
	if err != nil {
		return MergeResult{}, err
	}
	defer lease.Release()
	state, err := store.Inspect(input.RunID)
	if err != nil {
		return MergeResult{}, err
	}
	runDir := filepath.Join(input.StateRoot, "runs", input.RunID)

	switch state.State {
	case domain.StateCIPending:
	case domain.StateAccepted:
		// C7: the convergence event already landed; rebuild a missing outcome
		// idempotently and never rewrite an existing one.
		return rebuildMergeConvergence(runDir, store, input.Validator, state)
	default:
		return MergeResult{}, fmt.Errorf("run state %s cannot merge", state.State)
	}

	// T15: a provider that cannot mechanically bind the expected head must
	// BLOCKED, never fall back to before/after observation.
	if !input.Merger.BindsExpectedHead() {
		result, blockedErr := block(store, lease, state, runDir, errors.New("provider cannot mechanically bind the expected head OID to the merge request"))
		return MergeResult{State: result.State}, blockedErr
	}

	// §1 admission (M1-M9) against the frozen local records. A current-ledger
	// drift (digest mismatch) is permanent and blocks; a missing precondition
	// stays a plain rejection with zero remote side effect.
	admission, err := loadMergeAdmission(runDir, store, state, input.Validator)
	if err != nil {
		if port.IsPermanent(err) {
			result, blockedErr := block(store, lease, state, runDir, err)
			return MergeResult{State: result.State}, blockedErr
		}
		return MergeResult{}, err
	}
	ciDeadline := frozenCIDeadline(state.CreatedAt, admission.publication.PublishedAt, admission.task.Budgets)
	if now.Compare(ciDeadline) >= 0 {
		return MergeResult{}, errCIDeadlineExceeded
	}

	// M10: fresh all-green required checks bound to the current head. This
	// runs for both initial and recovery binding (ADR 0032 §2); its digest is
	// frozen into a NEW intent only.
	checkDigest, err := observeFreshChecks(ctx, input, admission, state, runDir)
	if err != nil {
		if port.IsPermanent(err) {
			result, blockedErr := block(store, lease, state, runDir, err)
			return MergeResult{State: result.State}, blockedErr
		}
		return MergeResult{}, err
	}

	authorityNamespaceID, err := reconcileAuthorityNamespaceID(input.StateRoot)
	if err != nil {
		return MergeResult{}, err
	}

	// §3 canonical idempotency identity is derived from frozen records only
	// (authorityNamespaceId, runId, publicationRecordId, headOid,
	// mergeMethod): it never depends on the request time or operator, so a
	// re-entry always resolves to the same intent.
	intentID, err := domain.SCMMergeIntentIdentity{
		AuthorityNamespaceID: authorityNamespaceID,
		RunID:                state.RunID,
		PublicationRecordID:  admission.publicationDigest,
		HeadOid:              admission.publication.HeadSHA,
		MergeMethod:          admission.task.Publication.MergeMethod,
	}.IntentID()
	if err != nil {
		return MergeResult{}, err
	}

	// §2 initial vs recovery binding (T17, C2-C6): recovery must first find
	// and fully validate the existing immutable intent by frozen identity and
	// then continue through it, never re-minting; only a confirmed absence
	// constructs and put-if-absents a new intent.
	existing, found, err := existingMergeIntent(filepath.Join(runDir, "merge-intents"), input.Validator, intentID)
	if err != nil {
		return MergeResult{}, err
	}
	var intent domain.SCMMergeIntent
	if found {
		if err := bindExistingMergeIntent(existing, admission, state, authorityNamespaceID); err != nil {
			result, blockedErr := block(store, lease, state, runDir, err)
			return MergeResult{State: result.State}, blockedErr
		}
		intent = existing
		if err := bindPersistedRemoteChecks(runDir, input.Validator, intent, admission.publication, admission.task.Publication.RequiredChecks, state); err != nil {
			result, blockedErr := block(store, lease, state, runDir, err)
			return MergeResult{State: result.State}, blockedErr
		}
	} else {
		// §2/§4 fresh observation of the authenticated principal + credential
		// resolution identity + SCMMerger security domain, frozen into the
		// new intent.
		principal, credentialDigest, obsErr := input.CredentialObserver.ObserveCredentialIdentity(ctx)
		if obsErr != nil {
			if port.IsPermanent(obsErr) {
				result, blockedErr := block(store, lease, state, runDir, obsErr)
				return MergeResult{State: result.State}, blockedErr
			}
			return MergeResult{}, obsErr
		}
		if principal == "" || credentialDigest == "" {
			cause := port.Permanent(errors.New("credential identity observation is empty"))
			result, blockedErr := block(store, lease, state, runDir, cause)
			return MergeResult{State: result.State}, blockedErr
		}
		mergerSecurityDomainID := input.Merger.SecurityDomainID()
		if mergerSecurityDomainID == "" {
			cause := port.Permanent(errors.New("merge executor security domain is empty"))
			result, blockedErr := block(store, lease, state, runDir, cause)
			return MergeResult{State: result.State}, blockedErr
		}
		intent = domain.SCMMergeIntent{
			APIVersion:               domain.APIVersionV1Alpha1,
			Kind:                     domain.KindSCMMergeIntent,
			AuthorityNamespaceID:     authorityNamespaceID,
			TaskID:                   state.TaskID,
			RunID:                    state.RunID,
			PublicationRecordID:      admission.publicationDigest,
			PublicationDigest:        admission.publicationDigest,
			ReviewDecisionDigest:     admission.decisionDigest,
			VerificationDigest:       admission.decision.VerificationDigest,
			EvidenceDigest:           admission.decision.EvidenceDigest,
			PolicyDigest:             state.PolicyDigest,
			PublishApprovalRecordID:  admission.approval.RecordID,
			PublishApprovalDigest:    admission.approvalDigest,
			RemoteCheckRecordDigest:  checkDigest,
			RepositoryRef:            admission.publication.Repository.NameWithOwner,
			PRNumber:                 admission.publication.Request.Number,
			HeadOid:                  admission.publication.HeadSHA,
			BaseOid:                  state.BaseSHA,
			MergeMethod:              admission.task.Publication.MergeMethod,
			RequestedBy:              requestedBy,
			RequestedAt:              now,
			ExpectedMergedBy:         principal,
			MergerSecurityDomainID:   mergerSecurityDomainID,
			MergerCredentialIdentity: credentialDigest,
		}
		intent.IntentID = intentID
		intentDigest, digestErr := intent.Digest()
		if digestErr != nil {
			return MergeResult{}, digestErr
		}
		intent.IntentDigest = intentDigest
		if err := intent.Validate(); err != nil {
			return MergeResult{}, err
		}
	}

	// §2 fresh target observation (repository, PR, head, base, marker). Any
	// identity/marker/closed drift is permanent and blocks before any side
	// effect.
	target, err := input.TargetObserver.ObserveTarget(ctx, intent)
	if err != nil {
		if port.IsPermanent(err) {
			result, blockedErr := block(store, lease, state, runDir, err)
			return MergeResult{State: result.State}, blockedErr
		}
		return MergeResult{}, err
	}
	if err := validateMergeTarget(target, intent, admission.publication.BaseBranch); err != nil {
		result, blockedErr := block(store, lease, state, runDir, err)
		return MergeResult{State: result.State}, blockedErr
	}

	// §2 initial binding requires Draft; a ready PR with no prior intent can
	// never be merged by back-filling an intent afterwards (T17).
	if !found && !target.Draft {
		cause := port.Permanent(errors.New("initial merge admission requires the PR to remain Draft with the run marker"))
		result, blockedErr := block(store, lease, state, runDir, cause)
		return MergeResult{State: result.State}, blockedErr
	}

	// §4 execution-time security domain re-observation gate: the actual merge
	// executor must still belong to the intent-bound security domain before
	// any mutation.
	if input.Merger.SecurityDomainID() != intent.MergerSecurityDomainID {
		result, blockedErr := block(store, lease, state, runDir, port.Permanent(errors.New("merge executor security domain does not bind the intent")))
		return MergeResult{State: result.State}, blockedErr
	}

	// ADR 0018/0032: controlled merge consumes a Core-issued
	// PublicationAuthorization. Issuance binds the exact intent/review/evidence
	// chain, expected GitHub principal and Publication security domain; every
	// mutation below performs a current-ledger recheck of this record.
	authorization, err := loadOrIssueMergeAuthorization(input, intent, admission, ciDeadline, now, runDir, found)
	if err != nil {
		result, blockedErr := block(store, lease, state, runDir, port.Permanent(err))
		return MergeResult{State: result.State}, blockedErr
	}
	// Persisting the deterministic authorization first closes the crash cut
	// between intent persistence and authorization issuance. A crash here
	// leaves an idempotently replayable authorization but no remote effect;
	// intent-first still holds because both records precede every mutation.
	if !found {
		intent, _, err = persistMergeIntent(runDir, input.Validator, intent)
		if err != nil {
			return MergeResult{}, err
		}
	}

	receipt, err := executeMergeSideEffects(ctx, input, admission, intent, target, authorization, runDir)
	if err != nil {
		if port.IsPermanent(err) {
			result, blockedErr := block(store, lease, state, runDir, err)
			return MergeResult{State: result.State}, blockedErr
		}
		return MergeResult{State: state}, err
	}
	if err := recheckMergeAuthorization(input, authorization, intent, "converge", time.Now().UTC()); err != nil {
		result, blockedErr := block(store, lease, state, runDir, port.Permanent(fmt.Errorf("%w: %v", port.ErrMergeIdentityMismatch, err)))
		return MergeResult{State: result.State}, blockedErr
	}

	// §6 journal CAS: append the receipt-bound publication.merged event, then
	// stage the no-replace outcome and write the ACCEPTED snapshot.
	event, next, err := mergeTransition(state, map[string]any{
		"intentId":                intent.IntentID,
		"intentDigest":            intent.IntentDigest,
		"receiptId":               receipt.ReceiptID,
		"receiptDigest":           receipt.ReceiptDigest,
		"headOid":                 intent.HeadOid,
		"mergeCommitSha":          receipt.MergeCommitSha,
		"mergeMethod":             intent.MergeMethod,
		"publicationDigest":       admission.publicationDigest,
		"remoteCheckRecordDigest": intent.RemoteCheckRecordDigest,
	}, lifecycle.Guard{LeaseHeld: true, MergeAuthorized: true, EvidenceCurrent: true, PublicationCurrent: true})
	if err != nil {
		return MergeResult{}, err
	}
	next.Publication = state.Publication
	if err := store.Append(lease, event, state.Sequence); err != nil {
		return MergeResult{}, err
	}
	preparedOutcome, err := prepareMergeOutcome(runDir, input.Validator, next, "merged by Marshal after required checks", admission.decisionDigest, admission.decision.EvidenceDigest, intent.IntentDigest, receipt.ReceiptDigest)
	if err != nil {
		return MergeResult{}, err
	}
	if err := preparedOutcome.Commit(); err != nil {
		return MergeResult{}, err
	}
	if err := store.WriteSnapshot(lease, next); err != nil {
		return MergeResult{}, err
	}
	return MergeResult{State: next, Intent: intent, Receipt: receipt}, nil
}

// bindExistingMergeIntent fully re-binds an already-persisted immutable intent
// to the current frozen admission before re-entering (ADR 0032 §2 recovery
// binding). The canonical idempotency tuple already pins authorityNamespaceId,
// runId, publicationRecordId, headOid and mergeMethod; this verifies the
// remaining frozen anchors (task, base, evidence and authorization digests) so
// a stale or drifted intent fails closed without any side effect.
func bindExistingMergeIntent(intent domain.SCMMergeIntent, admission mergeAdmission, state domain.RunState, authorityNamespaceID string) error {
	if intent.AuthorityNamespaceID != authorityNamespaceID || intent.RunID != state.RunID || intent.TaskID != state.TaskID {
		return port.Permanent(errors.New("existing merge intent identity does not bind the run"))
	}
	if intent.PublicationRecordID != admission.publicationDigest || intent.PublicationDigest != admission.publicationDigest {
		return port.Permanent(errors.New("existing merge intent does not bind the current publication generation"))
	}
	if intent.HeadOid != admission.publication.HeadSHA || intent.BaseOid != state.BaseSHA || intent.MergeMethod != admission.task.Publication.MergeMethod {
		return port.Permanent(errors.New("existing merge intent head, base branch, base OID or method does not bind the frozen anchors"))
	}
	if intent.ReviewDecisionDigest != admission.decisionDigest || intent.VerificationDigest != admission.decision.VerificationDigest ||
		intent.EvidenceDigest != admission.decision.EvidenceDigest || intent.PolicyDigest != state.PolicyDigest ||
		intent.PublishApprovalRecordID != admission.approval.RecordID || intent.PublishApprovalDigest != admission.approvalDigest {
		return port.Permanent(errors.New("existing merge intent evidence or authorization no longer binds the current admission"))
	}
	return nil
}

// loadMergeAdmission validates the frozen local records for §1 M1-M9. It is
// pure: no network observation and no side effect.
func loadMergeAdmission(runDir string, store *runstore.Store, state domain.RunState, validator *contract.Validator) (mergeAdmission, error) {
	var admission mergeAdmission
	frozen, err := frozenEvidence(store, state.RunID)
	if err != nil {
		return admission, err
	}
	taskData, err := os.ReadFile(filepath.Join(runDir, "task-spec.json"))
	if err != nil {
		return admission, err
	}
	if err := validator.Validate(domain.KindTask, taskData); err != nil {
		return admission, err
	}
	if digest, _ := canonical.DigestJSON(taskData); digest != state.SpecDigest {
		return admission, port.Permanent(errors.New("TaskSpec digest mismatch"))
	}
	var task domain.TaskSpec
	if err := json.Unmarshal(taskData, &task); err != nil {
		return admission, err
	}
	// M1, M3, M4.
	if task.Publication.MergePolicy != domain.MergePolicyPolicy {
		return admission, errors.New("merge admission requires mergePolicy=policy")
	}
	if task.Publication.Provider != domain.PublicationProviderGitHub || task.Publication.Mode != domain.PublicationModeDraft {
		return admission, errors.New("merge admission requires a GitHub draft publication")
	}
	switch task.Publication.MergeMethod {
	case domain.MergeMethodMerge, domain.MergeMethodSquash, domain.MergeMethodRebase:
	default:
		return admission, errors.New("merge admission requires a closed mergeMethod")
	}
	if !nonEmptyUniqueRequiredChecks(task.Publication.RequiredChecks) {
		return admission, errors.New("merge admission requires a non-empty de-duplicated requiredChecks set")
	}
	admission.task = task

	// M2: policy grants both publication and merge, and the digest is frozen.
	policyData, err := os.ReadFile(filepath.Join(runDir, "policy-snapshot.json"))
	if err != nil {
		return admission, err
	}
	if err := validator.Validate(domain.KindPolicySnapshot, policyData); err != nil {
		return admission, err
	}
	if digest, _ := canonical.DigestJSON(policyData); digest != state.PolicyDigest {
		return admission, port.Permanent(errors.New("PolicySnapshot digest mismatch"))
	}
	var policy struct {
		Effective struct {
			AllowPublication bool `json:"allowPublication"`
			AllowMerge       bool `json:"allowMerge"`
		} `json:"effective"`
	}
	if err := json.Unmarshal(policyData, &policy); err != nil {
		return admission, err
	}
	if !policy.Effective.AllowPublication || !policy.Effective.AllowMerge {
		return admission, errors.New("frozen policy does not authorize merge")
	}

	// M8: current publication generation passes the frozen digest recheck.
	publicationData, err := currentPublicationData(runDir, state)
	if err != nil {
		return admission, err
	}
	if err := validator.Validate(domain.KindPublicationRecord, publicationData); err != nil {
		return admission, err
	}
	publicationDigest, err := canonical.DigestJSON(publicationData)
	if err != nil {
		return admission, err
	}
	frozenDigest, err := frozenPublicationDigest(store, state.RunID)
	if err != nil {
		return admission, err
	}
	if publicationDigest != frozenDigest {
		return admission, port.Permanent(errors.New("current PublicationRecord differs from frozen lifecycle identity"))
	}
	var publication domain.PublicationRecord
	if err := json.Unmarshal(publicationData, &publication); err != nil {
		return admission, err
	}
	if state.Publication == nil || state.Publication.HeadSHA != publication.HeadSHA || state.Publication.ExternalID != publication.Request.ID ||
		state.Publication.Repository != publication.Repository.NameWithOwner || publication.TaskID != state.TaskID || publication.RunID != state.RunID {
		return admission, port.Permanent(errors.New("RunState publication identity mismatch"))
	}
	admission.publication = publication
	admission.publicationData = publicationData
	admission.publicationDigest = publicationDigest

	// M5/M6: current accept ReviewDecision with the merge recommendation and
	// the publication-generation digest binding.
	decisionData, err := os.ReadFile(filepath.Join(runDir, "decisions", fmt.Sprintf("decision-%03d.json", state.ReviewRound)))
	if err != nil {
		return admission, err
	}
	if err := validator.Validate(domain.KindReviewDecision, decisionData); err != nil {
		return admission, err
	}
	decisionDigest, err := canonical.DigestJSON(decisionData)
	if err != nil {
		return admission, err
	}
	if decisionDigest != frozen.decisionDigest {
		return admission, port.Permanent(errors.New("ReviewDecision differs from frozen lifecycle event"))
	}
	var decision domain.ReviewDecision
	if err := json.Unmarshal(decisionData, &decision); err != nil {
		return admission, err
	}
	if decision.TaskID != state.TaskID || decision.RunID != state.RunID || decision.SpecDigest != state.SpecDigest ||
		decision.Verdict != "accept" || decision.PublicationRecommendation != "publish" ||
		decision.MergeRecommendation != mergeRecommendationEligible || len(decision.BlockingFindings) != 0 {
		return admission, errors.New("ReviewDecision does not authorize a controlled merge")
	}
	if decision.ReviewRound != publication.ReviewRound || decisionDigest != publication.ReviewDecisionDigest ||
		decision.VerificationDigest != publication.VerificationDigest || decision.EvidenceDigest != publication.EvidenceDigest {
		return admission, errors.New("ReviewDecision does not bind the current publication generation")
	}
	if decision.VerificationDigest != frozen.reportDigest || decision.EvidenceDigest != frozen.evidenceDigest {
		return admission, port.Permanent(errors.New("ReviewDecision evidence differs from frozen lifecycle event"))
	}
	admission.decision = decision
	admission.decisionDigest = decisionDigest

	// M7: a current human publish ApprovalRecord bound to the same decision
	// and evidence digests.
	records, err := store.ReadControlRecords(state.RunID, validator)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return admission, err
	}
	approval, err := authorizeMergeApproval(records, state, decisionDigest, decision.EvidenceDigest, decision.ReviewRound)
	if err != nil {
		return admission, err
	}
	admission.approval = approval
	approvalData, err := json.Marshal(approval)
	if err != nil {
		return admission, err
	}
	admission.approvalDigest, err = canonical.DigestJSON(approvalData)
	if err != nil {
		return admission, err
	}
	return admission, nil
}

// authorizeMergeApproval finds the current human publish ApprovalRecord bound
// to the exact decision digest, evidence digest and review round, with the
// frozen input digests still matching (ADR 0010 input-change invalidation).
func authorizeMergeApproval(records []runstore.ControlRecord, state domain.RunState, decisionDigest, evidenceDigest string, reviewRound uint) (domain.ApprovalRecord, error) {
	for _, entry := range records {
		approval := entry.Approval
		if approval == nil || approval.Gate != domain.ApprovalGatePublish {
			continue
		}
		if approval.Outcome != domain.ApprovalOutcomeApproved || approval.Source.Type != domain.ControlSourceTypeHuman {
			continue
		}
		binding := approval.Binding
		if binding.ReviewRound != reviewRound || binding.DecisionDigest != decisionDigest || binding.EvidenceDigest != evidenceDigest {
			continue
		}
		if binding.SpecDigest != state.SpecDigest || binding.PolicyDigest != state.PolicyDigest ||
			binding.CapabilityDigest != state.CapabilityDigest || binding.BaseSHA != state.BaseSHA {
			continue
		}
		return *approval, nil
	}
	return domain.ApprovalRecord{}, errors.New("no current human publish approval binds the merge admission")
}

// observeFreshChecks enforces M10: a schema-valid fresh RemoteCheckRecord
// whose identity binds the current run/publication and whose required-checks
// set exactly matches the frozen set with every required check green.
func observeFreshChecks(ctx context.Context, input MergeInput, admission mergeAdmission, state domain.RunState, runDir string) (string, error) {
	checkRecord, err := input.CheckObserver.ObserveChecks(ctx, domain.Record{Kind: domain.KindPublicationRecord, Data: admission.publicationData}, admission.task.Publication.RequiredChecks)
	if err != nil {
		return "", err
	}
	if checkRecord.Kind != domain.KindRemoteCheckRecord || input.Validator.Validate(domain.KindRemoteCheckRecord, checkRecord.Data) != nil {
		return "", errors.New("check observer returned invalid RemoteCheckRecord")
	}
	var checks domain.RemoteCheckRecord
	if err := json.Unmarshal(checkRecord.Data, &checks); err != nil {
		return "", err
	}
	if checks.TaskID != state.TaskID || checks.RunID != state.RunID || checks.RepositoryID != admission.publication.Repository.ID ||
		checks.RequestID != admission.publication.Request.ID || checks.HeadSHA != admission.publication.HeadSHA {
		return "", port.Permanent(errors.New("RemoteCheckRecord identity mismatch"))
	}
	if checks.Status != domain.CheckStatusPass {
		return "", fmt.Errorf("required checks are not all green: %s", checks.Status)
	}
	if !requiredChecksMatch(checks.Checks, admission.task.Publication.RequiredChecks) {
		return "", port.Permanent(errors.New("RemoteCheckRecord requiredChecks set does not match the frozen identity"))
	}
	digest, err := canonical.DigestJSON(checkRecord.Data)
	if err != nil {
		return "", err
	}
	if err := persistRemoteCheckRecord(runDir, digest, checkRecord.Data); err != nil {
		return "", err
	}
	return digest, nil
}

func requiredChecksMatch(observed []domain.RemoteCheck, required []string) bool {
	requiredSet := make(map[string]bool, len(required))
	for _, name := range required {
		if requiredSet[name] {
			return false
		}
		requiredSet[name] = true
	}
	if len(observed) != len(requiredSet) {
		return false
	}
	for _, check := range observed {
		if !check.Required || !requiredSet[check.Name] || check.Status != domain.CheckStatusPass {
			return false
		}
		requiredSet[check.Name] = false
	}
	for _, present := range requiredSet {
		if present {
			return false
		}
	}
	return true
}

func nonEmptyUniqueRequiredChecks(checks []string) bool {
	if len(checks) == 0 {
		return false
	}
	seen := make(map[string]bool, len(checks))
	for _, name := range checks {
		if name == "" || seen[name] {
			return false
		}
		seen[name] = true
	}
	return true
}

// validateMergeTarget binds the fresh target observation to the frozen intent
// anchors (repository, PR, head, base == frozen baseSha, marker). It does not
// require Draft: that requirement belongs to the initial-binding branch. Every
// drift is permanent so the caller routes it through the single BLOCKED path.
func validateMergeTarget(target domain.SCMMergeTarget, intent domain.SCMMergeIntent, expectedBaseBranch string) error {
	if target.Repository != intent.RepositoryRef || target.PRNumber != intent.PRNumber {
		return port.Permanent(errors.New("merge target repository or PR number drifted"))
	}
	if target.HeadOid != intent.HeadOid {
		return port.Permanent(errors.New("merge target head OID drifted from the frozen publication head"))
	}
	if target.BaseBranch != expectedBaseBranch || target.BaseOid != intent.BaseOid {
		return port.Permanent(errors.New("merge target base branch or OID drifted from the frozen publication anchors"))
	}
	if !target.MarkerPresent {
		return port.Permanent(errors.New("merge target PR does not carry the run marker"))
	}
	if target.State == domain.MergeTargetStateClosed {
		return port.Permanent(errors.New("merge target PR is closed"))
	}
	return nil
}

// executeMergeSideEffects runs ready + merge + receipt observation for an
// already-persisted intent, driven by the fresh target classification. It is
// re-entrant for the ADR 0032 §7 C2-C6 recovery windows and never mints a
// new intent or authorization.
func executeMergeSideEffects(ctx context.Context, input MergeInput, admission mergeAdmission, intent domain.SCMMergeIntent, target domain.SCMMergeTarget, authorization authority.PublicationAuthorization, runDir string) (domain.SCMMergeReceipt, error) {
	state := classifyMergeTarget(target, intent, admission.publication.BaseBranch)
	switch state {
	case domain.MergeReadyMerged:
		return observeMergeReceiptForIntent(ctx, input, admission, intent)
	case domain.MergeReadyDrifted:
		return domain.SCMMergeReceipt{}, port.Permanent(errors.New("merge target drifted during admission"))
	case domain.MergeReadyStillDraft:
		if err := authorizedMutation(ctx, input, authorization, intent, runDir, mergeDeliveryReady, targetFence(input, admission, intent, domain.MergeReadyStillDraft), func() error {
			return input.Merger.ReadyForReview(ctx, intent)
		}); err != nil {
			if port.IsPermanent(err) {
				return domain.SCMMergeReceipt{}, err
			}
			return recoverReady(ctx, input, admission, intent, authorization, runDir)
		}
		return mergeAndObserve(ctx, input, admission, intent, authorization, runDir)
	case domain.MergeReadyReady:
		return mergeAndObserve(ctx, input, admission, intent, authorization, runDir)
	default:
		return domain.SCMMergeReceipt{}, errors.New("unreachable merge target classification")
	}
}

// recoverReady is the ADR 0032 §5 ObserveReady recovery state machine for a
// failed or lost ready response: it re-observes the PR and continues without
// blind retry.
func recoverReady(ctx context.Context, input MergeInput, admission mergeAdmission, intent domain.SCMMergeIntent, authorization authority.PublicationAuthorization, runDir string) (domain.SCMMergeReceipt, error) {
	target, err := input.TargetObserver.ObserveTarget(ctx, intent)
	if err != nil {
		return domain.SCMMergeReceipt{}, err
	}
	switch classifyMergeTarget(target, intent, admission.publication.BaseBranch) {
	case domain.MergeReadyStillDraft:
		// ready did not take effect: idempotent retry with the same intent.
		if err := authorizedMutation(ctx, input, authorization, intent, runDir, mergeDeliveryReady, targetFence(input, admission, intent, domain.MergeReadyStillDraft), func() error {
			return input.Merger.ReadyForReview(ctx, intent)
		}); err != nil {
			return domain.SCMMergeReceipt{}, err
		}
		return mergeAndObserve(ctx, input, admission, intent, authorization, runDir)
	case domain.MergeReadyReady:
		// ready took effect (lost response): continue to merge.
		return mergeAndObserve(ctx, input, admission, intent, authorization, runDir)
	case domain.MergeReadyMerged:
		return observeMergeReceiptForIntent(ctx, input, admission, intent)
	default:
		return domain.SCMMergeReceipt{}, port.Permanent(errors.New("merge target drifted after ready"))
	}
}

// mergeAndObserve performs the merge and then observes the immutable receipt.
// On a failed merge it reconciles through ObserveMergeReceipt / ObserveReady
// first (C4) instead of blind replay.
func mergeAndObserve(ctx context.Context, input MergeInput, admission mergeAdmission, intent domain.SCMMergeIntent, authorization authority.PublicationAuthorization, runDir string) (domain.SCMMergeReceipt, error) {
	// ADR 0032 §2 requires an immediate target cut before every merge
	// mutation. GitHub can mechanically fence the head OID but not the base
	// OID, so the earlier admission observation cannot authorize this later
	// side effect (ReadyForReview itself may have taken time).
	target, err := input.TargetObserver.ObserveTarget(ctx, intent)
	if err != nil {
		return domain.SCMMergeReceipt{}, err
	}
	switch classifyMergeTarget(target, intent, admission.publication.BaseBranch) {
	case domain.MergeReadyMerged:
		return observeMergeReceiptForIntent(ctx, input, admission, intent)
	case domain.MergeReadyReady:
		// This is the sole mutation-authorizing target state.
	case domain.MergeReadyStillDraft:
		return domain.SCMMergeReceipt{}, port.Permanent(errors.New("merge target became Draft before merge mutation"))
	default:
		return domain.SCMMergeReceipt{}, port.Permanent(errors.New("merge target drifted immediately before merge mutation"))
	}
	if err := authorizedMutation(ctx, input, authorization, intent, runDir, mergeDeliveryMerge, targetFence(input, admission, intent, domain.MergeReadyReady), func() error {
		return input.Merger.Merge(ctx, intent)
	}); err != nil {
		if port.IsPermanent(err) {
			return domain.SCMMergeReceipt{}, err
		}
		receipt, obsErr := tryObserveMergeReceipt(ctx, input, admission, intent)
		if obsErr == nil {
			return receipt, nil
		}
		if !errors.Is(obsErr, port.ErrPRNotMerged) {
			return domain.SCMMergeReceipt{}, obsErr
		}
		target, terr := input.TargetObserver.ObserveTarget(ctx, intent)
		if terr != nil {
			return domain.SCMMergeReceipt{}, terr
		}
		switch classifyMergeTarget(target, intent, admission.publication.BaseBranch) {
		case domain.MergeReadyMerged:
			return tryObserveMergeReceipt(ctx, input, admission, intent)
		case domain.MergeReadyReady:
			if retryErr := authorizedMutation(ctx, input, authorization, intent, runDir, mergeDeliveryMerge, targetFence(input, admission, intent, domain.MergeReadyReady), func() error {
				return input.Merger.Merge(ctx, intent)
			}); retryErr != nil {
				return domain.SCMMergeReceipt{}, retryErr
			}
		case domain.MergeReadyStillDraft:
			if retryErr := authorizedMutation(ctx, input, authorization, intent, runDir, mergeDeliveryReady, targetFence(input, admission, intent, domain.MergeReadyStillDraft), func() error {
				return input.Merger.ReadyForReview(ctx, intent)
			}); retryErr != nil {
				return domain.SCMMergeReceipt{}, retryErr
			}
			// Re-enter through the immediate target cut; ready may have raced
			// with base/head/marker drift and never directly authorizes merge.
			return mergeAndObserve(ctx, input, admission, intent, authorization, runDir)
		default:
			return domain.SCMMergeReceipt{}, port.Permanent(errors.New("merge target drifted during recovery"))
		}
	}
	return observeMergeReceiptForIntent(ctx, input, admission, intent)
}

func targetFence(input MergeInput, admission mergeAdmission, intent domain.SCMMergeIntent, expected domain.MergeReadyState) func(context.Context) error {
	return func(ctx context.Context) error {
		target, err := input.TargetObserver.ObserveTarget(ctx, intent)
		if err != nil {
			return err
		}
		if actual := classifyMergeTarget(target, intent, admission.publication.BaseBranch); actual != expected {
			return port.Permanent(fmt.Errorf("merge target changed immediately before mutation: got %s want %s", actual, expected))
		}
		return nil
	}
}

func tryObserveMergeReceipt(ctx context.Context, input MergeInput, admission mergeAdmission, intent domain.SCMMergeIntent) (domain.SCMMergeReceipt, error) {
	receiptRecord, err := input.ReceiptObserver.ObserveMergeReceipt(ctx, domain.Record{Kind: domain.KindPublicationRecord, Data: admission.publicationData})
	if err != nil {
		return domain.SCMMergeReceipt{}, err
	}
	return persistMergedReceipt(filepath.Join(input.StateRoot, "runs", intent.RunID), input.Validator, receiptRecord, intent, admission.publicationDigest)
}

func observeMergeReceiptForIntent(ctx context.Context, input MergeInput, admission mergeAdmission, intent domain.SCMMergeIntent) (domain.SCMMergeReceipt, error) {
	return tryObserveMergeReceipt(ctx, input, admission, intent)
}

// mergeTransition builds the publication.merged event with the frozen
// producer actor and the closed payload, then reduces CI_PENDING -> ACCEPTED.
func mergeTransition(state domain.RunState, payload map[string]any, guard lifecycle.Guard) (domain.RunEvent, domain.RunState, error) {
	id, err := domain.NewID("event")
	if err != nil {
		return domain.RunEvent{}, state, err
	}
	event := domain.RunEvent{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: id, RunID: state.RunID,
		Sequence: state.Sequence + 1, Type: lifecycle.PublicationMergedEventType,
		StateFrom: state.State, StateTo: domain.StateAccepted, Timestamp: time.Now().UTC(),
		Actor: &domain.Actor{Type: lifecycle.MergerActorType, ID: lifecycle.MergerActorID}, Payload: payload,
	}
	next, err := lifecycle.Reduce(state, event, guard)
	return event, next, err
}

// rebuildMergeConvergence implements C7: when the run already reached
// ACCEPTED, replay reconstructs the outcome from the journal and the
// persisted receipt/intent binding; an existing final outcome is never
// rewritten (no-replace).
func rebuildMergeConvergence(runDir string, store *runstore.Store, validator *contract.Validator, state domain.RunState) (MergeResult, error) {
	events, _, err := store.ReadEvents(state.RunID)
	if err != nil {
		return MergeResult{}, err
	}
	var merged *domain.RunEvent
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type == lifecycle.PublicationMergedEventType {
			merged = &events[index]
			break
		}
	}
	if merged == nil {
		return MergeResult{}, errors.New("run is ACCEPTED without a publication.merged event")
	}
	intentID, _ := merged.Payload["intentId"].(string)
	intentDigest, _ := merged.Payload["intentDigest"].(string)
	receiptID, _ := merged.Payload["receiptId"].(string)
	receiptDigest, _ := merged.Payload["receiptDigest"].(string)
	publicationDigest, _ := merged.Payload["publicationDigest"].(string)
	remoteCheckDigest, _ := merged.Payload["remoteCheckRecordDigest"].(string)
	if intentDigest == "" || receiptDigest == "" || publicationDigest == "" {
		return MergeResult{}, errors.New("publication.merged event lacks the receipt/intent binding")
	}
	intent, found, err := existingMergeIntentByDigest(filepath.Join(runDir, "merge-intents"), validator, intentDigest)
	if err != nil || !found {
		if err != nil {
			return MergeResult{}, err
		}
		return MergeResult{}, errors.New("run is ACCEPTED without the bound merge intent")
	}
	publicationData, err := os.ReadFile(filepath.Join(runDir, "publication-record.json"))
	if err != nil {
		return MergeResult{}, err
	}
	if err := validator.Validate(domain.KindPublicationRecord, publicationData); err != nil {
		return MergeResult{}, err
	}
	recomputedPublicationDigest, err := canonical.DigestJSON(publicationData)
	if err != nil {
		return MergeResult{}, err
	}
	if recomputedPublicationDigest != publicationDigest || intent.PublicationRecordID != publicationDigest || intent.PublicationDigest != publicationDigest {
		return MergeResult{}, errors.New("C7 publication binding does not match the journal and merge intent")
	}
	var currentPublication domain.PublicationRecord
	if err := json.Unmarshal(publicationData, &currentPublication); err != nil {
		return MergeResult{}, err
	}
	taskData, err := os.ReadFile(filepath.Join(runDir, "task-spec.json"))
	if err != nil {
		return MergeResult{}, err
	}
	if err := validator.Validate(domain.KindTask, taskData); err != nil {
		return MergeResult{}, err
	}
	var task domain.TaskSpec
	if err := json.Unmarshal(taskData, &task); err != nil {
		return MergeResult{}, err
	}
	if err := bindPersistedRemoteChecks(runDir, validator, intent, currentPublication, task.Publication.RequiredChecks, state); err != nil {
		return MergeResult{}, err
	}
	receiptData, err := os.ReadFile(filepath.Join(runDir, "scm-merge-receipt.json"))
	if err != nil {
		return MergeResult{}, err
	}
	if err := validator.Validate(domain.KindSCMMergeReceipt, receiptData); err != nil {
		return MergeResult{}, err
	}
	var receipt domain.SCMMergeReceipt
	if err := json.Unmarshal(receiptData, &receipt); err != nil {
		return MergeResult{}, err
	}
	if err := receipt.Validate(); err != nil {
		return MergeResult{}, err
	}
	if err := validateMergeReceiptBinding(receipt, intent, publicationDigest); err != nil {
		return MergeResult{}, err
	}
	if receipt.ReceiptDigest != receiptDigest || receipt.ReceiptID != receiptID || intent.IntentID != intentID ||
		intent.RemoteCheckRecordDigest != remoteCheckDigest || receipt.HeadOid != payloadString(merged.Payload, "headOid") ||
		receipt.MergeCommitSha != payloadString(merged.Payload, "mergeCommitSha") || receipt.MergeMethod != payloadString(merged.Payload, "mergeMethod") {
		return MergeResult{}, errors.New("C7 merge event does not bind the persisted intent and receipt")
	}
	decisionData, err := os.ReadFile(filepath.Join(runDir, "decisions", fmt.Sprintf("decision-%03d.json", state.ReviewRound)))
	if err != nil {
		return MergeResult{}, err
	}
	decisionDigest, err := canonical.DigestJSON(decisionData)
	if err != nil {
		return MergeResult{}, err
	}
	var decision domain.ReviewDecision
	if err := json.Unmarshal(decisionData, &decision); err != nil {
		return MergeResult{}, err
	}
	if err := validator.Validate(domain.KindReviewDecision, decisionData); err != nil {
		return MergeResult{}, err
	}
	if decisionDigest != intent.ReviewDecisionDigest || decision.EvidenceDigest != intent.EvidenceDigest ||
		decision.VerificationDigest != intent.VerificationDigest || decision.Verdict != "accept" {
		return MergeResult{}, errors.New("C7 review evidence does not bind the merge intent")
	}
	expectedJSON, expectedMarkdown, err := mergeOutcomeDocuments(runDir, validator, state, "merged by Marshal after required checks", decisionDigest, decision.EvidenceDigest, intent.IntentDigest, receiptDigest)
	if err != nil {
		return MergeResult{}, err
	}
	for _, document := range []struct {
		path string
		data []byte
	}{{filepath.Join(runDir, "outcome.json"), expectedJSON}, {filepath.Join(runDir, "outcome.md"), expectedMarkdown}} {
		existing, readErr := os.ReadFile(document.path)
		if readErr == nil {
			if string(existing) != string(document.data) {
				return MergeResult{}, errors.New("existing C7 Outcome document conflicts with the journal-bound reconstruction")
			}
			continue
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return MergeResult{}, readErr
		}
		if err := putFileNoReplace(document.path, document.data); err != nil {
			return MergeResult{}, err
		}
	}
	return MergeResult{State: state, Intent: intent, Receipt: receipt}, nil
}

func payloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func existingMergeIntentByDigest(directory string, validator *contract.Validator, digest string) (domain.SCMMergeIntent, bool, error) {
	data, err := os.ReadFile(filepath.Join(directory, strings.TrimPrefix(digest, "sha256:")+".json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.SCMMergeIntent{}, false, nil
		}
		return domain.SCMMergeIntent{}, false, err
	}
	if err := validator.Validate(domain.KindSCMMergeIntent, data); err != nil {
		return domain.SCMMergeIntent{}, false, err
	}
	var intent domain.SCMMergeIntent
	if err := json.Unmarshal(data, &intent); err != nil {
		return domain.SCMMergeIntent{}, false, err
	}
	if err := intent.Validate(); err != nil {
		return domain.SCMMergeIntent{}, false, err
	}
	if intent.IntentDigest != digest {
		return domain.SCMMergeIntent{}, false, errors.New("stored merge intent digest does not match the journal binding")
	}
	return intent, true, nil
}
