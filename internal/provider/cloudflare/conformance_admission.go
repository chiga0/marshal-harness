package cloudflare

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/provider"
)

var ErrConformanceAdmission = errors.New("cloudflare conformance admission rejected")

const IndependentVerifierRole = "independent-verifier"

// ConformanceAdmissionReceipt is the independently signed, short-lived input
// from which hardened ConformanceEvidence may be derived. Its digest binds all
// identity, freshness, revocation and four-dimensional result facts.
type ConformanceAdmissionReceipt struct {
	ReceiptDigest                    string                                                     `json:"receiptDigest"`
	AuthorityNamespaceId             authority.AuthorityNamespaceId                             `json:"authorityNamespaceId"`
	SecurityDomainId                 authority.SecurityDomainId                                 `json:"securityDomainId"`
	ProviderInstanceId               string                                                     `json:"providerInstanceId"`
	RegistrationId                   string                                                     `json:"registrationId"`
	RegistrationDigest               string                                                     `json:"registrationDigest"`
	ProviderCapabilitySnapshotDigest string                                                     `json:"providerCapabilitySnapshotDigest"`
	ConfigDigest                     string                                                     `json:"configDigest"`
	TrustRootKeyId                   string                                                     `json:"trustRootKeyId"`
	VerifierId                       string                                                     `json:"verifierId"`
	VerifierRole                     string                                                     `json:"verifierRole"`
	SuiteName                        string                                                     `json:"suiteName"`
	Generation                       int64                                                      `json:"generation"`
	DimensionResults                 map[provider.ConformanceDimension]provider.DimensionResult `json:"dimensionResults"`
	ValidFrom                        string                                                     `json:"validFrom"`
	ValidUntil                       string                                                     `json:"validUntil"`
	Revoked                          bool                                                       `json:"revoked"`
	EvidenceClass                    LiveEvidenceClass                                          `json:"evidenceClass"`
	SignatureAlgorithm               string                                                     `json:"signatureAlgorithm"`
	Signature                        string                                                     `json:"signature"`
}

func (receipt ConformanceAdmissionReceipt) validateContent() error {
	if err := receipt.AuthorityNamespaceId.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrConformanceAdmission, err)
	}
	if err := receipt.SecurityDomainId.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrConformanceAdmission, err)
	}
	for name, value := range map[string]string{
		"providerInstanceId": receipt.ProviderInstanceId, "registrationId": receipt.RegistrationId,
		"verifierId": receipt.VerifierId, "suiteName": receipt.SuiteName, "trustRootKeyId": receipt.TrustRootKeyId,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: %s must be non-empty", ErrConformanceAdmission, name)
		}
	}
	for name, value := range map[string]string{
		"registrationDigest": receipt.RegistrationDigest, "providerCapabilitySnapshotDigest": receipt.ProviderCapabilitySnapshotDigest,
		"configDigest": receipt.ConfigDigest,
	} {
		if err := requireLiveDigest(name, value); err != nil {
			return fmt.Errorf("%w: %v", ErrConformanceAdmission, err)
		}
	}
	if receipt.VerifierRole != IndependentVerifierRole || receipt.VerifierId == "worker" {
		return fmt.Errorf("%w: verifier must be independent of the worker and provider", ErrConformanceAdmission)
	}
	if receipt.Generation < 1 {
		return fmt.Errorf("%w: generation must be positive", ErrConformanceAdmission)
	}
	if len(receipt.DimensionResults) != 4 {
		return fmt.Errorf("%w: dimensionResults must cover exactly four dimensions", ErrConformanceAdmission)
	}
	for _, dimension := range []provider.ConformanceDimension{provider.ConformanceDimensionMount, provider.ConformanceDimensionNetwork, provider.ConformanceDimensionResource, provider.ConformanceDimensionCredential} {
		result, ok := receipt.DimensionResults[dimension]
		if !ok || result != provider.DimensionResultPassed {
			return fmt.Errorf("%w: %s conformance did not pass", ErrConformanceAdmission, dimension)
		}
	}
	for dimension := range receipt.DimensionResults {
		if err := dimension.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrConformanceAdmission, err)
		}
	}
	if err := receipt.EvidenceClass.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrConformanceAdmission, err)
	}
	if receipt.EvidenceClass != LiveEvidenceClassLive {
		return fmt.Errorf("%w: simulated evidence cannot be hardened", ErrConformanceAdmission)
	}
	if receipt.SignatureAlgorithm != SignatureAlgorithmEd25519 {
		return fmt.Errorf("%w: unsupported signature algorithm", ErrConformanceAdmission)
	}
	from, err := time.Parse(time.RFC3339, receipt.ValidFrom)
	if err != nil {
		return fmt.Errorf("%w: invalid validFrom", ErrConformanceAdmission)
	}
	until, err := time.Parse(time.RFC3339, receipt.ValidUntil)
	if err != nil || !until.After(from) {
		return fmt.Errorf("%w: invalid validity interval", ErrConformanceAdmission)
	}
	return nil
}

func (receipt ConformanceAdmissionReceipt) Digest() (string, error) {
	if err := receipt.validateContent(); err != nil {
		return "", err
	}
	detached := receipt
	detached.ReceiptDigest = ""
	detached.Signature = ""
	return liveCanonicalDigest(detached)
}

func (receipt ConformanceAdmissionReceipt) Validate() error {
	if err := receipt.validateContent(); err != nil {
		return err
	}
	if receipt.ReceiptDigest == "" || receipt.Signature == "" {
		return fmt.Errorf("%w: digest and signature are required", ErrConformanceAdmission)
	}
	digest, err := receipt.Digest()
	if err != nil {
		return err
	}
	if digest != receipt.ReceiptDigest {
		return fmt.Errorf("%w: receipt digest mismatch", ErrConformanceAdmission)
	}
	return nil
}

// AdmitConformanceReceipt is intentionally fail closed until Core exposes an
// atomic authority-ledger transaction that can read the current verifier-key
// authority, revocation, generation, registration, snapshot and eligibility
// facts and append the immutable receipt/artifact/admission relation.
//
// ConformanceEvidence cannot directly represent registrationId, snapshot
// digest, generation or verifier principal. Substituting receiptDigest into
// ProbeArtifactDigest would make those bindings opaque and non-revalidatable,
// so no receipt can currently be mapped to hardened evidence at this boundary.
// The arguments remain typed to make accidental admission through a weaker
// overload impossible; they are diagnostic input only.
func AdmitConformanceReceipt(receipt ConformanceAdmissionReceipt, registration provider.ProviderRegistration, snapshot provider.ProviderCapabilitySnapshot) (provider.ConformanceEvidence, error) {
	return provider.ConformanceEvidence{}, fmt.Errorf(
		"%w: hardened admission unavailable: ConformanceEvidence and the current Core ledger cannot persist and revalidate the exact registration, snapshot, generation, verifier-authority, revocation and admission relation",
		ErrConformanceAdmission,
	)
}

func SignConformanceReceipt(receipt *ConformanceAdmissionReceipt, signer ReceiptSigner) error {
	if receipt == nil || signer == nil {
		return fmt.Errorf("%w: receipt and signer are required", ErrConformanceAdmission)
	}
	receipt.TrustRootKeyId = signer.KeyId()
	receipt.SignatureAlgorithm = signer.Algorithm()
	digest, err := receipt.Digest()
	if err != nil {
		return err
	}
	receipt.ReceiptDigest = digest
	signature, err := signer.Sign(digest)
	if err != nil {
		return fmt.Errorf("%w: signing failed", ErrConformanceAdmission)
	}
	receipt.Signature = base64.StdEncoding.EncodeToString(signature)
	return nil
}
