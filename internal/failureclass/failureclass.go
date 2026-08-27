package failureclass

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrInvalidObservedPeak 拒绝负数 ObservedPeakBytes（fail closed）。
	ErrInvalidObservedPeak = errors.New("invalid observed-peak")
	// ErrUnknownTermination 拒绝封闭枚举外的 TerminationReason。
	ErrUnknownTermination = errors.New("unknown termination reason")
	// ErrUnknownSource 拒绝封闭枚举外的 ObservationSource。
	ErrUnknownSource = errors.New("unknown observation source")
	// ErrMalformedObservationDigest 拒绝形态非法或缺失的 ObservationDigest；
	// malformed digest 永远是硬错误，绝不静默降级为更保守的分类。
	ErrMalformedObservationDigest = errors.New("malformed observation digest")
)

// ObservationSource 是观察来源的封闭枚举：infra-failure 分类权只允许来自
// workload/Provider 故障域外的观察；Provider 自报只能诊断或收紧。
type ObservationSource string

const (
	// ObservationSourceAuthorityObserved 表示来自 workload/Provider 故障域外
	// 的带外观察（如 supervisor / kernel held handle）。
	ObservationSourceAuthorityObserved ObservationSource = "authority-observed"
	// ObservationSourceProviderAsserted 表示 Provider 自报。
	ObservationSourceProviderAsserted ObservationSource = "provider-asserted"
)

// Valid 报告来源是否属于封闭枚举；未知来源 fail closed。
func (s ObservationSource) Valid() bool {
	switch s {
	case ObservationSourceAuthorityObserved, ObservationSourceProviderAsserted:
		return true
	default:
		return false
	}
}

// TerminationReason 是 workload 终止原因的封闭枚举。
type TerminationReason string

const (
	// TerminationCompleted 表示正常完成。
	TerminationCompleted TerminationReason = "termination:completed"
	// TerminationExitNonZero 表示非零退出（semantic failure）。
	TerminationExitNonZero TerminationReason = "termination:exit-nonzero"
	// TerminationOOMKilled 表示被 OOM killer 终止（infra 候选）。
	TerminationOOMKilled TerminationReason = "termination:oom-killed"
	// TerminationTimeLimit 表示超出时限（infra 候选）。
	TerminationTimeLimit TerminationReason = "termination:time-limit"
	// TerminationSignalKilled 表示被外部信号终止（含 SIGKILL，infra 候选）。
	TerminationSignalKilled TerminationReason = "termination:signal-killed"
	// TerminationIOError 表示 I/O 错误终止（infra 候选）。
	TerminationIOError TerminationReason = "termination:io-error"
	// TerminationNetworkUnreachable 表示网络不可达终止（infra 候选）。
	TerminationNetworkUnreachable TerminationReason = "termination:network-unreachable"
	// TerminationUnknown 表示终止原因未知（永不放宽，按冻结规则归为
	// failure:semantic）。
	TerminationUnknown TerminationReason = "termination:unknown"
)

// Valid 报告终止原因是否属于封闭枚举；未知值 fail closed。
func (r TerminationReason) Valid() bool {
	switch r {
	case TerminationCompleted,
		TerminationExitNonZero,
		TerminationOOMKilled,
		TerminationTimeLimit,
		TerminationSignalKilled,
		TerminationIOError,
		TerminationNetworkUnreachable,
		TerminationUnknown:
		return true
	default:
		return false
	}
}

// infraCandidate 报告该终止原因是否属于 infra 候选（仅在
// authority-observed 来源下可分类为 failure:infra）。
func (r TerminationReason) infraCandidate() bool {
	switch r {
	case TerminationOOMKilled,
		TerminationTimeLimit,
		TerminationSignalKilled,
		TerminationIOError,
		TerminationNetworkUnreachable:
		return true
	default:
		return false
	}
}

// FailureClass 是故障分类结论的封闭枚举。
type FailureClass string

const (
	// FailureClassCompleted 表示正常完成，无故障。
	FailureClassCompleted FailureClass = "failure:completed"
	// FailureClassSemantic 表示 semantic failure，必须走 rework，不可豁免。
	FailureClassSemantic FailureClass = "failure:semantic"
	// FailureClassInfra 表示 authority-observed 的 infra failure，可放宽
	// retry/预算并豁免 semantic rework。
	FailureClassInfra FailureClass = "failure:infra"
	// FailureClassProviderClaimedInfra 表示 Provider 自报的 infra failure，
	// 仅诊断用，不得放宽 retry/预算，不得豁免 semantic rework。
	FailureClassProviderClaimedInfra FailureClass = "failure:provider-claimed-infra"
)

// ResourceEnvelope 是一次 workload 终止的资源观察信封。ObservedPeakBytes、
// Termination、Source 与 ObservationDigest 共同冻结 infra-failure 分类权的
// 输入面。
type ResourceEnvelope struct {
	// ObservedPeakBytes 是观察到的资源峰值（字节），必须 >= 0。
	ObservedPeakBytes int64
	// Termination 是终止原因（封闭枚举）。
	Termination TerminationReason
	// Source 是观察来源（封闭枚举）。
	Source ObservationSource
	// ObservationDigest 是带外观察记录的 digest，必须为
	// "sha256:" + 64 位小写 hex。
	ObservationDigest string
}

// Validate 对信封做 fail-closed 校验；任何违例返回带 "failureclass: "
// 前缀的类型化错误。
func (e ResourceEnvelope) Validate() error {
	if e.ObservedPeakBytes < 0 {
		return fmt.Errorf("failureclass: %w: ObservedPeakBytes must be >= 0, got %d", ErrInvalidObservedPeak, e.ObservedPeakBytes)
	}
	if !e.Termination.Valid() {
		return fmt.Errorf("failureclass: %w: %q", ErrUnknownTermination, string(e.Termination))
	}
	if !e.Source.Valid() {
		return fmt.Errorf("failureclass: %w: %q", ErrUnknownSource, string(e.Source))
	}
	if err := validateObservationDigest(e.ObservationDigest); err != nil {
		return fmt.Errorf("failureclass: %w: %v", ErrMalformedObservationDigest, err)
	}
	return nil
}

// Classification 是一次故障分类的确定性结论。ObservationDigest 回显输入
// 信封的 digest，使决策始终绑定到具体的观察记录。
type Classification struct {
	// Class 是分类结论（封闭枚举）。
	Class FailureClass
	// Source 回显输入信封的观察来源。
	Source ObservationSource
	// ObservationDigest 回显输入信封的 digest（决策绑定到观察）。
	ObservationDigest string
	// MayRelaxBudget 表示该分类允许 R4 恢复决策放宽 retry/预算。
	MayRelaxBudget bool
	// MayExemptSemanticRework 表示该分类允许 R4 恢复决策豁免 semantic
	// rework。
	MayExemptSemanticRework bool
}

// Classifier 是无状态确定性分类器。分类权的方向性合同全部冻结在
// Classify 内，不依赖任何时钟与外部状态。
type Classifier struct{}

// NewClassifier 构造 Classifier；无依赖，不可能失败。
func NewClassifier() *Classifier {
	return &Classifier{}
}

// Classify 按冻结的方向性合同分类：
//
//   - 信封校验失败 → 带 "failureclass: " 前缀的类型化错误；
//   - termination:completed → failure:completed（来源无关，放宽标志 false）；
//   - termination:exit-nonzero → failure:semantic（无论来源：authority
//     观察也不能把 semantic failure 洗白成 infra，放宽标志 false）；
//   - infra 候选（oom-killed / time-limit / signal-killed / io-error /
//     network-unreachable）：
//   - authority-observed 且 digest 合法（由 Validate 保证）→
//     failure:infra，MayRelaxBudget=true，MayExemptSemanticRework=true；
//   - provider-asserted → failure:provider-claimed-infra，放宽标志恒
//     false（Provider 声明只能诊断或收紧）；
//   - termination:unknown → failure:semantic，放宽标志 false。冻结规则：
//     unknown 永不放宽，一律按最保守的 semantic 处理（无论来源）。
func (c *Classifier) Classify(env ResourceEnvelope) (Classification, error) {
	if err := env.Validate(); err != nil {
		return Classification{}, err
	}

	result := Classification{
		Source:            env.Source,
		ObservationDigest: env.ObservationDigest,
	}

	switch {
	case env.Termination == TerminationCompleted:
		result.Class = FailureClassCompleted
	case env.Termination == TerminationExitNonZero:
		result.Class = FailureClassSemantic
	case env.Termination.infraCandidate():
		if env.Source == ObservationSourceAuthorityObserved {
			result.Class = FailureClassInfra
			result.MayRelaxBudget = true
			result.MayExemptSemanticRework = true
		} else {
			result.Class = FailureClassProviderClaimedInfra
		}
	default:
		// 冻结规则：termination:unknown 归为 failure:semantic，放宽标志
		// 恒 false——unknown 永不放宽 retry/预算，也永不豁免 semantic
		// rework（无论来源）。
		result.Class = FailureClassSemantic
	}

	return result, nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

func validateObservationDigest(value string) error {
	const prefix = "sha256:"
	if strings.TrimSpace(value) == "" {
		return errors.New("ObservationDigest must not be empty")
	}
	if !strings.HasPrefix(value, prefix) {
		return errors.New("ObservationDigest must carry the sha256: prefix")
	}
	hexPart := strings.TrimPrefix(value, prefix)
	if len(hexPart) != 64 {
		return errors.New("ObservationDigest must be a 64-character sha256 hex digest")
	}
	for _, c := range hexPart {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return errors.New("ObservationDigest must be lowercase hex")
		}
	}
	return nil
}
