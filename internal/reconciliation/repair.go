package reconciliation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
	journalData, status, err := readEvidence(ctx, filepath.Join(input.StateRoot, "runs", input.RunID, "events.jsonl"))
	if err != nil {
		return nil, false, err
	} else if status != fileOK {
		return nil, false, nil
	}
	events, truncated, err := store.ReadEvents(input.RunID)
	if err != nil || truncated || len(events) == 0 {
		return nil, false, nil
	}
	rawLines := splitJournalLines(journalData)
	if len(rawLines) != len(events) {
		return nil, false, nil
	}
	for index, event := range events {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if !admitCanonicalJournalLine(rawLines[index]) {
			return nil, false, nil
		}
		data, err := json.Marshal(event)
		if err != nil || input.Validator.Validate(domain.KindRunEvent, data) != nil || event.RunID != input.RunID || (event.Type == lifecycle.RepairAuditEventType && !validRepairAudit(event)) {
			return nil, false, nil
		}
		if event.Type == lifecycle.RepairAuditEventType && !validRepairAuditLiteral(event, rawLines[index]) {
			return nil, false, nil
		}
	}
	return events, true, nil
}

// splitJournalLines returns the non-empty journal lines of raw data; it
// yields nil when the trailing newline is missing so the caller refuses the
// ambiguous evidence instead of guessing line boundaries.
func splitJournalLines(data []byte) [][]byte {
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return nil
	}
	var lines [][]byte
	for _, line := range bytes.Split(data[:len(data)-1], []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			lines = append(lines, line)
		}
	}
	return lines
}

// admitCanonicalJournalLine applies the same strict canonical admission the
// execution gate uses: invalid UTF-8, recursive duplicate object members, a
// trailing second JSON value and any other parse failure refuse the repair.
func admitCanonicalJournalLine(line []byte) bool {
	if !utf8.Valid(line) {
		return false
	}
	_, err := canonical.JSON(line)
	return err == nil
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

// canonicalRepairSequencePattern freezes the only admitted
// sourceJournalSequence notation: a canonical unsigned decimal integer —
// digits only, no sign, no decimal point, no exponent and no superfluous
// leading zeros ("0" itself is canonical, "04" is not). JSON number
// notations such as 2.0, 1e0 or -1 are rejected even though they decode to
// the same float64 value, and oversized literals that would lose precision
// are rejected by the bounded ParseUint.
var canonicalRepairSequencePattern = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

// validRepairAudit validates the parsed audit event. sourceJournalSequence
// arrives as float64 from the journal decoder, so only non-negative integral
// values that round-trip without precision loss can match; notation-level
// canonical strictness is enforced by validRepairAuditLiteral against the
// raw journal bytes.
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
		if number < 0 || number != math.Trunc(number) || number >= 1<<63 {
			return false
		}
		return uint64(number) == want
	case json.Number:
		if !canonicalRepairSequencePattern.MatchString(number.String()) {
			return false
		}
		parsed, err := strconv.ParseUint(number.String(), 10, 64)
		return err == nil && parsed == want
	default:
		return false
	}
}

// rawNumberLiteral recovers the verbatim JSON literal of a numeric payload
// member from the raw event line; the parsed float64 payload cannot express
// the original notation.
func rawNumberLiteral(rawLine []byte, key string) (string, bool) {
	decoder := json.NewDecoder(bytes.NewReader(rawLine))
	decoder.UseNumber()
	var wrapper struct {
		Payload map[string]any `json:"payload"`
	}
	if err := decoder.Decode(&wrapper); err != nil {
		return "", false
	}
	number, ok := wrapper.Payload[key].(json.Number)
	if !ok {
		return "", false
	}
	return number.String(), true
}

// validRepairAuditLiteral enforces the canonical unsigned decimal integer
// notation of sourceJournalSequence on the raw journal bytes and binds its
// exact value to the previous journal sequence.
func validRepairAuditLiteral(event domain.RunEvent, rawLine []byte) bool {
	literal, ok := rawNumberLiteral(rawLine, "sourceJournalSequence")
	if !ok || !canonicalRepairSequencePattern.MatchString(literal) {
		return false
	}
	value, err := strconv.ParseUint(literal, 10, 64)
	return err == nil && event.Sequence > 0 && value == event.Sequence-1
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
