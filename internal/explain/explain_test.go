package explain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/recovery"
)

func writeRunDir(t *testing.T, state domain.RunState, events []domain.RunEvent, taskSpecPublicationRequired bool) (stateRoot, runID string) {
	t.Helper()
	stateRoot = t.TempDir()
	runID = state.RunID
	runDir := filepath.Join(stateRoot, "runs", runID)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Inspect 的快照一致性：sequence==事件条数避免 replay，state==末事件
	// StateTo 与 journal 尾对齐。
	state.Sequence = uint64(len(events))
	if len(events) > 0 {
		state.State = events[len(events)-1].StateTo
		state.UpdatedAt = events[len(events)-1].Timestamp
	}
	stateBytes, err := json.Marshal(struct {
		APIVersion domain.APIVersion `json:"apiVersion"`
		Kind       domain.Kind       `json:"kind"`
		domain.RunState
	}{state.APIVersion, state.Kind, state})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "state.json"), append(stateBytes, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	var eventsBytes strings.Builder
	for _, e := range events {
		data, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		eventsBytes.Write(append(data, '\n'))
	}
	if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), []byte(eventsBytes.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	taskSpec := map[string]any{"publication": map[string]any{"required": taskSpecPublicationRequired}}
	specBytes, _ := json.Marshal(taskSpec)
	if err := os.WriteFile(filepath.Join(runDir, "task-spec.json"), specBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return stateRoot, runID
}

func baseEvent(runID, attemptID, typ string, seq uint64, from, to domain.State, ts time.Time) domain.RunEvent {
	return domain.RunEvent{
		APIVersion: "marshal.dev/v1alpha1", Kind: domain.KindRunEvent,
		EventID: "event:test-" + typ, RunID: runID, AttemptID: attemptID,
		Sequence: seq, Type: typ, StateFrom: from, StateTo: to,
		Timestamp: ts.UTC(), Payload: map[string]any{},
	}
}

func TestAssembleTerminalRunResumesConsumption(t *testing.T) {
	runID := "run-explain-terminal"
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	st, runID := writeRunDir(t,
		domain.RunState{APIVersion: "marshal.dev/v1alpha1", Kind: domain.KindRunState, TaskID: "T1", RunID: runID, State: domain.StateReviewPending, Sequence: 5, AttemptsUsed: 1, ReviewRound: 0, CreatedAt: now, UpdatedAt: now},
		[]domain.RunEvent{
			baseEvent(runID, "", "planning.spec-accepted", 1, domain.StateCreated, domain.StatePlanned, now.Add(-90*time.Minute)),
			baseEvent(runID, "", "planning.inputs-frozen", 2, domain.StatePlanned, domain.StateReady, now.Add(-85*time.Minute)),
			baseEvent(runID, "attempt-1", "worker.started", 3, domain.StateReady, domain.StateRunning, now.Add(-time.Hour)),
			baseEvent(runID, "attempt-1", "worker.completed", 4, domain.StateRunning, domain.StateVerifying, now.Add(-30*time.Minute)),
			baseEvent(runID, "attempt-1", "verification.completed", 5, domain.StateVerifying, domain.StateReviewPending, now.Add(-25*time.Minute)),
		}, false)

	x, err := Assemble(st, runID, now)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if x.Decision.Action != recovery.ActionResume || x.Decision.Rationale != recovery.RationaleAttemptAlreadyFinal {
		t.Errorf("terminal run must resume consumption, got %+v", x.Decision)
	}
	if !strings.Contains(x.Rendered, "authoritative timeline") || !strings.Contains(x.Rendered, "decision: action=resume") {
		t.Errorf("render malformed:\n%s", x.Rendered)
	}
}

func TestAssembleRetryPendingConsumesTerminalFailure(t *testing.T) {
	runID := "run-explain-retry"
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	st, runID := writeRunDir(t,
		domain.RunState{APIVersion: "marshal.dev/v1alpha1", Kind: domain.KindRunState, TaskID: "T1", RunID: runID, State: domain.StateRetryPending, AttemptsUsed: 1, OperationalRetriesUsed: 1, CreatedAt: now, UpdatedAt: now},
		[]domain.RunEvent{
			baseEvent(runID, "", "planning.spec-accepted", 1, domain.StateCreated, domain.StatePlanned, now.Add(-90*time.Minute)),
			baseEvent(runID, "attempt-1", "worker.started", 2, domain.StateReady, domain.StateRunning, now.Add(-time.Hour)),
			baseEvent(runID, "attempt-1", "worker.failed", 3, domain.StateRunning, domain.StateRetryPending, now.Add(-40*time.Minute)),
		}, false)

	x, err := Assemble(st, runID, now)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	// RETRY_PENDING 是入账终态：恢复结论是消费既有 Outcome（resume），
	// 不重启 Attempt；retry 属 policy 通道而非恢复。
	if x.Decision.Action != recovery.ActionResume || x.Decision.Rationale != recovery.RationaleAttemptAlreadyFinal {
		t.Errorf("retry-pending must resume as attempt-already-terminal, got %+v", x.Decision)
	}
}

func TestAssembleStaleRunningFencesAndReconciles(t *testing.T) {
	runID := "run-explain-stale"
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	st, runID := writeRunDir(t,
		domain.RunState{APIVersion: "marshal.dev/v1alpha1", Kind: domain.KindRunState, TaskID: "T1", RunID: runID, State: domain.StateRunning, AttemptsUsed: 1, CreatedAt: now, UpdatedAt: now},
		[]domain.RunEvent{
			baseEvent(runID, "", "planning.spec-accepted", 1, domain.StateCreated, domain.StatePlanned, now.Add(-3*time.Hour)),
			baseEvent(runID, "attempt-1", "worker.started", 2, domain.StateReady, domain.StateRunning, now.Add(-2*time.Hour)),
		}, true)

	x, err := Assemble(st, runID, now)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !x.Decision.RequiresFence || !x.Decision.RequiresReconcile {
		t.Errorf("stale RUNNING with side effect must fence+reconcile, got %+v", x.Decision)
	}
	if x.Decision.Action != recovery.ActionNewAttempt {
		t.Errorf("must not resume unprovable, got %+v", x.Decision)
	}
	if x.Decision.Rationale != recovery.RationaleAmbiguousSideEffect && x.Decision.Rationale != recovery.RationaleUnsafeToProve && x.Decision.Rationale != recovery.RationaleLeaseDead {
		t.Errorf("expected ambiguous/unsafe/lease-dead rationale, got %q", x.Decision.Rationale)
	}
	if !strings.Contains(x.Rendered, "幂等键对账") {
		t.Errorf("render must include reconcile action:\n%s", x.Rendered)
	}
}

// ── R6-TOP5: deriveBindings fault injection（anchor 缺失/损坏/被替换） ──────

func TestAssembleAnchorMissingDefaultsToLegacyOK(t *testing.T) {
	runID := "run-explain-anchor-missing"
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	st, runID := writeRunDir(t,
		domain.RunState{APIVersion: "marshal.dev/v1alpha1", Kind: domain.KindRunState, TaskID: "T1", RunID: runID, State: domain.StateRunning, AttemptsUsed: 1, CreatedAt: now, UpdatedAt: now},
		[]domain.RunEvent{
			baseEvent(runID, "", "planning.spec-accepted", 1, domain.StateCreated, domain.StatePlanned, now.Add(-3*time.Hour)),
			baseEvent(runID, "attempt-1", "worker.started", 2, domain.StateReady, domain.StateRunning, now.Add(-2*time.Hour)),
		}, false)
	// 无 attempts/attempt-1/ 目录 → anchor 缺失 → legacy fallback 双 OK。
	x, err := Assemble(st, runID, now)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !x.Input.Bindings.AgentOK || !x.Input.Bindings.SandboxOK {
		t.Errorf("missing anchor must default to legacy OK, got bindings=%+v", x.Input.Bindings)
	}
}

func TestAssembleAnchorCorruptedReportsBindingLost(t *testing.T) {
	runID := "run-explain-anchor-corrupt"
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	st, runID := writeRunDir(t,
		domain.RunState{APIVersion: "marshal.dev/v1alpha1", Kind: domain.KindRunState, TaskID: "T1", RunID: runID, State: domain.StateRunning, AttemptsUsed: 1, CreatedAt: now, UpdatedAt: now},
		[]domain.RunEvent{
			baseEvent(runID, "", "planning.spec-accepted", 1, domain.StateCreated, domain.StatePlanned, now.Add(-3*time.Hour)),
			baseEvent(runID, "attempt-1", "worker.started", 2, domain.StateReady, domain.StateRunning, now.Add(-2*time.Hour)),
		}, false)
	// 写入损坏的 anchor 文件 → binding-lost。
	anchorDir := filepath.Join(st, "runs", runID, "attempts", "attempt-1")
	if err := os.MkdirAll(anchorDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(anchorDir, "sandbox-binding-admission.json"), []byte("{not-valid-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	x, err := Assemble(st, runID, now)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if x.Input.Bindings.AgentOK || x.Input.Bindings.SandboxOK {
		t.Errorf("corrupted anchor must report binding-lost, got bindings=%+v", x.Input.Bindings)
	}
	if !x.Decision.RequiresFence {
		t.Errorf("binding-lost must require fence, got %+v", x.Decision)
	}
	if x.Decision.Rationale != recovery.RationaleBindingLost {
		t.Errorf("expected RationaleBindingLost, got %q", x.Decision.Rationale)
	}
}

func TestAssembleAnchorRejectedReportsBindingLost(t *testing.T) {
	runID := "run-explain-anchor-rejected"
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	st, runID := writeRunDir(t,
		domain.RunState{APIVersion: "marshal.dev/v1alpha1", Kind: domain.KindRunState, TaskID: "T1", RunID: runID, State: domain.StateRunning, AttemptsUsed: 1, CreatedAt: now, UpdatedAt: now},
		[]domain.RunEvent{
			baseEvent(runID, "", "planning.spec-accepted", 1, domain.StateCreated, domain.StatePlanned, now.Add(-3*time.Hour)),
			baseEvent(runID, "attempt-1", "worker.started", 2, domain.StateReady, domain.StateRunning, now.Add(-2*time.Hour)),
		}, false)
	// 写入 accepted=false 的 anchor → binding 损伤。
	anchorDir := filepath.Join(st, "runs", runID, "attempts", "attempt-1")
	if err := os.MkdirAll(anchorDir, 0o700); err != nil {
		t.Fatal(err)
	}
	anchor := map[string]any{
		"accepted":        false,
		"attemptId":       "attempt-1",
		"agentSideOk":     false,
		"sandboxSideOk":   true,
		"admissionReason": "agent registration revoked",
	}
	data, _ := json.Marshal(anchor)
	if err := os.WriteFile(filepath.Join(anchorDir, "sandbox-binding-admission.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	x, err := Assemble(st, runID, now)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if x.Input.Bindings.AgentOK {
		t.Errorf("rejected anchor must report agent binding lost, got bindings=%+v", x.Input.Bindings)
	}
	if !x.Decision.RequiresFence {
		t.Errorf("binding-lost must require fence, got %+v", x.Decision)
	}
}
