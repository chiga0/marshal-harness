//go:build darwin && arm64

package productionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// This session fixture writes real descriptor-bound Run files but no Git
// worktree/Pi output. Planning recovery and the actual CLI factory are tested
// separately; this is not evidence that a real team has delivered a product.
func materializationFixture(t *testing.T) (publicFixedDeliveryInputs, application.ApproveInitialTeamRequest, *int, *int) {
	t.Helper()
	fixture := newPublicFixedDeliveryInputs(t)
	request := repositoryTeamCreationRequest(t, fixture)
	prepares, materializations := new(int), new(int)
	fixture.inputs.TeamInputPreflight = func(raw []byte) error {
		if !bytes.Equal(raw, request.Inputs) {
			return errors.New("fixture input mismatch")
		}
		return nil
	}
	fixture.inputs.TeamRunPreparer = func(ctx context.Context, task, policy []byte, runID string) ([]byte, error) {
		*prepares++
		return json.Marshal(map[string]any{
			"runId": runID, "repositoryRoot": fixture.repository, "baseSha": strings.Repeat("a", 40), "preparedAt": time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC),
			"task": json.RawMessage(task), "policy": json.RawMessage(policy), "capability": map[string]any{"adapterId": "pi", "probeStatus": "supported"},
			"selectionAttempts": []any{map[string]any{"AdapterID": "pi", "Outcome": "selected"}},
		})
	}
	fixture.inputs.TeamRunMaterializer = func(ctx context.Context, raw []byte, guard func(context.Context, func() error) error) (domain.RunState, error) {
		*materializations++
		var frozen struct {
			RunID      string          `json:"runId"`
			BaseSHA    string          `json:"baseSha"`
			PreparedAt time.Time       `json:"preparedAt"`
			Task       json.RawMessage `json:"task"`
			Policy     json.RawMessage `json:"policy"`
			Capability json.RawMessage `json:"capability"`
		}
		if err := json.Unmarshal(raw, &frozen); err != nil {
			return domain.RunState{}, err
		}
		var task struct {
			Metadata struct {
				ID string `json:"id"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(frozen.Task, &task); err != nil {
			return domain.RunState{}, err
		}
		state := domain.NewRunState(task.Metadata.ID, frozen.RunID, frozen.PreparedAt)
		state.SpecDigest = canonical.DigestBytes(frozen.Task)
		state.PolicyDigest = canonical.DigestBytes(frozen.Policy)
		state.CapabilityDigest = canonical.DigestBytes(frozen.Capability)
		state.BaseSHA = frozen.BaseSHA
		state.WorktreePath = filepath.Join(fixture.repository, "fixture-worktree")
		state.State = domain.StateReady
		state.Sequence = 2
		err := guard(ctx, func() error {
			store := runstore.New(filepath.Join(fixture.repository, ".marshal"))
			lease, err := store.Acquire(frozen.RunID)
			if err != nil {
				return err
			}
			defer lease.Release()
			if existing, err := runstore.InspectUnderLease(lease); err == nil {
				state = existing
				return nil
			}
			directory, err := runstore.OpenDirectoryUnderLease(lease)
			if err != nil {
				return err
			}
			for name, data := range map[string][]byte{"task-spec.json": frozen.Task, "policy-snapshot.json": frozen.Policy, "capability-snapshot.json": frozen.Capability} {
				if err := runstore.WriteFileInDirectory(directory, name, data, 0o600); err != nil {
					directory.Close()
					return err
				}
			}
			if err := directory.Close(); err != nil {
				return err
			}
			for index, target := range []domain.State{domain.StatePlanned, domain.StateReady} {
				from := domain.StateCreated
				if index == 1 {
					from = domain.StatePlanned
				}
				event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: []string{"event-fixture-plan", "event-fixture-ready"}[index], RunID: frozen.RunID, Sequence: uint64(index + 1), Type: []string{"planning.spec-accepted", "planning.inputs-frozen"}[index], StateFrom: from, StateTo: target, Timestamp: frozen.PreparedAt, Payload: map[string]any{}}
				if index == 1 {
					event.Payload = map[string]any{"specDigest": state.SpecDigest, "policyDigest": state.PolicyDigest, "capabilityDigest": state.CapabilityDigest,
						"baseSha": state.BaseSHA, "worktreePath": state.WorktreePath, "maxAttempts": 1}
				}
				if err := store.Append(lease, event, uint64(index)); err != nil {
					return err
				}
			}
			return store.WriteSnapshot(lease, state)
		})
		return state, err
	}
	return fixture, request, prepares, materializations
}

func TestRepositoryTeamMaterializationReadsBackAndReusesAfterColdOwner(t *testing.T) {
	fixture, request, prepares, materializations := materializationFixture(t)
	session, err := OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if _, err := session.MaterializeInitialTeamRun(context.Background(), request, "service"); err == nil || *prepares != 0 || *materializations != 0 {
		t.Fatal("unapproved materialization executed")
	}
	if _, err := session.ApproveInitialTeam(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	first, err := session.MaterializeInitialTeamRun(context.Background(), request, "service")
	if err != nil {
		t.Fatal(err)
	}
	if *prepares != 1 || *materializations != 1 || first.Sequence != 2 {
		t.Fatal("missing READY materialization")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	session, err = OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	second, err := session.MaterializeInitialTeamRun(context.Background(), request, "service")
	if err != nil || second.RunID != first.RunID || second.CreatedAt != first.CreatedAt || *prepares != 1 || *materializations != 2 {
		t.Fatalf("cold reuse: %v", err)
	}
	if _, err := session.MaterializeInitialTeamRun(context.Background(), request, "integration"); err == nil || *materializations != 2 {
		t.Fatal("integration materialized before dependency")
	}
}

func TestRepositoryTeamApprovedContinuationWithoutOriginalRequest(t *testing.T) {
	fixture, request, prepares, calls := materializationFixture(t)
	session, err := OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if _, err := session.MaterializeApprovedInitialTeamRun(context.Background(), "absent-goal", "service", canonical.DigestBytes([]byte("absent"))); err == nil || *prepares != 0 || *calls != 0 {
		t.Fatal("an unapproved selector created a Run")
	}
	approval, err := session.ApproveInitialTeam(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if *prepares != 0 || *calls != 0 {
		t.Fatal("approval eagerly materialized")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	// Simulate response loss and a cold server with only its real ledger. No
	// original request or transport deadline is supplied to continuation.
	session, err = OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range []string{"integration", "unknown-node"} {
		if _, err := session.MaterializeApprovedInitialTeamRun(context.Background(), approval.GoalID, node, approval.FactDigest); err == nil || *prepares != 0 || *calls != 0 {
			t.Fatal("non-ready node was materialized")
		}
	}
	if _, err := session.MaterializeApprovedInitialTeamRun(context.Background(), approval.GoalID, "service", canonical.DigestBytes([]byte("stale-plan"))); err == nil || *prepares != 0 || *calls != 0 {
		t.Fatal("stale plan selector reached the preparer")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := session.MaterializeApprovedInitialTeamRun(ctx, approval.GoalID, "service", approval.FactDigest); err == nil || *prepares != 0 || *calls != 0 {
		t.Fatal("canceled continuation reached the preparer")
	}
	first, err := session.MaterializeApprovedInitialTeamRun(context.Background(), approval.GoalID, "service", approval.FactDigest)
	if err != nil || first.State != domain.StateReady || first.Sequence != 2 || *prepares != 1 || *calls != 1 {
		t.Fatalf("approved cold continuation: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	session, err = OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := session.MaterializeApprovedInitialTeamRun(context.Background(), approval.GoalID, "service", approval.FactDigest)
	if err != nil || replayed.RunID != first.RunID || replayed.CreatedAt != first.CreatedAt || *prepares != 1 || *calls != 2 {
		t.Fatalf("frozen continuation repeated preparation: %v", err)
	}
}

func TestRepositoryTeamApprovedContinuationKeepsFailureBounded(t *testing.T) {
	for _, mode := range []string{"preflight", "prepare", "materialize"} {
		t.Run(mode, func(t *testing.T) {
			fixture, request, prepares, calls := materializationFixture(t)
			session, err := OpenRepositorySession(context.Background(), fixture.inputs)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close() })
			approval, err := session.ApproveInitialTeam(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			failureCalls := 0
			fail := func() error { failureCalls++; return errors.New("fixture structural failure") }
			switch mode {
			case "preflight":
				fixture.inputs.TeamInputPreflight = func([]byte) error { return fail() }
			case "prepare":
				fixture.inputs.TeamRunPreparer = func(context.Context, []byte, []byte, string) ([]byte, error) { return nil, fail() }
			case "materialize":
				fixture.inputs.TeamRunMaterializer = func(context.Context, []byte, func(context.Context, func() error) error) (domain.RunState, error) {
					return domain.RunState{}, fail()
				}
			}
			session, err = OpenRepositorySession(context.Background(), fixture.inputs)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := session.MaterializeApprovedInitialTeamRun(context.Background(), approval.GoalID, "service", approval.FactDigest); err == nil || failureCalls != 1 || *calls != 0 {
				t.Fatal("continuation ignored or retried a failure")
			}
			wantPrepared := 0
			if mode == "materialize" {
				wantPrepared = 1
			}
			if *prepares != wantPrepared {
				t.Fatal("failure caused unexpected preparation")
			}
			_, frozen, err := session.ingress.ReadTeamRunCreation(session.acquisition.Scope, approval.GoalID, "service")
			if err != nil || frozen != (mode == "materialize") {
				t.Fatal("failure lost frozen inputs or fabricated creation")
			}
			plan, found, err := session.ingress.ReadTeamPlan(session.acquisition.Scope, approval.GoalID)
			if err != nil || !found || plan.FactDigest != approval.FactDigest || len(plan.Materializations) != 3 {
				t.Fatal("failure changed original plan/budget obligations")
			}
		})
	}
}

func TestRepositoryTeamMaterializerCannotReplaceCommittedResultWithClaims(t *testing.T) {
	for _, mode := range []string{"missing", "failure", "forged-ready", "changed-result", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			fixture, request, _, calls := materializationFixture(t)
			original := fixture.inputs.TeamRunMaterializer
			if mode == "missing" {
				fixture.inputs.TeamRunMaterializer = nil
			} else {
				fixture.inputs.TeamRunMaterializer = func(ctx context.Context, raw []byte, guard func(context.Context, func() error) error) (domain.RunState, error) {
					if mode == "failure" {
						return domain.RunState{}, errors.New("fixture failure")
					}
					if mode == "forged-ready" {
						return domain.RunState{State: domain.StateReady, Sequence: 2}, nil
					}
					state, err := original(ctx, raw, guard)
					if mode == "changed-result" {
						state.SpecDigest = canonical.DigestBytes([]byte("forged"))
					}
					return state, err
				}
			}
			session, err := OpenRepositorySession(context.Background(), fixture.inputs)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			if _, err := session.ApproveInitialTeam(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			if _, err := session.MaterializeInitialTeamRun(ctx, request, "service"); err == nil {
				t.Fatal("invalid materialization accepted")
			}
			if (mode == "missing" || mode == "cancel") && *calls != 0 {
				t.Fatal("disabled/canceled materializer executed")
			}
		})
	}
}

func TestRepositoryTeamStartupRecoversFrozenCreationWithoutOriginalRequest(t *testing.T) {
	for _, mode := range []string{"missing-run", "partial-run", "ready"} {
		t.Run(mode, func(t *testing.T) {
			fixture, request, prepares, calls := materializationFixture(t)
			session, err := OpenRepositorySession(context.Background(), fixture.inputs)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close() })
			if err := session.RecoverInitialTeamCreations(context.Background()); err != nil || *prepares != 0 || *calls != 0 {
				t.Fatalf("empty ledger triggered creation: %v", err)
			}
			if _, err := session.ApproveInitialTeam(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			if err := session.RecoverInitialTeamCreations(context.Background()); err != nil || *prepares != 0 || *calls != 0 {
				t.Fatalf("unfrozen node was prepared at startup: %v", err)
			}
			creation, err := session.PrepareInitialTeamRun(context.Background(), request, "service")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "partial-run" {
				store := runstore.New(filepath.Join(fixture.repository, ".marshal"))
				lease, err := store.Acquire(creation.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if err := lease.Release(); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "ready" {
				if _, err := session.MaterializeInitialTeamRun(context.Background(), request, "service"); err != nil {
					t.Fatal(err)
				}
			}
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			// No original request, preflight or preparer is available on restart.
			fixture.inputs.TeamInputPreflight = nil
			fixture.inputs.TeamRunPreparer = nil
			session, err = OpenRepositorySession(context.Background(), fixture.inputs)
			if err != nil {
				t.Fatal(err)
			}
			before := *calls
			if err := session.RecoverInitialTeamCreations(context.Background()); err != nil {
				t.Fatal(err)
			}
			if *prepares != 1 || *calls != before+1 {
				t.Fatal("startup refreshed probe or omitted materialization")
			}
			lease, err := session.runs.AcquireExisting(creation.RunID)
			if err != nil {
				t.Fatal(err)
			}
			state, readErr := runstore.InspectUnderLease(lease)
			releaseErr := lease.Release()
			if readErr != nil || releaseErr != nil || state.RunID != creation.RunID || state.State != domain.StateReady || state.Sequence != 2 {
				t.Fatalf("original READY missing: %v %v", readErr, releaseErr)
			}
		})
	}
}

func TestRepositoryTeamStartupRejectsConflictAndMissingComposition(t *testing.T) {
	for _, mode := range []string{"missing-materializer", "malformed-journal", "leased", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			fixture, request, _, calls := materializationFixture(t)
			if mode == "missing-materializer" {
				fixture.inputs.TeamRunMaterializer = nil
			}
			session, err := OpenRepositorySession(context.Background(), fixture.inputs)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			if _, err := session.ApproveInitialTeam(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			creation, err := session.PrepareInitialTeamRun(context.Background(), request, "service")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "leased" || mode == "malformed-journal" {
				store := runstore.New(filepath.Join(fixture.repository, ".marshal"))
				lease, err := store.Acquire(creation.RunID)
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Release()
				if mode == "malformed-journal" {
					if err := lease.Release(); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(fixture.repository, ".marshal", "runs", creation.RunID, "events.jsonl"), []byte("{broken"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			if err := session.RecoverInitialTeamCreations(ctx); err == nil || *calls != 0 {
				t.Fatalf("invalid startup recovered: %v", err)
			}
		})
	}
}

func TestRepositoryTeamStartupDoesNotRecreateAdvancedRun(t *testing.T) {
	fixture, request, prepares, calls := materializationFixture(t)
	session, err := OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.ApproveInitialTeam(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	state, err := session.MaterializeInitialTeamRun(context.Background(), request, "service")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := session.runs.AcquireExisting(state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event-fixture-aborted", RunID: state.RunID, Sequence: 3, Type: lifecycle.AbortEventType, StateFrom: domain.StateReady, StateTo: domain.StateAborted, Timestamp: state.CreatedAt,
		Actor: &domain.Actor{Type: domain.ControlSourceTypeHuman, ID: "fixture-user"}, Payload: map[string]any{"terminalReason": lifecycle.PreAttemptAbortTerminalReason, "reason": "fixture abort"}}
	if err := lifecycle.ValidateTransition(state.State, state.RunID, state.Sequence, event); err != nil {
		lease.Release()
		t.Fatal(err)
	}
	if err := session.runs.Append(lease, event, state.Sequence); err != nil {
		lease.Release()
		t.Fatal(err)
	}
	state.State, state.Sequence = domain.StateAborted, 3
	if err := session.runs.WriteSnapshot(lease, state); err != nil {
		lease.Release()
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if err := session.RecoverInitialTeamCreations(context.Background()); err != nil || *prepares != 1 || *calls != 1 {
		t.Fatalf("advanced Run was recreated: %v", err)
	}
	// Existing Run authority alone is not enough if its creation input drifts.
	if err := os.WriteFile(filepath.Join(fixture.repository, ".marshal", "runs", state.RunID, "task-spec.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := session.RecoverInitialTeamCreations(context.Background()); err == nil || *calls != 1 {
		t.Fatal("advanced Run input conflict was silently skipped")
	}
}
