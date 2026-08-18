package authority

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

const (
	testDigestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testDigestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type bundleFixture struct {
	manifest   []byte
	signature  []byte
	leaves     []ImmutableLeaf
	snapshot   CurrentSnapshot
	keys       PublicKeyring
	privateKey ed25519.PrivateKey
	now        time.Time
}

func TestVerifyCurrentBundleExactProductionBindings(t *testing.T) {
	fixture := newBundleFixture(t)
	verified, err := VerifyCurrentBundle(fixture.manifest, fixture.signature, fixture.leaves, fixture.snapshot, AcceptedState{}, fixture.keys, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if verified.BundleDigest != fixture.snapshot.BundleDigest || verified.Accepted.FenceDigest != fixture.snapshot.FenceDigest || verified.Accepted.ProviderSequence != fixture.snapshot.ProviderSequence {
		t.Fatal("verified bundle did not preserve exact current bindings")
	}
}

func TestVerifyCurrentBundleAcceptsExactNextHighWaterLink(t *testing.T) {
	previous := newBundleFixture(t)
	next := cloneFixture(previous)
	var manifest AuthorityBundleManifestV1
	if json.Unmarshal(next.manifest, &manifest) != nil {
		t.Fatal("decode manifest fixture")
	}
	manifest.ProviderSequence++
	manifest.AuthorityGeneration++
	manifest.PreviousBundleDigest = &previous.snapshot.BundleDigest
	resealBundle(t, &next, manifest)
	verified, err := VerifyCurrentBundle(next.manifest, next.signature, next.leaves, next.snapshot, acceptedFrom(previous.snapshot, previous.snapshot.ProviderSequence, previous.snapshot.AuthorityGeneration, previous.snapshot.BundleDigest), next.keys, next.now)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Accepted.ProviderSequence != previous.snapshot.ProviderSequence+1 {
		t.Fatal("next high-water sequence was not preserved")
	}
}

func TestVerifyCurrentBundleFailsClosed(t *testing.T) {
	base := newBundleFixture(t)
	tests := map[string]func(*bundleFixture) AcceptedState{
		"wrong-profile": func(f *bundleFixture) AcceptedState {
			f.snapshot.AuthorityProfile = "codex-cli-adr0037-v1"
			return AcceptedState{}
		},
		"revoked-evidence": func(f *bundleFixture) AcceptedState {
			f.snapshot.RevokedObjectDigests = []string{f.snapshot.EvidenceDigest}
			return AcceptedState{}
		},
		"config-binding": func(f *bundleFixture) AcceptedState {
			f.snapshot.ConfigDigest = testDigestA
			return AcceptedState{}
		},
		"evidence-binding": func(f *bundleFixture) AcceptedState {
			f.snapshot.EvidenceDigest = testDigestA
			return AcceptedState{}
		},
		"revocation-binding": func(f *bundleFixture) AcceptedState {
			f.snapshot.RevocationSetDigest = testDigestA
			return AcceptedState{}
		},
		"generation-binding": func(f *bundleFixture) AcceptedState {
			f.snapshot.AuthorityGeneration++
			return AcceptedState{}
		},
		"sequence-binding": func(f *bundleFixture) AcceptedState {
			f.snapshot.ProviderSequence++
			return AcceptedState{}
		},
		"rollback": func(f *bundleFixture) AcceptedState {
			return acceptedFrom(f.snapshot, f.snapshot.ProviderSequence+1, f.snapshot.AuthorityGeneration+1, f.snapshot.BundleDigest)
		},
		"same-generation-fork": func(f *bundleFixture) AcceptedState {
			previous := acceptedFrom(f.snapshot, f.snapshot.ProviderSequence-1, f.snapshot.AuthorityGeneration, testDigestA)
			previous.ConfigDigest = testDigestB
			return previous
		},
		"same-sequence-fork": func(f *bundleFixture) AcceptedState {
			return acceptedFrom(f.snapshot, f.snapshot.ProviderSequence, f.snapshot.AuthorityGeneration-1, testDigestA)
		},
		"counter-jump": func(f *bundleFixture) AcceptedState {
			return acceptedFrom(f.snapshot, f.snapshot.ProviderSequence-2, f.snapshot.AuthorityGeneration-1, testDigestA)
		},
		"tampered-leaf": func(f *bundleFixture) AcceptedState {
			f.leaves[0].Content = []byte(`{"changed":true}`)
			return AcceptedState{}
		},
		"missing-fence-leaf": func(f *bundleFixture) AcceptedState {
			f.snapshot.FenceDigest = testDigestA
			return AcceptedState{}
		},
		"expired": func(f *bundleFixture) AcceptedState {
			f.now = f.now.Add(2 * time.Hour)
			return AcceptedState{}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := cloneFixture(base)
			previous := mutate(&fixture)
			if _, err := VerifyCurrentBundle(fixture.manifest, fixture.signature, fixture.leaves, fixture.snapshot, previous, fixture.keys, fixture.now); !errors.Is(err, ErrBundleRejected) {
				t.Fatal("invalid bundle was not rejected")
			}
		})
	}
}

func TestDetachedSignatureRejectsWrongDomainUsageProducerEpochAndShape(t *testing.T) {
	base := newBundleFixture(t)
	for name, mutate := range map[string]func(*bundleFixture){
		"wrong-usage": func(f *bundleFixture) {
			record := f.keys.records[0]
			record.Usage = "launch"
			f.keys, _ = NewPublicKeyring([]PublicKeyRecord{record})
		},
		"wrong-producer": func(f *bundleFixture) { f.snapshot.ManifestProducerDigest = testDigestA },
		"wrong-key-epoch": func(f *bundleFixture) {
			f.signature = mutateEnvelope(t, f.signature, "keyEpoch", uint64(8))
		},
		"revoked-key": func(f *bundleFixture) {
			record := f.keys.records[0]
			revokedAt := f.now.Add(time.Minute)
			record.RevokedAt = &revokedAt
			f.keys, _ = NewPublicKeyring([]PublicKeyRecord{record})
		},
		"wrong-domain": func(f *bundleFixture) {
			f.signature = mutateEnvelope(t, f.signature, "signatureDomain", "marshal-bundle-prepared-receipt-v1\x00")
		},
		"unknown-member": func(f *bundleFixture) {
			var object map[string]any
			if json.Unmarshal(f.signature, &object) != nil {
				t.Fatal("decode fixture")
			}
			object["extra"] = true
			f.signature = canonicalValue(t, object)
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := cloneFixture(base)
			mutate(&fixture)
			if _, err := VerifyCurrentBundle(fixture.manifest, fixture.signature, fixture.leaves, fixture.snapshot, AcceptedState{}, fixture.keys, fixture.now); !errors.Is(err, ErrBundleRejected) {
				t.Fatal("invalid detached signature was not rejected")
			}
		})
	}
}

func TestPublicKeyringRejectsPrivateAndAmbiguousKeys(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := fixedTime()
	record := PublicKeyRecord{KeyID: "bundle-key", KeyEpoch: 7, Usage: BundleKeyUsage, ProducerPrincipalDigest: testDigestA, PublicKey: publicKey, ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour)}
	privateRecord := record
	privateRecord.PublicKey = ed25519.PublicKey(privateKey)
	if _, err := NewPublicKeyring([]PublicKeyRecord{privateRecord}); !errors.Is(err, ErrBundleRejected) {
		t.Fatal("private key entered consumer keyring")
	}
	if _, err := NewPublicKeyring([]PublicKeyRecord{record, record}); !errors.Is(err, ErrBundleRejected) {
		t.Fatal("ambiguous duplicate public key identity accepted")
	}
}

func TestManifestAndLeafOrderingAreClosedAndImmutable(t *testing.T) {
	base := newBundleFixture(t)
	var object map[string]any
	if json.Unmarshal(base.manifest, &object) != nil {
		t.Fatal("decode fixture")
	}
	object["extra"] = true
	unknown := canonicalValue(t, object)
	if _, err := VerifyCurrentBundle(unknown, base.signature, base.leaves, base.snapshot, AcceptedState{}, base.keys, base.now); !errors.Is(err, ErrBundleRejected) {
		t.Fatal("unknown manifest member accepted")
	}
	reordered := cloneFixture(base)
	reordered.leaves[0], reordered.leaves[1] = reordered.leaves[1], reordered.leaves[0]
	if _, err := VerifyCurrentBundle(reordered.manifest, reordered.signature, reordered.leaves, reordered.snapshot, AcceptedState{}, reordered.keys, reordered.now); !errors.Is(err, ErrBundleRejected) {
		t.Fatal("leaf batch order substitution accepted")
	}
}

func newBundleFixture(t *testing.T) bundleFixture {
	t.Helper()
	now := fixedTime()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaves := []ImmutableLeaf{
		{LeafKind: "config", Content: canonicalValue(t, map[string]any{"kind": "config", "value": 1})},
		{LeafKind: "evidence", Content: canonicalValue(t, map[string]any{"kind": "evidence", "value": 2})},
		{LeafKind: "fence", Content: canonicalValue(t, map[string]any{"kind": "fence", "value": 3})},
		{LeafKind: "keyset", Content: canonicalValue(t, map[string]any{"kind": "keyset", "value": 4})},
		{LeafKind: "revocation", Content: canonicalValue(t, map[string]any{"kind": "revocation", "value": 5})},
	}
	sort.Slice(leaves, func(i, j int) bool {
		left, right := leaves[i].LeafKind+"\x00"+canonical.DigestBytes(leaves[i].Content), leaves[j].LeafKind+"\x00"+canonical.DigestBytes(leaves[j].Content)
		return left < right
	})
	descriptors := make([]AuthorityBundleLeafV1, len(leaves))
	digests := make(map[string]string, len(leaves))
	for i, leaf := range leaves {
		digest := canonical.DigestBytes(leaf.Content)
		descriptors[i] = AuthorityBundleLeafV1{LeafKind: leaf.LeafKind, Digest: digest, Size: uint64(len(leaf.Content)), MediaType: "application/json"}
		digests[leaf.LeafKind] = digest
	}
	manifest := AuthorityBundleManifestV1{
		SchemaVersion: ManifestSchema, ProviderInstanceID: "provider-123456", AuthorityProfile: "qoder-cli-adr0034-v1", HostIdentityDigest: testDigestA,
		ProviderSequence: 7, AuthorityGeneration: 3, TrustRootGeneration: 2, KeysetDigest: digests["keyset"], RevocationSetDigest: digests["revocation"],
		ConfigDigest: digests["config"], EvidenceDigest: digests["evidence"], ProfileLeaves: descriptors,
		CreatedAt: now.Add(-time.Minute).Format(time.RFC3339Nano), ValidUntil: now.Add(time.Hour).Format(time.RFC3339Nano), PreviousBundleDigest: nil, TransactionID: "transaction-123456",
	}
	manifestRaw := canonicalValue(t, manifest)
	bundleDigest := canonical.DigestBytes(manifestRaw)
	producer := testDigestB
	envelope := SignedObjectEnvelopeV1{ObjectDigest: bundleDigest, SignatureAlgorithm: "Ed25519", SignatureEncoding: "base64url-unpadded", KeyID: "bundle-key", KeyEpoch: 7, SignatureDomain: BundleDomain}
	envelope.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, append([]byte(BundleDomain), []byte(bundleDigest)...)))
	signatureRaw := canonicalValue(t, map[string]any{"schemaVersion": SignatureSchema, "signedObjectEnvelope": envelope})
	keys, err := NewPublicKeyring([]PublicKeyRecord{{KeyID: "bundle-key", KeyEpoch: 7, Usage: BundleKeyUsage, ProducerPrincipalDigest: producer, PublicKey: publicKey, ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(2 * time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := CurrentSnapshot{
		ProviderInstanceID: manifest.ProviderInstanceID, AuthorityProfile: manifest.AuthorityProfile, HostIdentityDigest: manifest.HostIdentityDigest,
		BundleDigest: bundleDigest, ProviderSequence: manifest.ProviderSequence, AuthorityGeneration: manifest.AuthorityGeneration, TrustRootGeneration: manifest.TrustRootGeneration,
		KeysetDigest: manifest.KeysetDigest, RevocationSetDigest: manifest.RevocationSetDigest, ConfigDigest: manifest.ConfigDigest, EvidenceDigest: manifest.EvidenceDigest,
		FenceDigest: digests["fence"], ManifestProducerDigest: producer,
	}
	return bundleFixture{manifest: manifestRaw, signature: signatureRaw, leaves: leaves, snapshot: snapshot, keys: keys, privateKey: privateKey, now: now}
}

func resealBundle(t *testing.T, fixture *bundleFixture, manifest AuthorityBundleManifestV1) {
	t.Helper()
	fixture.manifest = canonicalValue(t, manifest)
	fixture.snapshot.BundleDigest = canonical.DigestBytes(fixture.manifest)
	fixture.snapshot.ProviderSequence = manifest.ProviderSequence
	fixture.snapshot.AuthorityGeneration = manifest.AuthorityGeneration
	envelope := SignedObjectEnvelopeV1{ObjectDigest: fixture.snapshot.BundleDigest, SignatureAlgorithm: "Ed25519", SignatureEncoding: "base64url-unpadded", KeyID: "bundle-key", KeyEpoch: 7, SignatureDomain: BundleDomain}
	envelope.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(fixture.privateKey, append([]byte(BundleDomain), []byte(fixture.snapshot.BundleDigest)...)))
	fixture.signature = canonicalValue(t, map[string]any{"schemaVersion": SignatureSchema, "signedObjectEnvelope": envelope})
}

func acceptedFrom(snapshot CurrentSnapshot, sequence, generation uint64, bundleDigest string) AcceptedState {
	return AcceptedState{Initialized: true, ProviderInstanceID: snapshot.ProviderInstanceID, AuthorityProfile: snapshot.AuthorityProfile, BundleDigest: bundleDigest, ProviderSequence: sequence, AuthorityGeneration: generation, TrustRootGeneration: snapshot.TrustRootGeneration, KeysetDigest: snapshot.KeysetDigest, RevocationSetDigest: snapshot.RevocationSetDigest, ConfigDigest: snapshot.ConfigDigest, EvidenceDigest: snapshot.EvidenceDigest, FenceDigest: snapshot.FenceDigest}
}

func cloneFixture(source bundleFixture) bundleFixture {
	cloned := source
	cloned.manifest = append([]byte(nil), source.manifest...)
	cloned.signature = append([]byte(nil), source.signature...)
	cloned.leaves = make([]ImmutableLeaf, len(source.leaves))
	for i, leaf := range source.leaves {
		cloned.leaves[i] = ImmutableLeaf{LeafKind: leaf.LeafKind, Content: append([]byte(nil), leaf.Content...)}
	}
	cloned.snapshot.RevokedObjectDigests = append([]string(nil), source.snapshot.RevokedObjectDigests...)
	return cloned
}

func mutateEnvelope(t *testing.T, raw []byte, field string, value any) []byte {
	t.Helper()
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil {
		t.Fatal("decode signature fixture")
	}
	envelope := object["signedObjectEnvelope"].(map[string]any)
	envelope[field] = value
	return canonicalValue(t, object)
}

func canonicalValue(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	result, err := canonical.JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func fixedTime() time.Time { return time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC) }
