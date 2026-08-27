package resultbinding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	// AgentRegistrationActive 验证 agent adapter registration 在当前
	// authority 中仍为 active（R2/R3 纠偏：agent 侧 current-ledger recheck，
	// 替代 seedRegistry 总是构造 active registration 的临时自洽验证）。
	AgentRegistrationActive(registrationID string) (bool, error)
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

	// sandbox 侧：从真实 durable ledger 验证 provider registration 仍 active。
	reg, err := authority.ProviderRegistration()
	if err != nil {
		return nil, fmt.Errorf("resultbinding: %w: durable registration lookup: %v", ErrAdmissionRejected, err)
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

	// agent 侧 current-ledger recheck（R2/R3 纠偏）：从 durable authority
	// 验证 agent adapter registration 当前仍为 active，替代 seedRegistry
	// 总是构造 active registration 的临时自洽验证。registration 被撤销
	// 的 agent 不得接纳结果。
	agentRegID := AgentRegistrationID(facts.CapabilityDigest)
	agentActive, err := authority.AgentRegistrationActive(agentRegID)
	if err != nil {
		return nil, fmt.Errorf("resultbinding: %w: agent registration active check: %v", ErrAdmissionRejected, err)
	}
	if !agentActive {
		admission := &Admission{
			AttemptID:       facts.AttemptID,
			Accepted:        false,
			SandboxOK:       true,
			AgentOK:         false,
			AdmissionReason: "agent registration revoked or expired in durable authority",
		}
		return admission, fmt.Errorf("resultbinding: %w: agent registration %s not active", ErrAdmissionRejected, agentRegID)
	}

	// 用 binding 文件冻结的 facts（而非结果携带的临时值）构建 admission。
	// liveState 来自 ingress 时刻的 Inspect。
	facts.LiveAllocationState = liveState

	// bindingcheck 仍需 registry/ledger 做 snapshot 一致性与 allocation
	// generation 校验。registry 从 binding 文件重建（binding 文件本身是
	// dispatch 时冻结的 authority，篡改已被 digest 检测拦截），但
	// registration 的 active 状态已在上面从 durable authority 验证。
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

// admitWithRegistryLedger 是共享的 admission 核心逻辑（seed 与 durable
// 路径共用 bindingcheck/attemptgate/resultingress 验证）。
func admitWithRegistryLedger(ctx context.Context, facts Facts, resultBytes []byte, registry *agentregistry.Registry, ledger *bindingcheck.SandboxLedger) (*Admission, error) {
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
	decision, err := gate.AdmitAttemptResult(facts.AttemptID, facts.CapabilityDigest)
	admission := &Admission{AttemptID: facts.AttemptID}
	if decision.ProfileDigest != "" {
		admission.ProfileDigest = decision.ProfileDigest
	}
	admission.RegistrationID = AgentRegistrationID(facts.CapabilityDigest)
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
		RegistrationID: AgentRegistrationID(facts.CapabilityDigest),
		SnapshotDigest: facts.CapabilityDigest,
		EvidenceDigest: facts.CapabilityDigest,
	}
	ingress, err := resultingress.NewIngress(ledgerBinding)
	if err != nil {
		return nil, fmt.Errorf("resultbinding: construct ingress: %w", err)
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
		RegistrationID:       AgentRegistrationID(facts.CapabilityDigest),
		SnapshotDigest:       facts.CapabilityDigest,
		EvidenceDigest:       facts.CapabilityDigest,
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
	return admission, nil
}
