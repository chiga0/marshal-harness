package resultingress

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

func testStopIntent(t *testing.T, state AttemptAuthorityState) AttemptStopIntent {
	t.Helper()
	intent, err := SealAttemptStopIntent(state.Identity, AttemptStopIntent{
		RequestID: "cancel-1", ExpectedSequence: 3, ExpectedAuthorityHead: attemptTestDigest("current-run"),
		OperatorUID: 501, ObservedAt: "2026-08-28T00:00:03Z", Category: StopOperatorRequest,
	})
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func testDeadlineWitness() BusinessDeadlineWitness {
	return BusinessDeadlineWitness{
		SpecDigest: attemptTestDigest("spec"), ProcessStartedFactDigest: attemptTestDigest("started"),
		CreationEventDigest: attemptTestDigest("creation"),
		RunCreatedAt:        "2026-08-28T00:00:00Z", ProcessStartedAt: "2026-08-28T00:00:02Z",
		RunTimeoutSeconds: 10, AttemptTimeoutSeconds: 3,
	}
}

func TestBusinessDeadlineImmutableMinimumAndInvalidSources(t *testing.T) {
	for _, tc := range []struct {
		name         string
		run, attempt int64
		want         string
		category     AttemptStopCategory
	}{
		{"attempt-first", 10, 3, "2026-08-28T00:00:05Z", StopAttemptDeadline},
		{"run-first", 3, 10, "2026-08-28T00:00:03Z", StopRunDeadline},
		{"run-wins-tie", 5, 3, "2026-08-28T00:00:05Z", StopRunDeadline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := testDeadlineWitness()
			w.RunTimeoutSeconds, w.AttemptTimeoutSeconds = tc.run, tc.attempt
			sealed, err := SealBusinessDeadline(w)
			if err != nil {
				t.Fatal(err)
			}
			deadline, category, err := sealed.Effective()
			if err != nil || deadline.Format("2006-01-02T15:04:05Z07:00") != tc.want || category != tc.category {
				t.Fatalf("deadline=%v category=%s err=%v", deadline, category, err)
			}
			sealed.AttemptDeadline = "2026-08-29T00:00:00Z"
			if _, _, err := sealed.Effective(); err == nil {
				t.Fatal("accepted recomputed recovery deadline")
			}
		})
	}
	for name, mutate := range map[string]func(*BusinessDeadlineWitness){
		"zero-budget":            func(w *BusinessDeadlineWitness) { w.RunTimeoutSeconds = 0 },
		"negative-budget":        func(w *BusinessDeadlineWitness) { w.AttemptTimeoutSeconds = -1 },
		"overflow":               func(w *BusinessDeadlineWitness) { w.RunTimeoutSeconds = math.MaxInt64 },
		"year-overflow":          func(w *BusinessDeadlineWitness) { w.RunCreatedAt = "9999-12-31T23:59:59Z" },
		"offset":                 func(w *BusinessDeadlineWitness) { w.RunCreatedAt = "2026-08-28T00:00:00+00:00" },
		"started-before-created": func(w *BusinessDeadlineWitness) { w.ProcessStartedAt = "2026-08-27T23:59:59Z" },
		"missing-spec":           func(w *BusinessDeadlineWitness) { w.SpecDigest = "" },
		"missing-started":        func(w *BusinessDeadlineWitness) { w.ProcessStartedFactDigest = "" },
	} {
		t.Run(name, func(t *testing.T) {
			w := testDeadlineWitness()
			mutate(&w)
			if _, err := SealBusinessDeadline(w); err == nil {
				t.Fatal("accepted invalid source")
			}
		})
	}
}

func TestStopIntentDigestIdentityAndDeadlineGuards(t *testing.T) {
	id := attemptTestIdentity()
	intent := testStopIntent(t, AttemptAuthorityState{Identity: id})
	for name, mutate := range map[string]func(*AttemptStopIntent){
		"actor":      func(s *AttemptStopIntent) { s.OperatorUID++ },
		"run-head":   func(s *AttemptStopIntent) { s.ExpectedAuthorityHead = attemptTestDigest("other") },
		"request-id": func(s *AttemptStopIntent) { s.RequestID = "cancel-2" },
		"sequence":   func(s *AttemptStopIntent) { s.ExpectedSequence++ },
		"observed":   func(s *AttemptStopIntent) { s.ObservedAt = "2026-08-28T00:00:04Z" },
		"category":   func(s *AttemptStopIntent) { s.Category = "provider-request" },
		"digest":     func(s *AttemptStopIntent) { s.IntentDigest = attemptTestDigest("forged") },
	} {
		t.Run(name, func(t *testing.T) {
			changed := intent
			mutate(&changed)
			if changed.Validate(id) == nil {
				t.Fatal("accepted changed intent")
			}
		})
	}
	other := id
	other.AttemptID = "attempt-2"
	if intent.Validate(other) == nil {
		t.Fatal("accepted cross-attempt replay")
	}
	w, err := SealBusinessDeadline(testDeadlineWitness())
	if err != nil {
		t.Fatal(err)
	}
	intent.Deadline, intent.Category, intent.ObservedAt = w, StopAttemptDeadline, w.AttemptDeadline
	if _, err := SealAttemptStopIntent(id, intent); err != nil {
		t.Fatal(err)
	}
	intent.ObservedAt = "2026-08-28T00:00:04Z"
	if _, err := SealAttemptStopIntent(id, intent); err == nil {
		t.Fatal("accepted premature timeout")
	}
	intent.ObservedAt, intent.Category = w.AttemptDeadline, StopRunDeadline
	if _, err := SealAttemptStopIntent(id, intent); err == nil {
		t.Fatal("accepted wrong deadline category")
	}
	intent.Category = StopOperatorRequest
	if _, err := SealAttemptStopIntent(id, intent); err == nil {
		t.Fatal("accepted deadline witness on operator stop")
	}
	if (AttemptStopIntent{Category: "unknown"}).Eligibility() != (EligibilityTerminal{}) {
		t.Fatal("unknown category grants terminal authority")
	}
}

func appendStopForTest(store *DurableStore, state AttemptAuthorityState, intent AttemptStopIntent) (AttemptAppendResult, error) {
	run := attemptTestRunAuthority(state.Identity)
	return store.CompareAndAppendBarrier(context.Background(), attemptRunVerifier{want: run}, state.Revision, state.HeadDigest,
		BarrierAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run},
		AttemptTransition{Kind: AttemptTransitionTerminalizationBarrier, Identity: state.Identity, TerminalizationID: "stop-1", EligibilityTerminal: intent.Eligibility(), StopIntent: intent})
}

func TestStopBarrierColdReplayAndAdmissionOrdering(t *testing.T) {
	for _, admittedFirst := range []bool{false, true} {
		name := "stop-first"
		if admittedFirst {
			name = "admission-first"
		}
		t.Run(name, func(t *testing.T) {
			store, err := OpenResultIngressStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			started := openStartedAttempt(t, store)
			ingress, err := NewDurableIngress(attemptTestBinding(), store)
			if err != nil {
				t.Fatal(err)
			}
			drc, envelope := attemptTestDRCForState(started, KindWorkerResult, 1)
			current := started
			if admittedFirst {
				if _, err := ingress.Admit(context.Background(), drc, envelope); err != nil {
					t.Fatal(err)
				}
				var found bool
				current, found, err = store.AttemptState(started.Identity)
				if err != nil || !found {
					t.Fatalf("found=%v err=%v", found, err)
				}
			}
			intent := testStopIntent(t, current)
			stopped, err := appendStopForTest(store, current, intent)
			if admittedFirst {
				if !errors.Is(err, ErrStopTooLate) {
					t.Fatalf("stop err=%v", err)
				}
				unchanged, _, err := store.AttemptState(started.Identity)
				if err != nil || unchanged.HeadDigest != current.HeadDigest || unchanged.BarrierDigest != "" {
					t.Fatal("failed stop changed authority")
				}
				return
			}
			if err != nil || !stopped.Appended || !stopped.State.AdmissionClosed || stopped.State.StopIntent != intent || stopped.State.TerminalGeneration != started.Identity.DispatchGeneration+1 {
				t.Fatalf("stop appended=%v err=%v", stopped.Appended, err)
			}
			if _, err := ingress.Admit(context.Background(), drc, envelope); !errors.Is(err, ErrStaleLease) {
				t.Fatalf("late admission err=%v", err)
			}
			reopened, err := OpenResultIngressStore(store.dir)
			if err != nil {
				t.Fatal(err)
			}
			recovered, found, err := reopened.AttemptState(started.Identity)
			if err != nil || !found || recovered.StopIntent != intent || recovered.HeadDigest != stopped.State.HeadDigest {
				t.Fatalf("cold replay found=%v err=%v", found, err)
			}
			replay, err := appendStopForTest(reopened, current, intent)
			if err != nil || replay.Appended || replay.State.HeadDigest != stopped.State.HeadDigest {
				t.Fatalf("retry appended=%v err=%v", replay.Appended, err)
			}
			intent.RequestID = "cancel-2"
			intent, err = SealAttemptStopIntent(started.Identity, intent)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := appendStopForTest(reopened, current, intent); err == nil {
				t.Fatal("different request treated as exact replay")
			}
		})
	}
}

func TestStopAbsentPreservesLegacyFactBytes(t *testing.T) {
	b, err := json.Marshal(AttemptTransition{Kind: AttemptTransitionTerminalizationBarrier, Identity: attemptTestIdentity()})
	if err != nil || strings.Contains(string(b), "stopIntent") {
		t.Fatalf("absent stop serialized: err=%v", err)
	}
}

func TestStopDeadlineMustBindDurableProcessStart(t *testing.T) {
	for _, forged := range []bool{false, true} {
		name := "current-start"
		if forged {
			name = "different-start-fact"
		}
		t.Run(name, func(t *testing.T) {
			store, err := OpenResultIngressStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			started := openFreshStartedAttempt(t, store)
			w := testDeadlineWitness()
			w.ProcessStartedFactDigest, w.ProcessStartedAt = started.ProcessStartedDigest, started.ObservedAt
			if forged {
				w.ProcessStartedFactDigest = attemptTestDigest("another-start")
			}
			w, err = SealBusinessDeadline(w)
			if err != nil {
				t.Fatal(err)
			}
			intent := testStopIntent(t, started)
			intent.Category, intent.Deadline, intent.ObservedAt = StopAttemptDeadline, w, w.AttemptDeadline
			intent, err = SealAttemptStopIntent(started.Identity, intent)
			if err != nil {
				t.Fatal(err)
			}
			result, err := appendStopForTest(store, started, intent)
			if forged {
				if !errors.Is(err, ErrAttemptAuthorityConflict) {
					t.Fatalf("forged deadline err=%v", err)
				}
				current, _, err := store.AttemptState(started.Identity)
				if err != nil || current.HeadDigest != started.HeadDigest {
					t.Fatal("forged deadline changed authority")
				}
				return
			}
			if err != nil || !result.Appended || result.State.StopIntent != intent {
				t.Fatalf("current deadline appended=%v err=%v", result.Appended, err)
			}
		})
	}
}
