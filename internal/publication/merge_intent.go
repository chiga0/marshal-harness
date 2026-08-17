package publication

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

// persistMergeIntent enforces the ADR 0032 §3 put-if-absent intent
// transaction. The intent's canonical idempotency identity is the
// (authorityNamespaceId, runId, publicationRecordId, headOid, mergeMethod)
// tuple; an existing intent of the same identity merges idempotently when the
// detached digest matches and conflicts (fail closed) when it does not. The
// intent is stored under merge-intents/<intentDigest hex>.json and is never
// rewritten in place.
func persistMergeIntent(runDir string, validator *contract.Validator, intent domain.SCMMergeIntent) (domain.SCMMergeIntent, bool, error) {
	if err := intent.Validate(); err != nil {
		return domain.SCMMergeIntent{}, false, err
	}
	identity := intent.Identity()
	intentID, err := identity.IntentID()
	if err != nil {
		return domain.SCMMergeIntent{}, false, err
	}
	if intent.IntentID != intentID {
		return domain.SCMMergeIntent{}, false, errors.New("SCMMergeIntent intentId does not match the canonical idempotency tuple")
	}
	directory := filepath.Join(runDir, "merge-intents")
	existing, found, err := existingMergeIntent(directory, validator, intentID)
	if err != nil {
		return domain.SCMMergeIntent{}, false, err
	}
	if found {
		if existing.IntentDigest != intent.IntentDigest {
			return domain.SCMMergeIntent{}, false, errors.New("SCMMergeIntent conflicts with an existing intent of the same canonical identity")
		}
		return existing, false, nil
	}
	data, err := json.Marshal(intent)
	if err != nil {
		return domain.SCMMergeIntent{}, false, err
	}
	if err := validator.Validate(domain.KindSCMMergeIntent, data); err != nil {
		return domain.SCMMergeIntent{}, false, err
	}
	path := filepath.Join(directory, strings.TrimPrefix(intent.IntentDigest, "sha256:")+".json")
	if _, err := os.Lstat(path); err == nil {
		return domain.SCMMergeIntent{}, false, errors.New("SCMMergeIntent digest-addressed path already exists with different identity")
	} else if !errors.Is(err, os.ErrNotExist) {
		return domain.SCMMergeIntent{}, false, err
	}
	if err := atomicWrite(path, append(data, '\n')); err != nil {
		return domain.SCMMergeIntent{}, false, err
	}
	return intent, true, nil
}

// existingMergeIntent scans the append-only intent directory for an intent
// with the given canonical intentId, validating every stored record so a
// tampered or corrupt record fails closed instead of being ignored.
func existingMergeIntent(directory string, validator *contract.Validator, intentID string) (domain.SCMMergeIntent, bool, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.SCMMergeIntent{}, false, nil
		}
		return domain.SCMMergeIntent{}, false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return domain.SCMMergeIntent{}, false, err
		}
		if err := validator.Validate(domain.KindSCMMergeIntent, data); err != nil {
			return domain.SCMMergeIntent{}, false, fmt.Errorf("existing merge intent is invalid: %w", err)
		}
		var existing domain.SCMMergeIntent
		if err := json.Unmarshal(data, &existing); err != nil {
			return domain.SCMMergeIntent{}, false, err
		}
		if existing.IntentID != intentID {
			continue
		}
		if err := existing.Validate(); err != nil {
			return domain.SCMMergeIntent{}, false, err
		}
		return existing, true, nil
	}
	return domain.SCMMergeIntent{}, false, nil
}

// validateMergeReceiptBinding is the ADR 0032 §5 receipt binding table for
// the controlled-merge production path. Every field must match the frozen
// intent (including the authorityNamespaceId/runId dual key-space and the
// publicationRecordId/publicationDigest triple equality); any missing, empty
// or mismatching field fails closed and the run must not reach ACCEPTED.
func validateMergeReceiptBinding(receipt domain.SCMMergeReceipt, intent domain.SCMMergeIntent, publicationDigest string) error {
	recomputed, err := receipt.Digest()
	if err != nil || receipt.ReceiptDigest != recomputed {
		return port.Permanent(errors.New("SCMMergeReceipt digest recomputation mismatch"))
	}
	if receipt.AuthorityNamespaceID != intent.AuthorityNamespaceID || receipt.RunID != intent.RunID {
		return port.Permanent(errors.New("SCMMergeReceipt authorityNamespaceId or runId does not bind the merge intent"))
	}
	if receipt.HeadOid != intent.HeadOid || receipt.BaseOid != intent.BaseOid || receipt.MergeMethod != intent.MergeMethod {
		return port.Permanent(errors.New("SCMMergeReceipt head, base or method does not bind the merge intent"))
	}
	if receipt.PublicationRecordID != intent.PublicationRecordID || intent.PublicationRecordID != intent.PublicationDigest || intent.PublicationDigest != publicationDigest || receipt.PublicationRecordID != publicationDigest {
		return port.Permanent(errors.New("SCMMergeReceipt publicationRecordId does not satisfy the triple publication digest equality"))
	}
	if receipt.RepositoryRef != intent.RepositoryRef || receipt.PRNumber != intent.PRNumber {
		return port.Permanent(errors.New("SCMMergeReceipt repository or PR number does not bind the merge intent"))
	}
	if "github-login:"+receipt.MergedBy != intent.ExpectedMergedBy {
		return port.Permanent(errors.New("SCMMergeReceipt mergedBy does not bind the expected merge executor"))
	}
	return nil
}

// persistMergedReceipt validates the observed SCMMergeReceipt against the
// intent and enforces the same immutability rule as the ADR 0026 path: an
// existing receipt with the identical canonical form merges, any difference
// conflicts and fails closed.
func persistMergedReceipt(runDir string, validator *contract.Validator, receiptRecord domain.Record, intent domain.SCMMergeIntent, publicationDigest string) (domain.SCMMergeReceipt, error) {
	if receiptRecord.Kind != domain.KindSCMMergeReceipt || validator.Validate(domain.KindSCMMergeReceipt, receiptRecord.Data) != nil {
		return domain.SCMMergeReceipt{}, port.Permanent(errors.New("merge observer returned an invalid SCMMergeReceipt"))
	}
	var receipt domain.SCMMergeReceipt
	if err := json.Unmarshal(receiptRecord.Data, &receipt); err != nil {
		return domain.SCMMergeReceipt{}, port.Permanent(err)
	}
	if err := receipt.Validate(); err != nil {
		return domain.SCMMergeReceipt{}, port.Permanent(err)
	}
	if err := validateMergeReceiptBinding(receipt, intent, publicationDigest); err != nil {
		return domain.SCMMergeReceipt{}, err
	}
	path := filepath.Join(runDir, "scm-merge-receipt.json")
	existing, err := os.ReadFile(path)
	if err == nil {
		existingDigest, existingErr := canonical.DigestJSON(existing)
		incomingDigest, incomingErr := canonical.DigestJSON(receiptRecord.Data)
		if existingErr != nil || incomingErr != nil || existingDigest != incomingDigest {
			return domain.SCMMergeReceipt{}, port.Permanent(errors.New("existing SCMMergeReceipt conflicts with the observed merge fact"))
		}
		return receipt, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return domain.SCMMergeReceipt{}, err
	}
	if err := atomicWrite(path, append(receiptRecord.Data, '\n')); err != nil {
		return domain.SCMMergeReceipt{}, err
	}
	return receipt, nil
}

// classifyMergeTarget classifies a fresh pre-merge observation into the ADR
// 0032 §5 ObserveReady recovery state. Identity drift (repository, PR number,
// head, base or marker) and a closed PR always fail closed as drifted BEFORE
// any OPEN/MERGED classification, so a merged PR whose identity or marker
// drifted is never silently accepted as already merged.
func classifyMergeTarget(target domain.SCMMergeTarget, intent domain.SCMMergeIntent, expectedBaseBranch string) domain.MergeReadyState {
	if target.Repository != intent.RepositoryRef || target.PRNumber != intent.PRNumber ||
		target.HeadOid != intent.HeadOid || target.BaseBranch != expectedBaseBranch ||
		target.BaseOid != intent.BaseOid || !target.MarkerPresent {
		return domain.MergeReadyDrifted
	}
	if target.State == domain.MergeTargetStateClosed {
		return domain.MergeReadyDrifted
	}
	if target.State == domain.MergeTargetStateMerged {
		return domain.MergeReadyMerged
	}
	if target.State != domain.MergeTargetStateOpen {
		return domain.MergeReadyDrifted
	}
	if target.Draft {
		return domain.MergeReadyStillDraft
	}
	return domain.MergeReadyReady
}

// preparedMergeOutcome stages the ADR 0032 merge outcome (intentDigest and
// receiptDigest bound) with the same no-replace pending/final hard-link
// semantics as review.PrepareOutcome.
type preparedMergeOutcome struct {
	pendingJSON, finalJSON string
	pendingMD, finalMD     string
}

func prepareMergeOutcome(runDir string, validator *contract.Validator, state domain.RunState, summary, decisionDigest, evidenceDigest, intentDigest, receiptDigest string) (*preparedMergeOutcome, error) {
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
	recomputedDecisionDigest, err := canonical.DigestJSON(decisionData)
	if err != nil {
		return nil, err
	}
	if decision.TaskID != state.TaskID || decision.RunID != state.RunID || decision.Verdict != "accept" ||
		recomputedDecisionDigest != decisionDigest || decision.EvidenceDigest != evidenceDigest {
		return nil, errors.New("terminal Outcome review identity mismatch")
	}
	outcome := domain.OutcomeBundle{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindOutcome,
		TaskID: state.TaskID, RunID: state.RunID, TerminalState: state.State, Verdict: decision.Verdict,
		FinalReviewRound: decision.ReviewRound, FinalReviewDigest: decisionDigest, FinalEvidenceDigest: decision.EvidenceDigest,
		Summary: summary, FindingCount: uint(len(decision.BlockingFindings) + len(decision.NonBlockingFindings)),
		RetentionPolicy: "default", GeneratedAt: state.UpdatedAt,
		IntentDigest: intentDigest, ReceiptDigest: receiptDigest,
	}
	jsonData, err := json.MarshalIndent(outcome, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := validator.Validate(domain.KindOutcome, jsonData); err != nil {
		return nil, fmt.Errorf("generated merge outcome violates contract: %w", err)
	}
	markdown := fmt.Sprintf("# Run 结果报告\n\n- 任务 ID：%s\n- Run ID：%s\n- 终态：%s\n- Review Verdict：%s\n- Review Round：%d\n- 生成时间：%s\n\n## 摘要\n\n%s\n\n## 证据绑定\n\n- Decision：%s\n- Evidence：%s\n- Merge Intent：%s\n- Merge Receipt：%s\n\n## 保留策略\n\n默认保留；清理不得销毁本 Outcome。\n",
		state.TaskID, state.RunID, state.State, decision.Verdict, decision.ReviewRound, state.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		summary, decisionDigest, evidenceDigest, intentDigest, receiptDigest)
	prepared := &preparedMergeOutcome{
		finalJSON:   filepath.Join(runDir, "outcome.json"),
		pendingJSON: filepath.Join(runDir, "outcome.json.pending"),
		finalMD:     filepath.Join(runDir, "outcome.md"),
		pendingMD:   filepath.Join(runDir, "outcome.md.pending"),
	}
	for _, final := range []string{prepared.finalJSON, prepared.finalMD} {
		if _, err := os.Lstat(final); err == nil {
			return nil, fmt.Errorf("terminal outcome already exists: %s", final)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	for _, pending := range []string{prepared.pendingJSON, prepared.pendingMD} {
		if err := os.Remove(pending); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("remove orphan outcome %s: %w", pending, err)
		}
	}
	if err := atomicWrite(prepared.pendingJSON, append(jsonData, '\n')); err != nil {
		prepared.Abort()
		return nil, err
	}
	if err := atomicWrite(prepared.pendingMD, []byte(markdown)); err != nil {
		prepared.Abort()
		return nil, err
	}
	return prepared, nil
}

func (o *preparedMergeOutcome) Commit() error {
	for _, pair := range [][2]string{{o.pendingJSON, o.finalJSON}, {o.pendingMD, o.finalMD}} {
		if err := os.Link(pair[0], pair[1]); err != nil {
			return err
		}
		if err := os.Remove(pair[0]); err != nil {
			return err
		}
	}
	return nil
}

func (o *preparedMergeOutcome) Abort() {
	for _, path := range []string{o.pendingJSON, o.pendingMD} {
		if path != "" {
			_ = os.Remove(path)
		}
	}
}
