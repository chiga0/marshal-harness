package agentregistry

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// ── ProviderType (closed enum) ────────────────────────────────────────────────

// ProviderType is the closed set of provider types. Unknown values fail closed.
// The sole legal value in R3-A is ProviderTypeAgent; additional values are
// reserved for future R3-D binding (e.g. sandbox evidence boundaries).
type ProviderType string

const (
	ProviderTypeAgent ProviderType = "agent"
)

func (p ProviderType) validate() error {
	switch p {
	case ProviderTypeAgent:
		return nil
	default:
		return fmt.Errorf("agentregistry: unknown ProviderType %q", string(p))
	}
}

// ── LifecycleState (closed enum) ──────────────────────────────────────────────

// LifecycleState is the closed set of registration lifecycle states.
type LifecycleState string

const (
	LifecycleStateActive    LifecycleState = "active"
	LifecycleStateSuspended LifecycleState = "suspended"
	LifecycleStateRevoked   LifecycleState = "revoked"
	LifecycleStateReplaced  LifecycleState = "replaced"
	LifecycleStateExpired   LifecycleState = "expired"
)

func (s LifecycleState) validate() error {
	switch s {
	case LifecycleStateActive, LifecycleStateSuspended,
		LifecycleStateRevoked, LifecycleStateReplaced, LifecycleStateExpired:
		return nil
	default:
		return fmt.Errorf("agentregistry: unknown LifecycleState %q", string(s))
	}
}

// ── AgentRegistration ─────────────────────────────────────────────────────────

// AgentRegistration is the durable, closed identity record for an AgentProvider.
// All fields are required; Validate fails closed on any missing or invalid value.
type AgentRegistration struct {
	RegistrationID       string // "registration:<hex>" or deterministic derivative
	AuthorityNamespaceID string
	SecurityDomainID     string
	Principal            string
	ProviderType         ProviderType // closed enum; only "agent" is valid in R3-A
	ProviderName         string
	ProviderVersion      string
	ProtocolVersion      string
	Scope                string
	IdempotencyKey       string
	RequestDigest        string         // sha256:<64-hex>
	LifecycleState       LifecycleState // closed enum
	CreatedAt            time.Time      // RFC3339 UTC
	UpdatedAt            time.Time      // RFC3339 UTC
}

// Validate fails closed on any missing or structurally invalid field.
func (r AgentRegistration) Validate() error {
	if strings.TrimSpace(r.RegistrationID) == "" {
		return fmt.Errorf("agentregistry: RegistrationID must not be empty")
	}
	if strings.TrimSpace(r.AuthorityNamespaceID) == "" {
		return fmt.Errorf("agentregistry: AuthorityNamespaceID must not be empty")
	}
	if strings.TrimSpace(r.SecurityDomainID) == "" {
		return fmt.Errorf("agentregistry: SecurityDomainID must not be empty")
	}
	if strings.TrimSpace(r.Principal) == "" {
		return fmt.Errorf("agentregistry: Principal must not be empty")
	}
	if err := r.ProviderType.validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.ProviderName) == "" {
		return fmt.Errorf("agentregistry: ProviderName must not be empty")
	}
	if strings.TrimSpace(r.ProviderVersion) == "" {
		return fmt.Errorf("agentregistry: ProviderVersion must not be empty")
	}
	if strings.TrimSpace(r.ProtocolVersion) == "" {
		return fmt.Errorf("agentregistry: ProtocolVersion must not be empty")
	}
	if strings.TrimSpace(r.Scope) == "" {
		return fmt.Errorf("agentregistry: Scope must not be empty")
	}
	if strings.TrimSpace(r.IdempotencyKey) == "" {
		return fmt.Errorf("agentregistry: IdempotencyKey must not be empty")
	}
	if err := requireDigest("RequestDigest", r.RequestDigest); err != nil {
		return fmt.Errorf("agentregistry: %w", err)
	}
	if err := r.LifecycleState.validate(); err != nil {
		return err
	}
	if r.CreatedAt.IsZero() {
		return fmt.Errorf("agentregistry: CreatedAt must not be zero")
	}
	if r.UpdatedAt.IsZero() {
		return fmt.Errorf("agentregistry: UpdatedAt must not be zero")
	}
	return nil
}

// registrationJSON is the canonical serialisation shape for Digest().
type registrationJSON struct {
	RegistrationID       string `json:"registrationId"`
	AuthorityNamespaceID string `json:"authorityNamespaceId"`
	SecurityDomainID     string `json:"securityDomainId"`
	Principal            string `json:"principal"`
	ProviderType         string `json:"providerType"`
	ProviderName         string `json:"providerName"`
	ProviderVersion      string `json:"providerVersion"`
	ProtocolVersion      string `json:"protocolVersion"`
	Scope                string `json:"scope"`
	IdempotencyKey       string `json:"idempotencyKey"`
	RequestDigest        string `json:"requestDigest"`
	LifecycleState       string `json:"lifecycleState"`
	CreatedAtUnixSec     int64  `json:"createdAtUnixSec"`
	UpdatedAtUnixSec     int64  `json:"updatedAtUnixSec"`
}

// Digest returns the sha256 digest of the canonical JSON form of the registration.
func (r AgentRegistration) Digest() (string, error) {
	raw, err := json.Marshal(registrationJSON{
		RegistrationID:       r.RegistrationID,
		AuthorityNamespaceID: r.AuthorityNamespaceID,
		SecurityDomainID:     r.SecurityDomainID,
		Principal:            r.Principal,
		ProviderType:         string(r.ProviderType),
		ProviderName:         r.ProviderName,
		ProviderVersion:      r.ProviderVersion,
		ProtocolVersion:      r.ProtocolVersion,
		Scope:                r.Scope,
		IdempotencyKey:       r.IdempotencyKey,
		RequestDigest:        r.RequestDigest,
		LifecycleState:       string(r.LifecycleState),
		CreatedAtUnixSec:     r.CreatedAt.Unix(),
		UpdatedAtUnixSec:     r.UpdatedAt.Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("agentregistry: registration serialisation failed: %w", err)
	}
	return canonical.DigestJSON(raw)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func requireDigest(field, value string) error {
	const prefix = "sha256:"
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	if !strings.HasPrefix(value, prefix) {
		return fmt.Errorf("%s must carry the sha256: prefix", field)
	}
	hex := strings.TrimPrefix(value, prefix)
	if len(hex) != 64 {
		return fmt.Errorf("%s must be a 64-character sha256 hex digest", field)
	}
	for _, c := range hex {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("%s must be lowercase hex", field)
		}
	}
	return nil
}
