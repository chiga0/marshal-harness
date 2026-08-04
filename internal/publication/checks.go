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

var errCIDeadlineExceeded = errors.New("ci-deadline-exceeded")

type CheckInput struct {
	StateRoot, RunID string
	Observer         port.RemoteCheckObserver
	Validator        *contract.Validator
	Now              time.Time
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
	if deadline := state.CreatedAt.Add(time.Duration(task.Budgets.RunTimeoutSeconds) * time.Second); now.Compare(deadline) >= 0 {
		result, blockedErr := block(store, lease, state, runDir, errCIDeadlineExceeded)
		return CheckResult{State: result.State}, blockedErr
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
	if err != nil || publicationDigest != frozenDigest {
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
