package adapter

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

// snapshotPayload builds a fully capable CapabilitySnapshot body. Each non-nil
// mutation overrides one field so every failure case changes exactly one
// aspect of an otherwise valid snapshot.
func snapshotPayload(t *testing.T, mutate func(map[string]any)) domain.Record {
	t.Helper()
	snapshot := map[string]any{
		"apiVersion":     "marshal.dev/v1alpha1",
		"kind":           "CapabilitySnapshot",
		"adapterId":      "claude-code",
		"adapterVersion": "0.1.0",
		"executable":     "/usr/local/bin/claude",
		"binaryVersion":  "1.0.0",
		"probeStatus":    "supported",
		"probedAt":       "2024-01-01T00:00:00Z",
		"notes":          []string{"provider free text must not leak"},
		"probeErrors":    []string{"provider error text must not leak"},
		"capabilities": map[string]any{
			"structuredOutput":        []string{"jsonl", "text"},
			"nonInteractiveEdit":      true,
			"processTreeCancellation": true,
			"sessionPolicies":         []string{"ephemeral", "persist"},
			"modelSelection":          false,
			"executionProfiles":       []string{"workspace-write"},
			"nativeBudgets":           []string{"wall-time"},
			"notes":                   []string{"capability free text must not leak"},
		},
	}
	if mutate != nil {
		mutate(snapshot)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot fixture: %v", err)
	}
	return domain.Record{Kind: domain.KindCapabilitySnapshot, Data: data}
}

func baseTask() domain.TaskSpec {
	return domain.TaskSpec{
		Worker: domain.TaskWorker{
			PreferredAdapter: "claude-code",
			ExecutionProfile: "workspace-write",
			SessionPolicy:    "ephemeral",
		},
	}
}

func TestValidateCapability(t *testing.T) {
	tests := []struct {
		name        string
		record      func(t *testing.T) domain.Record
		task        domain.TaskSpec
		wantAdapter string
		wantErr     string
	}{
		{
			name: "success without model",
			record: func(t *testing.T) domain.Record {
				return snapshotPayload(t, nil)
			},
			task:        baseTask(),
			wantAdapter: "claude-code",
		},
		{
			name: "success with model selection",
			record: func(t *testing.T) domain.Record {
				return snapshotPayload(t, func(snapshot map[string]any) {
					capabilities := snapshot["capabilities"].(map[string]any)
					capabilities["modelSelection"] = true
				})
			},
			task: func() domain.TaskSpec {
				task := baseTask()
				task.Worker.Model = "claude-sonnet-4"
				return task
			}(),
			wantAdapter: "claude-code",
		},
		{
			name: "wrong record kind",
			record: func(t *testing.T) domain.Record {
				record := snapshotPayload(t, nil)
				record.Kind = domain.KindTask
				return record
			},
			task:    baseTask(),
			wantErr: ErrCapabilityInvalidKind,
		},
		{
			name: "malformed json",
			record: func(t *testing.T) domain.Record {
				return domain.Record{Kind: domain.KindCapabilitySnapshot, Data: []byte("{not json")}
			},
			task:    baseTask(),
			wantErr: ErrCapabilityMalformed,
		},
		{
			name: "empty adapter id",
			record: func(t *testing.T) domain.Record {
				return snapshotPayload(t, func(snapshot map[string]any) {
					snapshot["adapterId"] = "   "
				})
			},
			task:    baseTask(),
			wantErr: ErrCapabilityMissingAdapterID,
		},
		{
			name: "unsupported probe status",
			record: func(t *testing.T) domain.Record {
				return snapshotPayload(t, func(snapshot map[string]any) {
					snapshot["probeStatus"] = "unsupported"
				})
			},
			task:    baseTask(),
			wantErr: ErrCapabilityUnsupported,
		},
		{
			name: "structured output without jsonl",
			record: func(t *testing.T) domain.Record {
				return snapshotPayload(t, func(snapshot map[string]any) {
					capabilities := snapshot["capabilities"].(map[string]any)
					capabilities["structuredOutput"] = []string{"text"}
				})
			},
			task:    baseTask(),
			wantErr: ErrCapabilityStructuredOutput,
		},
		{
			name: "non interactive edit disabled",
			record: func(t *testing.T) domain.Record {
				return snapshotPayload(t, func(snapshot map[string]any) {
					capabilities := snapshot["capabilities"].(map[string]any)
					capabilities["nonInteractiveEdit"] = false
				})
			},
			task:    baseTask(),
			wantErr: ErrCapabilityNonInteractiveEdit,
		},
		{
			name: "process tree cancellation disabled",
			record: func(t *testing.T) domain.Record {
				return snapshotPayload(t, func(snapshot map[string]any) {
					capabilities := snapshot["capabilities"].(map[string]any)
					capabilities["processTreeCancellation"] = false
				})
			},
			task:    baseTask(),
			wantErr: ErrCapabilityProcessTree,
		},
		{
			name: "missing session policy",
			record: func(t *testing.T) domain.Record {
				return snapshotPayload(t, func(snapshot map[string]any) {
					capabilities := snapshot["capabilities"].(map[string]any)
					capabilities["sessionPolicies"] = []string{"persist"}
				})
			},
			task:    baseTask(),
			wantErr: ErrCapabilitySessionPolicy,
		},
		{
			name: "missing execution profile",
			record: func(t *testing.T) domain.Record {
				return snapshotPayload(t, func(snapshot map[string]any) {
					capabilities := snapshot["capabilities"].(map[string]any)
					capabilities["executionProfiles"] = []string{"read-only"}
				})
			},
			task:    baseTask(),
			wantErr: ErrCapabilityExecutionProfile,
		},
		{
			name: "model requested without model selection",
			record: func(t *testing.T) domain.Record {
				return snapshotPayload(t, nil)
			},
			task: func() domain.TaskSpec {
				task := baseTask()
				task.Worker.Model = "claude-sonnet-4"
				return task
			}(),
			wantErr: ErrCapabilityModelSelection,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapterID, err := ValidateCapability(test.record(t), test.task)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateCapability() error = %v, want nil", err)
				}
				if adapterID != test.wantAdapter {
					t.Fatalf("ValidateCapability() adapterID = %q, want %q", adapterID, test.wantAdapter)
				}
				return
			}
			if adapterID != "" {
				t.Fatalf("ValidateCapability() adapterID = %q, want empty on error", adapterID)
			}
			if err == nil {
				t.Fatalf("ValidateCapability() error = nil, want %q", test.wantErr)
			}
			if err.Error() != test.wantErr {
				t.Fatalf("ValidateCapability() error = %q, want %q", err.Error(), test.wantErr)
			}
			if !port.IsPermanent(err) {
				t.Fatalf("ValidateCapability() error is not permanent")
			}
			for _, leaked := range []string{"provider free text", "provider error text", "capability free text"} {
				if strings.Contains(err.Error(), leaked) {
					t.Fatalf("ValidateCapability() error leaks provider free text %q", leaked)
				}
			}
		})
	}
}
