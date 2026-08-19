package app

// Cloudflare truthful live-evidence composition: the independent credentialed
// operator that signs unforgeable receipts and the independent verifier that
// consumes them through the durable atomic compare-and-consume replay
// ledger.
//
// The separation of authority is mechanical: the operator only ever calls an
// injected ReceiptSigner (it never constructs a signer, private key or
// credential), and the verifier owns the ledger (the app never forges
// consumption authority by writing to it out of band). The verifier holds
// only public trust roots; the private signing key never enters this package.

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/provider"
	"github.com/chiga0/marshal-harness/internal/provider/cloudflare"
)

// AdmitCloudflareConformance is the app composition boundary for hardened
// admission. It remains fail closed until Core supplies the authority-ledger
// transaction required by the provider boundary.
func AdmitCloudflareConformance(receipt cloudflare.ConformanceAdmissionReceipt, registration provider.ProviderRegistration, snapshot provider.ProviderCapabilitySnapshot) (provider.ConformanceEvidence, error) {
	return cloudflare.AdmitConformanceReceipt(receipt, registration, snapshot)
}

// LiveEvidenceOperator is the independent credentialed operator that signs
// unforgeable live-evidence receipts over exact Cloudflare facts. It is
// bound to one service identity and one injected ReceiptSigner; the private
// key lives behind the signer and never enters this package.
type LiveEvidenceOperator struct {
	serviceId string
	signer    cloudflare.ReceiptSigner
	now       func() time.Time
}

// NewLiveEvidenceOperator constructs the operator fail closed. The signer is
// injected and must bind a non-empty key id and the single supported
// signature algorithm.
func NewLiveEvidenceOperator(serviceId string, signer cloudflare.ReceiptSigner) (*LiveEvidenceOperator, error) {
	if strings.TrimSpace(serviceId) == "" {
		return nil, fmt.Errorf("app: live evidence operator requires a non-empty service id")
	}
	if signer == nil {
		return nil, fmt.Errorf("app: live evidence operator requires a non-nil signer")
	}
	if strings.TrimSpace(signer.KeyId()) == "" {
		return nil, fmt.Errorf("app: live evidence operator signer must bind a non-empty key id")
	}
	if signer.Algorithm() != cloudflare.SignatureAlgorithmEd25519 {
		return nil, fmt.Errorf("app: live evidence operator signer must use %s", cloudflare.SignatureAlgorithmEd25519)
	}
	return &LiveEvidenceOperator{
		serviceId: serviceId,
		signer:    signer,
		now:       time.Now,
	}, nil
}

// Sign issues one signed receipt over the given facts. The operator is bound
// to a single service identity, so facts carrying any other service id fail
// closed. The receipt is always truthful live evidence: a simulated class is
// never produced by this operator.
func (o *LiveEvidenceOperator) Sign(facts cloudflare.LiveFacts) (cloudflare.LiveEvidenceReceipt, error) {
	if facts.ServiceId != o.serviceId {
		return cloudflare.LiveEvidenceReceipt{}, fmt.Errorf("%w: the operator is bound to service %q", cloudflare.ErrLiveEvidenceBindingMismatch, o.serviceId)
	}
	receipt := cloudflare.LiveEvidenceReceipt{
		EvidenceClass:      cloudflare.LiveEvidenceClassLive,
		Facts:              facts,
		TrustRootKeyId:     o.signer.KeyId(),
		SignatureAlgorithm: o.signer.Algorithm(),
		SignedAt:           o.now().UTC().Format(time.RFC3339),
	}
	digest, err := receipt.Digest()
	if err != nil {
		return cloudflare.LiveEvidenceReceipt{}, err
	}
	receipt.ReceiptDigest = digest
	signature, err := o.signer.Sign(digest)
	if err != nil {
		return cloudflare.LiveEvidenceReceipt{}, fmt.Errorf("app: live evidence sign: %w", err)
	}
	receipt.Signature = base64.StdEncoding.EncodeToString(signature)
	return receipt, nil
}

// LiveEvidenceBinding freezes the exact service/run/attempt/allocation/
// generation binding a verifier expects. Any receipt whose facts do not match
// exactly fails closed.
type LiveEvidenceBinding struct {
	ServiceId    string
	RunId        string
	AttemptId    string
	AllocationId string
	Generation   int64
}

// LiveEvidenceVerifier is the independent verifier that admits one
// live-evidence receipt exactly once. It holds public trust roots and owns
// the durable replay ledger; a receipt is admitted only when its signature
// verifies against the bound trust root, its class is truthful live, its
// live result is available, and its facts match the expected binding, and
// then only through the ledger's compare-and-consume.
type LiveEvidenceVerifier struct {
	trustRoots map[string]cloudflare.TrustRoot
	ledger     *cloudflare.LiveEvidenceLedger
}

// NewLiveEvidenceVerifier constructs the verifier fail closed: at least one
// valid trust root and a non-nil ledger are required.
func NewLiveEvidenceVerifier(trustRoots map[string]cloudflare.TrustRoot, ledger *cloudflare.LiveEvidenceLedger) (*LiveEvidenceVerifier, error) {
	if ledger == nil {
		return nil, fmt.Errorf("app: live evidence verifier requires a durable ledger")
	}
	if len(trustRoots) == 0 {
		return nil, fmt.Errorf("app: live evidence verifier requires at least one trust root")
	}
	copied := make(map[string]cloudflare.TrustRoot, len(trustRoots))
	for keyId, root := range trustRoots {
		if keyId == "" || root.KeyId == "" || keyId != root.KeyId {
			return nil, fmt.Errorf("app: trust root map key must match the trust root key id")
		}
		if err := root.Validate(); err != nil {
			return nil, fmt.Errorf("app: invalid trust root %q: %w", keyId, err)
		}
		copied[keyId] = root
	}
	return &LiveEvidenceVerifier{trustRoots: copied, ledger: ledger}, nil
}

// Verify checks one receipt without consuming it: structural validation,
// truthful-live class, available live result, trust-root resolution and
// signature verification, then the exact binding match. A verified receipt is
// admissible but not yet consumed.
func (v *LiveEvidenceVerifier) Verify(receipt cloudflare.LiveEvidenceReceipt, binding LiveEvidenceBinding) error {
	if err := receipt.Validate(); err != nil {
		return err
	}
	if receipt.EvidenceClass != cloudflare.LiveEvidenceClassLive {
		return fmt.Errorf("%w: evidenceClass %q", cloudflare.ErrLiveEvidenceSimulated, string(receipt.EvidenceClass))
	}
	if receipt.Facts.LiveResult == cloudflare.LiveResultUnavailable {
		return cloudflare.ErrLiveEvidenceUnavailable
	}
	root, ok := v.trustRoots[receipt.TrustRootKeyId]
	if !ok {
		return fmt.Errorf("%w: trust root %q is not configured", cloudflare.ErrLiveEvidenceTrustRootMissing, receipt.TrustRootKeyId)
	}
	if err := root.Verify(receipt.ReceiptDigest, receipt.Signature); err != nil {
		return err
	}
	return v.matchBinding(receipt.Facts, binding)
}

// Consume verifies one receipt and then consumes it through the durable
// ledger's compare-and-consume. The identical receipt therefore succeeds
// exactly once across restarts, concurrency and failure recovery: a replay
// is rejected fail closed.
func (v *LiveEvidenceVerifier) Consume(receipt cloudflare.LiveEvidenceReceipt, binding LiveEvidenceBinding) (bool, error) {
	if err := v.Verify(receipt, binding); err != nil {
		return false, err
	}
	consumed, err := v.ledger.Consume(receipt.ReceiptDigest)
	if err != nil {
		return false, err
	}
	return consumed, nil
}

// Ledger exposes the verifier-owned durable ledger for observation only. It
// returns the ledger itself, whose Consumed/Count/ConsumedDigests methods are
// read-only.
func (v *LiveEvidenceVerifier) Ledger() *cloudflare.LiveEvidenceLedger {
	return v.ledger
}

// matchBinding fails closed unless the receipt facts carry the exact
// service/run/attempt/allocation/generation binding.
func (v *LiveEvidenceVerifier) matchBinding(facts cloudflare.LiveFacts, binding LiveEvidenceBinding) error {
	if facts.ServiceId != binding.ServiceId ||
		facts.RunId != binding.RunId ||
		facts.AttemptId != binding.AttemptId ||
		facts.AllocationId != binding.AllocationId ||
		facts.Generation != binding.Generation {
		return fmt.Errorf("%w: receipt facts do not exactly match the expected binding", cloudflare.ErrLiveEvidenceBindingMismatch)
	}
	return nil
}
