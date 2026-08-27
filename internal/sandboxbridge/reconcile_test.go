package sandboxbridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

func mustRecord(t *testing.T, runID, allocationID string) AllocationRecord {
	t.Helper()
	return AllocationRecord{
		Schema:              allocationRecordSchema,
		TaskID:              "T1",
		RunID:               runID,
		AttemptID:           "A1",
		AllocationID:        allocationID,
		Generation:          1,
		FencingToken:        "sha256:" + strings.Repeat("f", 64),
		RequirementsProfile: "workspace-write",
		RecordedAt:          time.Unix(1, 0).UTC().Format(time.RFC3339),
		OwnerState:          "running",
	}
}

func TestAllocationRecordRoundTrip(t *testing.T) {
	dir := t.TempDir()
	attemptDir := filepath.Join(dir, "attempts", "a1")
	if err := os.MkdirAll(attemptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := mustRecord(t, "R1", "alloc-1")
	if err := recordAllocation(filepath.Join(attemptDir, "control"), rec); err != nil {
		t.Fatalf("recordAllocation: %v", err)
	}
	loaded, ok, err := LoadAllocationRecord(attemptDir)
	if err != nil || !ok {
		t.Fatalf("LoadAllocationRecord: ok=%v err=%v", ok, err)
	}
	if loaded.AllocationID != "alloc-1" || loaded.RunID != "R1" || loaded.FencingToken != rec.FencingToken {
		t.Errorf("round trip mismatch: %+v", loaded)
	}
}

func TestLoadAllocationRecordFailClosed(t *testing.T) {
	if _, ok, err := LoadAllocationRecord(t.TempDir()); ok || err != nil {
		t.Errorf("missing record must be (false, nil), got ok=%v err=%v", ok, err)
	}
	dir := t.TempDir()
	attemptDir := filepath.Join(dir, "attempts", "a1")
	os.MkdirAll(attemptDir, 0o700)
	if err := os.WriteFile(filepath.Join(attemptDir, allocationRecordName), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadAllocationRecord(attemptDir); err == nil {
		t.Errorf("broken record must fail closed")
	}
}

func TestSweepOrphans(t *testing.T) {
	provider := sandbox.NewFakeProvider(sandbox.FakeConfig{})
	dir := t.TempDir()

	mustProvision := func(runID, allocationID string) {
		t.Helper()
		requirements, err := domain.NewSandboxRequirements(domain.AccessModeWorkspaceWrite, domain.AssuranceLevelWorkspaceWrite)
		if err != nil {
			t.Fatalf("requirements: %v", err)
		}
		id := sandbox.OperationIdentity{
			TaskId:       "T1",
			RunId:        runID,
			AttemptId:    "A1",
			WorkloadRole: sandbox.WorkloadRoleWorker,
			AllocationId: allocationID,
			Generation:   1,
			FencingToken: "sha256:" + strings.Repeat("f", 64),
			CommandId:    "command-provision",
		}
		req := sandbox.ProvisionRequest{Identity: id, Requirements: requirements, AllowedStoreIds: []string{}}
		if _, err := provider.Provision(context.Background(), req); err != nil {
			t.Fatalf("provision %q: %v", allocationID, err)
		}
	}
	mustProvision("run-dead", "alloc-dead")
	mustProvision("run-alive", "alloc-alive")

	// 两个 record：owner-terminal 与 owner-alive
	deadDir := filepath.Join(dir, "attempts", "dead")
	aliveDir := filepath.Join(dir, "attempts", "alive")
	os.MkdirAll(deadDir, 0o700)
	os.MkdirAll(aliveDir, 0o700)
	deadRec := mustRecord(t, "run-dead", "alloc-dead")
	aliveRec := mustRecord(t, "run-alive", "alloc-alive")
	if err := recordAllocation(filepath.Join(deadDir, "control"), deadRec); err != nil {
		t.Fatal(err)
	}
	if err := recordAllocation(filepath.Join(aliveDir, "control"), aliveRec); err != nil {
		t.Fatal(err)
	}

	resolver := NewMapResolver(map[string]bool{"run-dead": true, "run-alive": false})
	result := SweepOrphans(context.Background(), provider, resolver, []string{deadDir, aliveDir}, time.Minute, time.Unix(1000, 0))
	if result.Terminated != 1 || result.KeptAlive != 1 || len(result.Errors) != 0 {
		t.Errorf("sweep result = %+v, want {terminated:1 keptAlive:1 errors:0}", result)
	}

	// owner 未知的 record 必须按存活处理（fail closed）。
	unknownDir := filepath.Join(dir, "attempts", "unknown")
	os.MkdirAll(unknownDir, 0o700)
	if err := recordAllocation(filepath.Join(unknownDir, "control"), mustRecord(t, "run-unknown", "alloc-unknown")); err != nil {
		t.Fatal(err)
	}
	result2 := SweepOrphans(context.Background(), provider, resolver, []string{unknownDir}, time.Minute, time.Unix(1001, 0))
	if result2.Terminated != 0 || result2.KeptAlive != 1 {
		t.Errorf("unknown owner must be kept alive, got %+v", result2)
	}
}

func TestSweepOrphansNilDeps(t *testing.T) {
	result := SweepOrphans(context.Background(), nil, nil, []string{t.TempDir()}, time.Minute, time.Now())
	if len(result.Errors) == 0 {
		t.Errorf("nil deps must fail closed with error")
	}
}

// 端到端：桥在执行前把 allocation 身份落盘进 attempt 目录。
func TestRunWorkerRecordsAllocation(t *testing.T) {
	controlRoot := filepath.Join(t.TempDir(), "attempts", "a1", "control")
	if err := os.MkdirAll(controlRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	provider := sandbox.NewFakeProvider(sandbox.FakeConfig{})
	bridge, err := NewBridge(provider)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{id: "fake"}
	request := validRequest(t)
	var withRoot map[string]any
	if err := jsonUnmarshalForTest(request.Data, &withRoot); err != nil {
		t.Fatal(err)
	}
	withRoot["controlRoot"] = controlRoot
	raw, err := jsonMarshalForTest(withRoot)
	if err != nil {
		t.Fatal(err)
	}
	request.Data = raw

	if _, err := bridge.RunWorker(context.Background(), adapter, request); err != nil {
		t.Fatalf("RunWorker: %v", err)
	}
	loaded, ok, err := LoadAllocationRecord(filepath.Dir(controlRoot))
	if err != nil || !ok {
		t.Fatalf("allocation record must exist after bridged run: ok=%v err=%v", ok, err)
	}
	if loaded.RunID != "R1" || loaded.AttemptID != "A1" || loaded.AllocationID == "" || loaded.FencingToken == "" {
		t.Errorf("record content: %+v", loaded)
	}
	if loaded.RecordedAt == "" {
		t.Errorf("record must carry timestamp")
	}
}
