package resultbinding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/agentregistry"
	"github.com/chiga0/marshal-harness/internal/bindingcheck"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/runtimeprofile"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

var (
	// ErrMalformedFacts 拒绝缺失或形态非法的冻结事实。
	ErrMalformedFacts = errors.New("malformed binding facts")
	// ErrAdmissionRejected 表示双 binding recheck 或 ResultIngress 拒绝接纳。
	ErrAdmissionRejected = errors.New("admission rejected")
)

const (
	// ProtocolVersion 是 v1.0 本地 worker-shaped agent binding 的协议版本。
	ProtocolVersion = "marshal-worker/v1alpha1"
	// AuthorityNamespaceID 是本地生产 binding 的权威命名空间。
	AuthorityNamespaceID = "authority:marshal-local"
)

// Facts 是一个 Attempt 接收期的冻结绑定事实。
type Facts struct {
	TaskID, RunID, AttemptID      string
	AgentAdapterID                string // == ProviderName（adapter id 即 provider 名）
	AgentExecutable               string // 审计字段（不参与门禁）
	AgentProviderVersion          string
	CapabilityDigest              string // 冻结 agent capability snapshot digest + 本 Attempt 的 admission evidence
	// AgentRegistrationID 是 dispatch 时从稳定 capability identity digest
	// 派生并冻结进 AttemptBinding 的精确 registration id。ingress/admission
	// 只允许对该 id 做 exact lookup，不再从 CapabilityDigest 现场重新派生，
	// 也不允许「任意 active registration」降级。为空时（旧绑定）回退到从
	// 稳定 digest 派生，但生产新绑定必须显式冻结。
	AgentRegistrationID           string
	SandboxCapabilityDigest       string // 冻结 sandbox provider capability snapshot digest（双 binding 分离）
	ExecutionProfile              string
	SandboxProviderRegistrationID string
	AllocationID                  string
	AllocationGeneration          int64
	LiveAllocationState           sandbox.AllocationState // admission 时刻 Inspect 回读的 live state
	FencingToken                  string
	LeaseExpiry                   time.Time
}

func (f Facts) validate() error {
	for field, value := range map[string]string{
		"TaskID": f.TaskID, "RunID": f.RunID, "AttemptID": f.AttemptID,
		"AgentAdapterID":                f.AgentAdapterID,
		"AgentProviderVersion":          f.AgentProviderVersion,
		"ExecutionProfile":              f.ExecutionProfile,
		"SandboxProviderRegistrationID": f.SandboxProviderRegistrationID,
		"AllocationID":                  f.AllocationID,
		"FencingToken":                  f.FencingToken,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("resultbinding: %w: %s must not be empty", ErrMalformedFacts, field)
		}
	}
	if err := requireDigest("CapabilityDigest", f.CapabilityDigest); err != nil {
		return fmt.Errorf("resultbinding: %w: %v", ErrMalformedFacts, err)
	}
	// SandboxCapabilityDigest 如果为空，回退到 CapabilityDigest（向后兼容）。
	// 生产路径必须分离设置——execchain.go 在 dispatch 时分别填充。
	if f.SandboxCapabilityDigest == "" {
		f.SandboxCapabilityDigest = f.CapabilityDigest
	}
	if err := requireDigest("SandboxCapabilityDigest", f.SandboxCapabilityDigest); err != nil {
		return fmt.Errorf("resultbinding: %w: %v", ErrMalformedFacts, err)
	}
	if f.AllocationGeneration < 1 {
		return fmt.Errorf("resultbinding: %w: generation must be >= 1, got %d", ErrMalformedFacts, f.AllocationGeneration)
	}
	if f.LeaseExpiry.IsZero() {
		return fmt.Errorf("resultbinding: %w: LeaseExpiry must not be zero", ErrMalformedFacts)
	}
	if err := f.LiveAllocationState.Validate(); err != nil {
		return fmt.Errorf("resultbinding: %w: %v", ErrMalformedFacts, err)
	}
	return nil
}

// AgentRegistrationID 从 capability digest 确定性派生 registration id。
func AgentRegistrationID(capabilityDigest string) string {
	trimmed := strings.TrimPrefix(capabilityDigest, "sha256:")
	if len(trimmed) > 32 {
		trimmed = trimmed[:32]
	}
	return "registration:" + trimmed
}

// EffectiveAgentRegistrationID 返回本 Attempt 接纳时用于 exact lookup 的精确
// agent registration id。生产新绑定在 dispatch 时已把稳定派生的
// AgentRegistrationID 冻结进 Facts，直接返回；旧绑定（未冻结）回退到从完整
// CapabilityDigest 派生以保持兼容。两种情况下返回的都是**唯一确定的 id**，
// 接纳端只对它做 exact lookup——不存在「任意 active registration 即通过」
// 的降级。
func (f Facts) EffectiveAgentRegistrationID() string {
	if strings.TrimSpace(f.AgentRegistrationID) != "" {
		return f.AgentRegistrationID
	}
	return AgentRegistrationID(f.CapabilityDigest)
}

// StableCapabilityDigest 计算 capability snapshot 的**稳定身份 digest**：只
// 投影与 `execution.sameCapabilityIdentity` 一致的身份字段（adapterId、
// adapterVersion、executable、executableDigest、binaryVersion、probeStatus），
// 显式排除 `probedAt` 等每次 probe 都会变化的诊断字段。
//
// 不变量：对同一可执行二进制，无论何时、何种顺序触发 Probe()，
// StableCapabilityDigest 恒定；两次 probe 的稳定 digest 相等 ⟺ 它们的
// 身份字段完全一致。因此由它派生的 AgentRegistrationID 跨「CLI 注册期
// probe」与「dispatch 期冻结」严格一致，无需任何降级匹配。
//
// Fail closed：无法解析或身份字段缺失时返回错误，不允许回退到原始
// 完整 digest（那会重新引入 probedAt 漂移）。
func StableCapabilityDigest(rawSnapshot []byte) (string, error) {
	var snap struct {
		AdapterID        string `json:"adapterId"`
		AdapterVersion   string `json:"adapterVersion"`
		Executable       string `json:"executable"`
		ExecutableDigest string `json:"executableDigest"`
		BinaryVersion    string `json:"binaryVersion"`
		ProbeStatus      string `json:"probeStatus"`
	}
	if err := json.Unmarshal(rawSnapshot, &snap); err != nil {
		return "", fmt.Errorf("resultbinding: stable capability digest: %w", err)
	}
	if strings.TrimSpace(snap.AdapterID) == "" {
		return "", errors.New("resultbinding: stable capability digest: adapterId must not be empty")
	}
	identity := map[string]string{
		"adapterId":        snap.AdapterID,
		"adapterVersion":   snap.AdapterVersion,
		"executable":       snap.Executable,
		"executableDigest": snap.ExecutableDigest,
		"binaryVersion":    snap.BinaryVersion,
		"probeStatus":      snap.ProbeStatus,
	}
	identityJSON, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("resultbinding: stable capability digest: %w", err)
	}
	digest, err := canonical.DigestJSON(identityJSON)
	if err != nil {
		return "", fmt.Errorf("resultbinding: stable capability digest: %w", err)
	}
	return digest, nil
}

func seedRegistry(facts Facts) (*agentregistry.Registry, error) {
	registrationID := AgentRegistrationID(facts.CapabilityDigest)
	registry := agentregistry.NewRegistry()
	now := time.Unix(1, 0).UTC()
	registration := agentregistry.AgentRegistration{
		RegistrationID:       registrationID,
		AuthorityNamespaceID: AuthorityNamespaceID,
		SecurityDomainID:     "default/execution/embedded-" + facts.AgentAdapterID,
		Principal:            "principal:agent:" + facts.AgentAdapterID,
		ProviderType:         agentregistry.ProviderTypeAgent,
		ProviderName:         facts.AgentAdapterID,
		ProviderVersion:      facts.AgentProviderVersion,
		ProtocolVersion:      ProtocolVersion,
		Scope:                "worker",
		IdempotencyKey:       "cap:" + facts.CapabilityDigest,
		RequestDigest:        facts.CapabilityDigest,
		LifecycleState:       agentregistry.LifecycleStateActive,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if _, err := registry.Register(registration); err != nil {
		return nil, fmt.Errorf("resultbinding: seed agent registration: %w", err)
	}
	snap := agentregistry.AgentCapabilitySnapshot{
		SnapshotDigest:             facts.CapabilityDigest,
		RegistrationID:             registrationID,
		ProtocolVersion:            ProtocolVersion,
		ProviderName:               facts.AgentAdapterID,
		ProviderVersion:            facts.AgentProviderVersion,
		Capabilities:               []agentregistry.Capability{agentregistry.CapabilityExecutionProfileWorkspaceWrite},
		ConformanceEvidenceDigests: []string{facts.CapabilityDigest},
		SnapshotState:              agentregistry.SnapshotStateActive,
	}
	if _, err := registry.AddSnapshot(snap); err != nil {
		return nil, fmt.Errorf("resultbinding: seed agent snapshot: %w", err)
	}
	return registry, nil
}

func seedSandboxLedger(facts Facts) (*bindingcheck.SandboxLedger, error) {
	ledger := bindingcheck.NewSandboxLedger()
	if _, err := ledger.PutAllocation(facts.AllocationID, facts.SandboxProviderRegistrationID, int(facts.AllocationGeneration)); err != nil {
		return nil, fmt.Errorf("resultbinding: seed sandbox allocation: %w", err)
	}
	// 把 Inspect 读回的 live state 映射进 bindingcheck 生命周期：live 不是
	// active 即终态，全部 fail closed（terminated/failed/provisioning →
	// revoked；replaced → 用 Replace 派生新一代使绑定 generation mismatch）。
	switch facts.LiveAllocationState {
	case sandbox.AllocationActive:
		// keep active
	case sandbox.AllocationReplaced:
		if _, err := ledger.Replace(facts.AllocationID); err != nil {
			return nil, fmt.Errorf("resultbinding: seed replaced allocation: %w", err)
		}
	default:
		if err := ledger.Revoke(facts.AllocationID); err != nil {
			return nil, fmt.Errorf("resultbinding: seed terminated allocation: %w", err)
		}
	}
	return ledger, nil
}

// Admission 是一次双 binding + ResultIngress 接纳的全部锚点（仅审计面，
// 控制权只在 Admission.Accepted）。
type Admission struct {
	Accepted        bool     `json:"accepted"`
	AttemptID       string   `json:"attemptId"`
	ProfileDigest   string   `json:"profileDigest,omitempty"`
	RegistrationID  string   `json:"agentRegistrationId,omitempty"`
	DrcDigest       string   `json:"drcDigest,omitempty"`
	EnvelopeDigest  string   `json:"envelopeDigest,omitempty"`
	AdmissionFact   string   `json:"admissionFactDigest,omitempty"`
	AdmissionReason string   `json:"admissionReason,omitempty"`
	AgentOK         bool     `json:"agentSideOk"`
	SandboxOK       bool     `json:"sandboxSideOk"`
	AgentReasons    []string `json:"agentSideReasons,omitempty"`
	SandboxReasons  []string `json:"sandboxSideReasons,omitempty"`
	EvidenceOK      bool     `json:"evidenceOk"`
	EvidenceReason  string   `json:"evidenceReason,omitempty"`
}

// AdmitWorkerResult 对一个 attempt 的真实 WorkerResult bytes 执行双
// binding recheck（agent snapshot / sandbox live state 各自复核）+ DRC-bound
// ResultIngress 接纳。accepted=false 时返回带细节的档案化拒绝，绝不放行。
//
// 生产路径应使用 AdmitWithDurableAuthority（从 immutable AttemptBinding +
// 真实 durable authority 读取：agent 侧 AgentRegistrationActive current-ledger
// recheck，sandbox 侧 ProviderRegistrationActive + Inspect live state）。
// 本函数保留为测试兼容路径（seedRegistry/seedSandboxLedger 以输入 Facts
// 临时构造，不检查 registration 当前 lifecycle 状态）。
func AdmitWorkerResult(ctx context.Context, facts Facts, resultBytes []byte) (*Admission, error) {
	if err := facts.validate(); err != nil {
		return nil, err
	}
	registry, err := seedRegistry(facts)
	if err != nil {
		return nil, err
	}
	ledger, err := seedSandboxLedger(facts)
	if err != nil {
		return nil, err
	}
	return admitWithRegistryLedger(ctx, facts, resultBytes, registry, ledger)
}

func newAgentBinding(facts Facts) (runtimeprofile.AgentBinding, error) {
	return runtimeprofile.NewAgentBinding(AgentRegistrationID(facts.CapabilityDigest), facts.CapabilityDigest, facts.AgentAdapterID, facts.AgentProviderVersion, ProtocolVersion)
}

func newSandboxBinding(facts Facts) (runtimeprofile.SandboxBinding, error) {
	return runtimeprofile.NewSandboxBinding(facts.SandboxProviderRegistrationID, facts.AllocationID, facts.AllocationGeneration)
}

func newProfile(agent runtimeprofile.AgentBinding, sandbox runtimeprofile.SandboxBinding, facts Facts) (runtimeprofile.WorkerRuntimeProfile, error) {
	return runtimeprofile.NewProfile(agent, sandbox, compatibilityDigest(facts))
}

func compatibilityDigest(facts Facts) string {
	raw, err := canonical.DigestJSON([]byte(facts.CapabilityDigest + "|" + facts.SandboxCapabilityDigest + "|" + facts.SandboxProviderRegistrationID + "|" + ProtocolVersion))
	if err != nil {
		return facts.CapabilityDigest
	}
	return raw
}

func requireDigest(field, value string) error {
	const prefix = "sha256:"
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	if !strings.HasPrefix(value, prefix) {
		return fmt.Errorf("%s must carry the sha256: prefix", field)
	}
	hex := strings.TrimPrefix(value, prefix)
	if len(hex) != 64 {
		return fmt.Errorf("%s must be a 64-character sha256 hex digest", field)
	}
	for _, c := range hex {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("%s must be lowercase hex", field)
		}
	}
	return nil
}
