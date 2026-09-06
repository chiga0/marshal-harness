package resultingress

import (
	"context"
	"errors"
	"testing"
	"time"
)

func deadlineAdmissionFixture(t *testing.T) (*DurableStore, *Ingress, AttemptAuthorityState, BusinessDeadlineWitness) {
	t.Helper()
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	opened := appendFreshReservedAttempt(t, store, attemptTestIdentity())
	provisioned := appendTestAcceptedProvision(t, store, opened)
	authorized, err := appendAuthorizedAttempt(t, store, provisioned.Revision, provisioned.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: opened.Identity, LaunchAuthorizationID: "deadline-test-launch"})
	if err != nil {
		t.Fatal(err)
	}
	started := appendHistoricalAttemptTransition(t, store, authorized.State, AttemptTransition{Kind: AttemptTransitionProcessStarted, Identity: opened.Identity, CommandID: "deadline-test-command", ObservedAt: "2026-08-28T00:00:02Z", Process: attemptTestProcess(t), LaunchMaterialsDigest: authorized.State.LaunchMaterialsDigest, AgentLaunchSpecDigest: authorized.State.AgentLaunchSpecDigest})
	ingress, err := NewDurableIngress(attemptTestBinding(), store)
	if err != nil {
		t.Fatal(err)
	}
	w := testDeadlineWitness()
	w.ProcessStartedFactDigest, w.ProcessStartedAt = started.ProcessStartedDigest, started.ObservedAt
	w, err = SealBusinessDeadline(w)
	if err != nil {
		t.Fatal(err)
	}
	return store, ingress, started, w
}

func deadlineObservation() ResultObservationBinding {
	return ResultObservationBinding{ObservationDigest: attemptTestDigest("observation"), SnapshotDigest: attemptTestDigest("snapshot"), DiffDigest: attemptTestDigest("diff")}
}

func TestBusinessDeadlineAdmissionBoundaryAndReplay(t *testing.T) {
	for _, offset := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		t.Run(offset.String(), func(t *testing.T) {
			store, ingress, started, witness := deadlineAdmissionFixture(t)
			deadline, _, err := witness.Effective()
			if err != nil {
				t.Fatal(err)
			}
			ingress.clock = func() time.Time { return deadline.Add(offset) }
			drc, envelope := attemptTestDRCForState(started, KindWorkerResult, 1)
			fact, err := ingress.AdmitWithBusinessDeadline(context.Background(), drc, envelope, "", deadlineObservation(), witness)
			if offset >= 0 {
				if !errors.Is(err, ErrBusinessDeadlineExceeded) {
					t.Fatalf("late admission: %v", err)
				}
				current, found, err := store.AttemptState(started.Identity)
				if err != nil || !found || current.HeadDigest != started.HeadDigest || current.CommittedResultFactDigest != "" {
					t.Fatalf("late result changed authority: %+v %v", current, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			// A cold ingress after expiry must return the original effect, not
			// create a new admission or reinterpret success as a timeout.
			recovered, err := NewDurableIngress(attemptTestBinding(), store)
			if err != nil {
				t.Fatal(err)
			}
			recovered.clock = func() time.Time { return deadline.Add(time.Hour) }
			replay, err := recovered.AdmitWithBusinessDeadline(context.Background(), drc, envelope, "", deadlineObservation(), witness)
			if err != nil || !replay.IdempotentReplay || replay.FactDigest != fact.FactDigest {
				t.Fatalf("lost committed winner: %+v %v", replay, err)
			}
		})
	}
}

func TestBusinessDeadlineAdmissionRejectsSourceDrift(t *testing.T) {
	for name, mutate := range map[string]func(*BusinessDeadlineWitness){
		"spec":             func(w *BusinessDeadlineWitness) { w.SpecDigest = attemptTestDigest("other-spec") },
		"process-fact":     func(w *BusinessDeadlineWitness) { w.ProcessStartedFactDigest = attemptTestDigest("other-process") },
		"process-time":     func(w *BusinessDeadlineWitness) { w.ProcessStartedAt = "2026-08-28T00:00:03Z" },
		"derived-deadline": func(w *BusinessDeadlineWitness) { w.AttemptDeadline = "2026-08-29T00:00:05Z" },
	} {
		t.Run(name, func(t *testing.T) {
			store, ingress, started, witness := deadlineAdmissionFixture(t)
			ingress.clock = func() time.Time { return time.Date(2026, 8, 28, 0, 0, 3, 0, time.UTC) }
			mutate(&witness)
			drc, envelope := attemptTestDRCForState(started, KindWorkerResult, 1)
			if _, err := ingress.AdmitWithBusinessDeadline(context.Background(), drc, envelope, "", deadlineObservation(), witness); err == nil {
				t.Fatal("forged deadline admitted")
			}
			current, found, err := store.AttemptState(started.Identity)
			if err != nil || !found || current.HeadDigest != started.HeadDigest {
				t.Fatalf("invalid source changed authority: %+v %v", current, err)
			}
		})
	}
}
