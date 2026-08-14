package publication

import (
	"encoding/json"
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
// R1 scope note: the ADR's schema-level fields (TaskSpec
// ciObserveTimeoutSeconds, PublicationRecord ciDeadline, RemoteCheckRecord
// checks[].completedAt) are frozen contracts that the embedded Draft
// 2020-12 schemas do not admit yet, so this implementation derives the
// identical values from the digest-anchored immutable inputs and parses
// completedAt facts tolerantly. Every adjudication rule is written against
// those values directly, so the schema extension activates without further
// changes here.

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

// frozenCIDeadline derives the immutable CI adjudication basis of one
// publication generation. ADR 0028 plan B freezes ciDeadline at publish time
// and forbids regenerating it from mutable state (no recomputation from the
// current instant, no re-derivation from a different spec). Until the
// PublicationRecord schema admits a ciDeadline member, the frozen value is
// derived deterministically from the facts that exist at publish time and
// never change afterwards: the Run's createdAt, the PublicationRecord's
// publishedAt (both anchored by the frozen publication.completed digest) and
// the frozen TaskSpec budgets.
//
// The CI budget is the existing runTimeoutSeconds value anchored at the
// publication instant: Worker, review and publish durations no longer erode
// the CI window, so runTimeoutSeconds stops swallowing the CI budget from
// the Run creation instant (Issue #30 expectation 4). The createdAt lower
// bound keeps the deadline monotone for lineages whose publishedAt precedes
// the Run creation (fabricated or clock-anomalous records) and preserves the
// legacy CreatedAt-anchored value exactly for them; in real lineages
// publishedAt never precedes createdAt, so the deadline is anchored purely
// at publication.
func frozenCIDeadline(createdAt, publishedAt time.Time, budgets domain.TaskBudgets) time.Time {
	anchor := createdAt.UTC()
	if publishedAt.UTC().After(anchor) {
		anchor = publishedAt.UTC()
	}
	return anchor.Add(time.Duration(budgets.RunTimeoutSeconds) * time.Second)
}

// adjudicateTimelyCompletion decides whether an all-required-pass remote
// check observation proves timely completion against the frozen ciDeadline
// (ADR 0028 adjudication step 4, Issue #30 expectation 2).
//
// Proof order per required check that passed:
//
//  1. a trusted provider completedAt inside the consistency window
//     [publishedAt − Δskew, ciDeadline + Δskew] proves timely completion
//     even when the local observation happens after the deadline — the
//     remote fact stands and the local observation instant does not erase
//     it;
//  2. an observation strictly before the frozen ciDeadline proves
//     completion on time by causal order: a check can only be observed
//     passing after it completed, so completion ≤ observation < ciDeadline;
//
// every other shape fails closed with a fixed reason code and no presumption
// favors acceptance: completedAt missing where a trusted completion time is
// required (ci-completed-at-missing), completedAt later than ciDeadline +
// Δskew (ci-completed-at-exceeds-deadline), completedAt earlier than
// publishedAt − Δskew, which is impossible for the published head
// (ci-completed-at-inconsistent).
func adjudicateTimelyCompletion(checks domain.RemoteCheckRecord, completions map[string]time.Time, ciDeadline, publishedAt, observedAt time.Time) error {
	ciDeadline, publishedAt, observedAt = ciDeadline.UTC(), publishedAt.UTC(), observedAt.UTC()
	for _, check := range checks.Checks {
		if !check.Required || check.Status != domain.CheckStatusPass {
			continue
		}
		completedAt, present := completions[check.Name]
		if !present {
			if observedAt.Compare(ciDeadline) >= 0 {
				return errCICompletedAtMissing
			}
			continue
		}
		if completedAt.Before(publishedAt.Add(-ciClockSkewTolerance)) {
			return errCICompletedAtInconsistent
		}
		if completedAt.After(ciDeadline.Add(ciClockSkewTolerance)) {
			return errCICompletedAtExceedsDeadline
		}
	}
	return nil
}

// parseCheckCompletionTimes extracts the optional provider completedAt facts
// from raw RemoteCheckRecord bytes (the ADR 0028 trusted completion times).
// The frozen RemoteCheckRecord schema does not yet admit a completedAt
// member, so observer records validated today carry none and this yields an
// empty map; the parsing contract is wired end-to-end so the ADR's schema
// extension activates the completion-time rules above without further
// changes. Any decode failure or malformed, zero or unnamed timestamp drops
// the fact: a dropped fact is indistinguishable from a missing one and fails
// closed wherever a trusted completion time is required — no presumption, no
// backfill (ADR 0028 backward compatibility).
func parseCheckCompletionTimes(recordData []byte) map[string]time.Time {
	completions := map[string]time.Time{}
	var side struct {
		Checks []struct {
			Name        string     `json:"name"`
			Required    bool       `json:"required"`
			CompletedAt *time.Time `json:"completedAt"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(recordData, &side); err != nil {
		return completions
	}
	for _, check := range side.Checks {
		if !check.Required || check.Name == "" || check.CompletedAt == nil || check.CompletedAt.IsZero() {
			continue
		}
		completions[check.Name] = check.CompletedAt.UTC()
	}
	return completions
}

// runBlockedByCIDeadline reports whether the run's terminal BLOCKED state was
// produced by the CI deadline adjudication: the journal's publication.blocked
// event carries the fixed ci-deadline-exceeded reason code. The sentinel
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
		return reason == errCIDeadlineExceeded.Error(), nil
	}
	return false, nil
}
