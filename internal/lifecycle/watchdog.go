package lifecycle

import (
	"math"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
)

// Wall-clock watchdog (issue #68): a pure, advisory classifier of
// non-terminal Runs against the frozen budgets.RunTimeoutSeconds window.
// RunTimeoutSeconds previously bounded only the verification gate
// (marshal task verify) and the CI deadline check; nothing flagged a Run
// that died silently and stayed non-terminal forever. The watchdog closes
// that observation gap and nothing else: it never writes Run state, never
// appends journal events and never executes an abort. Terminal disposition
// is always completed through the existing legal command the emitted
// guidance sentinel points to (the ADR 0012 abort exit, the ADR 0029
// pre-attempt abort exit), which keeps failing closed on its own.

// TimeoutCategory is the closed wall-clock classification of one Run.
type TimeoutCategory string

const (
	// CategoryNotTimedOut: the Run is inside its wall-clock window, or the
	// verdict fails closed (undefined budget, missing timestamps, inverted
	// clocks, terminal state).
	CategoryNotTimedOut TimeoutCategory = "not-timed-out"
	// CategoryVerifyingExempt: a VERIFYING Run inside the verification
	// gate's own RunTimeoutSeconds window is exempt and never judged timed
	// out, even when the overall run window has already been exceeded.
	CategoryVerifyingExempt TimeoutCategory = "verifying-gate-exempt"
	// CategoryHungPreAttempt: a PLANNED/READY Run past the overall window;
	// it never started an Attempt and is an ADR 0029 pre-attempt abort
	// candidate.
	CategoryHungPreAttempt TimeoutCategory = "hung-pre-attempt"
	// CategoryHungWithAttempt: a Run past the overall window that started
	// at least one Attempt; it is an ADR 0012 abort candidate. The abort
	// command itself still fails closed on ineligible source states.
	CategoryHungWithAttempt TimeoutCategory = "hung-with-attempt"
	// CategoryHungWait: a Run past the overall window that no abort exit
	// accepts, annotated separately from the abort candidates: states
	// waiting for an external judgment (REVIEW_PENDING, PUBLISHED,
	// CI_PENDING), a Run waiting for its publication transaction to settle
	// (PUBLISHING) and CREATED, for which no legal disposition command
	// exists yet. The guidance is always wait.
	CategoryHungWait TimeoutCategory = "hung-wait"
)

// Guidance sentinels (closed set, machine readable, ADR 0029 sentinel
// style): every timed-out verdict carries exactly one. Extending the set
// requires an ADR revision, never code alone.
const (
	// GuidanceWait: no abort exit applies; wait for the external judgment,
	// the gate progression or operator guardrails.
	GuidanceWait = "wait"
	// GuidancePreAttemptAbort points at `marshal task abort` through the
	// ADR 0029 pre-attempt exit (closed source set PLANNED/READY).
	GuidancePreAttemptAbort = "pre-attempt-abort"
	// GuidanceAttemptAbort points at `marshal task abort` through the
	// ADR 0012 exit for attempt-bearing Runs.
	GuidanceAttemptAbort = "attempt-abort"
)

// maxWatchdogTimeoutSeconds bounds the budget so the Duration arithmetic
// below can never overflow; a larger budget is treated as undefined.
const maxWatchdogTimeoutSeconds = math.MaxInt64 / int64(time.Second)

// WatchdogInput carries everything the pure wall-clock judgment may look at:
// the Run state, its creation and last-transition timestamps, the frozen
// budgets.RunTimeoutSeconds and the evaluation instant. Nothing else.
type WatchdogInput struct {
	State             domain.State
	CreatedAt         time.Time
	LastTransitionAt  time.Time
	RunTimeoutSeconds int64
	Now               time.Time
}

// WatchdogVerdict is the pure judgment for one WatchdogInput: identical
// input always yields an identical verdict.
type WatchdogVerdict struct {
	TimedOut bool
	Category TimeoutCategory
	Guidance string
	// HungFor is how long the Run has dwelt in its current state at the
	// evaluation instant; zero when the inputs cannot prove dwelling.
	HungFor time.Duration
}

// EvaluateWatchdog classifies one Run against its wall-clock budget. It is
// a pure function and fails closed: an undefined budget, missing
// timestamps, inverted clocks and terminal states all yield "not timed
// out", so a candidate is only ever reported on a proven window breach.
//
// The overall window is measured from CreatedAt and a Run becomes timed
// out at or after the deadline, mirroring the CI deadline comparison in
// publication checks. VERIFYING is exempt while inside the verification
// gate's own RunTimeoutSeconds window measured from gate entry (the verify
// command runs with exactly that timeout); outside the window it is judged
// like any other attempt-bearing state.
func EvaluateWatchdog(input WatchdogInput) WatchdogVerdict {
	verdict := WatchdogVerdict{Category: CategoryNotTimedOut, Guidance: GuidanceWait}
	if input.RunTimeoutSeconds <= 0 || input.RunTimeoutSeconds > maxWatchdogTimeoutSeconds {
		return verdict
	}
	if input.State.Terminal() {
		return verdict
	}
	now := input.Now.UTC()
	createdAt := input.CreatedAt.UTC()
	transitionAt := input.LastTransitionAt.UTC()
	if transitionAt.IsZero() {
		transitionAt = createdAt
	}
	if now.IsZero() || createdAt.IsZero() || now.Before(createdAt) {
		return verdict
	}
	if hungFor := now.Sub(transitionAt); hungFor > 0 {
		verdict.HungFor = hungFor
	}
	timeout := time.Duration(input.RunTimeoutSeconds) * time.Second
	if input.State == domain.StateVerifying {
		if now.Compare(transitionAt.Add(timeout)) < 0 {
			verdict.Category = CategoryVerifyingExempt
			return verdict
		}
		verdict.TimedOut = true
		verdict.Category = CategoryHungWithAttempt
		verdict.Guidance = GuidanceAttemptAbort
		return verdict
	}
	if now.Compare(createdAt.Add(timeout)) < 0 {
		return verdict
	}
	verdict.TimedOut = true
	switch input.State {
	case domain.StatePlanned, domain.StateReady:
		verdict.Category = CategoryHungPreAttempt
		verdict.Guidance = GuidancePreAttemptAbort
	case domain.StateRunning, domain.StateRetryPending, domain.StateReworkRequested:
		verdict.Category = CategoryHungWithAttempt
		verdict.Guidance = GuidanceAttemptAbort
	default:
		verdict.Category = CategoryHungWait
		verdict.Guidance = GuidanceWait
	}
	return verdict
}

// WatchdogForRun evaluates the wall-clock verdict for one Run snapshot
// using the frozen TaskSpec budget; the snapshot's CreatedAt and UpdatedAt
// supply the creation and last-transition timestamps.
func WatchdogForRun(state domain.RunState, runTimeoutSeconds int64, now time.Time) WatchdogVerdict {
	return EvaluateWatchdog(WatchdogInput{
		State:             state.State,
		CreatedAt:         state.CreatedAt,
		LastTransitionAt:  state.UpdatedAt,
		RunTimeoutSeconds: runTimeoutSeconds,
		Now:               now,
	})
}

// TimeoutCandidate is one advisory row of the doctor timeout section:
// runId, state, hang duration, category, disposition guidance. Doctor only
// reports these rows; it never acts on them.
type TimeoutCandidate struct {
	RunID    string          `json:"runId"`
	State    domain.State    `json:"state"`
	HungFor  string          `json:"hungFor"`
	Category TimeoutCategory `json:"category"`
	Guidance string          `json:"guidance"`
}

// CandidateFromVerdict renders one timed-out verdict as a doctor row.
func CandidateFromVerdict(runID string, state domain.State, verdict WatchdogVerdict) TimeoutCandidate {
	return TimeoutCandidate{
		RunID:    runID,
		State:    state,
		HungFor:  verdict.HungFor.String(),
		Category: verdict.Category,
		Guidance: verdict.Guidance,
	}
}

// GuidanceCommand maps a guidance sentinel to the existing legal command it
// points to. Both abort sentinels point at the single explicit abort entry
// (`marshal task abort`), which itself fails closed on ineligible source
// states; the wait sentinel has no command.
func GuidanceCommand(guidance, runID string) string {
	switch guidance {
	case GuidancePreAttemptAbort, GuidanceAttemptAbort:
		return "marshal task abort --run " + runID + " --actor ID --reason TEXT"
	default:
		return ""
	}
}
