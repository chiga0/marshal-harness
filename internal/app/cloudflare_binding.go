package app

// cloudflare_binding.go assembles the Cloudflare Bridge sandbox provider
// binding of the embedded runtime (M10 wire). The Bridge Bearer token (API
// key) is a transport credential only: it enters the cloudflare.Provider as
// BridgeToken and the cloudflare.Credential redacts it from every
// String/Format path. The token never appears in the registration, snapshot,
// diagnostic text or any persistent record (ADR 0018 §12).

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/provider"
	"github.com/chiga0/marshal-harness/internal/provider/cloudflare"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// Environment variable names of the Cloudflare Bridge binding.
const (
	CloudflareProviderEnv  = "MARSHAL_SANDBOX_PROVIDER"
	CloudflareBridgeURLEnv = "MARSHAL_CF_BRIDGE_URL"
	CloudflareAPIKeyEnv    = "MARSHAL_CF_API_KEY"
	CloudflareStateDirEnv  = "MARSHAL_CF_STATE_DIR"
)

// Cloudflare binding identity constants. The registration CreatedAt is a
// fixed constant — never the construction clock — so an identical durable
// ledger replay merges idempotently on any day instead of conflicting on a
// divergent registrationDigest.
const (
	cloudflareRegistrationID        = "cloudflare-sandbox-provider"
	cloudflareIdempotencyKey        = "embedded" + ":cloudflare-sandbox-provider"
	cloudflareProviderType          = "sandbox"
	cloudflareProviderName          = "cloudflare"
	cloudflareProviderVersion       = "m10-wire"
	cloudflareProtocolVersion       = "marshal-sandbox/1"
	cloudflareWorkerPrincipal       = "cloudflare-sandbox-provider"
	cloudflareRegistrationCreatedAt = "2026-08-24T00:00:00Z"
	cloudflareSnapshotCreatedAt     = "2026-08-24T00:00:01Z"
	cloudflareIsolationDomain       = "cloudflare-bridge"
)

// Deterministic derivation seeds of the cloudflare registration records; the
// two-part concatenation keeps every Digest-family fixture value
// gitleaks-safe.
var (
	cloudflareRegistrationRequestDigest = sandbox.RecomputeSHA256([]byte("embedded-registration" + "\x00" + "cloudflare-sandbox-provider"))
	cloudflareConfigDigest              = sandbox.RecomputeSHA256([]byte("cloudflare-sandbox" + "\x00" + "effective-config"))
)

// CloudflareBindingConfig carries the env-derived Cloudflare Bridge binding
// configuration. The API key is a transport credential only: it enters the
// cloudflare.Provider as the Bridge Bearer token and never appears in the
// registration, snapshot, diagnostic text or any persistent record.
type CloudflareBindingConfig struct {
	BridgeURL  string
	APIKey     string
	StateDir   string
	HTTPClient *http.Client
}

// CloudflareBindingConfigFromEnv reads the Cloudflare Bridge binding
// configuration from environment variables. A nil getenv fails closed. Every
// required variable must carry a non-empty trimmed value.
func CloudflareBindingConfigFromEnv(getenv func(string) string) (CloudflareBindingConfig, error) {
	if getenv == nil {
		return CloudflareBindingConfig{}, errors.New("app: cloudflare binding: getenv must not be nil")
	}
	bridgeURL := strings.TrimSpace(getenv(CloudflareBridgeURLEnv))
	if bridgeURL == "" {
		return CloudflareBindingConfig{}, fmt.Errorf("app: cloudflare binding: %s must be a non-empty string", CloudflareBridgeURLEnv)
	}
	apiKey := strings.TrimSpace(getenv(CloudflareAPIKeyEnv))
	if apiKey == "" {
		return CloudflareBindingConfig{}, fmt.Errorf("app: cloudflare binding: %s must be a non-empty string", CloudflareAPIKeyEnv)
	}
	stateDir := strings.TrimSpace(getenv(CloudflareStateDirEnv))
	if stateDir == "" {
		return CloudflareBindingConfig{}, fmt.Errorf("app: cloudflare binding: %s must be a non-empty string", CloudflareStateDirEnv)
	}
	return CloudflareBindingConfig{
		BridgeURL: bridgeURL,
		APIKey:    apiKey,
		StateDir:  stateDir,
	}, nil
}

// NewCloudflareProvider constructs the Cloudflare Bridge sandbox provider
// with a file-backed state store from the binding configuration. The state
// directory is created if it does not exist. The API key enters only as the
// Bridge Bearer token (cloudflare.Credential redacts it from every
// String/Format path) and never surfaces in registration, snapshot or
// diagnostic text.
func NewCloudflareProvider(config CloudflareBindingConfig) (*cloudflare.Provider, error) {
	if err := os.MkdirAll(config.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("app: cloudflare binding: state directory: %w", err)
	}
	stateStore, err := cloudflare.NewFileStateStore(filepath.Join(config.StateDir, "cloudflare-state.json"))
	if err != nil {
		return nil, fmt.Errorf("app: cloudflare binding: state store: %w", err)
	}
	cloudflareProvider, err := cloudflare.NewProvider(cloudflare.ProviderConfig{
		BridgeBaseURL: config.BridgeURL,
		BridgeToken:   config.APIKey,
		StateStore:    stateStore,
		HTTPClient:    config.HTTPClient,
	})
	if err != nil {
		return nil, fmt.Errorf("app: cloudflare binding: provider: %w", err)
	}
	return cloudflareProvider, nil
}

// cloudflareRegistration builds the Cloudflare provider registration with the
// fixed frozen createdAt, so identical replays merge idempotently. The
// providerId, type, name, version and attestation come from the Cloudflare
// binding constants, never from the frozen Local constants.
func cloudflareRegistration(namespace authority.AuthorityNamespaceId, actorDomain authority.SecurityDomainId) (provider.ProviderRegistration, error) {
	registration := provider.ProviderRegistration{
		RegistrationId:       cloudflareRegistrationID,
		AuthorityNamespaceId: namespace,
		SecurityDomainId:     actorDomain,
		Principal:            cloudflareWorkerPrincipal,
		ProviderType:         cloudflareProviderType,
		ProviderName:         cloudflareProviderName,
		ProviderVersion:      cloudflareProviderVersion,
		ProtocolVersion:      cloudflareProtocolVersion,
		Scope:                namespace.AuthorityScopeId,
		IdempotencyKey:       cloudflareIdempotencyKey,
		RequestDigest:        cloudflareRegistrationRequestDigest,
		Attestation: provider.Attestation{
			ProviderInstanceId: "cloudflare-sandbox-instance",
			ConfigDigest:       cloudflareConfigDigest,
			TrustRootKeyId:     "cloudflare-trust-root-key",
			TrustRootAlgorithm: "ed25519",
		},
		LifecycleState: provider.LifecycleStateActive,
		CreatedAt:      cloudflareRegistrationCreatedAt,
	}
	digest, err := registration.Digest()
	if err != nil {
		return provider.ProviderRegistration{}, err
	}
	registration.RegistrationDigest = digest
	return registration, nil
}

// cloudflareSnapshot captures the capability snapshot of the Cloudflare
// provider: the capabilities declare the workspace-write accessMode and the
// workspace-write assurance ceiling, the attestation aligns with the
// registration and the conformanceEvidenceDigests set is empty (the binding
// holds no suite-issued evidence).
func cloudflareSnapshot(registration provider.ProviderRegistration) (provider.ProviderCapabilitySnapshot, error) {
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
		CreatedAt:                  cloudflareSnapshotCreatedAt,
		Attestation:                registration.Attestation,
	}
	digest, err := snapshot.Digest()
	if err != nil {
		return provider.ProviderCapabilitySnapshot{}, err
	}
	snapshot.ProviderCapabilitySnapshotDigest = digest
	return snapshot, nil
}

// CloudflareProviderOverride builds a ProviderOverride from environment
// variables, suitable for injection into NewEmbeddedSandboxRuntime via
// WithProviderOverride. The API key is read only from the environment and
// enters only the cloudflare.Provider as the Bridge Bearer token; it never
// appears in the registration, snapshot, diagnostic text or any persistent
// record.
func CloudflareProviderOverride(getenv func(string) string) (ProviderOverride, error) {
	config, err := CloudflareBindingConfigFromEnv(getenv)
	if err != nil {
		return ProviderOverride{}, err
	}
	cloudflareProvider, err := NewCloudflareProvider(config)
	if err != nil {
		return ProviderOverride{}, err
	}
	return ProviderOverride{
		Provider: cloudflareProvider,
		ProviderDomain: authority.SecurityDomainId{
			TenantNamespace:   embeddedTenantNamespace,
			TrustDomainKind:   authority.TrustDomainKindExecution,
			IsolationDomainId: cloudflareIsolationDomain,
		},
		BuildRegistration: cloudflareRegistration,
		BuildSnapshot:     cloudflareSnapshot,
	}, nil
}
