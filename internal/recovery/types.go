package recovery

import (
	"errors"
	"fmt"
	"strings"
)

// ── ObservationKind（Provider Inspect/Reconcile 归一结论，封闭枚举） ─────────

// ObservationKind 是 Provider Inspect/Reconcile 对 pending command 的归一化
// 观察结论。Provider 只回答观察，不决定业务结论；unknown/unreachable 一律
// 按「不能证明安全」处理。
type ObservationKind string

const (
	// ObservationExecuting：command 已确认仍在执行（lease 内、存活）。
	ObservationExecuting ObservationKind = "executing"
	// ObservationTerminalSuccess：command 已确认终态成功（如丢失的响应可由 Inspect 重建）。
	ObservationTerminalSuccess ObservationKind = "terminal-success"
	// ObservationTerminalFailure：command 已确认终态失败。
	ObservationTerminalFailure ObservationKind = "terminal-failure"
	// ObservationNeverReceived：command 确认从未到达执行侧。
	ObservationNeverReceived ObservationKind = "never-received"
	// ObservationUnknown：Provider 无法判定（视同不能证明安全）。
	ObservationUnknown ObservationKind = "unknown"
	// ObservationUnreachable：Provider 不可达（network partition 等）。
	ObservationUnreachable ObservationKind = "unreachable"
)

func (k ObservationKind) valid() bool {
	switch k {
	case ObservationExecuting,
		ObservationTerminalSuccess,
		ObservationTerminalFailure,
		ObservationNeverReceived,
		ObservationUnknown,
		ObservationUnreachable:
		return true
	default:
		return false
	}
}

// ── LedgerView（权威账本视图，决策唯一事实来源） ─────────────────────────────

// LeaseState 是 current lease 的封闭状态枚举。
type LeaseState string

const (
	LeaseActive   LeaseState = "active"
	LeaseExpired  LeaseState = "expired"
	LeaseRevoked  LeaseState = "revoked"
	LeaseReplaced LeaseState = "replaced"
)

func (s LeaseState) valid() bool {
	switch s {
	case LeaseActive, LeaseExpired, LeaseRevoked, LeaseReplaced:
		return true
	default:
		return false
	}
}

// LedgerView 是恢复决策消费的唯一权威事实视图。任何字段缺失 fail
// closed——恢复链从 ledger 反推，不允许从 Provider/会话内存补充事实。
type LedgerView struct {
	AttemptID string
	// PendingCommandID 是 pending/ambiguous command 的 id；无 pending
	// command 时为空（此时恢复结论只取决于账本终态）。
	PendingCommandID string
	// CommandDigest 绑定 pending command 的内容（无 pending 时可为空）。
	CommandDigest string
	Lease         LeaseState
	Generation    int64 // 当前 generation，>0
	// AttemptTerminal 表示 Attempt 在账本上已是终态（此时不允许 resume）。
	AttemptTerminal bool
	// SideEffectDeclared 表示该 command 声明了外部副作用（publication
	// intent 等）；ambiguous 情形下强制 reconcile。
	SideEffectDeclared bool
}

func (v LedgerView) validate() error {
	if strings.TrimSpace(v.AttemptID) == "" {
		return fmt.Errorf("recovery: %w: AttemptID empty", ErrMalformedInput)
	}
	if !v.Lease.valid() {
		return fmt.Errorf("recovery: %w: unknown LeaseState %q", ErrMalformedInput, string(v.Lease))
	}
	if v.Generation < 1 {
		return fmt.Errorf("recovery: %w: Generation must be positive, got %d", ErrMalformedInput, v.Generation)
	}
	if v.PendingCommandID == "" && v.CommandDigest != "" {
		return fmt.Errorf("recovery: %w: CommandDigest without PendingCommandID", ErrMalformedInput)
	}
	return nil
}

// ── BindingView / FailureClassView ──────────────────────────────────────────

// BindingView 是 bindingcheck 双侧 recheck 的归一输入。任一侧失效即整体
// 失效；两侧始终独立评估（与 bindingcheck.Checker 对齐）。
type BindingView struct {
	AgentOK   bool
	SandboxOK bool
}

func (b BindingView) ok() bool { return b.AgentOK && b.SandboxOK }

// FailureClassView 是 failureclass 分类的归一输入：只携带方向性冻结后的
// 两个放宽标志（authority-observed infra 才可能为 true）。
type FailureClassView struct {
	MayRelaxBudget          bool
	MayExemptSemanticRework bool
}

// ── RecoveryInput：一次恢复决策的全部输入 ────────────────────────────────────

// RecoveryInput 把恢复决策的所有外部事实收敛为一个纯值。Decide 是纯函数：
// 同值输入永远得到同值输出（幂等的语义基础）。
type RecoveryInput struct {
	Ledger      LedgerView
	Observation ObservationKind
	Bindings    BindingView
	Failure     FailureClassView
	// DuplicateOfAdmitted 表示本次 pending 内容与账本中已接纳 command
	// digest 相同（duplicate delivery 场景）。
	DuplicateOfAdmitted bool
	// StaleResultPresented 表示出现了旧 generation/旧 fencing 的晚到结果
	// （stale result 场景，已被 ingress 隔离；它永远不能驱动新 Attempt）。
	StaleResultPresented bool
	// PartialArtifact 表示 Candidate/Artifact bytes 无法按 digest 完整重算
	// （partial artifact 场景，产物不可证明完整）。
	PartialArtifact bool
}

func (in RecoveryInput) validate() error {
	if err := in.Ledger.validate(); err != nil {
		return err
	}
	if !in.Observation.valid() {
		return fmt.Errorf("recovery: %w: unknown ObservationKind %q", ErrMalformedInput, string(in.Observation))
	}
	return nil
}

// ── RecoveryAction / Decision ───────────────────────────────────────────────

// RecoveryAction 是恢复动作的封闭枚举。
type RecoveryAction string

const (
	// ActionResume：继续既有 Attempt（不从执行侧重放已完成内容）。
	ActionResume RecoveryAction = "resume"
	// ActionNewAttempt：新 Attempt 接管（是否需要先 fence 见 RequiresFence）。
	ActionNewAttempt RecoveryAction = "new-attempt"
)

// Decision 是一次恢复决策的唯一幂等结论。
type Decision struct {
	Action RecoveryAction
	// RequiresFence 为 true 时，新 Attempt 前必须先 cancel + generation
	// bump（无法证明在途静止时恒为 true）。
	RequiresFence bool
	// RequiresReconcile 为 true 时，新 Attempt 在与同一 effect target
	// 交互前必须先完成幂等键对账（ambiguous side effect 场景恒为 true）。
	RequiresReconcile bool
	// BudgetExempt 携带 failureclass 的放宽结论（仅 authority-observed
	// infra 可 true）；为 true 时新 Attempt 不消耗 semantic 预算。
	BudgetExempt bool
	// Rationale 是机器可读主要原因码（封闭枚举）。
	Rationale RationaleCode
}

// RationaleCode 是决策原因的封闭枚举。
type RationaleCode string

const (
	RationaleDuplicateDelivery   RationaleCode = "duplicate-delivery"
	RationaleLostResponseRebuild RationaleCode = "lost-response-rebuild"
	RationaleTerminalFailure     RationaleCode = "terminal-failure-observed"
	RationaleNeverReceived       RationaleCode = "never-received"
	RationaleStaleDismissed      RationaleCode = "stale-dismissed"
	RationaleUnsafeToProve       RationaleCode = "unsafe-to-prove"
	RationaleBindingLost         RationaleCode = "binding-lost"
	RationaleLeaseDead           RationaleCode = "lease-dead"
	RationalePartialArtifact     RationaleCode = "partial-artifact"
	RationaleAmbiguousSideEffect RationaleCode = "ambiguous-side-effect"
	RationaleAttemptAlreadyFinal RationaleCode = "attempt-already-terminal"
	RationaleExecutingResume     RationaleCode = "executing-resume"
)

// ── Explanation（explain 渲染模型） ─────────────────────────────────────────

// Explanation 是恢复决策的权威解释模型：时间线、当前 lease/bindings、
// 外部冲突、决策与下一动作。字段全部机器可读；Render 为唯一文本出口。
type Explanation struct {
	AttemptID  string
	Timeline   []string // 权威时间线条目（机器可读短句）
	LeaseState LeaseState
	Generation int64
	Bindings   BindingView
	Conflicts  []string // 外部冲突（stale/duplicate/ambiguous 等）
	Decision   Decision
	NextAction string
	BudgetNote string
}

// ── 错误 ────────────────────────────────────────────────────────────────────

var (
	// ErrMalformedInput 拒绝结构性非法输入（fail closed）。
	ErrMalformedInput = errors.New("malformed recovery input")
)
