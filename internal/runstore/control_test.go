package runstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
)

const testDigest = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newControlValidator(t *testing.T) ControlValidator {
	t.Helper()
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	return validator
}

func newControlStore(t *testing.T) (*Store, *Lease) {
	t.Helper()
	store := New(t.TempDir())
	lease, err := store.Acquire("run:1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	state := domain.NewRunState("task:1", "run:1", time.Unix(1, 0).UTC())
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	return store, lease
}

func approvalFixture(sequence uint64) domain.ApprovalRecord {
	return domain.ApprovalRecord{
		APIVersion:      domain.APIVersionV1Alpha1,
		Kind:            domain.KindApprovalRecord,
		RecordID:        fmt.Sprintf("approval:%d", sequence),
		TaskID:          "task:1",
		RunID:           "run:1",
		ControlSequence: sequence,
		Gate:            domain.ApprovalGatePlan,
		Source:          domain.ControlSource{Type: domain.ControlSourceTypeHuman, ID: "operator-1"},
		Binding: domain.ApprovalBinding{
			StateSequence:    0,
			SpecDigest:       testDigest,
			PolicyDigest:     testDigest,
			CapabilityDigest: testDigest,
			BaseSHA:          strings.Repeat("a", 40),
		},
		Outcome:   domain.ApprovalOutcomeApproved,
		CreatedAt: time.Unix(2, 0).UTC(),
	}
}

func interventionFixture(sequence uint64) domain.InterventionRecord {
	return domain.InterventionRecord{
		APIVersion:      domain.APIVersionV1Alpha1,
		Kind:            domain.KindInterventionRecord,
		RecordID:        fmt.Sprintf("intervention:%d", sequence),
		TaskID:          "task:1",
		RunID:           "run:1",
		ControlSequence: sequence,
		StateSequence:   0,
		AttemptID:       "attempt:1",
		Category:        domain.InterventionCategoryPause,
		Source:          domain.ControlSource{Type: domain.ControlSourceTypeHuman, ID: "operator-1"},
		Effect:          domain.InterventionEffectPaused,
		CreatedAt:       time.Unix(3, 0).UTC(),
	}
}

func TestControlRecordsAppendAndReadTyped(t *testing.T) {
	t.Parallel()
	store, lease := newControlStore(t)
	validator := newControlValidator(t)
	if err := store.AppendApproval(lease, validator, approvalFixture(1)); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendIntervention(lease, validator, interventionFixture(2)); err != nil {
		t.Fatal(err)
	}
	records, err := store.ReadControlRecords("run:1", validator)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("record count = %d", len(records))
	}
	if records[0].Kind != domain.KindApprovalRecord || records[0].Approval == nil || records[0].Intervention != nil {
		t.Fatalf("first record = %+v", records[0])
	}
	if records[0].Approval.RecordID != "approval:1" || records[0].Approval.Gate != domain.ApprovalGatePlan {
		t.Fatalf("approval fields = %+v", records[0].Approval)
	}
	if records[1].Kind != domain.KindInterventionRecord || records[1].Intervention == nil || records[1].Approval != nil {
		t.Fatalf("second record = %+v", records[1])
	}
	if records[1].Intervention.RecordID != "intervention:2" || records[1].Intervention.Category != domain.InterventionCategoryPause {
		t.Fatalf("intervention fields = %+v", records[1].Intervention)
	}
}

func TestControlMutationHookRejectsReplacementBeforeAnyBytes(t *testing.T) {
	store, lease := newControlStore(t)
	validator := newControlValidator(t)
	runDirectory, err := store.runDir("run:1")
	if err != nil {
		t.Fatal(err)
	}
	oldDirectory := runDirectory + ".old"
	lease.beforeMutation = func() error {
		lease.beforeMutation = nil
		if err := os.Rename(runDirectory, oldDirectory); err != nil {
			return err
		}
		if err := os.Mkdir(runDirectory, 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(runDirectory, "lease.lock"), nil, 0o600)
	}
	if err := store.AppendApproval(lease, validator, approvalFixture(1)); err == nil {
		t.Fatal("control mutation crossed a replaced run authority")
	}
	for _, directory := range []string{runDirectory, oldDirectory} {
		path := filepath.Join(directory, "control", "records.jsonl")
		if data, readErr := os.ReadFile(path); readErr == nil && len(data) != 0 {
			t.Fatalf("%s received unauthorized bytes: %q", directory, data)
		} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			t.Fatal(readErr)
		}
	}
}

func TestControlAppendRequiresMatchingLease(t *testing.T) {
	t.Parallel()
	store, _ := newControlStore(t)
	validator := newControlValidator(t)
	if err := store.AppendApproval(nil, validator, approvalFixture(1)); err == nil {
		t.Fatal("append without lease succeeded")
	}
	other, err := store.Acquire("run:2")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Release()
	if err := store.AppendApproval(other, validator, approvalFixture(1)); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-run lease error = %v", err)
	}
	released, err := store.Acquire("run:3")
	if err != nil {
		t.Fatal(err)
	}
	if err := released.Release(); err != nil {
		t.Fatal(err)
	}
	releasedRecord := approvalFixture(1)
	releasedRecord.RunID = "run:3"
	if err := store.AppendApproval(released, validator, releasedRecord); err == nil {
		t.Fatal("append with released lease succeeded")
	}
}

func TestControlSequenceAndRecordIDConflicts(t *testing.T) {
	t.Parallel()
	store, lease := newControlStore(t)
	validator := newControlValidator(t)
	stale := approvalFixture(2)
	if err := store.AppendApproval(lease, validator, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale sequence error = %v", err)
	}
	if err := store.AppendApproval(lease, validator, approvalFixture(1)); err != nil {
		t.Fatal(err)
	}
	duplicateID := interventionFixture(2)
	duplicateID.RecordID = "approval:1"
	if err := store.AppendIntervention(lease, validator, duplicateID); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate record ID error = %v", err)
	}
	duplicateSequence := interventionFixture(1)
	if err := store.AppendIntervention(lease, validator, duplicateSequence); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate sequence error = %v", err)
	}
	if err := store.AppendIntervention(lease, validator, interventionFixture(2)); err != nil {
		t.Fatal(err)
	}
}

func TestControlAppendRejectsSchemaViolation(t *testing.T) {
	t.Parallel()
	store, lease := newControlStore(t)
	validator := newControlValidator(t)
	invalid := approvalFixture(1)
	invalid.Gate = "bogus"
	if err := store.AppendApproval(lease, validator, invalid); err == nil {
		t.Fatal("schema-invalid approval accepted")
	}
	if _, err := store.ReadControlRecords("run:1", validator); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal should not exist, error = %v", err)
	}
}

func TestControlAppendRejectsIdentityAndSequenceBinding(t *testing.T) {
	t.Parallel()
	store, lease := newControlStore(t)
	validator := newControlValidator(t)
	wrongTask := approvalFixture(1)
	wrongTask.TaskID = "task:2"
	if err := store.AppendApproval(lease, validator, wrongTask); !errors.Is(err, ErrConflict) {
		t.Fatalf("task identity error = %v", err)
	}
	future := approvalFixture(1)
	future.Binding.StateSequence = 1
	if err := store.AppendApproval(lease, validator, future); !errors.Is(err, ErrConflict) {
		t.Fatalf("future binding error = %v", err)
	}
	event := transition("event:1", 1, domain.StateCreated, domain.StatePlanned)
	if err := store.Append(lease, event, 0); err != nil {
		t.Fatal(err)
	}
	stale := approvalFixture(1)
	if err := store.AppendApproval(lease, validator, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale binding error = %v", err)
	}
	current := approvalFixture(1)
	current.Binding.StateSequence = 1
	if err := store.AppendApproval(lease, validator, current); err != nil {
		t.Fatal(err)
	}
}

func TestControlReadRejectsCorruptJournal(t *testing.T) {
	t.Parallel()
	wrongIdentity := approvalFixture(1)
	wrongIdentity.RunID = "run:2"
	wrongIdentityLine, err := marshalControlRecord(wrongIdentity)
	if err != nil {
		t.Fatal(err)
	}
	futureBinding := approvalFixture(1)
	futureBinding.Binding.StateSequence = 1
	futureBindingLine, err := marshalControlRecord(futureBinding)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		content string
	}{
		{name: "malformed json", content: "{not-json}\n"},
		{name: "unknown kind", content: `{"apiVersion":"marshal.dev/v1alpha1","kind":"Outcome"}` + "\n"},
		{name: "unsupported apiVersion", content: `{"apiVersion":"marshal.dev/v9","kind":"ApprovalRecord"}` + "\n"},
		{name: "schema-invalid record", content: `{"apiVersion":"marshal.dev/v1alpha1","kind":"ApprovalRecord","recordId":"approval:1","controlSequence":1}` + "\n"},
		{name: "wrong run identity", content: wrongIdentityLine},
		{name: "future state binding", content: futureBindingLine},
		{name: "blank record", content: "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, lease := newControlStore(t)
			writeControlJournal(t, store, "run:1", tc.content)
			validator := newControlValidator(t)
			if _, err := store.ReadControlRecords("run:1", validator); !errors.Is(err, ErrConflict) {
				t.Fatalf("read error = %v", err)
			}
			if err := store.AppendApproval(lease, validator, approvalFixture(1)); err == nil {
				t.Fatal("append over corrupt journal succeeded")
			}
		})
	}
}

func TestControlReadRejectsSequenceGapAndDuplicate(t *testing.T) {
	t.Parallel()
	valid, err := marshalControlRecord(approvalFixture(1))
	if err != nil {
		t.Fatal(err)
	}
	second := approvalFixture(1)
	second.RecordID = "approval:1b"
	gap := approvalFixture(2)
	gapLine, err := marshalControlRecord(gap)
	if err != nil {
		t.Fatal(err)
	}
	secondLine, err := marshalControlRecord(second)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		content string
	}{
		{name: "sequence gap", content: gapLine},
		{name: "duplicate sequence", content: valid + secondLine},
		{name: "duplicate record ID", content: valid + valid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, _ := newControlStore(t)
			writeControlJournal(t, store, "run:1", tc.content)
			if _, err := store.ReadControlRecords("run:1", newControlValidator(t)); !errors.Is(err, ErrConflict) {
				t.Fatalf("read error = %v", err)
			}
		})
	}
}

func TestControlTruncatedTailFailsClosed(t *testing.T) {
	t.Parallel()
	store, lease := newControlStore(t)
	validator := newControlValidator(t)
	if err := store.AppendApproval(lease, validator, approvalFixture(1)); err != nil {
		t.Fatal(err)
	}
	journal := controlJournalPath(t, store, "run:1")
	file, err := os.OpenFile(journal, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString(`{"apiVersion":"marshal.dev/v1alpha1"`)
	_ = file.Close()
	records, err := store.ReadControlRecords("run:1", validator)
	if !errors.Is(err, ErrTruncatedTail) || len(records) != 1 {
		t.Fatalf("records = %d, error = %v", len(records), err)
	}
	if err := store.AppendIntervention(lease, validator, interventionFixture(2)); !errors.Is(err, ErrTruncatedTail) {
		t.Fatalf("append over truncated tail error = %v", err)
	}
}

func TestControlJournalPermissions(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not enforced on Windows")
	}
	store, lease := newControlStore(t)
	if err := store.AppendApproval(lease, newControlValidator(t), approvalFixture(1)); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(controlDirPath(t, store, "run:1"))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("control directory mode = %v", dirInfo.Mode().Perm())
	}
	fileInfo, err := os.Stat(controlJournalPath(t, store, "run:1"))
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("control journal mode = %v", fileInfo.Mode().Perm())
	}
}

func TestControlJournalRejectsOversizedAppend(t *testing.T) {
	t.Parallel()
	if maxControlJournalBytes != 32<<20 {
		t.Fatalf("default limit = %d", maxControlJournalBytes)
	}
	store, lease := newControlStore(t)
	validator := newControlValidator(t)
	if err := store.AppendApproval(lease, validator, approvalFixture(1)); err != nil {
		t.Fatal(err)
	}
	journal := controlJournalPath(t, store, "run:1")
	before, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	// Any non-empty record must exceed a limit equal to the current file size.
	err = store.appendControlRecord(lease, validator, controlEntry{
		kind:            domain.KindInterventionRecord,
		recordID:        "intervention:2",
		runID:           "run:1",
		taskID:          "task:1",
		stateSequence:   0,
		controlSequence: 2,
	}, interventionFixture(2), int64(len(before)))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("oversized append error = %v", err)
	}
	after, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("oversized append modified the journal")
	}
}

func TestControlReadRejectsOversizedJournalBeforeDecode(t *testing.T) {
	t.Parallel()
	store, _ := newControlStore(t)
	validator := newControlValidator(t)
	writeControlJournal(t, store, "run:1", "{}\n")
	if _, _, err := store.readControlJournal("run:1", "task:1", 0, validator, 2); !errors.Is(err, ErrConflict) {
		t.Fatalf("oversized journal read error = %v", err)
	}
}

func TestControlAppendDoesNotTouchEventsOrState(t *testing.T) {
	t.Parallel()
	store, lease := newControlStore(t)
	runDir := filepath.Join(store.root, "runs", "run:1")
	eventsPath := filepath.Join(runDir, "events.jsonl")
	eventsBefore, eventsErr := os.ReadFile(eventsPath)
	if eventsErr != nil && !errors.Is(eventsErr, os.ErrNotExist) {
		t.Fatal(eventsErr)
	}
	stateBefore, err := os.ReadFile(filepath.Join(runDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendApproval(lease, newControlValidator(t), approvalFixture(1)); err != nil {
		t.Fatal(err)
	}
	eventsAfter, eventsErr := os.ReadFile(eventsPath)
	if errors.Is(eventsErr, os.ErrNotExist) {
		if len(eventsBefore) > 0 {
			t.Fatal("control append removed events.jsonl")
		}
	} else if eventsErr != nil {
		t.Fatal(eventsErr)
	} else if string(eventsBefore) != string(eventsAfter) {
		t.Fatal("control append modified events.jsonl")
	}
	stateAfter, err := os.ReadFile(filepath.Join(runDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stateBefore) != string(stateAfter) {
		t.Fatal("control append modified state.json")
	}
	state, err := store.Inspect("run:1")
	if err != nil || state.Sequence != 0 || state.State != domain.StateCreated {
		t.Fatalf("run state after control append = %+v, error = %v", state, err)
	}
}

func marshalControlRecord(record any) (string, error) {
	data, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	return string(data) + "\n", nil
}

func writeControlJournal(t *testing.T, store *Store, runID, content string) {
	t.Helper()
	directory := controlDirPath(t, store, runID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "records.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func controlDirPath(t *testing.T, store *Store, runID string) string {
	t.Helper()
	return filepath.Join(store.root, "runs", runID, "control")
}

func controlJournalPath(t *testing.T, store *Store, runID string) string {
	t.Helper()
	return filepath.Join(controlDirPath(t, store, runID), "records.jsonl")
}
