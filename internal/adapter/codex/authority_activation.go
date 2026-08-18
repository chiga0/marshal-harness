package codex

import (
	"context"
	"errors"
	"runtime"
	"strings"

	"github.com/chiga0/marshal-harness/internal/contract"
)

var ErrCodexConformancePending = errors.New("codex credentialed live conformance pending")

type authorityAdmission struct {
	metadata CodexAuthorityMetadataV1
}

// authorityProbeMaterial is one fresh, closed authority transaction. The
// source must recover State atomically before returning its signed objects.
// It contains no private key and is consumed once by one Probe or Run launch.
type authorityProbeMaterial struct {
	State               CodexConsumerAuthorityStateV1
	KeysetEnvelope      SignedEnvelopeV1
	ConfigEnvelope      SignedEnvelopeV1
	EvidenceEnvelope    SignedEnvelopeV1
	ObservationEnvelope SignedEnvelopeV1
	ReceiptEnvelopes    []SignedEnvelopeV1
	ExpectedHostNonce   []byte
	HostVerifier        TPMHostAttestationVerifier
}

type atomicAuthoritySource interface {
	LoadFreshAuthority(context.Context) (authorityProbeMaterial, error)
}

type atomicAuthorityConsumer interface {
	bindAtomicAuthoritySource(atomicAuthoritySource)
}

var _ atomicAuthorityConsumer = (*Adapter)(nil)

// bindAtomicAuthoritySource is package-private until the credentialed source
// exists. Binding does not admit support: every Probe reloads and completely
// verifies a fresh transaction; no successful admission is cached.
func (a *Adapter) bindAtomicAuthoritySource(source atomicAuthoritySource) {
	a.authorityMu.Lock()
	defer a.authorityMu.Unlock()
	a.authoritySource = source
	a.authorityNonceFence = NewHostAttestationNonceFence()
	a.lastAuthorityState = nil
}

func (a *Adapter) hasAtomicAuthoritySource() bool {
	a.authorityMu.Lock()
	defer a.authorityMu.Unlock()
	return a.authoritySource != nil
}

func (a *Adapter) consumeFreshAuthority(ctx context.Context, snapshot *executableSnapshot, action string) (authorityAdmission, *AuthorityFailure) {
	a.authorityMu.Lock()
	defer a.authorityMu.Unlock()
	if ctx == nil || ctx.Err() != nil {
		return authorityAdmission{}, newAuthorityFailure(action, "codex_conformance_pending", "Codex credentialed live conformance is pending", AuthorityFailureDetails{}, ErrCodexConformancePending, a.now())
	}
	if a.authoritySource == nil || a.authorityNonceFence == nil {
		return authorityAdmission{}, newAuthorityFailure(action, "codex_conformance_pending", conformancePendingReason, AuthorityFailureDetails{}, ErrCodexConformancePending, a.now())
	}
	material, err := a.authoritySource.LoadFreshAuthority(ctx)
	if err != nil {
		return authorityAdmission{}, newAuthorityFailure(action, "codex_authority_temporarily_unavailable", "Codex authority material is unavailable", AuthorityFailureDetails{}, err, a.now())
	}
	if err := material.State.Validate(); err != nil {
		return authorityAdmission{}, newAuthorityFailure(action, "codex_fence_invalid", "Codex recovered authority state is invalid", AuthorityFailureDetails{}, err, a.now())
	}
	if err := ValidateStateAdvance(a.lastAuthorityState, material.State); err != nil {
		return authorityAdmission{}, newAuthorityFailure(action, "codex_authority_rollback", "Codex recovered authority state was rejected", AuthorityFailureDetails{}, err, a.now())
	}
	bundle, err := VerifyAuthorityBundle(a.now().UTC(), material.State.ActiveRootPin, material.KeysetEnvelope, material.ConfigEnvelope, material.EvidenceEnvelope, material.ObservationEnvelope, material.ReceiptEnvelopes, material.ExpectedHostNonce, material.HostVerifier, a.authorityNonceFence)
	if err != nil {
		return authorityAdmission{}, newAuthorityFailure(action, "codex_evidence_invalid", "Codex signed authority bundle was rejected", AuthorityFailureDetails{}, err, a.now())
	}
	if err := validateRecoveredAuthorityState(material, bundle); err != nil {
		return authorityAdmission{}, newAuthorityFailure(action, "codex_fence_invalid", "Codex signed authority bundle differs from recovered state", AuthorityFailureDetails{}, err, a.now())
	}
	identity, err := snapshot.authorityExecutableIdentity()
	if err != nil {
		return authorityAdmission{}, newAuthorityFailure(action, "codex_mount_identity_unsupported", "Codex held executable mount identity is unavailable", AuthorityFailureDetails{Platform: runtime.GOOS}, err, a.now())
	}
	binaryDigest, err := canonicalDigest(identity)
	if err != nil || binaryDigest != bundle.Evidence.BinaryIdentityDigest || identity != bundle.Observation.BinaryIdentity {
		return authorityAdmission{}, newAuthorityFailure(action, "codex_evidence_binary_mismatch", "Codex authority evidence does not match the held executable", AuthorityFailureDetails{EvidenceDigest: bundle.Config.CurrentEvidenceDigest}, err, a.now())
	}
	expectedContract, err := compiledCodexContractBinding()
	if err != nil || !isSupportedBinary(identity.Version) || !equalCodexContractBinding(bundle.Observation.Contract, expectedContract) {
		return authorityAdmission{}, newAuthorityFailure(action, "codex_evidence_contract_mismatch", "Codex authority evidence does not match the compiled adapter contract", AuthorityFailureDetails{}, err, a.now())
	}
	contract := bundle.Observation.Contract
	observedAt, err := parseAuthorityTime(bundle.Observation.ObservedAt)
	if err != nil {
		return authorityAdmission{}, newAuthorityFailure(action, "codex_evidence_invalid", "Codex authority evidence is invalid", AuthorityFailureDetails{}, err, a.now())
	}
	metadata, err := NewAuthorityMetadata(bundle.Config, bundle.Evidence, material.State.ActiveRootPin, material.State.Fence, CodexContractMetadataInput{
		CodexVersion: identity.Version, ArgvMatrixDigest: contract.ArgvMatrixDigest, EnvironmentDigest: contract.EnvironmentDigest,
		EventContractDigest: contract.EventContractDigest, PermissionContractDigest: contract.PermissionContractDigest,
		ToolPolicyDigest: contract.ToolPolicyDigest, ResultContractDigest: contract.ResultContractDigest,
		OutputLimitDigest: contract.OutputLimitDigest, NativeBudgetsDigest: contract.NativeBudgetsDigest,
		ExecutionProfiles: append([]string(nil), contract.ExecutionProfiles...),
	}, observedAt)
	if err != nil {
		return authorityAdmission{}, newAuthorityFailure(action, "codex_evidence_invalid", "Codex authority metadata is invalid", AuthorityFailureDetails{}, err, a.now())
	}
	validUntil, err := parseAuthorityTime(metadata.ValidUntil)
	if err != nil || !a.now().UTC().Before(validUntil) {
		return authorityAdmission{}, newAuthorityFailure(action, "codex_evidence_expired", "Codex authority evidence is expired", AuthorityFailureDetails{EvidenceDigest: metadata.EvidenceDigest}, err, a.now())
	}
	state := material.State
	a.lastAuthorityState = &state
	return authorityAdmission{metadata: metadata}, nil
}

func validateRecoveredAuthorityState(material authorityProbeMaterial, bundle VerifiedAuthorityBundleV1) error {
	state, fence, config := material.State, material.State.Fence, bundle.Config
	if material.KeysetEnvelope.PayloadDigest != state.ActiveRootPin.KeysetDigest || material.ConfigEnvelope.PayloadDigest != fence.ConfigDigest || material.EvidenceEnvelope.PayloadDigest != fence.CurrentEvidenceDigest {
		return errors.New("codex signed envelope digests differ from recovered fence")
	}
	if bundle.ConfigDigest != fence.ConfigDigest || config.AuthorityNamespace != fence.AuthorityNamespace || config.AuthorityGeneration != fence.AuthorityGeneration || config.TrustRootGeneration != fence.TrustRootGeneration || config.KeysetDigest != fence.KeysetDigest || config.CurrentEvidenceDigest != fence.CurrentEvidenceDigest || config.RevocationSetDigest != fence.RevocationSetDigest || config.HostIdentityDigest != fence.HostIdentityDigest || config.BootstrapID != fence.BootstrapID {
		return errors.New("codex config projection differs from recovered fence")
	}
	if bundle.Evidence.AuthorityNamespace != fence.AuthorityNamespace || bundle.Evidence.AuthorityGeneration != fence.AuthorityGeneration || bundle.Evidence.TrustRootGeneration != fence.TrustRootGeneration || bundle.Evidence.HostIdentityDigest != fence.HostIdentityDigest || bundle.Evidence.BootstrapID != fence.BootstrapID {
		return errors.New("codex evidence projection differs from recovered fence")
	}
	return nil
}

// NewFromAuthorityConfig is intentionally a hard-disabled production seam.
// This core slice can parse and verify hermetic authority objects, but no
// registry, consumer, or live-evidence enablement is authorized by ADR 0037
// acceptance alone.
func NewFromAuthorityConfig(ctx context.Context, executable string, validator *contract.Validator, configPath string) (*Adapter, error) {
	if runtime.GOOS != "linux" {
		return nil, newAuthorityFailure("constructor", "codex_platform_unsupported", "Codex production authority is unsupported on this platform", AuthorityFailureDetails{Platform: runtime.GOOS}, ErrCodexConformancePending, authorityNow())
	}
	if ctx == nil || ctx.Err() != nil || validator == nil || strings.TrimSpace(executable) == "" || strings.TrimSpace(configPath) == "" {
		return nil, newAuthorityFailure("constructor", "codex_conformance_pending", "Codex credentialed live conformance is pending", AuthorityFailureDetails{}, ErrCodexConformancePending, authorityNow())
	}
	return nil, newAuthorityFailure("constructor", "codex_conformance_pending", "Codex credentialed live conformance is pending", AuthorityFailureDetails{}, ErrCodexConformancePending, authorityNow())
}
