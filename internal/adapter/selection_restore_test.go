package adapter

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

func TestRestoreSelectionRetainsSnapshotAndRechecksAdmissionWithoutProbe(t *testing.T) {
	worker := &probeSpy{id: "pi", err: errors.New("probe must not execute")}
	admissions := 0
	selector, err := NewAdmissionSelector(registryWith(t, worker), func(port.WorkerAdapter) error {
		admissions++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := capabilityRecord("pi", "supported")
	want := bytes.Clone(snapshot.Data)
	attempts := []SelectionAttempt{{AdapterID: "pi", Outcome: OutcomeSelected}}
	result, err := selector.RestoreSelection(context.Background(), SelectionRequest{PreferredAdapter: "pi", AllowedAdapters: []string{"pi"}}, snapshot, attempts)
	if err != nil || result.Adapter != worker || admissions != 1 || worker.calls != 0 {
		t.Fatalf("restore: admission=%d probes=%d err=%v", admissions, worker.calls, err)
	}
	clear(snapshot.Data)
	attempts[0].Outcome = "forged"
	if !bytes.Equal(result.Capability.Data, want) || result.Attempts[0].Outcome != OutcomeSelected {
		t.Fatal("restore retained mutable caller buffers")
	}
}

func TestRestoreSelectionInvalidEvidenceNeverProbes(t *testing.T) {
	for _, mode := range []string{"cancel", "nil-context", "nil-selector", "nil-registry", "empty-candidate", "fallback", "denied", "missing", "changed-id", "ineligible", "admission", "admission-cancel", "no-attempt", "extra-attempt", "wrong-attempt", "outcome", "wrong-kind", "wrong-snapshot-id", "unsupported", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			worker := &probeSpy{id: "pi"}
			selector, err := NewSelector(registryWith(t, worker))
			if err != nil {
				t.Fatal(err)
			}
			request := SelectionRequest{PreferredAdapter: "pi", AllowedAdapters: []string{"pi"}}
			snapshot := capabilityRecord("pi", "supported")
			attempts := []SelectionAttempt{{AdapterID: "pi", Outcome: OutcomeSelected}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var supplied context.Context = ctx
			switch mode {
			case "cancel":
				cancel()
			case "nil-context":
				supplied = nil
			case "nil-selector":
				selector = nil
			case "nil-registry":
				selector = &Selector{}
			case "empty-candidate":
				request.PreferredAdapter = ""
			case "fallback":
				request.FallbackAdapters = []string{"other"}
			case "denied":
				request.AllowedAdapters = nil
			case "missing":
				selector.registry = NewRegistry()
			case "changed-id":
				worker.id = "other"
			case "ineligible":
				selector.eligibility = func(port.WorkerAdapter) bool { return false }
			case "admission":
				selector.admission = func(port.WorkerAdapter) error { return errors.New("denied") }
			case "admission-cancel":
				selector.admission = func(port.WorkerAdapter) error { cancel(); return nil }
			case "no-attempt":
				attempts = nil
			case "extra-attempt":
				attempts = append(attempts, attempts[0])
			case "wrong-attempt":
				attempts[0].AdapterID = "other"
			case "outcome":
				attempts[0].Outcome = OutcomeProbeFailed
			case "wrong-kind":
				snapshot.Kind = domain.KindTask
			case "wrong-snapshot-id":
				snapshot = capabilityRecord("other", "supported")
			case "unsupported":
				snapshot = capabilityRecord("pi", "unsupported")
			case "malformed":
				snapshot.Data = []byte("{")
			}
			if _, err := selector.RestoreSelection(supplied, request, snapshot, attempts); err == nil || worker.calls != 0 {
				t.Fatalf("invalid restore: probes=%d err=%v", worker.calls, err)
			}
		})
	}
}
