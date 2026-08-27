package revokedrain

import (
	"errors"
	"strings"
	"testing"
)

type bindingFakeResolver struct {
	material      BindingMaterial
	registration  BindingRegistration
	snapshot      BindingSnapshot
	materialErr   error
	regErr        error
	snapshotErr   error
	materialCalls int
	regCalls      int
	snapshotCalls int
}

func (f *bindingFakeResolver) ResolveBindingMaterial(string) (BindingMaterial, error) {
	f.materialCalls++
	return f.material, f.materialErr
}
func (f *bindingFakeResolver) LookupBindingRegistration(string) (BindingRegistration, error) {
	f.regCalls++
	return f.registration, f.regErr
}
func (f *bindingFakeResolver) LookupBindingSnapshot(string) (BindingSnapshot, error) {
	f.snapshotCalls++
	return f.snapshot, f.snapshotErr
}

func bindingDigest(ch string) string { return "sha256:" + strings.Repeat(ch, 64) }

func bindingFixture(port BindingPort, kind BindingMaterialKind) (BindingTrustedTarget, BindingMaterialRef, *bindingFakeResolver) {
	target := BindingTrustedTarget{
		Port: port, Kind: kind, SecurityDomainID: "tenant/domain-a", ProtocolFamily: "worker.v1",
		Audience: "result-ingress", Label: "worker-result", RegistrationID: "registration:a",
		CurrentSnapshotRef: bindingDigest("b"),
	}
	ref := BindingMaterialRef{Ref: "material:a"}
	material := BindingMaterial{
		Ref: ref.Ref, Digest: bindingDigest("a"), Kind: kind, BearerPrincipal: "principal:a",
		RegistrationID: target.RegistrationID, SnapshotRef: target.CurrentSnapshotRef, Port: port,
		SecurityDomainID: target.SecurityDomainID, ProtocolFamily: target.ProtocolFamily,
		Audience: target.Audience, Label: target.Label, State: BindingStateActive,
	}
	registration := BindingRegistration{
		RegistrationID: target.RegistrationID, Principal: material.BearerPrincipal, Port: port,
		SecurityDomainID: target.SecurityDomainID, ProtocolFamily: target.ProtocolFamily,
		Audience: target.Audience, State: BindingStateActive,
	}
	snapshot := BindingSnapshot{
		SnapshotRef: target.CurrentSnapshotRef, RegistrationID: target.RegistrationID, Port: port,
		SecurityDomainID: target.SecurityDomainID, ProtocolFamily: target.ProtocolFamily,
		Audience: target.Audience, State: BindingStateActive,
	}
	return target, ref, &bindingFakeResolver{material: material, registration: registration, snapshot: snapshot}
}

func TestBoundaryCheckerAcceptsOwnedPortAndKindMatrix(t *testing.T) {
	for _, port := range []BindingPort{BindingPortAgent, BindingPortSandbox} {
		for _, kind := range []BindingMaterialKind{BindingMaterialEvidence, BindingMaterialCredential, BindingMaterialToken} {
			t.Run(string(port)+"/"+string(kind), func(t *testing.T) {
				target, ref, resolver := bindingFixture(port, kind)
				checker, err := NewBoundaryChecker(target, resolver)
				if err != nil {
					t.Fatal(err)
				}
				if got := checker.Validate(ref); !got.Accepted || got.Reason != BindingAccepted {
					t.Fatalf("Validate() = %s", got)
				}
			})
		}
	}
}

func TestBoundaryCheckerRejectsConstructionAndOpaqueRef(t *testing.T) {
	target, _, resolver := bindingFixture(BindingPortAgent, BindingMaterialEvidence)
	if _, err := NewBoundaryChecker(BindingTrustedTarget{}, resolver); err == nil {
		t.Fatal("empty target accepted")
	}
	if _, err := NewBoundaryChecker(target, nil); err == nil {
		t.Fatal("nil resolver accepted")
	}
	checker, err := NewBoundaryChecker(target, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if got := checker.Validate(BindingMaterialRef{}); got.Reason != BindingInvalidReference {
		t.Fatalf("zero ref = %s", got)
	}
	if got := checker.Validate(BindingMaterialRef{Ref: "material:other"}); got.Reason != BindingReferenceMismatch {
		t.Fatalf("wrong ref = %s", got)
	}
}

func TestBoundaryCheckerRejectsAuthorityLookupFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*bindingFakeResolver)
		want   BindingReason
	}{
		{"material-not-found", func(f *bindingFakeResolver) { f.materialErr = ErrBindingNotFound }, BindingMaterialLookupFailed},
		{"material-unavailable", func(f *bindingFakeResolver) { f.materialErr = ErrBindingUnavailable }, BindingMaterialLookupFailed},
		{"material-ambiguous", func(f *bindingFakeResolver) { f.materialErr = ErrBindingAmbiguous }, BindingMaterialAmbiguous},
		{"registration", func(f *bindingFakeResolver) { f.regErr = errors.New("boom") }, BindingRegistrationLookupFailed},
		{"snapshot", func(f *bindingFakeResolver) { f.snapshotErr = errors.New("boom") }, BindingSnapshotLookupFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, ref, resolver := bindingFixture(BindingPortAgent, BindingMaterialEvidence)
			tt.mutate(resolver)
			checker, err := NewBoundaryChecker(target, resolver)
			if err != nil {
				t.Fatal(err)
			}
			if got := checker.Validate(ref); got.Accepted || got.Reason != tt.want {
				t.Fatalf("Validate() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestBoundaryCheckerRejectsEveryAuthorityTupleDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*BindingTrustedTarget, *bindingFakeResolver)
		want   BindingReason
	}{
		{"material-invalid-digest", func(_ *BindingTrustedTarget, f *bindingFakeResolver) { f.material.Digest = "sha256:short" }, BindingMaterialInvalid},
		{"material-inactive", func(_ *BindingTrustedTarget, f *bindingFakeResolver) { f.material.State = BindingStateRevoked }, BindingInactive},
		{"material-kind", func(_ *BindingTrustedTarget, f *bindingFakeResolver) { f.material.Kind = BindingMaterialToken }, BindingKindMismatch},
		{"material-label", func(_ *BindingTrustedTarget, f *bindingFakeResolver) { f.material.Label = "other" }, BindingLabelMismatch},
		{"material-registration", func(_ *BindingTrustedTarget, f *bindingFakeResolver) {
			f.material.RegistrationID = "registration:other"
		}, BindingReferenceMismatch},
		{"material-snapshot", func(_ *BindingTrustedTarget, f *bindingFakeResolver) { f.material.SnapshotRef = bindingDigest("c") }, BindingReferenceMismatch},
		{"registration-invalid", func(_ *BindingTrustedTarget, f *bindingFakeResolver) { f.registration.Port = "future" }, BindingRegistrationInvalid},
		{"registration-inactive", func(_ *BindingTrustedTarget, f *bindingFakeResolver) { f.registration.State = BindingStateReplaced }, BindingInactive},
		{"registration-ref", func(_ *BindingTrustedTarget, f *bindingFakeResolver) {
			f.registration.RegistrationID = "registration:other"
		}, BindingReferenceMismatch},
		{"registration-principal", func(_ *BindingTrustedTarget, f *bindingFakeResolver) { f.registration.Principal = "principal:other" }, BindingPrincipalMismatch},
		{"snapshot-invalid", func(_ *BindingTrustedTarget, f *bindingFakeResolver) { f.snapshot.Audience = "" }, BindingSnapshotInvalid},
		{"snapshot-inactive", func(_ *BindingTrustedTarget, f *bindingFakeResolver) { f.snapshot.State = BindingStateExpired }, BindingInactive},
		{"snapshot-ref", func(_ *BindingTrustedTarget, f *bindingFakeResolver) { f.snapshot.SnapshotRef = bindingDigest("c") }, BindingReferenceMismatch},
		{"snapshot-registration", func(_ *BindingTrustedTarget, f *bindingFakeResolver) {
			f.snapshot.RegistrationID = "registration:other"
		}, BindingReferenceMismatch},
		{"material-port", func(_ *BindingTrustedTarget, f *bindingFakeResolver) { f.material.Port = BindingPortSandbox }, BindingPortMismatch},
		{"registration-domain", func(_ *BindingTrustedTarget, f *bindingFakeResolver) {
			f.registration.SecurityDomainID = "tenant/domain-b"
		}, BindingSecurityDomainMismatch},
		{"snapshot-protocol", func(_ *BindingTrustedTarget, f *bindingFakeResolver) { f.snapshot.ProtocolFamily = "worker.v2" }, BindingProtocolMismatch},
		{"material-audience", func(_ *BindingTrustedTarget, f *bindingFakeResolver) { f.material.Audience = "other" }, BindingAudienceMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, ref, resolver := bindingFixture(BindingPortAgent, BindingMaterialEvidence)
			tt.mutate(&target, resolver)
			checker, err := NewBoundaryChecker(target, resolver)
			if err != nil {
				t.Fatal(err)
			}
			if got := checker.Validate(ref); got.Accepted || got.Reason != tt.want {
				t.Fatalf("Validate() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestBoundaryCheckerRejectsAllCrossPortReplayDirections(t *testing.T) {
	for _, source := range []BindingPort{BindingPortAgent, BindingPortSandbox} {
		for _, kind := range []BindingMaterialKind{BindingMaterialEvidence, BindingMaterialCredential, BindingMaterialToken} {
			target, ref, resolver := bindingFixture(source, kind)
			if source == BindingPortAgent {
				target.Port = BindingPortSandbox
			} else {
				target.Port = BindingPortAgent
			}
			checker, err := NewBoundaryChecker(target, resolver)
			if err != nil {
				t.Fatal(err)
			}
			if got := checker.Validate(ref); got.Accepted || got.Reason != BindingPortMismatch {
				t.Fatalf("source=%s kind=%s Validate() = %s", source, kind, got)
			}
		}
	}
}

func TestBoundaryCheckerRechecksFreshAuthority(t *testing.T) {
	target, ref, resolver := bindingFixture(BindingPortAgent, BindingMaterialCredential)
	checker, err := NewBoundaryChecker(target, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if got := checker.Validate(ref); !got.Accepted {
		t.Fatalf("first = %s", got)
	}
	resolver.registration.State = BindingStateRevoked
	if got := checker.Validate(ref); got.Accepted || got.Reason != BindingInactive {
		t.Fatalf("second = %s", got)
	}
	if resolver.materialCalls != 2 || resolver.regCalls != 2 || resolver.snapshotCalls != 1 {
		t.Fatalf("fresh calls material=%d reg=%d snapshot=%d", resolver.materialCalls, resolver.regCalls, resolver.snapshotCalls)
	}
}
