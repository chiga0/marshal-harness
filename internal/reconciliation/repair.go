package reconciliation

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

var errRepairFailed = errors.New("reconciliation repair failed")

type RepairResult struct {
	Outcome string `json:"outcome"`
	EventID string `json:"eventId,omitempty"`
	Report  Report `json:"report"`
}

// Repair explicitly rebuilds a local Snapshot from authoritative evidence.
// It holds the Run lease, appends one same-state audit event, then atomically
// writes state.json. Ambiguous evidence is refused without guessing.
func Repair(ctx context.Context, input Input, now time.Time) (RepairResult, error) {
	if input.Validator == nil || domain.ValidateID(input.RunID) != nil || !cleanAbsolute(input.StateRoot) || !cleanAbsolute(input.RepositoryRoot) {
		return RepairResult{}, errInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return RepairResult{}, err
	}
	store := runstore.New(input.StateRoot)
	lease, err := store.Acquire(input.RunID)
	if err != nil {
		return RepairResult{}, errRepairFailed
	}
	defer func() { _ = lease.Release() }()

	before, err := Inspect(ctx, input)
	if err != nil {
		return RepairResult{}, err
	}
	if before.Status == "ok" {
		return RepairResult{Outcome: "not-needed", Report: before}, nil
	}

	events, valid, err := repairEvents(ctx, input, store)
	if err != nil {
		return RepairResult{}, err
	}
	if !valid {
		return RepairResult{Outcome: "refused", Report: before}, nil
	}
	var state domain.RunState
	if staleOnly(before) && before.State != nil {
		state = *before.State
	} else {
		state, valid, err = rebuildSnapshot(ctx, input, events)
		if err != nil {
			return RepairResult{}, err
		}
		if !valid {
			return RepairResult{Outcome: "refused", Report: before}, nil
		}
	}
	if state.Sequence != uint64(len(events)) {
		return RepairResult{Outcome: "refused", Report: before}, nil
	}

	eventID := ""
	appendAudit := true
	if len(events) > 0 && events[len(events)-1].Type == lifecycle.RepairAuditEventType {
		eventID = events[len(events)-1].EventID
		appendAudit = false
	} else {
		if now.IsZero() {
			now = time.Now().UTC()
		}
		now = now.UTC()
		eventID, err = domain.NewID("repair")
		if err != nil {
			return RepairResult{}, errRepairFailed
		}
	}
	if snapshotDamaged(before) {
		if err := archiveDamagedSnapshot(ctx, input, eventID); err != nil {
			return RepairResult{}, errRepairFailed
		}
	}
	if appendAudit {
		event := domain.RunEvent{
			APIVersion: domain.APIVersionV1Alpha1,
			Kind:       domain.KindRunEvent,
			EventID:    eventID,
			RunID:      input.RunID,
			Sequence:   state.Sequence + 1,
			Type:       lifecycle.RepairAuditEventType,
			StateFrom:  state.State,
			StateTo:    state.State,
			Timestamp:  now,
			Actor:      &domain.Actor{Type: "system", ID: "marshal-reconciliation"},
			Payload: map[string]any{
				"repairKind":            "snapshot-rebuild",
				"sourceJournalSequence": state.Sequence,
			},
		}
		data, marshalErr := json.Marshal(event)
		if marshalErr != nil || input.Validator.Validate(domain.KindRunEvent, data) != nil {
			return RepairResult{}, errRepairFailed
		}
		if err := store.Append(lease, event, state.Sequence); err != nil {
			return RepairResult{}, errRepairFailed
		}
		state, err = lifecycle.Replay(state, event)
		if err != nil {
			return RepairResult{}, errRepairFailed
		}
	}

	if err := store.WriteSnapshot(lease, state); err != nil {
		return RepairResult{}, errRepairFailed
	}
	after, err := Inspect(ctx, input)
	if err != nil || after.Status != "ok" || after.State == nil || after.State.Sequence != state.Sequence {
		return RepairResult{}, errRepairFailed
	}
	return RepairResult{Outcome: "applied", EventID: eventID, Report: after}, nil
}

func staleOnly(report Report) bool {
	return len(report.Findings) == 1 && report.Findings[0].Code == "snapshot-stale" && report.Findings[0].Repairable
}

func repairEvents(ctx context.Context, input Input, store *runstore.Store) ([]domain.RunEvent, bool, error) {
	if _, status, err := readEvidence(ctx, filepath.Join(input.StateRoot, "runs", input.RunID, "events.jsonl")); err != nil {
		return nil, false, err
	} else if status != fileOK {
		return nil, false, nil
	}
	events, truncated, err := store.ReadEvents(input.RunID)
	if err != nil || truncated || len(events) == 0 {
		return nil, false, nil
	}
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		data, err := json.Marshal(event)
		if err != nil || input.Validator.Validate(domain.KindRunEvent, data) != nil || event.RunID != input.RunID || (event.Type == lifecycle.RepairAuditEventType && !validRepairAudit(event)) {
			return nil, false, nil
		}
	}
	return events, true, nil
}

func rebuildSnapshot(ctx context.Context, input Input, events []domain.RunEvent) (domain.RunState, bool, error) {
	if len(events) < 2 || events[0].Sequence != 1 || events[0].Type != "planning.spec-accepted" || events[0].StateFrom != domain.StateCreated || events[0].StateTo != domain.StatePlanned {
		return domain.RunState{}, false, nil
	}
	runDir := filepath.Join(input.StateRoot, "runs", input.RunID)
	taskData, taskStatus, err := readEvidence(ctx, filepath.Join(runDir, "task-spec.json"))
	if err != nil {
		return domain.RunState{}, false, err
	}
	if taskStatus != fileOK || input.Validator.Validate(domain.KindTask, taskData) != nil {
		return domain.RunState{}, false, nil
	}
	policyData, policyStatus, err := readEvidence(ctx, filepath.Join(runDir, "policy-snapshot.json"))
	if err != nil {
		return domain.RunState{}, false, err
	}
	if policyStatus != fileOK || input.Validator.Validate(domain.KindPolicySnapshot, policyData) != nil {
		return domain.RunState{}, false, nil
	}
	capabilityData, capabilityStatus, err := readEvidence(ctx, filepath.Join(runDir, "capability-snapshot.json"))
	if err != nil {
		return domain.RunState{}, false, err
	}
	if capabilityStatus != fileOK || input.Validator.Validate(domain.KindCapabilitySnapshot, capabilityData) != nil {
		return domain.RunState{}, false, nil
	}
	var task domain.TaskSpec
	var policy struct {
		TaskID string `json:"taskId"`
		RunID  string `json:"runId"`
	}
	if json.Unmarshal(taskData, &task) != nil || json.Unmarshal(policyData, &policy) != nil || task.Metadata.ID == "" || task.Repository.Path != input.RepositoryRoot || !cleanAbsolute(task.Repository.Path) || policy.TaskID != task.Metadata.ID || policy.RunID != input.RunID {
		return domain.RunState{}, false, nil
	}
	specDigest, err := canonical.DigestJSON(taskData)
	if err != nil {
		return domain.RunState{}, false, nil
	}
	policyDigest, err := canonical.DigestJSON(policyData)
	if err != nil {
		return domain.RunState{}, false, nil
	}
	capabilityDigest, err := canonical.DigestJSON(capabilityData)
	if err != nil {
		return domain.RunState{}, false, nil
	}
	if payloadString(events[0].Payload, "specDigest") != specDigest {
		return domain.RunState{}, false, nil
	}

	var ready *domain.RunEvent
	for index := range events {
		if events[index].Type == "planning.inputs-frozen" {
			if ready != nil {
				return domain.RunState{}, false, nil
			}
			ready = &events[index]
		}
	}
	if ready == nil || ready.StateFrom != domain.StatePlanned || ready.StateTo != domain.StateReady || payloadString(ready.Payload, "specDigest") != specDigest || payloadString(ready.Payload, "policyDigest") != policyDigest || payloadString(ready.Payload, "capabilityDigest") != capabilityDigest {
		return domain.RunState{}, false, nil
	}
	baseSHA := payloadString(ready.Payload, "baseSha")
	worktreePath := payloadString(ready.Payload, "worktreePath")
	if !regexp.MustCompile(`^[0-9a-f]{40,64}$`).MatchString(baseSHA) || !managedWorktreePath(input.StateRoot, worktreePath) {
		return domain.RunState{}, false, nil
	}

	state := domain.NewRunState(task.Metadata.ID, input.RunID, events[0].Timestamp)
	state.SpecDigest = specDigest
	state.PolicyDigest = policyDigest
	state.CapabilityDigest = capabilityDigest
	state.BaseSHA = baseSHA
	state.WorktreePath = worktreePath
	for _, event := range events {
		state, err = lifecycle.Replay(state, event)
		if err != nil {
			return domain.RunState{}, false, nil
		}
	}
	stateData, err := json.Marshal(state)
	if err != nil || input.Validator.Validate(domain.KindRunState, stateData) != nil {
		return domain.RunState{}, false, nil
	}
	return state, true, nil
}

func managedWorktreePath(stateRoot, path string) bool {
	if !cleanAbsolute(path) {
		return false
	}
	relative, err := filepath.Rel(filepath.Join(stateRoot, "worktrees"), path)
	return err == nil && relative != "." && relative != "" && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func payloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func validRepairAudit(event domain.RunEvent) bool {
	if event.Actor == nil || event.Actor.Type != "system" || event.Actor.ID != "marshal-reconciliation" || payloadString(event.Payload, "repairKind") != "snapshot-rebuild" {
		return false
	}
	value, ok := event.Payload["sourceJournalSequence"]
	if !ok || event.Sequence == 0 {
		return false
	}
	want := event.Sequence - 1
	switch number := value.(type) {
	case float64:
		return number >= 0 && number == float64(want)
	case json.Number:
		parsed, err := strconv.ParseUint(string(number), 10, 64)
		return err == nil && parsed == want
	case uint64:
		return number == want
	case int:
		return number >= 0 && uint64(number) == want
	default:
		return false
	}
}

func snapshotDamaged(report Report) bool {
	for _, finding := range report.Findings {
		if finding.Code == "snapshot-invalid" || finding.Code == "snapshot-identity-mismatch" {
			return true
		}
	}
	return false
}

func archiveDamagedSnapshot(ctx context.Context, input Input, eventID string) error {
	data, status, err := readEvidence(ctx, filepath.Join(input.StateRoot, "runs", input.RunID, "state.json"))
	if err != nil || status != fileOK {
		return err
	}
	directory := filepath.Join(input.StateRoot, "runs", input.RunID, "diagnostics")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	name := "state-before-" + strings.NewReplacer(":", "_", "/", "_").Replace(eventID) + ".json"
	target := filepath.Join(directory, name)
	if _, err := os.Lstat(target); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".repair-diagnostic-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, target)
}
