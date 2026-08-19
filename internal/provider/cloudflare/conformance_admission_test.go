package cloudflare

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/provider"
)

func admissionFixture(t *testing.T) (ConformanceAdmissionReceipt, provider.ProviderRegistration, provider.ProviderCapabilitySnapshot, TrustRoot, *LiveEvidenceLedger) {
	t.Helper()
	ns := authority.AuthorityNamespaceId{TenantNamespace: "tenant", ControlPlaneId: "cp", AuthorityScopeId: "scope"}
	domain := authority.SecurityDomainId{TenantNamespace: "tenant", TrustDomainKind: authority.TrustDomainKindExecution, IsolationDomainId: "cf-prod"}
	att := provider.Attestation{ProviderInstanceId: "cf-instance", ConfigDigest: canonical.DigestBytes([]byte("config")), TrustRootKeyId: "root-1", TrustRootAlgorithm: SignatureAlgorithmEd25519}
	reg := provider.ProviderRegistration{RegistrationId: "reg-1", AuthorityNamespaceId: ns, SecurityDomainId: domain, Principal: "cf-principal", ProviderType: "cloudflare", ProviderName: "cloudflare-sandbox", ProviderVersion: "1", ProtocolVersion: "v1", Scope: "prod", IdempotencyKey: "key", RequestDigest: canonical.DigestBytes([]byte("request")), Attestation: att, LifecycleState: provider.LifecycleStateActive, CreatedAt: "2026-08-20T00:00:00Z"}
	reg.RegistrationDigest, _ = reg.Digest()
	snapshot := provider.ProviderCapabilitySnapshot{RegistrationId: reg.RegistrationId, ProtocolVersion: reg.ProtocolVersion, ProviderType: reg.ProviderType, ProviderName: reg.ProviderName, ProviderVersion: reg.ProviderVersion, Capabilities: map[string]string{"sandbox": "hardened"}, Scope: reg.Scope, SnapshotState: provider.SnapshotStateActive, CreatedAt: "2026-08-20T00:00:00Z", Attestation: att}
	snapshot.ProviderCapabilitySnapshotDigest, _ = snapshot.Digest()
	pub, priv := testEd25519Keys()
	signer := ed25519TestSigner{keyId: "root-1", private: priv}
	receipt := ConformanceAdmissionReceipt{AuthorityNamespaceId: ns, SecurityDomainId: domain, ProviderInstanceId: att.ProviderInstanceId, RegistrationId: reg.RegistrationId, RegistrationDigest: reg.RegistrationDigest, ProviderCapabilitySnapshotDigest: snapshot.ProviderCapabilitySnapshotDigest, ConfigDigest: att.ConfigDigest, VerifierId: "marshal-conformance-verifier", VerifierRole: IndependentVerifierRole, SuiteName: "cf-live-conformance-v1", Generation: 7, DimensionResults: map[provider.ConformanceDimension]provider.DimensionResult{provider.ConformanceDimensionMount: provider.DimensionResultPassed, provider.ConformanceDimensionNetwork: provider.DimensionResultPassed, provider.ConformanceDimensionResource: provider.DimensionResultPassed, provider.ConformanceDimensionCredential: provider.DimensionResultPassed}, ValidFrom: "2026-08-20T00:00:00Z", ValidUntil: "2026-08-20T01:00:00Z", EvidenceClass: LiveEvidenceClassLive}
	if err := SignConformanceReceipt(&receipt, signer); err != nil {
		t.Fatal(err)
	}
	ledger, err := NewLiveEvidenceLedger(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	return receipt, reg, snapshot, TrustRoot{KeyId: "root-1", Algorithm: SignatureAlgorithmEd25519, PublicKey: pub}, ledger
}

func resignAdmission(t *testing.T, receipt *ConformanceAdmissionReceipt) {
	t.Helper()
	_, priv := testEd25519Keys()
	if err := SignConformanceReceipt(receipt, ed25519TestSigner{keyId: receipt.TrustRootKeyId, private: priv}); err != nil {
		t.Fatal(err)
	}
}

func TestAdmitConformanceReceiptFailsClosedWithoutCoreAuthorityRelation(t *testing.T) {
	r, reg, snap, _, _ := admissionFixture(t)
	e, err := AdmitConformanceReceipt(r, reg, snap)
	if !errors.Is(err, ErrConformanceAdmission) || !strings.Contains(err.Error(), "Core ledger") {
		t.Fatalf("expected explicit unavailable authority relation, got evidence=%#v err=%v", e, err)
	}
	if !reflect.DeepEqual(e, provider.ConformanceEvidence{}) {
		t.Fatalf("fail-closed admission returned evidence: %#v", e)
	}
}

func TestAdmitConformanceReceiptNegativeMatrix(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ConformanceAdmissionReceipt, *provider.ProviderRegistration, *provider.ProviderCapabilitySnapshot, *TrustRoot)
		resign bool
	}{
		{"self-signed worker", func(r *ConformanceAdmissionReceipt, _ *provider.ProviderRegistration, _ *provider.ProviderCapabilitySnapshot, _ *TrustRoot) {
			r.VerifierId = "worker"
		}, false},
		{"worker signing alias", func(r *ConformanceAdmissionReceipt, _ *provider.ProviderRegistration, _ *provider.ProviderCapabilitySnapshot, _ *TrustRoot) {
			r.VerifierId = "worker-verifier-alias"
		}, true},
		{"provider self-signing alias", func(r *ConformanceAdmissionReceipt, reg *provider.ProviderRegistration, _ *provider.ProviderCapabilitySnapshot, _ *TrustRoot) {
			r.VerifierId = reg.Principal + "-verifier-alias"
		}, true},
		{"simulated", func(r *ConformanceAdmissionReceipt, _ *provider.ProviderRegistration, _ *provider.ProviderCapabilitySnapshot, _ *TrustRoot) {
			r.EvidenceClass = LiveEvidenceClassSimulated
		}, false},
		{"ordinary-user verifier", func(r *ConformanceAdmissionReceipt, _ *provider.ProviderRegistration, _ *provider.ProviderCapabilitySnapshot, _ *TrustRoot) {
			r.VerifierRole = "ordinary-user"
		}, false},
		{"failed credential dimension", func(r *ConformanceAdmissionReceipt, _ *provider.ProviderRegistration, _ *provider.ProviderCapabilitySnapshot, _ *TrustRoot) {
			r.DimensionResults[provider.ConformanceDimensionCredential] = provider.DimensionResultFailed
		}, false},
		{"revoked", func(r *ConformanceAdmissionReceipt, _ *provider.ProviderRegistration, _ *provider.ProviderCapabilitySnapshot, _ *TrustRoot) {
			r.Revoked = true
		}, true},
		{"generation mismatch", func(r *ConformanceAdmissionReceipt, _ *provider.ProviderRegistration, _ *provider.ProviderCapabilitySnapshot, _ *TrustRoot) {
			r.Generation = 8
		}, true},
		{"registration substitution", func(_ *ConformanceAdmissionReceipt, reg *provider.ProviderRegistration, _ *provider.ProviderCapabilitySnapshot, _ *TrustRoot) {
			reg.RegistrationId = "other"
			reg.RegistrationDigest, _ = reg.Digest()
		}, false},
		{"snapshot substitution", func(_ *ConformanceAdmissionReceipt, _ *provider.ProviderRegistration, s *provider.ProviderCapabilitySnapshot, _ *TrustRoot) {
			s.Capabilities["sandbox"] = "other"
			s.ProviderCapabilitySnapshotDigest, _ = s.Digest()
		}, false},
		{"config substitution", func(r *ConformanceAdmissionReceipt, _ *provider.ProviderRegistration, _ *provider.ProviderCapabilitySnapshot, _ *TrustRoot) {
			r.ConfigDigest = canonical.DigestBytes([]byte("other"))
		}, true},
		{"key substitution", func(r *ConformanceAdmissionReceipt, _ *provider.ProviderRegistration, _ *provider.ProviderCapabilitySnapshot, _ *TrustRoot) {
			r.TrustRootKeyId = "other"
		}, true},
		{"provider substitution", func(r *ConformanceAdmissionReceipt, _ *provider.ProviderRegistration, _ *provider.ProviderCapabilitySnapshot, _ *TrustRoot) {
			r.ProviderInstanceId = "other"
		}, true},
		{"security domain mismatch", func(r *ConformanceAdmissionReceipt, _ *provider.ProviderRegistration, _ *provider.ProviderCapabilitySnapshot, _ *TrustRoot) {
			r.SecurityDomainId.IsolationDomainId = "other"
		}, true},
		{"digest tamper", func(r *ConformanceAdmissionReceipt, _ *provider.ProviderRegistration, _ *provider.ProviderCapabilitySnapshot, _ *TrustRoot) {
			r.SuiteName = "tampered"
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, reg, snap, root, _ := admissionFixture(t)
			tc.mutate(&r, &reg, &snap, &root)
			if tc.resign {
				resignAdmission(t, &r)
			}
			if _, err := AdmitConformanceReceipt(r, reg, snap); !errors.Is(err, ErrConformanceAdmission) {
				t.Fatalf("expected admission rejection, got %v", err)
			}
		})
	}

	t.Run("expired", func(t *testing.T) {
		r, reg, snap, _, _ := admissionFixture(t)
		if _, err := AdmitConformanceReceipt(r, reg, snap); !errors.Is(err, ErrConformanceAdmission) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("replay", func(t *testing.T) {
		r, reg, snap, _, _ := admissionFixture(t)
		if _, err := AdmitConformanceReceipt(r, reg, snap); !errors.Is(err, ErrConformanceAdmission) {
			t.Fatal("first request must fail closed")
		}
		if _, err := AdmitConformanceReceipt(r, reg, snap); !errors.Is(err, ErrConformanceAdmission) {
			t.Fatalf("got %v", err)
		}
	})
}
