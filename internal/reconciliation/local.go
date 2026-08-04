package reconciliation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

const maxEvidenceBytes int64 = 32 << 20

var (
	errInvalidInput = errors.New("invalid reconciliation input")
)

type Input struct {
	StateRoot      string
	RepositoryRoot string
	RunID          string
	Validator      *contract.Validator
}

type Finding struct {
	Code       string `json:"code"`
	Severity   string `json:"severity"`
	Repairable bool   `json:"repairable"`
}

type Report struct {
	RunID            string           `json:"runId"`
	Status           string           `json:"status"`
	SnapshotSequence uint64           `json:"snapshotSequence"`
	JournalSequence  uint64           `json:"journalSequence"`
	State            *domain.RunState `json:"state,omitempty"`
	Findings         []Finding        `json:"findings"`
}

// Inspect reconciles durable local evidence without acquiring a lease or
// modifying the run directory. Invalid caller input is returned as an error;
// damaged or inconsistent run evidence is represented by fixed findings.
func Inspect(ctx context.Context, input Input) (Report, error) {
	if input.Validator == nil || domain.ValidateID(input.RunID) != nil || !cleanAbsolute(input.StateRoot) || !cleanAbsolute(input.RepositoryRoot) {
		return Report{}, errInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}

	report := Report{RunID: input.RunID, Findings: []Finding{}}
	seen := make(map[string]bool)
	add := func(code, severity string, repairable bool) {
		if !seen[code] {
			seen[code] = true
			report.Findings = append(report.Findings, Finding{Code: code, Severity: severity, Repairable: repairable})
		}
	}

	runDir := filepath.Join(input.StateRoot, "runs", input.RunID)
	snapshotData, snapshotStatus, err := readEvidence(ctx, filepath.Join(runDir, "state.json"))
	if err != nil {
		return Report{}, err
	}
	var snapshot domain.RunState
	snapshotValid := false
	switch snapshotStatus {
	case fileMissing:
		add("snapshot-missing", "error", false)
	case fileInvalid:
		add("snapshot-invalid", "error", false)
	default:
		if input.Validator.Validate(domain.KindRunState, snapshotData) != nil || json.Unmarshal(snapshotData, &snapshot) != nil {
			add("snapshot-invalid", "error", false)
		} else if snapshot.RunID != input.RunID {
			add("snapshot-identity-mismatch", "error", false)
		} else {
			snapshotValid = true
			report.SnapshotSequence = snapshot.Sequence
		}
	}

	_, journalStatus, err := readEvidence(ctx, filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		return Report{}, err
	}
	store := runstore.New(input.StateRoot)
	journalValid := false
	var events []domain.RunEvent
	switch journalStatus {
	case fileMissing:
		add("journal-missing", "error", false)
	case fileInvalid:
		add("journal-invalid", "error", false)
	default:
		var truncated bool
		events, truncated, err = store.ReadEvents(input.RunID)
		if err != nil && !errors.Is(err, runstore.ErrTruncatedTail) {
			add("journal-invalid", "error", false)
		} else {
			journalValid = true
			report.JournalSequence = uint64(len(events))
			if truncated {
				add("journal-truncated", "warning", true)
			}
			for _, event := range events {
				data, marshalErr := json.Marshal(event)
				if marshalErr != nil || input.Validator.Validate(domain.KindRunEvent, data) != nil || event.RunID != input.RunID {
					journalValid = false
					add("journal-invalid", "error", false)
					break
				}
			}
		}
	}

	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if snapshotValid && journalValid {
		state, inspectErr := store.Inspect(input.RunID)
		if inspectErr != nil {
			add("journal-snapshot-conflict", "error", false)
		} else {
			report.State = &state
			if state.Sequence > snapshot.Sequence {
				add("snapshot-stale", "warning", true)
			}
			if err := checkFrozen(ctx, input, runDir, state, add); err != nil {
				return Report{}, err
			}
		}
	}

	report.Status = "ok"
	for _, finding := range report.Findings {
		if finding.Severity == "error" {
			report.Status = "blocked"
			return report, nil
		}
		report.Status = "warning"
	}
	return report, nil
}

type fileStatus uint8

const (
	fileOK fileStatus = iota
	fileMissing
	fileInvalid
)

func readEvidence(ctx context.Context, path string) ([]byte, fileStatus, error) {
	if err := ctx.Err(); err != nil {
		return nil, fileInvalid, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fileMissing, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxEvidenceBytes {
		return nil, fileInvalid, nil
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fileMissing, nil
	}
	if err != nil {
		return nil, fileInvalid, nil
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxEvidenceBytes+1))
	if err != nil || int64(len(data)) > maxEvidenceBytes {
		return nil, fileInvalid, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, fileInvalid, err
	}
	return data, fileOK, nil
}

func checkFrozen(ctx context.Context, input Input, runDir string, state domain.RunState, add func(string, string, bool)) error {
	type frozenCheck struct {
		name, prefix, digest string
		kind                 domain.Kind
	}
	checks := []frozenCheck{
		{"task-spec.json", "task-spec", state.SpecDigest, domain.KindTask},
		{"policy-snapshot.json", "policy-snapshot", state.PolicyDigest, domain.KindPolicySnapshot},
		{"capability-snapshot.json", "capability-snapshot", state.CapabilityDigest, domain.KindCapabilitySnapshot},
	}
	for _, check := range checks {
		data, status, err := readEvidence(ctx, filepath.Join(runDir, check.name))
		if err != nil {
			return err
		}
		if status == fileMissing {
			add(check.prefix+"-missing", "error", false)
			continue
		}
		if status == fileInvalid || input.Validator.Validate(check.kind, data) != nil {
			add(check.prefix+"-invalid", "error", false)
			continue
		}
		digest, err := canonical.DigestJSON(data)
		if err != nil {
			add(check.prefix+"-invalid", "error", false)
			continue
		}
		if digest != check.digest {
			add(check.prefix+"-digest-mismatch", "error", false)
		}
		switch check.kind {
		case domain.KindTask:
			var task struct {
				Metadata struct {
					ID string `json:"id"`
				} `json:"metadata"`
				Repository struct {
					Path string `json:"path"`
				} `json:"repository"`
			}
			if json.Unmarshal(data, &task) != nil || task.Metadata.ID != state.TaskID || task.Repository.Path != input.RepositoryRoot || !cleanAbsolute(task.Repository.Path) {
				add("task-spec-identity-mismatch", "error", false)
			}
		case domain.KindPolicySnapshot:
			var policy struct {
				TaskID string `json:"taskId"`
				RunID  string `json:"runId"`
			}
			if json.Unmarshal(data, &policy) != nil || policy.TaskID != state.TaskID || policy.RunID != state.RunID {
				add("policy-snapshot-identity-mismatch", "error", false)
			}
		}
	}
	return nil
}

func cleanAbsolute(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}
