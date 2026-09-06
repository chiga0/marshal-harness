//go:build darwin && arm64

package productionruntime

import (
	"context"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

func TestFixedLifecycleStoppedCollectDistinguishesFreshHeadFromPendingReplay(t *testing.T) {
	fixture := newFixedDeliveryFixture(t)
	running := advanceFixedDeliveryRunToRunning(t, fixture)
	request := application.CollectRunResultRequest{RunID: running.RunID, AttemptID: running.AttemptID, ExpectedSequence: running.Sequence, ExpectedAuthorityHead: running.AuthorityHead}
	begin := func(key string, input application.CollectRunResultRequest) (FixedLifecyclePending, bool, error) {
		binding, err := NewFixedLifecycleDeliveryBinding(key, FixedLifecycleCollectOperation, input, fixture.deadline)
		if err != nil {
			t.Fatal(err)
		}
		return fixture.store.BeginLifecycleBound(context.Background(), key, FixedLifecycleCollectOperation, input, application.CurrentRunRequest(input), fixture.deadline, binding)
	}
	original, replay, err := begin("collect:before-stop", request)
	if err != nil || replay {
		t.Fatalf("initial pending: %v", err)
	}
	lease, err := fixture.session.runs.AcquireExisting(running.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	state, err := runstore.InspectUnderLease(lease)
	if err != nil {
		t.Fatal(err)
	}
	// This fixture exercises the real delivery/store boundary only. Its
	// terminal references are synthetic, not proof of stop/cleanup authority.
	payload, err := stopEventPayload(stoppedAttemptFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	payload["originalRunAuthorityHead"] = running.AuthorityHead
	event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event:delivery-stopped", RunID: running.RunID, AttemptID: running.AttemptID, Sequence: running.Sequence + 1, Type: lifecycle.WorkerStoppedEventType, StateFrom: domain.StateRunning, StateTo: domain.StateBlocked, Timestamp: time.Date(2029, 1, 1, 0, 0, 4, 0, time.UTC), Actor: &domain.Actor{Type: "system", ID: "marshal-core"}, Payload: payload}
	next, err := lifecycle.Reduce(state, event, lifecycle.Guard{LeaseHeld: true, StopAuthorized: true, ChildrenStopped: true, EvidenceCurrent: true, EvidenceFlushed: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.session.runs.Append(lease, event, state.Sequence); err != nil {
		t.Fatal(err)
	}
	if err := fixture.session.runs.WriteSnapshot(lease, next); err != nil {
		t.Fatal(err)
	}
	authority, err := fixture.session.runs.ReadRunStartAuthorityUnderLease(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := begin("collect:fresh-stale-head", request); err == nil {
		t.Fatal("fresh request accepted stale RUNNING head")
	}
	if got, replay, err := begin("collect:before-stop", request); err != nil || !replay || got != original {
		t.Fatalf("original pending replay: %v", err)
	}
	current := request
	current.ExpectedSequence, current.ExpectedAuthorityHead = authority.Run.Sequence, authority.Run.AuthorityHead
	if _, replay, err := begin("collect:after-stop", current); err != nil || replay {
		t.Fatalf("fresh current terminal head: %v", err)
	}
	current.ExpectedSequence++
	if _, _, err := begin("collect:wrong-terminal", current); err == nil {
		t.Fatal("fresh request accepted wrong terminal sequence")
	}
}
