package jitgate

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

func digestOf(parts ...string) string {
	return canonical.DigestBytes([]byte(strings.Join(parts, "|")))
}

const testValidUntilUnix = int64(6000)

func testNow() time.Time { return time.Unix(5000, 0).UTC() }

func mustToken(t *testing.T) AdmissionToken {
	t.Helper()
	token, err := NewAdmissionToken(
		"decision-0001",
		"registration:aaaa1111",
		digestOf("snapshot", "v1"),
		digestOf("policy", "v1"),
		testValidUntilUnix,
	)
	if err != nil {
		t.Fatalf("NewAdmissionToken: %v", err)
	}
	return token
}

func passingView(token AdmissionToken) LedgerView {
	return LedgerView{
		RegistrationActive:   true,
		ActiveSnapshotDigest: token.SnapshotDigest,
		CurrentPolicyDigest:  token.PolicyDigest,
		PolicyActive:         true,
	}
}

func assertJitgateError(t *testing.T, err error, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Errorf("expected %v, got %v", want, err)
	}
	if err == nil || !strings.HasPrefix(err.Error(), "jitgate: ") {
		t.Errorf("error %v missing jitgate: prefix", err)
	}
}

// ── AdmissionToken 构造 ─────────────────────────────────────────────────────

func TestNewAdmissionToken_Positive(t *testing.T) {
	token := mustToken(t)
	if err := requireDigest("TokenDigest", token.TokenDigest); err != nil {
		t.Errorf("derived TokenDigest malformed: %v", err)
	}
	if err := token.Validate(); err != nil {
		t.Errorf("freshly constructed token must validate, got %v", err)
	}

	// 同输入 digest 稳定；任一身份字段变化 digest 必变。
	again, err := NewAdmissionToken("decision-0001", "registration:aaaa1111", digestOf("snapshot", "v1"), digestOf("policy", "v1"), testValidUntilUnix)
	if err != nil {
		t.Fatalf("NewAdmissionToken again: %v", err)
	}
	if again.TokenDigest != token.TokenDigest {
		t.Errorf("TokenDigest must be deterministic: %q vs %q", token.TokenDigest, again.TokenDigest)
	}
	other, err := NewAdmissionToken("decision-0002", "registration:aaaa1111", digestOf("snapshot", "v1"), digestOf("policy", "v1"), testValidUntilUnix)
	if err != nil {
		t.Fatalf("NewAdmissionToken other: %v", err)
	}
	if other.TokenDigest == token.TokenDigest {
		t.Errorf("different identity fields must yield different TokenDigest")
	}
}

func TestNewAdmissionToken_MalformedInputs(t *testing.T) {
	type args struct {
		decisionID     string
		registrationID string
		snapshotDigest string
		policyDigest   string
		validUntilUnix int64
	}
	baseline := func() args {
		return args{
			decisionID:     "decision-0001",
			registrationID: "registration:aaaa1111",
			snapshotDigest: digestOf("snapshot", "v1"),
			policyDigest:   digestOf("policy", "v1"),
			validUntilUnix: testValidUntilUnix,
		}
	}

	tests := []struct {
		name   string
		mutate func(*args)
	}{
		{"empty decision id", func(a *args) { a.decisionID = "" }},
		{"whitespace decision id", func(a *args) { a.decisionID = "   " }},
		{"empty registration id", func(a *args) { a.registrationID = "" }},
		{"registration id missing prefix", func(a *args) { a.registrationID = "aaaa1111" }},
		{"empty snapshot digest", func(a *args) { a.snapshotDigest = "" }},
		{"snapshot digest missing sha256 prefix", func(a *args) { a.snapshotDigest = strings.TrimPrefix(digestOf("snapshot", "v1"), "sha256:") }},
		{"snapshot digest too short", func(a *args) { a.snapshotDigest = "sha256:abc123" }},
		{"snapshot digest uppercase hex", func(a *args) {
			a.snapshotDigest = "sha256:" + strings.ToUpper(strings.TrimPrefix(digestOf("snapshot", "v1"), "sha256:"))
		}},
		{"snapshot digest non-hex", func(a *args) { a.snapshotDigest = "sha256:" + strings.Repeat("g", 64) }},
		{"empty policy digest", func(a *args) { a.policyDigest = "" }},
		{"policy digest too short", func(a *args) { a.policyDigest = "sha256:def456" }},
		{"zero validUntil", func(a *args) { a.validUntilUnix = 0 }},
		{"negative validUntil", func(a *args) { a.validUntilUnix = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := baseline()
			tt.mutate(&a)
			_, err := NewAdmissionToken(a.decisionID, a.registrationID, a.snapshotDigest, a.policyDigest, a.validUntilUnix)
			assertJitgateError(t, err, ErrMalformedToken)
		})
	}
}

// ── AdmissionToken.Validate ─────────────────────────────────────────────────

func TestAdmissionToken_Validate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AdmissionToken)
		want   error
	}{
		// 篡改：单字段变更为另一合法形态值，重算必不一致。
		{"tampered decision id", func(tok *AdmissionToken) { tok.DecisionID = "decision-9999" }, ErrTokenTampered},
		{"tampered registration id", func(tok *AdmissionToken) { tok.RegistrationID = "registration:bbbb2222" }, ErrTokenTampered},
		{"tampered snapshot digest", func(tok *AdmissionToken) { tok.SnapshotDigest = digestOf("snapshot", "v2") }, ErrTokenTampered},
		{"tampered policy digest", func(tok *AdmissionToken) { tok.PolicyDigest = digestOf("policy", "v2") }, ErrTokenTampered},
		{"tampered validUntil", func(tok *AdmissionToken) { tok.ValidUntilUnix = testValidUntilUnix + 1 }, ErrTokenTampered},
		{"empty stored digest", func(tok *AdmissionToken) { tok.TokenDigest = "" }, ErrTokenTampered},
		{"malformed stored digest", func(tok *AdmissionToken) { tok.TokenDigest = "sha256:short" }, ErrTokenTampered},
		{"forged stored digest", func(tok *AdmissionToken) { tok.TokenDigest = digestOf("forged") }, ErrTokenTampered},
		// 字段形态非法：身份校验先于 digest 比对，fail closed。
		{"empty decision id", func(tok *AdmissionToken) { tok.DecisionID = "" }, ErrMalformedToken},
		{"registration prefix dropped", func(tok *AdmissionToken) { tok.RegistrationID = "aaaa1111" }, ErrMalformedToken},
		{"snapshot digest malformed", func(tok *AdmissionToken) { tok.SnapshotDigest = "sha256:short" }, ErrMalformedToken},
		{"policy digest malformed", func(tok *AdmissionToken) { tok.PolicyDigest = "" }, ErrMalformedToken},
		{"validUntil zeroed", func(tok *AdmissionToken) { tok.ValidUntilUnix = 0 }, ErrMalformedToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := mustToken(t)
			tt.mutate(&token)
			assertJitgateError(t, token.Validate(), tt.want)
		})
	}
}

// ── VerifyBeforeProvision：正向与单变量拒绝 ────────────────────────────────

func TestVerifyBeforeProvision_Positive(t *testing.T) {
	token := mustToken(t)
	verdict, err := VerifyBeforeProvision(token, passingView(token), testNow())
	if err != nil {
		t.Fatalf("VerifyBeforeProvision: %v", err)
	}
	if verdict == nil || !verdict.OK {
		t.Fatalf("expected passing verdict, got %+v", verdict)
	}
	if verdict.Reason != "" {
		t.Errorf("passing verdict must not carry a reason, got %q", verdict.Reason)
	}
}

// 六个 RejectionReason 各命中一次：五种策略/生命周期拒绝经 Verdict（error
// 恒为 nil），token-tampered 经 error 通道（Verdict 恒为 nil）。每个用例
// 都只对通过基线做单一变量变更。
func TestVerifyBeforeProvision_SingleVariableRejections(t *testing.T) {
	tests := []struct {
		name       string
		mutateView func(*LedgerView)
		mutateNow  func() time.Time // nil → 基线 now
		want       RejectionReason
	}{
		{
			name: "registration-inactive",
			mutateView: func(v *LedgerView) {
				v.RegistrationActive = false
				v.ActiveSnapshotDigest = ""
			},
			want: RejectionReasonRegistrationInactive,
		},
		{
			name:       "snapshot-superseded",
			mutateView: func(v *LedgerView) { v.ActiveSnapshotDigest = digestOf("snapshot", "v2") },
			want:       RejectionReasonSnapshotSuperseded,
		},
		{
			name:       "policy-inactive",
			mutateView: func(v *LedgerView) { v.PolicyActive = false },
			want:       RejectionReasonPolicyInactive,
		},
		{
			name:       "policy-rotated",
			mutateView: func(v *LedgerView) { v.CurrentPolicyDigest = digestOf("policy", "v2") },
			want:       RejectionReasonPolicyRotated,
		},
		{
			name:      "token-expired",
			mutateNow: func() time.Time { return time.Unix(testValidUntilUnix, 0).UTC() },
			want:      RejectionReasonTokenExpired,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := mustToken(t)
			view := passingView(token)
			if tt.mutateView != nil {
				tt.mutateView(&view)
			}
			now := testNow()
			if tt.mutateNow != nil {
				now = tt.mutateNow()
			}
			verdict, err := VerifyBeforeProvision(token, view, now)
			if err != nil {
				t.Fatalf("business rejection must not leak into error, got %v", err)
			}
			if verdict == nil {
				t.Fatalf("business rejection must produce a verdict")
			}
			if verdict.OK {
				t.Errorf("expected rejection, got OK verdict")
			}
			if verdict.Reason != tt.want {
				t.Errorf("reason = %q, want %q", verdict.Reason, tt.want)
			}
		})
	}

	// token-tampered：对通过基线的 token 做单一变量篡改 → error 通道。
	t.Run("token-tampered", func(t *testing.T) {
		token := mustToken(t)
		token.PolicyDigest = digestOf("policy", "v2") // 篡改后不再 re-derive
		verdict, err := VerifyBeforeProvision(token, passingView(mustToken(t)), testNow())
		if verdict != nil {
			t.Errorf("structural problem must not leak into Verdict, got %+v", verdict)
		}
		assertJitgateError(t, err, ErrTokenTampered)
	})
}

// ── VerifyBeforeProvision：validUntil 边界（半开区间 [issue, validUntil)）──

func TestVerifyBeforeProvision_ValidUntilBoundary(t *testing.T) {
	token := mustToken(t)
	view := passingView(token)

	// now == validUntil → 已过期。
	verdict, err := VerifyBeforeProvision(token, view, time.Unix(testValidUntilUnix, 0).UTC())
	if err != nil {
		t.Fatalf("boundary rejection must not leak into error, got %v", err)
	}
	if verdict == nil || verdict.OK || verdict.Reason != RejectionReasonTokenExpired {
		t.Errorf("now == validUntil must be token-expired, got %+v", verdict)
	}

	// now == validUntil-1 → 仍有效。
	verdict, err = VerifyBeforeProvision(token, view, time.Unix(testValidUntilUnix-1, 0).UTC())
	if err != nil {
		t.Fatalf("VerifyBeforeProvision: %v", err)
	}
	if verdict == nil || !verdict.OK {
		t.Errorf("now == validUntil-1 must pass, got %+v", verdict)
	}

	// now 远超 validUntil → 过期。
	verdict, err = VerifyBeforeProvision(token, view, time.Unix(testValidUntilUnix+3600, 0).UTC())
	if err != nil {
		t.Fatalf("expired rejection must not leak into error, got %v", err)
	}
	if verdict == nil || verdict.OK || verdict.Reason != RejectionReasonTokenExpired {
		t.Errorf("now > validUntil must be token-expired, got %+v", verdict)
	}
}

// ── VerifyBeforeProvision：形态非法输入（结构性问题 → error 通道）─────────

func TestVerifyBeforeProvision_MalformedView(t *testing.T) {
	tests := []struct {
		name       string
		mutateView func(*LedgerView)
	}{
		{"empty current policy digest", func(v *LedgerView) { v.CurrentPolicyDigest = "" }},
		{"short current policy digest", func(v *LedgerView) { v.CurrentPolicyDigest = "sha256:short" }},
		{"policy digest missing prefix", func(v *LedgerView) { v.CurrentPolicyDigest = strings.TrimPrefix(digestOf("policy", "v1"), "sha256:") }},
		{"active registration with empty snapshot digest", func(v *LedgerView) { v.ActiveSnapshotDigest = "" }},
		{"active registration with malformed snapshot digest", func(v *LedgerView) { v.ActiveSnapshotDigest = "garbage" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := mustToken(t)
			view := passingView(token)
			tt.mutateView(&view)
			verdict, err := VerifyBeforeProvision(token, view, testNow())
			if verdict != nil {
				t.Errorf("structural problem must not leak into Verdict, got %+v", verdict)
			}
			assertJitgateError(t, err, ErrMalformedView)
		})
	}

	// 零值 LedgerView（CurrentPolicyDigest 为空）也是形态非法。
	t.Run("zero ledger view", func(t *testing.T) {
		verdict, err := VerifyBeforeProvision(mustToken(t), LedgerView{}, testNow())
		if verdict != nil {
			t.Errorf("structural problem must not leak into Verdict, got %+v", verdict)
		}
		assertJitgateError(t, err, ErrMalformedView)
	})

	// RegistrationActive==false 时 ActiveSnapshotDigest 不参与形态校验：
	// view 合法，按 registration-inactive 拒绝。
	t.Run("inactive registration ignores snapshot digest", func(t *testing.T) {
		token := mustToken(t)
		view := passingView(token)
		view.RegistrationActive = false
		view.ActiveSnapshotDigest = "not-a-digest"
		verdict, err := VerifyBeforeProvision(token, view, testNow())
		if err != nil {
			t.Fatalf("business rejection must not leak into error, got %v", err)
		}
		if verdict == nil || verdict.OK || verdict.Reason != RejectionReasonRegistrationInactive {
			t.Errorf("expected registration-inactive, got %+v", verdict)
		}
	})
}

func TestVerifyBeforeProvision_MalformedToken(t *testing.T) {
	var zero AdmissionToken
	verdict, err := VerifyBeforeProvision(zero, passingView(mustToken(t)), testNow())
	if verdict != nil {
		t.Errorf("structural problem must not leak into Verdict, got %+v", verdict)
	}
	assertJitgateError(t, err, ErrMalformedToken)
}

// ── VerifyBeforeProvision：冻结顺序，首个失败生效 ──────────────────────────

func TestVerifyBeforeProvision_FirstFailureWins(t *testing.T) {
	tests := []struct {
		name       string
		mutateView func(*LedgerView)
		now        time.Time
		want       RejectionReason
	}{
		{
			name: "registration-inactive precedes all lifecycle failures",
			mutateView: func(v *LedgerView) {
				v.RegistrationActive = false
				v.ActiveSnapshotDigest = digestOf("snapshot", "v2")
				v.PolicyActive = false
				v.CurrentPolicyDigest = digestOf("policy", "v2")
			},
			now:  time.Unix(testValidUntilUnix, 0).UTC(),
			want: RejectionReasonRegistrationInactive,
		},
		{
			name: "snapshot-superseded precedes policy failures and expiry",
			mutateView: func(v *LedgerView) {
				v.ActiveSnapshotDigest = digestOf("snapshot", "v2")
				v.PolicyActive = false
				v.CurrentPolicyDigest = digestOf("policy", "v2")
			},
			now:  time.Unix(testValidUntilUnix, 0).UTC(),
			want: RejectionReasonSnapshotSuperseded,
		},
		{
			name: "policy-inactive precedes policy-rotated and expiry",
			mutateView: func(v *LedgerView) {
				v.PolicyActive = false
				v.CurrentPolicyDigest = digestOf("policy", "v2")
			},
			now:  time.Unix(testValidUntilUnix, 0).UTC(),
			want: RejectionReasonPolicyInactive,
		},
		{
			name:       "policy-rotated precedes expiry",
			mutateView: func(v *LedgerView) { v.CurrentPolicyDigest = digestOf("policy", "v2") },
			now:        time.Unix(testValidUntilUnix, 0).UTC(),
			want:       RejectionReasonPolicyRotated,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := mustToken(t)
			view := passingView(token)
			tt.mutateView(&view)
			verdict, err := VerifyBeforeProvision(token, view, tt.now)
			if err != nil {
				t.Fatalf("business rejection must not leak into error, got %v", err)
			}
			if verdict == nil || verdict.OK || verdict.Reason != tt.want {
				t.Errorf("reason = %+v, want %q", verdict, tt.want)
			}
		})
	}

	// 结构性问题先于一切业务判定：view 与 token 同时失效时 error 通道生效。
	t.Run("structural problems precede verdicts", func(t *testing.T) {
		token := mustToken(t)
		view := passingView(token)
		view.RegistrationActive = false // 本会是 registration-inactive
		view.CurrentPolicyDigest = ""   // 但 view 形态非法
		verdict, err := VerifyBeforeProvision(token, view, testNow())
		if verdict != nil {
			t.Errorf("structural problem must not leak into Verdict, got %+v", verdict)
		}
		assertJitgateError(t, err, ErrMalformedView)
	})
}
