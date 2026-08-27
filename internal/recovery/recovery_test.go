package recovery

import (
	"errors"
	"strings"
	"testing"
)

func baseInput() RecoveryInput {
	return RecoveryInput{
		Ledger: LedgerView{
			AttemptID:        "attempt-1",
			PendingCommandID: "cmd-1",
			CommandDigest:    "sha256:" + strings.Repeat("a", 64),
			Lease:            LeaseActive,
			Generation:       3,
		},
		Observation: ObservationExecuting,
		Bindings:    BindingView{AgentOK: true, SandboxOK: true},
	}
}

func decide(t *testing.T, in RecoveryInput) (Decision, Explanation) {
	t.Helper()
	d, ex, err := Decide(in)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if d.Action != ActionResume && d.Action != ActionNewAttempt {
		t.Fatalf("decision action outside closed set: %q", d.Action)
	}
	return d, ex
}

// ── 故障矩阵：八类故障各得唯一幂等结论 ─────────────────────────────────────

// 1. duplicate delivery：幂等消费 resume，绝不第二效果。
func TestMatrix_DuplicateDelivery(t *testing.T) {
	in := baseInput()
	in.DuplicateOfAdmitted = true
	d, ex := decide(t, in)

	if d.Action != ActionResume || d.Rationale != RationaleDuplicateDelivery {
		t.Errorf("duplicate delivery must resume idempotently, got %+v", d)
	}
	if d.RequiresFence || d.RequiresReconcile {
		t.Errorf("duplicate delivery must not fence or reconcile, got %+v", d)
	}
	assertConflict(t, ex, "duplicate-delivery-idempotent")
}

// 2. lost response：Inspect 重建结果 resume，不重放执行。
func TestMatrix_LostResponse(t *testing.T) {
	in := baseInput()
	in.Observation = ObservationTerminalSuccess
	d, _ := decide(t, in)

	if d.Action != ActionResume || d.Rationale != RationaleLostResponseRebuild {
		t.Errorf("lost response must resume from inspect rebuild, got %+v", d)
	}
	if d.RequiresFence {
		t.Errorf("confirmed terminal success must not fence")
	}
}

// 3. process death：Provider 无法判定 → 不能证明安全 → fence + new Attempt。
func TestMatrix_ProcessDeath(t *testing.T) {
	in := baseInput()
	in.Observation = ObservationUnknown
	d, ex := decide(t, in)

	if d.Action != ActionNewAttempt || !d.RequiresFence {
		t.Errorf("process death (unknown) must fence + new attempt, got %+v", d)
	}
	if d.Rationale != RationaleUnsafeToProve {
		t.Errorf("expected unsafe-to-prove, got %q", d.Rationale)
	}
	if !strings.Contains(ex.NextAction, "fence") || !strings.Contains(ex.NextAction, "新 Attempt") {
		t.Errorf("next action must fence then new attempt, got %q", ex.NextAction)
	}
}

// 4. provider restart（重验后仍在执行，lease/binding 全绿）→ resume。
func TestMatrix_ProviderRestartReverified(t *testing.T) {
	in := baseInput()
	in.Observation = ObservationExecuting
	in.Ledger.Generation = 5
	d, _ := decide(t, in)

	if d.Action != ActionResume || d.Rationale != RationaleExecutingResume {
		t.Errorf("reverified executing must resume, got %+v", d)
	}
}

// 5. network partition：provider 不可达 → fence + new Attempt；声明副作用时
// 强制 reconcile（ambiguous side effect 的唯一防线）。
func TestMatrix_NetworkPartition(t *testing.T) {
	in := baseInput()
	in.Observation = ObservationUnreachable
	in.Ledger.SideEffectDeclared = true
	d, ex := decide(t, in)

	if d.Action != ActionNewAttempt || !d.RequiresFence {
		t.Errorf("network partition must fence + new attempt, got %+v", d)
	}
	if !d.RequiresReconcile || d.Rationale != RationaleAmbiguousSideEffect {
		t.Errorf("ambiguous side effect must force reconcile, got %+v", d)
	}
	assertConflict(t, ex, "provider-unreachable")
	if !strings.Contains(ex.NextAction, "幂等键对账") {
		t.Errorf("next action must include idempotency reconcile, got %q", ex.NextAction)
	}
}

// 5b. network partition 无副作用声明：fence + new Attempt，无需 reconcile。
func TestMatrix_NetworkPartitionNoSideEffect(t *testing.T) {
	in := baseInput()
	in.Observation = ObservationUnreachable
	d, _ := decide(t, in)

	if d.Action != ActionNewAttempt || !d.RequiresFence || d.RequiresReconcile {
		t.Errorf("no side effect ⇒ fence + new attempt without reconcile, got %+v", d)
	}
	if d.Rationale != RationaleUnsafeToProve {
		t.Errorf("expected unsafe-to-prove, got %q", d.Rationale)
	}
}

// 6. stale result：被隔离进冲突清单，永远不能驱动 new Attempt。
func TestMatrix_StaleResult(t *testing.T) {
	in := baseInput()
	in.StaleResultPresented = true
	in.DuplicateOfAdmitted = true // 晚到重复 + 当前 attempt 仍在执行
	d, ex := decide(t, in)

	if d.Action != ActionResume {
		t.Errorf("stale result must never drive new attempt, got %+v", d)
	}
	assertConflict(t, ex, "stale-result-quarantined")
}

// 7. partial artifact：产物不可证明完整 → fence + new Attempt。
func TestMatrix_PartialArtifact(t *testing.T) {
	in := baseInput()
	in.PartialArtifact = true
	in.Observation = ObservationUnknown
	d, _ := decide(t, in)

	if d.Action != ActionNewAttempt || !d.RequiresFence || d.Rationale != RationalePartialArtifact {
		t.Errorf("partial artifact must fence + new attempt, got %+v", d)
	}
}

// 8. ambiguous side effect：lease 失效 + 观察 unknown + 声明副作用 →
// reconcile 后 fence + new Attempt；rationale 升格为 ambiguous-side-effect。
func TestMatrix_AmbiguousSideEffectLeaseDead(t *testing.T) {
	in := baseInput()
	in.Ledger.Lease = LeaseExpired
	in.Ledger.SideEffectDeclared = true
	in.Observation = ObservationUnknown
	d, _ := decide(t, in)

	if d.Action != ActionNewAttempt || !d.RequiresFence || !d.RequiresReconcile {
		t.Errorf("ambiguous side effect must reconcile + fence + new attempt, got %+v", d)
	}
	if d.Rationale != RationaleAmbiguousSideEffect {
		t.Errorf("expected ambiguous-side-effect rationale, got %q", d.Rationale)
	}
}

// ── 决策表分支细则 ─────────────────────────────────────────────────────────

// 账本终态：按既有 Outcome 继续，不得重启。
func TestDecide_AttemptAlreadyTerminal(t *testing.T) {
	in := baseInput()
	in.Ledger.AttemptTerminal = true
	in.Ledger.PendingCommandID = ""
	in.Ledger.CommandDigest = ""
	d, ex := decide(t, in)

	if d.Action != ActionResume || d.Rationale != RationaleAttemptAlreadyFinal {
		t.Errorf("terminal attempt must resume consumption only, got %+v", d)
	}
	if d.RequiresFence || d.RequiresReconcile {
		t.Errorf("terminal attempt must not fence or reconcile")
	}
	if !strings.Contains(ex.NextAction, "不得重启") {
		t.Errorf("next action must forbid restart, got %q", ex.NextAction)
	}
}

// binding 失效：fence + new Attempt（即便 Inspect 说仍在执行——旧组合不可继续）。
func TestDecide_BindingLost(t *testing.T) {
	in := baseInput()
	in.Bindings.AgentOK = false
	d, _ := decide(t, in)

	if d.Action != ActionNewAttempt || !d.RequiresFence || d.Rationale != RationaleBindingLost {
		t.Errorf("binding lost must fence + new attempt, got %+v", d)
	}
}

// lease 失效但 Inspect 确认 terminal-failure：免 fence（在途可证静止）。
func TestDecide_LeaseDeadQuiescedByTerminal(t *testing.T) {
	in := baseInput()
	in.Ledger.Lease = LeaseRevoked
	in.Observation = ObservationTerminalFailure
	d, _ := decide(t, in)

	if d.Action != ActionNewAttempt || d.RequiresFence {
		t.Errorf("lease dead + confirmed terminal must skip fence, got %+v", d)
	}
	if d.Rationale != RationaleLeaseDead {
		t.Errorf("expected lease-dead, got %q", d.Rationale)
	}
}

// never-received：新 Attempt 免 fence（执行侧可证未启动），即便声明副作用
// 也无需 reconcile（从未执行 ⇒ 无副作用可能）。
func TestDecide_NeverReceived(t *testing.T) {
	in := baseInput()
	in.Observation = ObservationNeverReceived
	in.Ledger.SideEffectDeclared = true
	d, _ := decide(t, in)

	if d.Action != ActionNewAttempt || d.RequiresFence || d.RequiresReconcile {
		t.Errorf("never-received must skip fence and reconcile, got %+v", d)
	}
	if d.Rationale != RationaleNeverReceived {
		t.Errorf("expected never-received, got %q", d.Rationale)
	}
}

// terminal-failure + authority-observed infra 分类：new Attempt + 预算豁免。
func TestDecide_TerminalFailureInfraExempt(t *testing.T) {
	in := baseInput()
	in.Observation = ObservationTerminalFailure
	in.Failure = FailureClassView{MayRelaxBudget: true, MayExemptSemanticRework: true}
	d, ex := decide(t, in)

	if d.Action != ActionNewAttempt || d.RequiresFence || !d.BudgetExempt {
		t.Errorf("authority infra failure must yield budget-exempt new attempt, got %+v", d)
	}
	if ex.BudgetNote == "" {
		t.Errorf("budget exempt must be explained")
	}
}

// terminal-failure + 普通 semantic 分类：resume 消费失败 Outcome。
func TestDecide_TerminalFailureSemanticResume(t *testing.T) {
	in := baseInput()
	in.Observation = ObservationTerminalFailure
	d, _ := decide(t, in)

	if d.Action != ActionResume || d.BudgetExempt {
		t.Errorf("semantic terminal failure must resume consumption without exemption, got %+v", d)
	}
}

// executing 但 lease 已失效：不得 resume（回退到 lease-dead 分支）；
// executing 不证在途静止，须 fence。
func TestDecide_ExecutingWithDeadLease(t *testing.T) {
	in := baseInput()
	in.Ledger.Lease = LeaseReplaced
	d, _ := decide(t, in)

	if d.Action != ActionNewAttempt || d.Rationale != RationaleLeaseDead {
		t.Errorf("executing with dead lease must not resume, got %+v", d)
	}
	if !d.RequiresFence {
		t.Errorf("executing does not prove quiescence; fence required, got %+v", d)
	}
}

// ── 幂等性：同值输入永远同值输出 ───────────────────────────────────────────

func TestDecide_Idempotent(t *testing.T) {
	scenarios := map[string]func() RecoveryInput{
		"duplicate":     func() RecoveryInput { in := baseInput(); in.DuplicateOfAdmitted = true; return in },
		"lost-response": func() RecoveryInput { in := baseInput(); in.Observation = ObservationTerminalSuccess; return in },
		"process-death": func() RecoveryInput { in := baseInput(); in.Observation = ObservationUnknown; return in },
		"partition": func() RecoveryInput {
			in := baseInput()
			in.Observation = ObservationUnreachable
			in.Ledger.SideEffectDeclared = true
			return in
		},
		"partial-artifact": func() RecoveryInput { in := baseInput(); in.PartialArtifact = true; return in },
		"never-received":   func() RecoveryInput { in := baseInput(); in.Observation = ObservationNeverReceived; return in },
	}
	for name, mk := range scenarios {
		t.Run(name, func(t *testing.T) {
			in := mk()
			d1, ex1, err1 := Decide(in)
			d2, ex2, err2 := Decide(in)
			if err1 != nil || err2 != nil {
				t.Fatalf("Decide: %v / %v", err1, err2)
			}
			if d1 != d2 {
				t.Errorf("non-idempotent decision: %+v vs %+v", d1, d2)
			}
			if Render(ex1) != Render(ex2) {
				t.Errorf("non-idempotent render")
			}
		})
	}
}

// ── fail closed：结构性非法输入 ────────────────────────────────────────────

func TestDecide_MalformedInput(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*RecoveryInput)
	}{
		{"empty attempt", func(in *RecoveryInput) { in.Ledger.AttemptID = "" }},
		{"unknown lease", func(in *RecoveryInput) { in.Ledger.Lease = LeaseState("zombie") }},
		{"zero generation", func(in *RecoveryInput) { in.Ledger.Generation = 0 }},
		{"digest without command", func(in *RecoveryInput) { in.Ledger.PendingCommandID = "" }},
		{"unknown observation", func(in *RecoveryInput) { in.Observation = ObservationKind("resurrected") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInput()
			tc.mutate(&in)
			_, _, err := Decide(in)
			if !errors.Is(err, ErrMalformedInput) {
				t.Errorf("expected ErrMalformedInput, got %v", err)
			}
			if err != nil && !strings.HasPrefix(err.Error(), "recovery: ") {
				t.Errorf("error %q missing recovery: prefix", err.Error())
			}
		})
	}
}

// ── explain 渲染：非作者可复盘 ─────────────────────────────────────────────

func TestRender_ContainsAllReviewElements(t *testing.T) {
	in := baseInput()
	in.Observation = ObservationUnreachable
	in.Ledger.SideEffectDeclared = true
	in.StaleResultPresented = true
	_, ex := decide(t, in)

	text := Render(ex)
	for _, want := range []string{
		"authoritative timeline:",
		"current: lease=active generation=3",
		"external conflicts:",
		"stale-result-quarantined",
		"provider-unreachable",
		"decision: action=new-attempt requiresFence=true requiresReconcile=true",
		"rationale=ambiguous-side-effect",
		"next action:",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("render missing %q:\n%s", want, text)
		}
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func assertConflict(t *testing.T, ex Explanation, want string) {
	t.Helper()
	for _, c := range ex.Conflicts {
		if c == want {
			return
		}
	}
	t.Errorf("expected conflict %q in %v", want, ex.Conflicts)
}
