package resultbinding

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/agentregistry"
	"github.com/chiga0/marshal-harness/internal/attemptgate"
	"github.com/chiga0/marshal-harness/internal/bindingcheck"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/resultingress"
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
	CapabilityDigest              string // 冻结 capability snapshot digest + 本 Attempt 的 admission evidence
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
	checker, err := bindingcheck.NewChecker(registry, ledger)
	if err != nil {
		return nil, fmt.Errorf("resultbinding: %w", err)
	}
	agentBinding, err := runtimeprofile.NewAgentBinding(AgentRegistrationID(facts.CapabilityDigest), facts.CapabilityDigest, facts.AgentAdapterID, facts.AgentProviderVersion, ProtocolVersion)
	if err != nil {
		return nil, fmt.Errorf("resultbinding: agent binding: %w", err)
	}
	sandboxBinding, err := runtimeprofile.NewSandboxBinding(facts.SandboxProviderRegistrationID, facts.AllocationID, facts.AllocationGeneration)
	if err != nil {
		return nil, fmt.Errorf("resultbinding: sandbox binding: %w", err)
	}
	profile, err := runtimeprofile.NewProfile(agentBinding, sandboxBinding, compatibilityDigest(facts))
	if err != nil {
		return nil, fmt.Errorf("resultbinding: worker profile: %w", err)
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
	admission := &Admission{
		AttemptID: facts.AttemptID,
	}
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

	requestDigest := canonical.DigestBytes(resultBytes)
	envelope := resultingress.ResultEnvelope{Kind: resultingress.KindWorkerResult, ResultDigest: requestDigest, Sequence: 1}
	expiry := facts.LeaseExpiry
	ledgerBinding := resultingress.LedgerBinding{
		LeaseID:        facts.AllocationID,
		Generation:     uint64(facts.AllocationGeneration),
		FencingToken:   facts.FencingToken,
		AttemptID:      facts.AttemptID,
		AllocationID:   facts.AllocationID,
		Expiry:         expiry,
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
		Expiry:               expiry,
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

func compatibilityDigest(facts Facts) string {
	raw, err := canonical.DigestJSON([]byte(facts.CapabilityDigest + "|" + facts.SandboxProviderRegistrationID + "|" + ProtocolVersion))
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
