package perfbench

import (
	"slices"
	"time"
)

// 冻结的五条 SLO 指标名。Observation.Name 必须取其中之一，未知名在
// CheckThresholds 中被忽略（判定逻辑见 CheckThresholds 注释）。
const (
	// MetricRecheckP99 对应 bindingcheck.Checker.Recheck 单调用延迟。
	MetricRecheckP99 = "bindingcheck.Checker.Recheck"
	// MetricAdmissionP99 对应 attemptgate.Gate.AdmitAttemptResult 单调用延迟。
	MetricAdmissionP99 = "attemptgate.Gate.AdmitAttemptResult"
	// MetricJitP99 对应 jitgate.VerifyBeforeProvision 单调用延迟。
	MetricJitP99 = "jitgate.VerifyBeforeProvision"
	// MetricIngressP99 对应 resultingress.Ingress.Admit（cold worker-result
	// 路径）单调用延迟。
	MetricIngressP99 = "resultingress.Ingress.Admit"
	// MetricEffectP99 对应 effectsink.ExecuteIfAdmitted 单调用延迟。
	MetricEffectP99 = "effectsink.ExecuteIfAdmitted"
)

// Thresholds 是 v1.0 SLO 基线的五档 p99 上限，单位微秒。字段与五条冻结
// 指标名一一对应。
type Thresholds struct {
	AdmissionP99Micros  int64
	EffectGateP99Micros int64
	JitP99Micros        int64
	RecheckP99Micros    int64
	IngressP99Micros    int64
}

// DefaultThresholds 返回冻结的 v1.0 SLO 值：五条指标 p99 一律 5000 微秒
// （5ms）。这是对内存内确定性域调用的宽松上限；数值出处（pending R6-C
// soak 校准的临时基线）见包注释 doc.go。
func DefaultThresholds() Thresholds {
	return Thresholds{
		AdmissionP99Micros:  5000,
		EffectGateP99Micros: 5000,
		JitP99Micros:        5000,
		RecheckP99Micros:    5000,
		IngressP99Micros:    5000,
	}
}

// Observation 是一条指标的实测 p99 观测值（微秒）。
type Observation struct {
	Name      string
	P99Micros int64
}

// Violation 是一条超限记录：指标名、实测值与阈值。Got 与 Want 均为精确值。
type Violation struct {
	Name      string
	Got, Want int64
}

// CheckThresholds 对每条 Observation 查对应阈值：P99Micros 严格大于阈值
// 即产生一条 Violation，携带精确的 Got/Want。返回 nil 或空切片表示全部
// 通过。Name 不在五条冻结指标名内的 Observation 被忽略（映射缺失不构成
// 业务判定，拼写约束由本包常量与基准测试侧的断言共同守护）。纯函数，
// 无时钟、无 I/O。
func CheckThresholds(th Thresholds, obs []Observation) []Violation {
	var violations []Violation
	for _, o := range obs {
		want, ok := thresholdFor(th, o.Name)
		if !ok {
			continue
		}
		if o.P99Micros > want {
			violations = append(violations, Violation{Name: o.Name, Got: o.P99Micros, Want: want})
		}
	}
	return violations
}

// thresholdFor 把冻结指标名映射到阈值字段；未知名返回 ok=false。
func thresholdFor(th Thresholds, name string) (int64, bool) {
	switch name {
	case MetricAdmissionP99:
		return th.AdmissionP99Micros, true
	case MetricEffectP99:
		return th.EffectGateP99Micros, true
	case MetricJitP99:
		return th.JitP99Micros, true
	case MetricRecheckP99:
		return th.RecheckP99Micros, true
	case MetricIngressP99:
		return th.IngressP99Micros, true
	default:
		return 0, false
	}
}

// EstimateP99 对采样延迟求 p99（微秒）。规则冻结：先复制并升序排序
// （不改动调用方切片），再取索引 ceil(0.99*n)-1（最近秩法；索引以整数
// 推导 rank=(99*n+99)/100 计算，无浮点误差）。n=1 时取唯一样本；
// n=100 时取第 99 小（索引 98）。空输入返回 (0, false)。
func EstimateP99(samples []time.Duration) (int64, bool) {
	n := len(samples)
	if n == 0 {
		return 0, false
	}
	sorted := make([]time.Duration, n)
	copy(sorted, samples)
	slices.Sort(sorted)
	rank := (99*n + 99) / 100 // ceil(0.99*n)，整数除法
	return sorted[rank-1].Microseconds(), true
}

// EstimateP99Micros 是 EstimateP99 的单返回值形式：空输入返回 0。
// 需要区分"空输入"与"p99 恰为 0 微秒"的调用方应使用 EstimateP99。
func EstimateP99Micros(samples []time.Duration) int64 {
	p99, _ := EstimateP99(samples)
	return p99
}
