package control

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

func TestMediatedSteeringRecordsRounds(t *testing.T) {
	sealedMigrationSkip(t)
	t.Parallel()
	fixture := newApprovalFixture(t, nil, false)
	advanceFixtureToRunning(t, fixture)
	first, err := RecordIntervention(fixture.interventionInput(domain.InterventionCategoryClarification, "解释现有验收命令的工作目录。"))
	if err != nil {
		t.Fatal(err)
	}
	secondInput := fixture.interventionInput(domain.InterventionCategoryImplementationCorrection, "保持冻结范围，修正并发测试实现。")
	secondInput.SourceType = domain.ControlSourceTypeLeadAgent
	second, err := RecordIntervention(secondInput)
	if err != nil {
		t.Fatal(err)
	}
	if first.SteeringRound != 1 || second.SteeringRound != 2 || first.ControlSequence != 1 || second.ControlSequence != 2 ||
		first.Effect != domain.InterventionEffectContinue || first.InstructionDigest != canonical.DigestBytes([]byte(first.Instruction)) {
		t.Fatalf("steering records = %+v / %+v", first, second)
	}
}

func TestSteeringPolicyAndBudgetFailClosed(t *testing.T) {
	sealedMigrationSkip(t)
	t.Parallel()
	t.Run("disabled", func(t *testing.T) {
		t.Parallel()
		fixture := newApprovalFixture(t, func(policy map[string]any) {
			control := policy["control"].(map[string]any)
			control["allowMediatedSteering"] = false
			control["maxSteeringRounds"] = float64(0)
		}, false)
		advanceFixtureToRunning(t, fixture)
		if _, err := RecordIntervention(fixture.interventionInput(domain.InterventionCategoryClarification, "说明")); !errors.Is(err, ErrSteeringDenied) {
			t.Fatalf("RecordIntervention() = %v", err)
		}
	})
	t.Run("budget", func(t *testing.T) {
		t.Parallel()
		fixture := newApprovalFixture(t, func(policy map[string]any) {
			policy["control"].(map[string]any)["maxSteeringRounds"] = float64(1)
		}, false)
		advanceFixtureToRunning(t, fixture)
		if _, err := RecordIntervention(fixture.interventionInput(domain.InterventionCategoryClarification, "第一轮")); err != nil {
			t.Fatal(err)
		}
		if _, err := RecordIntervention(fixture.interventionInput(domain.InterventionCategoryImplementationCorrection, "第二轮")); !errors.Is(err, ErrSteeringBudget) {
			t.Fatalf("RecordIntervention() = %v", err)
		}
	})
}

func TestScopeChangeRequiresNewRunAndPreservesFrozenInputs(t *testing.T) {
	t.Parallel()
	fixture := newApprovalFixture(t, nil, false)
	paths := []string{"task-spec.json", "policy-snapshot.json", "capability-snapshot.json", "state.json", "events.jsonl"}
	before := map[string][]byte{}
	for _, name := range paths {
		data, err := os.ReadFile(filepath.Join(fixture.runDir, name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = data
	}
	input := fixture.interventionInput(domain.InterventionCategoryScopeChange, "新增 deliverable，必须创建新的 Run。")
	input.AttemptID = ""
	record, err := RecordIntervention(input)
	if err != nil {
		t.Fatal(err)
	}
	if record.Effect != domain.InterventionEffectNewRunRequired || record.AttemptID != "" {
		t.Fatalf("scope-change record = %+v", record)
	}
	for _, name := range paths {
		after, err := os.ReadFile(filepath.Join(fixture.runDir, name))
		if err != nil || string(after) != string(before[name]) {
			t.Fatalf("scope-change modified %s: %v", name, err)
		}
	}
}

func TestManualPTYPolicy(t *testing.T) {
	sealedMigrationSkip(t)
	t.Parallel()
	t.Run("record and reverify", func(t *testing.T) {
		t.Parallel()
		fixture := newApprovalFixture(t, nil, false)
		advanceFixtureToRunning(t, fixture)
		input := fixture.interventionInput(domain.InterventionCategoryManualPTY, "")
		input.SourceType = domain.ControlSourceTypeTerminalHook
		record, err := RecordIntervention(input)
		if err != nil {
			t.Fatal(err)
		}
		if record.Effect != domain.InterventionEffectRequiredReverification || record.Instruction != "" || record.SteeringRound != 0 {
			t.Fatalf("manual PTY record = %+v", record)
		}
	})
	t.Run("denied", func(t *testing.T) {
		t.Parallel()
		fixture := newApprovalFixture(t, func(policy map[string]any) {
			policy["control"].(map[string]any)["directPtyPolicy"] = "deny"
		}, false)
		advanceFixtureToRunning(t, fixture)
		input := fixture.interventionInput(domain.InterventionCategoryManualPTY, "")
		input.SourceType = domain.ControlSourceTypeHuman
		if _, err := RecordIntervention(input); !errors.Is(err, ErrDirectPTYDenied) {
			t.Fatalf("RecordIntervention() = %v", err)
		}
	})
}

func TestInterventionRejectsWrongAttemptAndInput(t *testing.T) {
	sealedMigrationSkip(t)
	t.Parallel()
	fixture := newApprovalFixture(t, nil, false)
	advanceFixtureToRunning(t, fixture)
	wrong := fixture.interventionInput(domain.InterventionCategoryClarification, "说明")
	wrong.AttemptID = "attempt-other"
	if _, err := RecordIntervention(wrong); !errors.Is(err, ErrInterventionUnavailable) {
		t.Fatalf("wrong attempt = %v", err)
	}
	missing := fixture.interventionInput(domain.InterventionCategoryClarification, "")
	if _, err := RecordIntervention(missing); !errors.Is(err, ErrInvalidControlInput) {
		t.Fatalf("missing instruction = %v", err)
	}
	hook := fixture.interventionInput(domain.InterventionCategoryImplementationCorrection, "纠偏")
	hook.SourceType = domain.ControlSourceTypeTerminalHook
	if _, err := RecordIntervention(hook); !errors.Is(err, ErrInvalidControlInput) {
		t.Fatalf("terminal hook steering = %v", err)
	}
	undelivered := fixture.interventionInput(domain.InterventionCategoryClarification, "尚未送达")
	undelivered.DeliveryAccepted = false
	if _, err := RecordIntervention(undelivered); !errors.Is(err, ErrInvalidControlInput) {
		t.Fatalf("undelivered steering = %v", err)
	}
}

func advanceFixtureToRunning(t *testing.T, fixture approvalFixture) {
	t.Helper()
	store := runstore.New(fixture.root)
	lease, err := store.Acquire(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	state, err := store.Inspect(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	event := domain.RunEvent{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent,
		EventID: fmt.Sprintf("event-running-%d", state.Sequence+1), RunID: fixture.runID, AttemptID: "attempt-01",
		Sequence: state.Sequence + 1, Type: "fixture.worker-started", StateFrom: state.State, StateTo: domain.StateRunning,
		Timestamp: time.Unix(50, 0).UTC(), Payload: map[string]any{},
	}
	if err := store.Append(lease, event, state.Sequence); err != nil {
		t.Fatal(err)
	}
}

func (f approvalFixture) interventionInput(category, instruction string) InterventionInput {
	delivered := category == domain.InterventionCategoryClarification || category == domain.InterventionCategoryImplementationCorrection
	return InterventionInput{
		StateRoot: f.root, RunID: f.runID, AttemptID: "attempt-01", Category: category,
		SourceType: domain.ControlSourceTypeHuman, SourceID: "operator-01", Instruction: instruction,
		DeliveryAccepted: delivered, Now: time.Unix(100, 0).UTC(), Validator: f.validator,
	}
}
