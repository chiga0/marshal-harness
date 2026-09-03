package cloudflare

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// staticTestResolver resolves a fixed valid Core authority context whose
// namespace equals its Core issuer.
type staticTestResolver struct{}

func (staticTestResolver) ResolveAuthorityContext() (AuthorityContext, error) {
	return testEffectAuthorityContext(), nil
}

func (staticTestResolver) CoreIssuer() authority.AuthorityNamespaceId {
	return testEffectAuthorityContext().Namespace
}

// failingTestResolver is Core-backed but always fails to resolve the
// authority context.
type failingTestResolver struct{}

func (failingTestResolver) ResolveAuthorityContext() (AuthorityContext, error) {
	return AuthorityContext{}, ErrAuthorityContextUnresolved
}

func (failingTestResolver) CoreIssuer() authority.AuthorityNamespaceId {
	return testEffectAuthorityContext().Namespace
}

// staticOnlyResolver implements only CoreAuthorityResolver, not the
// Core-backed marker, to freeze that a static identifier wrapper is refused.
type staticOnlyResolver struct{}

func (staticOnlyResolver) ResolveAuthorityContext() (AuthorityContext, error) {
	return testEffectAuthorityContext(), nil
}

// pointerCoreResolver is a Core-backed resolver on a pointer receiver, so a
// nil pointer is a typed nil the production root must reject.
type pointerCoreResolver struct{}

func (*pointerCoreResolver) ResolveAuthorityContext() (AuthorityContext, error) {
	return testEffectAuthorityContext(), nil
}

func (*pointerCoreResolver) CoreIssuer() authority.AuthorityNamespaceId {
	return testEffectAuthorityContext().Namespace
}

// mismatchedIssuerResolver is Core-backed but resolves a context whose
// namespace does not match its Core issuer, so the production root must
// refuse it fail closed rather than bind a forged namespace.
type mismatchedIssuerResolver struct{}

func (mismatchedIssuerResolver) ResolveAuthorityContext() (AuthorityContext, error) {
	ctx := testEffectAuthorityContext()
	ctx.Namespace = authority.AuthorityNamespaceId{TenantNamespace: "other", ControlPlaneId: "default", AuthorityScopeId: "test-scope"}
	return ctx, nil
}

func (mismatchedIssuerResolver) CoreIssuer() authority.AuthorityNamespaceId {
	return testEffectAuthorityContext().Namespace
}

// memoryEffectAuthoritySink implements EffectAuthoritySink but is not the
// durable file-backed sink, so the production root must refuse it.
type memoryEffectAuthoritySink struct{}

func (memoryEffectAuthoritySink) SinkID() string { return "cloudflare-effect-authority:memory" }
func (memoryEffectAuthoritySink) PersistEffectAuthority(EffectAuthorityRecord) error {
	return nil
}

// TestNewProductionProviderRequiresDurablePieces freezes the fail-closed
// startup contract: a file-backed state store, a durable effect authority
// sink and the Core authority resolver are all mandatory, never an
// in-memory fallback.
func TestNewProductionProviderRequiresDurablePieces(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("production"))
	store, err := NewFileStateStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("NewFileStateStore: %v", err)
	}
	sink, err := NewFileEffectAuthoritySink(filepath.Join(t.TempDir(), "effects.json"))
	if err != nil {
		t.Fatalf("NewFileEffectAuthoritySink: %v", err)
	}
	baseConfig := func() ProductionProviderConfig {
		return ProductionProviderConfig{
			ProviderConfig: ProviderConfig{
				BridgeBaseURL: fb.server.URL,
				BridgeToken:   fb.token,
				StateStore:    store,
			},
			AuthoritySink:     sink,
			AuthorityResolver: staticTestResolver{},
		}
	}

	if _, err := NewProductionProvider(baseConfig()); err != nil {
		t.Fatalf("a fully wired production provider must construct: %v", err)
	}

	cases := []struct {
		name   string
		config func() ProductionProviderConfig
	}{
		{"nil-store", func() ProductionProviderConfig {
			config := baseConfig()
			config.StateStore = nil
			return config
		}},
		{"memory-store", func() ProductionProviderConfig {
			config := baseConfig()
			config.StateStore = newMemoryStateStore()
			return config
		}},
		{"nil-sink", func() ProductionProviderConfig {
			config := baseConfig()
			config.AuthoritySink = nil
			return config
		}},
		{"memory-sink", func() ProductionProviderConfig {
			config := baseConfig()
			config.AuthoritySink = memoryEffectAuthoritySink{}
			return config
		}},
		{"typed-nil-sink", func() ProductionProviderConfig {
			config := baseConfig()
			var typedNil *FileEffectAuthoritySink
			config.AuthoritySink = typedNil
			return config
		}},
		{"nil-resolver", func() ProductionProviderConfig {
			config := baseConfig()
			config.AuthorityResolver = nil
			return config
		}},
		{"non-core-backed-resolver", func() ProductionProviderConfig {
			config := baseConfig()
			config.AuthorityResolver = staticOnlyResolver{}
			return config
		}},
		{"typed-nil-resolver", func() ProductionProviderConfig {
			config := baseConfig()
			var typedNil *pointerCoreResolver
			config.AuthorityResolver = typedNil
			return config
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewProductionProvider(tc.config()); !errors.Is(err, ErrProductionConfigInvalid) {
				t.Fatalf("the missing durable piece must fail closed with ErrProductionConfigInvalid, got %v", err)
			}
		})
	}
}

// TestNewProductionProviderResolvesAuthorityAtConstruction freezes that a
// Core-backed resolver whose context cannot be resolved fails startup with
// the authority sentinel — before any Bridge call — never a deferred failure
// after a remote Provision side effect.
func TestNewProductionProviderResolvesAuthorityAtConstruction(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("resolver-failure"))
	store, err := NewFileStateStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("NewFileStateStore: %v", err)
	}
	sink, err := NewFileEffectAuthoritySink(filepath.Join(t.TempDir(), "effects.json"))
	if err != nil {
		t.Fatalf("NewFileEffectAuthoritySink: %v", err)
	}
	_, err = NewProductionProvider(ProductionProviderConfig{
		ProviderConfig: ProviderConfig{
			BridgeBaseURL: fb.server.URL,
			BridgeToken:   fb.token,
			StateStore:    store,
		},
		AuthoritySink:     sink,
		AuthorityResolver: failingTestResolver{},
	})
	if !errors.Is(err, ErrAuthorityContextUnresolved) {
		t.Fatalf("a failing resolver must fail startup with ErrAuthorityContextUnresolved, got %v", err)
	}
	if got := fb.TotalRequests(); got != 0 {
		t.Fatalf("a failing resolver must fail before any Bridge call, got %d requests", got)
	}
}

// TestNewProductionProviderRejectsMismatchedCoreIssuer freezes that a
// Core-backed resolver whose resolved namespace does not equal its Core
// runtime issuer fails startup with the production sentinel — before any
// Bridge call — never binding a forged namespace.
func TestNewProductionProviderRejectsMismatchedCoreIssuer(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("issuer-mismatch"))
	store, err := NewFileStateStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("NewFileStateStore: %v", err)
	}
	sink, err := NewFileEffectAuthoritySink(filepath.Join(t.TempDir(), "effects.json"))
	if err != nil {
		t.Fatalf("NewFileEffectAuthoritySink: %v", err)
	}
	_, err = NewProductionProvider(ProductionProviderConfig{
		ProviderConfig: ProviderConfig{
			BridgeBaseURL: fb.server.URL,
			BridgeToken:   fb.token,
			StateStore:    store,
		},
		AuthoritySink:     sink,
		AuthorityResolver: mismatchedIssuerResolver{},
	})
	if !errors.Is(err, ErrProductionConfigInvalid) {
		t.Fatalf("a resolver whose namespace diverges from its Core issuer must fail with ErrProductionConfigInvalid, got %v", err)
	}
	if got := fb.TotalRequests(); got != 0 {
		t.Fatalf("a mismatched issuer must fail before any Bridge call, got %d requests", got)
	}
}

// TestProductionProviderRecordsNormalizedEffects freezes the normalized
// effect reconcile wiring: a production provider records one cross-bound
// effect per completed Provision and Terminate through the durable sink.
func TestProductionProviderRecordsNormalizedEffects(t *testing.T) {
	name := "prod-effect"
	alloc := "alloc-" + name
	fb := newFakeBridge(t, testBridgeToken(name))
	store, err := NewFileStateStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("NewFileStateStore: %v", err)
	}
	sink, err := NewFileEffectAuthoritySink(filepath.Join(t.TempDir(), "effects.json"))
	if err != nil {
		t.Fatalf("NewFileEffectAuthoritySink: %v", err)
	}
	provider, err := NewProductionProvider(ProductionProviderConfig{
		ProviderConfig: ProviderConfig{
			BridgeBaseURL: fb.server.URL,
			BridgeToken:   fb.token,
			MaxRetries:    2,
			RetryDelay:    -1,
			StateStore:    store,
		},
		AuthoritySink:     sink,
		AuthorityResolver: staticTestResolver{},
	})
	if err != nil {
		t.Fatalf("NewProductionProvider: %v", err)
	}
	ctx := context.Background()
	identity := func(commandId string) sandbox.OperationIdentity {
		return scenarioIdentity(name, alloc, commandId, 1)
	}

	if _, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     identity("cmd-provision"),
		Requirements: workspaceRequirements(t),
	}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if _, err := provider.Terminate(ctx, sandbox.TerminateRequest{
		Identity:     identity("cmd-terminate"),
		AllocationId: alloc,
	}); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	records := sink.Records()
	if len(records) != 2 {
		t.Fatalf("exactly one effect per completed operation was expected, got %d records", len(records))
	}
	if records[0].Operation != sandbox.OperationProvision || records[1].Operation != sandbox.OperationTerminate {
		t.Fatalf("the effect records must observe provision then terminate, got %q then %q", records[0].Operation, records[1].Operation)
	}
	if records[0].Namespace != testEffectAuthorityContext().Namespace {
		t.Fatalf("the effect record must carry the resolved Core namespace")
	}
	if records[1].Receipt.ActorProvenance.SecurityDomainId != testEffectAuthorityContext().ProviderSecurityDomain {
		t.Fatalf("the effect receipt must carry the resolved provider actor provenance")
	}
	for _, record := range records {
		if record.EffectId != effectIdentity(record.Operation, alloc, 1) {
			t.Fatalf("the effect record must carry the derived effect identity")
		}
		if record.ReconcileIdentity != reconcileIdentity(record.RunId, record.AttemptId, record.Generation, record.EffectId) {
			t.Fatalf("the effect record must carry the same-scope-derived reconcile identity")
		}
		if record.Intent.DispositionClass == authority.DispositionClassSandboxProvision && record.Operation != sandbox.OperationProvision {
			t.Fatalf("the disposition class must match the operation")
		}
	}
}
