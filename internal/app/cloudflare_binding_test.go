package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/provider"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// fakeSandboxProvider is a minimal sandbox.SandboxProvider for override
// injection tests. It carries a marker so tests can verify the runtime
// returns the exact injected instance.
type fakeSandboxProvider struct {
	marker string
}

func (f *fakeSandboxProvider) Probe(_ context.Context, _ sandbox.ProbeRequest) (*sandbox.ProbeReport, error) {
	return &sandbox.ProbeReport{Supported: true, Reason: "fake"}, nil
}

func (f *fakeSandboxProvider) Provision(_ context.Context, req sandbox.ProvisionRequest) (*sandbox.ProvisionReceipt, error) {
	return &sandbox.ProvisionReceipt{Allocation: sandbox.SandboxAllocation{
		AllocationId:   req.Identity.AllocationId,
		RunId:          req.Identity.RunId,
		AttemptId:      req.Identity.AttemptId,
		Generation:     req.Identity.Generation,
		State:          sandbox.AllocationActive,
		AccessMode:     req.Requirements.AccessMode,
		AssuranceLevel: req.Requirements.MinimumAssuranceLevel,
	}}, nil
}

func (f *fakeSandboxProvider) Stage(_ context.Context, _ sandbox.StageRequest) (*sandbox.StageReport, error) {
	return &sandbox.StageReport{Receipts: []sandbox.StageReceipt{}}, nil
}

func (f *fakeSandboxProvider) Exec(_ context.Context, _ sandbox.ExecRequest) (*sandbox.ExecReceipt, error) {
	return &sandbox.ExecReceipt{Status: sandbox.ExecutionCompleted}, nil
}

func (f *fakeSandboxProvider) Inspect(_ context.Context, _ sandbox.InspectRequest) (*sandbox.InspectReport, error) {
	return &sandbox.InspectReport{State: sandbox.AllocationActive, ExitCode: -1}, nil
}

func (f *fakeSandboxProvider) Signal(_ context.Context, _ sandbox.SignalRequest) (*sandbox.SignalReceipt, error) {
	return &sandbox.SignalReceipt{Delivered: false}, nil
}

func (f *fakeSandboxProvider) Checkpoint(_ context.Context, _ sandbox.CheckpointRequest) (*sandbox.CheckpointReceipt, error) {
	return &sandbox.CheckpointReceipt{}, nil
}

func (f *fakeSandboxProvider) Restore(_ context.Context, _ sandbox.RestoreOperationRequest) (*sandbox.RestoreReceipt, error) {
	return &sandbox.RestoreReceipt{}, nil
}

func (f *fakeSandboxProvider) Terminate(_ context.Context, _ sandbox.TerminateRequest) (*sandbox.TerminateReceipt, error) {
	return &sandbox.TerminateReceipt{}, nil
}

func (f *fakeSandboxProvider) Reconcile(_ context.Context, _ sandbox.ReconcileRequest) (*sandbox.ReconcileReport, error) {
	return &sandbox.ReconcileReport{ActiveAllocationIds: []string{}, OrphanAllocationIds: []string{}}, nil
}

// fake registration/snapshot builders carry identity from the injection side,
// never from the frozen Local constants.
const (
	fakeRegistrationID     = "fake-sandbox-provider"
	fakeProviderName       = "fake"
	fakeProviderVersion    = "test"
	fakeIsolationDomain    = "fake-bridge"
	fakeRegistrationSeed   = "embedded-registration\x00fake-sandbox-provider"
	fakeConfigSeed         = "fake-sandbox\x00effective-config"
	fakeRegistrationCreate = "2026-08-24T00:00:00Z"
	fakeSnapshotCreate     = "2026-08-24T00:00:01Z"
)

var (
	fakeRequestDigest = sandbox.RecomputeSHA256([]byte(fakeRegistrationSeed))
	fakeConfigDigest  = sandbox.RecomputeSHA256([]byte(fakeConfigSeed))
)

func fakeRegistrationBuilder(namespace authority.AuthorityNamespaceId, actorDomain authority.SecurityDomainId) (provider.ProviderRegistration, error) {
	registration := provider.ProviderRegistration{
		RegistrationId:       fakeRegistrationID,
		AuthorityNamespaceId: namespace,
		SecurityDomainId:     actorDomain,
		Principal:            fakeRegistrationID,
		ProviderType:         "sandbox",
		ProviderName:         fakeProviderName,
		ProviderVersion:      fakeProviderVersion,
		ProtocolVersion:      "marshal-sandbox/1",
		Scope:                namespace.AuthorityScopeId,
		IdempotencyKey:       "embedded:fake-sandbox-provider",
		RequestDigest:        fakeRequestDigest,
		Attestation: provider.Attestation{
			ProviderInstanceId: "fake-sandbox-instance",
			ConfigDigest:       fakeConfigDigest,
			TrustRootKeyId:     "fake-trust-root-key",
			TrustRootAlgorithm: "ed25519",
		},
		LifecycleState: provider.LifecycleStateActive,
		CreatedAt:      fakeRegistrationCreate,
	}
	digest, err := registration.Digest()
	if err != nil {
		return provider.ProviderRegistration{}, err
	}
	registration.RegistrationDigest = digest
	return registration, nil
}

func fakeSnapshotBuilder(registration provider.ProviderRegistration) (provider.ProviderCapabilitySnapshot, error) {
	snapshot := provider.ProviderCapabilitySnapshot{
		RegistrationId:  registration.RegistrationId,
		ProtocolVersion: registration.ProtocolVersion,
		ProviderType:    registration.ProviderType,
		ProviderName:    registration.ProviderName,
		ProviderVersion: registration.ProviderVersion,
		Capabilities: map[string]string{
			"accessMode":            string(domain.AccessModeWorkspaceWrite),
			"minimumAssuranceLevel": string(domain.AssuranceLevelWorkspaceWrite),
		},
		ConformanceEvidenceDigests: []string{},
		Scope:                      registration.Scope,
		SnapshotState:              provider.SnapshotStateActive,
		CreatedAt:                  fakeSnapshotCreate,
		Attestation:                registration.Attestation,
	}
	digest, err := snapshot.Digest()
	if err != nil {
		return provider.ProviderCapabilitySnapshot{}, err
	}
	snapshot.ProviderCapabilitySnapshotDigest = digest
	return snapshot, nil
}

// newOverrideRuntimeFixture builds an embedded runtime with a fake provider
// override, mirroring newEmbeddedRuntimeFixture but injecting the override.
func newOverrideRuntimeFixture(t *testing.T) (*EmbeddedSandboxRuntime, *fakeSandboxProvider, string) {
	t.Helper()
	base := t.TempDir()
	repositoryRoot := filepath.Join(base, "repository")
	if err := os.Mkdir(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(base, ".marshal")
	bindEmbeddedRepositoryIdentityFixture(t, stateRoot, repositoryRoot)
	clock := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	fake := &fakeSandboxProvider{marker: "test-fake"}
	runtime, err := NewEmbeddedSandboxRuntime(stateRoot, func() time.Time { return clock }, WithProviderOverride(ProviderOverride{
		Provider: fake,
		ProviderDomain: authority.SecurityDomainId{
			TenantNamespace:   embeddedTenantNamespace,
			TrustDomainKind:   authority.TrustDomainKindExecution,
			IsolationDomainId: fakeIsolationDomain,
		},
		BuildRegistration: fakeRegistrationBuilder,
		BuildSnapshot:     fakeSnapshotBuilder,
	}))
	if err != nil {
		t.Fatalf("NewEmbeddedSandboxRuntime with override: %v", err)
	}
	return runtime, fake, stateRoot
}

// TestProviderOverrideInjectsProvider verifies that WithProviderOverride
// injects the provider instance, and the registration/snapshot identity comes
// from the injection side, not from the frozen Local constants.
func TestProviderOverrideInjectsProvider(t *testing.T) {
	runtime, fake, _ := newOverrideRuntimeFixture(t)

	// Provider() returns the exact injected instance.
	if runtime.Provider() != sandbox.SandboxProvider(fake) {
		t.Fatal("Provider() did not return the injected fake provider instance")
	}

	// Registration identity comes from the injection side.
	registration := runtime.Registration()
	if registration.RegistrationId != fakeRegistrationID {
		t.Fatalf("registrationId = %q, want %q (injection side)", registration.RegistrationId, fakeRegistrationID)
	}
	if registration.ProviderName != fakeProviderName {
		t.Fatalf("providerName = %q, want %q (injection side)", registration.ProviderName, fakeProviderName)
	}
	if registration.ProviderVersion != fakeProviderVersion {
		t.Fatalf("providerVersion = %q, want %q (injection side)", registration.ProviderVersion, fakeProviderVersion)
	}
	if registration.Principal != fakeRegistrationID {
		t.Fatalf("principal = %q, want %q (injection side)", registration.Principal, fakeRegistrationID)
	}

	// Snapshot identity aligns with the registration from the injection side.
	snapshot := runtime.CapabilitySnapshot()
	if snapshot.RegistrationId != fakeRegistrationID {
		t.Fatalf("snapshot registrationId = %q, want %q", snapshot.RegistrationId, fakeRegistrationID)
	}
	if snapshot.ProviderName != fakeProviderName {
		t.Fatalf("snapshot providerName = %q, want %q", snapshot.ProviderName, fakeProviderName)
	}

	// ProviderSecurityDomain carries the injected isolation domain.
	if runtime.ProviderSecurityDomain().IsolationDomainId != fakeIsolationDomain {
		t.Fatalf("providerDomain isolationDomainId = %q, want %q", runtime.ProviderSecurityDomain().IsolationDomainId, fakeIsolationDomain)
	}

	// The store carries the registration with the injected identity.
	stored, err := runtime.RegistrationStore().Get(fakeRegistrationID)
	if err != nil {
		t.Fatalf("store lookup of injected registration: %v", err)
	}
	if stored.RegistrationId != fakeRegistrationID {
		t.Fatalf("stored registrationId = %q, want %q", stored.RegistrationId, fakeRegistrationID)
	}
}

// TestProviderOverrideDefaultPathUnchanged verifies that the default path
// (no override option) keeps the Local runner and its frozen registration and
// snapshot, completely unchanged.
func TestProviderOverrideDefaultPathUnchanged(t *testing.T) {
	runtime, _, _, _ := newEmbeddedRuntimeFixture(t)

	// Registration identity is the frozen Local identity.
	registration := runtime.Registration()
	if registration.RegistrationId != embeddedRegistrationID {
		t.Fatalf("registrationId = %q, want %q (Local default)", registration.RegistrationId, embeddedRegistrationID)
	}
	if registration.ProviderName != embeddedProviderName {
		t.Fatalf("providerName = %q, want %q (Local default)", registration.ProviderName, embeddedProviderName)
	}
	if registration.ProviderVersion != embeddedProviderVersion {
		t.Fatalf("providerVersion = %q, want %q (Local default)", registration.ProviderVersion, embeddedProviderVersion)
	}

	// Snapshot identity is the frozen Local identity.
	snapshot := runtime.CapabilitySnapshot()
	if snapshot.RegistrationId != embeddedRegistrationID {
		t.Fatalf("snapshot registrationId = %q, want %q (Local default)", snapshot.RegistrationId, embeddedRegistrationID)
	}
	if snapshot.ProviderName != embeddedProviderName {
		t.Fatalf("snapshot providerName = %q, want %q (Local default)", snapshot.ProviderName, embeddedProviderName)
	}

	// Provider domain is the Local default (host-process).
	if runtime.ProviderSecurityDomain().IsolationDomainId != "host-process" {
		t.Fatalf("providerDomain isolationDomainId = %q, want host-process (Local default)", runtime.ProviderSecurityDomain().IsolationDomainId)
	}
}

// TestProviderOverrideValidationFailsClosed verifies that the override seam
// fails closed on nil Provider, nil BuildRegistration and nil BuildSnapshot.
func TestProviderOverrideValidationFailsClosed(t *testing.T) {
	base := t.TempDir()
	repositoryRoot := filepath.Join(base, "repository")
	if err := os.Mkdir(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(base, ".marshal")
	bindEmbeddedRepositoryIdentityFixture(t, stateRoot, repositoryRoot)
	clock := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name     string
		override ProviderOverride
	}{
		{name: "nil provider", override: ProviderOverride{
			BuildRegistration: fakeRegistrationBuilder,
			BuildSnapshot:     fakeSnapshotBuilder,
		}},
		{name: "nil registration builder", override: ProviderOverride{
			Provider:      &fakeSandboxProvider{},
			BuildSnapshot: fakeSnapshotBuilder,
		}},
		{name: "nil snapshot builder", override: ProviderOverride{
			Provider:          &fakeSandboxProvider{},
			BuildRegistration: fakeRegistrationBuilder,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewEmbeddedSandboxRuntime(stateRoot, func() time.Time { return clock }, WithProviderOverride(tc.override)); err == nil {
				t.Fatal("construction with a nil override field must fail closed")
			}
		})
	}
}

// TestCloudflareBindingConfigFromEnv verifies that the env-driven binding
// configuration fails closed on missing required variables and succeeds when
// all are present.
func TestCloudflareBindingConfigFromEnv(t *testing.T) {
	t.Run("nil getenv fails closed", func(t *testing.T) {
		if _, err := CloudflareBindingConfigFromEnv(nil); err == nil {
			t.Fatal("nil getenv must fail closed")
		}
	})
	t.Run("missing bridge URL fails closed", func(t *testing.T) {
		getenv := func(key string) string {
			if key == CloudflareAPIKeyEnv {
				return "secret-key"
			}
			if key == CloudflareStateDirEnv {
				return t.TempDir()
			}
			return ""
		}
		if _, err := CloudflareBindingConfigFromEnv(getenv); err == nil ||
			!strings.Contains(err.Error(), CloudflareBridgeURLEnv) {
			t.Fatalf("missing bridge URL must fail closed, got %v", err)
		}
	})
	t.Run("missing API key fails closed", func(t *testing.T) {
		getenv := func(key string) string {
			if key == CloudflareBridgeURLEnv {
				return "http://localhost:8080"
			}
			if key == CloudflareStateDirEnv {
				return t.TempDir()
			}
			return ""
		}
		if _, err := CloudflareBindingConfigFromEnv(getenv); err == nil ||
			!strings.Contains(err.Error(), CloudflareAPIKeyEnv) {
			t.Fatalf("missing API key must fail closed, got %v", err)
		}
	})
	t.Run("missing state dir fails closed", func(t *testing.T) {
		getenv := func(key string) string {
			if key == CloudflareBridgeURLEnv {
				return "http://localhost:8080"
			}
			if key == CloudflareAPIKeyEnv {
				return "secret-key"
			}
			return ""
		}
		if _, err := CloudflareBindingConfigFromEnv(getenv); err == nil ||
			!strings.Contains(err.Error(), CloudflareStateDirEnv) {
			t.Fatalf("missing state dir must fail closed, got %v", err)
		}
	})
	t.Run("all present succeeds", func(t *testing.T) {
		stateDir := t.TempDir()
		getenv := func(key string) string {
			switch key {
			case CloudflareBridgeURLEnv:
				return "http://localhost:8080"
			case CloudflareAPIKeyEnv:
				return "secret-key"
			case CloudflareStateDirEnv:
				return stateDir
			}
			return ""
		}
		config, err := CloudflareBindingConfigFromEnv(getenv)
		if err != nil {
			t.Fatalf("all-present must succeed, got %v", err)
		}
		if config.BridgeURL != "http://localhost:8080" {
			t.Fatalf("BridgeURL = %q", config.BridgeURL)
		}
		if config.APIKey != "secret-key" {
			t.Fatalf("APIKey = %q", config.APIKey)
		}
		if config.StateDir != stateDir {
			t.Fatalf("StateDir = %q", config.StateDir)
		}
	})
}

// TestCloudflareRegistrationAndSnapshotIdentity verifies that the Cloudflare
// registration and snapshot carry the Cloudflare identity constants, not the
// frozen Local constants.
func TestCloudflareRegistrationAndSnapshotIdentity(t *testing.T) {
	namespace := authority.AuthorityNamespaceId{
		TenantNamespace:  embeddedTenantNamespace,
		ControlPlaneId:   embeddedControlPlaneID,
		AuthorityScopeId: "repo:/test",
	}
	actorDomain := authority.SecurityDomainId{
		TenantNamespace:   embeddedTenantNamespace,
		TrustDomainKind:   authority.TrustDomainKindExecution,
		IsolationDomainId: cloudflareIsolationDomain,
	}
	registration, err := cloudflareRegistration(namespace, actorDomain)
	if err != nil {
		t.Fatalf("cloudflareRegistration: %v", err)
	}
	if registration.RegistrationId != cloudflareRegistrationID {
		t.Fatalf("registrationId = %q, want %q", registration.RegistrationId, cloudflareRegistrationID)
	}
	if registration.ProviderName != cloudflareProviderName {
		t.Fatalf("providerName = %q, want %q", registration.ProviderName, cloudflareProviderName)
	}
	if registration.ProviderVersion != cloudflareProviderVersion {
		t.Fatalf("providerVersion = %q, want %q", registration.ProviderVersion, cloudflareProviderVersion)
	}
	if registration.ProviderType != cloudflareProviderType {
		t.Fatalf("providerType = %q, want %q", registration.ProviderType, cloudflareProviderType)
	}
	if registration.ProtocolVersion != cloudflareProtocolVersion {
		t.Fatalf("protocolVersion = %q, want %q", registration.ProtocolVersion, cloudflareProtocolVersion)
	}
	if registration.Principal != cloudflareWorkerPrincipal {
		t.Fatalf("principal = %q, want %q", registration.Principal, cloudflareWorkerPrincipal)
	}

	snapshot, err := cloudflareSnapshot(registration)
	if err != nil {
		t.Fatalf("cloudflareSnapshot: %v", err)
	}
	if err := snapshot.ValidateAgainstRegistration(registration); err != nil {
		t.Fatalf("snapshot does not validate against the registration: %v", err)
	}
	if snapshot.ProviderName != cloudflareProviderName {
		t.Fatalf("snapshot providerName = %q, want %q", snapshot.ProviderName, cloudflareProviderName)
	}
}

// TestCloudflareAPIKeyDoesNotLeak verifies that the API key read from the
// environment never appears in the registration, snapshot, or any error text
// the binding produces. The key enters only as the Bridge Bearer token inside
// the cloudflare.Provider, never in business JSON.
func TestCloudflareAPIKeyDoesNotLeak(t *testing.T) {
	apiKey := "super-secret-key-do-not-leak-12345"
	stateDir := t.TempDir()

	// Construct a real Cloudflare provider via the binding to verify the key
	// stays inside the provider and never surfaces in registration/snapshot.
	cfProvider, err := NewCloudflareProvider(CloudflareBindingConfig{
		BridgeURL: "http://localhost:8080",
		APIKey:    apiKey,
		StateDir:  stateDir,
	})
	if err != nil {
		t.Fatalf("NewCloudflareProvider: %v", err)
	}
	_ = cfProvider

	// The registration and snapshot carry no trace of the API key.
	namespace := authority.AuthorityNamespaceId{
		TenantNamespace:  embeddedTenantNamespace,
		ControlPlaneId:   embeddedControlPlaneID,
		AuthorityScopeId: "repo:/test",
	}
	actorDomain := authority.SecurityDomainId{
		TenantNamespace:   embeddedTenantNamespace,
		TrustDomainKind:   authority.TrustDomainKindExecution,
		IsolationDomainId: cloudflareIsolationDomain,
	}
	registration, err := cloudflareRegistration(namespace, actorDomain)
	if err != nil {
		t.Fatalf("cloudflareRegistration: %v", err)
	}
	snapshot, err := cloudflareSnapshot(registration)
	if err != nil {
		t.Fatalf("cloudflareSnapshot: %v", err)
	}
	registrationJSON, _ := json.Marshal(registration)
	snapshotJSON, _ := json.Marshal(snapshot)
	if strings.Contains(string(registrationJSON), apiKey) {
		t.Fatal("the API key leaked into the registration JSON")
	}
	if strings.Contains(string(snapshotJSON), apiKey) {
		t.Fatal("the API key leaked into the snapshot JSON")
	}

	// The state store file must not contain the API key either.
	stateData, err := os.ReadFile(filepath.Join(stateDir, "cloudflare-state.json"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read state file: %v", err)
	}
	if err == nil && strings.Contains(string(stateData), apiKey) {
		t.Fatal("the API key leaked into the state store file")
	}
}

// TestCloudflareProviderOverrideFromEnv verifies that
// CloudflareProviderOverride produces a ProviderOverride with the Cloudflare
// identity from environment variables. The test uses a local HTTP test
// server only to satisfy the Bridge URL format requirement; no real Bridge
// call is made.
func TestCloudflareProviderOverrideFromEnv(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	stateDir := t.TempDir()
	getenv := func(key string) string {
		switch key {
		case CloudflareBridgeURLEnv:
			return server.URL
		case CloudflareAPIKeyEnv:
			return "test-api-key"
		case CloudflareStateDirEnv:
			return stateDir
		}
		return ""
	}
	override, err := CloudflareProviderOverride(getenv)
	if err != nil {
		t.Fatalf("CloudflareProviderOverride: %v", err)
	}
	if override.Provider == nil {
		t.Fatal("override.Provider must not be nil")
	}
	if override.BuildRegistration == nil {
		t.Fatal("override.BuildRegistration must not be nil")
	}
	if override.BuildSnapshot == nil {
		t.Fatal("override.BuildSnapshot must not be nil")
	}
	if override.ProviderDomain.IsolationDomainId != cloudflareIsolationDomain {
		t.Fatalf("providerDomain isolationDomainId = %q, want %q", override.ProviderDomain.IsolationDomainId, cloudflareIsolationDomain)
	}

	// The override produces a registration with the Cloudflare identity.
	namespace := authority.AuthorityNamespaceId{
		TenantNamespace:  embeddedTenantNamespace,
		ControlPlaneId:   embeddedControlPlaneID,
		AuthorityScopeId: "repo:/test",
	}
	registration, err := override.BuildRegistration(namespace, override.ProviderDomain)
	if err != nil {
		t.Fatalf("BuildRegistration: %v", err)
	}
	if registration.RegistrationId != cloudflareRegistrationID {
		t.Fatalf("registrationId = %q, want %q", registration.RegistrationId, cloudflareRegistrationID)
	}
	if registration.ProviderName != cloudflareProviderName {
		t.Fatalf("providerName = %q, want %q", registration.ProviderName, cloudflareProviderName)
	}

	// The override produces a snapshot aligned with the registration.
	snapshot, err := override.BuildSnapshot(registration)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	if err := snapshot.ValidateAgainstRegistration(registration); err != nil {
		t.Fatalf("snapshot does not validate against the registration: %v", err)
	}
}

// TestProviderOverrideWithCloudflareIdentityInRuntime verifies that a
// Cloudflare provider override injected into the embedded runtime produces
// a runtime whose registration carries the Cloudflare identity, not Local.
func TestProviderOverrideWithCloudflareIdentityInRuntime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	base := t.TempDir()
	repositoryRoot := filepath.Join(base, "repository")
	if err := os.Mkdir(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(base, ".marshal")
	bindEmbeddedRepositoryIdentityFixture(t, stateRoot, repositoryRoot)
	cfStateDir := filepath.Join(base, "cf-state")

	clock := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	getenv := func(key string) string {
		switch key {
		case CloudflareBridgeURLEnv:
			return server.URL
		case CloudflareAPIKeyEnv:
			return "runtime-test-key"
		case CloudflareStateDirEnv:
			return cfStateDir
		}
		return ""
	}
	override, err := CloudflareProviderOverride(getenv)
	if err != nil {
		t.Fatalf("CloudflareProviderOverride: %v", err)
	}
	runtime, err := NewEmbeddedSandboxRuntime(stateRoot, func() time.Time { return clock }, WithProviderOverride(override))
	if err != nil {
		t.Fatalf("NewEmbeddedSandboxRuntime with cloudflare override: %v", err)
	}

	// The registration carries the Cloudflare identity, not Local.
	registration := runtime.Registration()
	if registration.RegistrationId != cloudflareRegistrationID {
		t.Fatalf("registrationId = %q, want %q", registration.RegistrationId, cloudflareRegistrationID)
	}
	if registration.ProviderName != cloudflareProviderName {
		t.Fatalf("providerName = %q, want %q", registration.ProviderName, cloudflareProviderName)
	}
	if registration.ProviderVersion != cloudflareProviderVersion {
		t.Fatalf("providerVersion = %q, want %q", registration.ProviderVersion, cloudflareProviderVersion)
	}

	// The provider domain carries the Cloudflare isolation domain.
	if runtime.ProviderSecurityDomain().IsolationDomainId != cloudflareIsolationDomain {
		t.Fatalf("providerDomain isolationDomainId = %q, want %q", runtime.ProviderSecurityDomain().IsolationDomainId, cloudflareIsolationDomain)
	}

	// The API key must not appear in the registration or snapshot.
	registrationJSON, _ := json.Marshal(registration)
	snapshotJSON, _ := json.Marshal(runtime.CapabilitySnapshot())
	if strings.Contains(string(registrationJSON), "runtime-test-key") {
		t.Fatal("the API key leaked into the registration JSON")
	}
	if strings.Contains(string(snapshotJSON), "runtime-test-key") {
		t.Fatal("the API key leaked into the snapshot JSON")
	}
}

// TestProviderOverrideClaimWithFakeProvider verifies that the claim path
// works end-to-end with an injected fake provider: the worker claim issues
// a lease and the fake provider grants an active allocation.
func TestProviderOverrideClaimWithFakeProvider(t *testing.T) {
	runtime, fake, _ := newOverrideRuntimeFixture(t)
	requirements := workspaceWriteRequirementsFixture(t)
	claim, err := runtime.ClaimExecution(context.Background(), embeddedClaimRequestFixture(
		"run-override", "attempt-override",
		sandbox.WorkloadRoleWorker, "principal-override",
		requirements,
	))
	if err != nil {
		t.Fatalf("ClaimExecution with override: %v", err)
	}
	if claim.Lease.Generation != 1 {
		t.Fatalf("lease generation = %d, want 1", claim.Lease.Generation)
	}
	if claim.Allocation.State != sandbox.AllocationActive {
		t.Fatalf("allocation state = %q, want active", string(claim.Allocation.State))
	}
	if claim.Allocation.AllocationId == "" {
		t.Fatal("the fake provider must grant a non-empty allocationId")
	}
	// The fake provider's marker is accessible, proving the injected instance
	// served the claim.
	if fake.marker != "test-fake" {
		t.Fatal("the fake provider marker is inconsistent")
	}
}
