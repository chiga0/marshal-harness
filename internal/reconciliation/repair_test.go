package reconciliation

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

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

func TestRepairCorruptSnapshotAppendsAuditAndRebuilds(t *testing.T) {
	fixture := newReadyFixture(t)
	statePath := filepath.Join(fixture.runDir, "state.json")
	if err := os.WriteFile(statePath, []byte(`{"secret":"must-not-leak"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 13, 0, 0, 0, time.UTC)
	result, err := Repair(context.Background(), fixture.input, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "applied" || result.EventID == "" || result.Report.Status != "ok" || result.Report.State == nil {
		t.Fatalf("result = %+v", result)
	}
	if result.Report.State.State != domain.StateReady || result.Report.State.Sequence != 3 || !result.Report.State.UpdatedAt.Equal(now) {
		t.Fatalf("repaired state = %+v", result.Report.State)
	}
	events, _, err := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
	if err != nil || len(events) != 3 {
		t.Fatalf("events = %d, err = %v", len(events), err)
	}
	repair := events[2]
	if repair.EventID != result.EventID || repair.Type != lifecycle.RepairAuditEventType || repair.StateFrom != domain.StateReady || repair.StateTo != domain.StateReady || repair.Payload["repairKind"] != "snapshot-rebuild" {
		t.Fatalf("repair event = %+v", repair)
	}
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), "must-not-leak") || strings.Contains(string(data), statePath) {
		t.Fatalf("repair leaked damaged evidence: %s", data)
	}
	diagnostics, err := filepath.Glob(filepath.Join(fixture.runDir, "diagnostics", "state-before-*.json"))
	if err != nil || len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %v, err = %v", diagnostics, err)
	}
	archived, err := os.ReadFile(diagnostics[0])
	if err != nil || !strings.Contains(string(archived), "must-not-leak") {
		t.Fatalf("damaged snapshot was not preserved: %q, %v", archived, err)
	}
	info, err := os.Stat(diagnostics[0])
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("diagnostic permissions = %v", info.Mode().Perm())
	}
}

func TestRepairMissingSnapshotAndAlreadyHealthy(t *testing.T) {
	fixture := newReadyFixture(t)
	if err := os.Remove(filepath.Join(fixture.runDir, "state.json")); err != nil {
		t.Fatal(err)
	}
	result, err := Repair(context.Background(), fixture.input, time.Unix(20, 0))
	if err != nil || result.Outcome != "applied" || result.Report.State == nil || result.Report.State.Sequence != 3 {
		t.Fatalf("missing snapshot repair = %+v, err = %v", result, err)
	}
	again, err := Repair(context.Background(), fixture.input, time.Unix(30, 0))
	if err != nil || again.Outcome != "not-needed" || again.EventID != "" || again.Report.Status != "ok" {
		t.Fatalf("healthy repair = %+v, err = %v", again, err)
	}
}

func TestRepairRetryAfterAuditAppendDoesNotDuplicateEvent(t *testing.T) {
	fixture := newReadyFixture(t)
	statePath := filepath.Join(fixture.runDir, "state.json")
	if err := os.WriteFile(statePath, []byte(`{"broken":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := Repair(context.Background(), fixture.input, time.Unix(20, 0))
	if err != nil || first.Outcome != "applied" {
		t.Fatalf("first repair = %+v, err = %v", first, err)
	}
	// Simulate a crash after the audit event committed but before the repaired
	// snapshot became durable by damaging state.json again.
	if err := os.WriteFile(statePath, []byte(`{"broken-again":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := Repair(context.Background(), fixture.input, time.Unix(30, 0))
	if err != nil || second.Outcome != "applied" || second.EventID != first.EventID {
		t.Fatalf("retry repair = %+v, err = %v", second, err)
	}
	events, _, err := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
	if err != nil || len(events) != 3 {
		t.Fatalf("repair retry duplicated journal event: %d, %v", len(events), err)
	}
}

func TestRepairRefusesAmbiguousEvidenceWithoutJournalMutation(t *testing.T) {
	tests := []struct {
		name string
		edit func(*testing.T, *localFixture)
	}{
		{"truncated journal", func(t *testing.T, fixture *localFixture) {
			file, err := os.OpenFile(filepath.Join(fixture.runDir, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteString(`{"partial":"secret"}`); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}},
		{"frozen digest mismatch", func(t *testing.T, fixture *localFixture) {
			if err := os.WriteFile(filepath.Join(fixture.runDir, "capability-snapshot.json"), []byte(`{"secret":"changed"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReadyFixture(t)
			if err := os.WriteFile(filepath.Join(fixture.runDir, "state.json"), []byte(`{"broken":true}`), 0o600); err != nil {
				t.Fatal(err)
			}
			test.edit(t, fixture)
			before, err := os.ReadFile(filepath.Join(fixture.runDir, "events.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			result, err := Repair(context.Background(), fixture.input, time.Unix(20, 0))
			if err != nil || result.Outcome != "refused" {
				t.Fatalf("result = %+v, err = %v", result, err)
			}
			after, err := os.ReadFile(filepath.Join(fixture.runDir, "events.jsonl"))
			if err != nil || string(after) != string(before) {
				t.Fatal("refused repair mutated journal")
			}
			data, _ := json.Marshal(result)
			if strings.Contains(string(data), "secret") || strings.Contains(string(data), fixture.runDir) {
				t.Fatalf("refused repair leaked evidence: %s", data)
			}
		})
	}
}

func TestRepairRequiresExclusiveLease(t *testing.T) {
	fixture := newReadyFixture(t)
	store := runstore.New(fixture.input.StateRoot)
	lease, err := store.Acquire(fixture.input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	_, err = Repair(context.Background(), fixture.input, time.Unix(20, 0))
	if !errors.Is(err, errRepairFailed) || strings.Contains(err.Error(), fixture.input.StateRoot) {
		t.Fatalf("lease error = %v", err)
	}
}

func TestInspectAndRepairRejectForgedRepairAudit(t *testing.T) {
	fixture := newReadyFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.runDir, "state.json"), []byte(`{"broken":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Repair(context.Background(), fixture.input, time.Unix(20, 0)); err != nil {
		t.Fatal(err)
	}
	store := runstore.New(fixture.input.StateRoot)
	events, _, err := store.ReadEvents(fixture.input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	events[len(events)-1].Payload["sourceJournalSequence"] = float64(999)
	var journal []byte
	for _, event := range events {
		journal = append(journal, marshalJSON(t, event)...)
		journal = append(journal, '\n')
	}
	writeLocalFile(t, fixture, "events.jsonl", journal)
	report, err := Inspect(context.Background(), fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	assertFinding(t, report, "journal-invalid", "error", false)
	result, err := Repair(context.Background(), fixture.input, time.Unix(30, 0))
	if err != nil || result.Outcome != "refused" {
		t.Fatalf("forged audit repair = %+v, err = %v", result, err)
	}
}

// TestRepairRefusesNonCanonicalAuditSequenceNotation proves repair only
// admits sourceJournalSequence as a canonical unsigned decimal integer: a
// committed audit event rewritten with a non-canonical notation that still
// decodes to the correct value must be refused.
func TestRepairRefusesNonCanonicalAuditSequenceNotation(t *testing.T) {
	fixture := newReadyFixture(t)
	statePath := filepath.Join(fixture.runDir, "state.json")
	if err := os.WriteFile(statePath, []byte(`{"broken":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := Repair(context.Background(), fixture.input, time.Unix(20, 0))
	if err != nil || first.Outcome != "applied" {
		t.Fatalf("first repair = %+v, err = %v", first, err)
	}
	path := filepath.Join(fixture.runDir, "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"sourceJournalSequence":2`)) {
		t.Fatalf("committed audit event does not carry the canonical literal: %s", data)
	}
	for _, notation := range []string{"2.0", "2e0", "-2", "02"} {
		forged := bytes.ReplaceAll(data, []byte(`"sourceJournalSequence":2`), []byte(`"sourceJournalSequence":`+notation))
		if err := os.WriteFile(path, forged, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(statePath, []byte(`{"broken-again":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		result, err := Repair(context.Background(), fixture.input, time.Unix(30, 0))
		if err != nil || result.Outcome != "refused" {
			t.Fatalf("notation %s accepted: %+v, err = %v", notation, result, err)
		}
	}
}

func newReadyFixture(t *testing.T) *localFixture {
	t.Helper()
	fixture := newLocalFixture(t)
	worktreePath := filepath.Join(fixture.input.StateRoot, "worktrees", "ENG-123")
	baseSHA := strings.Repeat("1", 40)
	started := fixture.state.CreatedAt
	planned := domain.RunEvent{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent,
		EventID: "planned-01", RunID: fixture.input.RunID, Sequence: 1,
		Type: "planning.spec-accepted", StateFrom: domain.StateCreated, StateTo: domain.StatePlanned,
		Timestamp: started, Actor: &domain.Actor{Type: "system", ID: "marshal-planning"},
		Payload: map[string]any{"specDigest": fixture.state.SpecDigest},
	}
	ready := domain.RunEvent{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent,
		EventID: "ready-02", RunID: fixture.input.RunID, Sequence: 2,
		Type: "planning.inputs-frozen", StateFrom: domain.StatePlanned, StateTo: domain.StateReady,
		Timestamp: started.Add(time.Second), Actor: &domain.Actor{Type: "system", ID: "marshal-planning"},
		Payload: map[string]any{
			"specDigest": fixture.state.SpecDigest, "policyDigest": fixture.state.PolicyDigest,
			"capabilityDigest": fixture.state.CapabilityDigest, "baseSha": baseSHA, "worktreePath": worktreePath,
		},
	}
	state := fixture.state
	var err error
	state, err = lifecycle.Replay(state, planned)
	if err != nil {
		t.Fatal(err)
	}
	state, err = lifecycle.Replay(state, ready)
	if err != nil {
		t.Fatal(err)
	}
	state.CapabilityDigest = fixture.state.CapabilityDigest
	state.BaseSHA = baseSHA
	state.WorktreePath = worktreePath
	fixture.state = state
	writeJSONFile(t, fixture, "state.json", state)
	var journal []byte
	for _, event := range []domain.RunEvent{planned, ready} {
		data := marshalJSON(t, event)
		journal = append(journal, data...)
		journal = append(journal, '\n')
	}
	writeLocalFile(t, fixture, "events.jsonl", journal)
	return fixture
}
