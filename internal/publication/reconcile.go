package publication

import (
	"bytes"
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

// ADR 0026 frozen reconcile constants.
const (
	ReconcileTypeAcceptAfterMerge = domain.ReconcileTypeAcceptAfterMerge
	ReconcileReasonMergedHead     = "merged-head.reconciled-after-block"
	// ReconcileReasonCIDeadlineReconciled is the ADR 0028 positive reason
	// code: a deadline-blocked Run reconciled to ACCEPTED on a positive
	// timely-completion proof. reconcileReason is a machine-readable
	// reason-code field, so the closed vocabulary extends without a schema
	// change.
	ReconcileReasonCIDeadlineReconciled = "ci-deadline-reconciled"
	ReconcileTerminalReason             = "reconciled-after-merge"
	reconcileActorID                    = "marshal-reconciliation"
	reconcileAcceptedSummary            = "merged head reconciled after publication block"
)

type ReconcileInput struct {
	StateRoot, RunID string
	// MergeObserver captures the immutable SCMMergeReceipt; CheckObserver
	// re-verifies the merged head's required checks. Reconcile only observes:
	// it never merges or otherwise mutates the remote PR (merge-never).
	MergeObserver port.MergeReceiptObserver
	CheckObserver port.RemoteCheckObserver
	Validator     *contract.Validator
	ReconciledBy  string
	Now           time.Time
}

type ReconcileResult struct {
	State   domain.RunState                   `json:"state"`
	Receipt domain.SCMMergeReceipt            `json:"scmMergeReceipt"`
	Record  domain.PublicationReconcileRecord `json:"publicationReconcileRecord"`
}

// Reconcile implements the ADR 0026 accept-after-merge typed reconciliation:
// it migrates a post-publication terminal BLOCKED run to ACCEPTED, gated by
// the current-ledger recheck, the immutable SCMMergeReceipt and an append-only
// PublicationReconcileRecord. It never bypasses required checks or the
// ReviewDecision, never rewrites the PublicationRecord or ReviewDecision, and
// fails closed with the run kept in BLOCKED on any missing prerequisite,
// identity mismatch, digest mismatch or idempotency conflict.
//
// For Runs whose terminal block was the CI deadline adjudication (ADR 0028),
// the merged-head green checks additionally pass the trusted-completion
// precondition against the frozen ciDeadline: a timely recovery records the
// ci-deadline-reconciled reason code, while a late or unproven recovery fails
// closed with the run kept in BLOCKED and no RunState mutation. Recovery is
// append-only and idempotent; nothing here ever mutates the RunState
// directly.
func Reconcile(ctx context.Context, input ReconcileInput) (ReconcileResult, error) {
	if input.MergeObserver == nil || input.CheckObserver == nil || input.Validator == nil {
		return ReconcileResult{}, errors.New("merge observer, check observer and validator are required")
	}
	reconciledBy := strings.TrimSpace(input.ReconciledBy)
	if reconciledBy == "" || len(reconciledBy) > 256 {
		return ReconcileResult{}, errors.New("reconciledBy is required")
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
		return ReconcileResult{}, err
	}
	defer lease.Release()
	state, err := store.Inspect(input.RunID)
	if err != nil {
		return ReconcileResult{}, err
	}
	runDir := filepath.Join(input.StateRoot, "runs", input.RunID)

	// (1) State gate: only a terminal BLOCKED run qualifies. An already
	// ACCEPTED run with a persisted record of the same identity merges
	// offline and idempotently — no network observation, no rewrite.
	switch state.State {
	case domain.StateBlocked:
	case domain.StateAccepted:
		record, found, scanErr := existingReconcileRecord(runDir, input.Validator, input.RunID)
		if scanErr != nil {
			return ReconcileResult{}, scanErr
		}
		if !found {
			return ReconcileResult{}, errors.New("run is ACCEPTED without a publication reconcile record")
		}
		return ReconcileResult{State: state, Record: record}, nil
	default:
		return ReconcileResult{}, fmt.Errorf("run state %s cannot be reconciled; only post-publication BLOCKED runs qualify", state.State)
	}
	if state.Publication == nil {
		return ReconcileResult{}, errors.New("BLOCKED run lacks a publication snapshot; reconcile requires a post-publication block")
	}

	// (2) Current-ledger recheck eligibility: frozen review/verification
	// evidence, the frozen PublicationRecord digest and an accepting round
	// decision with no blocking findings. A BLOCKED run without a frozen
	// PublicationRecord (for example PUBLISHING -> BLOCKED) is rejected.
	frozen, err := frozenEvidence(store, state.RunID)
	if err != nil {
		return ReconcileResult{}, err
	}
	publicationData, err := currentPublicationData(runDir, state)
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("BLOCKED run lacks a frozen PublicationRecord: %w", err)
	}
	if err := input.Validator.Validate(domain.KindPublicationRecord, publicationData); err != nil {
		return ReconcileResult{}, err
	}
	var publication domain.PublicationRecord
	if err := json.Unmarshal(publicationData, &publication); err != nil {
		return ReconcileResult{}, err
	}
	publicationDigest, err := canonical.DigestJSON(publicationData)
	if err != nil {
		return ReconcileResult{}, err
	}
	frozenDigest, err := frozenPublicationDigest(store, state.RunID)
	if err != nil {
		return ReconcileResult{}, err
	}
	if publicationDigest != frozenDigest || state.Publication.HeadSHA != publication.HeadSHA || state.Publication.ExternalID != publication.Request.ID || state.Publication.Repository != publication.Repository.NameWithOwner || publication.TaskID != state.TaskID || publication.RunID != state.RunID {
		return ReconcileResult{}, errors.New("current PublicationRecord differs from frozen lifecycle identity")
	}
	if _, _, _, err := authorizePublicationDecision(runDir, state, input.Validator, frozen); err != nil {
		return ReconcileResult{}, err
	}

	// (3) Merge fact capture: observe the immutable SCMMergeReceipt from the
	// merged PR node. An unmerged PR must go through accept, not reconcile.
	receiptRecord, err := input.MergeObserver.ObserveMergeReceipt(ctx, domain.Record{Kind: domain.KindPublicationRecord, Data: publicationData})
	if err != nil {
		if errors.Is(err, port.ErrPRNotMerged) {
			return ReconcileResult{}, errors.New("PR is not merged; reconcile is only for merged publications, use marshal task accept otherwise")
		}
		return ReconcileResult{}, err
	}

	taskData, err := os.ReadFile(filepath.Join(runDir, "task-spec.json"))
	if err != nil {
		return ReconcileResult{}, err
	}
	if err := input.Validator.Validate(domain.KindTask, taskData); err != nil {
		return ReconcileResult{}, err
	}
	if digest, _ := canonical.DigestJSON(taskData); digest != state.SpecDigest {
		return ReconcileResult{}, errors.New("TaskSpec digest mismatch")
	}
	var task domain.TaskSpec
	if err := json.Unmarshal(taskData, &task); err != nil {
		return ReconcileResult{}, err
	}

	// (4) Merged-head required checks re-verification: re-observe the merged
	// PR's required checks and materialize a fresh RemoteCheckRecord only
	// when every required check is green. Reconcile never bypasses checks.
	checkRecord, err := input.CheckObserver.ObserveChecks(ctx, domain.Record{Kind: domain.KindPublicationRecord, Data: publicationData}, task.Publication.RequiredChecks)
	if err != nil {
		return ReconcileResult{}, err
	}
	if checkRecord.Kind != domain.KindRemoteCheckRecord || input.Validator.Validate(domain.KindRemoteCheckRecord, checkRecord.Data) != nil {
		return ReconcileResult{}, errors.New("check observer returned invalid RemoteCheckRecord")
	}
	var checks domain.RemoteCheckRecord
	if err := json.Unmarshal(checkRecord.Data, &checks); err != nil {
		return ReconcileResult{}, err
	}
	if checks.TaskID != state.TaskID || checks.RunID != state.RunID || checks.RepositoryID != publication.Repository.ID || checks.RequestID != publication.Request.ID || checks.HeadSHA != publication.HeadSHA {
		return ReconcileResult{}, errors.New("RemoteCheckRecord identity mismatch")
	}
	if checks.Status != "pass" {
		return ReconcileResult{}, fmt.Errorf("merged head required checks are not all green: %s", checks.Status)
	}
	checkDigest, err := canonical.DigestJSON(checkRecord.Data)
	if err != nil {
		return ReconcileResult{}, err
	}

	// (4.5) ADR 0028 trusted-completion precondition for deadline blocks: a
	// Run whose terminal block was the CI deadline adjudication may only be
	// reconciled on a positive timely-completion proof against the frozen
	// ciDeadline (an in-window re-observation, or provider completedAt facts
	// inside the adjudication window). A late re-observation without trusted
	// completion facts fails closed with the run kept in BLOCKED — reconcile
	// never credits a late green light without on-time proof, keeping the ADR
	// 0026 "reconcile never bypasses required checks" invariant in its time
	// dimension. Non-deadline blocks keep the exact ADR 0026 semantics.
	reconcileReason := ReconcileReasonMergedHead
	deadlineBlocked, deadlineErr := runBlockedByCIDeadline(store, state.RunID)
	if deadlineErr != nil {
		return ReconcileResult{}, deadlineErr
	}
	if deadlineBlocked {
		ciDeadline := frozenCIDeadline(state.CreatedAt, publication.PublishedAt, task.Budgets)
		if adjudicationErr := adjudicateTimelyCompletion(checks, parseCheckCompletionTimes(checkRecord.Data), ciDeadline, publication.PublishedAt, now); adjudicationErr != nil {
			return ReconcileResult{}, adjudicationErr
		}
		reconcileReason = ReconcileReasonCIDeadlineReconciled
	}

	// (5) Record ordering for crash-cut safety: receipt (immutable) first,
	// then the append-only reconcile record; both carry deterministic
	// identities so replay merges instead of duplicating.
	receipt, err := persistMergeReceipt(runDir, input.Validator, receiptRecord, state.RunID, publication, publicationDigest)
	if err != nil {
		return ReconcileResult{}, err
	}
	if err := atomicWrite(filepath.Join(runDir, "remote-check-record.json"), append(checkRecord.Data, '\n')); err != nil {
		return ReconcileResult{}, err
	}
	authorityNamespaceID, err := reconcileAuthorityNamespaceID(input.StateRoot)
	if err != nil {
		return ReconcileResult{}, err
	}
	record := domain.PublicationReconcileRecord{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindPublicationReconcileRecord,
		ReconcileID:          reconcileID(authorityNamespaceID, state.RunID, receipt.ReceiptID, ReconcileTypeAcceptAfterMerge),
		AuthorityNamespaceID: authorityNamespaceID,
		RunID:                state.RunID,
		SCMMergeReceiptID:    receipt.ReceiptID,
		ReconcileType:        ReconcileTypeAcceptAfterMerge,
		ObservedState:        domain.StateBlocked,
		DecidedState:         domain.StateAccepted,
		EvidenceDigests:      []string{publicationDigest, frozen.decisionDigest, receipt.ReceiptDigest, checkDigest},
		ReconcileReason:      reconcileReason,
		ReconciledBy:         reconciledBy,
		ReconciledAt:         now,
	}
	recordDigest, err := record.Digest()
	if err != nil {
		return ReconcileResult{}, err
	}
	record.ReconcileRecordDigest = recordDigest
	recordData, err := json.Marshal(record)
	if err != nil {
		return ReconcileResult{}, err
	}
	if err := input.Validator.Validate(domain.KindPublicationReconcileRecord, recordData); err != nil {
		return ReconcileResult{}, err
	}
	persistedRecord, err := persistReconcileRecord(runDir, input.Validator, record, recordData)
	if err != nil {
		return ReconcileResult{}, err
	}

	// (6) Event append -> archive blocked outcome -> write ACCEPTED outcome
	// -> snapshot. runstore.Append enforces the expectedSequence CAS and
	// eventID de-duplication, so a concurrent ledger write rolls the whole
	// transition back.
	eventID, err := domain.NewID("event")
	if err != nil {
		return ReconcileResult{}, err
	}
	event := domain.RunEvent{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent,
		EventID: eventID, RunID: state.RunID, Sequence: state.Sequence + 1,
		Type: lifecycle.PublicationReconcileEventType, StateFrom: domain.StateBlocked, StateTo: domain.StateAccepted,
		Timestamp: now, Actor: &domain.Actor{Type: "system", ID: reconcileActorID},
		Payload: map[string]any{
			"receiptDigest":     receipt.ReceiptDigest,
			"reconcileId":       persistedRecord.ReconcileID,
			"publicationDigest": publicationDigest,
			"decisionDigest":    frozen.decisionDigest,
			"terminalReason":    ReconcileTerminalReason,
		},
	}
	next, err := lifecycle.Reduce(state, event, lifecycle.Guard{LeaseHeld: true, ReconcileAuthorized: true, EvidenceCurrent: true, PublicationCurrent: true, DecisionCurrent: true})
	if err != nil {
		return ReconcileResult{}, err
	}
	next.Publication = state.Publication
	if err := store.Append(lease, event, state.Sequence); err != nil {
		return ReconcileResult{}, err
	}
	if err := archiveBlockedOutcome(runDir); err != nil {
		return ReconcileResult{}, err
	}
	preparedOutcome, err := prepareOutcome(runDir, input.Validator, next, reconcileAcceptedSummary, publication.ReviewDecisionDigest, publication.EvidenceDigest)
	if err != nil {
		return ReconcileResult{}, err
	}
	if err := preparedOutcome.Commit(); err != nil {
		return ReconcileResult{}, err
	}
	if err := store.WriteSnapshot(lease, next); err != nil {
		return ReconcileResult{}, err
	}
	return ReconcileResult{State: next, Receipt: receipt, Record: persistedRecord}, nil
}

// persistMergeReceipt validates the observed SCMMergeReceipt against the
// frozen PublicationRecord and enforces the immutability rule: an existing
// receipt with the identical canonical form merges; any difference conflicts
// and fails closed.
func persistMergeReceipt(runDir string, validator *contract.Validator, receiptRecord domain.Record, runID string, publication domain.PublicationRecord, publicationDigest string) (domain.SCMMergeReceipt, error) {
	if receiptRecord.Kind != domain.KindSCMMergeReceipt || validator.Validate(domain.KindSCMMergeReceipt, receiptRecord.Data) != nil {
		return domain.SCMMergeReceipt{}, errors.New("merge observer returned an invalid SCMMergeReceipt")
	}
	var receipt domain.SCMMergeReceipt
	if err := json.Unmarshal(receiptRecord.Data, &receipt); err != nil {
		return domain.SCMMergeReceipt{}, err
	}
	if err := receipt.Validate(); err != nil {
		return domain.SCMMergeReceipt{}, err
	}
	if err := validateReceiptBinding(receipt, runID, publicationDigest, publication); err != nil {
		return domain.SCMMergeReceipt{}, err
	}
	path := filepath.Join(runDir, "scm-merge-receipt.json")
	existing, err := os.ReadFile(path)
	if err == nil {
		existingDigest, existingErr := canonical.DigestJSON(existing)
		incomingDigest, incomingErr := canonical.DigestJSON(receiptRecord.Data)
		if existingErr != nil || incomingErr != nil || existingDigest != incomingDigest {
			return domain.SCMMergeReceipt{}, errors.New("existing SCMMergeReceipt conflicts with the observed merge fact")
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

// validateReceiptBinding recomputes the detached receiptDigest and binds the
// receipt identity to the exact frozen PublicationRecord: run, publication
// digest, repository, PR number and pre-merge head/base OIDs.
func validateReceiptBinding(receipt domain.SCMMergeReceipt, runID, publicationDigest string, publication domain.PublicationRecord) error {
	recomputed, err := receipt.Digest()
	if err != nil || receipt.ReceiptDigest != recomputed {
		return errors.New("SCMMergeReceipt digest recomputation mismatch")
	}
	if receipt.RunID != runID || receipt.PublicationRecordID != publicationDigest ||
		receipt.RepositoryRef != publication.Repository.NameWithOwner || receipt.PRNumber != publication.Request.Number ||
		receipt.HeadOid != publication.HeadSHA || receipt.BaseOid != publication.BaseSHA {
		return errors.New("SCMMergeReceipt identity does not bind the frozen PublicationRecord")
	}
	return nil
}

// reconcileID derives the deterministic record identity from the canonical
// idempotency tuple (authorityNamespaceId, runId, scmMergeReceiptId,
// reconcileType): identical tuples always produce the identical reconcileId.
func reconcileID(authorityNamespaceID, runID, receiptID, reconcileType string) string {
	document, err := json.Marshal(map[string]string{
		"authorityNamespaceId": authorityNamespaceID,
		"reconcileType":        reconcileType,
		"runId":                runID,
		"scmMergeReceiptId":    receiptID,
	})
	if err != nil {
		return ""
	}
	digest, err := canonical.DigestJSON(document)
	if err != nil {
		return ""
	}
	return "reconcile-" + strings.TrimPrefix(digest, "sha256:")
}

// reconcileAuthorityNamespaceID derives the frozen local authority namespace
// exactly as ADR 0026 freezes it: tenantNamespace=local,
// controlPlaneId=default, authorityScopeId=repository identity read from
// repo.json (RepositoryIdentity), canonicalized and digested. Errors never
// carry local absolute paths.
func reconcileAuthorityNamespaceID(stateRoot string) (string, error) {
	data, err := os.ReadFile(filepath.Join(stateRoot, "repo.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("repository identity record is missing from the state root")
		}
		return "", errors.New("read repository identity failed")
	}
	var identity struct {
		APIVersion     string `json:"apiVersion"`
		Kind           string `json:"kind"`
		RepositoryRoot string `json:"repositoryRoot"`
	}
	if err := json.Unmarshal(data, &identity); err != nil {
		return "", err
	}
	if identity.APIVersion != string(domain.APIVersionV1Alpha1) || identity.Kind != "RepositoryIdentity" || strings.TrimSpace(identity.RepositoryRoot) == "" {
		return "", errors.New("unsupported repository identity record")
	}
	namespace := authority.AuthorityNamespaceId{TenantNamespace: "local", ControlPlaneId: "default", AuthorityScopeId: identity.RepositoryRoot}
	return namespace.Digest()
}

// existingReconcileRecord scans the append-only record directory for a valid
// record bound to runID. It serves the offline idempotent merge for runs
// that already reached ACCEPTED.
func existingReconcileRecord(runDir string, validator *contract.Validator, runID string) (domain.PublicationReconcileRecord, bool, error) {
	directory := filepath.Join(runDir, "publication-reconcile-records")
	entries, err := os.ReadDir(directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.PublicationReconcileRecord{}, false, nil
		}
		return domain.PublicationReconcileRecord{}, false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return domain.PublicationReconcileRecord{}, false, err
		}
		if err := validator.Validate(domain.KindPublicationReconcileRecord, data); err != nil {
			return domain.PublicationReconcileRecord{}, false, fmt.Errorf("existing publication reconcile record is invalid: %w", err)
		}
		var record domain.PublicationReconcileRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return domain.PublicationReconcileRecord{}, false, err
		}
		if record.RunID != runID {
			continue
		}
		if err := record.Validate(); err != nil {
			return domain.PublicationReconcileRecord{}, false, err
		}
		if err := verifyRecordDigest(&record); err != nil {
			return domain.PublicationReconcileRecord{}, false, err
		}
		return record, true, nil
	}
	return domain.PublicationReconcileRecord{}, false, nil
}

// persistReconcileRecord enforces the append-only idempotency identity
// canonical (authorityNamespaceId, runId, scmMergeReceiptId, reconcileType):
// identical identity with identical key content merges without rewriting the
// record or appending a second event; identical identity with differing key
// content conflicts and fails closed; a different identity (for example a
// different merge commit) conflicts with the existing immutable receipt.
func persistReconcileRecord(runDir string, validator *contract.Validator, record domain.PublicationReconcileRecord, recordData []byte) (domain.PublicationReconcileRecord, error) {
	directory := filepath.Join(runDir, "publication-reconcile-records")
	entries, err := os.ReadDir(directory)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return domain.PublicationReconcileRecord{}, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return domain.PublicationReconcileRecord{}, err
		}
		if err := validator.Validate(domain.KindPublicationReconcileRecord, data); err != nil {
			return domain.PublicationReconcileRecord{}, fmt.Errorf("existing publication reconcile record is invalid: %w", err)
		}
		var existing domain.PublicationReconcileRecord
		if err := json.Unmarshal(data, &existing); err != nil {
			return domain.PublicationReconcileRecord{}, err
		}
		if existing.AuthorityNamespaceID != record.AuthorityNamespaceID || existing.RunID != record.RunID ||
			existing.SCMMergeReceiptID != record.SCMMergeReceiptID || existing.ReconcileType != record.ReconcileType {
			continue
		}
		// Same idempotency identity: the stored record must first survive
		// detached digest recomputation. A tampered stored digest is a
		// dedicated recomputation failure, reported distinctly from
		// key-content conflicts below.
		if err := verifyRecordDigest(&existing); err != nil {
			return domain.PublicationReconcileRecord{}, err
		}
		// Key content must match or the record conflicts. reconciledAt and
		// the detached digest are not key content: a crash-cut replay at a
		// later instant must merge, not conflict.
		if !equalStringSlices(existing.EvidenceDigests, record.EvidenceDigests) ||
			existing.ReconciledBy != record.ReconciledBy || existing.ReconcileReason != record.ReconcileReason ||
			existing.ObservedState != record.ObservedState || existing.DecidedState != record.DecidedState {
			return domain.PublicationReconcileRecord{}, errors.New("publication reconcile record conflicts with an existing record of the same identity")
		}
		if entry.Name() != record.ReconcileID+".json" {
			return domain.PublicationReconcileRecord{}, errors.New("publication reconcile record identity is stored under a conflicting name")
		}
		// Idempotent merge: keep the original bytes (including reconciledAt
		// and the record digest) and do not write a second record.
		return existing, nil
	}
	path := filepath.Join(directory, record.ReconcileID+".json")
	if _, err := os.Lstat(path); err == nil {
		return domain.PublicationReconcileRecord{}, errors.New("publication reconcile record already exists with different content")
	} else if !errors.Is(err, os.ErrNotExist) {
		return domain.PublicationReconcileRecord{}, err
	}
	if err := atomicWrite(path, append(recordData, '\n')); err != nil {
		return domain.PublicationReconcileRecord{}, err
	}
	return record, nil
}

// verifyRecordDigest recomputes the detached reconcileRecordDigest of a
// stored record and fails closed on any mismatch: verifiers strip the digest
// field and recompute over the identical canonical form, so a stored record
// whose digest no longer binds its content is tampered evidence.
func verifyRecordDigest(record *domain.PublicationReconcileRecord) error {
	recomputed, err := record.Digest()
	if err != nil || record.ReconcileRecordDigest != recomputed {
		return errors.New("publication reconcile record digest recomputation mismatch")
	}
	return nil
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// archiveBlockedOutcome moves the terminal BLOCKED outcome bundle into
// runDir/outcomes/blocked-<digest prefix>/ so the reconcile can stage the
// ACCEPTED outcome. Blocked outcomes are only archived, never deleted.
func archiveBlockedOutcome(runDir string) error {
	source := filepath.Join(runDir, "outcome.json")
	data, err := os.ReadFile(source)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	digest, err := canonical.DigestJSON(data)
	if err != nil {
		return err
	}
	directory := filepath.Join(runDir, "outcomes", "blocked-"+strings.TrimPrefix(digest, "sha256:")[:12])
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	for _, name := range []string{"outcome.json", "outcome.md"} {
		content, readErr := os.ReadFile(filepath.Join(runDir, name))
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return readErr
		}
		destination := filepath.Join(directory, name)
		if _, statErr := os.Lstat(destination); statErr == nil {
			existing, existingErr := os.ReadFile(destination)
			if existingErr != nil || !bytes.Equal(existing, content) {
				return fmt.Errorf("outcome archive contains conflicting %s", name)
			}
			if err := os.Remove(filepath.Join(runDir, name)); err != nil {
				return err
			}
			continue
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		if err := os.Rename(filepath.Join(runDir, name), destination); err != nil {
			return err
		}
	}
	return nil
}
