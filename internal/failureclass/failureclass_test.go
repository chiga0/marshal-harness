package failureclass

import (
	"errors"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

func digestOf(parts ...string) string {
	return canonical.DigestBytes([]byte(strings.Join(parts, "|")))
}

const testObservationSeed = "out-of-band-observation"

func testObservationDigest() string { return digestOf(testObservationSeed, "v1") }

func mustEnv(t *testing.T, termination TerminationReason, source ObservationSource) ResourceEnvelope {
	t.Helper()
	env := ResourceEnvelope{
		ObservedPeakBytes: 1024,
		Termination:       termination,
		Source:            source,
		ObservationDigest: testObservationDigest(),
	}
	if err := env.Validate(); err != nil {
		t.Fatalf("fixture envelope must validate: %v", err)
	}
	return env
}

// ── 决策表（方向性合同）────────────────────────────────────────────────────

func TestClassify_DecisionTable(t *testing.T) {
	classifier := NewClassifier()

	infraCandidates := []TerminationReason{
		TerminationOOMKilled,
		TerminationTimeLimit,
		TerminationSignalKilled,
		TerminationIOError,
		TerminationNetworkUnreachable,
	}

	type want struct {
		class                   FailureClass
		mayRelaxBudget          bool
		mayExemptSemanticRework bool
	}

	cases := []struct {
		name        string
		termination TerminationReason
		source      ObservationSource
		want        want
	}{
		{
			name:        "completed / authority-observed",
			termination: TerminationCompleted,
			source:      ObservationSourceAuthorityObserved,
			want:        want{FailureClassCompleted, false, false},
		},
		{
			name:        "completed / provider-asserted",
			termination: TerminationCompleted,
			source:      ObservationSourceProviderAsserted,
			want:        want{FailureClassCompleted, false, false},
		},
		{
			name:        "exit-nonzero / authority-observed",
			termination: TerminationExitNonZero,
			source:      ObservationSourceAuthorityObserved,
			want:        want{FailureClassSemantic, false, false},
		},
		{
			name:        "exit-nonzero / provider-asserted",
			termination: TerminationExitNonZero,
			source:      ObservationSourceProviderAsserted,
			want:        want{FailureClassSemantic, false, false},
		},
		{
			name:        "unknown / authority-observed",
			termination: TerminationUnknown,
			source:      ObservationSourceAuthorityObserved,
			want:        want{FailureClassSemantic, false, false},
		},
		{
			name:        "unknown / provider-asserted",
			termination: TerminationUnknown,
			source:      ObservationSourceProviderAsserted,
			want:        want{FailureClassSemantic, false, false},
		},
	}
	for _, term := range infraCandidates {
		cases = append(cases,
			struct {
				name        string
				termination TerminationReason
				source      ObservationSource
				want        want
			}{
				name:        string(term) + " / authority-observed",
				termination: term,
				source:      ObservationSourceAuthorityObserved,
				want:        want{FailureClassInfra, true, true},
			},
			struct {
				name        string
				termination TerminationReason
				source      ObservationSource
				want        want
			}{
				name:        string(term) + " / provider-asserted",
				termination: term,
				source:      ObservationSourceProviderAsserted,
				want:        want{FailureClassProviderClaimedInfra, false, false},
			},
		)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := classifier.Classify(mustEnv(t, tc.termination, tc.source))
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			if got.Class != tc.want.class {
				t.Errorf("Class = %q, want %q", got.Class, tc.want.class)
			}
			if got.MayRelaxBudget != tc.want.mayRelaxBudget {
				t.Errorf("MayRelaxBudget = %v, want %v", got.MayRelaxBudget, tc.want.mayRelaxBudget)
			}
			if got.MayExemptSemanticRework != tc.want.mayExemptSemanticRework {
				t.Errorf("MayExemptSemanticRework = %v, want %v", got.MayExemptSemanticRework, tc.want.mayExemptSemanticRework)
			}
			if got.Source != tc.source {
				t.Errorf("Source = %q, want %q", got.Source, tc.source)
			}
		})
	}
}

// ── 不变量 1：伪造 infra-failure 永不放宽 ─────────────────────────────────

// provider-asserted 的 infra 候选（任意形态合法的 digest）绝不产生放宽标志。
func TestClassify_ForgedInfraNeverRelaxes(t *testing.T) {
	classifier := NewClassifier()
	infraCandidates := []TerminationReason{
		TerminationOOMKilled,
		TerminationTimeLimit,
		TerminationSignalKilled,
		TerminationIOError,
		TerminationNetworkUnreachable,
	}
	digests := []string{
		testObservationDigest(),
		digestOf("forged", "attempt-1"),
		digestOf("forged", "attempt-2"),
	}
	for _, term := range infraCandidates {
		for _, d := range digests {
			env := ResourceEnvelope{
				ObservedPeakBytes: 1,
				Termination:       term,
				Source:            ObservationSourceProviderAsserted,
				ObservationDigest: d,
			}
			got, err := classifier.Classify(env)
			if err != nil {
				t.Fatalf("Classify(%s, digest=%q): %v", term, d, err)
			}
			if got.Class != FailureClassProviderClaimedInfra {
				t.Errorf("Classify(%s, provider-asserted): Class = %q, want %q", term, got.Class, FailureClassProviderClaimedInfra)
			}
			if got.MayRelaxBudget || got.MayExemptSemanticRework {
				t.Errorf("forged infra (%s, provider-asserted) must never relax, got %+v", term, got)
			}
		}
	}
}

// ── 不变量 2：authority infra 仅在 digest 合法时放宽；非法 digest 是硬错误 ──

func TestClassify_AuthorityInfraRequiresValidDigest(t *testing.T) {
	classifier := NewClassifier()

	env := mustEnv(t, TerminationOOMKilled, ObservationSourceAuthorityObserved)
	got, err := classifier.Classify(env)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Class != FailureClassInfra || !got.MayRelaxBudget || !got.MayExemptSemanticRework {
		t.Errorf("authority-observed infra with valid digest must relax, got %+v", got)
	}

	malformed := []string{
		"",
		"   ",
		"sha256:short",
		"SHA256:" + strings.Repeat("a", 64),
		"sha256:" + strings.Repeat("A", 64), // 非小写
		"sha256:" + strings.Repeat("g", 64), // 非 hex
		"sha256:" + strings.Repeat("a", 63),
		"sha256:" + strings.Repeat("a", 65),
		"sha256: " + strings.Repeat("a", 64),
	}
	for _, d := range malformed {
		bad := env
		bad.ObservationDigest = d
		_, err := classifier.Classify(bad)
		if !errors.Is(err, ErrMalformedObservationDigest) {
			t.Errorf("digest %q: expected ErrMalformedObservationDigest, got %v", d, err)
			continue
		}
		if !strings.HasPrefix(err.Error(), "failureclass: ") {
			t.Errorf("digest %q: error %q missing failureclass: prefix", d, err.Error())
		}
	}
}

// ── 不变量 3：semantic 保持 semantic，authority 不可洗白 ────────────────────

func TestClassify_SemanticStaysSemanticWithAuthority(t *testing.T) {
	classifier := NewClassifier()
	got, err := classifier.Classify(mustEnv(t, TerminationExitNonZero, ObservationSourceAuthorityObserved))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Class != FailureClassSemantic {
		t.Errorf("authority-observed exit-nonzero must stay %q, got %q", FailureClassSemantic, got.Class)
	}
	if got.MayRelaxBudget || got.MayExemptSemanticRework {
		t.Errorf("semantic failure must never relax even with authority source, got %+v", got)
	}
}

// ── 不变量 4：completed 永不放宽 ───────────────────────────────────────────

func TestClassify_CompletedNeverRelaxes(t *testing.T) {
	classifier := NewClassifier()
	for _, source := range []ObservationSource{ObservationSourceAuthorityObserved, ObservationSourceProviderAsserted} {
		got, err := classifier.Classify(mustEnv(t, TerminationCompleted, source))
		if err != nil {
			t.Fatalf("Classify(%s): %v", source, err)
		}
		if got.Class != FailureClassCompleted {
			t.Errorf("Class = %q, want %q", got.Class, FailureClassCompleted)
		}
		if got.MayRelaxBudget || got.MayExemptSemanticRework {
			t.Errorf("completed must never relax (source %s), got %+v", source, got)
		}
	}
}

// ── 不变量 5：信封校验 fail closed ─────────────────────────────────────────

func TestValidate_FailClosed(t *testing.T) {
	valid := mustEnv(t, TerminationOOMKilled, ObservationSourceAuthorityObserved)

	cases := []struct {
		name    string
		mutate  func(*ResourceEnvelope)
		wantErr error
	}{
		{
			name:    "negative observed peak",
			mutate:  func(e *ResourceEnvelope) { e.ObservedPeakBytes = -1 },
			wantErr: ErrInvalidObservedPeak,
		},
		{
			name:    "unknown termination",
			mutate:  func(e *ResourceEnvelope) { e.Termination = TerminationReason("termination:disk-full") },
			wantErr: ErrUnknownTermination,
		},
		{
			name:    "empty termination",
			mutate:  func(e *ResourceEnvelope) { e.Termination = "" },
			wantErr: ErrUnknownTermination,
		},
		{
			name:    "unknown source",
			mutate:  func(e *ResourceEnvelope) { e.Source = ObservationSource("kernel-asserted") },
			wantErr: ErrUnknownSource,
		},
		{
			name:    "empty source",
			mutate:  func(e *ResourceEnvelope) { e.Source = "" },
			wantErr: ErrUnknownSource,
		},
		{
			name:    "empty digest",
			mutate:  func(e *ResourceEnvelope) { e.ObservationDigest = "" },
			wantErr: ErrMalformedObservationDigest,
		},
		{
			name:    "missing sha256 prefix",
			mutate:  func(e *ResourceEnvelope) { e.ObservationDigest = strings.Repeat("a", 64) },
			wantErr: ErrMalformedObservationDigest,
		},
		{
			name:    "uppercase hex digest",
			mutate:  func(e *ResourceEnvelope) { e.ObservationDigest = "sha256:" + strings.Repeat("A", 64) },
			wantErr: ErrMalformedObservationDigest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := valid
			tc.mutate(&env)
			err := env.Validate()
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
			if !strings.HasPrefix(err.Error(), "failureclass: ") {
				t.Errorf("error %q missing failureclass: prefix", err.Error())
			}

			// Classify 必须经过同一 fail-closed 校验。
			if _, cErr := NewClassifier().Classify(env); !errors.Is(cErr, tc.wantErr) {
				t.Errorf("Classify: expected %v, got %v", tc.wantErr, cErr)
			}
		})
	}
}

func TestValidate_Positive(t *testing.T) {
	env := mustEnv(t, TerminationOOMKilled, ObservationSourceAuthorityObserved)
	if err := env.Validate(); err != nil {
		t.Fatalf("valid envelope must pass, got %v", err)
	}
	env.ObservedPeakBytes = 0 // 零峰值合法
	if err := env.Validate(); err != nil {
		t.Fatalf("zero peak must pass, got %v", err)
	}
}

// ── 不变量 6：digest 回显（决策绑定到观察）──────────────────────────────────

func TestClassify_DigestEcho(t *testing.T) {
	classifier := NewClassifier()
	digests := []string{testObservationDigest(), digestOf("observation", "other")}
	terminations := []TerminationReason{
		TerminationCompleted,
		TerminationExitNonZero,
		TerminationOOMKilled,
		TerminationUnknown,
	}
	for _, d := range digests {
		for _, term := range terminations {
			for _, source := range []ObservationSource{ObservationSourceAuthorityObserved, ObservationSourceProviderAsserted} {
				env := ResourceEnvelope{
					ObservedPeakBytes: 42,
					Termination:       term,
					Source:            source,
					ObservationDigest: d,
				}
				got, err := classifier.Classify(env)
				if err != nil {
					t.Fatalf("Classify(%s, %s): %v", term, source, err)
				}
				if got.ObservationDigest != d {
					t.Errorf("ObservationDigest echo = %q, want %q (term %s, source %s)", got.ObservationDigest, d, term, source)
				}
			}
		}
	}
}

// ── 不变量 7：Provider 声明只能诊断或收紧 ───────────────────────────────────

func TestClassify_ProviderTighteningDirection(t *testing.T) {
	classifier := NewClassifier()

	semantic, err := classifier.Classify(mustEnv(t, TerminationExitNonZero, ObservationSourceProviderAsserted))
	if err != nil {
		t.Fatalf("Classify(exit-nonzero): %v", err)
	}
	if semantic.Class != FailureClassSemantic {
		t.Errorf("provider-asserted exit-nonzero: Class = %q, want %q", semantic.Class, FailureClassSemantic)
	}
	if semantic.MayRelaxBudget {
		t.Errorf("provider-asserted exit-nonzero: MayRelaxBudget must be false")
	}
	if semantic.MayExemptSemanticRework {
		t.Errorf("provider-asserted exit-nonzero: MayExemptSemanticRework must be false")
	}

	infra, err := classifier.Classify(mustEnv(t, TerminationOOMKilled, ObservationSourceProviderAsserted))
	if err != nil {
		t.Fatalf("Classify(oom-killed): %v", err)
	}
	if infra.Class != FailureClassProviderClaimedInfra {
		t.Errorf("provider-asserted oom-killed: Class = %q, want %q", infra.Class, FailureClassProviderClaimedInfra)
	}
	if infra.MayRelaxBudget {
		t.Errorf("provider-asserted oom-killed: MayRelaxBudget must be false")
	}
	if infra.MayExemptSemanticRework {
		t.Errorf("provider-asserted oom-killed: MayExemptSemanticRework must be false")
	}
}
