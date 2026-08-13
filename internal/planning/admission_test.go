package planning

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

const (
	admissionBaseSHA    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	admissionSpecDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// admissionAcceptedChain is the shortest legal journal path to ACCEPTED.
var admissionAcceptedChain = []domain.State{
	domain.StatePlanned,
	domain.StateReady,
	domain.StateRunning,
	domain.StateVerifying,
	domain.StateReviewPending,
	domain.StateAccepted,
}

// seedAdmissionRun journals the transition chain with fixture.transition
// events and writes a snapshot that matches the journal tail, mirroring the
// fixture pattern used by the control tests. The resulting run passes the
// journal/snapshot consistency checks of the read-only Inspect.
func seedAdmissionRun(t *testing.T, store *runstore.Store, runID string, chain []domain.State, baseSHA, specDigest string) {
	t.Helper()
	lease, err := store.Acquire(runID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	current := domain.StateCreated
	for index, target := range chain {
		attemptID := ""
		if target == domain.StateRunning {
			attemptID = "attempt-01"
		}
		event := domain.RunEvent{
			APIVersion: domain.APIVersionV1Alpha1,
			Kind:       domain.KindRunEvent,
			EventID:    fmt.Sprintf("event-%02d", index+1),
			RunID:      runID,
			AttemptID:  attemptID,
			Sequence:   uint64(index + 1),
			Type:       "fixture.transition",
			StateFrom:  current,
			StateTo:    target,
			Timestamp:  time.Unix(int64(index+2), 0).UTC(),
			Payload:    map[string]any{},
		}
		if err := store.Append(lease, event, uint64(index)); err != nil {
			t.Fatal(err)
		}
		current = target
	}
	state := domain.NewRunState("task-admission", runID, time.Unix(1, 0).UTC())
	state.State = current
	state.Sequence = uint64(len(chain))
	state.BaseSHA = baseSHA
	state.SpecDigest = specDigest
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
}

func assertDependencyError(t *testing.T, err error, wantCategory, wantRunID, wantField string) {
	t.Helper()
	if err == nil {
		t.Fatalf("ResolveRunDependencies() error = nil, want %q", wantCategory)
	}
	message := err.Error()
	if !strings.Contains(message, wantCategory) || !strings.Contains(message, "runId="+wantRunID) {
		t.Fatalf("error = %q, want category %q naming runId=%s", message, wantCategory, wantRunID)
	}
	if wantField != "" && !strings.Contains(message, "field="+wantField) {
		t.Fatalf("error = %q, want field=%s", message, wantField)
	}
	if !port.IsPermanent(err) {
		t.Fatalf("error %q is not permanent", message)
	}
}

func TestResolveRunDependenciesSatisfied(t *testing.T) {
	store := runstore.New(t.TempDir())
	seedAdmissionRun(t, store, "run-dep-accepted", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest)
	seedAdmissionRun(t, store, "run-dep-ready", []domain.State{domain.StatePlanned, domain.StateReady}, admissionBaseSHA, admissionSpecDigest)

	// Exact ACCEPTED match with both optional bindings pinned.
	err := ResolveRunDependencies(store, []RunDependency{{
		RunID:         "run-dep-accepted",
		RequiredState: domain.StateAccepted,
		BaseSHA:       admissionBaseSHA,
		SpecDigest:    admissionSpecDigest,
	}})
	if err != nil {
		t.Fatalf("ResolveRunDependencies() = %v, want nil", err)
	}

	// Empty optional fields disable the corresponding checks.
	if err := ResolveRunDependencies(store, []RunDependency{{RunID: "run-dep-accepted", RequiredState: domain.StateAccepted}}); err != nil {
		t.Fatalf("ResolveRunDependencies(optional fields empty) = %v, want nil", err)
	}

	// Several satisfied dependencies resolve in order.
	err = ResolveRunDependencies(store, []RunDependency{
		{RunID: "run-dep-accepted", RequiredState: domain.StateAccepted},
		{RunID: "run-dep-ready", RequiredState: domain.StateReady, BaseSHA: admissionBaseSHA, SpecDigest: admissionSpecDigest},
	})
	if err != nil {
		t.Fatalf("ResolveRunDependencies(multiple) = %v, want nil", err)
	}
}

func TestResolveRunDependenciesEmptyList(t *testing.T) {
	store := runstore.New(t.TempDir())
	if err := ResolveRunDependencies(store, nil); err != nil {
		t.Fatalf("ResolveRunDependencies(nil) = %v, want nil", err)
	}
	if err := ResolveRunDependencies(store, []RunDependency{}); err != nil {
		t.Fatalf("ResolveRunDependencies(empty) = %v, want nil", err)
	}
}

func TestResolveRunDependenciesStateMismatch(t *testing.T) {
	store := runstore.New(t.TempDir())
	seedAdmissionRun(t, store, "run-dep-ready", []domain.State{domain.StatePlanned, domain.StateReady}, admissionBaseSHA, admissionSpecDigest)

	err := ResolveRunDependencies(store, []RunDependency{{RunID: "run-dep-ready", RequiredState: domain.StateAccepted}})
	assertDependencyError(t, err, ErrDependencyStateMismatch, "run-dep-ready", "state")
}

func TestResolveRunDependenciesUnknownRun(t *testing.T) {
	store := runstore.New(t.TempDir())
	seedAdmissionRun(t, store, "run-dep-accepted", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest)

	err := ResolveRunDependencies(store, []RunDependency{{RunID: "run-missing", RequiredState: domain.StateAccepted}})
	assertDependencyError(t, err, ErrDependencyRunNotFound, "run-missing", "")
}

func TestResolveRunDependenciesBaseSHAMismatch(t *testing.T) {
	store := runstore.New(t.TempDir())
	seedAdmissionRun(t, store, "run-dep-accepted", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest)

	err := ResolveRunDependencies(store, []RunDependency{{
		RunID:         "run-dep-accepted",
		RequiredState: domain.StateAccepted,
		BaseSHA:       strings.Repeat("f", 40),
	}})
	assertDependencyError(t, err, ErrDependencyBaseMismatch, "run-dep-accepted", "baseSha")
}

func TestResolveRunDependenciesSpecDigestMismatch(t *testing.T) {
	store := runstore.New(t.TempDir())
	seedAdmissionRun(t, store, "run-dep-accepted", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest)

	err := ResolveRunDependencies(store, []RunDependency{{
		RunID:         "run-dep-accepted",
		RequiredState: domain.StateAccepted,
		SpecDigest:    "sha256:" + strings.Repeat("e", 64),
	}})
	assertDependencyError(t, err, ErrDependencyDigestMismatch, "run-dep-accepted", "specDigest")
}

func TestResolveRunDependenciesUnreadableStateFailsClosed(t *testing.T) {
	root := t.TempDir()
	store := runstore.New(root)
	seedAdmissionRun(t, store, "run-dep-corrupt", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest)
	if err := os.WriteFile(filepath.Join(root, "runs", "run-dep-corrupt", "state.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := ResolveRunDependencies(store, []RunDependency{{RunID: "run-dep-corrupt", RequiredState: domain.StateAccepted}})
	assertDependencyError(t, err, ErrDependencyRunUnreadable, "run-dep-corrupt", "")

	// A syntactically invalid run ID fails closed instead of escaping.
	err = ResolveRunDependencies(store, []RunDependency{{RunID: "../escape", RequiredState: domain.StateAccepted}})
	assertDependencyError(t, err, ErrDependencyRunUnreadable, "../escape", "")
}

func TestResolveRunDependenciesIsReadOnlyAndOrdered(t *testing.T) {
	store := runstore.New(t.TempDir())
	seedAdmissionRun(t, store, "run-dep-accepted", admissionAcceptedChain, admissionBaseSHA, admissionSpecDigest)

	// The first failing dependency determines the error.
	err := ResolveRunDependencies(store, []RunDependency{
		{RunID: "run-missing", RequiredState: domain.StateAccepted},
		{RunID: "run-dep-accepted", RequiredState: domain.StateAccepted},
	})
	assertDependencyError(t, err, ErrDependencyRunNotFound, "run-missing", "")

	// Resolution never mutates the depended-on run.
	state, err := store.Inspect("run-dep-accepted")
	if err != nil || state.State != domain.StateAccepted || state.Sequence != uint64(len(admissionAcceptedChain)) ||
		state.BaseSHA != admissionBaseSHA || state.SpecDigest != admissionSpecDigest {
		t.Fatalf("inspect after resolution = %+v, err = %v", state, err)
	}
	events, truncated, err := store.ReadEvents("run-dep-accepted")
	if err != nil || truncated || len(events) != len(admissionAcceptedChain) {
		t.Fatalf("journal after resolution = %d events, truncated = %v, err = %v", len(events), truncated, err)
	}
}
