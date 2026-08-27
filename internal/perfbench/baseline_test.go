package perfbench

import (
	"testing"
	"time"
)

// ── EstimateP99 / EstimateP99Micros ──────────────────────────────────────────

func TestEstimateP99_EmptyInput(t *testing.T) {
	if got, ok := EstimateP99(nil); ok || got != 0 {
		t.Errorf("EstimateP99(nil) = (%d, %v), want (0, false)", got, ok)
	}
	if got, ok := EstimateP99([]time.Duration{}); ok || got != 0 {
		t.Errorf("EstimateP99(empty) = (%d, %v), want (0, false)", got, ok)
	}
	if got := EstimateP99Micros(nil); got != 0 {
		t.Errorf("EstimateP99Micros(nil) = %d, want 0", got)
	}
}

func TestEstimateP99_SingleSample(t *testing.T) {
	got, ok := EstimateP99([]time.Duration{42 * time.Microsecond})
	if !ok {
		t.Fatal("EstimateP99 single sample must return ok=true")
	}
	if got != 42 {
		t.Errorf("EstimateP99([42µs]) = %d, want 42", got)
	}
}

func TestEstimateP99_N100Boundary(t *testing.T) {
	// 1µs..100µs 升序共 100 个样本：rank = ceil(0.99*100) = 99，取第 99 小
	// （索引 98），即 99µs——而非最大值 100µs。
	samples := make([]time.Duration, 100)
	for i := range samples {
		samples[i] = time.Duration(i+1) * time.Microsecond
	}
	got, ok := EstimateP99(samples)
	if !ok {
		t.Fatal("EstimateP99 must return ok=true for n=100")
	}
	if got != 99 {
		t.Errorf("EstimateP99(n=100) = %d, want 99 (index 98, nearest-rank)", got)
	}
}

func TestEstimateP99_SortsCopyAndDoesNotMutateInput(t *testing.T) {
	samples := []time.Duration{300 * time.Microsecond, 100 * time.Microsecond, 200 * time.Microsecond}
	got, ok := EstimateP99(samples)
	if !ok {
		t.Fatal("EstimateP99 must return ok=true")
	}
	// n=3：rank = ceil(2.97) = 3，取最大值 300。
	if got != 300 {
		t.Errorf("EstimateP99([300,100,200]µs) = %d, want 300", got)
	}
	if samples[0] != 300*time.Microsecond || samples[1] != 100*time.Microsecond || samples[2] != 200*time.Microsecond {
		t.Errorf("EstimateP99 must not mutate input order, got %v", samples)
	}
}

func TestEstimateP99_SubMicrosecondTruncation(t *testing.T) {
	// 亚微秒延迟截断为 0 微秒：记录为冻结行为，thousands-ns 级实现远低于
	// 任何阈值截断不影响判定。
	got, ok := EstimateP99([]time.Duration{500 * time.Nanosecond})
	if !ok {
		t.Fatal("EstimateP99 must return ok=true")
	}
	if got != 0 {
		t.Errorf("EstimateP99([500ns]) = %d, want 0 (truncation to µs)", got)
	}
}

func TestEstimateP99Micros_Delegates(t *testing.T) {
	samples := make([]time.Duration, 100)
	for i := range samples {
		samples[i] = time.Duration(i+1) * time.Microsecond
	}
	a, ok := EstimateP99(samples)
	b := EstimateP99Micros(samples)
	if !ok || a != b {
		t.Errorf("EstimateP99Micros = %d must equal EstimateP99 = (%d, %v)", b, a, ok)
	}
}

// ── DefaultThresholds ────────────────────────────────────────────────────────

func TestDefaultThresholds_FrozenValues(t *testing.T) {
	th := DefaultThresholds()
	want := Thresholds{
		AdmissionP99Micros:  5000,
		EffectGateP99Micros: 5000,
		JitP99Micros:        5000,
		RecheckP99Micros:    5000,
		IngressP99Micros:    5000,
	}
	if th != want {
		t.Errorf("DefaultThresholds() = %+v, want %+v (v1.0 frozen 5000µs)", th, want)
	}
}

// ── CheckThresholds ──────────────────────────────────────────────────────────

func TestCheckThresholds_AllPass(t *testing.T) {
	obs := []Observation{
		{Name: MetricRecheckP99, P99Micros: 100},
		{Name: MetricAdmissionP99, P99Micros: 4999},
		{Name: MetricJitP99, P99Micros: 5000}, // 等于阈值：通过（严格大于才违规）
		{Name: MetricIngressP99, P99Micros: 0},
		{Name: MetricEffectP99, P99Micros: 1},
	}
	if got := CheckThresholds(DefaultThresholds(), obs); len(got) != 0 {
		t.Errorf("expected no violations, got %+v", got)
	}
}

func TestCheckThresholds_ViolationsCarryExactValues(t *testing.T) {
	obs := []Observation{
		{Name: MetricRecheckP99, P99Micros: 5001},
		{Name: MetricJitP99, P99Micros: 9000},
		{Name: MetricIngressP99, P99Micros: 5000}, // 边界值，不违规
	}
	got := CheckThresholds(DefaultThresholds(), obs)
	if len(got) != 2 {
		t.Fatalf("expected 2 violations, got %+v", got)
	}
	want0 := Violation{Name: MetricRecheckP99, Got: 5001, Want: 5000}
	want1 := Violation{Name: MetricJitP99, Got: 9000, Want: 5000}
	if got[0] != want0 {
		t.Errorf("violation[0] = %+v, want %+v", got[0], want0)
	}
	if got[1] != want1 {
		t.Errorf("violation[1] = %+v, want %+v", got[1], want1)
	}
}

func TestCheckThresholds_UnknownNamesIgnored(t *testing.T) {
	obs := []Observation{
		{Name: "no.such.Metric", P99Micros: 1 << 60},
	}
	if got := CheckThresholds(DefaultThresholds(), obs); len(got) != 0 {
		t.Errorf("unknown metric names must be ignored, got %+v", got)
	}
}

func TestCheckThresholds_EmptyObservations(t *testing.T) {
	if got := CheckThresholds(DefaultThresholds(), nil); len(got) != 0 {
		t.Errorf("nil observations must yield no violations, got %+v", got)
	}
}

func TestCheckThresholds_CustomThresholds(t *testing.T) {
	th := DefaultThresholds()
	th.IngressP99Micros = 10
	obs := []Observation{
		{Name: MetricIngressP99, P99Micros: 11},
		{Name: MetricEffectP99, P99Micros: 5000},
	}
	got := CheckThresholds(th, obs)
	if len(got) != 1 {
		t.Fatalf("expected 1 violation, got %+v", got)
	}
	want := Violation{Name: MetricIngressP99, Got: 11, Want: 10}
	if got[0] != want {
		t.Errorf("violation = %+v, want %+v", got[0], want)
	}
}
