package publication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/port"
	"github.com/chiga0/marshal-harness/internal/review"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

type CheckInput struct {
	StateRoot, RunID string
	Observer         port.RemoteCheckObserver
	// MergeObserver is optional. When set, ObserveChecks identifies a PR that
	// a maintainer merged outside Marshal (ADR 0026 path A): the immutable
	// SCMMergeReceipt is captured and persisted, and an all-green merged head
	// is accepted through the ordinary checks-passed transition. Path A never
	// writes a PublicationReconcileRecord.
	MergeObserver port.MergeReceiptObserver
	Validator     *contract.Validator
	Now           time.Time
}

type CheckResult struct {
	State  domain.RunState          `json:"state"`
	Checks domain.RemoteCheckRecord `json:"checks"`
}

func ObserveChecks(ctx context.Context, input CheckInput) (CheckResult, error) {
	if input.Observer == nil || input.Validator == nil {
		return CheckResult{}, errors.New("check observer and validator are required")
	}
	store := runstore.New(input.StateRoot)
	lease, err := store.Acquire(input.RunID)
	if err != nil {
		return CheckResult{}, err
	}
	defer lease.Release()
	state, err := store.Inspect(input.RunID)
	if err != nil {
		return CheckResult{}, err
	}
	if state.State != domain.StateCIPending {
		return CheckResult{}, fmt.Errorf("run state %s is not CI_PENDING", state.State)
	}
	runDir := filepath.Join(input.StateRoot, "runs", input.RunID)
	taskData, err := os.ReadFile(filepath.Join(runDir, "task-spec.json"))
	if err != nil {
		return CheckResult{}, err
	}
	if err := input.Validator.Validate(domain.KindTask, taskData); err != nil {
		return CheckResult{}, err
	}
	if digest, _ := canonical.DigestJSON(taskData); digest != state.SpecDigest {
		return CheckResult{}, errors.New("TaskSpec digest mismatch")
	}
	var task domain.TaskSpec
	if err := json.Unmarshal(taskData, &task); err != nil {
		return CheckResult{}, err
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	// Explicit producer-authority admission at the consumption site: the
	// publication.completed event ObserveChecks consumes must have been
	// recorded by the exact publisher actor. An omitted or forged actor fails
	// closed here, before the observer runs and without mutating the run,
	// independently of the PublicationRecord digest comparison below, so the
	// two defenses coexist.
	if err := requirePublicationCompletedAuthority(store, state.RunID); err != nil {
		return CheckResult{}, err
	}
	publicationData, err := os.ReadFile(filepath.Join(runDir, "publication-record.json"))
	if err != nil {
		return CheckResult{}, err
	}
	if err := input.Validator.Validate(domain.KindPublicationRecord, publicationData); err != nil {
		return CheckResult{}, err
	}
	publicationDigest, _ := canonical.DigestJSON(publicationData)
	frozenDigest, err := frozenPublicationDigest(store, state.RunID)
	if err != nil {
		// Journal authority failure, such as a publication.completed event
		// recorded by a forged or omitted producer actor: fail closed with
		// the authority error and without mutating the run.
		return CheckResult{}, err
	}
	if publicationDigest != frozenDigest {
		result, blockedErr := block(store, lease, state, runDir, errors.New("PublicationRecord differs from frozen lifecycle event"))
		return CheckResult{State: result.State}, blockedErr
	}
	var published domain.PublicationRecord
	if err := json.Unmarshal(publicationData, &published); err != nil {
		return CheckResult{}, err
	}
	if state.Publication == nil || state.Publication.HeadSHA != published.HeadSHA || state.Publication.ExternalID != published.Request.ID || published.TaskID != state.TaskID || published.RunID != state.RunID {
		return CheckResult{}, errors.New("RunState publication identity mismatch")
	}
	// ADR 0028 phased deadline gate: the adjudication basis is the frozen
	// ciDeadline derived from the digest-anchored publication facts (see
	// frozenCIDeadline), not the run-creation-anchored runTimeoutSeconds that
	// used to swallow the CI budget. An observation that arrives late relative
	// to the run budget but before the frozen ciDeadline still reads the
	// remote facts, so a published head whose required checks passed on time
	// is never terminally blocked by the local observation instant alone
	// (Issue #30 expectations 1 and 4). At or after the frozen ciDeadline the
	// run fails closed with the fixed sentinel before any remote observation;
	// recovery afterwards is only possible through typed reconciliation, and a
	// blocked run is never revived by observation.
	ciDeadline := frozenCIDeadline(state.CreatedAt, published.PublishedAt, task.Budgets)
	if now.Compare(ciDeadline) >= 0 {
		result, blockedErr := block(store, lease, state, runDir, errCIDeadlineExceeded)
		return CheckResult{State: result.State}, blockedErr
	}
	// ADR 0026 path A: after the state gate and the local deadline check
	// (which stay first and are never exempted), identify a PR that has been
	// MERGED with its immutable merge facts intact and persist the
	// SCMMergeReceipt before check observation. An unmerged PR keeps the
	// original checks observation flow unchanged.
	if input.MergeObserver != nil {
		receiptRecord, mergeErr := input.MergeObserver.ObserveMergeReceipt(ctx, domain.Record{Kind: domain.KindPublicationRecord, Data: publicationData})
		if mergeErr != nil {
			if !errors.Is(mergeErr, port.ErrPRNotMerged) {
				if port.IsPermanent(mergeErr) {
					result, blockedErr := block(store, lease, state, runDir, mergeErr)
					return CheckResult{State: result.State}, blockedErr
				}
				return CheckResult{State: state}, mergeErr
			}
		} else {
			if _, persistErr := persistMergeReceipt(runDir, input.Validator, receiptRecord, state.RunID, published, publicationDigest); persistErr != nil {
				result, blockedErr := block(store, lease, state, runDir, persistErr)
				return CheckResult{State: result.State}, blockedErr
			}
		}
	}
	observedRecord, err := input.Observer.ObserveChecks(ctx, domain.Record{Kind: domain.KindPublicationRecord, Data: publicationData}, task.Publication.RequiredChecks)
	if err != nil {
		if port.IsPermanent(err) {
			result, blockedErr := block(store, lease, state, runDir, err)
			return CheckResult{State: result.State}, blockedErr
		}
		return CheckResult{State: state}, err
	}
	if observedRecord.Kind != domain.KindRemoteCheckRecord || input.Validator.Validate(domain.KindRemoteCheckRecord, observedRecord.Data) != nil {
		result, blockedErr := block(store, lease, state, runDir, errors.New("observer returned invalid RemoteCheckRecord"))
		return CheckResult{State: result.State}, blockedErr
	}
	var checks domain.RemoteCheckRecord
	if err := json.Unmarshal(observedRecord.Data, &checks); err != nil {
		result, blockedErr := block(store, lease, state, runDir, err)
		return CheckResult{State: result.State}, blockedErr
	}
	if checks.TaskID != state.TaskID || checks.RunID != state.RunID || checks.RepositoryID != published.Repository.ID || checks.RequestID != published.Request.ID || checks.HeadSHA != published.HeadSHA {
		result, blockedErr := block(store, lease, state, runDir, errors.New("RemoteCheckRecord identity mismatch"))
		return CheckResult{State: result.State}, blockedErr
	}
	if err := atomicWrite(filepath.Join(runDir, "remote-check-record.json"), append(observedRecord.Data, '\n')); err != nil {
		return CheckResult{}, err
	}
	if checks.Status == "pending" {
		return CheckResult{State: state, Checks: checks}, nil
	}
	var event domain.RunEvent
	var next domain.RunState
	switch checks.Status {
	case "pass":
		// ADR 0028 trusted-completion adjudication: a pass proves timely
		// completion when the observation precedes the frozen ciDeadline or
		// when provider completedAt facts fall inside the adjudication
		// window; every other shape fails closed with a fixed reason code.
		// With the frozen RemoteCheckRecord schema no completedAt facts exist
		// yet, so the in-window observation itself carries the proof and the
		// existing checks-passed semantics are unchanged.
		if adjudicationErr := adjudicateTimelyCompletion(checks, parseCheckCompletionTimes(observedRecord.Data), ciDeadline, published.PublishedAt, now); adjudicationErr != nil {
			result, blockedErr := block(store, lease, state, runDir, adjudicationErr)
			return CheckResult{State: result.State, Checks: checks}, blockedErr
		}
		event, next, err = transition(state, "publication.checks-passed", domain.StateAccepted, map[string]any{"terminalReason": "published head passed all required checks"}, lifecycle.Guard{LeaseHeld: true, EvidenceCurrent: true, RequiredGatesPass: true, PublicationCurrent: true})
	case "fail":
		if state.ReworkRoundsUsed < uint(task.Budgets.MaxReworkRounds) {
			event, next, err = transition(state, "publication.checks-failed", domain.StateReworkRequested, map[string]any{"headSha": checks.HeadSHA}, lifecycle.Guard{LeaseHeld: true, BudgetAvailable: true, PublicationCurrent: true})
		} else {
			result, blockedErr := block(store, lease, state, runDir, errors.New("remote checks failed and rework budget is exhausted"))
			return CheckResult{State: result.State, Checks: checks}, blockedErr
		}
	default:
		result, blockedErr := block(store, lease, state, runDir, errors.New("remote check observation is an external failure"))
		return CheckResult{State: result.State, Checks: checks}, blockedErr
	}
	if err != nil {
		return CheckResult{}, err
	}
	next.Publication = state.Publication
	var preparedOutcome *review.PreparedRecords
	if next.State == domain.StateAccepted {
		preparedOutcome, err = prepareOutcome(runDir, input.Validator, next, "published head passed all required checks", published.ReviewDecisionDigest, published.EvidenceDigest)
		if err != nil {
			result, blockedErr := block(store, lease, state, runDir, err)
			return CheckResult{State: result.State, Checks: checks}, blockedErr
		}
	}
	if err := store.Append(lease, event, state.Sequence); err != nil {
		if preparedOutcome != nil {
			preparedOutcome.Abort()
		}
		return CheckResult{}, err
	}
	if preparedOutcome != nil {
		if err := preparedOutcome.Commit(); err != nil {
			return CheckResult{}, err
		}
	}
	if err := store.WriteSnapshot(lease, next); err != nil {
		return CheckResult{}, err
	}
	return CheckResult{State: next, Checks: checks}, nil
}

// requirePublicationCompletedAuthority fails closed unless every
// publication.completed event in the run journal carries the exact publisher
// producer actor {type:publisher, id:marshal-github-publisher}. ObserveChecks
// consumes that event type to anchor the frozen PublicationRecord digest, so
// the producer authority is verified at this consumption site itself — an
// omitted or forged actor is rejected with a fixed, assertable error instead
// of being left to downstream record comparisons.
func requirePublicationCompletedAuthority(store *runstore.Store, runID string) error {
	events, _, err := store.ReadEvents(runID)
	if err != nil {
		return err
	}
	for _, event := range events {
		if event.Type != "publication.completed" {
			continue
		}
		if !actorMatches(event.Actor, "publisher", "marshal-github-publisher") {
			return errors.New("publication.completed event must be recorded by publisher/marshal-github-publisher")
		}
	}
	return nil
}
