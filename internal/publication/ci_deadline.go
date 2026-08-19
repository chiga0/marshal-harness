package publication

import (
	"errors"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// ADR 0028 frozen CI deadline contract (phased observation, Issue #30).
//
// The CI-observation budget of a Run is adjudicated against a deadline that
// is frozen at publication time. The local observation instant never
// retroactively erases a remote fact that was already established on time:
// an observation that arrives late relative to the run budget but before the
// frozen ciDeadline still reads the remote facts, and a published head whose
// required checks passed on time is accepted instead of being terminally
// blocked by the observation instant alone.
//
// ciClockSkewTolerance is the fixed provider/Marshal clock skew tolerance
// Δskew frozen by ADR 0028 (contract-recommended value 300 seconds; the
// implementation freezes it as a constant and asserts it by test).
const ciClockSkewTolerance = 300 * time.Second

// Closed machine-readable reason codes (ADR 0028). ci-deadline-exceeded keeps
// its pre-existing sentinel name and meaning; the completion-time codes are
// the fail-closed diagnoses for observations that cannot prove timely
// completion.
var (
	errCIDeadlineExceeded           = errors.New("ci-deadline-exceeded")
	errCICompletedAtMissing         = errors.New("ci-completed-at-missing")
	errCICompletedAtExceedsDeadline = errors.New("ci-completed-at-exceeds-deadline")
	errCICompletedAtInconsistent    = errors.New("ci-completed-at-inconsistent")
)

// frozenCIDeadline computes the value that publication freezes into a new
// PublicationRecord. New TaskSpecs with an explicit CI budget are anchored at
// publication. A TaskSpec without that optional field keeps ADR 0028's exact
// legacy fallback: CreatedAt + runTimeoutSeconds.
func frozenCIDeadline(createdAt, publishedAt time.Time, budgets domain.TaskBudgets) time.Time {
	if budgets.CIObserveTimeoutSeconds > 0 {
		return publishedAt.UTC().Add(time.Duration(budgets.CIObserveTimeoutSeconds) * time.Second)
	}
	return createdAt.UTC().Add(time.Duration(budgets.RunTimeoutSeconds) * time.Second)
}

// publicationCIDeadline resolves the immutable adjudication basis. A record
// that already carries ciDeadline is authoritative because its bytes are
// bound by publication.completed. Only a legacy record may derive the exact
// backward-compatible fallback from the frozen TaskSpec and Run CreatedAt.
func publicationCIDeadline(createdAt time.Time, published domain.PublicationRecord, budgets domain.TaskBudgets) (time.Time, error) {
	if published.CIDeadline != nil {
		deadline := published.CIDeadline.UTC()
		if deadline.IsZero() {
			return time.Time{}, errCICompletedAtInconsistent
		}
		return deadline, nil
	}
	return frozenCIDeadline(createdAt, published.PublishedAt, domain.TaskBudgets{RunTimeoutSeconds: budgets.RunTimeoutSeconds}), nil
}

// adjudicateTimelyCompletion decides whether an all-required-pass remote
// check observation proves timely completion against the frozen ciDeadline
// (ADR 0028 adjudication step 4, Issue #30 expectation 2).
//
// Proof order per required check that passed:
//
//  1. every required passing check has a trusted provider completedAt inside
//     the consistency window
//     [publishedAt − Δskew, ciDeadline + Δskew] proves timely completion
//     even when the local observation happens after the deadline — the
//     remote fact stands and the local observation instant does not erase
//     it;
//
// Every other shape fails closed. In particular, local observation time is
// never substituted for a missing provider completion time.
func adjudicateTimelyCompletion(checks domain.RemoteCheckRecord, requiredChecks []string, ciDeadline, publishedAt time.Time) error {
	ciDeadline, publishedAt = ciDeadline.UTC(), publishedAt.UTC()
	expected := make(map[string]struct{}, len(requiredChecks))
	for _, name := range requiredChecks {
		if _, duplicate := expected[name]; duplicate {
			return errCICompletedAtInconsistent
		}
		expected[name] = struct{}{}
	}
	seen := make(map[string]struct{})
	for _, check := range checks.Checks {
		_, wanted := expected[check.Name]
		if !wanted {
			// The observer is required to return the exact frozen check set.
			// Even an optional extra identity is not part of the adjudication
			// input authorized by PublicationRecord and therefore fails closed.
			return errCICompletedAtInconsistent
		}
		if !check.Required || check.Status != domain.CheckStatusPass {
			return errCICompletedAtInconsistent
		}
		if check.CompletedAt == nil || check.CompletedAt.IsZero() {
			return errCICompletedAtMissing
		}
		completedAt := check.CompletedAt.UTC()
		if _, duplicate := seen[check.Name]; duplicate {
			// A duplicate identity is conflicting evidence even when its two
			// timestamp values happen to compare equal.
			return errCICompletedAtInconsistent
		}
		seen[check.Name] = struct{}{}
		if completedAt.Before(publishedAt.Add(-ciClockSkewTolerance)) {
			return errCICompletedAtInconsistent
		}
		if completedAt.After(ciDeadline.Add(ciClockSkewTolerance)) {
			return errCICompletedAtExceedsDeadline
		}
	}
	if len(seen) != len(expected) {
		return errCICompletedAtMissing
	}
	return nil
}

// runBlockedByCIDeadline reports whether the run's terminal BLOCKED state was
// produced by any ADR 0028 CI timing adjudication. All four negative timing
// reasons require a fresh positive timely-completion proof before typed
// reconciliation may recover the Run. The ci-deadline-exceeded sentinel
// shares its name and meaning with the legacy deadline block, so runs blocked
// before this change are recognized too.
func runBlockedByCIDeadline(store *runstore.Store, runID string) (bool, error) {
	events, _, err := store.ReadEvents(runID)
	if err != nil {
		return false, err
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != "publication.blocked" {
			continue
		}
		reason, _ := event.Payload["error"].(string)
		switch reason {
		case errCIDeadlineExceeded.Error(), errCICompletedAtMissing.Error(), errCICompletedAtExceedsDeadline.Error(), errCICompletedAtInconsistent.Error():
			return true, nil
		default:
			return false, nil
		}
	}
	return false, nil
}
