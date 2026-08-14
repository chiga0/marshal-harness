package lifecycle

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
)

// watchdogWindow is the frozen test budget: 4 hours. The fixture Run is
// created at 00:00, last transitioned at 01:00 and therefore reaches its
// overall deadline at 04:00.
const watchdogWindow = int64(4 * 3600)

var (
	watchdogCreated    = time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	watchdogTransition = time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC)
)

func watchdogInput(state domain.State, now time.Time) WatchdogInput {
	return WatchdogInput{
		State:             state,
		CreatedAt:         watchdogCreated,
		LastTransitionAt:  watchdogTransition,
		RunTimeoutSeconds: watchdogWindow,
		Now:               now,
	}
}

func TestEvaluateWatchdogCategories(t *testing.T) {
	t.Parallel()
	deadline := watchdogCreated.Add(time.Duration(watchdogWindow) * time.Second)
	tests := []struct {
		name  string
		input WatchdogInput
		want  WatchdogVerdict
	}{
		{
			name:  "not timed out",
			input: watchdogInput(domain.StateRunning, watchdogCreated.Add(2*time.Hour)),
			want:  WatchdogVerdict{Category: CategoryNotTimedOut, Guidance: GuidanceWait, HungFor: time.Hour},
		},
		{
			name:  "one second before the deadline",
			input: watchdogInput(domain.StateRunning, deadline.Add(-time.Second)),
			want:  WatchdogVerdict{Category: CategoryNotTimedOut, Guidance: GuidanceWait, HungFor: 2*time.Hour + 59*time.Minute + 59*time.Second},
		},
		{
			name:  "exactly at the deadline",
			input: watchdogInput(domain.StateRunning, deadline),
			want:  WatchdogVerdict{TimedOut: true, Category: CategoryHungWithAttempt, Guidance: GuidanceAttemptAbort, HungFor: 3 * time.Hour},
		},
		{
			name:  "running hung past the window",
			input: watchdogInput(domain.StateRunning, deadline.Add(time.Hour)),
			want:  WatchdogVerdict{TimedOut: true, Category: CategoryHungWithAttempt, Guidance: GuidanceAttemptAbort, HungFor: 4 * time.Hour},
		},
		{
			name:  "retry pending hung past the window",
			input: watchdogInput(domain.StateRetryPending, deadline.Add(time.Hour)),
			want:  WatchdogVerdict{TimedOut: true, Category: CategoryHungWithAttempt, Guidance: GuidanceAttemptAbort, HungFor: 4 * time.Hour},
		},
		{
			name:  "rework requested hung past the window",
			input: watchdogInput(domain.StateReworkRequested, deadline.Add(time.Hour)),
			want:  WatchdogVerdict{TimedOut: true, Category: CategoryHungWithAttempt, Guidance: GuidanceAttemptAbort, HungFor: 4 * time.Hour},
		},
		{
			name:  "planned hung without attempt",
			input: watchdogInput(domain.StatePlanned, deadline.Add(time.Hour)),
			want:  WatchdogVerdict{TimedOut: true, Category: CategoryHungPreAttempt, Guidance: GuidancePreAttemptAbort, HungFor: 4 * time.Hour},
		},
		{
			name:  "ready hung without attempt",
			input: watchdogInput(domain.StateReady, deadline.Add(time.Hour)),
			want:  WatchdogVerdict{TimedOut: true, Category: CategoryHungPreAttempt, Guidance: GuidancePreAttemptAbort, HungFor: 4 * time.Hour},
		},
		{
			name:  "created hung has no abort exit",
			input: watchdogInput(domain.StateCreated, deadline.Add(time.Hour)),
			want:  WatchdogVerdict{TimedOut: true, Category: CategoryHungWait, Guidance: GuidanceWait, HungFor: 4 * time.Hour},
		},
		{
			name:  "review pending waiting past the window",
			input: watchdogInput(domain.StateReviewPending, deadline.Add(time.Hour)),
			want:  WatchdogVerdict{TimedOut: true, Category: CategoryHungWait, Guidance: GuidanceWait, HungFor: 4 * time.Hour},
		},
		{
			name:  "publishing waiting past the window",
			input: watchdogInput(domain.StatePublishing, deadline.Add(time.Hour)),
			want:  WatchdogVerdict{TimedOut: true, Category: CategoryHungWait, Guidance: GuidanceWait, HungFor: 4 * time.Hour},
		},
		{
			name:  "published waiting past the window",
			input: watchdogInput(domain.StatePublished, deadline.Add(time.Hour)),
			want:  WatchdogVerdict{TimedOut: true, Category: CategoryHungWait, Guidance: GuidanceWait, HungFor: 4 * time.Hour},
		},
		{
			name:  "ci pending waiting past the window",
			input: watchdogInput(domain.StateCIPending, deadline.Add(time.Hour)),
			want:  WatchdogVerdict{TimedOut: true, Category: CategoryHungWait, Guidance: GuidanceWait, HungFor: 4 * time.Hour},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := EvaluateWatchdog(test.input); got != test.want {
				t.Fatalf("verdict = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestEvaluateWatchdogVerifyingGateWindow(t *testing.T) {
	t.Parallel()
	gateEntry := watchdogCreated.Add(3 * time.Hour)
	verifying := func(now time.Time) WatchdogInput {
		input := watchdogInput(domain.StateVerifying, now)
		input.LastTransitionAt = gateEntry
		return input
	}
	tests := []struct {
		name  string
		input WatchdogInput
		want  WatchdogVerdict
	}{
		{
			name:  "inside both windows",
			input: verifying(watchdogCreated.Add(2 * time.Hour)),
			want:  WatchdogVerdict{Category: CategoryVerifyingExempt, Guidance: GuidanceWait, HungFor: 0},
		},
		{
			// The overall window expired at 04:00 but the verification gate
			// carries its own RunTimeoutSeconds budget from gate entry.
			name:  "overall window exceeded gate window exempts",
			input: verifying(watchdogCreated.Add(6 * time.Hour)),
			want:  WatchdogVerdict{Category: CategoryVerifyingExempt, Guidance: GuidanceWait, HungFor: 3 * time.Hour},
		},
		{
			name:  "one second before the gate deadline",
			input: verifying(gateEntry.Add(time.Duration(watchdogWindow)*time.Second - time.Second)),
			want:  WatchdogVerdict{Category: CategoryVerifyingExempt, Guidance: GuidanceWait, HungFor: 4*time.Hour - time.Second},
		},
		{
			name:  "exactly at the gate deadline",
			input: verifying(gateEntry.Add(time.Duration(watchdogWindow) * time.Second)),
			want:  WatchdogVerdict{TimedOut: true, Category: CategoryHungWithAttempt, Guidance: GuidanceAttemptAbort, HungFor: 4 * time.Hour},
		},
		{
			name:  "past the gate window",
			input: verifying(watchdogCreated.Add(8 * time.Hour)),
			want:  WatchdogVerdict{TimedOut: true, Category: CategoryHungWithAttempt, Guidance: GuidanceAttemptAbort, HungFor: 5 * time.Hour},
		},
		{
			// A snapshot without a last-transition timestamp falls back to
			// CreatedAt, never to a fabricated instant.
			name: "zero transition falls back to creation",
			input: func() WatchdogInput {
				input := verifying(watchdogCreated.Add(3 * time.Hour))
				input.LastTransitionAt = time.Time{}
				return input
			}(),
			want: WatchdogVerdict{Category: CategoryVerifyingExempt, Guidance: GuidanceWait, HungFor: 3 * time.Hour},
		},
		{
			name: "zero transition fallback past the gate window",
			input: func() WatchdogInput {
				input := verifying(watchdogCreated.Add(5 * time.Hour))
				input.LastTransitionAt = time.Time{}
				return input
			}(),
			want: WatchdogVerdict{TimedOut: true, Category: CategoryHungWithAttempt, Guidance: GuidanceAttemptAbort, HungFor: 5 * time.Hour},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := EvaluateWatchdog(test.input); got != test.want {
				t.Fatalf("verdict = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestEvaluateWatchdogFailsClosed(t *testing.T) {
	t.Parallel()
	farFuture := watchdogCreated.Add(1000 * time.Hour)
	notTimedOut := WatchdogVerdict{Category: CategoryNotTimedOut, Guidance: GuidanceWait}
	tests := []struct {
		name  string
		input WatchdogInput
		want  WatchdogVerdict
	}{
		{
			name: "zero budget",
			input: func() WatchdogInput {
				input := watchdogInput(domain.StateRunning, farFuture)
				input.RunTimeoutSeconds = 0
				return input
			}(),
			want: notTimedOut,
		},
		{
			name: "negative budget",
			input: func() WatchdogInput {
				input := watchdogInput(domain.StateRunning, farFuture)
				input.RunTimeoutSeconds = -1
				return input
			}(),
			want: notTimedOut,
		},
		{
			name: "overflowing budget is undefined",
			input: func() WatchdogInput {
				input := watchdogInput(domain.StateRunning, farFuture)
				input.RunTimeoutSeconds = maxWatchdogTimeoutSeconds + 1
				return input
			}(),
			want: notTimedOut,
		},
		{
			name: "zero creation timestamp",
			input: func() WatchdogInput {
				input := watchdogInput(domain.StateRunning, farFuture)
				input.CreatedAt = time.Time{}
				return input
			}(),
			want: notTimedOut,
		},
		{
			name: "zero evaluation instant",
			input: func() WatchdogInput {
				input := watchdogInput(domain.StateRunning, farFuture)
				input.Now = time.Time{}
				return input
			}(),
			want: notTimedOut,
		},
		{
			name: "clock inverted against creation",
			input: func() WatchdogInput {
				input := watchdogInput(domain.StateRunning, farFuture)
				input.Now = watchdogCreated.Add(-time.Second)
				return input
			}(),
			want: notTimedOut,
		},
		{
			name: "future transition clamps dwelling",
			input: func() WatchdogInput {
				input := watchdogInput(domain.StateRunning, watchdogCreated.Add(2*time.Hour))
				input.LastTransitionAt = watchdogCreated.Add(6 * time.Hour)
				return input
			}(),
			want: notTimedOut,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := EvaluateWatchdog(test.input); got != test.want {
				t.Fatalf("verdict = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestEvaluateWatchdogTerminalStatesNeverTimeOut(t *testing.T) {
	t.Parallel()
	farFuture := watchdogCreated.Add(1000 * time.Hour)
	for _, state := range domain.States() {
		if !state.Terminal() {
			continue
		}
		state := state
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			got := EvaluateWatchdog(watchdogInput(state, farFuture))
			want := WatchdogVerdict{Category: CategoryNotTimedOut, Guidance: GuidanceWait}
			if got != want {
				t.Fatalf("terminal %s verdict = %+v, want %+v", state, got, want)
			}
		})
	}
}

func TestEvaluateWatchdogCoversEveryState(t *testing.T) {
	t.Parallel()
	expected := map[domain.State]TimeoutCategory{
		domain.StateCreated:         CategoryHungWait,
		domain.StatePlanned:         CategoryHungPreAttempt,
		domain.StateReady:           CategoryHungPreAttempt,
		domain.StateRunning:         CategoryHungWithAttempt,
		domain.StateRetryPending:    CategoryHungWithAttempt,
		domain.StateVerifying:       CategoryHungWithAttempt,
		domain.StateReviewPending:   CategoryHungWait,
		domain.StateReworkRequested: CategoryHungWithAttempt,
		domain.StatePublishing:      CategoryHungWait,
		domain.StatePublished:       CategoryHungWait,
		domain.StateCIPending:       CategoryHungWait,
		domain.StateAccepted:        CategoryNotTimedOut,
		domain.StateRejected:        CategoryNotTimedOut,
		domain.StateBlocked:         CategoryNotTimedOut,
		domain.StateAborted:         CategoryNotTimedOut,
		domain.StateNoChange:        CategoryNotTimedOut,
	}
	states := domain.States()
	if len(states) != len(expected) {
		t.Fatalf("state vocabulary drifted: %d states, %d expectations", len(states), len(expected))
	}
	for _, state := range states {
		state := state
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			got := EvaluateWatchdog(watchdogInput(state, watchdogCreated.Add(1000*time.Hour)))
			if got.Category != expected[state] {
				t.Fatalf("state %s category = %s, want %s", state, got.Category, expected[state])
			}
			wantTimedOut := !state.Terminal()
			if got.TimedOut != wantTimedOut {
				t.Fatalf("state %s timedOut = %t, want %t", state, got.TimedOut, wantTimedOut)
			}
		})
	}
}

func TestWatchdogForRunUsesSnapshotTimestamps(t *testing.T) {
	t.Parallel()
	state := domain.NewRunState("task:1", "run:1", watchdogCreated)
	state.State = domain.StatePlanned
	state.UpdatedAt = watchdogTransition
	now := watchdogCreated.Add(5 * time.Hour)
	want := WatchdogVerdict{TimedOut: true, Category: CategoryHungPreAttempt, Guidance: GuidancePreAttemptAbort, HungFor: 4 * time.Hour}
	if got := WatchdogForRun(state, watchdogWindow, now); got != want {
		t.Fatalf("verdict = %+v, want %+v", got, want)
	}
}

// TestEvaluateWatchdogDeterministicDoubleRun is the purity double-run
// assertion: every fixture evaluated twice must yield byte-identical
// verdicts.
func TestEvaluateWatchdogDeterministicDoubleRun(t *testing.T) {
	t.Parallel()
	deadline := watchdogCreated.Add(time.Duration(watchdogWindow) * time.Second)
	gateEntry := watchdogCreated.Add(3 * time.Hour)
	verifying := watchdogInput(domain.StateVerifying, deadline.Add(2*time.Hour))
	verifying.LastTransitionAt = gateEntry
	noBudget := watchdogInput(domain.StateRunning, deadline.Add(time.Hour))
	noBudget.RunTimeoutSeconds = 0
	zeroCreated := watchdogInput(domain.StateRunning, deadline.Add(time.Hour))
	zeroCreated.CreatedAt = time.Time{}
	inputs := []WatchdogInput{
		watchdogInput(domain.StateRunning, watchdogCreated.Add(2*time.Hour)),
		watchdogInput(domain.StateRunning, deadline.Add(-time.Second)),
		watchdogInput(domain.StateRunning, deadline),
		watchdogInput(domain.StatePlanned, deadline.Add(time.Hour)),
		watchdogInput(domain.StateReady, deadline.Add(time.Hour)),
		watchdogInput(domain.StateRunning, deadline.Add(time.Hour)),
		watchdogInput(domain.StateRetryPending, deadline.Add(time.Hour)),
		watchdogInput(domain.StateReworkRequested, deadline.Add(time.Hour)),
		watchdogInput(domain.StateCreated, deadline.Add(time.Hour)),
		watchdogInput(domain.StateReviewPending, deadline.Add(time.Hour)),
		watchdogInput(domain.StatePublishing, deadline.Add(time.Hour)),
		watchdogInput(domain.StatePublished, deadline.Add(time.Hour)),
		watchdogInput(domain.StateCIPending, deadline.Add(time.Hour)),
		verifying,
		watchdogInput(domain.StateBlocked, deadline.Add(100*time.Hour)),
		watchdogInput(domain.StateAccepted, deadline.Add(100*time.Hour)),
		noBudget,
		zeroCreated,
	}
	for index, input := range inputs {
		input := input
		t.Run(string(input.State)+"_"+string(rune('a'+index)), func(t *testing.T) {
			t.Parallel()
			first := EvaluateWatchdog(input)
			second := EvaluateWatchdog(input)
			if first != second {
				t.Fatalf("double run diverged for %+v: %+v vs %+v", input, first, second)
			}
		})
	}
}

// TestTimeoutCandidateDoctorRow pins the doctor timeout section contract:
// exactly runId, state, hang duration, category and disposition guidance,
// with the frozen field names and sentinel values.
func TestTimeoutCandidateDoctorRow(t *testing.T) {
	t.Parallel()
	preAttempt := EvaluateWatchdog(watchdogInput(domain.StatePlanned, watchdogCreated.Add(5*time.Hour)))
	row := CandidateFromVerdict("run:1", domain.StatePlanned, preAttempt)
	data, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"runId":"run:1","state":"PLANNED","hungFor":"4h0m0s","category":"hung-pre-attempt","guidance":"pre-attempt-abort"}`
	if string(data) != want {
		t.Fatalf("doctor row = %s, want %s", data, want)
	}

	waiting := EvaluateWatchdog(watchdogInput(domain.StateCIPending, watchdogCreated.Add(5*time.Hour)))
	row = CandidateFromVerdict("run:2", domain.StateCIPending, waiting)
	data, err = json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	want = `{"runId":"run:2","state":"CI_PENDING","hungFor":"4h0m0s","category":"hung-wait","guidance":"wait"}`
	if string(data) != want {
		t.Fatalf("doctor row = %s, want %s", data, want)
	}
}

func TestGuidanceCommandPointsAtAbortEntry(t *testing.T) {
	t.Parallel()
	wantAbort := "marshal task abort --run run:1 --actor ID --reason TEXT"
	for _, guidance := range []string{GuidancePreAttemptAbort, GuidanceAttemptAbort} {
		if got := GuidanceCommand(guidance, "run:1"); got != wantAbort {
			t.Fatalf("guidance %s command = %q, want %q", guidance, got, wantAbort)
		}
	}
	for _, guidance := range []string{GuidanceWait, "", "bogus"} {
		if got := GuidanceCommand(guidance, "run:1"); got != "" {
			t.Fatalf("guidance %q command = %q, want none", guidance, got)
		}
	}
}
