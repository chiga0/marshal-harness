//go:build darwin && arm64

package productionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
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
				event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: []string{"event-fixture-plan", "event-fixture-ready"}[index], RunID: frozen.RunID, Sequence: uint64(index + 1), Type: "run.transition", StateFrom: from, StateTo: target, Timestamp: frozen.PreparedAt, Payload: map[string]any{}}
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
