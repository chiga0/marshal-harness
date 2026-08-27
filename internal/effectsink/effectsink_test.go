package effectsink

import (
	"errors"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

func digestOf(parts ...string) string {
	return canonical.DigestBytes([]byte(strings.Join(parts, "|")))
}

func mustIntent(t *testing.T) EffectIntent {
	t.Helper()
	intent, err := NewEffectIntent(
		"intent-0001",
		SinkKindSCMMutation,
		"repo:marshal-harness/branch:feat-x",
		"idem-0001",
		7,
		"fence-token-aaaa",
		digestOf("publication-authorization", "v1"),
		digestOf("target-state", "v1"),
	)
	if err != nil {
		t.Fatalf("NewEffectIntent: %v", err)
	}
	return intent
}

func passingView(intent EffectIntent) CurrentView {
	return CurrentView{
		CurrentGeneration:          intent.Generation,
		CurrentFencingToken:        intent.FencingToken,
		AuthorizationRevoked:       false,
		CurrentAuthorizationDigest: intent.AuthorizationDigest,
		CurrentTargetDigest:        intent.TargetDigest,
	}
}

func assertEffectsinkError(t *testing.T, err error, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Errorf("expected %v, got %v", want, err)
	}
	if err == nil || !strings.HasPrefix(err.Error(), "effectsink: ") {
		t.Errorf("error %v missing effectsink: prefix", err)
	}
}

func assertVerdict(t *testing.T, got *Verdict, wantOK bool, wantReason RejectionReason) {
	t.Helper()
	if got == nil {
		t.Fatalf("expected Verdict, got nil")
	}
	if got.OK != wantOK {
		t.Errorf("Verdict.OK = %v, want %v (verdict %+v)", got.OK, wantOK, got)
	}
	if got.Reason != wantReason {
		t.Errorf("Verdict.Reason = %q, want %q", got.Reason, wantReason)
	}
}

// ── EffectIntent 构造 ─────────────────────────────────────────────────────

func TestNewEffectIntent_Positive(t *testing.T) {
	intent := mustIntent(t)
	if err := requireDigest("IntentDigest", intent.IntentDigest); err != nil {
		t.Errorf("derived IntentDigest malformed: %v", err)
	}
	if err := intent.Validate(); err != nil {
		t.Errorf("freshly constructed intent must validate, got %v", err)
	}

	// 同输入 digest 稳定；任一身份字段变化 digest 必变。
	again, err := NewEffectIntent("intent-0001", SinkKindSCMMutation, "repo:marshal-harness/branch:feat-x", "idem-0001", 7, "fence-token-aaaa", digestOf("publication-authorization", "v1"), digestOf("target-state", "v1"))
	if err != nil {
		t.Fatalf("NewEffectIntent again: %v", err)
	}
	if again.IntentDigest != intent.IntentDigest {
		t.Errorf("IntentDigest must be deterministic: %q vs %q", intent.IntentDigest, again.IntentDigest)
	}
	other, err := NewEffectIntent("intent-0002", SinkKindSCMMutation, "repo:marshal-harness/branch:feat-x", "idem-0001", 7, "fence-token-aaaa", digestOf("publication-authorization", "v1"), digestOf("target-state", "v1"))
	if err != nil {
		t.Fatalf("NewEffectIntent other: %v", err)
	}
	if other.IntentDigest == intent.IntentDigest {
		t.Errorf("different identity fields must yield different IntentDigest")
	}
}

func TestNewEffectIntent_MalformedInputs(t *testing.T) {
	type args struct {
		intentID            string
		sink                SinkKind
		targetID            string
		idempotencyKey      string
		generation          int64
		fencingToken        string
		authorizationDigest string
		targetDigest        string
	}
	baseline := func() args {
		return args{
			intentID:            "intent-0001",
			sink:                SinkKindSCMMutation,
			targetID:            "repo:marshal-harness/branch:feat-x",
			idempotencyKey:      "idem-0001",
			generation:          7,
			fencingToken:        "fence-token-aaaa",
			authorizationDigest: digestOf("publication-authorization", "v1"),
			targetDigest:        digestOf("target-state", "v1"),
		}
	}

	tests := []struct {
		name   string
		mutate func(*args)
		want   error
	}{
		{"empty intent id", func(a *args) { a.intentID = "" }, ErrMalformedIntent},
		{"whitespace intent id", func(a *args) { a.intentID = "   " }, ErrMalformedIntent},
		{"empty sink", func(a *args) { a.sink = "" }, ErrUnknownSinkKind},
		{"unknown sink", func(a *args) { a.sink = SinkKind("dns-poison") }, ErrUnknownSinkKind},
		{"empty target id", func(a *args) { a.targetID = "" }, ErrMalformedIntent},
		{"whitespace target id", func(a *args) { a.targetID = "  " }, ErrMalformedIntent},
		{"empty idempotency key", func(a *args) { a.idempotencyKey = "" }, ErrMalformedIntent},
		{"zero generation", func(a *args) { a.generation = 0 }, ErrMalformedIntent},
		{"negative generation", func(a *args) { a.generation = -1 }, ErrMalformedIntent},
		{"empty fencing token", func(a *args) { a.fencingToken = "" }, ErrMalformedIntent},
		{"whitespace fencing token", func(a *args) { a.fencingToken = "   " }, ErrMalformedIntent},
		{"empty authorization digest", func(a *args) { a.authorizationDigest = "" }, ErrMalformedIntent},
		{"authorization digest missing sha256 prefix", func(a *args) {
			a.authorizationDigest = strings.TrimPrefix(digestOf("publication-authorization", "v1"), "sha256:")
		}, ErrMalformedIntent},
		{"authorization digest too short", func(a *args) { a.authorizationDigest = "sha256:abc123" }, ErrMalformedIntent},
		{"authorization digest uppercase hex", func(a *args) {
			a.authorizationDigest = "sha256:" + strings.ToUpper(strings.TrimPrefix(digestOf("publication-authorization", "v1"), "sha256:"))
		}, ErrMalformedIntent},
		{"authorization digest non-hex", func(a *args) { a.authorizationDigest = "sha256:" + strings.Repeat("g", 64) }, ErrMalformedIntent},
		{"empty target digest", func(a *args) { a.targetDigest = "" }, ErrMalformedIntent},
		{"target digest too short", func(a *args) { a.targetDigest = "sha256:def456" }, ErrMalformedIntent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := baseline()
			tt.mutate(&a)
			_, err := NewEffectIntent(a.intentID, a.sink, a.targetID, a.idempotencyKey, a.generation, a.fencingToken, a.authorizationDigest, a.targetDigest)
			assertEffectsinkError(t, err, tt.want)
		})
	}
}

// 所有封闭枚举成员（含 other-effect）都必须可构造。
func TestNewEffectIntent_AllSinkKinds(t *testing.T) {
	for _, sink := range []SinkKind{SinkKindSCMMutation, SinkKindArtifactWrite, SinkKindSecretUse, SinkKindOtherEffect} {
		t.Run(string(sink), func(t *testing.T) {
			intent, err := NewEffectIntent("intent-0001", sink, "target-1", "idem-0001", 7, "fence-token-aaaa", digestOf("publication-authorization", "v1"), digestOf("target-state", "v1"))
			if err != nil {
				t.Fatalf("NewEffectIntent(%s): %v", sink, err)
			}
			if err := intent.Validate(); err != nil {
				t.Errorf("intent for sink %s must validate, got %v", sink, err)
			}
		})
	}
}

// ── EffectIntent.Validate ─────────────────────────────────────────────────

func TestEffectIntent_Validate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*EffectIntent)
		want   error
	}{
		// 篡改：单字段变更为另一合法形态值，重算必不一致。
		{"tampered intent id", func(i *EffectIntent) { i.IntentID = "intent-9999" }, ErrIntentTampered},
		{"tampered sink", func(i *EffectIntent) { i.Sink = SinkKindSecretUse }, ErrIntentTampered},
		{"tampered target id", func(i *EffectIntent) { i.TargetID = "repo:marshal-harness/branch:main" }, ErrIntentTampered},
		{"tampered idempotency key", func(i *EffectIntent) { i.IdempotencyKey = "idem-9999" }, ErrIntentTampered},
		{"tampered generation", func(i *EffectIntent) { i.Generation = 8 }, ErrIntentTampered},
		{"tampered fencing token", func(i *EffectIntent) { i.FencingToken = "fence-token-bbbb" }, ErrIntentTampered},
		{"tampered authorization digest", func(i *EffectIntent) {
			i.AuthorizationDigest = digestOf("publication-authorization", "v2")
		}, ErrIntentTampered},
		{"tampered target digest", func(i *EffectIntent) { i.TargetDigest = digestOf("target-state", "v2") }, ErrIntentTampered},
		{"empty stored digest", func(i *EffectIntent) { i.IntentDigest = "" }, ErrIntentTampered},
		{"malformed stored digest", func(i *EffectIntent) { i.IntentDigest = "sha256:short" }, ErrIntentTampered},
		{"forged stored digest", func(i *EffectIntent) { i.IntentDigest = digestOf("forged") }, ErrIntentTampered},
		// 字段形态非法：身份校验先于 digest 比对，fail closed。
		{"empty intent id", func(i *EffectIntent) { i.IntentID = "" }, ErrMalformedIntent},
		{"unknown sink", func(i *EffectIntent) { i.Sink = SinkKind("unknown") }, ErrUnknownSinkKind},
		{"empty target id", func(i *EffectIntent) { i.TargetID = "" }, ErrMalformedIntent},
		{"empty idempotency key", func(i *EffectIntent) { i.IdempotencyKey = "" }, ErrMalformedIntent},
		{"zero generation", func(i *EffectIntent) { i.Generation = 0 }, ErrMalformedIntent},
		{"empty fencing token", func(i *EffectIntent) { i.FencingToken = "" }, ErrMalformedIntent},
		{"authorization digest malformed", func(i *EffectIntent) { i.AuthorizationDigest = "sha256:short" }, ErrMalformedIntent},
		{"target digest malformed", func(i *EffectIntent) { i.TargetDigest = "sha256:short" }, ErrMalformedIntent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := mustIntent(t)
			tt.mutate(&intent)
			err := intent.Validate()
			assertEffectsinkError(t, err, tt.want)

			// 结构性错误同样经 VerifyBeforeEffect 走 error 通道，不产出 Verdict。
			verdict, verr := VerifyBeforeEffect(intent, passingView(mustIntent(t)))
			if verdict != nil {
				t.Errorf("structural failure must not produce Verdict, got %+v", verdict)
			}
			assertEffectsinkError(t, verr, tt.want)
		})
	}
}

// ── CurrentView.Validate ──────────────────────────────────────────────────

func TestCurrentView_Validate(t *testing.T) {
	baseline := mustIntent(t)

	tests := []struct {
		name   string
		mutate func(*CurrentView)
		want   error
	}{
		{"zero generation", func(v *CurrentView) { v.CurrentGeneration = 0 }, ErrMalformedView},
		{"negative generation", func(v *CurrentView) { v.CurrentGeneration = -3 }, ErrMalformedView},
		{"empty fencing token", func(v *CurrentView) { v.CurrentFencingToken = "" }, ErrMalformedView},
		{"whitespace fencing token", func(v *CurrentView) { v.CurrentFencingToken = "  " }, ErrMalformedView},
		{"empty current authorization digest", func(v *CurrentView) { v.CurrentAuthorizationDigest = "" }, ErrMalformedView},
		{"current authorization digest malformed", func(v *CurrentView) {
			v.CurrentAuthorizationDigest = "sha256:short"
		}, ErrMalformedView},
		{"current authorization digest uppercase", func(v *CurrentView) {
			v.CurrentAuthorizationDigest = "sha256:" + strings.ToUpper(strings.TrimPrefix(v.CurrentAuthorizationDigest, "sha256:"))
		}, ErrMalformedView},
		{"empty current target digest", func(v *CurrentView) { v.CurrentTargetDigest = "" }, ErrMalformedView},
		{"current target digest malformed", func(v *CurrentView) { v.CurrentTargetDigest = "sha256:xyz" }, ErrMalformedView},
		// 撤销态不豁免 digest 形态校验：view 必须内部一致，fail closed。
		{"revoked but authorization digest malformed", func(v *CurrentView) {
			v.AuthorizationRevoked = true
			v.CurrentAuthorizationDigest = ""
		}, ErrMalformedView},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := passingView(baseline)
			tt.mutate(&view)
			err := view.Validate()
			assertEffectsinkError(t, err, tt.want)

			// 结构性错误经 VerifyBeforeEffect 走 error 通道，不产出 Verdict。
			verdict, verr := VerifyBeforeEffect(baseline, view)
			if verdict != nil {
				t.Errorf("malformed view must not produce Verdict, got %+v", verdict)
			}
			assertEffectsinkError(t, verr, tt.want)
		})
	}
}

// ── VerifyBeforeEffect ────────────────────────────────────────────────────

func TestVerifyBeforeEffect_AllPass(t *testing.T) {
	intent := mustIntent(t)
	verdict, err := VerifyBeforeEffect(intent, passingView(intent))
	if err != nil {
		t.Fatalf("VerifyBeforeEffect: %v", err)
	}
	assertVerdict(t, verdict, true, "")
	if verdict.AlreadyExecuted {
		t.Errorf("fresh verify must not claim AlreadyExecuted")
	}
}

// revoke→effect 竞态：意图签发后授权被撤销，意图到达 sink 时永不执行。
// 这是本包存在的核心防线，单独命名。
func TestVerifyBeforeEffect_RevokeEffectRace(t *testing.T) {
	intent := mustIntent(t)
	view := passingView(intent)
	view.AuthorizationRevoked = true

	verdict, err := VerifyBeforeEffect(intent, view)
	if err != nil {
		t.Fatalf("lifecycle rejection must not surface as error, got %v", err)
	}
	assertVerdict(t, verdict, false, RejectionReasonAuthorizationRevoked)
}

// 五类生命周期拒绝各以通过基线的单变量 delta 构造。
func TestVerifyBeforeEffect_LifecycleRejections(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*CurrentView)
		wantReason RejectionReason
	}{
		{"generation stale: ledger advanced", func(v *CurrentView) {
			v.CurrentGeneration = 8
		}, RejectionReasonGenerationStale},
		{"generation stale: ledger behind", func(v *CurrentView) {
			v.CurrentGeneration = 6
		}, RejectionReasonGenerationStale},
		{"fencing mismatch", func(v *CurrentView) {
			v.CurrentFencingToken = "fence-token-bbbb"
		}, RejectionReasonFencingMismatch},
		{"authorization superseded", func(v *CurrentView) {
			v.CurrentAuthorizationDigest = digestOf("publication-authorization", "v2")
		}, RejectionReasonAuthorizationSuperseded},
		{"target drifted", func(v *CurrentView) {
			v.CurrentTargetDigest = digestOf("target-state", "v2")
		}, RejectionReasonTargetDrifted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := mustIntent(t)
			view := passingView(intent)
			tt.mutate(&view)

			// 单变量 delta 前提：基线必须通过。
			baseVerdict, err := VerifyBeforeEffect(intent, passingView(intent))
			if err != nil || !baseVerdict.OK {
				t.Fatalf("baseline must pass: verdict %+v, err %v", baseVerdict, err)
			}

			verdict, err := VerifyBeforeEffect(intent, view)
			if err != nil {
				t.Fatalf("lifecycle rejection must not surface as error, got %v", err)
			}
			assertVerdict(t, verdict, false, tt.wantReason)
			if verdict.AlreadyExecuted {
				t.Errorf("rejection must not claim AlreadyExecuted")
			}
		})
	}
}

// 评估顺序冻结：revoked 优先于其它 lifecycle 拒绝。
func TestVerifyBeforeEffect_RevokedWinsOverOtherRejections(t *testing.T) {
	intent := mustIntent(t)
	view := passingView(intent)
	view.AuthorizationRevoked = true
	view.CurrentGeneration = 999             // 同时 generation-stale
	view.CurrentFencingToken = "other-token" // 同时 fencing-mismatch
	view.CurrentAuthorizationDigest = digestOf("publication-authorization", "v2")
	view.CurrentTargetDigest = digestOf("target-state", "v2")

	verdict, err := VerifyBeforeEffect(intent, view)
	if err != nil {
		t.Fatalf("VerifyBeforeEffect: %v", err)
	}
	assertVerdict(t, verdict, false, RejectionReasonAuthorizationRevoked)
}

// ── EffectLedger ──────────────────────────────────────────────────────────

func TestEffectLedger_MarkExecutedAndReplay(t *testing.T) {
	ledger := NewEffectLedger()
	intent := mustIntent(t)

	if _, ok := ledger.Executed(intent.IdempotencyKey); ok {
		t.Fatalf("fresh ledger must not contain key %q", intent.IdempotencyKey)
	}
	if err := ledger.MarkExecuted(intent); err != nil {
		t.Fatalf("MarkExecuted: %v", err)
	}
	got, ok := ledger.Executed(intent.IdempotencyKey)
	if !ok {
		t.Fatalf("Executed must find key %q after MarkExecuted", intent.IdempotencyKey)
	}
	if got.IntentDigest != intent.IntentDigest {
		t.Errorf("stored IntentDigest = %q, want %q", got.IntentDigest, intent.IntentDigest)
	}

	// 同 key 同 IntentDigest 重放：幂等成功。
	if err := ledger.MarkExecuted(intent); err != nil {
		t.Errorf("idempotent replay must succeed, got %v", err)
	}
}

func TestEffectLedger_ConflictFailsClosed(t *testing.T) {
	ledger := NewEffectLedger()
	first := mustIntent(t)
	if err := ledger.MarkExecuted(first); err != nil {
		t.Fatalf("MarkExecuted first: %v", err)
	}

	// 同 key 不同 IntentDigest：另一条合法 intent 复用同一 idempotency key。
	second, err := NewEffectIntent(
		"intent-0002",
		SinkKindSCMMutation,
		"repo:marshal-harness/branch:feat-x",
		"idem-0001", // 与 first 相同的 key
		7,
		"fence-token-aaaa",
		digestOf("publication-authorization", "v1"),
		digestOf("target-state", "v1"),
	)
	if err != nil {
		t.Fatalf("NewEffectIntent second: %v", err)
	}

	err = ledger.MarkExecuted(second)
	assertEffectsinkError(t, err, ErrEffectConflict)

	// fail closed：账本保持首次内容，永不覆盖。
	got, ok := ledger.Executed(first.IdempotencyKey)
	if !ok {
		t.Fatalf("ledger must retain first intent")
	}
	if got.IntentDigest != first.IntentDigest {
		t.Errorf("conflict must never overwrite: stored digest = %q, want first %q", got.IntentDigest, first.IntentDigest)
	}
}

func TestEffectLedger_MarkExecutedRequiresValidIntent(t *testing.T) {
	ledger := NewEffectLedger()

	tampered := mustIntent(t)
	tampered.Generation = 8
	assertEffectsinkError(t, ledger.MarkExecuted(tampered), ErrIntentTampered)

	malformed := mustIntent(t)
	malformed.TargetID = ""
	assertEffectsinkError(t, ledger.MarkExecuted(malformed), ErrMalformedIntent)

	if _, ok := ledger.Executed(tampered.IdempotencyKey); ok {
		t.Errorf("invalid intent must never be recorded")
	}
}

func TestEffectLedger_NilLedger(t *testing.T) {
	var ledger *EffectLedger
	assertEffectsinkError(t, ledger.MarkExecuted(mustIntent(t)), ErrNilLedger)

	_, err := ExecuteIfAdmitted(nil, mustIntent(t), passingView(mustIntent(t)))
	assertEffectsinkError(t, err, ErrNilLedger)
}

// ── ExecuteIfAdmitted 组合门禁 ────────────────────────────────────────────

// admit → mark → replay idempotent：同 key 同 digest 重放不产生第二次
// 外部效果声明。
func TestExecuteIfAdmitted_AdmitMarkReplayIdempotent(t *testing.T) {
	ledger := NewEffectLedger()
	intent := mustIntent(t)

	first, err := ExecuteIfAdmitted(ledger, intent, passingView(intent))
	if err != nil {
		t.Fatalf("first ExecuteIfAdmitted: %v", err)
	}
	assertVerdict(t, first, true, "")
	if first.AlreadyExecuted {
		t.Errorf("first admission must not claim AlreadyExecuted")
	}
	if _, ok := ledger.Executed(intent.IdempotencyKey); !ok {
		t.Fatalf("admitted intent must be marked executed")
	}

	second, err := ExecuteIfAdmitted(ledger, intent, passingView(intent))
	if err != nil {
		t.Fatalf("replay ExecuteIfAdmitted: %v", err)
	}
	assertVerdict(t, second, true, "")
	if !second.AlreadyExecuted {
		t.Errorf("replay of identical intent must set AlreadyExecuted")
	}
}

// admit → revoke → second attempt rejected AND NOT executed：授权撤销后
// 新意图（不同 key）被拒绝且账本不含该 key；即使重放已执行过的旧意图，
// revoke 仍优先于幂等重放。
func TestExecuteIfAdmitted_RevokeAfterAdmitPreventsFurtherEffects(t *testing.T) {
	ledger := NewEffectLedger()
	first := mustIntent(t)
	if _, err := ExecuteIfAdmitted(ledger, first, passingView(first)); err != nil {
		t.Fatalf("first ExecuteIfAdmitted: %v", err)
	}

	revokedView := passingView(first)
	revokedView.AuthorizationRevoked = true

	// 新意图（不同 idempotency key）在撤销后到达：拒绝且永不记录。
	second, err := NewEffectIntent(
		"intent-0002",
		SinkKindArtifactWrite,
		"artifact:bundle-7",
		"idem-0002",
		7,
		"fence-token-aaaa",
		digestOf("publication-authorization", "v1"),
		digestOf("target-state", "v1"),
	)
	if err != nil {
		t.Fatalf("NewEffectIntent second: %v", err)
	}
	secondView := revokedView
	secondView.CurrentTargetDigest = second.TargetDigest

	verdict, err := ExecuteIfAdmitted(ledger, second, secondView)
	if err != nil {
		t.Fatalf("lifecycle rejection must not surface as error, got %v", err)
	}
	assertVerdict(t, verdict, false, RejectionReasonAuthorizationRevoked)
	if _, ok := ledger.Executed(second.IdempotencyKey); ok {
		t.Errorf("rejected intent must NOT be marked executed")
	}

	// 撤销后重放已执行的旧意图：同样被拒绝（revoke 优先于幂等重放）。
	replay, err := ExecuteIfAdmitted(ledger, first, revokedView)
	if err != nil {
		t.Fatalf("replay rejection must not surface as error, got %v", err)
	}
	assertVerdict(t, replay, false, RejectionReasonAuthorizationRevoked)
}

// conflict never overwrites：同 key 不同 digest 的第二意图经组合门禁
// 报 ErrEffectConflict，账本保持首次内容。
func TestExecuteIfAdmitted_ConflictNeverOverwrites(t *testing.T) {
	ledger := NewEffectLedger()
	first := mustIntent(t)
	if _, err := ExecuteIfAdmitted(ledger, first, passingView(first)); err != nil {
		t.Fatalf("first ExecuteIfAdmitted: %v", err)
	}

	second, err := NewEffectIntent(
		"intent-0002",
		SinkKindSCMMutation,
		"repo:marshal-harness/branch:feat-x",
		"idem-0001", // 与 first 相同的 key
		7,
		"fence-token-aaaa",
		digestOf("publication-authorization", "v1"),
		digestOf("target-state", "v1"),
	)
	if err != nil {
		t.Fatalf("NewEffectIntent second: %v", err)
	}

	// verify 本身必须通过（second 也是合法意图且 view 匹配），冲突
	// 只能在账本层暴露——证明拒绝来自防重而非 recheck。
	verdict, err := ExecuteIfAdmitted(ledger, second, passingView(first))
	if verdict != nil {
		t.Errorf("conflict must surface as error, not Verdict, got %+v", verdict)
	}
	assertEffectsinkError(t, err, ErrEffectConflict)

	got, ok := ledger.Executed(first.IdempotencyKey)
	if !ok {
		t.Fatalf("ledger must retain first intent")
	}
	if got.IntentDigest != first.IntentDigest {
		t.Errorf("conflict must never overwrite: stored digest = %q, want first %q", got.IntentDigest, first.IntentDigest)
	}
}

// structural error 经组合门禁原样上抛，不产出 Verdict、不记录。
func TestExecuteIfAdmitted_StructuralErrorsPassthrough(t *testing.T) {
	ledger := NewEffectLedger()

	tampered := mustIntent(t)
	tampered.FencingToken = "fence-token-forged"
	verdict, err := ExecuteIfAdmitted(ledger, tampered, passingView(mustIntent(t)))
	if verdict != nil {
		t.Errorf("structural failure must not produce Verdict, got %+v", verdict)
	}
	assertEffectsinkError(t, err, ErrIntentTampered)

	badView := passingView(mustIntent(t))
	badView.CurrentTargetDigest = ""
	verdict, err = ExecuteIfAdmitted(ledger, mustIntent(t), badView)
	if verdict != nil {
		t.Errorf("malformed view must not produce Verdict, got %+v", verdict)
	}
	assertEffectsinkError(t, err, ErrMalformedView)

	if _, ok := ledger.Executed(tampered.IdempotencyKey); ok {
		t.Errorf("structurally rejected intent must never be recorded")
	}
}
