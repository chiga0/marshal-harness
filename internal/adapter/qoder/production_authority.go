package qoder

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

const (
	exactAuthorityDocumentLimit = 64 << 10
	exactAuthorityAPIVersion    = "marshal.dev/v1alpha1"
	exactSignatureAlgorithm     = "Ed25519"
	exactSignatureEncoding      = "base64url-unpadded"
	osTrustRootSigningDomain    = "marshal-qoder-os-trust-root-v1\x00"
	osTrustAnchorSigningDomain  = "marshal-qoder-os-trust-anchor-v1\x00"
	hostIdentitySigningDomain   = "marshal-qoder-host-identity-v1\x00"
	fenceGenesisSigningDomain   = "marshal-qoder-fence-genesis-v1\x00"
	fenceAdvanceSigningDomain   = "marshal-qoder-fence-advance-v1\x00"
)

// QoderOSTrustRootRecord is the exact, non-secret trust-ledger record frozen
// by ADR 0034. Pointer members encode required JSON nulls without omitting the
// member; callers cannot use this type to carry private key material.
type QoderOSTrustRootRecord struct {
	APIVersion                      string  `json:"apiVersion"`
	Kind                            string  `json:"kind"`
	SchemaVersion                   uint64  `json:"schemaVersion"`
	TrustDomainID                   string  `json:"trustDomainId"`
	RootSequence                    uint64  `json:"rootSequence"`
	Role                            string  `json:"role"`
	KeyID                           string  `json:"keyId"`
	KeyEpoch                        uint64  `json:"keyEpoch"`
	Operation                       string  `json:"operation"`
	PublicKeyEncoding               string  `json:"publicKeyEncoding"`
	Ed25519PublicKey                string  `json:"ed25519PublicKey"`
	PublicKeyDigest                 string  `json:"publicKeyDigest"`
	PreviousRecordDigest            *string `json:"previousRecordDigest"`
	EffectiveAt                     string  `json:"effectiveAt"`
	AnchorProviderIdentity          string  `json:"anchorProviderIdentity"`
	AnchorProviderCounter           uint64  `json:"anchorProviderCounter"`
	AuthorizingKeyID                *string `json:"authorizingKeyId"`
	AuthorizingKeyEpoch             *uint64 `json:"authorizingKeyEpoch"`
	AuthorizationSignatureAlgorithm *string `json:"authorizationSignatureAlgorithm"`
	AuthorizationSignatureEncoding  *string `json:"authorizationSignatureEncoding"`
	AuthorizationSignature          *string `json:"authorizationSignature"`
	RecordDigest                    string  `json:"recordDigest"`
}

type QoderOSTrustAnchorReceipt struct {
	APIVersion                  string  `json:"apiVersion"`
	Kind                        string  `json:"kind"`
	SchemaVersion               uint64  `json:"schemaVersion"`
	AnchorProviderIdentity      string  `json:"anchorProviderIdentity"`
	TrustDomainID               string  `json:"trustDomainId"`
	RootSequence                uint64  `json:"rootSequence"`
	RootRecordDigest            string  `json:"rootRecordDigest"`
	PreviousRootRecordDigest    *string `json:"previousRootRecordDigest"`
	AnchorProviderCounter       uint64  `json:"anchorProviderCounter"`
	PreviousAnchorReceiptDigest *string `json:"previousAnchorReceiptDigest"`
	ObservedAt                  string  `json:"observedAt"`
	ProviderKeyID               string  `json:"providerKeyId"`
	ProviderKeyEpoch            uint64  `json:"providerKeyEpoch"`
	SignatureAlgorithm          string  `json:"signatureAlgorithm"`
	SignatureEncoding           string  `json:"signatureEncoding"`
	Signature                   string  `json:"signature"`
	RecordDigest                string  `json:"recordDigest"`
}

type HostAttestationIdentity struct {
	APIVersion                      string  `json:"apiVersion"`
	Kind                            string  `json:"kind"`
	SchemaVersion                   uint64  `json:"schemaVersion"`
	ProviderIdentity                string  `json:"providerIdentity"`
	HostKeyID                       string  `json:"hostKeyId"`
	HostKeyEpoch                    uint64  `json:"hostKeyEpoch"`
	HostPublicKeyEncoding           string  `json:"hostPublicKeyEncoding"`
	HostPublicKeyDigest             string  `json:"hostPublicKeyDigest"`
	OSAttestedMachineIdentityDigest string  `json:"osAttestedMachineIdentityDigest"`
	OS                              string  `json:"os"`
	Arch                            string  `json:"arch"`
	IssuedAt                        string  `json:"issuedAt"`
	PreviousHostIdentityDigest      *string `json:"previousHostIdentityDigest"`
	ProviderKeyID                   string  `json:"providerKeyId"`
	ProviderKeyEpoch                uint64  `json:"providerKeyEpoch"`
	SignatureAlgorithm              string  `json:"signatureAlgorithm"`
	SignatureEncoding               string  `json:"signatureEncoding"`
	Signature                       string  `json:"signature"`
	RecordDigest                    string  `json:"recordDigest"`
}

type ConsumerFenceGenesisRequest struct {
	ConsumerInstanceID string
	RepositoryIdentity string
}

type ConsumerFenceAdvanceRequest struct {
	ConsumerInstanceID            string
	RepositoryIdentity            string
	TransactionID                 string
	PreparedRecordDigest          string
	AuthorityGeneration           uint64
	ConfigDigest                  string
	ExpectedProviderCounter       uint64
	ExpectedPreviousReceiptDigest *string
}

type ConsumerFenceReceipt struct {
	APIVersion            string  `json:"apiVersion"`
	Kind                  string  `json:"kind"`
	SchemaVersion         uint64  `json:"schemaVersion"`
	ProviderIdentity      string  `json:"providerIdentity"`
	ConsumerInstanceID    string  `json:"consumerInstanceId"`
	RepositoryIdentity    string  `json:"repositoryIdentity"`
	ProviderCounter       uint64  `json:"providerCounter"`
	TransactionID         string  `json:"transactionId"`
	PreparedRecordDigest  *string `json:"preparedRecordDigest"`
	AuthorityGeneration   uint64  `json:"authorityGeneration"`
	ConfigDigest          *string `json:"configDigest"`
	PreviousReceiptDigest *string `json:"previousReceiptDigest"`
	ObservedAt            string  `json:"observedAt"`
	ProviderKeyID         string  `json:"providerKeyId"`
	ProviderKeyEpoch      uint64  `json:"providerKeyEpoch"`
	SignatureAlgorithm    string  `json:"signatureAlgorithm"`
	SignatureEncoding     string  `json:"signatureEncoding"`
	Signature             string  `json:"signature"`
	RecordDigest          string  `json:"recordDigest"`
}

// The three providers are deliberately separate principals. Marshal receives
// only signed public records and cannot mint, reset, or advance any provider.
type QoderOSTrustAnchorProvider interface {
	CurrentTrustRoot(context.Context) ([]byte, []byte, error)
}

type HostAttestationProvider interface {
	CurrentHostIdentity(context.Context) ([]byte, error)
}

type ConsumerFenceAnchorProvider interface {
	Genesis(context.Context, ConsumerFenceGenesisRequest) ([]byte, error)
	CompareAndAdvance(context.Context, ConsumerFenceAdvanceRequest) ([]byte, error)
}

type ExactAuthorityProviders struct {
	osTrust QoderOSTrustAnchorProvider
	host    HostAttestationProvider
	fence   ConsumerFenceAnchorProvider
}

func NewExactAuthorityProviders(osTrust QoderOSTrustAnchorProvider, host HostAttestationProvider, fence ConsumerFenceAnchorProvider) (*ExactAuthorityProviders, error) {
	if osTrust == nil || host == nil || fence == nil {
		return nil, errors.New("qoder exact authority providers are incomplete")
	}
	return &ExactAuthorityProviders{osTrust: osTrust, host: host, fence: fence}, nil
}

func (providers *ExactAuthorityProviders) CurrentTrustRoot(ctx context.Context) (QoderOSTrustRootRecord, QoderOSTrustAnchorReceipt, error) {
	if providers == nil || providers.osTrust == nil || ctx == nil || ctx.Err() != nil {
		return QoderOSTrustRootRecord{}, QoderOSTrustAnchorReceipt{}, errors.New("qoder OS trust provider is unavailable")
	}
	recordDocument, receiptDocument, err := providers.osTrust.CurrentTrustRoot(ctx)
	if err != nil {
		return QoderOSTrustRootRecord{}, QoderOSTrustAnchorReceipt{}, errors.New("qoder OS trust provider failed")
	}
	record, err := DecodeQoderOSTrustRootRecord(recordDocument)
	if err != nil {
		return QoderOSTrustRootRecord{}, QoderOSTrustAnchorReceipt{}, err
	}
	receipt, err := DecodeQoderOSTrustAnchorReceipt(receiptDocument)
	if err != nil {
		return QoderOSTrustRootRecord{}, QoderOSTrustAnchorReceipt{}, err
	}
	return record, receipt, nil
}

func (providers *ExactAuthorityProviders) CurrentHostIdentity(ctx context.Context) (HostAttestationIdentity, error) {
	if providers == nil || providers.host == nil || ctx == nil || ctx.Err() != nil {
		return HostAttestationIdentity{}, errors.New("qoder host attestation provider is unavailable")
	}
	document, err := providers.host.CurrentHostIdentity(ctx)
	if err != nil {
		return HostAttestationIdentity{}, errors.New("qoder host attestation provider failed")
	}
	return DecodeHostAttestationIdentity(document)
}

func (providers *ExactAuthorityProviders) FenceGenesis(ctx context.Context, request ConsumerFenceGenesisRequest) (ConsumerFenceReceipt, error) {
	if providers == nil || providers.fence == nil || ctx == nil || ctx.Err() != nil {
		return ConsumerFenceReceipt{}, errors.New("qoder consumer fence provider is unavailable")
	}
	document, err := providers.fence.Genesis(ctx, request)
	if err != nil {
		return ConsumerFenceReceipt{}, errors.New("qoder consumer fence provider failed")
	}
	return DecodeConsumerFenceReceipt(document)
}

func (providers *ExactAuthorityProviders) FenceCompareAndAdvance(ctx context.Context, request ConsumerFenceAdvanceRequest) (ConsumerFenceReceipt, error) {
	if providers == nil || providers.fence == nil || ctx == nil || ctx.Err() != nil {
		return ConsumerFenceReceipt{}, errors.New("qoder consumer fence provider is unavailable")
	}
	document, err := providers.fence.CompareAndAdvance(ctx, request)
	if err != nil {
		return ConsumerFenceReceipt{}, errors.New("qoder consumer fence provider failed")
	}
	return DecodeConsumerFenceReceipt(document)
}

func DecodeQoderOSTrustRootRecord(document []byte) (QoderOSTrustRootRecord, error) {
	return decodeExactAuthorityDocument[QoderOSTrustRootRecord](document)
}

func DecodeQoderOSTrustAnchorReceipt(document []byte) (QoderOSTrustAnchorReceipt, error) {
	return decodeExactAuthorityDocument[QoderOSTrustAnchorReceipt](document)
}

func DecodeHostAttestationIdentity(document []byte) (HostAttestationIdentity, error) {
	return decodeExactAuthorityDocument[HostAttestationIdentity](document)
}

func DecodeConsumerFenceReceipt(document []byte) (ConsumerFenceReceipt, error) {
	return decodeExactAuthorityDocument[ConsumerFenceReceipt](document)
}

func decodeExactAuthorityDocument[T any](document []byte) (T, error) {
	var zero T
	if len(document) == 0 || len(document) > exactAuthorityDocumentLimit {
		return zero, errors.New("qoder exact authority document is empty or oversized")
	}
	canonicalDocument, err := canonical.JSON(document)
	if err != nil || !bytes.Equal(document, canonicalDocument) {
		return zero, errors.New("qoder exact authority document is not canonical")
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var value T
	if err := decoder.Decode(&value); err != nil {
		return zero, errors.New("qoder exact authority document is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return zero, errors.New("qoder exact authority document is invalid")
	}
	return value, nil
}

func ValidateQoderOSTrustRootRecord(record QoderOSTrustRootRecord, authorizingKeyID string, authorizingKeyEpoch uint64, authorizingKey ed25519.PublicKey, now time.Time) error {
	publicKey, publicErr := decodeCandidateRawURL(record.Ed25519PublicKey)
	effectiveAt, timeErr := time.Parse(time.RFC3339Nano, record.EffectiveAt)
	if record.APIVersion != exactAuthorityAPIVersion || record.Kind != "QoderOSTrustRootRecord" || record.SchemaVersion != 1 || !validCandidateASCII(record.TrustDomainID) || !validTrustRootRole(record.Role) || !validCandidateASCII(record.KeyID) || record.KeyEpoch > candidateMaxJSONInteger || record.PublicKeyEncoding != exactSignatureEncoding || publicErr != nil || len(publicKey) != ed25519.PublicKeySize || record.PublicKeyDigest != digestBytes(publicKey) || !validSHA256Digest(record.RecordDigest) || timeErr != nil || !validCandidateTimestamp(record.EffectiveAt) || effectiveAt.After(now) || record.AnchorProviderCounter != record.RootSequence {
		return errors.New("qoder OS trust root record is invalid")
	}
	if record.Operation != "activate" && record.Operation != "revoke" {
		return errors.New("qoder OS trust root operation is invalid")
	}
	if record.RecordDigest != digestRecordWithoutFields(record, "authorizationSignature", "recordDigest") {
		return errors.New("qoder OS trust root digest is invalid")
	}
	if record.RootSequence == 0 {
		if record.Role != "trust-ledger-operator" || record.KeyEpoch != 0 || record.Operation != "activate" || record.PreviousRecordDigest != nil || record.AuthorizingKeyID != nil || record.AuthorizingKeyEpoch != nil || record.AuthorizationSignatureAlgorithm != nil || record.AuthorizationSignatureEncoding != nil || record.AuthorizationSignature != nil {
			return errors.New("qoder OS trust root genesis is invalid")
		}
		return nil
	}
	if !validCandidateASCII(authorizingKeyID) || authorizingKeyEpoch > candidateMaxJSONInteger || record.PreviousRecordDigest == nil || !validSHA256Digest(*record.PreviousRecordDigest) || record.AuthorizingKeyID == nil || *record.AuthorizingKeyID != authorizingKeyID || record.AuthorizingKeyEpoch == nil || *record.AuthorizingKeyEpoch != authorizingKeyEpoch || record.AuthorizationSignatureAlgorithm == nil || *record.AuthorizationSignatureAlgorithm != exactSignatureAlgorithm || record.AuthorizationSignatureEncoding == nil || *record.AuthorizationSignatureEncoding != exactSignatureEncoding || record.AuthorizationSignature == nil || len(authorizingKey) != ed25519.PublicKeySize {
		return errors.New("qoder OS trust root authorization is incomplete")
	}
	signature, err := decodeCandidateRawURL(*record.AuthorizationSignature)
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(authorizingKey, []byte(osTrustRootSigningDomain+record.RecordDigest), signature) {
		return errors.New("qoder OS trust root authorization is not trusted")
	}
	return nil
}

func ValidateQoderOSTrustAnchorReceipt(receipt QoderOSTrustAnchorReceipt, root QoderOSTrustRootRecord, providerIdentity, providerKeyID string, providerKeyEpoch uint64, providerKey ed25519.PublicKey, now time.Time) error {
	observedAt, timeErr := time.Parse(time.RFC3339Nano, receipt.ObservedAt)
	if !validCandidateASCII(providerIdentity) || !validCandidateASCII(providerKeyID) || providerKeyEpoch > candidateMaxJSONInteger || receipt.APIVersion != exactAuthorityAPIVersion || receipt.Kind != "QoderOSTrustAnchorReceipt" || receipt.SchemaVersion != 1 || receipt.AnchorProviderIdentity != providerIdentity || receipt.ProviderKeyID != providerKeyID || receipt.ProviderKeyEpoch != providerKeyEpoch || receipt.TrustDomainID != root.TrustDomainID || receipt.RootSequence != root.RootSequence || receipt.RootRecordDigest != root.RecordDigest || receipt.AnchorProviderCounter != root.AnchorProviderCounter || !sameOptionalDigest(receipt.PreviousRootRecordDigest, root.PreviousRecordDigest) || !validOptionalDigest(receipt.PreviousAnchorReceiptDigest) || receipt.SignatureAlgorithm != exactSignatureAlgorithm || receipt.SignatureEncoding != exactSignatureEncoding || timeErr != nil || !validCandidateTimestamp(receipt.ObservedAt) || observedAt.After(now) || receipt.RecordDigest != digestRecordWithoutFields(receipt, "signature", "recordDigest") {
		return errors.New("qoder OS trust anchor receipt is invalid")
	}
	signature, err := decodeCandidateRawURL(receipt.Signature)
	if err != nil || len(providerKey) != ed25519.PublicKeySize || !ed25519.Verify(providerKey, []byte(osTrustAnchorSigningDomain+receipt.RecordDigest), signature) {
		return errors.New("qoder OS trust anchor receipt is not trusted")
	}
	return nil
}

func ValidateHostAttestationIdentity(identity HostAttestationIdentity, providerIdentity, providerKeyID string, providerKeyEpoch uint64, providerKey ed25519.PublicKey, now time.Time) error {
	issuedAt, timeErr := time.Parse(time.RFC3339Nano, identity.IssuedAt)
	if !validCandidateASCII(providerIdentity) || !validCandidateASCII(providerKeyID) || providerKeyEpoch > candidateMaxJSONInteger || identity.APIVersion != exactAuthorityAPIVersion || identity.Kind != "HostAttestationIdentity" || identity.SchemaVersion != 1 || identity.ProviderIdentity != providerIdentity || identity.ProviderKeyID != providerKeyID || identity.ProviderKeyEpoch != providerKeyEpoch || !validCandidateASCII(identity.HostKeyID) || identity.HostKeyEpoch > candidateMaxJSONInteger || identity.HostPublicKeyEncoding != exactSignatureEncoding || !validSHA256Digest(identity.HostPublicKeyDigest) || !validSHA256Digest(identity.OSAttestedMachineIdentityDigest) || identity.OS != runtime.GOOS || identity.Arch != runtime.GOARCH || !validOptionalDigest(identity.PreviousHostIdentityDigest) || identity.SignatureAlgorithm != exactSignatureAlgorithm || identity.SignatureEncoding != exactSignatureEncoding || timeErr != nil || !validCandidateTimestamp(identity.IssuedAt) || issuedAt.After(now) || identity.RecordDigest != digestRecordWithoutFields(identity, "signature", "recordDigest") {
		return errors.New("qoder host attestation identity is invalid")
	}
	signature, err := decodeCandidateRawURL(identity.Signature)
	if err != nil || len(providerKey) != ed25519.PublicKeySize || !ed25519.Verify(providerKey, []byte(hostIdentitySigningDomain+identity.RecordDigest), signature) {
		return errors.New("qoder host attestation identity is not trusted")
	}
	return nil
}

func ValidateConsumerFenceReceipt(receipt ConsumerFenceReceipt, request ConsumerFenceAdvanceRequest, providerIdentity, providerKeyID string, providerKeyEpoch uint64, providerKey ed25519.PublicKey, now time.Time) error {
	observedAt, timeErr := time.Parse(time.RFC3339Nano, receipt.ObservedAt)
	if !validCandidateASCII(providerIdentity) || !validCandidateASCII(providerKeyID) || providerKeyEpoch > candidateMaxJSONInteger || !validCandidateASCII(request.ConsumerInstanceID) || !validCandidateASCII(request.RepositoryIdentity) || !validCandidateASCII(request.TransactionID) || !validSHA256Digest(request.PreparedRecordDigest) || request.AuthorityGeneration == 0 || request.AuthorityGeneration > candidateMaxJSONInteger || !validSHA256Digest(request.ConfigDigest) || request.ExpectedProviderCounter == ^uint64(0) || receipt.APIVersion != exactAuthorityAPIVersion || receipt.Kind != "QoderConsumerFenceAdvanceReceipt" || receipt.SchemaVersion != 1 || receipt.ProviderIdentity != providerIdentity || receipt.ConsumerInstanceID != request.ConsumerInstanceID || receipt.RepositoryIdentity != request.RepositoryIdentity || receipt.TransactionID != request.TransactionID || receipt.PreparedRecordDigest == nil || *receipt.PreparedRecordDigest != request.PreparedRecordDigest || receipt.AuthorityGeneration != request.AuthorityGeneration || receipt.ConfigDigest == nil || *receipt.ConfigDigest != request.ConfigDigest || receipt.ProviderCounter != request.ExpectedProviderCounter+1 || !sameOptionalDigest(receipt.PreviousReceiptDigest, request.ExpectedPreviousReceiptDigest) || receipt.ProviderKeyID != providerKeyID || receipt.ProviderKeyEpoch != providerKeyEpoch || receipt.SignatureAlgorithm != exactSignatureAlgorithm || receipt.SignatureEncoding != exactSignatureEncoding || timeErr != nil || !validCandidateTimestamp(receipt.ObservedAt) || observedAt.After(now) || receipt.RecordDigest != digestRecordWithoutFields(receipt, "signature", "recordDigest") {
		return errors.New("qoder consumer fence receipt is invalid")
	}
	signature, err := decodeCandidateRawURL(receipt.Signature)
	if err != nil || len(providerKey) != ed25519.PublicKeySize || !ed25519.Verify(providerKey, []byte(fenceAdvanceSigningDomain+receipt.RecordDigest), signature) {
		return errors.New("qoder consumer fence receipt is not trusted")
	}
	return nil
}

func ValidateConsumerFenceGenesisReceipt(receipt ConsumerFenceReceipt, request ConsumerFenceGenesisRequest, providerIdentity, providerKeyID string, providerKeyEpoch uint64, providerKey ed25519.PublicKey, now time.Time) error {
	observedAt, timeErr := time.Parse(time.RFC3339Nano, receipt.ObservedAt)
	if !validCandidateASCII(request.ConsumerInstanceID) || !validCandidateASCII(request.RepositoryIdentity) || receipt.APIVersion != exactAuthorityAPIVersion || receipt.Kind != "QoderConsumerFenceGenesisReceipt" || receipt.SchemaVersion != 1 || receipt.ProviderIdentity != providerIdentity || receipt.ConsumerInstanceID != request.ConsumerInstanceID || receipt.RepositoryIdentity != request.RepositoryIdentity || receipt.ProviderCounter != 0 || receipt.TransactionID != "genesis" || receipt.PreparedRecordDigest != nil || receipt.AuthorityGeneration != 0 || receipt.ConfigDigest != nil || receipt.PreviousReceiptDigest != nil || receipt.ProviderKeyID != providerKeyID || receipt.ProviderKeyEpoch != providerKeyEpoch || receipt.SignatureAlgorithm != exactSignatureAlgorithm || receipt.SignatureEncoding != exactSignatureEncoding || timeErr != nil || !validCandidateTimestamp(receipt.ObservedAt) || observedAt.After(now) || receipt.RecordDigest != digestRecordWithoutFields(receipt, "signature", "recordDigest") {
		return errors.New("qoder consumer fence genesis receipt is invalid")
	}
	signature, err := decodeCandidateRawURL(receipt.Signature)
	if err != nil || len(providerKey) != ed25519.PublicKeySize || !ed25519.Verify(providerKey, []byte(fenceGenesisSigningDomain+receipt.RecordDigest), signature) {
		return errors.New("qoder consumer fence genesis receipt is not trusted")
	}
	return nil
}

func validTrustRootRole(role string) bool {
	switch role {
	case "trust-ledger-operator", "host-attestation-provider", "consumer-fence-provider", "credential-capability-provider":
		return true
	default:
		return false
	}
}

func sameOptionalDigest(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right && validSHA256Digest(*left)
}

func validOptionalDigest(value *string) bool {
	return value == nil || validSHA256Digest(*value)
}
