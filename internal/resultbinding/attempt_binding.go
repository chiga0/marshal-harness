package resultbinding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/agentregistry"
	"github.com/chiga0/marshal-harness/internal/attemptgate"
	"github.com/chiga0/marshal-harness/internal/bindingcheck"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/provider"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// AttemptBinding 是 dispatch 时冻结的 immutable Attempt 绑定事实，写入
// attempt 目录作为 ingress 时的权威来源（R2/R3 纠偏：替代以结果携带
// Facts 临时构造的 seed 路径）。携带 content digest 防篡改。
type AttemptBinding struct {
	Schema        string `json:"schema"`
	BindingDigest string `json:"bindingDigest"` // detached content digest
	Facts         Facts  `json:"facts"`
	CreatedAt     string `json:"createdAt"`
}

const AttemptBindingSchema = "marshal.attempt-binding.v1"
const AttemptBindingFileName = "attempt-binding.json"

// WriteAttemptBinding 把冻结的 Facts 序列化为 canonical JSON、计算
// detached digest、写入 attempt 目录。文件是 immutable 的——ingress 时
// 重新打开并验证 digest，任何篡改 fail closed。
//
// Creation-once（R2/R3 深化）：如果 binding 文件已存在且 digest 匹配，
// 写入是幂等的（同一 attempt 重复 dispatch 的安全重放）；如果文件已
// 存在但 digest 不匹配，fail closed（binding 被替换或篡改）。
func WriteAttemptBinding(attemptDir string, facts Facts) error {
	if attemptDir == "" {
		return errors.New("resultbinding: attempt directory must not be empty")
	}
	if err := facts.validate(); err != nil {
		return fmt.Errorf("resultbinding: write binding: %w", err)
	}
	binding := AttemptBinding{
		Schema:    AttemptBindingSchema,
		Facts:     facts,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	digest, err := binding.digest()
	if err != nil {
		return fmt.Errorf("resultbinding: write binding: %w", err)
	}
	binding.BindingDigest = digest
	raw, err := json.MarshalIndent(binding, "", "  ")
	if err != nil {
		return fmt.Errorf("resultbinding: write binding: %w", err)
	}
	path := filepath.Join(attemptDir, AttemptBindingFileName)
	// Creation-once：如果文件已存在，验证 digest 匹配（幂等重放）。
	if existing, readErr := os.ReadFile(path); readErr == nil {
		existingDigest, parseErr := parseBindingDigest(existing)
		if parseErr != nil || existingDigest != digest {
			return fmt.Errorf("resultbinding: write binding: %w: attempt binding already exists with different content (creation-once violation)", ErrAdmissionRejected)
		}
		// 幂等重放：内容完全一致，不重写。
		return nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("resultbinding: write binding: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("resultbinding: write binding: %w", err)
	}
	return nil
}

// parseBindingDigest 从已有 binding 文件中提取 BindingDigest 字段，
// 不做完整反序列化（仅用于 creation-once 比较）。
func parseBindingDigest(raw []byte) (string, error) {
	var probe struct {
		BindingDigest string `json:"bindingDigest"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", err
	}
	return probe.BindingDigest, nil
}

// ReadAttemptBinding 从 attempt 目录读取 immutable binding 并验证
// content digest。digest 不匹配或文件缺失 fail closed。
func ReadAttemptBinding(attemptDir string) (*AttemptBinding, error) {
	if attemptDir == "" {
		return nil, errors.New("resultbinding: attempt directory must not be empty")
	}
	raw, err := os.ReadFile(filepath.Join(attemptDir, AttemptBindingFileName))
	if err != nil {
		return nil, fmt.Errorf("resultbinding: read binding: %w", err)
	}
	var binding AttemptBinding
	if err := json.Unmarshal(raw, &binding); err != nil {
		return nil, fmt.Errorf("resultbinding: read binding: %w", err)
	}
	if binding.Schema != AttemptBindingSchema {
		return nil, fmt.Errorf("resultbinding: read binding: schema mismatch %q", binding.Schema)
	}
	expected, err := binding.digest()
	if err != nil {
		return nil, fmt.Errorf("resultbinding: read binding: %w", err)
	}
	if binding.BindingDigest != expected {
		return nil, fmt.Errorf("resultbinding: read binding: %w: binding digest mismatch (tampered or corrupted)", ErrAdmissionRejected)
	}
	if err := binding.Facts.validate(); err != nil {
		return nil, fmt.Errorf("resultbinding: read binding: %w", err)
	}
	return &binding, nil
}

func (b AttemptBinding) digest() (string, error) {
	detached := b
	detached.BindingDigest = ""
	detached.CreatedAt = "" // CreatedAt 是元数据，不参与 authority 绑定
	raw, err := json.Marshal(detached)
	if err != nil {
		return "", err
	}
	canonicalized, err := canonical.JSON(raw)
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(canonicalized), nil
}

// DurableAuthoritySource 是 ingress 时读取真实 durable authority 的接缝。
// 生产路径必须注入；未注入时退化为 seed 路径（仅测试兼容）。
type DurableAuthoritySource interface {
	// ProviderRegistration 返回 dispatch 时冻结的 sandbox provider registration。
	ProviderRegistration() (provider.ProviderRegistration, error)
	// ProviderRegistrationActive 验证 registration 在 durable ledger 中
	// 仍为 active（未 revoked/expired）。
	ProviderRegistrationActive(registrationID string) (bool, error)
	// AgentAuthority 返回 exact registration 与 current active capability
	// snapshot 的一致耐久视图。结果接纳不得用结果携带字段临时构造 authority。
	AgentAuthority(registrationID string) (agentregistry.AgentRegistration, agentregistry.AgentCapabilitySnapshot, error)
	// ResultIngressDir 返回 ResultIngress replay/quarantine/idempotency 的
	// 耐久 append-only 账本目录（R2 纵切）。生产（embedded runtime）必须提供：
	// admission 在该目录打开 durable store、用 NewDurableIngress 执行跨进程
	// replay 检测。返回空字符串时 admission 退化为进程内存 ingress（仅测试
	// 或轻量场景）。
	ResultIngressDir() string
}

// AdmitWithDurableAuthority 从 immutable AttemptBinding（dispatch 时冻结）
// + 真实 durable authority 执行生产 admission。替代 seedRegistry/
// seedSandboxLedger 路径：agent 侧从 durable authority 验证 registration
// 当前仍为 active；sandbox 侧以 RegistrationStore 的真实 lifecycle 状态
// 为 authority，live allocation state 来自 Inspect。
func AdmitWithDurableAuthority(ctx context.Context, binding *AttemptBinding, resultBytes []byte, authority DurableAuthoritySource, liveState sandbox.AllocationState) (*Admission, error) {
	if binding == nil {
		return nil, fmt.Errorf("resultbinding: %w: nil binding", ErrAdmissionRejected)
	}
	if authority == nil {
		return nil, fmt.Errorf("resultbinding: %w: nil durable authority", ErrAdmissionRejected)
	}
	facts := binding.Facts
	if err := facts.validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(facts.AgentRegistrationID) == "" || strings.TrimSpace(facts.AgentCapabilitySnapshotDigest) == "" {
		admission := &Admission{AttemptID: facts.AttemptID, SandboxOK: true, AgentOK: false, AdmissionReason: "durable admission requires frozen agent registration and snapshot identity"}
		return admission, fmt.Errorf("resultbinding: %w: durable admission requires explicit AgentRegistrationID and AgentCapabilitySnapshotDigest", ErrAdmissionRejected)
	}

	// sandbox 侧：从真实 durable ledger 验证 provider registration 仍 active。
	reg, err := authority.ProviderRegistration()
	if err != nil {
		return nil, fmt.Errorf("resultbinding: %w: durable registration lookup: %v", ErrAdmissionRejected, err)
	}
	// current-ledger binding 机械断言：AttemptBinding 冻结的
	// SandboxProviderRegistrationID 必须与真实 durable ledger 当前的
	// ProviderRegistrationID 逐字相等。二者不等说明绑定引用的 provider
	// registration 已被替换/漂移，接纳 fail closed——不允许只靠消费端补前缀
	// 或临时 seed ledger 满足格式检查。
	if facts.SandboxProviderRegistrationID != reg.RegistrationId {
		admission := &Admission{
			AttemptID:       facts.AttemptID,
			Accepted:        false,
			SandboxOK:       false,
			AgentOK:         true,
			AdmissionReason: "attempt binding sandbox registration does not match current ledger",
		}
		return admission, fmt.Errorf("resultbinding: %w: AttemptBinding.SandboxProviderRegistrationID %q does not match current ledger ProviderRegistrationID %q", ErrAdmissionRejected, facts.SandboxProviderRegistrationID, reg.RegistrationId)
	}
	active, err := authority.ProviderRegistrationActive(reg.RegistrationId)
	if err != nil {
		return nil, fmt.Errorf("resultbinding: %w: durable registration active check: %v", ErrAdmissionRejected, err)
	}
	if !active {
		admission := &Admission{
			AttemptID:       facts.AttemptID,
			Accepted:        false,
			SandboxOK:       false,
			AgentOK:         true,
			AdmissionReason: "provider registration revoked or expired in durable ledger",
		}
		return admission, fmt.Errorf("resultbinding: %w: provider registration %s not active", ErrAdmissionRejected, reg.RegistrationId)
	}

	// agent 侧 current-ledger recheck：一次读取 exact registration + current
	// active snapshot，并逐项核对 AttemptBinding 冻结的身份。不得先检查一个
	// registration ID、再用 capability digest 临时派生另一个 registry。
	agentRegID := facts.EffectiveAgentRegistrationID()
	agentReg, agentSnap, err := authority.AgentAuthority(agentRegID)
	if err != nil {
		admission := &Admission{AttemptID: facts.AttemptID, SandboxOK: true, AgentOK: false, AdmissionReason: "agent authority missing or has no active snapshot"}
		return admission, fmt.Errorf("resultbinding: %w: agent authority lookup: %v", ErrAdmissionRejected, err)
	}
	if agentReg.RegistrationID != agentRegID ||
		agentReg.LifecycleState != agentregistry.LifecycleStateActive ||
		agentReg.ProviderType != agentregistry.ProviderTypeAgent ||
		agentReg.ProviderName != facts.AgentAdapterID ||
		agentReg.ProviderVersion != facts.AgentProviderVersion ||
		agentReg.ProtocolVersion != ProtocolVersion ||
		agentSnap.RegistrationID != agentRegID ||
		agentSnap.SnapshotState != agentregistry.SnapshotStateActive ||
		agentSnap.SnapshotDigest != facts.EffectiveAgentCapabilitySnapshotDigest() ||
		agentSnap.ProviderName != facts.AgentAdapterID ||
		agentSnap.ProviderVersion != facts.AgentProviderVersion ||
		agentSnap.ProtocolVersion != ProtocolVersion {
		admission := &Admission{
			AttemptID:       facts.AttemptID,
			Accepted:        false,
			SandboxOK:       true,
			AgentOK:         false,
			AdmissionReason: "attempt binding agent authority does not match current ledger",
		}
		return admission, fmt.Errorf("resultbinding: %w: agent registration/snapshot %s does not match current ledger", ErrAdmissionRejected, agentRegID)
	}

	// 用 binding 文件冻结的 facts（而非结果携带的临时值）构建 admission。
	// liveState 来自 ingress 时刻的 Inspect。
	facts.LiveAllocationState = liveState

	// bindingcheck 使用上面从 durable authority 读出的 exact 事实投影；不再
	// 由 AttemptBinding 自己 seed 一个永远 active 的 agent registry。
	registry, err := projectAgentRegistry(agentReg, agentSnap)
	if err != nil {
		return nil, err
	}
	ledger, err := seedSandboxLedger(facts)
	if err != nil {
		return nil, err
	}
	return admitWithRegistryLedger(ctx, facts, resultBytes, registry, ledger, authority.ResultIngressDir())
}

func projectAgentRegistry(reg agentregistry.AgentRegistration, snap agentregistry.AgentCapabilitySnapshot) (*agentregistry.Registry, error) {
	registry := agentregistry.NewRegistry()
	if _, err := registry.Register(reg); err != nil {
		return nil, fmt.Errorf("resultbinding: project durable agent registration: %w", err)
	}
	if _, err := registry.AddSnapshot(snap); err != nil {
		return nil, fmt.Errorf("resultbinding: project durable agent snapshot: %w", err)
	}
	return registry, nil
}

// admitWithRegistryLedger 是共享的 admission 核心逻辑（seed 与 durable
// 路径共用 bindingcheck/attemptgate/resultingress 验证）。ingressDir 为空
// 时 admission 使用进程内存 ingress（测试/seed）；非空时打开耐久 replay
// 账本（跨进程/重复送达 replay 检测，R2 纵切）。
func admitWithRegistryLedger(ctx context.Context, facts Facts, resultBytes []byte, registry *agentregistry.Registry, ledger *bindingcheck.SandboxLedger, ingressDir string) (*Admission, error) {
	if err := facts.validate(); err != nil {
		return nil, err
	}
	checker, err := bindingcheck.NewChecker(registry, ledger)
	if err != nil {
		return nil, fmt.Errorf("resultbinding: %w", err)
	}
	agentBinding, err := newAgentBinding(facts)
	if err != nil {
		return nil, err
	}
	sandboxBinding, err := newSandboxBinding(facts)
	if err != nil {
		return nil, err
	}
	profile, err := newProfile(agentBinding, sandboxBinding, facts)
	if err != nil {
		return nil, err
	}
	store := attemptgate.NewAttemptProfileStore()
	if err := store.Bind(facts.AttemptID, profile); err != nil {
		return nil, fmt.Errorf("resultbinding: bind attempt profile: %w", err)
	}
	gate, err := attemptgate.NewGate(store, checker, registry)
	if err != nil {
		return nil, fmt.Errorf("resultbinding: %w", err)
	}
	decision, err := gate.AdmitAttemptResult(facts.AttemptID, facts.EffectiveAgentCapabilitySnapshotDigest())
	admission := &Admission{AttemptID: facts.AttemptID}
	if decision.ProfileDigest != "" {
		admission.ProfileDigest = decision.ProfileDigest
	}
	admission.RegistrationID = facts.EffectiveAgentRegistrationID()
	admission.AgentOK = decision.Agent.OK
	admission.SandboxOK = decision.Sandbox.OK
	for _, r := range decision.Agent.Reasons {
		admission.AgentReasons = append(admission.AgentReasons, string(r))
	}
	for _, r := range decision.Sandbox.Reasons {
		admission.SandboxReasons = append(admission.SandboxReasons, string(r))
	}
	admission.EvidenceOK = decision.EvidenceOK
	if decision.EvidenceReason != "" {
		admission.EvidenceReason = string(decision.EvidenceReason)
	}
	if err != nil {
		admission.AdmissionReason = err.Error()
	}
	if err != nil || !decision.Accepted {
		admission.Accepted = false
		if admission.AdmissionReason == "" {
			admission.AdmissionReason = "dual-binding-or-evidence-reject"
		}
		return admission, fmt.Errorf("resultbinding: %w: %s", ErrAdmissionRejected, admission.AdmissionReason)
	}
	// ResultIngress 接纳（DRC-bound）。
	requestDigest := canonical.DigestBytes(resultBytes)
	envelope := resultingress.ResultEnvelope{Kind: resultingress.KindWorkerResult, ResultDigest: requestDigest, Sequence: 1}
	ledgerBinding := resultingress.LedgerBinding{
		LeaseID:        facts.AllocationID,
		Generation:     uint64(facts.AllocationGeneration),
		FencingToken:   facts.FencingToken,
		AttemptID:      facts.AttemptID,
		AllocationID:   facts.AllocationID,
		Expiry:         facts.LeaseExpiry,
		Revoked:        false,
		RegistrationID: facts.EffectiveAgentRegistrationID(),
		SnapshotDigest: facts.EffectiveAgentCapabilitySnapshotDigest(),
		EvidenceDigest: facts.EffectiveAgentCapabilitySnapshotDigest(),
	}
	// R2 纵切：ingressDir 非空时 admission 用耐久 replay 账本 +
	// NewDurableIngress（跨进程/重复送达 replay 检测）；为空（测试/seed）时
	// 回退进程内存 ingress，保持既有行为不变。
	var ingress *resultingress.Ingress
	if ingressDir != "" {
		store, storeErr := resultingress.OpenResultIngressStore(ingressDir)
		if storeErr != nil {
			return nil, fmt.Errorf("resultbinding: open durable result ingress store (fail closed): %v", storeErr)
		}
		ingress, err = resultingress.NewDurableIngress(ledgerBinding, store)
		if err != nil {
			return nil, fmt.Errorf("resultbinding: construct durable ingress: %w", err)
		}
	} else {
		ingress, err = resultingress.NewIngress(ledgerBinding)
		if err != nil {
			return nil, fmt.Errorf("resultbinding: construct ingress: %w", err)
		}
	}
	drc := resultingress.DRC{
		AuthorityNamespaceID: AuthorityNamespaceID,
		TaskID:               facts.TaskID,
		RunID:                facts.RunID,
		AttemptID:            facts.AttemptID,
		AllocationID:         facts.AllocationID,
		LeaseID:              facts.AllocationID,
		Generation:           uint64(facts.AllocationGeneration),
		FencingToken:         facts.FencingToken,
		CommandID:            "command-result",
		IdempotencyKey:       "ingress:attempt:" + facts.AttemptID,
		RequestDigest:        requestDigest,
		Nonce:                facts.FencingToken,
		Expiry:               facts.LeaseExpiry,
		Operation:            resultingress.OpResult,
		RegistrationID:       facts.EffectiveAgentRegistrationID(),
		SnapshotDigest:       facts.EffectiveAgentCapabilitySnapshotDigest(),
		EvidenceDigest:       facts.EffectiveAgentCapabilitySnapshotDigest(),
	}
	drcDigest, err := drc.Digest()
	if err != nil {
		return nil, fmt.Errorf("resultbinding: %w", err)
	}
	fact, err := ingress.Admit(ctx, drc, envelope)
	if err != nil {
		admission.Accepted = false
		admission.AdmissionReason = "resultingress: " + err.Error()
		admission.DrcDigest = drcDigest
		admission.EnvelopeDigest = requestDigest
		return admission, fmt.Errorf("resultbinding: %w: ingress: %v", ErrAdmissionRejected, err)
	}
	admission.Accepted = true
	admission.DrcDigest = drcDigest
	admission.EnvelopeDigest = requestDigest
	admission.AdmissionFact = fact.FactDigest
	admission.IdempotentReplay = fact.IdempotentReplay
	return admission, nil
}
