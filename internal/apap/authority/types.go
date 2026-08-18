// Package authority verifies the immutable APAP authority bundle delivery
// contract frozen by ADR 0038. It is a consumer-only package: it accepts
// public verification material and has no signing or private-key API.
package authority

import (
	"crypto/ed25519"
	"errors"
	"math"
	"time"
)

const (
	ManifestSchema  = "marshal.agent-authority-bundle.v1"
	SignatureSchema = "marshal.agent-authority-bundle-signature.v1"
	BundleDomain    = "marshal-agent-authority-bundle-v1\x00"
	BundleKeyUsage  = "bundle-manifest"

	maxManifestBytes = 64 << 10
	maxLeafBytes     = 1 << 20
	maxBundleBytes   = 8 << 20
	maxLeaves        = 64
)

var ErrBundleRejected = errors.New("apap authority: bundle rejected")

// SignedObjectEnvelopeV1 is the exact shared signature envelope. Verification
// is performed from canonical raw JSON so unknown and duplicate members cannot
// be hidden by Go decoding.
type SignedObjectEnvelopeV1 struct {
	ObjectDigest       string `json:"objectDigest"`
	SignatureAlgorithm string `json:"signatureAlgorithm"`
	SignatureEncoding  string `json:"signatureEncoding"`
	KeyID              string `json:"keyId"`
	KeyEpoch           uint64 `json:"keyEpoch"`
	SignatureDomain    string `json:"signatureDomain"`
	Signature          string `json:"signature"`
}

// PublicKeyRecord is provisioned trust material. PublicKey must be exactly an
// Ed25519 public key; an Ed25519 private key is therefore rejected by length.
type PublicKeyRecord struct {
	KeyID                   string
	KeyEpoch                uint64
	Usage                   string
	ProducerPrincipalDigest string
	PublicKey               ed25519.PublicKey
	ValidFrom               time.Time
	ValidUntil              time.Time
	RevokedAt               *time.Time
}

// PublicKeyring stores a defensive copy of consumer-visible public trust
// material. It intentionally exposes no mutation or signing surface.
type PublicKeyring struct{ records []PublicKeyRecord }

func NewPublicKeyring(records []PublicKeyRecord) (PublicKeyring, error) {
	copyRecords := make([]PublicKeyRecord, len(records))
	seen := make(map[string]struct{}, len(records))
	for i, record := range records {
		if len(record.PublicKey) != ed25519.PublicKeySize || !validKeyID(record.KeyID) || record.KeyEpoch > math.MaxInt64 || record.Usage == "" || !validDigest(record.ProducerPrincipalDigest) || record.ValidFrom.IsZero() || record.ValidUntil.IsZero() || !record.ValidUntil.After(record.ValidFrom) {
			return PublicKeyring{}, ErrBundleRejected
		}
		identity := record.KeyID + "\x00" + uintString(record.KeyEpoch) + "\x00" + record.Usage + "\x00" + record.ProducerPrincipalDigest
		if _, duplicate := seen[identity]; duplicate {
			return PublicKeyring{}, ErrBundleRejected
		}
		seen[identity] = struct{}{}
		copyRecords[i] = record
		copyRecords[i].PublicKey = append(ed25519.PublicKey(nil), record.PublicKey...)
		if record.RevokedAt != nil {
			revokedAt := record.RevokedAt.UTC()
			copyRecords[i].RevokedAt = &revokedAt
		}
	}
	return PublicKeyring{records: copyRecords}, nil
}

type AuthorityBundleLeafV1 struct {
	LeafKind  string `json:"leafKind"`
	Digest    string `json:"digest"`
	Size      uint64 `json:"size"`
	MediaType string `json:"mediaType"`
}

type AuthorityBundleManifestV1 struct {
	SchemaVersion        string                  `json:"schemaVersion"`
	ProviderInstanceID   string                  `json:"providerInstanceId"`
	AuthorityProfile     string                  `json:"authorityProfile"`
	HostIdentityDigest   string                  `json:"hostIdentityDigest"`
	ProviderSequence     uint64                  `json:"providerSequence"`
	AuthorityGeneration  uint64                  `json:"authorityGeneration"`
	TrustRootGeneration  uint64                  `json:"trustRootGeneration"`
	KeysetDigest         string                  `json:"keysetDigest"`
	RevocationSetDigest  string                  `json:"revocationSetDigest"`
	ConfigDigest         string                  `json:"configDigest"`
	EvidenceDigest       string                  `json:"evidenceDigest"`
	ProfileLeaves        []AuthorityBundleLeafV1 `json:"profileLeaves"`
	CreatedAt            string                  `json:"createdAt"`
	ValidUntil           string                  `json:"validUntil"`
	PreviousBundleDigest *string                 `json:"previousBundleDigest"`
	TransactionID        string                  `json:"transactionId"`
}

// ImmutableLeaf is a held leaf's non-secret byte projection. The caller must
// retain the underlying held handle while these bytes are read; this verifier
// rejects any mismatch with the immutable manifest descriptor.
type ImmutableLeaf struct {
	LeafKind string
	Content  []byte
}

// CurrentSnapshot is the external current/anchor projection to which a bundle
// must bind exactly. RevokedObjectDigests is already authenticated by the
// profile-specific revocation verifier whose digest is RevocationSetDigest.
type CurrentSnapshot struct {
	ProviderInstanceID     string
	AuthorityProfile       string
	HostIdentityDigest     string
	BundleDigest           string
	ProviderSequence       uint64
	AuthorityGeneration    uint64
	TrustRootGeneration    uint64
	KeysetDigest           string
	RevocationSetDigest    string
	ConfigDigest           string
	EvidenceDigest         string
	FenceDigest            string
	ManifestProducerDigest string
	RevokedObjectDigests   []string
}

// HighWaterState is the consumer's durable observed-current high-water. It can
// represent an ineligible poison value that prevents fallback to an older bundle.
type HighWaterState struct {
	Initialized         bool
	ProviderInstanceID  string
	AuthorityProfile    string
	BundleDigest        string
	ProviderSequence    uint64
	AuthorityGeneration uint64
	TrustRootGeneration uint64
	KeysetDigest        string
	RevocationSetDigest string
	ConfigDigest        string
	EvidenceDigest      string
	FenceDigest         string
}

type VerifiedBundle struct {
	Manifest     AuthorityBundleManifestV1
	BundleDigest string
}

// VerificationResult separates observation of the externally anchored current
// identity from eligibility. When MustPersistObserved is true, the caller must
// durably store ObservedCurrent before returning the verification error or
// using any older bundle. Eligible is true only after every bundle gate passes.
type VerificationResult struct {
	ObservedCurrent     HighWaterState
	MustPersistObserved bool
	Eligible            bool
	Bundle              VerifiedBundle
}

type profileLeafPolicy struct {
	requiredKinds  []string
	keysetKind     string
	revocationKind string
	configKind     string
	evidenceKind   string
	fenceKind      string
}

func profilePolicy(profile string) (profileLeafPolicy, bool) {
	prefix := ""
	switch profile {
	case "qoder-cli-adr0034-v1":
		prefix = "qoder"
	case "codex-cli-adr0037-v1":
		prefix = "codex"
	default:
		return profileLeafPolicy{}, false
	}
	return profileLeafPolicy{
		requiredKinds: []string{prefix + "-config", prefix + "-evidence", prefix + "-fence", prefix + "-host-attestation", prefix + "-keyset", prefix + "-policy", prefix + "-receipt-aggregate", prefix + "-revocation", prefix + "-trust-ledger"},
		keysetKind:    prefix + "-keyset", revocationKind: prefix + "-revocation", configKind: prefix + "-config", evidenceKind: prefix + "-evidence", fenceKind: prefix + "-fence",
	}, true
}
