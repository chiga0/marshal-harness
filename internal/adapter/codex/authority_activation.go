package codex

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/contract"
)

var ErrCodexConformancePending = errors.New("codex credentialed live conformance pending")

type authorityAdmission struct {
	identity   ExecutableIdentityV1
	metadata   CodexAuthorityMetadataV1
	validUntil time.Time
}

// verifiedAuthorityConsumer freezes the adapter-side consumer contract while
// keeping it package-private until the live credentialed provider exists.
type verifiedAuthorityConsumer interface {
	consumeVerifiedAuthority(context.Context, VerifiedAuthorityBundleV1, CodexActiveRootPinV1, CodexConsumerFenceV1) error
}

var _ verifiedAuthorityConsumer = (*Adapter)(nil)

// consumeVerifiedAuthority is the hermetic consumer seam between the ADR
// 0037 verifier and the adapter. It is deliberately unexported: only a future
// credentialed constructor may feed it the immediate result of
// VerifyAuthorityBundle. The current production constructor remains disabled.
func (a *Adapter) consumeVerifiedAuthority(ctx context.Context, bundle VerifiedAuthorityBundleV1, pin CodexActiveRootPinV1, fence CodexConsumerFenceV1) error {
	if ctx == nil || ctx.Err() != nil {
		return newAuthorityFailure("probe", "codex_conformance_pending", "Codex credentialed live conformance is pending", AuthorityFailureDetails{}, ErrCodexConformancePending, a.now())
	}
	if pin.Validate() != nil || bundle.Config.Validate() != nil || bundle.Evidence.Validate() != nil || bundle.Observation.Validate() != nil || bundle.Observation.Contract.Validate() != nil {
		return newAuthorityFailure("probe", "codex_evidence_invalid", "Codex authority evidence is invalid", AuthorityFailureDetails{}, nil, a.now())
	}
	snapshot, err := a.inspect(ctx)
	if err != nil {
		return newAuthorityFailure("probe", "codex_executable_unsafe", "Codex held executable identity is unavailable", AuthorityFailureDetails{}, err, a.now())
	}
	defer snapshot.close()
	identity, err := snapshot.authorityExecutableIdentity()
	if err != nil {
		return newAuthorityFailure("probe", "codex_mount_identity_unsupported", "Codex held executable mount identity is unavailable", AuthorityFailureDetails{Platform: runtime.GOOS}, err, a.now())
	}
	binaryDigest, err := canonicalDigest(identity)
	if err != nil || binaryDigest != bundle.Evidence.BinaryIdentityDigest || identity != bundle.Observation.BinaryIdentity {
		return newAuthorityFailure("probe", "codex_evidence_binary_mismatch", "Codex authority evidence does not match the held executable", AuthorityFailureDetails{EvidenceDigest: bundle.Config.CurrentEvidenceDigest}, err, a.now())
	}
	if !isSupportedBinary(identity.Version) || bundle.ConfigDigest != fence.ConfigDigest || bundle.Config.CurrentEvidenceDigest == "" {
		return newAuthorityFailure("probe", "codex_evidence_contract_mismatch", "Codex authority evidence does not match the adapter contract", AuthorityFailureDetails{}, nil, a.now())
	}
	contract := bundle.Observation.Contract
	observedAt, err := parseAuthorityTime(bundle.Observation.ObservedAt)
	if err != nil {
		return newAuthorityFailure("probe", "codex_evidence_invalid", "Codex authority evidence is invalid", AuthorityFailureDetails{}, err, a.now())
	}
	metadata, err := NewAuthorityMetadata(bundle.Config, bundle.Evidence, pin, fence, CodexContractMetadataInput{
		CodexVersion: identity.Version, ArgvMatrixDigest: contract.ArgvMatrixDigest, EnvironmentDigest: contract.EnvironmentDigest,
		EventContractDigest: contract.EventContractDigest, PermissionContractDigest: contract.PermissionContractDigest,
		ToolPolicyDigest: contract.ToolPolicyDigest, ResultContractDigest: contract.ResultContractDigest,
		OutputLimitDigest: contract.OutputLimitDigest, NativeBudgetsDigest: contract.NativeBudgetsDigest,
		ExecutionProfiles: append([]string(nil), contract.ExecutionProfiles...),
	}, observedAt)
	if err != nil {
		return newAuthorityFailure("probe", "codex_evidence_invalid", "Codex authority metadata is invalid", AuthorityFailureDetails{}, err, a.now())
	}
	validUntil, err := parseAuthorityTime(metadata.ValidUntil)
	if err != nil || !a.now().UTC().Before(validUntil) {
		return newAuthorityFailure("probe", "codex_evidence_expired", "Codex authority evidence is expired", AuthorityFailureDetails{EvidenceDigest: metadata.EvidenceDigest}, err, a.now())
	}
	a.mu.Lock()
	a.admission = &authorityAdmission{identity: identity, metadata: metadata, validUntil: validUntil}
	a.mu.Unlock()
	return nil
}

func (a *Adapter) authorityAdmission(snapshot *executableSnapshot) (authorityAdmission, bool) {
	identity, err := snapshot.authorityExecutableIdentity()
	if err != nil {
		return authorityAdmission{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.admission == nil || a.admission.identity != identity || !a.now().UTC().Before(a.admission.validUntil) {
		return authorityAdmission{}, false
	}
	return *a.admission, true
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
