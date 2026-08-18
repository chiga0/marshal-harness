package authority

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

var (
	digestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	idPattern       = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	leafKindPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
)

var manifestFields = []string{"authorityGeneration", "authorityProfile", "configDigest", "createdAt", "evidenceDigest", "hostIdentityDigest", "keysetDigest", "previousBundleDigest", "profileLeaves", "providerInstanceId", "providerSequence", "revocationSetDigest", "schemaVersion", "transactionId", "trustRootGeneration", "validUntil"}
var leafFields = []string{"digest", "leafKind", "mediaType", "size"}
var signatureFields = []string{"schemaVersion", "signedObjectEnvelope"}
var envelopeFields = []string{"keyEpoch", "keyId", "objectDigest", "signature", "signatureAlgorithm", "signatureDomain", "signatureEncoding"}

// VerifyCurrentBundle validates the complete immutable bundle and advances a
// caller-provided high-water projection without mutating it. Every rejection
// maps to one stable sentinel and exposes no untrusted field value.
func VerifyCurrentBundle(manifestRaw, detachedSignatureRaw []byte, leaves []ImmutableLeaf, snapshot CurrentSnapshot, previous HighWaterState, keys PublicKeyring, now time.Time) (VerificationResult, error) {
	result, err := observeCurrent(snapshot, previous)
	if err != nil {
		return result, err
	}
	manifest, canonicalManifest, err := parseManifest(manifestRaw, now)
	if err != nil {
		return result, ErrBundleRejected
	}
	bundleDigest := canonical.DigestBytes(canonicalManifest)
	if bundleDigest != snapshot.BundleDigest || !manifestMatchesSnapshot(manifest, snapshot) || rejectHighWater(manifest, snapshot, previous) {
		return result, ErrBundleRejected
	}
	if err := verifyLeaves(manifest, leaves, snapshot); err != nil {
		return result, err
	}
	if revoked(snapshot, bundleDigest) || revoked(snapshot, snapshot.EvidenceDigest) || revoked(snapshot, snapshot.ConfigDigest) || revoked(snapshot, snapshot.FenceDigest) || revoked(snapshot, snapshot.KeysetDigest) {
		return result, ErrBundleRejected
	}
	if err := verifyDetachedBundleSignature(detachedSignatureRaw, bundleDigest, snapshot.ManifestProducerDigest, keys, mustTime(manifest.CreatedAt)); err != nil {
		return result, err
	}
	result.Eligible = true
	result.Bundle = VerifiedBundle{Manifest: manifest, BundleDigest: bundleDigest}
	return result, nil
}

// VerifySignedObjectEnvelope verifies exact domain, producer, usage, key
// identity/epoch, validity, revocation, object digest, encoding and signature.
func VerifySignedObjectEnvelope(raw []byte, objectDigest, domain, usage, producerPrincipalDigest string, keys PublicKeyring, issuedAt time.Time) error {
	object, _, err := admitObject(raw, envelopeFields, maxManifestBytes)
	if err != nil || !validDigest(objectDigest) || !validDigest(producerPrincipalDigest) || issuedAt.IsZero() {
		return ErrBundleRejected
	}
	var envelope SignedObjectEnvelopeV1
	epoch, epochOK := rawUint(object["keyEpoch"])
	if !epochOK || epoch > math.MaxInt64 || json.Unmarshal(raw, &envelope) != nil || envelope.KeyEpoch != epoch || envelope.ObjectDigest != objectDigest || envelope.SignatureAlgorithm != "Ed25519" || envelope.SignatureEncoding != "base64url-unpadded" || envelope.SignatureDomain != domain || !validKeyID(envelope.KeyID) {
		return ErrBundleRejected
	}
	signature, err := base64.RawURLEncoding.DecodeString(envelope.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || base64.RawURLEncoding.EncodeToString(signature) != envelope.Signature {
		return ErrBundleRejected
	}
	var matched *PublicKeyRecord
	for i := range keys.records {
		record := &keys.records[i]
		if record.KeyID == envelope.KeyID && record.KeyEpoch == envelope.KeyEpoch && record.Usage == usage && record.ProducerPrincipalDigest == producerPrincipalDigest {
			if matched != nil {
				return ErrBundleRejected
			}
			matched = record
		}
	}
	if matched == nil || issuedAt.Before(matched.ValidFrom) || !issuedAt.Before(matched.ValidUntil) || matched.RevokedAt != nil {
		return ErrBundleRejected
	}
	message := append([]byte(domain), []byte(objectDigest)...)
	if !ed25519.Verify(matched.PublicKey, message, signature) {
		return ErrBundleRejected
	}
	return nil
}

func parseManifest(raw []byte, now time.Time) (AuthorityBundleManifestV1, []byte, error) {
	object, canonicalRaw, err := admitObject(raw, manifestFields, maxManifestBytes)
	if err != nil || now.IsZero() {
		return AuthorityBundleManifestV1{}, nil, ErrBundleRejected
	}
	var manifest AuthorityBundleManifestV1
	providerSequence, sequenceOK := rawUint(object["providerSequence"])
	authorityGeneration, authorityOK := rawUint(object["authorityGeneration"])
	trustRootGeneration, trustOK := rawUint(object["trustRootGeneration"])
	if !sequenceOK || !authorityOK || !trustOK || json.Unmarshal(canonicalRaw, &manifest) != nil || manifest.ProviderSequence != providerSequence || manifest.AuthorityGeneration != authorityGeneration || manifest.TrustRootGeneration != trustRootGeneration || manifest.SchemaVersion != ManifestSchema || !idPattern.MatchString(manifest.ProviderInstanceID) || !validProfile(manifest.AuthorityProfile) || !idPattern.MatchString(manifest.TransactionID) {
		return AuthorityBundleManifestV1{}, nil, ErrBundleRejected
	}
	for _, value := range []string{manifest.HostIdentityDigest, manifest.KeysetDigest, manifest.RevocationSetDigest, manifest.ConfigDigest, manifest.EvidenceDigest} {
		if !validDigest(value) {
			return AuthorityBundleManifestV1{}, nil, ErrBundleRejected
		}
	}
	if !bytes.Equal(object["previousBundleDigest"], []byte("null")) && (manifest.PreviousBundleDigest == nil || !validDigest(*manifest.PreviousBundleDigest)) {
		return AuthorityBundleManifestV1{}, nil, ErrBundleRejected
	}
	created, createdOK := canonicalTime(manifest.CreatedAt)
	validUntil, validOK := canonicalTime(manifest.ValidUntil)
	if !createdOK || !validOK || created.After(now) || !now.Before(validUntil) || !validUntil.After(created) {
		return AuthorityBundleManifestV1{}, nil, ErrBundleRejected
	}
	if len(manifest.ProfileLeaves) < 1 || len(manifest.ProfileLeaves) > maxLeaves {
		return AuthorityBundleManifestV1{}, nil, ErrBundleRejected
	}
	var rawLeaves []json.RawMessage
	if json.Unmarshal(object["profileLeaves"], &rawLeaves) != nil || len(rawLeaves) != len(manifest.ProfileLeaves) {
		return AuthorityBundleManifestV1{}, nil, ErrBundleRejected
	}
	previous := ""
	var total uint64
	for i, leaf := range manifest.ProfileLeaves {
		rawLeaf, _, leafErr := admitObject(rawLeaves[i], leafFields, maxManifestBytes)
		size, sizeOK := rawUint(rawLeaf["size"])
		if leafErr != nil || !sizeOK || leaf.Size != size || !leafKindPattern.MatchString(leaf.LeafKind) || !validDigest(leaf.Digest) || leaf.Size < 1 || leaf.Size > maxLeafBytes || leaf.MediaType != "application/json" {
			return AuthorityBundleManifestV1{}, nil, ErrBundleRejected
		}
		identity := leaf.LeafKind + "\x00" + leaf.Digest
		if i > 0 && identity <= previous {
			return AuthorityBundleManifestV1{}, nil, ErrBundleRejected
		}
		previous = identity
		total += leaf.Size
		if total > maxBundleBytes {
			return AuthorityBundleManifestV1{}, nil, ErrBundleRejected
		}
	}
	return manifest, canonicalRaw, nil
}

func verifyLeaves(manifest AuthorityBundleManifestV1, leaves []ImmutableLeaf, snapshot CurrentSnapshot) error {
	policy, ok := profilePolicy(manifest.AuthorityProfile)
	if !ok || len(manifest.ProfileLeaves) != len(policy.requiredKinds) || len(leaves) != len(manifest.ProfileLeaves) {
		return ErrBundleRejected
	}
	present := make(map[string]string, len(leaves))
	seenDigests := make(map[string]struct{}, len(leaves))
	var total int
	for i, leaf := range leaves {
		descriptor := manifest.ProfileLeaves[i]
		if descriptor.LeafKind != policy.requiredKinds[i] || leaf.LeafKind != descriptor.LeafKind || len(leaf.Content) != int(descriptor.Size) || len(leaf.Content) < 1 || len(leaf.Content) > maxLeafBytes {
			return ErrBundleRejected
		}
		canonicalLeaf, err := canonical.JSON(leaf.Content)
		if err != nil || !bytes.Equal(canonicalLeaf, leaf.Content) || canonical.DigestBytes(leaf.Content) != descriptor.Digest {
			return ErrBundleRejected
		}
		if _, duplicate := present[descriptor.LeafKind]; duplicate {
			return ErrBundleRejected
		}
		if _, duplicate := seenDigests[descriptor.Digest]; duplicate {
			return ErrBundleRejected
		}
		present[descriptor.LeafKind] = descriptor.Digest
		seenDigests[descriptor.Digest] = struct{}{}
		total += len(leaf.Content)
		if total > maxBundleBytes {
			return ErrBundleRejected
		}
	}
	bindings := map[string]string{policy.keysetKind: snapshot.KeysetDigest, policy.revocationKind: snapshot.RevocationSetDigest, policy.configKind: snapshot.ConfigDigest, policy.evidenceKind: snapshot.EvidenceDigest, policy.fenceKind: snapshot.FenceDigest}
	for kind, digest := range bindings {
		if present[kind] != digest {
			return ErrBundleRejected
		}
	}
	return nil
}

func verifyDetachedBundleSignature(raw []byte, bundleDigest, producer string, keys PublicKeyring, createdAt time.Time) error {
	object, _, err := admitObject(raw, signatureFields, maxManifestBytes)
	if err != nil || rawString(object["schemaVersion"]) != SignatureSchema {
		return ErrBundleRejected
	}
	return VerifySignedObjectEnvelope(object["signedObjectEnvelope"], bundleDigest, BundleDomain, BundleKeyUsage, producer, keys, createdAt)
}

func manifestMatchesSnapshot(manifest AuthorityBundleManifestV1, snapshot CurrentSnapshot) bool {
	return manifest.ProviderInstanceID == snapshot.ProviderInstanceID && manifest.AuthorityProfile == snapshot.AuthorityProfile && manifest.HostIdentityDigest == snapshot.HostIdentityDigest && manifest.ProviderSequence == snapshot.ProviderSequence && manifest.AuthorityGeneration == snapshot.AuthorityGeneration && manifest.TrustRootGeneration == snapshot.TrustRootGeneration && manifest.KeysetDigest == snapshot.KeysetDigest && manifest.RevocationSetDigest == snapshot.RevocationSetDigest && manifest.ConfigDigest == snapshot.ConfigDigest && manifest.EvidenceDigest == snapshot.EvidenceDigest
}

func rejectHighWater(manifest AuthorityBundleManifestV1, current CurrentSnapshot, previous HighWaterState) bool {
	if !previous.Initialized {
		return false
	}
	if current.ProviderInstanceID != previous.ProviderInstanceID || current.AuthorityProfile != previous.AuthorityProfile || current.ProviderSequence < previous.ProviderSequence || current.AuthorityGeneration < previous.AuthorityGeneration || current.TrustRootGeneration < previous.TrustRootGeneration {
		return true
	}
	if current.ProviderSequence == previous.ProviderSequence && current.BundleDigest != previous.BundleDigest {
		return true
	}
	if current.ProviderSequence > previous.ProviderSequence && (current.ProviderSequence != previous.ProviderSequence+1 || manifest.PreviousBundleDigest == nil || *manifest.PreviousBundleDigest != previous.BundleDigest) {
		return true
	}
	if current.AuthorityGeneration == previous.AuthorityGeneration && (current.BundleDigest != previous.BundleDigest || current.ConfigDigest != previous.ConfigDigest || current.EvidenceDigest != previous.EvidenceDigest || current.FenceDigest != previous.FenceDigest || current.RevocationSetDigest != previous.RevocationSetDigest) {
		return true
	}
	if current.TrustRootGeneration == previous.TrustRootGeneration && current.KeysetDigest != previous.KeysetDigest {
		return true
	}
	return false
}

func observeCurrent(snapshot CurrentSnapshot, previous HighWaterState) (VerificationResult, error) {
	if !validSnapshot(snapshot) {
		return VerificationResult{}, ErrBundleRejected
	}
	observed := HighWaterState{Initialized: true, ProviderInstanceID: snapshot.ProviderInstanceID, AuthorityProfile: snapshot.AuthorityProfile, BundleDigest: snapshot.BundleDigest, ProviderSequence: snapshot.ProviderSequence, AuthorityGeneration: snapshot.AuthorityGeneration, TrustRootGeneration: snapshot.TrustRootGeneration, KeysetDigest: snapshot.KeysetDigest, RevocationSetDigest: snapshot.RevocationSetDigest, ConfigDigest: snapshot.ConfigDigest, EvidenceDigest: snapshot.EvidenceDigest, FenceDigest: snapshot.FenceDigest}
	result := VerificationResult{ObservedCurrent: observed}
	if !previous.Initialized {
		result.MustPersistObserved = true
		return result, nil
	}
	if snapshot.ProviderInstanceID != previous.ProviderInstanceID || snapshot.AuthorityProfile != previous.AuthorityProfile || snapshot.ProviderSequence < previous.ProviderSequence || snapshot.AuthorityGeneration < previous.AuthorityGeneration || snapshot.TrustRootGeneration < previous.TrustRootGeneration {
		return VerificationResult{}, ErrBundleRejected
	}
	result.MustPersistObserved = snapshot.ProviderSequence > previous.ProviderSequence || snapshot.AuthorityGeneration > previous.AuthorityGeneration || snapshot.TrustRootGeneration > previous.TrustRootGeneration || snapshot.BundleDigest != previous.BundleDigest || snapshot.ConfigDigest != previous.ConfigDigest || snapshot.EvidenceDigest != previous.EvidenceDigest || snapshot.FenceDigest != previous.FenceDigest || snapshot.RevocationSetDigest != previous.RevocationSetDigest || snapshot.KeysetDigest != previous.KeysetDigest
	return result, nil
}

func validSnapshot(snapshot CurrentSnapshot) bool {
	if !idPattern.MatchString(snapshot.ProviderInstanceID) || !validProfile(snapshot.AuthorityProfile) {
		return false
	}
	for _, digest := range []string{snapshot.HostIdentityDigest, snapshot.BundleDigest, snapshot.KeysetDigest, snapshot.RevocationSetDigest, snapshot.ConfigDigest, snapshot.EvidenceDigest, snapshot.FenceDigest, snapshot.ManifestProducerDigest} {
		if !validDigest(digest) {
			return false
		}
	}
	bindingDigests := []string{snapshot.KeysetDigest, snapshot.RevocationSetDigest, snapshot.ConfigDigest, snapshot.EvidenceDigest, snapshot.FenceDigest}
	seenBindings := make(map[string]struct{}, len(bindingDigests))
	for _, digest := range bindingDigests {
		if _, duplicate := seenBindings[digest]; duplicate {
			return false
		}
		seenBindings[digest] = struct{}{}
	}
	copyRevoked := append([]string(nil), snapshot.RevokedObjectDigests...)
	if !sort.StringsAreSorted(copyRevoked) {
		return false
	}
	for i, digest := range copyRevoked {
		if !validDigest(digest) || (i > 0 && digest == copyRevoked[i-1]) {
			return false
		}
	}
	return true
}

func revoked(snapshot CurrentSnapshot, digest string) bool {
	index := sort.SearchStrings(snapshot.RevokedObjectDigests, digest)
	return index < len(snapshot.RevokedObjectDigests) && snapshot.RevokedObjectDigests[index] == digest
}

func admitObject(raw []byte, fields []string, limit int) (map[string]json.RawMessage, []byte, error) {
	if len(raw) == 0 || len(raw) > limit {
		return nil, nil, ErrBundleRejected
	}
	canonicalRaw, err := canonical.JSON(raw)
	if err != nil || !bytes.Equal(raw, canonicalRaw) {
		return nil, nil, ErrBundleRejected
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(canonicalRaw, &object) != nil || object == nil || len(object) != len(fields) {
		return nil, nil, ErrBundleRejected
	}
	for _, field := range fields {
		if _, ok := object[field]; !ok {
			return nil, nil, ErrBundleRejected
		}
	}
	return object, canonicalRaw, nil
}

func canonicalTime(value string) (time.Time, bool) {
	if len(value) == 0 || value[len(value)-1] != 'Z' {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed, err == nil && parsed.Format(time.RFC3339Nano) == value
}

func mustTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
func rawString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}
func rawUint(raw json.RawMessage) (uint64, bool) {
	if len(raw) == 0 || (len(raw) > 1 && raw[0] == '0') || raw[0] < '0' || raw[0] > '9' {
		return 0, false
	}
	for _, digit := range raw[1:] {
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	var value uint64
	if json.Unmarshal(raw, &value) != nil {
		return 0, false
	}
	return value, true
}

func validDigest(value string) bool { return digestPattern.MatchString(value) }
func validProfile(value string) bool {
	return value == "qoder-cli-adr0034-v1" || value == "codex-cli-adr0037-v1"
}
func validKeyID(value string) bool {
	if len(value) < 1 || len(value) > 256 {
		return false
	}
	for _, character := range []byte(value) {
		if character < 0x20 || character > 0x7e {
			return false
		}
	}
	return true
}
func uintString(value uint64) string { return strconv.FormatUint(value, 10) }
