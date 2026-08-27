package attemptgate

import (
	"errors"
	"fmt"
	"strings"

	"github.com/chiga0/marshal-harness/internal/agentregistry"
	"github.com/chiga0/marshal-harness/internal/bindingcheck"
	"github.com/chiga0/marshal-harness/internal/runtimeprofile"
)

var (
	// ErrNilDependency 拒绝构造时的 nil 依赖（fail closed）。
	ErrNilDependency = errors.New("nil dependency")
	// ErrMalformedEvidenceDigest 拒绝形态非法的 evidence digest。
	ErrMalformedEvidenceDigest = errors.New("malformed evidence digest")
)

// EvidenceRejectReason 是证据侧拒绝标签的封闭枚举。
type EvidenceRejectReason string

const (
	// EvidenceRejectReasonEmptySnapshot 表示 Agent 无 active snapshot，无法承载证据集合。
	EvidenceRejectReasonEmptySnapshot EvidenceRejectReason = "evidence-snapshot-inactive"
	// EvidenceRejectReasonNotBound 表示出示的 evidence digest 不在 active
	// snapshot 的封闭证据集合内（含跨 Port 出示与伪造 digest）。
	EvidenceRejectReasonNotBound EvidenceRejectReason = "evidence-not-bound"
)

// Decision 是一次结果接纳的确定性结论：双侧 recheck 结果、证据侧结论与
// 最终接纳标记。Decision 只描述接纳判定，不携带任何业务语义信任。
type Decision struct {
	AttemptID      string
	ProfileDigest  string
	Agent          bindingcheck.SideResult
	Sandbox        bindingcheck.SideResult
	EvidenceOK     bool
	EvidenceReason EvidenceRejectReason // 仅当 EvidenceOK == false 时有值
	Accepted       bool
}

// Gate 是结果接纳门禁：从 Attempt 解析 immutable profile，分别对
// AgentBinding 与 SandboxBinding 做 current-ledger recheck，并校验出示
// 证据属于该 Agent 当前 active snapshot 的封闭集合。任一步失败 fail
// closed；两侧始终独立完整评估，互不短路。
type Gate struct {
	store    *AttemptProfileStore
	checker  *bindingcheck.Checker
	registry *agentregistry.Registry
}

// NewGate 构造 Gate；任何 nil 依赖 fail closed。
func NewGate(store *AttemptProfileStore, checker *bindingcheck.Checker, registry *agentregistry.Registry) (*Gate, error) {
	if store == nil {
		return nil, errors.New("attemptgate: AttemptProfileStore must not be nil")
	}
	if checker == nil {
		return nil, fmt.Errorf("attemptgate: %w: bindingcheck.Checker", ErrNilDependency)
	}
	if registry == nil {
		return nil, fmt.Errorf("attemptgate: %w: agentregistry.Registry", ErrNilDependency)
	}
	return &Gate{store: store, checker: checker, registry: registry}, nil
}

// AdmitAttemptResult 对一次结果接纳做完整门禁判定：
//
//  1. attemptID 与 presentedEvidenceDigest 形态校验（fail closed）；
//  2. 从 AttemptProfileStore 解析 immutable profile（未知 Attempt 拒绝）；
//  3. bindingcheck.Checker 对 AgentBinding/SandboxBinding 分别做
//     current-ledger recheck（两侧独立完整评估）；
//  4. 证据侧：presentedEvidenceDigest 必须属于该 Agent 当前 active
//     snapshot 的 ConformanceEvidenceDigests 封闭集合；无 active
//     snapshot 或不在集合内均拒绝（跨 Port 出示、跨 registration 借用、
//     伪造 digest 走同一 fail-closed 路径）。
//
// 返回的 Decision 无条件携带双侧结果与证据侧结论；error 只用于结构性
// 拒绝（未知 Attempt、形态非法），业务拒绝一律经 Decision 表达。
func (g *Gate) AdmitAttemptResult(attemptID string, presentedEvidenceDigest string) (Decision, error) {
	if strings.TrimSpace(attemptID) == "" {
		return Decision{}, fmt.Errorf("attemptgate: %w: must not be empty", ErrInvalidAttempt)
	}
	if err := requireDigest("presentedEvidenceDigest", presentedEvidenceDigest); err != nil {
		return Decision{}, fmt.Errorf("attemptgate: %w: %v", ErrMalformedEvidenceDigest, err)
	}

	profile, err := g.store.Resolve(attemptID)
	if err != nil {
		return Decision{}, err
	}

	recheck, err := g.checker.Recheck(profile)
	if err != nil {
		return Decision{}, fmt.Errorf("attemptgate: recheck failed: %w", err)
	}

	evidenceOK, evidenceReason := g.recheckEvidence(profile.Agent, presentedEvidenceDigest)

	return Decision{
		AttemptID:      attemptID,
		ProfileDigest:  profile.ProfileDigest,
		Agent:          recheck.Agent,
		Sandbox:        recheck.Sandbox,
		EvidenceOK:     evidenceOK,
		EvidenceReason: evidenceReason,
		Accepted:       recheck.Accepted() && evidenceOK,
	}, nil
}

// recheckEvidence 校验证据 digest 属于 Agent 当前 active snapshot 的封闭
// 集合。Agent registration 失效时 ActiveSnapshot 仍可解析（集合随之冻结），
// 但 registration-inactive 已由 binding 侧拒绝；这里只判定集合归属。
func (g *Gate) recheckEvidence(agent runtimeprofile.AgentBinding, presented string) (bool, EvidenceRejectReason) {
	snap, err := g.registry.ActiveSnapshot(agent.RegistrationID)
	if err != nil {
		return false, EvidenceRejectReasonEmptySnapshot
	}
	for _, digest := range snap.ConformanceEvidenceDigests {
		if digest == presented {
			return true, ""
		}
	}
	return false, EvidenceRejectReasonNotBound
}

// ── helpers ─────────────────────────────────────────────────────────────────

func requireDigest(field, value string) error {
	const prefix = "sha256:"
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	if !strings.HasPrefix(value, prefix) {
		return fmt.Errorf("%s must carry the sha256: prefix", field)
	}
	hexPart := strings.TrimPrefix(value, prefix)
	if len(hexPart) != 64 {
		return fmt.Errorf("%s must be a 64-character sha256 hex digest", field)
	}
	for _, c := range hexPart {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("%s must be lowercase hex", field)
		}
	}
	return nil
}
